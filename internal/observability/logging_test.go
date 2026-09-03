package observability_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/maruina/azath/internal/observability"
)

func TestNewLogger_JSONFormat(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger, err := observability.NewLogger("info", "json", observability.WithWriter(&buf))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	logger.Info("hello world")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, buf.String())
	}
	for _, key := range []string{"time", "level", "msg"} {
		if _, ok := entry[key]; !ok {
			t.Errorf("expected JSON key %q not found in: %s", key, buf.String())
		}
	}
	if entry["msg"] != "hello world" {
		t.Errorf("expected msg=%q, got %q", "hello world", entry["msg"])
	}
}

func TestNewLogger_TextFormat(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger, err := observability.NewLogger("info", "text", observability.WithWriter(&buf))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	logger.Info("hello text")

	out := buf.String()
	if !strings.Contains(out, "hello text") {
		t.Errorf("expected %q in text output: %s", "hello text", out)
	}
}

func TestNewLogger_InvalidLevel(t *testing.T) {
	t.Parallel()
	_, err := observability.NewLogger("invalid", "json")
	if err == nil {
		t.Fatal("expected error for invalid level, got nil")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("error should mention the bad value, got: %v", err)
	}
}

func TestNewLogger_InvalidFormat(t *testing.T) {
	t.Parallel()
	_, err := observability.NewLogger("info", "xml")
	if err == nil {
		t.Fatal("expected error for invalid format, got nil")
	}
	if !strings.Contains(err.Error(), "xml") {
		t.Errorf("error should mention the bad value, got: %v", err)
	}
}

func TestNewLogger_LevelFiltering(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger, err := observability.NewLogger("warn", "json", observability.WithWriter(&buf))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logger.Info("should be filtered")
	if buf.Len() != 0 {
		t.Errorf("info message should be filtered at warn level, got: %s", buf.String())
	}

	logger.Warn("should appear")
	if buf.Len() == 0 {
		t.Error("warn message should appear at warn level")
	}
}
