package config_test

import (
	"strings"
	"testing"

	"github.com/maruina/azath/internal/config"
)

func devices(items ...config.DeviceConfig) []config.DeviceConfig {
	return items
}

func dev(uuid, name string) config.DeviceConfig {
	return config.DeviceConfig{UUID: uuid, Name: name}
}

func TestDiff_Identical(t *testing.T) {
	t.Parallel()
	a := &config.Config{Devices: devices(dev("uuid-1", "node-a"))}
	b := &config.Config{Devices: devices(dev("uuid-1", "node-a"))}
	if got := config.Diff(a, b); got != nil {
		t.Errorf("expected nil for identical configs, got %v", got)
	}
}

func TestDiff_OnlyInFirst(t *testing.T) {
	t.Parallel()
	a := &config.Config{Devices: devices(
		dev("uuid-1", "node-a"),
		dev("uuid-2", "node-b"),
	)}
	b := &config.Config{Devices: devices(
		dev("uuid-1", "node-a"),
	)}
	diffs := config.Diff(a, b)
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d: %v", len(diffs), diffs)
	}
	assertContains(t, diffs[0], "uuid-2")
	assertContains(t, diffs[0], "only in first config")
}

func TestDiff_OnlyInSecond(t *testing.T) {
	t.Parallel()
	a := &config.Config{Devices: devices(dev("uuid-1", "node-a"))}
	b := &config.Config{Devices: devices(
		dev("uuid-1", "node-a"),
		dev("uuid-2", "node-b"),
	)}
	diffs := config.Diff(a, b)
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d: %v", len(diffs), diffs)
	}
	assertContains(t, diffs[0], "uuid-2")
	assertContains(t, diffs[0], "only in second config")
}

func TestDiff_DifferentName(t *testing.T) {
	t.Parallel()
	a := &config.Config{Devices: devices(dev("uuid-1", "old-name"))}
	b := &config.Config{Devices: devices(dev("uuid-1", "new-name"))}
	diffs := config.Diff(a, b)
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d: %v", len(diffs), diffs)
	}
	assertContains(t, diffs[0], "uuid-1")
	assertContains(t, diffs[0], "name")
}

func TestDiff_MultipleMismatches(t *testing.T) {
	t.Parallel()
	a := &config.Config{Devices: devices(
		dev("uuid-1", "node-a"),
		dev("uuid-2", "node-b"),
	)}
	b := &config.Config{Devices: devices(
		dev("uuid-1", "node-a-renamed"),
		dev("uuid-3", "node-c"),
	)}
	diffs := config.Diff(a, b)
	// uuid-1 name mismatch, uuid-2 only in first, uuid-3 only in second = 3 diffs
	if len(diffs) != 3 {
		t.Fatalf("expected 3 diffs, got %d: %v", len(diffs), diffs)
	}
}

func TestDiff_Deterministic(t *testing.T) {
	t.Parallel()
	a := &config.Config{Devices: devices(
		dev("uuid-c", "node-c"),
		dev("uuid-a", "node-a"),
		dev("uuid-b", "node-b"),
	)}
	b := &config.Config{Devices: devices(
		dev("uuid-a", "node-a"),
	)}
	// uuid-b and uuid-c are only in a; output must be sorted by the full string.
	want := []string{
		`device "uuid-b" ("node-b") only in first config`,
		`device "uuid-c" ("node-c") only in first config`,
	}
	got := config.Diff(a, b)
	if len(got) != len(want) {
		t.Fatalf("Diff() returned %d entries, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Diff()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDiff_EmptyBoth(t *testing.T) {
	t.Parallel()
	a := &config.Config{}
	b := &config.Config{}
	if got := config.Diff(a, b); got != nil {
		t.Errorf("expected nil for two empty device lists, got %v", got)
	}
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("%q does not contain %q", s, substr)
	}
}
