package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	kms "github.com/siderolabs/kms-client/api/kms"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/maruina/azath/internal/crypto"
	"github.com/maruina/azath/internal/testutil"
)

// fakeUnsealer implements kms.KMSServiceServer for client tests.
type fakeUnsealer struct {
	kms.UnimplementedKMSServiceServer
	plaintext []byte
	fail      bool
}

func (f *fakeUnsealer) Seal(ctx context.Context, req *kms.Request) (*kms.Response, error) {
	return nil, status.Error(codes.Unimplemented, "seal not implemented")
}

func (f *fakeUnsealer) Unseal(ctx context.Context, req *kms.Request) (*kms.Response, error) {
	if f.fail {
		return nil, status.Error(codes.Internal, "unseal failed")
	}
	resp := make([]byte, len(f.plaintext))
	copy(resp, f.plaintext)
	return &kms.Response{Data: resp}, nil
}

func startFakeUnsealer(t *testing.T, plaintext []byte, fail bool) (addr string, cleanup func()) {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("creating listener: %v", err)
	}
	srv := grpc.NewServer()
	kms.RegisterKMSServiceServer(srv, &fakeUnsealer{plaintext: plaintext, fail: fail})
	go func() {
		_ = srv.Serve(lis)
	}()
	return lis.Addr().String(), func() { srv.Stop() }
}

func writeClientConfigWithEndpoints(t *testing.T, deviceUUID string, endpoints ...string) string {
	t.Helper()
	cfgYAML := fmt.Sprintf(`device:
  name: test-device
  uuid: %s
endpoints:
`, deviceUUID)
	for _, ep := range endpoints {
		cfgYAML += "  - " + ep + "\n"
	}
	return testutil.WriteConfig(t, cfgYAML)
}

func writeSealedBlob(t *testing.T, data []byte) string {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString(data)
	path := filepath.Join(t.TempDir(), "blob.sealed")
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		t.Fatalf("writing sealed blob: %v", err)
	}
	return path
}

func TestClientCmd_Flags(t *testing.T) {
	t.Parallel()
	cmd := newClientCmd()
	f := cmd.Flags()

	required := []string{"config", "sealed-blob"}
	for _, name := range required {
		if fl := f.Lookup(name); fl == nil {
			t.Errorf("--%s flag not registered", name)
		}
	}

	if fl := f.Lookup("endpoint"); fl == nil {
		t.Error("--endpoint flag not registered")
	}
	if fl := f.Lookup("timeout"); fl == nil {
		t.Error("--timeout flag not registered")
	} else if fl.DefValue != "30s" {
		t.Errorf("--timeout default = %q, want %q", fl.DefValue, "30s")
	}
	if fl := f.Lookup("insecure-dev"); fl == nil {
		t.Error("--insecure-dev flag not registered")
	} else if fl.DefValue != "false" {
		t.Errorf("--insecure-dev default = %q, want %q", fl.DefValue, "false")
	}
}

func TestClientCmd_MissingConfig(t *testing.T) {
	t.Parallel()
	err := runClient(t.Context(), "", nil, "/tmp/blob", 30*time.Second, false, []string{"echo", "test"})
	if err == nil {
		t.Fatal("expected error for missing config, got nil")
	}
}

func TestClientCmd_InvalidDeviceUUID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	badCfg := `device:
  name: test
  uuid: not-a-uuid
endpoints:
  - 127.0.0.1:5000
`
	if err := os.WriteFile(cfgPath, []byte(badCfg), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	blobPath := writeSealedBlob(t, []byte("data"))
	err := runClient(t.Context(), cfgPath, nil, blobPath, 30*time.Second, false, []string{"echo", "test"})
	if err == nil {
		t.Fatal("expected error for invalid UUID, got nil")
	}
}

func TestClientCmd_NoEndpoints(t *testing.T) {
	t.Parallel()
	cfgPath := writeClientConfigWithEndpoints(t, "550e8400-e29b-41d4-a716-446655440000")
	blobPath := writeSealedBlob(t, []byte("data"))
	err := runClient(t.Context(), cfgPath, nil, blobPath, 30*time.Second, false, []string{"echo", "test"})
	if err == nil {
		t.Fatal("expected error for no endpoints, got nil")
	}
}

func TestClientCmd_MissingSealedBlob(t *testing.T) {
	t.Parallel()
	cfgPath := writeClientConfigWithEndpoints(t, "550e8400-e29b-41d4-a716-446655440000", "127.0.0.1:5000")
	err := runClient(t.Context(), cfgPath, nil, "/nonexistent/blob", 30*time.Second, false, []string{"echo", "test"})
	if err == nil {
		t.Fatal("expected error for missing sealed blob, got nil")
	}
}

