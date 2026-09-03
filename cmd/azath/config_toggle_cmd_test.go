package main

import (
	"os"
	"strings"
	"testing"

	"github.com/maruina/azath/internal/config"
	"github.com/maruina/azath/internal/testutil"
)

func TestConfigDisableDevice_Success(t *testing.T) {
	t.Parallel()
	path := testutil.WriteConfig(t, validConfigYAML)

	out, err := runCmd(t, "config", "disable-device", "--name", "talos-node", "--config", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, `"talos-node" disabled`) {
		t.Errorf("output = %q, want %q", out, `"talos-node" disabled`)
	}

	// Re-load and verify.
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("loading modified config: %v", err)
	}
	if _, err := config.Validate(cfg); err != nil {
		t.Fatalf("modified config is invalid: %v", err)
	}

	for _, d := range cfg.Devices {
		if d.Name == "talos-node" {
			if !d.Disabled {
				t.Error("expected talos-node to be disabled, got enabled")
			}
			return
		}
	}
	t.Fatal("device talos-node not found in modified config")
}

func TestConfigDisableDevice_Idempotent(t *testing.T) {
	t.Parallel()
	path := testutil.WriteConfig(t, validConfigYAML)

	// First call.
	if _, err := runCmd(t, "config", "disable-device", "--name", "talos-node", "--config", path); err != nil {
		t.Fatalf("first disable failed: %v", err)
	}

	// Second call — must not error.
	if _, err := runCmd(t, "config", "disable-device", "--name", "talos-node", "--config", path); err != nil {
		t.Fatalf("second disable (idempotent) failed: %v", err)
	}

	// Should still be disabled.
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	for _, d := range cfg.Devices {
		if d.Name == "talos-node" && !d.Disabled {
			t.Error("expected talos-node to stay disabled after idempotent call")
		}
	}
}

func TestConfigEnableDevice_Success(t *testing.T) {
	t.Parallel()
	// Start with a config where the device is disabled.
	disabledYAML := strings.ReplaceAll(validConfigYAML, "name: talos-node", "name: talos-node\n    disabled: true")
	path := testutil.WriteConfig(t, disabledYAML)

	out, err := runCmd(t, "config", "enable-device", "--name", "talos-node", "--config", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, `"talos-node" enabled`) {
		t.Errorf("output = %q, want %q", out, `"talos-node" enabled`)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("loading modified config: %v", err)
	}
	if _, err := config.Validate(cfg); err != nil {
		t.Fatalf("modified config is invalid: %v", err)
	}

	for _, d := range cfg.Devices {
		if d.Name == "talos-node" {
			if d.Disabled {
				t.Error("expected talos-node to be enabled, got disabled")
			}
			return
		}
	}
	t.Fatal("device talos-node not found in modified config")
}

