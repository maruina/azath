package registry_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/maruina/azath/internal/observability"
	"github.com/maruina/azath/internal/registry"
)

func newTestMetrics(t *testing.T) *observability.Metrics {
	t.Helper()
	return observability.NewMetrics()
}

func mustLoad(t *testing.T, path string) *registry.Registry {
	t.Helper()
	r, err := registry.Load(path, nil, newTestMetrics(t), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return r
}

func newUUID(t *testing.T) string {
	t.Helper()
	return uuid.New().String()
}

func TestLoad_EmptyFile(t *testing.T) {
	t.Parallel()
	r := mustLoad(t, filepath.Join(t.TempDir(), "registry.json"))
	if r.Len() != 0 {
		t.Errorf("Len() = %d, want 0", r.Len())
	}
}

func TestLoad_CorruptJSON(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "registry.json")
	if err := os.WriteFile(path, []byte("not json{{{"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	m := newTestMetrics(t)
	_, err := registry.Load(path, nil, m, nil)
	if err == nil {
		t.Errorf("Load(%q) = nil, want error", path)
	}
	if count := registryLoadErrorsCount(t, m); count != 1 {
		t.Errorf("RegistryLoadErrors = %v, want 1", count)
	}
}

func TestLoad_DuplicateUUID(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "registry.json")
	id := newUUID(t)
	content := fmt.Sprintf(`{"devices":[{"name":"node-1","uuid":%q},{"name":"node-2","uuid":%q}]}`, id, id)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	m := newTestMetrics(t)
	_, err := registry.Load(path, nil, m, nil)
	if err == nil {
		t.Errorf("Load(%q) = nil, want error for duplicate UUID", path)
	}
	if count := registryLoadErrorsCount(t, m); count != 1 {
		t.Errorf("RegistryLoadErrors = %v, want 1", count)
	}
}

func TestLoad_RoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "registry.json")
	r := mustLoad(t, path)
	id := newUUID(t)
	if err := r.Register("node-1", id); err != nil {
		t.Fatalf("Register: %v", err)
	}

	r2, err := registry.Load(path, nil, newTestMetrics(t), nil)
	if err != nil {
		t.Fatalf("Load reload: %v", err)
	}
	entry, err := r2.Lookup(id)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	want := registry.DeviceEntry{Name: "node-1", UUID: id}
	if *entry != want {
		t.Errorf("Lookup = %+v, want %+v", *entry, want)
	}
}

func TestRegister_ValidDevice(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	r, err := registry.Load(filepath.Join(t.TempDir(), "registry.json"), nil, m, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := r.Register("node-1", newUUID(t)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if r.Len() != 1 {
		t.Errorf("Len() = %d, want 1", r.Len())
	}
	if size := registrySizeValue(t, m); size != 1 {
		t.Errorf("RegistrySize = %v, want 1", size)
	}
}

func TestRegister_InvalidUUID(t *testing.T) {
	t.Parallel()
	r := mustLoad(t, filepath.Join(t.TempDir(), "registry.json"))
	err := r.Register("node-1", "not-a-uuid")
	if !errors.Is(err, registry.ErrInvalidUUID) {
		t.Errorf("Register invalid UUID = %v, want ErrInvalidUUID", err)
	}
}

func TestRegister_Idempotent(t *testing.T) {
	t.Parallel()
	r := mustLoad(t, filepath.Join(t.TempDir(), "registry.json"))
	id := newUUID(t)
	if err := r.Register("node-1", id); err != nil {
		t.Fatalf("Register first: %v", err)
	}
	if err := r.Register("node-1", id); err != nil {
		t.Errorf("Register idempotent = %v, want nil", err)
	}
	if r.Len() != 1 {
		t.Errorf("Len() = %d, want 1", r.Len())
	}
}

func TestRegister_DuplicateUUID_DifferentName(t *testing.T) {
	t.Parallel()
	r := mustLoad(t, filepath.Join(t.TempDir(), "registry.json"))
	id := newUUID(t)
	if err := r.Register("node-1", id); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Register("node-2", id); !errors.Is(err, registry.ErrUUIDConflict) {
		t.Errorf("Register duplicate = %v, want ErrUUIDConflict", err)
	}
}

func TestRegister_UUIDCanonicalization(t *testing.T) {
	t.Parallel()
	r := mustLoad(t, filepath.Join(t.TempDir(), "registry.json"))
	upperID := strings.ToUpper(newUUID(t))
	if err := r.Register("node-1", upperID); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := r.Lookup(strings.ToLower(upperID)); err != nil {
		t.Errorf("Lookup canonical UUID = %v, want nil", err)
	}
}

func TestLookup_Found(t *testing.T) {
	t.Parallel()
	r := mustLoad(t, filepath.Join(t.TempDir(), "registry.json"))
	id := newUUID(t)
	if err := r.Register("node-1", id); err != nil {
		t.Fatalf("Register: %v", err)
	}
	entry, err := r.Lookup(id)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	want := registry.DeviceEntry{Name: "node-1", UUID: id}
	if *entry != want {
		t.Errorf("Lookup = %+v, want %+v", *entry, want)
	}
}

func TestLookup_NotFound(t *testing.T) {
	t.Parallel()
	r := mustLoad(t, filepath.Join(t.TempDir(), "registry.json"))
	_, err := r.Lookup(newUUID(t))
	if !errors.Is(err, registry.ErrDeviceNotFound) {
		t.Errorf("Lookup missing = %v, want ErrDeviceNotFound", err)
	}
}

func TestLookup_InvalidUUID(t *testing.T) {
	t.Parallel()
	r := mustLoad(t, filepath.Join(t.TempDir(), "registry.json"))
	_, err := r.Lookup("bad-uuid")
	if !errors.Is(err, registry.ErrInvalidUUID) {
		t.Errorf("Lookup invalid = %v, want ErrInvalidUUID", err)
	}
}

func TestDevices_Snapshot(t *testing.T) {
	t.Parallel()
	r := mustLoad(t, filepath.Join(t.TempDir(), "registry.json"))
	id := newUUID(t)
	if err := r.Register("node-1", id); err != nil {
		t.Fatalf("Register: %v", err)
	}
	snap := r.Devices()
	if len(snap) != 1 {
		t.Fatalf("Devices len = %d, want 1", len(snap))
	}
	snap[0].Name = "mutated"

	entry, err := r.Lookup(id)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	want := registry.DeviceEntry{Name: "node-1", UUID: id}
	if *entry != want {
		t.Errorf("Lookup after snapshot mutation = %+v, want %+v", *entry, want)
	}
}

func TestAtomicWrite_LeavesNoStaleFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")
	r := mustLoad(t, path)
	if err := r.Register("node-1", newUUID(t)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "registry.json" {
			t.Errorf("unexpected file in dir: %q", e.Name())
		}
	}
}

func TestRegister_EmptyName(t *testing.T) {
	t.Parallel()
	r := mustLoad(t, filepath.Join(t.TempDir(), "registry.json"))
	if err := r.Register("", newUUID(t)); err == nil {
		t.Errorf(`Register("", uuid) = nil, want error`)
	}
}

func TestConcurrent_MixedOps(t *testing.T) {
	t.Parallel()
	r := mustLoad(t, filepath.Join(t.TempDir(), "registry.json"))
	ids := make([]string, 10)
	for i := range ids {
		ids[i] = newUUID(t)
		if err := r.Register("node", ids[i]); err != nil {
			t.Fatalf("Register setup: %v", err)
		}
	}

	var wg sync.WaitGroup
	const goroutines = 48
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			id := ids[i%len(ids)]
			switch i % 3 {
			case 0:
				_ = r.Register("node", id)
			case 1:
				_, _ = r.Lookup(id)
			case 2:
				_ = r.Devices()
			}
		}(i)
	}
	wg.Wait()
}

func testHMACKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

func TestLoad_HMACVerification_Correct(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "registry.json")
	key := testHMACKey()

	r, err := registry.Load(path, key, newTestMetrics(t), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if regErr := r.Register("node-1", newUUID(t)); regErr != nil {
		t.Fatalf("Register: %v", regErr)
	}

	r2, err := registry.Load(path, key, newTestMetrics(t), nil)
	if err != nil {
		t.Fatalf("reload with HMAC: %v", err)
	}
	if r2.Len() != 1 {
		t.Errorf("Len() = %d, want 1", r2.Len())
	}
}

func TestLoad_HMACVerification_Tampered(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "registry.json")
	key := testHMACKey()

	r, err := registry.Load(path, key, newTestMetrics(t), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if regErr := r.Register("node-1", newUUID(t)); regErr != nil {
		t.Fatalf("Register: %v", regErr)
	}

	tampered := []byte(`{"devices":[{"name":"evil","uuid":"00000000-0000-0000-0000-000000000000"}]}`)
	if writeErr := os.WriteFile(path, tampered, 0o600); writeErr != nil {
		t.Fatalf("WriteFile tamper: %v", writeErr)
	}

	m := newTestMetrics(t)
	_, err = registry.Load(path, key, m, nil)
	if err == nil {
		t.Fatal("Load with tampered registry returned nil, want error")
	}
	if !strings.Contains(err.Error(), "HMAC mismatch") {
		t.Errorf("error = %q, want HMAC mismatch", err.Error())
	}
	if registryLoadErrorsCount(t, m) != 1 {
		t.Errorf("RegistryLoadErrors = %v, want 1", registryLoadErrorsCount(t, m))
	}
}

func TestLoad_HMACVerification_MissingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "registry.json")
	key := testHMACKey()

	r, err := registry.Load(path, nil, newTestMetrics(t), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if regErr := r.Register("node-1", newUUID(t)); regErr != nil {
		t.Fatalf("Register: %v", regErr)
	}

	m := newTestMetrics(t)
	_, err = registry.Load(path, key, m, nil)
	if err == nil {
		t.Fatal("Load with missing HMAC file returned nil, want error")
	}
	if !strings.Contains(err.Error(), "HMAC file missing") {
		t.Errorf("error = %q, want HMAC file missing", err.Error())
	}
	if registryLoadErrorsCount(t, m) != 1 {
		t.Errorf("RegistryLoadErrors = %v, want 1", registryLoadErrorsCount(t, m))
	}
}

func registryLoadErrorsCount(t *testing.T, m *observability.Metrics) float64 {
	t.Helper()
	return gatherMetricValue(t, m, "azath_registry_load_errors_total", func(v metricValues) float64 { return v.counter })
}

func registrySizeValue(t *testing.T, m *observability.Metrics) float64 {
	t.Helper()
	return gatherMetricValue(t, m, "azath_registry_size", func(v metricValues) float64 { return v.gauge })
}

type metricValues struct {
	counter float64
	gauge   float64
}

func gatherMetricValue(t *testing.T, m *observability.Metrics, name string, extract func(metricValues) float64) float64 {
	t.Helper()
	mfs, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, metric := range mf.GetMetric() {
			return extract(metricValues{
				counter: metric.GetCounter().GetValue(),
				gauge:   metric.GetGauge().GetValue(),
			})
		}
	}
	return 0
}
