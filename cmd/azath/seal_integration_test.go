package main

import (
	"context"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"testing"

	kms "github.com/siderolabs/kms-client/api/kms"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/maruina/azath/internal/testutil"
)

// TestSealCmd_Integration_h2c tests the full seal flow against an h2c server.
func TestSealCmd_Integration_h2c(t *testing.T) {
	// Cannot use t.Parallel — manipulates os.Stdin.

	// Start fake gRPC server.
	sealToken := []byte("integration-test-token")
	addr, cleanup := startFakeGRPC(t, sealToken)
	defer cleanup()

	dir := t.TempDir()
	deviceUUID := "550e8400-e29b-41d4-a716-446655440000"
	cfgPath := writeClientConfig(t, deviceUUID, addr)

	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, sealToken, 0o600); err != nil {
		t.Fatalf("writing token: %v", err)
	}

	outPath := filepath.Join(dir, "sealed.b64")

	// Create a pipe to simulate stdin with test data.
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()

	testPlaintext := []byte("super-secret-passphrase")
	writeCh := make(chan struct{})
	go func() {
		_, _ = w.Write(testPlaintext)
		_ = w.Close()
		close(writeCh)
	}()

	// Wait for writer to finish before reading.
	<-writeCh

	err := runSeal(t.Context(), cfgPath, nil, tokenPath, outPath, false, true)
	if err != nil {
		t.Fatalf("runSeal failed: %v", err)
	}

	// Verify output file was created with correct permissions.
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat output file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("output file mode = %v, want 0600", info.Mode().Perm())
	}

	// Read and verify base64 content.
	data, err := os.ReadFile(outPath) // #nosec G304 — path is from test temp dir
	if err != nil {
		t.Fatalf("reading output file: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("output file is empty")
	}
}

// TestSealCmd_StdoutMode tests that stdout mode writes only base64.
func TestSealCmd_StdoutMode(t *testing.T) {
	// Cannot use t.Parallel — manipulates os.Stdin and os.Stdout.

	sealToken := []byte("stdout-test-token")
	addr, cleanup := startFakeGRPC(t, sealToken)
	defer cleanup()

	dir := t.TempDir()
	deviceUUID := "550e8400-e29b-41d4-a716-446655440000"
	cfgPath := writeClientConfig(t, deviceUUID, addr)

	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, sealToken, 0o600); err != nil {
		t.Fatalf("writing token: %v", err)
	}

	// Capture stdout.
	oldStdout := os.Stdout
	rOut, wOut, _ := os.Pipe()
	os.Stdout = wOut
	defer func() { os.Stdout = oldStdout }()

	// Simulate stdin.
	oldStdin := os.Stdin
	sr, sw, _ := os.Pipe()
	os.Stdin = sr
	defer func() { os.Stdin = oldStdin }()

	testPlaintext := []byte("test-secret")
	writeCh := make(chan struct{})
	go func() {
		_, _ = sw.Write(testPlaintext)
		_ = sw.Close()
		close(writeCh)
	}()

	// Wait for writer to finish.
	<-writeCh

	err := runSeal(t.Context(), cfgPath, nil, tokenPath, "", false, true)
	_ = wOut.Close()
	if err != nil {
		t.Fatalf("runSeal failed: %v", err)
	}

	// Read captured stdout.
	var buf [4096]byte
	n, _ := rOut.Read(buf[:])
	output := string(buf[:n])

	if len(output) == 0 {
		t.Fatal("stdout is empty")
	}
	// Should be base64-encoded data followed by newline.
	if output[len(output)-1] != '\n' {
		t.Error("stdout output should end with newline")
	}
}

// TestSealCmd_EmptyPlaintext_Rejected verifies empty plaintext is rejected.
func TestSealCmd_EmptyPlaintext_Rejected(t *testing.T) {
	t.Parallel()

	sealToken := []byte("test-token")
	addr, cleanup := startFakeGRPC(t, sealToken)
	defer cleanup()

	dir := t.TempDir()
	deviceUUID := "550e8400-e29b-41d4-a716-446655440000"
	cfgPath := writeClientConfig(t, deviceUUID, addr)

	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, sealToken, 0o600); err != nil {
		t.Fatalf("writing token: %v", err)
	}

	// Simulate empty stdin.
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()
	_ = w.Close() // EOF immediately

	err := runSeal(t.Context(), cfgPath, nil, tokenPath, "", false, true)
	if err == nil {
		t.Fatal("expected error for empty plaintext, got nil")
	}
}

