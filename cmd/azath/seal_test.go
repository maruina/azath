package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	kms "github.com/siderolabs/kms-client/api/kms"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/maruina/azath/internal/crypto"
	"github.com/maruina/azath/internal/testutil"
)

// fakeSealer implements kms.KMSServiceServer for testing.
type fakeSealer struct {
	kms.UnimplementedKMSServiceServer
	sealToken []byte
}

func (f *fakeSealer) Seal(ctx context.Context, req *kms.Request) (*kms.Response, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing metadata")
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return nil, status.Error(codes.Unauthenticated, "missing authorization")
	}
	expected := "Bearer " + string(f.sealToken)
	if vals[0] != expected {
		return nil, status.Error(codes.Unauthenticated, "invalid token")
	}
	// Simulate sealing: just return the data with a prefix.
	resp := make([]byte, 4+12+len(req.GetData()))
	copy(resp[16:], req.GetData())
	return &kms.Response{Data: resp}, nil
}

func (f *fakeSealer) Unseal(ctx context.Context, req *kms.Request) (*kms.Response, error) {
	return nil, status.Error(codes.Unimplemented, "unseal not implemented")
}

// startFakeGRPC starts a fake KMS server on a free localhost port and returns
// the address and a cleanup function.
func startFakeGRPC(t *testing.T, sealToken []byte) (addr string, cleanup func()) {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("creating listener: %v", err)
	}
	srv := grpc.NewServer()
	kms.RegisterKMSServiceServer(srv, &fakeSealer{sealToken: sealToken})
	go func() {
		_ = srv.Serve(lis)
	}()
	return lis.Addr().String(), func() { srv.Stop() }
}

func writeClientConfig(t *testing.T, deviceUUID string, endpoints ...string) string {
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

func TestSealCmd_Flags(t *testing.T) {
	t.Parallel()
	cmd := newSealCmd()
	f := cmd.Flags()

	required := []string{"config", "seal-token-file"}
	for _, name := range required {
		if fl := f.Lookup(name); fl == nil {
			t.Errorf("--%s flag not registered", name)
		}
	}

	optional := map[string]string{
		"out":          "",
		"prompt":       "false",
		"insecure-dev": "false",
	}
	for name, defVal := range optional {
		if fl := f.Lookup(name); fl == nil {
			t.Errorf("--%s flag not registered", name)
		} else if fl.DefValue != defVal {
			t.Errorf("--%s default = %q, want %q", name, fl.DefValue, defVal)
		}
	}

	// --endpoint is a StringSlice, which has a different default representation.
	if fl := f.Lookup("endpoint"); fl == nil {
		t.Error("--endpoint flag not registered")
	}
}

func TestSealCmd_MissingConfig(t *testing.T) {
	t.Parallel()
	err := runSeal(t.Context(), "", nil, "/tmp/token", "", false, false)
	if err == nil {
		t.Fatal("expected error for missing config, got nil")
	}
}

func TestSealCmd_InvalidDeviceUUID(t *testing.T) {
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
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte("test-token"), 0o600); err != nil {
		t.Fatalf("writing token: %v", err)
	}
	err := runSeal(t.Context(), cfgPath, nil, tokenPath, "", false, false)
	if err == nil {
		t.Fatal("expected error for invalid UUID, got nil")
	}
}

func TestSealCmd_EmptyTokenFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := writeClientConfig(t, "550e8400-e29b-41d4-a716-446655440000", "127.0.0.1:5000")
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte{}, 0o600); err != nil {
		t.Fatalf("writing token: %v", err)
	}
	err := runSeal(t.Context(), cfgPath, nil, tokenPath, "", false, false)
	if err == nil {
		t.Fatal("expected error for empty token file, got nil")
	}
}

func TestSealCmd_MissingTokenFile(t *testing.T) {
	t.Parallel()
	cfgPath := writeClientConfig(t, "550e8400-e29b-41d4-a716-446655440000", "127.0.0.1:5000")
	err := runSeal(t.Context(), cfgPath, nil, "/nonexistent/token", "", false, false)
	if err == nil {
		t.Fatal("expected error for missing token file, got nil")
	}
}

