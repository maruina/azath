package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestSynologyCommandOutcomes(t *testing.T) {
	t.Parallel()
	addr, closeServer := startFakeUnsealer(t, []byte("private-passphrase"), false)
	defer closeServer()
	blob := writeSealedBlob(t, bytes.Repeat([]byte{1}, 40))
	cfg := writeClientConfigWithEndpoints(t, e2eDeviceUUID, addr)
	f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0) // #nosec G304 -- test-owned config
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(f, "shares:\n  - {name: homes, sealed_blob: %q}\n  - {name: media, sealed_blob: %q}\n  - {name: backup, sealed_blob: %q}\n", blob, blob, blob)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	var mounted []string
	var output bytes.Buffer
	cmd := newSynologyCmdWithMount(func(_ context.Context, name string, secret []byte) error {
		mounted = append(mounted, name)
		if string(secret) != "private-passphrase" {
			t.Error("wrong secret")
		}
		if name == "media" {
			return errors.New("private-passphrase subprocess diagnostics")
		}
		return nil
	})
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"--config", cfg, "--insecure-dev"})
	if cmd.ExecuteContext(t.Context()) == nil {
		t.Fatal("partial failure returned success")
	}
	if strings.Join(mounted, ",") != "homes,media,backup" {
		t.Fatalf("mounts: %v", mounted)
	}
	if strings.Contains(output.String(), "private-passphrase") {
		t.Fatal("secret exposed")
	}
	for _, name := range mounted {
		if !strings.Contains(output.String(), name) {
			t.Errorf("missing result: %s", name)
		}
	}
}

func TestSynologyRejectsArgumentsAndCancellation(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"--config", "unused", "--", "sh"}, {"--config", "unused", "--executable", "sh"}} {
		cmd := newSynologyCmdWithMount(func(context.Context, string, []byte) error { t.Error("mounted"); return nil })
		cmd.SetArgs(args)
		if cmd.ExecuteContext(t.Context()) == nil {
			t.Error("accepted arbitrary command")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cmd := newSynologyCmdWithMount(func(context.Context, string, []byte) error { t.Error("mounted after cancellation"); return nil })
	cmd.SetArgs([]string{"--config", "unused"})
	if cmd.ExecuteContext(ctx) == nil {
		t.Fatal("cancellation succeeded")
	}
}
