package main

import (
	"bytes"
	"strings"
	"testing"

	kms "github.com/siderolabs/kms-client/api/kms"
)

// Compile-time verification that kms proto types are importable and have the expected shape.
var (
	_ kms.KMSServiceServer = (*kms.UnimplementedKMSServiceServer)(nil)
	_ *kms.Request
	_ *kms.Response
)

func TestRootHelp(t *testing.T) {
	t.Parallel()
	cmd := newRootCmd("test", "abc1234")
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("--help returned error: %v", err)
	}
	if out := buf.String(); !strings.Contains(out, "azath") {
		t.Errorf("expected --help output to contain %q, got: %s", "azath", out)
	}
}

func TestVersion(t *testing.T) {
	t.Parallel()
	cmd := newRootCmd("1.2.3", "abc1234")
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--version"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("--version returned error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "1.2.3") {
		t.Errorf("expected --version output to contain %q, got: %s", "1.2.3", out)
	}
	if !strings.Contains(out, "abc1234") {
		t.Errorf("expected --version output to contain commit %q, got: %s", "abc1234", out)
	}
	if !strings.Contains(out, "commit:") {
		t.Errorf("expected --version output to contain %q, got: %s", "commit:", out)
	}
}
