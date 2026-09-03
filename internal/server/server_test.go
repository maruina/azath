package server_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	kms "github.com/siderolabs/kms-client/api/kms"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/maruina/azath/internal/crypto"
	"github.com/maruina/azath/internal/gate"
	"github.com/maruina/azath/internal/keymanager"
	"github.com/maruina/azath/internal/observability"
	"github.com/maruina/azath/internal/registry"
	"github.com/maruina/azath/internal/server"
)

// --- test helpers ---

const bufSize = 1 << 20 // 1 MB

// testKey returns a deterministic 32-byte key.
func testKey() []byte {
	key := make([]byte, crypto.KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

// altKey returns a different 32-byte key (for wrong-instance tests).
func altKey() []byte {
	key := make([]byte, crypto.KeySize)
	for i := range key {
		key[i] = byte(i + 32)
	}
	return key
}

// writeKeyFile writes a raw 32-byte key to a temp file and returns the path.
func writeKeyFile(t *testing.T, key []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.key")
	if err := os.WriteFile(path, key, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// newTestMetrics returns a fresh Metrics instance.
func newTestMetrics(t *testing.T) *observability.Metrics {
	t.Helper()
	return observability.NewMetrics()
}

// newTestRegistry returns an empty in-memory registry backed by a temp dir.
func newTestRegistry(t *testing.T, m *observability.Metrics) *registry.Registry {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registry.json")
	reg, err := registry.Load(path, nil, m, nil)
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	return reg
}

// newLoadedManager returns a Manager with the test key loaded.
func newLoadedManager(t *testing.T, key []byte, m *observability.Metrics) *keymanager.Manager {
	t.Helper()
	mgr := keymanager.New(m, nil)
	src := keymanager.NewFileSource(writeKeyFile(t, key))
	if err := mgr.Load(t.Context(), src); err != nil {
		t.Fatalf("manager.Load: %v", err)
	}
	return mgr
}

// newUnloadedManager returns a Manager that has not had Load called.
func newUnloadedManager(t *testing.T, m *observability.Metrics) *keymanager.Manager {
	t.Helper()
	return keymanager.New(m, nil)
}

const testSealToken = "super-secret-seal-token"

// newTestServerAndClient creates a KMSServer with the given options plus defaults,
// registers it on a bufconn listener, and returns a connected KMSServiceClient.
// The server is cleaned up on t.Cleanup.
//
// Callers must pass WithDevices if they want Seal/Unseal to succeed for any UUID.
// Without WithDevices, all Seal requests are rejected as unconfigured.
func newTestServerAndClient(t *testing.T, km *keymanager.Manager, reg *registry.Registry, m *observability.Metrics, opts ...server.Option) (*server.KMSServer, kms.KMSServiceClient) {
	t.Helper()

	allOpts := append([]server.Option{server.WithSealToken([]byte(testSealToken))}, opts...)

	srv := server.New(km, reg, m, allOpts...)

	lis := bufconn.Listen(bufSize)
	gs := grpc.NewServer()
	kms.RegisterKMSServiceServer(gs, srv)

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

// ctxWithBearer returns a context carrying the given bearer token.
func ctxWithBearer(ctx context.Context, token string) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
}

// sealOne is a helper that seals data for the given UUID and returns the sealed blob.
func sealOne(t *testing.T, ctx context.Context, client kms.KMSServiceClient, nodeUUID string, data []byte) []byte {
	t.Helper()
	resp, err := client.Seal(ctxWithBearer(ctx, testSealToken), &kms.Request{
		NodeUuid: nodeUUID,
		Data:     data,
	})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	return resp.Data
}

// --- Seal tests ---

func TestSeal(t *testing.T) {
	t.Parallel()

	plaintext := bytes.Repeat([]byte{0xAB}, 32)

	cases := []struct {
		name     string
		setup    func(t *testing.T) (context.Context, *kms.Request, []server.Option)
		wantCode codes.Code
		wantData bool // true = expect non-empty response data
	}{
		{
			name: "valid",
			setup: func(t *testing.T) (context.Context, *kms.Request, []server.Option) {
				t.Helper()
				nodeUUID := uuid.New().String()
				devices := map[string]server.DeviceInfo{
					nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
				}
				return ctxWithBearer(t.Context(), testSealToken), &kms.Request{
					NodeUuid: nodeUUID,
					Data:     plaintext,
				}, []server.Option{server.WithDevices(devices)}
			},
			wantCode: codes.OK,
			wantData: true,
		},
		{
			name: "missing_bearer_token",
			setup: func(t *testing.T) (context.Context, *kms.Request, []server.Option) {
				t.Helper()
				nodeUUID := uuid.New().String()
				devices := map[string]server.DeviceInfo{
					nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
				}
				return t.Context(), &kms.Request{
					NodeUuid: nodeUUID,
					Data:     plaintext,
				}, []server.Option{server.WithDevices(devices)}
			},
			wantCode: codes.Unauthenticated,
		},
		{
			name: "wrong_bearer_token",
			setup: func(t *testing.T) (context.Context, *kms.Request, []server.Option) {
				t.Helper()
				nodeUUID := uuid.New().String()
				devices := map[string]server.DeviceInfo{
					nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
				}
				return ctxWithBearer(t.Context(), "wrong-token"), &kms.Request{
					NodeUuid: nodeUUID,
					Data:     plaintext,
				}, []server.Option{server.WithDevices(devices)}
			},
			wantCode: codes.Unauthenticated,
		},
		{
			name: "invalid_uuid",
			setup: func(t *testing.T) (context.Context, *kms.Request, []server.Option) {
				t.Helper()
				return ctxWithBearer(t.Context(), testSealToken), &kms.Request{
					NodeUuid: "not-a-uuid",
					Data:     plaintext,
				}, nil
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "unknown_uuid",
			setup: func(t *testing.T) (context.Context, *kms.Request, []server.Option) {
				t.Helper()
				devices := map[string]server.DeviceInfo{}
				return ctxWithBearer(t.Context(), testSealToken), &kms.Request{
					NodeUuid: uuid.New().String(),
					Data:     plaintext,
				}, []server.Option{server.WithDevices(devices)}
			},
			wantCode: codes.PermissionDenied,
		},
		{
			name: "disabled_device",
			setup: func(t *testing.T) (context.Context, *kms.Request, []server.Option) {
				t.Helper()
				nodeUUID := uuid.New().String()
				devices := map[string]server.DeviceInfo{
					nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: true},
				}
				return ctxWithBearer(t.Context(), testSealToken), &kms.Request{
					NodeUuid: nodeUUID,
					Data:     plaintext,
				}, []server.Option{server.WithDevices(devices)}
			},
			wantCode: codes.PermissionDenied,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newTestMetrics(t)
			reg := newTestRegistry(t, m)
			mgr := newLoadedManager(t, testKey(), m)
			ctx, req, opts := tc.setup(t)
			_, client := newTestServerAndClient(t, mgr, reg, m, opts...)

			resp, err := client.Seal(ctx, req)
			st, _ := status.FromError(err)
			if st.Code() != tc.wantCode {
				t.Errorf("Seal code = %v, want %v (err: %v)", st.Code(), tc.wantCode, err)
			}
			if tc.wantData {
				if err != nil {
					t.Fatalf("Seal error: %v", err)
				}
				if len(resp.Data) == 0 {
					t.Error("Seal returned empty data, want non-empty")
				}
			}
		})
	}
}

func TestSeal_AutoRegistersNewDevice(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	reg := newTestRegistry(t, m)
	mgr := newLoadedManager(t, testKey(), m)
	nodeUUID := uuid.New().String()
	devices := map[string]server.DeviceInfo{
		nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
	}
	_, client := newTestServerAndClient(t, mgr, reg, m, server.WithDevices(devices))

	// Device not in registry initially.
	if reg.Len() != 0 {
		t.Fatalf("expected empty registry, got %d devices", reg.Len())
	}

	sealOne(t, t.Context(), client, nodeUUID, bytes.Repeat([]byte{1}, 32))

	// After seal, device must be registered.
	entry, err := reg.Lookup(nodeUUID)
	if err != nil {
		t.Fatalf("Lookup after Seal: %v", err)
	}
	if entry.UUID != nodeUUID {
		t.Errorf("registered UUID = %q, want %q", entry.UUID, nodeUUID)
	}
}

func TestSeal_IncrementsSealTotal(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	reg := newTestRegistry(t, m)
	mgr := newLoadedManager(t, testKey(), m)
	nodeUUID := uuid.New().String()
	devices := map[string]server.DeviceInfo{
		nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
	}
	_, client := newTestServerAndClient(t, mgr, reg, m, server.WithDevices(devices))

	for range 3 {
		sealOne(t, t.Context(), client, nodeUUID, bytes.Repeat([]byte{1}, 32))
	}

	mfs, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("Registry.Gather: %v", err)
	}
	found := false
	for _, mf := range mfs {
		if mf.GetName() != "azath_seal_total" {
			continue
		}
		found = true
		for _, metric := range mf.GetMetric() {
			if got := metric.GetCounter().GetValue(); got != 3 {
				t.Errorf("azath_seal_total = %v, want 3", got)
			}
		}
	}
	if !found {
		t.Error("azath_seal_total metric not found in Gather output")
	}
}

func TestSeal_MasterKeyNotLoaded(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	reg := newTestRegistry(t, m)
	mgr := newUnloadedManager(t, m)
	nodeUUID := uuid.New().String()
	devices := map[string]server.DeviceInfo{
		nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
	}
	_, client := newTestServerAndClient(t, mgr, reg, m, server.WithDevices(devices))

	// Pre-register so the lookup succeeds (though Seal will fail on km.Sealer())
	if err := reg.Register(nodeUUID, nodeUUID); err != nil {
		t.Fatalf("Register: %v", err)
	}

	_, err := client.Seal(ctxWithBearer(t.Context(), testSealToken), &kms.Request{
		NodeUuid: nodeUUID,
		Data:     bytes.Repeat([]byte{1}, 32),
	})
	if err == nil {
		t.Fatal("expected Seal to fail when master key not loaded, got nil error")
	}
}

// --- Unseal oracle contract tests ---

// unsealCounterValue reads the value of azath_unseal_total for the given reason label.
func unsealCounterValue(t *testing.T, m *observability.Metrics, reason string) float64 {
	t.Helper()
	// Seed the counter so it appears in Gather even with 0 observations.
	m.UnsealTotal.WithLabelValues(reason)
	mfs, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "azath_unseal_total" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			for _, lp := range metric.GetLabel() {
				if lp.GetName() == "reason" && lp.GetValue() == reason {
					return metric.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// stubGate is a Gate implementation for tests.
type stubGate struct {
	result gate.Decision
	err    error
}

func (g *stubGate) Check(_ context.Context, _ gate.Device) (gate.Decision, error) {
	return g.result, g.err
}

func (g *stubGate) Close() error { return nil }

func TestUnseal_OracleContract(t *testing.T) {
	t.Parallel()

	plaintext := bytes.Repeat([]byte{0xAB}, 32)
	nodeUUID := uuid.New().String()

	cases := []struct {
		name       string
		mutate     func(t *testing.T, km **keymanager.Manager, reg *registry.Registry, m *observability.Metrics, opts *[]server.Option, req **kms.Request, devices *map[string]server.DeviceInfo)
		wantReason string
		wantOK     bool // true = codes.OK with non-empty data
	}{
		{
			name: "unknown_uuid",
			mutate: func(_ *testing.T, _ **keymanager.Manager, _ *registry.Registry, _ *observability.Metrics, _ *[]server.Option, req **kms.Request, _ *map[string]server.DeviceInfo) {
				(*req).NodeUuid = uuid.New().String() // UUID not in config or registry
			},
			wantReason: "unknown_uuid",
			wantOK:     true,
		},
		{
			name: "disabled",
			mutate: func(_ *testing.T, _ **keymanager.Manager, _ *registry.Registry, _ *observability.Metrics, _ *[]server.Option, _ **kms.Request, devices *map[string]server.DeviceInfo) {
				// Mark nodeUUID as disabled in config.
				info := (*devices)[nodeUUID]
				info.Disabled = true
				(*devices)[nodeUUID] = info
			},
			wantReason: "disabled",
			wantOK:     true,
		},
		{
			name: "master_key_not_loaded",
			mutate: func(t *testing.T, km **keymanager.Manager, reg *registry.Registry, m *observability.Metrics, opts *[]server.Option, req **kms.Request, _ *map[string]server.DeviceInfo) {
				t.Helper()
				(*km) = newUnloadedManager(t, m)
			},
			wantReason: "master_key_not_loaded",
			wantOK:     true,
		},
		{
			name: "master_key_destroyed",
			mutate: func(t *testing.T, km **keymanager.Manager, reg *registry.Registry, m *observability.Metrics, opts *[]server.Option, req **kms.Request, _ *map[string]server.DeviceInfo) {
				t.Helper()
				// Destroy the loaded manager — ErrDestroyed also maps to master_key_not_loaded.
				(*km).Destroy()
			},
			wantReason: "master_key_not_loaded",
			wantOK:     true,
		},
		{
			name: "gate_denied",
			mutate: func(_ *testing.T, _ **keymanager.Manager, _ *registry.Registry, _ *observability.Metrics, opts *[]server.Option, _ **kms.Request, _ *map[string]server.DeviceInfo) {
				*opts = append(*opts, server.WithGate(&stubGate{result: gate.Denied}))
			},
			wantReason: "gate_denied",
			wantOK:     true,
		},
		{
			name: "gate_pending",
			mutate: func(_ *testing.T, _ **keymanager.Manager, _ *registry.Registry, _ *observability.Metrics, opts *[]server.Option, _ **kms.Request, _ *map[string]server.DeviceInfo) {
				*opts = append(*opts, server.WithGate(&stubGate{result: gate.Pending}))
			},
			wantReason: "gate_pending",
			wantOK:     true,
		},
		{
			name: "gate_error_fails_closed",
			mutate: func(_ *testing.T, _ **keymanager.Manager, _ *registry.Registry, _ *observability.Metrics, opts *[]server.Option, _ **kms.Request, _ *map[string]server.DeviceInfo) {
				*opts = append(*opts, server.WithGate(&stubGate{result: gate.Denied, err: errors.New("api unavailable")}))
			},
			wantReason: "gate_denied",
			wantOK:     true,
		},
		{
			name: "decrypt_error",
			mutate: func(_ *testing.T, _ **keymanager.Manager, _ *registry.Registry, _ *observability.Metrics, _ *[]server.Option, req **kms.Request, _ *map[string]server.DeviceInfo) {
				// Send tampered data — valid length but wrong content
				tampered := make([]byte, len((*req).Data))
				copy(tampered, (*req).Data)
				tampered[len(tampered)-1] ^= 0xFF
				(*req).Data = tampered
			},
			wantReason: "decrypt_error",
			wantOK:     true,
		},
		{
			name: "wrong_instance",
			mutate: func(t *testing.T, km **keymanager.Manager, reg *registry.Registry, m *observability.Metrics, _ *[]server.Option, req **kms.Request, _ *map[string]server.DeviceInfo) {
				t.Helper()
				// Seal with alt key then try to unseal with original key
				altMgr := newLoadedManager(t, altKey(), m)
				altSealer, err := altMgr.Sealer()
				if err != nil {
					t.Fatalf("Sealer: %v", err)
				}
				sealed, err := altSealer.Seal(plaintext, []byte(nodeUUID))
				if err != nil {
					t.Fatalf("Seal: %v", err)
				}
				altSealer.Destroy()
				(*req).Data = sealed
			},
			wantReason: "wrong_instance",
			wantOK:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := newTestMetrics(t)
			reg := newTestRegistry(t, m)
			km := newLoadedManager(t, testKey(), m)
			var opts []server.Option

			// Build devices map with nodeUUID configured.
			devices := map[string]server.DeviceInfo{
				nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
			}

			// Seal the plaintext first (to produce valid sealed blob for most cases).
			sealer, err := km.Sealer()
			if err != nil {
				t.Fatalf("Sealer: %v", err)
			}
			sealed, err := sealer.Seal(plaintext, []byte(nodeUUID))
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			sealer.Destroy()

			// Register the device in the registry.
			if regErr := reg.Register(nodeUUID, nodeUUID); regErr != nil {
				t.Fatalf("Register: %v", regErr)
			}

			req := &kms.Request{NodeUuid: nodeUUID, Data: sealed}
			tc.mutate(t, &km, reg, m, &opts, &req, &devices)

			opts = append(opts, server.WithDevices(devices))
			_, client := newTestServerAndClient(t, km, reg, m, opts...)

			resp, err := client.Unseal(t.Context(), req)
			st, _ := status.FromError(err)

			// All paths must return codes.OK.
			if st.Code() != codes.OK {
				t.Errorf("Unseal code = %v, want OK (err: %v)", st.Code(), err)
			}
			if err != nil {
				t.Fatalf("Unseal returned error: %v", err)
			}
			// Response must be non-empty.
			if len(resp.Data) == 0 {
				t.Error("Unseal returned empty data, want non-empty")
			}
			// All oracle contract cases must NOT return plaintext — only the success path does.
			if bytes.Equal(resp.Data, plaintext) {
				t.Errorf("Unseal(%s) failure returned actual plaintext, want random bytes", tc.name)
			}
			// Correct reason label must be incremented.
			if got := unsealCounterValue(t, m, tc.wantReason); got < 1 {
				t.Errorf("UnsealTotal{reason=%q} = %v, want >= 1", tc.wantReason, got)
			}
		})
	}
}

func TestUnseal_Success(t *testing.T) {
	t.Parallel()

	plaintext := bytes.Repeat([]byte{0xCD}, 32)
	nodeUUID := uuid.New().String()

	m := newTestMetrics(t)
	reg := newTestRegistry(t, m)
	mgr := newLoadedManager(t, testKey(), m)
	devices := map[string]server.DeviceInfo{
		nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
	}
	_, client := newTestServerAndClient(t, mgr, reg, m, server.WithDevices(devices))

	// Seal via client.
	sealed := sealOne(t, t.Context(), client, nodeUUID, plaintext)

	// Unseal and verify round-trip.
	resp, err := client.Unseal(t.Context(), &kms.Request{
		NodeUuid: nodeUUID,
		Data:     sealed,
	})
	if err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if !bytes.Equal(resp.Data, plaintext) {
		t.Errorf("Unseal data = %x, want %x", resp.Data, plaintext)
	}

	// Verify ok counter incremented.
	if got := unsealCounterValue(t, m, "ok"); got < 1 {
		t.Errorf("UnsealTotal{reason=ok} = %v, want >= 1", got)
	}
}

// --- Concurrent stress test ---

func TestServer_ConcurrentSealUnseal(t *testing.T) {
	t.Parallel()

	plaintext := bytes.Repeat([]byte{0xEF}, 32)
	nodeUUID := uuid.New().String()

	m := newTestMetrics(t)
	reg := newTestRegistry(t, m)
	mgr := newLoadedManager(t, testKey(), m)
	devices := map[string]server.DeviceInfo{
		nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
	}
	_, client := newTestServerAndClient(t, mgr, reg, m, server.WithDevices(devices))

	const goroutines = 100
	// Capture context once outside goroutines to avoid t.Context() in goroutines.
	ctx := t.Context()
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			// Inline Seal to avoid t.Fatalf from sealOne, which only exits the goroutine.
			sealResp, err := client.Seal(ctxWithBearer(ctx, testSealToken), &kms.Request{
				NodeUuid: nodeUUID,
				Data:     plaintext,
			})
			if err != nil {
				errs <- fmt.Errorf("Seal: %w", err)
				return
			}
			unsealResp, err := client.Unseal(ctx, &kms.Request{
				NodeUuid: nodeUUID,
				Data:     sealResp.Data,
			})
			if err != nil {
				errs <- fmt.Errorf("Unseal: %w", err)
				return
			}
			if !bytes.Equal(unsealResp.Data, plaintext) {
				errs <- fmt.Errorf("Unseal(%s): data mismatch", nodeUUID)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// --- Close / token zeroing test ---

func TestClose_ZerosSealToken(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	reg := newTestRegistry(t, m)
	mgr := newLoadedManager(t, testKey(), m)
	srv := server.New(mgr, reg, m, server.WithSealToken([]byte(testSealToken)))

	// Token should be set before Close.
	if len(server.SealTokenForTesting(srv)) == 0 {
		t.Fatal("SealToken is empty before Close")
	}

	srv.Close()

	// After Close, token must be zeroed.
	tok := server.SealTokenForTesting(srv)
	for i, b := range tok {
		if b != 0 {
			t.Errorf("sealToken[%d] = %#x after Close, want 0", i, b)
		}
	}
}

// --- Seal registration ordering ---

func TestSeal_DoesNotRegisterOnSealFailure(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	reg := newTestRegistry(t, m)
	// Use unloaded manager so km.Sealer() fails.
	mgr := newUnloadedManager(t, m)
	nodeUUID := uuid.New().String()
	devices := map[string]server.DeviceInfo{
		nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
	}
	_, client := newTestServerAndClient(t, mgr, reg, m, server.WithDevices(devices))

	_, err := client.Seal(ctxWithBearer(t.Context(), testSealToken), &kms.Request{
		NodeUuid: nodeUUID,
		Data:     bytes.Repeat([]byte{1}, 32),
	})
	if err == nil {
		t.Fatal("Seal with unloaded key succeeded, want error")
	}

	// Device must NOT be in the registry — no phantom entry.
	if reg.Len() != 0 {
		t.Errorf("registry has %d devices after failed Seal, want 0", reg.Len())
	}
}

// --- Auth: empty seal token bypass ---

func TestSeal_EmptyTokenBypassed(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	reg := newTestRegistry(t, m)
	mgr := newLoadedManager(t, testKey(), m)
	nodeUUID := uuid.New().String()
	devices := map[string]server.DeviceInfo{
		nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
	}

	// Create server WITHOUT WithSealToken — sealToken is nil/empty.
	srv := server.New(mgr, reg, m, server.WithDevices(devices))
	defer srv.Close()

	// Build an incoming context with an empty bearer token.
	incomingCtx := metadata.NewIncomingContext(
		t.Context(),
		metadata.Pairs("authorization", "Bearer "),
	)
	resp, err := srv.Seal(incomingCtx, &kms.Request{
		NodeUuid: nodeUUID,
		Data:     bytes.Repeat([]byte{1}, 32),
	})
	if err == nil {
		t.Fatalf("Seal with empty sealToken + empty bearer succeeded (resp=%v), want Unauthenticated", resp)
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unauthenticated {
		t.Errorf("Seal empty token code = %v, want Unauthenticated", st.Code())
	}
}

// --- Gate API error counter ---

func TestUnseal_GateError_IncrementsGateAPIErrors(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	reg := newTestRegistry(t, m)
	mgr := newLoadedManager(t, testKey(), m)

	nodeUUID := uuid.New().String()
	devices := map[string]server.DeviceInfo{
		nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
	}
	// Register and pre-seal so we have a valid blob.
	sealer, err := mgr.Sealer()
	if err != nil {
		t.Fatalf("Sealer: %v", err)
	}
	sealed, err := sealer.Seal(bytes.Repeat([]byte{0xAB}, 32), []byte(nodeUUID))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	sealer.Destroy()
	if regErr := reg.Register(nodeUUID, nodeUUID); regErr != nil {
		t.Fatalf("Register: %v", regErr)
	}

	_, client := newTestServerAndClient(t, mgr, reg, m,
		server.WithDevices(devices),
		server.WithGate(&stubGate{result: gate.Denied, err: errors.New("api timeout")}),
	)

	_, unsealErr := client.Unseal(t.Context(), &kms.Request{NodeUuid: nodeUUID, Data: sealed})
	if unsealErr != nil {
		t.Fatalf("Unseal: %v", unsealErr)
	}

	// GateAPIErrors{gate="telegram"} must have been incremented.
	m.GateAPIErrors.WithLabelValues("telegram")
	mfs, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	found := false
	for _, mf := range mfs {
		if mf.GetName() != "azath_gate_api_errors_total" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			for _, lp := range metric.GetLabel() {
				if lp.GetName() == "gate" && lp.GetValue() == "telegram" {
					found = true
					if got := metric.GetCounter().GetValue(); got < 1 {
						t.Errorf("azath_gate_api_errors_total{gate=telegram} = %v, want >= 1", got)
					}
				}
			}
		}
	}
	if !found {
		t.Error("azath_gate_api_errors_total{gate=telegram} not found")
	}
}

// --- Notifier tests ---

// stubNotifier records NotifySeal calls and can return a configured error.
type stubNotifier struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (n *stubNotifier) NotifySeal(_ context.Context, nodeUUID string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, nodeUUID)
	return n.err
}

func (n *stubNotifier) callCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.calls)
}

func TestNotifier_CalledOnceForNewDevice(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	reg := newTestRegistry(t, m)
	mgr := newLoadedManager(t, testKey(), m)
	nodeUUID := uuid.New().String()
	devices := map[string]server.DeviceInfo{
		nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
	}

	notifier := &stubNotifier{}
	_, client := newTestServerAndClient(t, mgr, reg, m,
		server.WithDevices(devices),
		server.WithNotifier(notifier),
	)

	// First Seal — new device, should trigger notification.
	sealOne(t, t.Context(), client, nodeUUID, bytes.Repeat([]byte{1}, 32))
	// Second Seal — known device, no notification.
	sealOne(t, t.Context(), client, nodeUUID, bytes.Repeat([]byte{2}, 32))

	// Give the goroutine time to execute (it's async).
	// Use a simple poll since we don't have a channel signal here.
	var count int
	for range 50 {
		count = notifier.callCount()
		if count >= 1 {
			break
		}
		// Brief yield to let the goroutine run.
		runtime_yield()
	}
	if count != 1 {
		t.Errorf("NotifySeal called %d times, want exactly 1 (new device only)", count)
	}
}

func TestNotifier_FailureIncrementsNotificationFailures(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	reg := newTestRegistry(t, m)
	mgr := newLoadedManager(t, testKey(), m)
	nodeUUID := uuid.New().String()
	devices := map[string]server.DeviceInfo{
		nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
	}

	notifier := &stubNotifier{err: errors.New("telegram down")}
	_, client := newTestServerAndClient(t, mgr, reg, m,
		server.WithDevices(devices),
		server.WithNotifier(notifier),
	)

	sealOne(t, t.Context(), client, nodeUUID, bytes.Repeat([]byte{1}, 32))

	// Poll until the goroutine completes.
	var count int
	for range 50 {
		count = notifier.callCount()
		if count >= 1 {
			break
		}
		runtime_yield()
	}
	if count < 1 {
		t.Fatal("NotifySeal never called")
	}

	m.NotificationFailures.WithLabelValues("unknown")
	mfs, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	found := false
	for _, mf := range mfs {
		if mf.GetName() != "azath_notification_failures_total" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			for _, lp := range metric.GetLabel() {
				if lp.GetName() == "provider" && lp.GetValue() == "unknown" {
					found = true
					if got := metric.GetCounter().GetValue(); got < 1 {
						t.Errorf("notification_failures_total{provider=unknown} = %v, want >= 1", got)
					}
				}
			}
		}
	}
	if !found {
		t.Error("notification_failures_total{provider=unknown} not found")
	}
}

func TestClose_DrainsNotifierGoroutines(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	reg := newTestRegistry(t, m)
	mgr := newLoadedManager(t, testKey(), m)
	nodeUUID := uuid.New().String()
	devices := map[string]server.DeviceInfo{
		nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
	}

	ready := make(chan struct{})
	unblock := make(chan struct{})
	var entered atomic.Bool

	blockingN := &blockingNotifier{
		ready:   ready,
		unblock: unblock,
		entered: &entered,
	}

	srv := server.New(mgr, reg, m,
		server.WithSealToken([]byte(testSealToken)),
		server.WithDevices(devices),
		server.WithNotifier(blockingN),
	)

	// Call Seal directly with an incoming metadata context (avoids gRPC stack).
	incomingCtx := metadata.NewIncomingContext(
		t.Context(),
		metadata.Pairs("authorization", "Bearer "+testSealToken),
	)
	_, err := srv.Seal(incomingCtx, &kms.Request{
		NodeUuid: nodeUUID,
		Data:     bytes.Repeat([]byte{1}, 32),
	})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Wait for the goroutine to enter NotifySeal.
	select {
	case <-ready:
	case <-t.Context().Done():
		t.Fatal("goroutine never entered NotifySeal")
	}

	// Close must block while the goroutine is running.
	closeDone := make(chan struct{})
	go func() {
		defer close(closeDone)
		srv.Close()
	}()

	select {
	case <-closeDone:
		t.Fatal("Close returned before notifier goroutine unblocked")
	default:
		// Good: Close is still waiting.
	}

	// Unblock the notifier.
	close(unblock)

	// Close must return promptly now.
	select {
	case <-closeDone:
		// Expected.
	case <-t.Context().Done():
		t.Fatal("Close never returned after notifier goroutine unblocked")
	}

	// Seal token must be zeroed.
	tok := server.SealTokenForTesting(srv)
	for i, b := range tok {
		if b != 0 {
			t.Errorf("sealToken[%d] = %#x after Close, want 0", i, b)
		}
	}
}

// blockingNotifier blocks in NotifySeal until unblocked by the test.
type blockingNotifier struct {
	ready   chan struct{}
	unblock chan struct{}
	entered *atomic.Bool
}

func (n *blockingNotifier) NotifySeal(_ context.Context, _ string) error {
	if n.entered.CompareAndSwap(false, true) {
		close(n.ready)
	}
	<-n.unblock
	return nil
}

// runtime_yield gives other goroutines a chance to run. Used only in tests
// that need to observe async side-effects without channels.
func runtime_yield() {
	var wg sync.WaitGroup
	wg.Go(func() {})
	wg.Wait()
}

// --- New panics on nil inputs ---

func TestNew_NilInputsPanic(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	reg := newTestRegistry(t, m)
	mgr := newUnloadedManager(t, m)

	cases := []struct {
		name string
		fn   func()
	}{
		{
			name: "nil_manager",
			fn:   func() { server.New(nil, reg, m) },
		},
		{
			name: "nil_registry",
			fn:   func() { server.New(mgr, nil, m) },
		},
		{
			name: "nil_metrics",
			fn:   func() { server.New(mgr, reg, nil) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("New with %s did not panic, want panic", tc.name)
				}
			}()
			tc.fn()
		})
	}
}
