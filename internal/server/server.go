package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"log/slog"
	"strings"
	"sync"
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
	"github.com/maruina/azath/internal/registry"
)

type deviceInfo struct {
	Name     string
	UUID     string
	Disabled bool
}

// DeviceInfo is exported for use by cmd/azath/serve.go when building the
// config-derived device lookup table.
type DeviceInfo = deviceInfo

// KMSServer implements kms.KMSServiceServer. It handles Seal and Unseal RPCs
// with bearer-token auth, gate approval, and async notifications.
// Call Close during shutdown to drain in-flight notifications and zero the seal token.
type KMSServer struct {
	kms.UnimplementedKMSServiceServer

	km        *keymanager.Manager
	reg       *registry.Registry
	metrics   *observability.Metrics
	logger    *slog.Logger
	sealToken []byte // zeroed by Close

	gate     Gate
	notifier Notifier

	// devices is the config-derived device lookup table: UUID -> deviceInfo.
	// Built at startup from config.devices[]; serves as source of truth for
	// which devices azath may serve.
	devices map[string]deviceInfo

	// notifyWg tracks in-flight notification goroutines. shutdownCtx cancels
	// them on Close; notifyWg.Wait() drains before the seal token is zeroed.
	notifyWg       sync.WaitGroup
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc
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
// A nil gate (the default) approves all requests automatically.
func WithGate(g Gate) Option {
	return func(s *KMSServer) { s.gate = g }
}

// WithNotifier sets the notifier for async event notifications.
// A nil notifier (the default) is a no-op.
func WithNotifier(n Notifier) Option {
	return func(s *KMSServer) { s.notifier = n }
}

// WithDevices sets the config-derived device lookup table.
// The map keys are canonical UUIDs (lowercase, hyphenated).
func WithDevices(devices map[string]deviceInfo) Option {
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

// New creates a KMSServer. Panics if km, reg, or m is nil.
func New(km *keymanager.Manager, reg *registry.Registry, m *observability.Metrics, opts ...Option) *KMSServer {
	if km == nil {
		panic("server.New: km must not be nil")
	}
	if reg == nil {
		panic("server.New: reg must not be nil")
	}
	if m == nil {
		panic("server.New: m must not be nil")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &KMSServer{
		km:             km,
		reg:            reg,
		metrics:        m,
		logger:         slog.New(slog.DiscardHandler),
		shutdownCtx:    ctx,
		shutdownCancel: cancel,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Close cancels in-flight notification goroutines, waits for them to finish,
// then zeros the seal token. Idempotent — safe to call multiple times.
func (s *KMSServer) Close() {
	s.shutdownCancel()
	s.notifyWg.Wait()
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

	// Register the device only after a successful seal. Talos KMS protocol sends
	// only a UUID; use it as the device name — no richer registration signal is
	// available from the protocol.
	_, lookupErr := s.reg.Lookup(nodeUUIDStr)
	isNew := errors.Is(lookupErr, registry.ErrDeviceNotFound)
	if isNew {
		if regErr := s.reg.Register(nodeUUIDStr, nodeUUIDStr); regErr != nil {
			s.logger.ErrorContext(ctx, "failed to register new device", "node_uuid", nodeUUIDStr[:8], "error", regErr)
			return nil, status.Errorf(codes.Internal, "registering device %s: %v", nodeUUIDStr, regErr)
		}
	} else if lookupErr != nil {
		s.logger.ErrorContext(ctx, "registry lookup failed", "node_uuid", nodeUUIDStr[:8], "error", lookupErr)
		return nil, status.Errorf(codes.Internal, "looking up device %s: %v", nodeUUIDStr, lookupErr)
	}

	s.metrics.SealTotal.Inc()
	s.metrics.SealDuration.Observe(time.Since(start).Seconds())

	if isNew && s.notifier != nil {
		// Use shutdownCtx as parent so Close() cancels in-flight notifications.
		// The 5s timeout bounds per-notification latency.
		notifCtx, cancel := context.WithTimeout(s.shutdownCtx, 5*time.Second)
		s.notifyWg.Go(func() {
			defer cancel()
			defer func() {
				if r := recover(); r != nil {
					s.logger.ErrorContext(notifCtx, "notifier panic", "node_uuid", nodeUUIDStr[:8], "panic", r)
				}
			}()
			if err := s.notifier.NotifySeal(notifCtx, nodeUUIDStr); err != nil {
				// Use "unknown" as provider label until gate implementations
				// provide their names via a Name() method.
				s.logger.ErrorContext(notifCtx, "NotifySeal failed", "node_uuid", nodeUUIDStr[:8], "error", err)
				s.metrics.NotificationFailures.WithLabelValues("unknown").Inc()
			}
		})
	}

	s.logger.InfoContext(ctx, "seal", "node_uuid", nodeUUIDStr[:8])

	return &kms.Response{Data: sealed}, nil
}

// Unseal decrypts sealed data. Every non-success path returns codes.OK with
// random bytes to prevent oracle attacks — the caller cannot distinguish
// failure modes from a valid decryption failure.
func (s *KMSServer) Unseal(ctx context.Context, req *kms.Request) (*kms.Response, error) {
	start := time.Now()

	// randomResp returns random bytes sized to the expected plaintext length.
	// Size is derived from the sealed blob so the response length is consistent
	// with a successful decrypt — preventing length-based oracle attacks.
	randomResp := func() *kms.Response {
		n := len(req.GetData()) - crypto.MinSealedLen
		if n <= 0 {
			n = crypto.KeySize
		}
		buf := make([]byte, n)
		// rand.Read overwrites buf with entropy; on failure (catastrophic: system
		// entropy unavailable) we log and return the zero buffer.
		if _, err := rand.Read(buf); err != nil {
			s.logger.ErrorContext(ctx, "rand.Read failed", "error", err)
		}
		return &kms.Response{Data: buf}
	}

	// fail increments the reason counter, observes duration, and returns random bytes.
	// All Unseal failure paths must go through fail to uphold the oracle contract.
	fail := func(reason string) (*kms.Response, error) {
		s.metrics.UnsealTotal.WithLabelValues(reason).Inc()
		s.metrics.UnsealDuration.Observe(time.Since(start).Seconds())
		s.logger.InfoContext(ctx, "unseal", "reason", reason)
		return randomResp(), nil
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

	_, err = s.reg.Lookup(nodeUUIDStr)
	if err != nil {
		// ErrDeviceNotFound maps to unknown_uuid. Callers cannot enumerate registered
		// devices by probing, and cannot distinguish an unregistered device from one
		// that doesn't exist via the response.
		return fail("unknown_uuid")
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

	if s.gate != nil {
		result, gateErr := s.gate.Check(ctx, gate.Device{Name: dev.Name, UUID: nodeUUIDStr})
		if gateErr != nil {
			// Gate infrastructure failure — log the real error so operators can
			// distinguish a policy denial from an API outage. Fail closed.
			s.logger.ErrorContext(ctx, "gate check error", "node_uuid", nodeUUIDStr[:8], "error", gateErr)
			s.metrics.GateAPIErrors.WithLabelValues("telegram").Inc()
			return fail("gate_denied")
		}
		// Check for explicit approval; treat all other values (Denied, Pending,
		// and any unknown future value) as non-approval — fail-closed by design.
		switch result {
		case gate.Approved:
			// proceed
		case gate.Pending:
			return fail("gate_pending")
		default:
			return fail("gate_denied")
		}
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
