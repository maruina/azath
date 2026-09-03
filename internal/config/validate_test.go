package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/maruina/azath/internal/config"
)

func validConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Listen:        "127.0.0.1:7800",
			MetricsListen: "127.0.0.1:9090",
			LogLevel:      "info",
			LogFormat:     "json",
			SealTokenRef:  "op://vault/azath/seal-token",
			Name:          "test-server",
		},
		MasterKey: config.MasterKeyConfig{
			Source: config.MasterKeySourceFile,
			Path:   "/etc/azath/master.key",
		},
		Registry: config.RegistryConfig{Path: "/etc/azath/registry.json"},
		Notifications: config.NotifyConfig{
			Telegram: &config.TelegramConfig{BotTokenRef: "op://vault/azath/bot-token", ChatID: "123456789"},
		},
		Devices: []config.DeviceConfig{{Name: "talos-node", UUID: "550e8400-e29b-41d4-a716-446655440000"}},
		Gate: &config.GateConfig{
			Type:     config.GateTypeTelegram,
			Telegram: &config.TelegramGateConfig{AuthorizedUserID: "987654321"},
		},
	}
}

func TestValidate_Valid(t *testing.T) {
	t.Parallel()
	if _, err := config.Validate(validConfig()); err != nil {
		t.Fatalf("expected nil error for valid config, got: %v", err)
	}
}

func TestValidate_EmptyDevicesAllowed(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.Devices = nil
	if _, err := config.Validate(cfg); err != nil {
		t.Fatalf("expected nil error for empty devices list, got: %v", err)
	}
}

func TestValidate_RequiredFields(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mutate  func(*config.Config)
		wantMsg string
	}{
		{name: "missing server.listen", mutate: func(c *config.Config) { c.Server.Listen = "" }, wantMsg: "server.listen is required"},
		{name: "missing seal_token_ref", mutate: func(c *config.Config) { c.Server.SealTokenRef = "" }, wantMsg: "server.seal_token_ref is required"},
		{name: "missing master_key.source", mutate: func(c *config.Config) { c.MasterKey.Source = "" }, wantMsg: "master_key.source must be: file"},
		{name: "unsupported master_key.source", mutate: func(c *config.Config) { c.MasterKey.Source = "1password" }, wantMsg: "master_key.source must be: file"},
		{name: "missing master_key.path", mutate: func(c *config.Config) { c.MasterKey.Path = "" }, wantMsg: `master_key.path is required when source is "file"`},
		{name: "missing registry.path", mutate: func(c *config.Config) { c.Registry.Path = "" }, wantMsg: "registry.path is required"},
		{name: "missing device.uuid", mutate: func(c *config.Config) { c.Devices[0].UUID = "" }, wantMsg: `devices[0].uuid "" is not a valid UUID`},
		{name: "device.uuid invalid", mutate: func(c *config.Config) { c.Devices[0].UUID = "not-a-uuid" }, wantMsg: `devices[0].uuid "not-a-uuid" is not a valid UUID`},
		{name: "missing device.name", mutate: func(c *config.Config) { c.Devices[0].Name = "" }, wantMsg: `devices[0].name is required`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			tc.mutate(cfg)
			_, err := config.Validate(cfg)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantMsg)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

func TestValidate_UUID_Invalid(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, uuid string }{
		{"forward slash", "etc/passwd"},
		{"backslash", `win\path`},
		{"too long", strings.Repeat("a", 65)},
		{"random string", "not-a-uuid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			cfg.Devices[0].UUID = tc.uuid
			_, err := config.Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), "is not a valid UUID") {
				t.Fatalf("Validate uuid %q error = %v, want invalid UUID", tc.uuid, err)
			}
		})
	}
}

func TestValidate_InvalidEnums(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mutate  func(*config.Config)
		wantMsg string
	}{
		{name: "invalid master_key.source", mutate: func(c *config.Config) { c.MasterKey.Source = "invalid" }, wantMsg: "master_key.source must be: file"},
		{name: "invalid log_level", mutate: func(c *config.Config) { c.Server.LogLevel = "verbose" }, wantMsg: "server.log_level must be one of: debug, info, warn, error"},
		{name: "invalid log_format", mutate: func(c *config.Config) { c.Server.LogFormat = "csv" }, wantMsg: `server.log_format must be one of: json, text`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			tc.mutate(cfg)
			_, err := config.Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("Validate error = %v, want %q", err, tc.wantMsg)
			}
		})
	}
}

