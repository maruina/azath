package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/maruina/azath/internal/gate"
	"github.com/maruina/azath/internal/keymanager"
	"github.com/maruina/azath/internal/observability"
	"github.com/maruina/azath/internal/server"
	"github.com/maruina/azath/internal/testutil"
)

// e2eDeviceUUID is the device the e2e server is configured to serve.
const e2eDeviceUUID = "550e8400-e29b-41d4-a716-446655440000"

// denyGate denies every Check, modeling a rejected or ignored approval.
type denyGate struct{}

func (denyGate) Check(_ context.Context, _ gate.Device) (gate.Decision, error) {
	return gate.Denied, nil
}

func (denyGate) Close() error { return nil }

// startE2EServer starts a real KMSServer behind the real NewGRPCServer wiring
// on a loopback h2c listener and returns its address.
func startE2EServer(t *testing.T, g gate.Gate, deviceUUID string) string {
	t.Helper()

	m := observability.NewMetrics()
	mgr := keymanager.New(m, nil)
	keyPath := filepath.Join(t.TempDir(), "master.key")
	testutil.WriteKeyFile(t, keyPath)
	if err := mgr.Load(t.Context(), keymanager.NewFileSource(keyPath)); err != nil {
		t.Fatalf("loading master key: %v", err)
	}

	kmsSrv := server.New(mgr, m,
		server.WithGate(g),
		server.WithDevices(map[string]server.DeviceInfo{
			deviceUUID: {Name: "synology", UUID: deviceUUID},
		}),
	)

	grpcSrv, _ := server.NewGRPCServer(kmsSrv, nil)
	lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	go func() {
		_ = grpcSrv.Serve(lis)
	}()
	t.Cleanup(func() {
		grpcSrv.Stop()
		kmsSrv.Close()
	})
	return lis.Addr().String()
}

// writeE2EClientConfig writes a client config pointing at the given endpoint.
func writeE2EClientConfig(t *testing.T, addr string) string {
	t.Helper()
	return testutil.WriteConfig(t, "device:\n  name: synology\n  uuid: "+e2eDeviceUUID+"\nendpoints:\n  - "+addr+"\n")
}

// sealViaStdin seals passphrase through the real server by running runSeal
// with a stdin pipe, mirroring how azath seal reads a secret non-interactively.
func sealViaStdin(t *testing.T, cfgPath, blobPath, passphrase string) {
	t.Helper()
	oldStdin := os.Stdin
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("creating pipe: %v", pipeErr)
	}
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()
	if _, writeErr := w.Write([]byte(passphrase)); writeErr != nil {
		t.Fatalf("writing stdin: %v", writeErr)
	}
	_ = w.Close()
	if sealErr := runSeal(t.Context(), cfgPath, nil, blobPath, false, true); sealErr != nil {
		t.Fatalf("runSeal: %v", sealErr)
	}
}
