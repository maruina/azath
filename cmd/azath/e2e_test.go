package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/maruina/azath/internal/gate"
	"github.com/maruina/azath/internal/keymanager"
	"github.com/maruina/azath/internal/observability"
	"github.com/maruina/azath/internal/registry"
	"github.com/maruina/azath/internal/server"
	"github.com/maruina/azath/internal/testutil"
)

// e2eSealToken is the bearer token the e2e server requires for Seal.
const e2eSealToken = "e2e-seal-token"

// e2eDeviceUUID is the device the e2e server is configured to serve.
const e2eDeviceUUID = "550e8400-e29b-41d4-a716-446655440000"

// pendingOnceGate pends the first Check per device and approves subsequent
// ones, modeling the Telegram approve flow: prompt on first request, approval
// cache hit on retry.
type pendingOnceGate struct {
	mu      sync.Mutex
	checked map[string]int
}

func (g *pendingOnceGate) Check(_ context.Context, d gate.Device) (gate.Decision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.checked == nil {
		g.checked = make(map[string]int)
	}
	g.checked[d.UUID]++
	if g.checked[d.UUID] <= 1 {
		return gate.Pending, nil
	}
	return gate.Approved, nil
}

func (g *pendingOnceGate) Close() error { return nil }

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

	reg, err := registry.Load(filepath.Join(t.TempDir(), "registry.json"), nil, m, nil)
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}

	kmsSrv := server.New(mgr, reg, m,
		server.WithSealToken([]byte(e2eSealToken)),
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

// captureScript returns a shell command pair that records its final argument
// and exits nonzero when it does not equal the expected passphrase. It models
// synoshare --enc_mount, which fails on a wrong passphrase.
func captureScript(capturePath, expected string) []string {
	return []string{"sh", "-c", `printf '%s' "$1" > "$AZATH_E2E_CAPTURE"; [ "$1" = "$AZATH_E2E_EXPECTED" ]`, "--"}
}

// readCapture reads the recorded final argument of the last capture run.
func readCapture(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 — test capture path
	if err != nil {
		t.Fatalf("reading capture file: %v", err)
	}
	return string(raw)
}

// captureOrNoExec returns the recorded final argument, or ok=false when the
// command never ran. Pending/denied responses carry random bytes that may
// contain NUL, which makes exec refuse the argument entirely — both shapes
// are a failed mount, so callers must tolerate a missing capture.
func captureOrNoExec(t *testing.T, path string) (string, bool) {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 — test capture path
	if errors.Is(err, os.ErrNotExist) {
		return "", false
	}
	if err != nil {
		t.Fatalf("reading capture file: %v", err)
	}
	return string(raw), true
}

// sealViaStdin seals passphrase through the real server by running runSeal
// with a stdin pipe, mirroring how azath seal reads a secret non-interactively.
func sealViaStdin(t *testing.T, cfgPath, tokenPath, blobPath, passphrase string) {
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
	if sealErr := runSeal(t.Context(), cfgPath, nil, tokenPath, blobPath, false, true); sealErr != nil {
		t.Fatalf("runSeal: %v", sealErr)
	}
}

// TestE2E_SealThenClientUnseal covers the full wire path: azath seal against
// a real KMS server, then azath client unsealing through the pending ->
// approve -> retry flow a Synology boot task experiences.
func TestE2E_SealThenClientUnseal(t *testing.T) {
	// Cannot use t.Parallel — manipulates os.Stdin and environment.
	addr := startE2EServer(t, &pendingOnceGate{}, e2eDeviceUUID)
	cfgPath := writeE2EClientConfig(t, addr)

	// Seal a passphrase through the real server.
	tokenPath := filepath.Join(t.TempDir(), "seal-token")
	if err := os.WriteFile(tokenPath, []byte(e2eSealToken), 0o600); err != nil {
		t.Fatalf("writing seal token: %v", err)
	}
	blobPath := filepath.Join(t.TempDir(), "homes.key.sealed")

	passphrase := "e2e-passphrase"
	sealViaStdin(t, cfgPath, tokenPath, blobPath, passphrase)

	capture := filepath.Join(t.TempDir(), "captured")
	t.Setenv("AZATH_E2E_CAPTURE", capture)
	t.Setenv("AZATH_E2E_EXPECTED", passphrase)
	cmd := captureScript(capture, passphrase)

	// First attempt: the gate pends, and the server returns random bytes with
	// a successful gRPC status (oracle contract). The client cannot tell the
	// difference and must fail the mount with the wrong passphrase. If the
	// random bytes contain NUL, exec refuses the argument entirely — also a
	// failed mount.
	if err := runClient(t.Context(), cfgPath, nil, blobPath, 30*time.Second, true, cmd); err == nil {
		t.Error("first client run expected to fail the mount, got nil error")
	}
	if got, ran := captureOrNoExec(t, capture); ran && got == passphrase {
		t.Errorf("first run passed the real passphrase to the command before approval")
	}

	// Second attempt: after approval (cache hit), the real passphrase arrives
	// and the mount succeeds.
	if err := runClient(t.Context(), cfgPath, nil, blobPath, 30*time.Second, true, cmd); err != nil {
		t.Fatalf("second client run: %v", err)
	}
	if got := readCapture(t, capture); got != passphrase {
		t.Errorf("command received %q, want the sealed passphrase", got)
	}
}

// TestE2E_ClientDenyIsIndistinguishable verifies that a denied unseal never
// exposes the passphrase and fails the mount.
func TestE2E_ClientDenyIsIndistinguishable(t *testing.T) {
	// Cannot use t.Parallel — manipulates environment.
	addr := startE2EServer(t, denyGate{}, e2eDeviceUUID)
	cfgPath := writeE2EClientConfig(t, addr)

	tokenPath := filepath.Join(t.TempDir(), "seal-token")
	if err := os.WriteFile(tokenPath, []byte(e2eSealToken), 0o600); err != nil {
		t.Fatalf("writing seal token: %v", err)
	}
	blobPath := filepath.Join(t.TempDir(), "homes.key.sealed")

	passphrase := "deny-passphrase"
	sealViaStdin(t, cfgPath, tokenPath, blobPath, passphrase)

	capture := filepath.Join(t.TempDir(), "captured")
	t.Setenv("AZATH_E2E_CAPTURE", capture)
	t.Setenv("AZATH_E2E_EXPECTED", passphrase)
	cmd := captureScript(capture, passphrase)

	if err := runClient(t.Context(), cfgPath, nil, blobPath, 30*time.Second, true, cmd); err == nil {
		t.Error("denied unseal expected to fail the mount, got nil error")
	}
	if got, ran := captureOrNoExec(t, capture); ran && got == passphrase {
		t.Error("denied unseal passed the real passphrase to the command")
	}
}
