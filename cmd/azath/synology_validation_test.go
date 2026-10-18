package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/maruina/azath/internal/testutil"
)

func TestSharedClientConfigRejectsInvalidInputs(t *testing.T) {
	t.Parallel()
	base := "device:\n  name: synology\n  uuid: " + e2eDeviceUUID + "\nendpoints:\n  - https://localhost:443\n"
	for _, suffix := range []string{"---\n{}\n", "shares:\n - {name: homes, sealed_blob: /a}\n - {name: homes, sealed_blob: /b}\n", "shares:\n - {name: '../evil', sealed_blob: /a}\n", "unknown: true\n", "shares:\n - {name: homes}\n", strings.Repeat("#", 65537)} {
		t.Run(fmt.Sprintf("case-%d", len(suffix)), func(t *testing.T) {
			t.Parallel()
			cfg := testutil.WriteConfig(t, base+suffix)
			if _, err := loadClientConfig(cfg); err == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
	for _, change := range []string{strings.Replace(base, e2eDeviceUUID, "bad", 1), strings.Replace(base, "https://localhost:443", "ftp://localhost", 1)} {
		if _, err := loadClientConfig(testutil.WriteConfig(t, change)); err == nil {
			t.Fatal("accepted invalid identity/endpoint")
		}
	}
}

func TestSynologyFailoverAndNoMountRetry(t *testing.T) {
	t.Parallel()
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprint(empty), func(t *testing.T) {
			t.Parallel()
			bad, closeBad := startFakeUnsealer(t, nil, !empty)
			defer closeBad()
			good, closeGood := startFakeUnsealer(t, []byte("secret"), false)
			defer closeGood()
			cfg := writeClientConfigWithEndpoints(t, e2eDeviceUUID, bad, good, good)
			blob := writeSealedBlob(t, bytes.Repeat([]byte{1}, 40))
			appendShares(t, cfg, blob)
			mounts := 0
			cmd := newSynologyCmdWithMount(func(context.Context, string, []byte) error { mounts++; return fmt.Errorf("secret") })
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"--config", cfg, "--insecure-dev"})
			if cmd.ExecuteContext(t.Context()) == nil || mounts != 2 {
				t.Fatalf("expected one failed mount per share, got %d", mounts)
			}
		})
	}
}

func appendShares(t *testing.T, cfg, blob string) {
	t.Helper()
	f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0) // #nosec G304 -- test-owned config
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(f, "shares:\n - {name: homes, sealed_blob: %q}\n - {name: media, sealed_blob: %q}\n", blob, blob)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSynologyCancellationStopsLaterMounts(t *testing.T) {
	t.Parallel()
	addr, closeServer := startFakeUnsealer(t, []byte("secret"), false)
	defer closeServer()
	cfg := writeClientConfigWithEndpoints(t, e2eDeviceUUID, addr)
	appendShares(t, cfg, writeSealedBlob(t, bytes.Repeat([]byte{1}, 40)))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	mounts := 0
	cmd := newSynologyCmdWithMount(func(ctx context.Context, _ string, _ []byte) error {
		mounts++
		if _, ok := ctx.Deadline(); !ok {
			t.Error("mount not bounded")
		}
		cancel()
		return nil
	})
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"--config", cfg, "--insecure-dev"})
	if cmd.ExecuteContext(ctx) == nil || mounts != 1 {
		t.Fatalf("canceled mounts: %d", mounts)
	}
}

func TestSynologyInvalidBlobAndNUL(t *testing.T) {
	t.Parallel()
	for _, secret := range [][]byte{[]byte("a\x00b"), bytes.Repeat([]byte{1}, 4097)} {
		addr, closeServer := startFakeUnsealer(t, secret, false)
		cfg := writeClientConfigWithEndpoints(t, e2eDeviceUUID, addr)
		appendShares(t, cfg, writeSealedBlob(t, bytes.Repeat([]byte{1}, 40)))
		cmd := newSynologyCmdWithMount(func(context.Context, string, []byte) error { t.Error("invalid secret mounted"); return nil })
		cmd.SetOut(io.Discard)
		cmd.SetArgs([]string{"--config", cfg, "--insecure-dev"})
		if cmd.ExecuteContext(t.Context()) == nil {
			t.Fatal("accepted invalid secret")
		}
		closeServer()
	}
	for _, data := range [][]byte{[]byte("short"), bytes.Repeat([]byte{1}, 20000)} {
		if _, err := readBlob(writeSealedBlob(t, data)); err == nil {
			t.Fatal("accepted invalid blob")
		}
	}
}

func TestTryUnsealDeadline(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	if _, err := tryUnseal(ctx, "127.0.0.1:1", e2eDeviceUUID, []byte("blob"), time.Minute, true); err == nil {
		t.Fatal("unreachable endpoint succeeded")
	}
}
