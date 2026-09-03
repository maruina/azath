package main

import (
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/maruina/azath/internal/config"
	"github.com/maruina/azath/internal/testutil"
)

func TestConfigNewDevice_Success(t *testing.T) {
	t.Parallel()
	path := testutil.WriteConfig(t, validConfigYAML)

	out, err := runCmd(t, "config", "new-device", "--name", "synology-nas", "--config", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// stdout must be a single valid UUID.
	gotUUID := strings.TrimSpace(out)
	if _, parseErr := uuid.Parse(gotUUID); parseErr != nil {
		t.Fatalf("output %q is not a valid UUID: %v", gotUUID, parseErr)
	}

	// Re-load the file and verify the device was appended.
	cfg, loadErr := config.Load(path)
	if loadErr != nil {
		t.Fatalf("loading modified config: %v", loadErr)
	}
	if _, err := config.Validate(cfg); err != nil {
		t.Fatalf("modified config is invalid: %v", err)
	}
	var found *config.DeviceConfig
	for i := range cfg.Devices {
		if cfg.Devices[i].Name == "synology-nas" {
			found = &cfg.Devices[i]
			break
		}
	}
	if found == nil {
		t.Fatal("device 'synology-nas' not found in modified config")
	}
	if found.UUID != gotUUID {
		t.Errorf("device uuid = %q, want %q", found.UUID, gotUUID)
	}
}

func TestConfigNewDevice_BootstrapEmptyDevices(t *testing.T) {
	t.Parallel()
	emptyDevicesYAML := `
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
devices: []
gate:
  type: telegram
  telegram:
    authorized_user_id: "123456789"
`
	path := testutil.WriteConfig(t, emptyDevicesYAML)

	out, err := runCmd(t, "config", "new-device", "--name", "synology", "--config", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	gotUUID := strings.TrimSpace(out)
	if _, parseErr := uuid.Parse(gotUUID); parseErr != nil {
		t.Fatalf("output %q is not a valid UUID: %v", gotUUID, parseErr)
	}

	cfg, loadErr := config.Load(path)
	if loadErr != nil {
		t.Fatalf("loading modified config: %v", loadErr)
	}
	if _, err := config.Validate(cfg); err != nil {
		t.Fatalf("modified config is invalid: %v", err)
	}
	if len(cfg.Devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(cfg.Devices))
	}
	if cfg.Devices[0].Name != "synology" {
		t.Errorf("device name = %q, want %q", cfg.Devices[0].Name, "synology")
	}
	if cfg.Devices[0].UUID != gotUUID {
		t.Errorf("device uuid = %q, want %q", cfg.Devices[0].UUID, gotUUID)
	}
}

func TestConfigNewDevice_PreservesComments(t *testing.T) {
	t.Parallel()
	yamlWithComments := `
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
  # enrolled 2024-01-15 for talos-control-plane-1
  - name: talos-node
    uuid: 550e8400-e29b-41d4-a716-446655440000
gate:
  type: telegram
  telegram:
    authorized_user_id: "123456789"
`
	path := testutil.WriteConfig(t, yamlWithComments)
	if _, err := runCmd(t, "config", "new-device", "--name", "synology-nas", "--config", path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content, err := os.ReadFile(path) // #nosec G304 — path comes from t.TempDir()
	if err != nil {
		t.Fatalf("reading modified config: %v", err)
	}
	if !strings.Contains(string(content), "# enrolled 2024-01-15 for talos-control-plane-1") {
		t.Errorf("comment not preserved after new-device:\n%s", content)
	}
}

func TestConfigNewDevice_PreservesExistingDevices(t *testing.T) {
	t.Parallel()
	path := testutil.WriteConfig(t, validConfigYAML)

	if _, err := runCmd(t, "config", "new-device", "--name", "new-node", "--config", path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("loading modified config: %v", err)
	}
	if len(cfg.Devices) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(cfg.Devices))
	}
	if cfg.Devices[0].Name != "talos-node" {
		t.Errorf("first device name = %q, want %q", cfg.Devices[0].Name, "talos-node")
	}
}

func TestConfigNewDevice_EmptyDevicesList(t *testing.T) {
	t.Parallel()
	// appendDeviceNode must handle devices: [] (empty sequence node with nil Content).
	data := []byte(`
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
devices: []
gate:
  type: telegram
  telegram:
    authorized_user_id: "123456789"
`)
	id := "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	result, err := appendDeviceNode(data, "my-node", id)
	if err != nil {
		t.Fatalf("appendDeviceNode: %v", err)
	}
	if !strings.Contains(string(result), "my-node") {
		t.Errorf("result does not contain device name: %s", result)
	}
	if !strings.Contains(string(result), id) {
		t.Errorf("result does not contain device UUID: %s", result)
	}
	// Round-trip: verify the output is a structurally valid config, not just
	// syntactically present text.
	cfg, loadErr := config.LoadFromBytes(result)
	if loadErr != nil {
		t.Fatalf("LoadFromBytes after appendDeviceNode: %v", loadErr)
	}
	if _, err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate after appendDeviceNode: %v", err)
	}
}

func TestConfigNewDevice_MissingName(t *testing.T) {
	t.Parallel()
	path := testutil.WriteConfig(t, validConfigYAML)
	_, err := runCmd(t, "config", "new-device", "--config", path)
	if err == nil {
		t.Fatal("expected error for missing --name, got nil")
	}
}

func TestConfigNewDevice_MissingConfig(t *testing.T) {
	t.Parallel()
	_, err := runCmd(t, "config", "new-device", "--name", "my-node")
	if err == nil {
		t.Fatal("expected error for missing --config, got nil")
	}
}

func TestConfigNewDevice_FileNotFound(t *testing.T) {
	t.Parallel()
	_, err := runCmd(t, "config", "new-device", "--name", "my-node", "--config", "/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestConfigNewDevice_InvalidExistingConfig(t *testing.T) {
	t.Parallel()
	// Valid YAML but missing seal_token_ref and gate — fails Validate.
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
	path := testutil.WriteConfig(t, invalid)
	_, err := runCmd(t, "config", "new-device", "--name", "my-node", "--config", path)
	if err == nil {
		t.Fatal("expected error for invalid existing config, got nil")
	}
}
