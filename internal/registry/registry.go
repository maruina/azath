// Package registry manages the legacy device registry — the persistent store tracking
// which devices have been reconciled from config.
//
// The registry is stored as a JSON file at 0600 via fsutil.Write, which is
// atomic: temp file in same dir → fsync → rename → dir fsync. A crash at any
// point leaves either the old or new file fully intact.
//
// When an hmacKey is provided to Load, the registry is integrity-protected:
// HMAC-SHA256 of the JSON bytes is stored in a sidecar file (path + ".hmac")
// and verified on load. Pass nil to skip HMAC (dev/test only).
package registry

import (
	"cmp"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"

	"github.com/google/uuid"
	"github.com/maruina/azath/internal/fsutil"
	"github.com/maruina/azath/internal/observability"
)

// Sentinel errors returned by Registry methods for invalid input and missing entries.
var (
	// ErrInvalidUUID is returned when a UUID string fails to parse.
	ErrInvalidUUID = errors.New("invalid UUID")
	// ErrDeviceNotFound is returned when a UUID is not present in the registry.
	ErrDeviceNotFound = errors.New("device not found")
	// ErrUUIDConflict is returned when Register is called with a UUID already
	// registered under a different device name.
	ErrUUIDConflict = errors.New("UUID already registered under a different name")
)

// DeviceEntry represents a single managed device in the registry.
type DeviceEntry struct {
	Name string `json:"name"`
	UUID string `json:"uuid"`
}

// registryFile is the on-disk JSON envelope.
type registryFile struct {
	Devices []DeviceEntry `json:"devices"`
}

// Registry is a thread-safe, persistent device store.
type Registry struct {
	mu      sync.RWMutex
	path    string
	logger  *slog.Logger
	metrics *observability.Metrics
	devices map[string]*DeviceEntry // keyed by canonical UUID
	hmacKey []byte                  // nil = HMAC disabled (dev/test)
}

// Load reads the registry from path. If the file does not exist, an empty
// registry is returned. Corrupt JSON increments RegistryLoadErrors and returns
// an error. All stored UUIDs are normalised to lowercase canonical form on load.
//
// If hmacKey is non-nil, the HMAC-SHA256 of the registry JSON is verified
// against the sidecar file at path+".hmac". A missing or mismatched HMAC is
// treated as a load error — it may indicate tampering. Pass nil to disable
// HMAC (dev/test only).
//
// logger may be nil; in that case log output is discarded.
func Load(path string, hmacKey []byte, metrics *observability.Metrics, logger *slog.Logger) (*Registry, error) {
	if metrics == nil {
		panic("registry.Load: metrics must not be nil")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	r := &Registry{
		path:    path,
		logger:  logger,
		metrics: metrics,
		devices: make(map[string]*DeviceEntry),
		hmacKey: hmacKey,
	}

	data, err := os.ReadFile(path) // #nosec G304 — path comes from validated config
	if errors.Is(err, os.ErrNotExist) {
		// First start: no file yet, empty registry.
		return r, nil
	}
	if err != nil {
		metrics.RegistryLoadErrors.Inc()
		return nil, fmt.Errorf("reading registry: %w", err)
	}

	if hmacKey != nil {
		if err := verifyHMAC(path, hmacKey, data); err != nil {
			metrics.RegistryLoadErrors.Inc()
			return nil, err
		}
	}

	var f registryFile
	if err := json.Unmarshal(data, &f); err != nil {
		metrics.RegistryLoadErrors.Inc()
		return nil, fmt.Errorf("parsing registry: %w", err)
	}

	for i := range f.Devices {
		d := &f.Devices[i]
		parsed, err := uuid.Parse(d.UUID)
		if err != nil {
			metrics.RegistryLoadErrors.Inc()
			return nil, fmt.Errorf("parsing registry: device[%d] %q has invalid UUID %q", i, d.Name, d.UUID)
		}
		d.UUID = parsed.String() // normalise to canonical lowercase form
		if existing, dup := r.devices[d.UUID]; dup {
			// Two entries with the same canonical UUID — file is malformed.
			// Silently picking one would risk un-wiping a device depending on
			// array order; treat it as a load failure instead.
			metrics.RegistryLoadErrors.Inc()
			return nil, fmt.Errorf("parsing registry: duplicate UUID %s (%q and %q)", d.UUID, existing.Name, d.Name)
		}
		r.devices[d.UUID] = d
	}

	metrics.RegistrySize.Set(float64(len(r.devices)))
	logger.Info("registry loaded", slog.Int("devices", len(r.devices)))
	return r, nil
}

// Register adds a device to the registry. Idempotent when called with the same
// name and UUID. Returns ErrUUIDConflict if the UUID is already registered under
// a different name. The UUID is validated and normalised before any map access.
// name must be non-empty.
func (r *Registry) Register(name, id string) error {
	if name == "" {
		return errors.New("device name must not be empty")
	}
	canonical, err := validateUUID(id)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.devices[canonical]; ok {
		if existing.Name == name {
			return nil // idempotent
		}
		return fmt.Errorf("%w: %s registered as %q", ErrUUIDConflict, canonical, existing.Name)
	}
	r.devices[canonical] = &DeviceEntry{Name: name, UUID: canonical}
	if err := r.persist(); err != nil {
		// Without rollback, a retry would see the UUID already in the in-memory map
		// and return ErrUUIDConflict, turning a transient I/O error into a permanent
		// failure. Roll back so the operation is safe to retry.
		delete(r.devices, canonical)
		r.metrics.RegistrySize.Set(float64(len(r.devices)))
		return fmt.Errorf("persisting registry: %w", err)
	}
	// Update gauge inside the lock so concurrent Register calls cannot publish
	// sizes out of order (an older goroutine overwriting a newer value).
	r.metrics.RegistrySize.Set(float64(len(r.devices)))
	return nil
}

// Lookup returns a copy of the DeviceEntry for id. The UUID is validated before
// any map access.
func (r *Registry) Lookup(id string) (*DeviceEntry, error) {
	canonical, err := validateUUID(id)
	if err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	d, ok := r.devices[canonical]
	if !ok {
		return nil, ErrDeviceNotFound
	}
	cp := *d
	return &cp, nil
}

// Devices returns a snapshot of all device entries as value copies. The
// returned slice is independent of the registry's internal state.
func (r *Registry) Devices() []DeviceEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]DeviceEntry, 0, len(r.devices))
	for _, d := range r.devices {
		out = append(out, *d)
	}
	return out
}

