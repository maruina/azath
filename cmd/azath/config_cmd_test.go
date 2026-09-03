package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/maruina/azath/internal/testutil"
)

// validConfigYAML is a minimal valid config with telegram gate.
// authorized_user_id lives in gate.telegram (gate-specific settings).
const validConfigYAML = `
server:
  listen: "127.0.0.1:7800"
  seal_token_ref: "op://vault/azath/seal-token"
  name: test-server
master_key:
  source: file
  path: /etc/azath/master.key
registry:
  path: /etc/azath/registry.json
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
`

func runCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd("test", "abc")
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

func TestConfigValidate_Valid(t *testing.T) {
	t.Parallel()
	out, err := runCmd(t, "config", "validate", testutil.WriteConfig(t, validConfigYAML))
	if err != nil {
		t.Fatalf("expected nil error for valid config, got: %v", err)
	}
	if !strings.Contains(out, "config is valid") {
		t.Errorf("expected 'config is valid' in output, got: %s", out)
	}
}

func TestConfigValidate_Invalid(t *testing.T) {
	t.Parallel()
	// Missing seal_token_ref.
	invalid := `
server:
  listen: "127.0.0.1:7800"
master_key:
  source: file
  path: /etc/azath/master.key
registry:
  path: /etc/azath/registry.json
devices:
  - name: talos-node
    uuid: 550e8400-e29b-41d4-a716-446655440000
`
	_, err := runCmd(t, "config", "validate", testutil.WriteConfig(t, invalid))
	if err == nil {
		t.Fatal("expected error for invalid config, got nil")
	}
	if !strings.Contains(err.Error(), "seal_token_ref") {
		t.Errorf("err.Error() = %q, want substring %q", err.Error(), "seal_token_ref")
	}
}

func TestConfigValidate_MultipleErrors(t *testing.T) {
	t.Parallel()
	// Several required fields missing — each should appear in the error.
	empty := `
server: {}
master_key:
  source: file
registry: {}
devices:
  - name: talos-node
    uuid: 550e8400-e29b-41d4-a716-446655440000
`
	_, err := runCmd(t, "config", "validate", testutil.WriteConfig(t, empty))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	for _, want := range []string{"server.listen", "server.seal_token_ref", "master_key.path", "registry.path"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected %q in err.Error(), got: %s", want, err.Error())
		}
	}
}

func TestConfigValidate_UnknownField(t *testing.T) {
	t.Parallel()
	typo := `
server:
  listen: "127.0.0.1:7800"
  seal_toke_ref: "op://vault/azath/seal-token"
master_key:
  source: file
  path: /etc/azath/master.key
registry:
  path: /etc/azath/registry.json
devices:
  - name: talos-node
    uuid: 550e8400-e29b-41d4-a716-446655440000
`
	_, err := runCmd(t, "config", "validate", testutil.WriteConfig(t, typo))
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
}

func TestConfigValidate_MissingFile(t *testing.T) {
	t.Parallel()
	_, err := runCmd(t, "config", "validate", "/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestConfigValidate_NoArgs(t *testing.T) {
	t.Parallel()
	_, err := runCmd(t, "config", "validate")
	if err == nil {
		t.Fatal("expected error when no path provided, got nil")
	}
}

func TestConfigInRootHelp(t *testing.T) {
	t.Parallel()
	out, err := runCmd(t, "--help")
	if err != nil {
		t.Fatalf("--help returned error: %v", err)
	}
	if !strings.Contains(out, "config") {
		t.Errorf("expected 'config' in --help output, got: %s", out)
	}
}