func TestClientCmd_InvalidBase64Blob(t *testing.T) {
	t.Parallel()
	cfgPath := writeClientConfigWithEndpoints(t, "550e8400-e29b-41d4-a716-446655440000", "127.0.0.1:5000")
	blobPath := filepath.Join(t.TempDir(), "blob.sealed")
	if err := os.WriteFile(blobPath, []byte("not-base64!!!"), 0o600); err != nil {
		t.Fatalf("writing blob: %v", err)
	}
	err := runClient(t.Context(), cfgPath, nil, blobPath, 30*time.Second, false, []string{"echo", "test"})
	if err == nil {
		t.Fatal("expected error for invalid base64, got nil")
	}
}

func TestClientCmd_AllEndpointsFail(t *testing.T) {
	t.Parallel()
	addr, cleanup := startFakeUnsealer(t, nil, true)
	defer cleanup()

	cfgPath := writeClientConfigWithEndpoints(t, "550e8400-e29b-41d4-a716-446655440000", addr)
	blobPath := writeSealedBlob(t, []byte("sealed-data"))
	err := runClient(t.Context(), cfgPath, nil, blobPath, 30*time.Second, true, []string{"true"})
	if err == nil {
		t.Fatal("expected error when all endpoints fail, got nil")
	}
}

func TestClientCmd_FirstEndpointFails_SecondSucceeds(t *testing.T) {
	t.Parallel()
	plaintext := []byte("secret-passphrase")

	failAddr, failCleanup := startFakeUnsealer(t, nil, true)
	defer failCleanup()

	successAddr, successCleanup := startFakeUnsealer(t, plaintext, false)
	defer successCleanup()

	cfgPath := writeClientConfigWithEndpoints(t, "550e8400-e29b-41d4-a716-446655440000", failAddr, successAddr)
	blobPath := writeSealedBlob(t, []byte("sealed-data"))

	err := runClient(t.Context(), cfgPath, nil, blobPath, 30*time.Second, true, []string{"true"})
	if err != nil {
		t.Fatalf("expected success via fallback, got error: %v", err)
	}
}

func TestClientCmd_Success_h2c(t *testing.T) {
	t.Parallel()
	plaintext := []byte("test-secret")
	addr, cleanup := startFakeUnsealer(t, plaintext, false)
	defer cleanup()

	cfgPath := writeClientConfigWithEndpoints(t, "550e8400-e29b-41d4-a716-446655440000", addr)
	blobPath := writeSealedBlob(t, []byte("sealed-data"))

	err := runClient(t.Context(), cfgPath, nil, blobPath, 30*time.Second, true, []string{"true"})
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
}

func TestClientCmd_FlagEndpointPrepended(t *testing.T) {
	t.Parallel()
	plaintext := []byte("test-secret")
	addr, cleanup := startFakeUnsealer(t, plaintext, false)
	defer cleanup()

	// Config has one endpoint; flag endpoint is prepended and tried first.
	// Both point to the same good server so the test succeeds regardless of order.
	cfgPath := writeClientConfigWithEndpoints(t, "550e8400-e29b-41d4-a716-446655440000", addr)
	blobPath := writeSealedBlob(t, []byte("sealed-data"))

	err := runClient(t.Context(), cfgPath, []string{addr}, blobPath, 30*time.Second, true, []string{"true"})
	if err != nil {
		t.Fatalf("expected success with flag endpoint, got error: %v", err)
	}
}

func TestClientCmd_FlagEndpointTriedFirst(t *testing.T) {
	t.Parallel()
	plaintext := []byte("test-secret")
	addr, cleanup := startFakeUnsealer(t, plaintext, false)
	defer cleanup()

	// Add a second fake that always fails; it should never be reached because
	// the flag endpoint (tried first) succeeds.
	failAddr, failCleanup := startFakeUnsealer(t, nil, true)
	defer failCleanup()

	// Config endpoint is the failing one; flag endpoint is the good one.
	cfgPath := writeClientConfigWithEndpoints(t, "550e8400-e29b-41d4-a716-446655440000", failAddr)
	blobPath := writeSealedBlob(t, []byte("sealed-data"))

	err := runClient(t.Context(), cfgPath, []string{addr}, blobPath, 30*time.Second, true, []string{"true"})
	if err != nil {
		t.Fatalf("expected success with flag endpoint tried first, got error: %v", err)
	}
}

