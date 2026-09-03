package config

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

var validSources = map[MasterKeySource]struct{}{
	MasterKeySourceFile: {},
}

var validLogLevels = map[string]struct{}{
	"debug": {},
	"info":  {},
	"warn":  {},
	"error": {},
}

var validLogFormats = map[string]struct{}{
	"json": {},
	"text": {},
}

// Validate checks business rules for cfg. All errors are collected and returned
// together via errors.Join. On success it returns a ValidatedConfig holding all
// parsed/resolved values (durations, integer IDs) derived from cfg.
func Validate(cfg *Config) (*ValidatedConfig, error) {
	var errs []error
	vc := &ValidatedConfig{}

	validateServer(cfg, &errs)
	validateMasterKey(cfg, &errs)
	validateRegistry(cfg, &errs)
	validateDevices(cfg, &errs)
	validateNotifications(cfg, vc, &errs)
	validateGate(cfg, vc, &errs)

	// server.name is required when Telegram gate is enabled.
	if cfg.Gate != nil && cfg.Gate.Type == GateTypeTelegram && cfg.Server.Name == "" {
		errs = append(errs, errors.New("server.name is required when gate.type is \"telegram\""))
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return vc, nil
}

func validateServer(cfg *Config, errs *[]error) {
	s := &cfg.Server
	if s.Listen == "" {
		*errs = append(*errs, errors.New("server.listen is required"))
	} else {
		validateLoopbackAddr("server.listen", s.Listen, errs)
	}
	if _, ok := validLogLevels[s.LogLevel]; !ok {
		*errs = append(*errs, errors.New("server.log_level must be one of: debug, info, warn, error"))
	}
	if _, ok := validLogFormats[s.LogFormat]; !ok {
		*errs = append(*errs, errors.New("server.log_format must be one of: json, text"))
	}
	if s.SealTokenRef == "" {
		*errs = append(*errs, errors.New("server.seal_token_ref is required"))
	}
	validateOpRef("server.seal_token_ref", s.SealTokenRef, errs)
	validateLoopbackAddr("server.metrics_listen", s.MetricsListen, errs)
}

// validateLoopbackAddr rejects any address that would bind to a non-loopback
// interface. Both the gRPC listener and the metrics endpoint must stay on
// loopback: the gRPC listener because Caddy terminates TLS and proxies to
// azath locally; the metrics endpoint because it has no authentication and
// exposes operational topology (key loaded, device count, gate denial rate).
func validateLoopbackAddr(field, addr string, errs *[]error) {
	if addr == "" {
		return // default will be applied by applyDefaults before validate is called
	}
	host, _, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		*errs = append(*errs, fmt.Errorf("%s: invalid address %q: %w", field, addr, splitErr))
		return
	}
	if host == "" {
		*errs = append(*errs, fmt.Errorf("%s must bind to a loopback address (e.g. 127.0.0.1:9090), not all interfaces", field))
		return
	}
	ip := net.ParseIP(host)
	if ip == nil {
		*errs = append(*errs, fmt.Errorf("%s: %q is not a valid IP address; use 127.0.0.1 or ::1", field, host))
		return
	}
	if !ip.IsLoopback() {
		*errs = append(*errs, fmt.Errorf("%s must bind to a loopback address (127.0.0.1 or ::1), got %q", field, host))
	}
}

func validateMasterKey(cfg *Config, errs *[]error) {
	m := &cfg.MasterKey
	if _, ok := validSources[m.Source]; !ok {
		*errs = append(*errs, errors.New("master_key.source must be: file"))
		return // remaining checks depend on a valid source
	}
	if m.Path == "" {
		*errs = append(*errs, errors.New(`master_key.path is required when source is "file"`))
	}
}

func validateRegistry(cfg *Config, errs *[]error) {
	if cfg.Registry.Path == "" {
		*errs = append(*errs, errors.New("registry.path is required"))
	}
}

func validateDevices(cfg *Config, errs *[]error) {
	uuids := make(map[string]int, len(cfg.Devices))
	seenNames := make(map[string]int, len(cfg.Devices))
	for i, d := range cfg.Devices {
		parsed, parseErr := uuid.Parse(d.UUID)
		if parseErr != nil {
			*errs = append(*errs, fmt.Errorf("devices[%d].uuid %q is not a valid UUID", i, d.UUID))
		} else {
			canonical := parsed.String() // normalize case before uniqueness check
			if prev, dup := uuids[canonical]; dup {
				*errs = append(*errs, fmt.Errorf("devices[%d].uuid %q is a duplicate of devices[%d]", i, d.UUID, prev))
			} else {
				uuids[canonical] = i
			}
		}

		if d.Name == "" {
			*errs = append(*errs, fmt.Errorf("devices[%d].name is required", i))
		} else if strings.ContainsAny(d.Name, "\n\r\x00") {
			*errs = append(*errs, fmt.Errorf("devices[%d].name must not contain control characters", i))
		} else if prev, dup := seenNames[d.Name]; dup {
			*errs = append(*errs, fmt.Errorf("devices[%d].name %q is a duplicate of devices[%d]", i, d.Name, prev))
		} else {
			seenNames[d.Name] = i
		}
	}
}