// Len returns the number of devices in the registry.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.devices)
}

func validateUUID(id string) (string, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return "", fmt.Errorf("UUID %q: %w", id, ErrInvalidUUID)
	}
	return parsed.String(), nil
}

// persist serialises the registry to disk atomically. Must be called with
// r.mu held for writing. Holding the lock through fsync is intentional: it
// serialises writes and keeps snapshot consistent. Latency is acceptable for
// startup registration operations.
func (r *Registry) persist() error {
	f := registryFile{
		Devices: make([]DeviceEntry, 0, len(r.devices)),
	}
	for _, d := range r.devices {
		f.Devices = append(f.Devices, *d)
	}
	// Sort by UUID for a canonical byte sequence. The HMAC must cover a
	// deterministic representation so the same device set always produces the
	// same bytes regardless of insertion order.
	slices.SortFunc(f.Devices, func(a, b DeviceEntry) int {
		return cmp.Compare(a.UUID, b.UUID)
	})

	data, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("marshaling registry: %w", err)
	}

	// Write registry first, then HMAC sidecar. If the HMAC write fails, the
	// next load will detect the mismatch and fail safely (fail-closed).
	if err := fsutil.Write(r.path, data); err != nil {
		return err
	}
	if r.hmacKey != nil {
		mac := computeHMAC(r.hmacKey, data)
		if err := fsutil.Write(r.path+".hmac", mac); err != nil {
			return fmt.Errorf("writing registry HMAC: %w", err)
		}
	}
	return nil
}

// verifyHMAC reads the HMAC sidecar file and checks it against data.
func verifyHMAC(path string, key, data []byte) error {
	mac, err := os.ReadFile(path + ".hmac") // #nosec G304 — derived from validated config path
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("registry HMAC file missing at %s.hmac: integrity check required", path)
	}
	if err != nil {
		return fmt.Errorf("reading registry HMAC: %w", err)
	}
	expected := computeHMAC(key, data)
	if !hmac.Equal(mac, expected) {
		return fmt.Errorf("registry HMAC mismatch: file may have been tampered")
	}
	return nil
}

// computeHMAC returns HMAC-SHA256(key, data).
func computeHMAC(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}
