package server

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	kms "github.com/siderolabs/kms-client/api/kms"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/maruina/azath/internal/crypto"
	"github.com/maruina/azath/internal/gate"
	"github.com/maruina/azath/internal/keymanager"
	"github.com/maruina/azath/internal/observability"
)

type DeviceInfo struct {
	Name     string
	UUID     string
	Disabled bool
}

// KMSServer implements kms.KMSServiceServer. It handles Seal and Unseal RPCs
// with gate approval and config-driven device authorization.
type KMSServer struct {
	kms.UnimplementedKMSServiceServer

	km      *keymanager.Manager
	metrics *observability.Metrics
	logger  *slog.Logger

	gate gate.Gate

	// devices is the config-derived device lookup table: UUID -> DeviceInfo.
	// Built at startup from config.devices[]; serves as source of truth for
	// which devices azath may serve.
	devices map[string]DeviceInfo
}

// Option configures a KMSServer.
type Option func(*KMSServer)

// WithGate sets the gate used to approve Unseal requests.
// A nil gate (the default) denies all requests.
func WithGate(g gate.Gate) Option {
	return func(s *KMSServer) { s.gate = g }
}

// WithDevices sets the config-derived device lookup table.
// The map keys are canonical UUIDs (lowercase, hyphenated).
func WithDevices(devices map[string]DeviceInfo) Option {
	return func(s *KMSServer) { s.devices = devices }
}

// WithLogger sets the structured logger. A nil logger discards all output.
func WithLogger(l *slog.Logger) Option {
	return func(s *KMSServer) {
		if l != nil {
			s.logger = l
		}
	}
}

// New creates a KMSServer. Panics if km or m is nil.
func New(km *keymanager.Manager, m *observability.Metrics, opts ...Option) *KMSServer {
	if km == nil {
		panic("server.New: km must not be nil")
	}
	if m == nil {
		panic("server.New: m must not be nil")
	}
	s := &KMSServer{
		km:      km,
		metrics: m,
		logger:  slog.New(slog.DiscardHandler),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Close shuts down the KMS server after draining RPCs.
func (s *KMSServer) Close() {}

// Seal encrypts the data in req.Data using the master key, with the node UUID
// as additional authenticated data. The device UUID must be configured and enabled.
func (s *KMSServer) Seal(ctx context.Context, req *kms.Request) (*kms.Response, error) {
	start := time.Now()

	nodeUUID, err := uuid.Parse(req.GetNodeUuid())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid node UUID: %v", err)
	}
	nodeUUIDStr := nodeUUID.String()

	// Check device against config source of truth.
	dev, known := s.devices[nodeUUIDStr]
	if !known {
		return nil, status.Errorf(codes.PermissionDenied, "device %s not configured", nodeUUIDStr)
	}
	if dev.Disabled {
		return nil, status.Errorf(codes.PermissionDenied, "device %s is disabled", nodeUUIDStr)
	}

	sealer, err := s.km.Sealer()
	if err != nil {
		s.logger.ErrorContext(ctx, "failed to get sealer for seal", "error", err)
		return nil, status.Errorf(codes.Internal, "getting sealer: %v", err)
	}
	defer sealer.Destroy()

	sealed, err := sealer.Seal(req.GetData(), []byte(nodeUUIDStr))
	if err != nil {
		s.logger.ErrorContext(ctx, "seal failed", "node_uuid", nodeUUIDStr[:8], "error", err)
		return nil, status.Errorf(codes.Internal, "seal: %v", err)
	}

	s.metrics.SealTotal.Inc()
	s.metrics.SealDuration.Observe(time.Since(start).Seconds())

	s.logger.InfoContext(ctx, "seal", "node_uuid", nodeUUIDStr[:8])

	return &kms.Response{Data: sealed}, nil
}

// Unseal decrypts sealed data. Every non-success path returns codes.OK with
// random bytes to prevent oracle attacks — the caller cannot distinguish
// failure modes from a valid decryption failure.
func (s *KMSServer) Unseal(ctx context.Context, req *kms.Request) (*kms.Response, error) {
	start := time.Now()

	// fail increments the reason counter, observes duration, and returns random bytes.
	// All Unseal failure paths must go through fail to uphold the oracle contract.
	fail := func(reason string) (*kms.Response, error) {
		s.metrics.UnsealTotal.WithLabelValues(reason).Inc()
		s.metrics.UnsealDuration.Observe(time.Since(start).Seconds())
		s.logger.InfoContext(ctx, "unseal", "reason", reason)
		return randomResponse(req), nil
	}

	nodeUUID, err := uuid.Parse(req.GetNodeUuid())
	if err != nil {
		return fail("unknown_uuid")
	}
	nodeUUIDStr := nodeUUID.String()

	// Check device against config source of truth first.
	dev, known := s.devices[nodeUUIDStr]
	if !known {
		return fail("unknown_uuid")
	}
	if dev.Disabled {
		return fail("disabled")
	}

	// Both ErrNotLoaded and ErrDestroyed map to master_key_not_loaded — all
	// key-unavailable states are indistinguishable to callers.
	sealer, err := s.km.Sealer()
	if err != nil {
		if errors.Is(err, keymanager.ErrDestroyed) {
			s.logger.ErrorContext(ctx, "master key destroyed, unseal impossible", "node_uuid", nodeUUIDStr[:8])
		}
		return fail("master_key_not_loaded")
	}
	defer sealer.Destroy()

	// deliberate: validate the blob with AES-GCM before prompting Telegram to avoid
	// alerting operators on malformed blobs or wrong instance tags. We decrypt a
	// second time after approval so plaintext is not retained in memory during
	// the human approval wait (which can take minutes). Cost is negligible for
	// small disk keys and passphrases.
	validated, err := sealer.Unseal(req.GetData(), []byte(nodeUUIDStr))
	if err != nil {
		if errors.Is(err, crypto.ErrShortBlob) || errors.Is(err, crypto.ErrWrongInstance) {
			return fail("wrong_instance")
		}
		return fail("decrypt_error")
	}
	crypto.Zero(validated)

	if s.gate == nil {
		return fail("gate_denied")
	}
	result, gateErr := s.gate.Check(ctx, gate.Device{Name: dev.Name, UUID: nodeUUIDStr})
	if gateErr != nil {
		// Gate infrastructure failure — log the real error so operators can
		// distinguish a policy denial from an API outage. Fail closed.
		s.logger.ErrorContext(ctx, "gate check error", "node_uuid", nodeUUIDStr[:8], "error", gateErr)
		s.metrics.GateAPIErrors.WithLabelValues("telegram").Inc()
		return fail("gate_denied")
	}
	if result != gate.Approved {
		return fail("gate_denied")
	}

	plaintext, err := sealer.Unseal(req.GetData(), []byte(nodeUUIDStr))
	if err != nil {
		if errors.Is(err, crypto.ErrShortBlob) || errors.Is(err, crypto.ErrWrongInstance) {
			return fail("wrong_instance")
		}
		return fail("decrypt_error")
	}

	s.metrics.UnsealTotal.WithLabelValues("ok").Inc()
	s.metrics.UnsealDuration.Observe(time.Since(start).Seconds())
	s.logger.InfoContext(ctx, "unseal", "node_uuid", nodeUUIDStr[:8], "device", dev.Name, "reason", "ok")

	return &kms.Response{Data: plaintext}, nil
}
