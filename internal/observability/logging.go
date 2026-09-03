package observability

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// LoggerOption configures NewLogger.
type LoggerOption func(*loggerConfig)

type loggerConfig struct {
	writer io.Writer
}

// WithWriter overrides the default output destination (os.Stderr).
func WithWriter(w io.Writer) LoggerOption {
	return func(c *loggerConfig) {
		c.writer = w
	}
}

// NewLogger creates an slog.Logger with the given level and format.
// format: "json" (production) | "text" (development).
// level: "debug" | "info" | "warn" | "error".
func NewLogger(level, format string, opts ...LoggerOption) (*slog.Logger, error) {
	cfg := &loggerConfig{writer: os.Stderr}
	for _, opt := range opts {
		opt(cfg)
	}

	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "info":
		l = slog.LevelInfo
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		return nil, fmt.Errorf("unsupported log level: %q", level)
	}

	ho := &slog.HandlerOptions{Level: l}
	var handler slog.Handler
	switch strings.ToLower(format) {
	case "json":
		handler = slog.NewJSONHandler(cfg.writer, ho)
	case "text":
		handler = slog.NewTextHandler(cfg.writer, ho)
	default:
		return nil, fmt.Errorf("unsupported log format: %q", format)
	}

	return slog.New(handler), nil
}
