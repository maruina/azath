package config_test

import (
	"strings"
	"testing"

	"github.com/maruina/azath/internal/config"
)

func TestRejectRegistryFields(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"registry: {}", "registry:\n  path: /obsolete/registry.json"} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			_, err := config.LoadFromBytes([]byte(input))
			if err == nil || !strings.Contains(err.Error(), "field registry not found") {
				t.Fatalf("expected unknown registry field, got %v", err)
			}
		})
	}
}
