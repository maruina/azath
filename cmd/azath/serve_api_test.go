package main

import (
	"strings"
	"testing"
)

func TestServeTelegramAPIOverrideValidation(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, base string
		dev        bool
		want       string
	}{
		{"production", "http://127.0.0.1:9000", false, "requires --dev"},
		{"credentials", "https://user:token@example.com", true, "origin"},
		{"scheme", "file:///tmp/api", true, "origin"},
		{"path", "https://example.com/api", true, "origin"},
		{"query", "https://example.com?token=x", true, "origin"},
		{"fragment", "https://example.com#x", true, "origin"},
		{"valid", "http://127.0.0.1:9000", true, "loading config"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := runServe(t.Context(), "/nonexistent/config.yaml", tt.dev, tt.base)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
		})
	}
}
