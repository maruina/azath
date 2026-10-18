package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maruina/azath/internal/testutil"
)

func TestServeReturnsMetricsBindErrorAfterCleanup(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "master.key")
	testutil.WriteKeyFile(t, keyPath)
	occupied, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = occupied.Close() }()
	grpcAddr := "127.0.0.1:" + freePort(t)
	configPath := writeDevConfig(t, keyPath, grpcAddr, occupied.Addr().String())
	t.Setenv("AZATH_BOT_TOKEN", "disposable-bot-token")
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer telegram.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err = runServe(ctx, configPath, true, telegram.URL)
	if err == nil || !strings.Contains(err.Error(), "metrics/health server") {
		t.Fatalf("got %v, want metrics bind error", err)
	}
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", grpcAddr)
	if err != nil {
		t.Fatalf("gRPC listener was not closed: %v", err)
	}
	_ = listener.Close()
}