func TestClientCmd_CommandExitCode2(t *testing.T) {
	t.Parallel()
	plaintext := []byte("test-secret")
	addr, cleanup := startFakeUnsealer(t, plaintext, false)
	defer cleanup()

	cfgPath := writeClientConfigWithEndpoints(t, "550e8400-e29b-41d4-a716-446655440000", addr)
	blobPath := writeSealedBlob(t, []byte("sealed-data"))

	// "false" always exits 1, which should produce exitCodeError with code 2.
	err := runClient(t.Context(), cfgPath, nil, blobPath, 30*time.Second, true, []string{"false"})
	if err == nil {
		t.Fatal("expected error for non-zero exit code, got nil")
	}
	var exitErr *exitCodeError
	if !errorsAsExitCode(err, &exitErr) || exitErr.code != 2 {
		t.Fatalf("expected exitCodeError with code 2, got: %v", err)
	}
}

func errorsAsExitCode(err error, target **exitCodeError) bool {
	if err == nil {
		return false
	}
	var e *exitCodeError
	if !errors.As(err, &e) {
		return false
	}
	*target = e
	return true
}
func TestClientCmd_EmptyPlaintextTriesNext(t *testing.T) {
	t.Parallel()
	plaintext := []byte("real-secret")

	addr1, cleanup1 := startFakeUnsealer(t, []byte{}, false)
	defer cleanup1()

	addr2, cleanup2 := startFakeUnsealer(t, plaintext, false)
	defer cleanup2()

	cfgPath := writeClientConfigWithEndpoints(t, "550e8400-e29b-41d4-a716-446655440000", addr1, addr2)
	blobPath := writeSealedBlob(t, []byte("sealed-data"))

	err := runClient(t.Context(), cfgPath, nil, blobPath, 30*time.Second, true, []string{"true"})
	if err != nil {
		t.Fatalf("expected success via fallback after empty plaintext, got error: %v", err)
	}
}

func TestClientCmd_PlaintextZeroed(t *testing.T) {
	t.Parallel()
	plaintext := []byte("secret-to-zero")
	addr, cleanup := startFakeUnsealer(t, plaintext, false)
	defer cleanup()

	cfgPath := writeClientConfigWithEndpoints(t, "550e8400-e29b-41d4-a716-446655440000", addr)
	blobPath := writeSealedBlob(t, []byte("sealed-data"))

	err := runClient(t.Context(), cfgPath, nil, blobPath, 30*time.Second, true, []string{"true"})
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}

	// Sanity check that crypto.Zero works.
	buf := []byte("test-data")
	crypto.Zero(buf)
	for i, b := range buf {
		if b != 0 {
			t.Errorf("buf[%d] = %d, want 0", i, b)
			break
		}
	}
}

func TestClientCmd_NoShellInterpolation(t *testing.T) {
	t.Parallel()
	plaintext := []byte("secret with $HOME && echo pwned")
	addr, cleanup := startFakeUnsealer(t, plaintext, false)
	defer cleanup()

	cfgPath := writeClientConfigWithEndpoints(t, "550e8400-e29b-41d4-a716-446655440000", addr)
	blobPath := writeSealedBlob(t, []byte("sealed-data"))

	// "printf %s" receives the argument literally — no shell expansion.
	err := runClient(t.Context(), cfgPath, nil, blobPath, 30*time.Second, true, []string{"printf", "%s"})
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
}

func TestClientCmd_CommandArgsNotLogged(t *testing.T) {
	t.Parallel()
	_, err := execCommand(t.Context(), []string{"/nonexistent/binary"}, []byte("secret"))
	if err == nil {
		t.Fatal("expected error for nonexistent binary, got nil")
	}
	if containsStr(err.Error(), "secret") {
		t.Errorf("error message contains secret: %v", err)
	}
}

func TestExecCommand_AppendsPlaintextAsFinalArg(t *testing.T) {
	t.Parallel()
	plaintext := []byte("my-passphrase")

	exitCode, err := execCommand(t.Context(), []string{"true"}, plaintext)
	if err != nil {
		t.Fatalf("execCommand returned error: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
}

func TestExecCommand_NoArgs(t *testing.T) {
	t.Parallel()
	_, err := execCommand(t.Context(), nil, []byte("secret"))
	if err == nil {
		t.Fatal("expected error for empty args, got nil")
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestTryUnseal_ContextTimeout(t *testing.T) {
	t.Parallel()
	_, err := tryUnseal(t.Context(), "127.0.0.1:1", "550e8400-e29b-41d4-a716-446655440000", []byte("data"), 100*time.Millisecond, false)
	if err == nil {
		t.Fatal("expected error for unreachable endpoint, got nil")
	}
}