func validateNotifications(cfg *Config, vc *ValidatedConfig, errs *[]error) {
	if t := cfg.Notifications.Telegram; t != nil {
		if t.BotTokenRef == "" {
			*errs = append(*errs, errors.New("notifications.telegram.bot_token_ref is required"))
		}
		if t.ChatID == "" {
			*errs = append(*errs, errors.New("notifications.telegram.chat_id is required"))
		}
		validateOpRef("notifications.telegram.bot_token_ref", t.BotTokenRef, errs)
		validateDuration("notifications.telegram.rate_limit", t.RateLimit, &vc.TelegramRateLimit, errs)
		validateDuration("notifications.telegram.approval_ttl", t.ApprovalTTL, &vc.TelegramApprovalTTL, errs)
	}
}

func validateGate(cfg *Config, vc *ValidatedConfig, errs *[]error) {
	g := cfg.Gate
	if g == nil {
		// Gate is required whenever devices are configured (all devices require approval).
		if len(cfg.Devices) > 0 {
			*errs = append(*errs, errors.New("gate is required when devices are configured"))
		}
		return
	}
	switch g.Type {
	case GateTypeTelegram:
		// notifications.telegram provides the bot credentials used to send approval requests.
		if cfg.Notifications.Telegram == nil {
			*errs = append(*errs, errors.New(`notifications.telegram is required when gate.type is "telegram"`))
			return
		}
		// gate.telegram provides the gate-specific setting: who is allowed to approve.
		if g.Telegram == nil {
			*errs = append(*errs, errors.New(`gate.telegram config is required when gate.type is "telegram"`))
			return
		}
		if g.Telegram.AuthorizedUserID == "" {
			*errs = append(*errs, errors.New(`gate.telegram.authorized_user_id is required when gate.type is "telegram"`))
		} else {
			id, parseErr := strconv.ParseInt(g.Telegram.AuthorizedUserID, 10, 64)
			if parseErr != nil || id <= 0 {
				*errs = append(*errs, errors.New("gate.telegram.authorized_user_id must be a positive integer"))
			} else {
				vc.TelegramAuthorizedUserID = id
			}
		}
		// Parse approval_cache_ttl; default to 2m if not set.
		cacheTTL := g.Telegram.ApprovalCacheTTL
		if cacheTTL == "" {
			vc.TelegramApprovalCacheTTL = 2 * time.Minute
		} else {
			validateDuration("gate.telegram.approval_cache_ttl", cacheTTL, &vc.TelegramApprovalCacheTTL, errs)
		}
	default:
		*errs = append(*errs, fmt.Errorf("gate.type must be: telegram; got %q", g.Type))
	}
}

// validateOpRef appends an error if value is non-empty and does not conform to
// the 1Password reference format: op://<vault>/<item>/<field>.
func validateOpRef(field, value string, errs *[]error) {
	if value == "" {
		return
	}
	rest, ok := strings.CutPrefix(value, "op://")
	if !ok {
		*errs = append(*errs, fmt.Errorf(`%s must start with "op://"`, field))
		return
	}
	// Require exactly three non-empty, non-whitespace path segments: vault/item/field.
	// Use SplitN with limit 3 so a four-segment path like op://v/i/f/extra is
	// caught: parts[2] becomes "f/extra", which fails the whitespace check.
	// Without the limit, SplitN(rest, "/", 4) would accept the first three
	// segments and silently ignore any trailing content.
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 ||
		strings.TrimSpace(parts[0]) == "" ||
		strings.TrimSpace(parts[1]) == "" ||
		strings.TrimSpace(parts[2]) == "" ||
		strings.Contains(parts[2], "/") {
		*errs = append(*errs, fmt.Errorf(`%s must have the format op://<vault>/<item>/<field>`, field))
	}
}

// validateDuration appends an error if value is non-empty, cannot be parsed as
// a duration, or is not positive. On success, the parsed value is stored in dest.
func validateDuration(field, value string, dest *time.Duration, errs *[]error) {
	if value != "" {
		d, err := time.ParseDuration(value)
		if err != nil {
			*errs = append(*errs, fmt.Errorf("%s: invalid duration: %w", field, err))
			return
		}
		if d <= 0 {
			*errs = append(*errs, fmt.Errorf("%s: duration must be positive, got %v", field, d))
			return
		}
		*dest = d
	}
}