func TestValidate_OpRefPrefix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mutate  func(*config.Config)
		wantMsg string
	}{
		{name: "seal_token_ref bad prefix", mutate: func(c *config.Config) { c.Server.SealTokenRef = "vault/azath/seal-token" }, wantMsg: `server.seal_token_ref must start with "op://"`},
		{name: "telegram.bot_token_ref bad prefix", mutate: func(c *config.Config) { c.Notifications.Telegram.BotTokenRef = "not-op" }, wantMsg: `notifications.telegram.bot_token_ref must start with "op://"`},
		{name: "seal_token_ref op:// with no path", mutate: func(c *config.Config) { c.Server.SealTokenRef = "op://" }, wantMsg: `server.seal_token_ref must have the format op://<vault>/<item>/<field>`},
		{name: "seal_token_ref op:// with one segment", mutate: func(c *config.Config) { c.Server.SealTokenRef = "op://vault" }, wantMsg: `server.seal_token_ref must have the format op://<vault>/<item>/<field>`},
		{name: "seal_token_ref op:// with two segments", mutate: func(c *config.Config) { c.Server.SealTokenRef = "op://vault/item" }, wantMsg: `server.seal_token_ref must have the format op://<vault>/<item>/<field>`},
		{name: "seal_token_ref field whitespace", mutate: func(c *config.Config) { c.Server.SealTokenRef = "op://vault/item/   " }, wantMsg: `server.seal_token_ref must have the format op://<vault>/<item>/<field>`},
		{name: "seal_token_ref extra segment", mutate: func(c *config.Config) { c.Server.SealTokenRef = "op://vault/item/field/extra" }, wantMsg: `server.seal_token_ref must have the format op://<vault>/<item>/<field>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			tc.mutate(cfg)
			_, err := config.Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("Validate error = %v, want %q", err, tc.wantMsg)
			}
		})
	}
}

func TestValidate_DuplicateUUID(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	const dup = "550e8400-e29b-41d4-a716-446655440000"
	cfg.Devices = []config.DeviceConfig{{Name: "node-a", UUID: dup}, {Name: "node-b", UUID: dup}}
	_, err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "duplicate of devices[0]") {
		t.Fatalf("Validate duplicate UUID error = %v", err)
	}
}

func TestValidate_DeviceNameControlChars(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, device string }{{"newline", "node\n1"}, {"carriage return", "node\r1"}, {"null byte", "node\x001"}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			cfg.Devices[0].Name = tc.device
			_, err := config.Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), "control characters") {
				t.Fatalf("Validate device name error = %v", err)
			}
		})
	}
}

func TestValidate_DuplicateName(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.Devices = []config.DeviceConfig{{Name: "talos-node", UUID: "550e8400-e29b-41d4-a716-446655440000"}, {Name: "talos-node", UUID: "6ba7b810-9dad-11d1-80b4-00c04fd430c8"}}
	_, err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "duplicate of devices[0]") {
		t.Fatalf("Validate duplicate name error = %v", err)
	}
}

func TestValidate_InvalidDuration(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.Notifications.Telegram.RateLimit = "not-a-duration"
	_, err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "invalid duration") {
		t.Fatalf("Validate duration error = %v", err)
	}
}

func TestValidate_NegativeDuration(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.Notifications.Telegram.RateLimit = "-5m"
	_, err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("Validate negative duration error = %v", err)
	}
}

func TestValidate_GateType(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mutate  func(*config.Config)
		wantMsg string
	}{
		{name: "unknown gate type", mutate: func(c *config.Config) { c.Gate = &config.GateConfig{Type: "magic"} }, wantMsg: "gate.type must be: telegram"},
		{name: "telegram type notifications.telegram nil", mutate: func(c *config.Config) { c.Notifications.Telegram = nil }, wantMsg: `notifications.telegram is required when gate.type is "telegram"`},
		{name: "telegram type gate.telegram nil", mutate: func(c *config.Config) { c.Gate = &config.GateConfig{Type: config.GateTypeTelegram} }, wantMsg: `gate.telegram config is required when gate.type is "telegram"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			tc.mutate(cfg)
			_, err := config.Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("Validate error = %v, want %q", err, tc.wantMsg)
			}
		})
	}
}

