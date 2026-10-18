package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	kms "github.com/siderolabs/kms-client/api/kms"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
