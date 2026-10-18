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
	"testing"

	"github.com/google/uuid"
	kms "github.com/siderolabs/kms-client/api/kms"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/maruina/azath/internal/crypto"
	"github.com/maruina/azath/internal/gate"
	"github.com/maruina/azath/internal/keymanager"
	"github.com/maruina/azath/internal/observability"
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

// newTestServerAndClient creates a KMSServer with the given options plus defaults,
// registers it on a bufconn listener, and returns a connected KMSServiceClient.
// The server is cleaned up on t.Cleanup.
//
// Callers must pass WithDevices if they want Seal/Unseal to succeed for any UUID.
// Without WithDevices, all Seal requests are rejected as unconfigured.
func newTestServerAndClient(t *testing.T, km *keymanager.Manager, m *observability.Metrics, opts ...server.Option) (*server.KMSServer, kms.KMSServiceClient) {
	t.Helper()

	srv := server.New(km, m, opts...)

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

// sealOne is a helper that seals data for the given UUID and returns the sealed blob.
func sealOne(t *testing.T, ctx context.Context, client kms.KMSServiceClient, nodeUUID string, data []byte) []byte {
	t.Helper()
	resp, err := client.Seal(ctx, &kms.Request{
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
				return t.Context(), &kms.Request{
					NodeUuid: nodeUUID,
					Data:     plaintext,
				}, []server.Option{server.WithDevices(devices)}
			},
			wantCode: codes.OK,
			wantData: true,
		},
		{
			name: "invalid_uuid",
			setup: func(t *testing.T) (context.Context, *kms.Request, []server.Option) {
				t.Helper()
				return t.Context(), &kms.Request{
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
				return t.Context(), &kms.Request{
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
				return t.Context(), &kms.Request{
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
			mgr := newLoadedManager(t, testKey(), m)
			ctx, req, opts := tc.setup(t)
			_, client := newTestServerAndClient(t, mgr, m, opts...)

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

func TestSeal_ConfiguredDeviceWithoutPersistentState(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	mgr := newLoadedManager(t, testKey(), m)
	nodeUUID := uuid.New().String()
	devices := map[string]server.DeviceInfo{
		nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
	}
	_, client := newTestServerAndClient(t, mgr, m, server.WithDevices(devices))

	sealOne(t, t.Context(), client, nodeUUID, bytes.Repeat([]byte{1}, 32))

}

func TestSeal_IncrementsSealTotal(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	mgr := newLoadedManager(t, testKey(), m)
	nodeUUID := uuid.New().String()
	devices := map[string]server.DeviceInfo{
		nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
	}
	_, client := newTestServerAndClient(t, mgr, m, server.WithDevices(devices))

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
	mgr := newUnloadedManager(t, m)
	nodeUUID := uuid.New().String()
	devices := map[string]server.DeviceInfo{
		nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
	}
	_, client := newTestServerAndClient(t, mgr, m, server.WithDevices(devices))

	// Pre-register so the lookup succeeds (though Seal will fail on km.Sealer())

	_, err := client.Seal(t.Context(), &kms.Request{
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
		mutate     func(t *testing.T, km **keymanager.Manager, m *observability.Metrics, opts *[]server.Option, req **kms.Request, devices *map[string]server.DeviceInfo)
		wantReason string
		wantOK     bool // true = codes.OK with non-empty data
	}{
		{
			name: "unknown_uuid",
			mutate: func(_ *testing.T, _ **keymanager.Manager, _ *observability.Metrics, _ *[]server.Option, req **kms.Request, _ *map[string]server.DeviceInfo) {
				(*req).NodeUuid = uuid.New().String() // UUID not in config
			},
			wantReason: "unknown_uuid",
			wantOK:     true,
		},
		{
			name: "disabled",
			mutate: func(_ *testing.T, _ **keymanager.Manager, _ *observability.Metrics, _ *[]server.Option, _ **kms.Request, devices *map[string]server.DeviceInfo) {
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
			mutate: func(t *testing.T, km **keymanager.Manager, m *observability.Metrics, opts *[]server.Option, req **kms.Request, _ *map[string]server.DeviceInfo) {
				t.Helper()
				(*km) = newUnloadedManager(t, m)
			},
			wantReason: "master_key_not_loaded",
			wantOK:     true,
		},
		{
			name: "master_key_destroyed",
			mutate: func(t *testing.T, km **keymanager.Manager, m *observability.Metrics, opts *[]server.Option, req **kms.Request, _ *map[string]server.DeviceInfo) {
				t.Helper()
				// Destroy the loaded manager — ErrDestroyed also maps to master_key_not_loaded.
				(*km).Destroy()
			},
			wantReason: "master_key_not_loaded",
			wantOK:     true,
		},
		{
			name: "gate_denied",
			mutate: func(_ *testing.T, _ **keymanager.Manager, _ *observability.Metrics, opts *[]server.Option, _ **kms.Request, _ *map[string]server.DeviceInfo) {
				*opts = append(*opts, server.WithGate(&stubGate{result: gate.Denied}))
			},
			wantReason: "gate_denied",
			wantOK:     true,
		},
		{
			name: "gate_error_fails_closed",
			mutate: func(_ *testing.T, _ **keymanager.Manager, _ *observability.Metrics, opts *[]server.Option, _ **kms.Request, _ *map[string]server.DeviceInfo) {
				*opts = append(*opts, server.WithGate(&stubGate{result: gate.Denied, err: errors.New("api unavailable")}))
			},
			wantReason: "gate_denied",
			wantOK:     true,
		},
		{
			name: "decrypt_error",
			mutate: func(_ *testing.T, _ **keymanager.Manager, _ *observability.Metrics, _ *[]server.Option, req **kms.Request, _ *map[string]server.DeviceInfo) {
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
			mutate: func(t *testing.T, km **keymanager.Manager, m *observability.Metrics, _ *[]server.Option, req **kms.Request, _ *map[string]server.DeviceInfo) {
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

			req := &kms.Request{NodeUuid: nodeUUID, Data: sealed}
			tc.mutate(t, &km, m, &opts, &req, &devices)

			opts = append(opts, server.WithDevices(devices))
			_, client := newTestServerAndClient(t, km, m, opts...)

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
	mgr := newLoadedManager(t, testKey(), m)
	devices := map[string]server.DeviceInfo{
		nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
	}
	_, client := newTestServerAndClient(t, mgr, m,
		server.WithDevices(devices), server.WithGate(&stubGate{result: gate.Approved}))

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
	mgr := newLoadedManager(t, testKey(), m)
	devices := map[string]server.DeviceInfo{
		nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
	}
	_, client := newTestServerAndClient(t, mgr, m,
		server.WithDevices(devices), server.WithGate(&stubGate{result: gate.Approved}))

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
			sealResp, err := client.Seal(ctx, &kms.Request{
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

// --- Seal with unavailable key ---

func TestSeal_UnloadedMasterKeyFails(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	// Use unloaded manager so km.Sealer() fails.
	mgr := newUnloadedManager(t, m)
	nodeUUID := uuid.New().String()
	devices := map[string]server.DeviceInfo{
		nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
	}
	_, client := newTestServerAndClient(t, mgr, m, server.WithDevices(devices))

	_, err := client.Seal(t.Context(), &kms.Request{
		NodeUuid: nodeUUID,
		Data:     bytes.Repeat([]byte{1}, 32),
	})
	if err == nil {
		t.Fatal("Seal with unloaded key succeeded, want error")
	}
}

// --- Gate API error counter ---

func TestUnseal_GateError_IncrementsGateAPIErrors(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	mgr := newLoadedManager(t, testKey(), m)

	nodeUUID := uuid.New().String()
	devices := map[string]server.DeviceInfo{
		nodeUUID: {Name: "test-device", UUID: nodeUUID, Disabled: false},
	}
	// Pre-seal so we have a valid blob.
	sealer, err := mgr.Sealer()
	if err != nil {
		t.Fatalf("Sealer: %v", err)
	}
	sealed, err := sealer.Seal(bytes.Repeat([]byte{0xAB}, 32), []byte(nodeUUID))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	sealer.Destroy()

	_, client := newTestServerAndClient(t, mgr, m,
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

func TestNew_NilInputsPanic(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := newUnloadedManager(t, m)

	cases := []struct {
		name string
		fn   func()
	}{
		{
			name: "nil_manager",
			fn:   func() { server.New(nil, m) },
		},
		{
			name: "nil_metrics",
			fn:   func() { server.New(mgr, nil) },
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