func TestValidate_GateAuthorizedUserID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, userID string }{{"non-integer", "not-a-number"}, {"negative", "-123"}, {"zero", "0"}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			cfg.Gate.Telegram.AuthorizedUserID = tc.userID
			_, err := config.Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), "must be a positive integer") {
				t.Fatalf("Validate authorized_user_id error = %v", err)
			}
		})
	}
}

func TestValidate_GateAuthorizedUserIDStored(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.Gate.Telegram.AuthorizedUserID = "987654321"
	vc, err := config.Validate(cfg)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if vc.TelegramAuthorizedUserID != 987654321 {
		t.Errorf("TelegramAuthorizedUserID = %d, want 987654321", vc.TelegramAuthorizedUserID)
	}
}

func TestValidate_ParsedDurationsStored(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.Notifications.Telegram.RateLimit = "3m"
	cfg.Notifications.Telegram.ApprovalTTL = "15m"
	vc, err := config.Validate(cfg)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if vc.TelegramRateLimit != 3*time.Minute {
		t.Errorf("TelegramRateLimit = %v, want %v", vc.TelegramRateLimit, 3*time.Minute)
	}
	if vc.TelegramApprovalTTL != 15*time.Minute {
		t.Errorf("TelegramApprovalTTL = %v, want %v", vc.TelegramApprovalTTL, 15*time.Minute)
	}
}

func TestValidate_ListenLoopbackOnly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		addr    string
		wantErr bool
		wantMsg string
	}{
		{"loopback IPv4", "127.0.0.1:7800", false, ""},
		{"loopback IPv6", "[::1]:7800", false, ""},
		{"all interfaces", ":7800", true, "loopback"},
		{"explicit all", "0.0.0.0:7800", true, "loopback"},
		{"routable", "10.0.0.1:7800", true, "loopback"},
		{"hostname not allowed", "localhost:7800", true, "not a valid IP"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			cfg.Server.Listen = tc.addr
			_, err := config.Validate(cfg)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
					t.Fatalf("Validate(%q) error = %v, want %q", tc.addr, err, tc.wantMsg)
				}
			} else if err != nil {
				t.Fatalf("Validate(%q) = %v, want nil", tc.addr, err)
			}
		})
	}
}

func TestValidate_MetricsListenLoopbackOnly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		addr    string
		wantErr bool
		wantMsg string
	}{
		{"loopback IPv4", "127.0.0.1:9090", false, ""},
		{"loopback IPv6", "[::1]:9090", false, ""},
		{"all interfaces", ":9090", true, "loopback"},
		{"explicit all", "0.0.0.0:9090", true, "loopback"},
		{"routable", "10.0.0.1:9090", true, "loopback"},
		{"hostname not allowed", "localhost:9090", true, "not a valid IP"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			cfg.Server.MetricsListen = tc.addr
			_, err := config.Validate(cfg)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
					t.Fatalf("Validate(%q) error = %v, want %q", tc.addr, err, tc.wantMsg)
				}
			} else if err != nil {
				t.Fatalf("Validate(%q) = %v, want nil", tc.addr, err)
			}
		})
	}
}

func TestValidate_GatedDevicesRequireGate(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.Gate = nil
	_, err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "gate is required") {
		t.Fatalf("Validate without gate error = %v", err)
	}
}

func TestValidate_NotificationRequiredFields(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mutate  func(*config.Config)
		wantMsg string
	}{
		{name: "telegram missing bot_token_ref", mutate: func(c *config.Config) { c.Notifications.Telegram = &config.TelegramConfig{ChatID: "123"} }, wantMsg: "notifications.telegram.bot_token_ref is required"},
		{name: "telegram missing chat_id", mutate: func(c *config.Config) { c.Notifications.Telegram = &config.TelegramConfig{BotTokenRef: "op://v/a/t"} }, wantMsg: "notifications.telegram.chat_id is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			tc.mutate(cfg)
			_, err := config.Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("Validate error = %v, want %q", err, tc.wantMsg)
			}
		})
	}
}

func TestValidate_MultiError(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server:    config.ServerConfig{LogLevel: "info", LogFormat: "json"},
		MasterKey: config.MasterKeyConfig{Source: "bad-source"},
		Devices:   []config.DeviceConfig{{Name: "x", UUID: "y"}},
	}
	_, err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected errors, got nil")
	}
	for _, s := range []string{"server.listen is required", "server.seal_token_ref is required", "master_key.source must be", "registry.path is required"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error missing %q\nfull error: %s", s, err.Error())
		}
	}
}
