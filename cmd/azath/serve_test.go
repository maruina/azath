package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maruina/azath/internal/testutil"
)

// writeDevConfig writes a minimal valid config for dev mode. keyPath and
// registryPath are absolute paths to the key file and registry JSON file.
// grpcAddr and metricsAddr must be valid loopback addresses (e.g. "127.0.0.1:4050").
func writeDevConfig(t *testing.T, keyPath, registryPath, grpcAddr, metricsAddr string) string {
	t.Helper()
	cfgYAML := fmt.Sprintf(`
server:
  listen: "%s"
  metrics_listen: "%s"
  seal_token_ref: "op://vault/azath/seal-token"
  name: dev-server
master_key:
  source: file
  path: %s
registry:
  path: %s
notifications:
  telegram:
    bot_token_ref: "op://vault/azath/bot-token"
    chat_id: "456"
devices:
  - name: talos-node
    uuid: 550e8400-e29b-41d4-a716-446655440000
gate:
  type: telegram
  telegram:
    authorized_user_id: "123456789"
`, grpcAddr, metricsAddr, keyPath, registryPath)
	return testutil.WriteConfig(t, cfgYAML)
}

// freePort finds an available TCP port on localhost and returns its string representation.
func freePort(t *testing.T) string {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	_, port, err := net.SplitHostPort(lis.Addr().String())
	if err != nil {
		t.Fatalf("freePort: splitting host/port: %v", err)
	}
	if err := lis.Close(); err != nil {
		t.Fatalf("freePort: closing listener: %v", err)
	}
	return port
}

func TestServeCmd_Flags(t *testing.T) {
	t.Parallel()
	cmd := newServeCmd()
	f := cmd.Flags()

	if fl := f.Lookup("config"); fl == nil {
		t.Fatal("--config flag not registered")
	} else if fl.DefValue != "/etc/azath/server.yaml" {
		t.Errorf("--config default = %q, want %q", fl.DefValue, "/etc/azath/server.yaml")
	}

	if fl := f.Lookup("dev"); fl == nil {
		t.Fatal("--dev flag not registered")
	} else if fl.DefValue != "false" {
		t.Errorf("--dev default = %q, want %q", fl.DefValue, "false")
	}
}

func TestServeCmd_MissingConfigFile(t *testing.T) {
	t.Parallel()
	err := runServe(t.Context(), "/nonexistent/server.yaml", true)
	if err == nil {
		t.Fatal("expected error for missing config file, got nil")
	}
	if !strings.Contains(err.Error(), "loading config") {
		t.Errorf("err = %q, want substring %q", err.Error(), "loading config")
	}
}

func TestServeCmd_InvalidConfig(t *testing.T) {
	t.Parallel()
	// Missing seal_token_ref — Validate should fail.
	invalid := `
server:
  listen: "127.0.0.1:0"
  metrics_listen: "127.0.0.1:0"
master_key:
  source: file
  path: /etc/azath/master.key
registry:
  path: /tmp/registry.json
devices:
  - name: talos-node
    uuid: 550e8400-e29b-41d4-a716-446655440000
`
	err := runServe(t.Context(), testutil.WriteConfig(t, invalid), true)
	if err == nil {
		t.Fatal("expected error for invalid config, got nil")
	}
	if !strings.Contains(err.Error(), "invalid config") {
		t.Errorf("err = %q, want substring %q", err.Error(), "invalid config")
	}
}

func TestServeCmd_MissingKeyFile(t *testing.T) {
	// t.Setenv cannot be combined with t.Parallel.
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "nonexistent.key")
	registryPath := filepath.Join(dir, "registry.json")
	cfgPath := writeDevConfig(t, keyPath, registryPath, "127.0.0.1:0", "127.0.0.1:0")

	t.Setenv("AZATH_SEAL_TOKEN", "test-seal-token")

	err := runServe(t.Context(), cfgPath, true)
	if err == nil {
		t.Fatal("expected error for missing key file, got nil")
	}
	if !strings.Contains(err.Error(), "loading master key") {
		t.Errorf("err = %q, want substring %q", err.Error(), "loading master key")
	}
}

func TestServeCmd_MissingSealTokenEnvVar(t *testing.T) {
	// t.Setenv cannot be combined with t.Parallel.
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "master.key")
	registryPath := filepath.Join(dir, "registry.json")

	testutil.WriteKeyFile(t, keyPath)

	cfgPath := writeDevConfig(t, keyPath, registryPath, "127.0.0.1:0", "127.0.0.1:0")

	// Ensure AZATH_SEAL_TOKEN is not set.
	t.Setenv("AZATH_SEAL_TOKEN", "")

	err := runServe(t.Context(), cfgPath, true)
	if err == nil {
		t.Fatal("expected error for missing seal token env var, got nil")
	}
	if !strings.Contains(err.Error(), "resolving seal token") {
		t.Errorf("err = %q, want substring %q", err.Error(), "resolving seal token")
	}
}

func TestServeCmd_DevModeBootAndShutdown(t *testing.T) {
	// t.Setenv cannot be combined with t.Parallel.
	dir := t.TempDir()

	keyPath := filepath.Join(dir, "master.key")
	testutil.WriteKeyFile(t, keyPath)

	registryPath := filepath.Join(dir, "registry.json")
	grpcAddr := "127.0.0.1:" + freePort(t)
	metricsAddr := "127.0.0.1:" + freePort(t)
	cfgPath := writeDevConfig(t, keyPath, registryPath, grpcAddr, metricsAddr)

	t.Setenv("AZATH_SEAL_TOKEN", "test-seal-token-value")
	t.Setenv("AZATH_BOT_TOKEN", "fake-bot-token-for-testing")

	ctx, cancel := context.WithCancel(t.Context())

	errCh := make(chan error, 1)
	go func() {
		errCh <- runServe(ctx, cfgPath, true)
	}()

	// Poll until the gRPC listener accepts connections (up to 5s).
	// On timeout, drain errCh to avoid leaking the runServe goroutine.
	pollCtx, pollCancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer pollCancel()
	dialer := &net.Dialer{}
	for {
		conn, dialErr := dialer.DialContext(pollCtx, "tcp", grpcAddr)
		if dialErr == nil {
			if err := conn.Close(); err != nil {
				t.Logf("closing probe connection: %v", err)
			}
			break
		}
		select {
		case <-pollCtx.Done():
			cancel() // stop runServe
			<-errCh  // drain to avoid goroutine leak
			t.Fatal("gRPC listener did not become available within 5s")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Cancel the context to trigger graceful shutdown.
	cancel()

	shutdownTimer := time.NewTimer(15 * time.Second)
	defer shutdownTimer.Stop()
	select {
	case err := <-errCh:
		// Context cancellation is expected during the signal wait; accept it as clean.
		if err != nil && !isContextError(err) {
			t.Errorf("runServe returned unexpected error: %v", err)
		}
	case <-shutdownTimer.C:
		t.Error("runServe did not return within 15s after context cancel")
	}
}

// isContextError reports whether err wraps context.Canceled or context.DeadlineExceeded.
func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
