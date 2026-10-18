package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	kms "github.com/siderolabs/kms-client/api/kms"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
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
// with bearer-token auth and gate approval.
// Call Close after draining RPCs to zero the seal token.
type KMSServer struct {
	kms.UnimplementedKMSServiceServer

	km        *keymanager.Manager
	metrics   *observability.Metrics
	logger    *slog.Logger
	sealToken []byte // zeroed by Close

	gate gate.Gate

	// devices is the config-derived device lookup table: UUID -> DeviceInfo.
	// Built at startup from config.devices[]; serves as source of truth for
	// which devices azath may serve.
	devices map[string]DeviceInfo
}

// Option configures a KMSServer.
type Option func(*KMSServer)

// WithSealToken sets the bearer token required for Seal RPCs.
// KMSServer copies the token; the caller may zero its own copy immediately.
func WithSealToken(token []byte) Option {
	return func(s *KMSServer) {
		s.sealToken = make([]byte, len(token))
		copy(s.sealToken, token)
	}
}

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

// Close zeros the seal token after the caller has drained RPCs.
func (s *KMSServer) Close() {
	crypto.Zero(s.sealToken)
}

// Seal encrypts the data in req.Data using the master key, with the node UUID
// as additional authenticated data. The caller must provide a valid bearer token
// matching the configured seal token.
func (s *KMSServer) Seal(ctx context.Context, req *kms.Request) (*kms.Response, error) {
	start := time.Now()

	if err := s.checkSealToken(ctx); err != nil {
		return nil, err
	}

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

// checkSealToken extracts the bearer token from gRPC metadata and verifies it
// against the configured seal token using constant-time comparison.
func (s *KMSServer) checkSealToken(ctx context.Context) error {
	// Guard against zero-length sealToken: subtle.ConstantTimeCompare(empty, nil)
	// returns 1, which would bypass auth when no token is configured.
	if len(s.sealToken) == 0 {
		return status.Error(codes.Unauthenticated, "missing or invalid seal token")
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing or invalid seal token")
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return status.Error(codes.Unauthenticated, "missing or invalid seal token")
	}
	tokenStr, ok := strings.CutPrefix(vals[0], "Bearer ")
	if !ok {
		return status.Error(codes.Unauthenticated, "missing or invalid seal token")
	}
	tokenBytes := []byte(tokenStr)
	// tokenBytes is a copy from the gRPC wire buffer; zeroing is best-effort
	// (the original string backing store cannot be zeroed).
	defer crypto.Zero(tokenBytes)
	if subtle.ConstantTimeCompare(tokenBytes, s.sealToken) != 1 {
		return status.Error(codes.Unauthenticated, "missing or invalid seal token")
	}
	return nil
}
