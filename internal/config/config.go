// Package config loads, parses, and validates azath configuration files.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// MasterKeySource identifies how the master key is sourced.
type MasterKeySource string

const (
	MasterKeySourceFile MasterKeySource = "file"
)

// GateType identifies the approval gate implementation.
type GateType string

const (
	GateTypeTelegram GateType = "telegram"
)

// Config is the top-level configuration for azath.
type Config struct {
	Server        ServerConfig    `yaml:"server"`
	MasterKey     MasterKeyConfig `yaml:"master_key"`
	Registry      RegistryConfig  `yaml:"registry"`
	Notifications NotifyConfig    `yaml:"notifications"`
	Devices       []DeviceConfig  `yaml:"devices"`
	Gate          *GateConfig     `yaml:"gate,omitempty"`
	Client        *ClientConfig   `yaml:"client,omitempty"`
}

// ValidatedConfig holds parsed/resolved values produced by Validate. Config is
// a pure YAML schema; no runtime-derived state lives in it. ValidatedConfig is
// the single source of truth for all computed fields needed at runtime.
type ValidatedConfig struct {
	// TelegramRateLimit is the parsed rate_limit from notifications.telegram.
	// Zero if notifications.telegram is absent.
	TelegramRateLimit time.Duration
	// TelegramApprovalTTL is the parsed approval_ttl from notifications.telegram.
	// Zero if notifications.telegram is absent.
	TelegramApprovalTTL time.Duration
	// TelegramAuthorizedUserID is the parsed authorized_user_id from gate.telegram.
	// Zero if gate.type is not "telegram".
	TelegramAuthorizedUserID int64
	// TelegramApprovalCacheTTL is the parsed approval_cache_ttl from gate.telegram.
	// Defaults to 2m when gate.type is telegram and not explicitly set to 0.
	TelegramApprovalCacheTTL time.Duration
}

// ServerConfig holds gRPC server and observability settings.
type ServerConfig struct {
	Listen        string `yaml:"listen"`
	MetricsListen string `yaml:"metrics_listen"`
	LogLevel      string `yaml:"log_level"`
	LogFormat     string `yaml:"log_format"`
	SealTokenRef  string `yaml:"seal_token_ref"`
	Name          string `yaml:"name,omitempty"`
}

// MasterKeyConfig specifies the local raw 32-byte master key file.
type MasterKeyConfig struct {
	Source MasterKeySource `yaml:"source"`
	Path   string          `yaml:"path,omitempty"`
}

// RegistryConfig specifies the device registry storage path.
type RegistryConfig struct {
	Path string `yaml:"path"`
}

// NotifyConfig holds notification provider settings.
type NotifyConfig struct {
	Telegram *TelegramConfig `yaml:"telegram,omitempty"`
}

// TelegramConfig holds Telegram bot settings for notifications. These settings
// are also used by the telegram gate for sending approval requests.
// See TelegramGateConfig for gate-specific settings (who may approve).
type TelegramConfig struct {
	BotTokenRef string `yaml:"bot_token_ref"`
	ChatID      string `yaml:"chat_id"`
	RateLimit   string `yaml:"rate_limit,omitempty"`
	ApprovalTTL string `yaml:"approval_ttl,omitempty"`
}

// DeviceConfig represents a single managed device.
// All devices require gate approval — there is no per-device override.
type DeviceConfig struct {
	Name     string `yaml:"name"`
	UUID     string `yaml:"uuid"`
	Disabled bool   `yaml:"disabled,omitempty"`
}

// GateConfig specifies the Telegram approval gate configuration.
type GateConfig struct {
	Type     GateType            `yaml:"type"`
	Telegram *TelegramGateConfig `yaml:"telegram,omitempty"`
}

// TelegramGateConfig holds gate-specific settings for the Telegram gate.
// Notification settings (bot_token_ref, chat_id, rate_limit, approval_ttl)
// remain in notifications.telegram.
type TelegramGateConfig struct {
	// AuthorizedUserID is the Telegram user ID of the person allowed to approve
	// unseal requests. Must be a positive integer string.
	AuthorizedUserID string `yaml:"authorized_user_id"`
	// ApprovalCacheTTL is the duration to cache approved devices. Zero disables caching.
	// Defaults to 2m if not set.
	ApprovalCacheTTL string `yaml:"approval_cache_ttl,omitempty"`
}

// ClientConfig holds planned settings for the azath client subcommand.
type ClientConfig struct {
	Device    ClientDeviceConfig `yaml:"device"`
	Endpoints []string           `yaml:"endpoints"`
}

// ClientDeviceConfig identifies the client device for KMS RPCs.
type ClientDeviceConfig struct {
	Name string `yaml:"name"`
	UUID string `yaml:"uuid"`
}

// Load reads a YAML config file from path, applies safe defaults, and returns
// the parsed Config. Unknown YAML fields are rejected. Multi-document files are
// rejected. It does not validate business rules — call Validate separately.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) // #nosec G304 — path comes from a CLI flag
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	return LoadFromBytes(data)
}

// LoadFromBytes parses a YAML config from in-memory bytes, applies safe defaults,
// and returns the parsed Config. Unknown fields and multi-document files are
// rejected. It does not validate business rules — call Validate separately.
func LoadFromBytes(data []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	// Reject multi-document files. yaml.Decoder.Decode silently ignores trailing
	// documents, which would hide concatenated or malformed content. Decode into
	// a throwaway value to probe whether a second document exists.
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parsing config: file must contain exactly one YAML document")
	}
	applyDefaults(&cfg)
	return &cfg, nil
}

// applyDefaults fills in safe defaults for optional fields.
func applyDefaults(cfg *Config) {
	if cfg.Server.MetricsListen == "" {
		cfg.Server.MetricsListen = "127.0.0.1:9090"
	}
	if cfg.Server.LogLevel == "" {
		cfg.Server.LogLevel = "info"
	}
	if cfg.Server.LogFormat == "" {
		cfg.Server.LogFormat = "json"
	}
	// Default Telegram durations to safe non-zero values. Zero rate_limit means
	// no rate limiting (unbounded approval spam); zero approval_ttl means
	// approvals expire immediately or never, depending on server semantics.
	if t := cfg.Notifications.Telegram; t != nil {
		if t.RateLimit == "" {
			t.RateLimit = "5m"
		}
		if t.ApprovalTTL == "" {
			t.ApprovalTTL = "10m"
		}
	}
}

// SafeAttrs returns slog attributes safe to log from cfg.
// Network addresses, filesystem paths, and secret refs are omitted — they are
// attack-surface signals that must not appear in structured logs.
func SafeAttrs(cfg *Config) []slog.Attr {
	return []slog.Attr{
		slog.String("server.log_level", cfg.Server.LogLevel),
		slog.String("server.log_format", cfg.Server.LogFormat),
		slog.String("master_key.source", string(cfg.MasterKey.Source)),
		slog.Int("devices", len(cfg.Devices)),
		slog.Bool("gate_configured", cfg.Gate != nil),
		slog.Bool("notifications.telegram", cfg.Notifications.Telegram != nil),
	}
}