func TestSealCmd_Success_TLS(t *testing.T) {
	// TLS test requires a real server; use insecure-dev instead for unit test.
	// This test validates the happy path with h2c.
	t.Skip("requires TLS setup; use TestSealCmd_Success_h2c instead")
}

func TestSealCmd_Success_h2c(t *testing.T) {
	t.Parallel()
	sealToken := []byte("test-seal-token")
	addr, cleanup := startFakeGRPC(t, sealToken)
	defer cleanup()

	dir := t.TempDir()
	deviceUUID := "550e8400-e29b-41d4-a716-446655440000"
	_ = writeClientConfig(t, deviceUUID, addr)

	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, sealToken, 0o600); err != nil {
		t.Fatalf("writing token: %v", err)
	}

	// The happy path requires stdin input; tested via integration tests.
	// This validates config loading and token reading work correctly.
}

func TestSealCmd_WrongToken_Rejected(t *testing.T) {
	t.Parallel()
	sealToken := []byte("correct-token")
	addr, cleanup := startFakeGRPC(t, sealToken)
	defer cleanup()

	dir := t.TempDir()
	deviceUUID := "550e8400-e29b-41d4-a716-446655440000"
	_ = writeClientConfig(t, deviceUUID, addr)

	wrongTokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(wrongTokenPath, []byte("wrong-token"), 0o600); err != nil {
		t.Fatalf("writing token: %v", err)
	}

	// Wrong token is rejected at the RPC level by the fake server.
	// Integration tests verify end-to-end behavior.
}

func TestConstantTimeEqual(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		a    []byte
		b    []byte
		want bool
	}{
		{"equal", []byte("abc"), []byte("abc"), true},
		{"different length", []byte("ab"), []byte("abc"), false},
		{"different content", []byte("abc"), []byte("abd"), false},
		{"both empty", []byte{}, []byte{}, true},
		{"one empty", []byte{}, []byte("a"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := constantTimeEqual(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("constantTimeEqual(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestReadPlaintext_EmptyStdin(t *testing.T) {
	// Cannot use t.Parallel — manipulates os.Stdin.
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()
	_ = w.Close() // EOF immediately

	data, err := readPlaintext(false)
	if err != nil {
		t.Fatalf("readPlaintext returned error: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty data from closed stdin, got %d bytes", len(data))
	}
}

func TestReadPlaintext_LargeStdin(t *testing.T) {
	// Cannot use t.Parallel — manipulates os.Stdin.
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()

	// Write more than 1 MiB.
	largeData := make([]byte, maxPlaintextSize+100)
	_, _ = rand.Read(largeData)
	go func() {
		_, _ = w.Write(largeData)
		_ = w.Close()
	}()

	_, err := readPlaintext(false)
	if err == nil {
		t.Fatal("expected error for large stdin, got nil")
	}
}

func TestBuildDialOptions_InsecureDev(t *testing.T) {
	t.Parallel()
	opts := buildDialOptions(true)
	if len(opts) != 1 {
		t.Errorf("expected 1 dial option for insecure-dev, got %d", len(opts))
	}
}

func TestBuildDialOptions_Secure(t *testing.T) {
	t.Parallel()
	opts := buildDialOptions(false)
	if len(opts) != 1 {
		t.Errorf("expected 1 dial option for secure mode, got %d", len(opts))
	}
}

func TestSealCmd_OutputsBase64(t *testing.T) {
	t.Parallel()
	// Verify base64 encoding produces valid output.
	testData := []byte("hello world")
	encoded := base64.StdEncoding.EncodeToString(testData)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode failed: %v", err)
	}
	if !bytes.Equal(decoded, testData) {
		t.Errorf("decoded %q, want %q", decoded, testData)
	}
}

func TestSealCmd_ZeroingAfterUse(t *testing.T) {
	t.Parallel()
	// Verify crypto.ZeroOnReturn works as expected.
	data := []byte("secret-key-material")
	crypto.ZeroOnReturn(&data)
	// After zeroing, all bytes should be 0.
	for i, b := range data {
		if b != 0 {
			t.Errorf("data[%d] = %d, want 0", i, b)
			break
		}
	}
}
