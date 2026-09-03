package config_test

import (
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/maruina/azath/internal/config"
	"github.com/maruina/azath/internal/testutil"
)

const minimalYAML = `
server:
  name: test-server
  listen: "127.0.0.1:7800"
  seal_token_ref: "op://vault/azath/seal-token"
master_key:
  source: file
  path: /etc/azath/master.key
registry:
  path: /etc/azath/registry.json
devices:
  - name: talos-node
    uuid: 550e8400-e29b-41d4-a716-446655440000
gate:
  type: telegram
  telegram:
    authorized_user_id: "987654321"
notifications:
  telegram:
    bot_token_ref: "op://vault/azath/bot-token"
    chat_id: "123456789"
`

const fullYAML = `
server:
  name: test-server
  listen: "127.0.0.1:7800"
  metrics_listen: "127.0.0.1:9090"
  log_level: debug
  log_format: text
  seal_token_ref: "op://vault/azath/seal-token"
master_key:
  source: file
  path: /etc/azath/master.key
registry:
  path: /etc/azath/registry.json
notifications:
  telegram:
    bot_token_ref: "op://vault/azath/bot-token"
    chat_id: "123456789"
    rate_limit: 5m
    approval_ttl: 10m
devices:
  - name: talos-node
    uuid: 550e8400-e29b-41d4-a716-446655440000
  - name: synology
    uuid: 6ba7b810-9dad-11d1-80b4-00c04fd430c8
    disabled: true
gate:
  type: telegram
  telegram:
    authorized_user_id: "987654321"
    approval_cache_ttl: 2m
client:
  device:
    name: synology
    uuid: 6ba7b810-9dad-11d1-80b4-00c04fd430c8
  endpoints:
    - "https://lan.azath.internal"
`

func TestLoad_ValidMinimal(t *testing.T) {
	t.Parallel()
	cfg, err := config.Load(testutil.WriteConfig(t, minimalYAML))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Server.Listen != "127.0.0.1:7800" {
		t.Errorf("server.listen = %q, want %q", cfg.Server.Listen, "127.0.0.1:7800")
	}
	if cfg.MasterKey.Source != config.MasterKeySourceFile {
		t.Errorf("master_key.source = %q, want %q", cfg.MasterKey.Source, config.MasterKeySourceFile)
	}
	if cfg.Registry.Path != "/etc/azath/registry.json" {
		t.Errorf("registry.path = %q, want %q", cfg.Registry.Path, "/etc/azath/registry.json")
	}
	if len(cfg.Devices) != 1 {
		t.Fatalf("len(devices) = %d, want 1", len(cfg.Devices))
	}
}

func TestLoad_ValidFull(t *testing.T) {
	t.Parallel()
	cfg, err := config.Load(testutil.WriteConfig(t, fullYAML))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Server.LogLevel != "debug" {
		t.Errorf("server.log_level = %q, want debug", cfg.Server.LogLevel)
	}
	if cfg.Notifications.Telegram == nil {
		t.Fatal("notifications.telegram is nil")
	}
	if cfg.Notifications.Telegram.RateLimit != "5m" {
		t.Errorf("rate_limit = %q, want 5m", cfg.Notifications.Telegram.RateLimit)
	}
	if cfg.Gate == nil || cfg.Gate.Type != config.GateTypeTelegram || cfg.Gate.Telegram == nil {
		t.Fatalf("telegram gate not loaded: %+v", cfg.Gate)
	}
	if cfg.Devices[1].Disabled != true {
		t.Fatal("disabled device field not loaded")
	}
	if cfg.Client == nil {
		t.Fatal("client is nil")
	}
	if cfg.Client.Device.Name != "synology" {
		t.Errorf("client.device.name = %q, want synology", cfg.Client.Device.Name)
	}
}

func TestLoad_AppliesDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := config.Load(testutil.WriteConfig(t, minimalYAML))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Server.MetricsListen != "127.0.0.1:9090" {
		t.Errorf("metrics_listen default = %q, want 127.0.0.1:9090", cfg.Server.MetricsListen)
	}
	if cfg.Server.LogLevel != "info" {
		t.Errorf("log_level default = %q, want info", cfg.Server.LogLevel)
	}
	if cfg.Server.LogFormat != "json" {
		t.Errorf("log_format default = %q, want json", cfg.Server.LogFormat)
	}
	if cfg.Notifications.Telegram.RateLimit != "5m" {
		t.Errorf("telegram rate_limit default = %q, want 5m", cfg.Notifications.Telegram.RateLimit)
	}
	if cfg.Notifications.Telegram.ApprovalTTL != "10m" {
		t.Errorf("telegram approval_ttl default = %q, want 10m", cfg.Notifications.Telegram.ApprovalTTL)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	t.Parallel()
	_, err := config.Load("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected os.ErrNotExist, got: %v", err)
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	t.Parallel()
	_, err := config.Load(testutil.WriteConfig(t, "{{not valid yaml{{"))
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

func TestLoad_UnknownField(t *testing.T) {
	t.Parallel()
	yaml := `
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
	_, err := config.Load(testutil.WriteConfig(t, yaml))
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
}

func TestLoad_MultiDocument(t *testing.T) {
	t.Parallel()
	multiDoc := minimalYAML + "\n---\nserver:\n  listen: \":9999\"\n"
	_, err := config.Load(testutil.WriteConfig(t, multiDoc))
	if err == nil {
		t.Fatal("expected error for multi-document YAML, got nil")
	}
}

func TestSafeAttrs_NoSensitiveValues(t *testing.T) {
	t.Parallel()
	cfg, err := config.Load(testutil.WriteConfig(t, fullYAML))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	buf := &strings.Builder{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	attrs := config.SafeAttrs(cfg)
	logArgs := make([]any, len(attrs))
	for i, a := range attrs {
		logArgs[i] = a
	}
	logger.Info("config loaded", logArgs...)

	output := buf.String()
	for _, v := range []string{
		"op://vault/azath/seal-token",
		"op://vault/azath/bot-token",
		"127.0.0.1:7800",
		"127.0.0.1:9090",
	} {
		if strings.Contains(output, v) {
			t.Errorf("log output contains sensitive value %q", v)
		}
	}
	for _, want := range []string{"gate_configured", "master_key.source"} {
		if !strings.Contains(output, want) {
			t.Errorf("expected %s in log output, got: %s", want, output)
		}
	}
}