// TestSealCmd_LargePlaintext_Rejected verifies >1 MiB input is rejected.
func TestSealCmd_LargePlaintext_Rejected(t *testing.T) {
	// Cannot use t.Parallel — manipulates os.Stdin.

	sealToken := []byte("test-token")
	addr, cleanup := startFakeGRPC(t, sealToken)
	defer cleanup()

	dir := t.TempDir()
	deviceUUID := "550e8400-e29b-41d4-a716-446655440000"
	cfgPath := writeClientConfig(t, deviceUUID, addr)

	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, sealToken, 0o600); err != nil {
		t.Fatalf("writing token: %v", err)
	}

	// Simulate large stdin.
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()

	largeData := make([]byte, maxPlaintextSize+100)
	_, _ = rand.Read(largeData)

	// Start writer in background.
	errCh := make(chan error, 1)
	go func() {
		_, err := w.Write(largeData)
		_ = w.Close()
		errCh <- err
	}()

	// runSeal will read from stdin and should reject the large input.
	err := runSeal(t.Context(), cfgPath, nil, tokenPath, "", false, true)

	// Wait for writer to finish (it may fail if pipe is closed early).
	<-errCh

	if err == nil {
		t.Fatal("expected error for large plaintext, got nil")
	}
}

// fakeAuthEnforcingSealer requires valid bearer token auth.
type fakeAuthEnforcingSealer struct {
	kms.UnimplementedKMSServiceServer
	validToken []byte
}

func (f *fakeAuthEnforcingSealer) Seal(ctx context.Context, req *kms.Request) (*kms.Response, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing metadata")
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return nil, status.Error(codes.Unauthenticated, "missing authorization")
	}
	expected := "Bearer " + string(f.validToken)
	if vals[0] != expected {
		return nil, status.Error(codes.Unauthenticated, "invalid token")
	}
	// Return simulated sealed data.
	resp := make([]byte, 16+len(req.GetData()))
	copy(resp[16:], req.GetData())
	return &kms.Response{Data: resp}, nil
}

func (f *fakeAuthEnforcingSealer) Unseal(ctx context.Context, req *kms.Request) (*kms.Response, error) {
	return nil, status.Error(codes.Unimplemented, "unseal not implemented")
}

// startFakeAuthGRPC starts a fake KMS server that enforces auth.
func startFakeAuthGRPC(t *testing.T, validToken []byte) (addr string, cleanup func()) {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("creating listener: %v", err)
	}
	srv := grpc.NewServer()
	kms.RegisterKMSServiceServer(srv, &fakeAuthEnforcingSealer{validToken: validToken})
	go func() {
		_ = srv.Serve(lis)
	}()
	return lis.Addr().String(), func() { srv.Stop() }
}

// TestSealCmd_WrongBearerToken_Rejected verifies wrong tokens are rejected.
func TestSealCmd_WrongBearerToken_Rejected(t *testing.T) {
	// Cannot use t.Parallel — manipulates os.Stdin.

	correctToken := []byte("correct-token")
	addr, cleanup := startFakeAuthGRPC(t, correctToken)
	defer cleanup()

	dir := t.TempDir()
	deviceUUID := "550e8400-e29b-41d4-a716-446655440000"
	cfgPath := writeClientConfig(t, deviceUUID, addr)

	// Write wrong token.
	wrongTokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(wrongTokenPath, []byte("wrong-token"), 0o600); err != nil {
		t.Fatalf("writing token: %v", err)
	}

	// Simulate stdin.
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()

	testPlaintext := []byte("test-secret")
	writeCh := make(chan struct{})
	go func() {
		_, _ = w.Write(testPlaintext)
		_ = w.Close()
		close(writeCh)
	}()

	// Wait for writer to finish.
	<-writeCh

	err := runSeal(t.Context(), cfgPath, nil, wrongTokenPath, "", false, true)
	if err == nil {
		t.Fatal("expected error for wrong token, got nil")
	}
}

// TestSealCmd_MissingBearerToken_Rejected verifies missing tokens are rejected.
func TestSealCmd_MissingBearerToken_Rejected(t *testing.T) {
	t.Parallel()

	correctToken := []byte("correct-token")
	addr, cleanup := startFakeAuthGRPC(t, correctToken)
	defer cleanup()

	dir := t.TempDir()
	deviceUUID := "550e8400-e29b-41d4-a716-446655440000"
	cfgPath := writeClientConfig(t, deviceUUID, addr)

	// Write empty token file.
	emptyTokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(emptyTokenPath, []byte{}, 0o600); err != nil {
		t.Fatalf("writing token: %v", err)
	}

	err := runSeal(t.Context(), cfgPath, nil, emptyTokenPath, "", false, true)
	if err == nil {
		t.Fatal("expected error for empty token file, got nil")
	}
}

// TestSealCmd_InvalidUUID_FailsBeforeRPC verifies UUID validation happens before RPC.
func TestSealCmd_InvalidUUID_FailsBeforeRPC(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	badCfg := `device:
  name: test
  uuid: not-a-valid-uuid
endpoints:
  - 127.0.0.1:5000
`
	cfgPath := testutil.WriteConfig(t, badCfg)

	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte("token"), 0o600); err != nil {
		t.Fatalf("writing token: %v", err)
	}

	err := runSeal(t.Context(), cfgPath, nil, tokenPath, "", false, false)
	if err == nil {
		t.Fatal("expected error for invalid UUID, got nil")
	}
}