func TestConfigEnableDevice_RemovesField(t *testing.T) {
	t.Parallel()
	// Start with a device that has disabled: true.
	disabledYAML := `server:
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
    disabled: true
gate:
  type: telegram
  telegram:
    authorized_user_id: "123456789"
`
	path := testutil.WriteConfig(t, disabledYAML)

	if _, err := runCmd(t, "config", "enable-device", "--name", "talos-node", "--config", path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read raw YAML — verify disabled field is removed, not just set to false.
	content, err := os.ReadFile(path) // #nosec G304 — path comes from t.TempDir()
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	if strings.Contains(string(content), "disabled:") {
		t.Errorf("disabled field should be removed from the YAML, got:\n%s", content)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("loading modified config: %v", err)
	}
	validateConf(t, cfg)
	for _, d := range cfg.Devices {
		if d.Name == "talos-node" && d.Disabled {
			t.Error("expected talos-node to be enabled after enable-device")
		}
	}
}

func TestConfigEnableDevice_Idempotent(t *testing.T) {
	t.Parallel()
	path := testutil.WriteConfig(t, validConfigYAML)

	// Call enable on an already-enabled device (no disabled field).
	if _, err := runCmd(t, "config", "enable-device", "--name", "talos-node", "--config", path); err != nil {
		t.Fatalf("enable on already-enabled device failed: %v", err)
	}
}

func TestConfigDisableDevice_UnknownName(t *testing.T) {
	t.Parallel()
	path := testutil.WriteConfig(t, validConfigYAML)
	_, err := runCmd(t, "config", "disable-device", "--name", "nonexistent", "--config", path)
	if err == nil {
		t.Fatal("expected error for unknown device name, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err.Error() = %q, want substring %q", err.Error(), "not found")
	}
}

func TestConfigEnableDevice_UnknownName(t *testing.T) {
	t.Parallel()
	path := testutil.WriteConfig(t, validConfigYAML)
	_, err := runCmd(t, "config", "enable-device", "--name", "nonexistent", "--config", path)
	if err == nil {
		t.Fatal("expected error for unknown device name, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err.Error() = %q, want substring %q", err.Error(), "not found")
	}
}

func TestConfigToggleDevice_MissingName(t *testing.T) {
	t.Parallel()
	path := testutil.WriteConfig(t, validConfigYAML)
	_, err := runCmd(t, "config", "disable-device", "--config", path)
	if err == nil {
		t.Fatal("expected error for missing --name, got nil")
	}
}

func TestConfigToggleDevice_MissingConfig(t *testing.T) {
	t.Parallel()
	_, err := runCmd(t, "config", "disable-device", "--name", "talos-node")
	if err == nil {
		t.Fatal("expected error for missing --config, got nil")
	}
}

func TestConfigToggleDevice_FileNotFound(t *testing.T) {
	t.Parallel()
	_, err := runCmd(t, "config", "disable-device", "--name", "talos-node", "--config", "/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestConfigToggleDevice_PreservesComments(t *testing.T) {
	t.Parallel()
	yamlWithComments := `server:
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
  # enrolled 2024-01-15 for talos-control-plane-1
  - name: talos-node
    uuid: 550e8400-e29b-41d4-a716-446655440000
gate:
  type: telegram
  telegram:
    authorized_user_id: "123456789"
`
	path := testutil.WriteConfig(t, yamlWithComments)

	if _, err := runCmd(t, "config", "disable-device", "--name", "talos-node", "--config", path); err != nil {
		t.Fatalf("disable-device: %v", err)
	}

	content, err := os.ReadFile(path) // #nosec G304 — path comes from t.TempDir()
	if err != nil {
		t.Fatalf("reading modified config: %v", err)
	}
	if !strings.Contains(string(content), "# enrolled 2024-01-15 for talos-control-plane-1") {
		t.Errorf("comment not preserved after disable-device:\n%s", content)
	}
}

func TestConfigToggleDevice_ToggleEnableRestores(t *testing.T) {
	t.Parallel()
	path := testutil.WriteConfig(t, validConfigYAML)

	// Disable.
	if _, err := runCmd(t, "config", "disable-device", "--name", "talos-node", "--config", path); err != nil {
		t.Fatalf("disable: %v", err)
	}
	validateConfigFile(t, path, func(cfg *config.Config) {
		for _, d := range cfg.Devices {
			if d.Name == "talos-node" && !d.Disabled {
				t.Error("expected talos-node to be disabled after disable-device")
			}
		}
	})

	// Enable again.
	if _, err := runCmd(t, "config", "enable-device", "--name", "talos-node", "--config", path); err != nil {
		t.Fatalf("enable: %v", err)
	}
	validateConfigFile(t, path, func(cfg *config.Config) {
		for _, d := range cfg.Devices {
			if d.Name == "talos-node" && d.Disabled {
				t.Error("expected talos-node to be enabled after enable-device")
			}
		}
	})
}

func TestConfigToggleDevice_InvalidExistingConfig(t *testing.T) {
	t.Parallel()
	invalid := `server:
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
	path := testutil.WriteConfig(t, invalid)
	_, err := runCmd(t, "config", "disable-device", "--name", "talos-node", "--config", path)
	if err == nil {
		t.Fatal("expected error for invalid existing config, got nil")
	}
}

// validateConfigFile loads and validates a config file, then runs check.
func validateConfigFile(t *testing.T, path string, check func(*config.Config)) {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	validateConf(t, cfg)
	check(cfg)
}

// validateConf panics on invalid config. Used within test helpers.
func validateConf(t *testing.T, cfg *config.Config) {
	t.Helper()
	if _, err := config.Validate(cfg); err != nil {
		t.Fatalf("config invalid: %v", err)
	}
}
