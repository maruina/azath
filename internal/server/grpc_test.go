package server_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/google/uuid"
	kms "github.com/siderolabs/kms-client/api/kms"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/maruina/azath/internal/gate"
	"github.com/maruina/azath/internal/keymanager"
	"github.com/maruina/azath/internal/observability"
	"github.com/maruina/azath/internal/registry"
	"github.com/maruina/azath/internal/server"
)

// loggerOpt passes a *slog.Logger through the variadic-any boundary of newTestServerWithGRPC.
type loggerOpt struct {
	logger *slog.Logger
}

func withLogger(l *slog.Logger) loggerOpt { return loggerOpt{l} }

// newTestServerWithGRPC creates a KMSServer via NewGRPCServer (with interceptors)
// and returns a connected KMSServiceClient.
func newTestServerWithGRPC(
	t *testing.T,
	km *keymanager.Manager,
	reg *registry.Registry,
	m *observability.Metrics,
	testOpts ...any,
) (*server.KMSServer, kms.KMSServiceClient) {
	t.Helper()

	var logger *slog.Logger
	var serverOpts []server.Option

	serverOpts = append(serverOpts, server.WithSealToken([]byte(testSealToken)))

	for _, opt := range testOpts {
		switch v := opt.(type) {
		case loggerOpt:
			logger = v.logger
		case server.Option:
			serverOpts = append(serverOpts, v)
		}
	}

	srv := server.New(km, reg, m, serverOpts...)
	gs, _ := server.NewGRPCServer(srv, logger)

	lis := bufconn.Listen(bufSize)
	go func() {
		if err := gs.Serve(lis); err != nil {
			// gs.Stop closes the listener; that exit is expected.
			t.Logf("gs.Serve exited: %v", err)
		}
	}()

	t.Cleanup(func() {
		gs.Stop()
		_ = lis.Close()
		srv.Close()
	})

	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = cc.Close() })

	return srv, kms.NewKMSServiceClient(cc)
}

// newHealthClient creates a HealthClient connected to the same bufconn listener.
func newHealthClient(t *testing.T, lis *bufconn.Listener) grpc_health_v1.HealthClient {
	t.Helper()
	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return grpc_health_v1.NewHealthClient(cc)
}

// --- gRPC integration tests ---

func TestNewGRPCServer_HealthCheck_Serving(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	reg := newTestRegistry(t, m)
	mgr := newUnloadedManager(t, m)

	srv := server.New(mgr, reg, m, server.WithSealToken([]byte(testSealToken)))
	gs, _ := server.NewGRPCServer(srv, nil)

	lis := bufconn.Listen(bufSize)
	go func() {
		if err := gs.Serve(lis); err != nil {
			t.Logf("gs.Serve exited: %v", err)
		}
	}()
	t.Cleanup(func() {
		gs.Stop()
		_ = lis.Close()
		srv.Close()
	})

	hc := newHealthClient(t, lis)
	resp, err := hc.Check(t.Context(), &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health.Check: %v", err)
	}
	if resp.Status != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Errorf("health status = %v, want SERVING", resp.Status)
	}
}

func TestNewGRPCServer_MaxRecvMsgSize_Enforced(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	reg := newTestRegistry(t, m)
	mgr := newUnloadedManager(t, m)
	nodeUUID := uuid.New().String()
	devices := map[string]server.DeviceInfo{
		nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
	}

	_, client := newTestServerWithGRPC(t, mgr, reg, m, server.WithDevices(devices))

	oversized := make([]byte, (1<<20)+1)
	_, err := client.Seal(ctxWithBearer(t.Context(), testSealToken), &kms.Request{
		NodeUuid: nodeUUID,
		Data:     oversized,
	})
	if err == nil {
		t.Fatal("Seal with oversized message succeeded, want error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.ResourceExhausted {
		t.Errorf("Seal oversized message code = %v, want ResourceExhausted", st.Code())
	}
}

func TestNewGRPCServer_PanicRecovery(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	reg := newTestRegistry(t, m)
	mgr := newLoadedManager(t, testKey(), m)

	nodeUUID := uuid.New().String()
	devices := map[string]server.DeviceInfo{
		nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
	}
	if err := reg.Register(nodeUUID, nodeUUID); err != nil {
		t.Fatalf("Register: %v", err)
	}

	_, client := newTestServerWithGRPC(t, mgr, reg, m,
		server.WithDevices(devices),
		server.WithGate(&panickingGate{}),
	)

	// Seal so we have a valid blob.
	sealed := sealOne(t, t.Context(), client, nodeUUID, bytes.Repeat([]byte{1}, 32))

	// Unseal triggers the gate which panics. The oracle contract requires
	// codes.OK + random bytes — a panic must not produce a distinguishable error.
	resp, err := client.Unseal(t.Context(), &kms.Request{
		NodeUuid: nodeUUID,
		Data:     sealed,
	})
	if err != nil {
		t.Fatalf("Unseal after gate panic returned error, want codes.OK: %v", err)
	}
	if len(resp.Data) == 0 {
		t.Error("Unseal after gate panic returned empty data, want oracle-safe random bytes")
	}

	// Server must still be alive — do another Seal successfully.
	_ = sealOne(t, t.Context(), client, nodeUUID, bytes.Repeat([]byte{2}, 32))
}

func TestNewGRPCServer_LoggingInterceptor(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger, err := observability.NewLogger("debug", "text", observability.WithWriter(&buf))
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	m := newTestMetrics(t)
	reg := newTestRegistry(t, m)
	mgr := newLoadedManager(t, testKey(), m)

	nodeUUID := uuid.New().String()
	devices := map[string]server.DeviceInfo{
		nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
	}

	_, client := newTestServerWithGRPC(t, mgr, reg, m,
		withLogger(logger),
		server.WithDevices(devices),
	)

	sealOne(t, t.Context(), client, nodeUUID, bytes.Repeat([]byte{1}, 32))

	logged := buf.String()
	for _, want := range []string{"method", "/sidero.kms.KMSService/Seal", "code", "duration_ms"} {
		if !strings.Contains(logged, want) {
			t.Errorf("loggingInterceptor: log missing %q\nfull log:\n%s", want, logged)
		}
	}
}

// panickingGate is a Gate that always panics, used to test panic recovery.
type panickingGate struct{}

func (g *panickingGate) Check(_ context.Context, _ gate.Device) (gate.Decision, error) {
	panic("test gate panic")
}

func (g *panickingGate) Close() error { return nil }
