package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http/httptest"
	"testing"
	"time"

	kms "github.com/siderolabs/kms-client/api/kms"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func TestEndpointTargetTLS(t *testing.T) {
	t.Parallel()
	server := grpc.NewServer()
	kms.RegisterKMSServiceServer(server, &fakeUnsealer{plaintext: []byte("test-passphrase")})
	defer server.Stop()
	https := httptest.NewUnstartedServer(server)
	https.EnableHTTP2 = true
	https.StartTLS()
	defer https.Close()
	roots := x509.NewCertPool()
	roots.AddCert(https.Certificate())
	target, err := endpointTarget(https.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13})))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	resp, err := kms.NewKMSServiceClient(conn).Unseal(ctx, &kms.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Data) != "test-passphrase" {
		t.Fatal("unexpected unseal response")
	}
}
