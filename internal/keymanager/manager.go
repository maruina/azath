// Package keymanager loads, manages, and derives keys from a master key.
//
// The master key is loaded once from a KeySource and held in memory until
// Destroy zeroes it. Sealers and derived keys use independent copies of the
// key so that in-flight callers are unaffected by a concurrent Destroy.
//
// Lifecycle: unloaded → loaded → destroyed. The destroyed state is terminal;
// Load after Destroy returns ErrDestroyed.
//
// Known limitation — GC zeroing: Manager.Destroy zeros the Manager's key copy,
// and each Sealer.Destroy zeros the Sealer's copy. Go's crypto/aes stores the
// expanded AES key schedule in unexported fields that cannot be zeroed. These
// are accepted residuals documented in the project threat model.
package keymanager

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"golang.org/x/crypto/hkdf"

	"github.com/maruina/azath/internal/crypto"
	"github.com/maruina/azath/internal/observability"
)

// Sentinel errors returned by Manager methods.
var (
	// ErrAlreadyLoaded is returned when Load is called on a Manager that already
	// holds a key.
	ErrAlreadyLoaded = errors.New("master key already loaded")
	// ErrNotLoaded is returned when Sealer or DeriveKey is called before Load.
	ErrNotLoaded = errors.New("master key not loaded")
	// ErrDestroyed is returned when any method is called after Destroy.
	ErrDestroyed = errors.New("key manager has been destroyed")
)

// KeySource is the interface implemented by backends that supply the raw master
// key bytes. The returned slice must be exactly crypto.KeySize bytes. The caller
// (Manager.Load) zeros the returned bytes after copying them; the source must
// not retain any reference to the returned slice after Load returns.
type KeySource interface {
	// Load reads the raw master key bytes. The context may be used for timeouts
	// or cancellation (e.g. a 60-second startup deadline).
	Load(ctx context.Context) ([]byte, error)
	// Name returns a human-readable source identifier for logging. Must not include
	// key bytes, file paths, or secret references.
	Name() string
}

// managerState encodes the three-phase lifecycle of a Manager.
// Using a named type prevents the "check loaded without checking destroyed"
// footgun that dual booleans allow.
type managerState int

const (
	stateUnloaded  managerState = iota // zero value — before Load
	stateLoaded                        // after a successful Load
	stateDestroyed                     // after Destroy; terminal
)

// Manager holds the master key in memory and dispenses Sealers and derived keys.
// It is safe for concurrent use by multiple goroutines.
type Manager struct {
	mu      sync.RWMutex
	key     []byte // nil before Load; zeroed and nil after Destroy
	state   managerState
	metrics *observability.Metrics
	logger  *slog.Logger
}

// New creates an unloaded Manager. metrics must not be nil (panics). If logger
// is nil, log output is discarded.
func New(metrics *observability.Metrics, logger *slog.Logger) *Manager {
	if metrics == nil {
		panic("keymanager.New: metrics must not be nil")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Manager{
		metrics: metrics,
		logger:  logger,
	}
}

// Load reads the master key from src and holds it in memory. Load may be called
// at most once; subsequent calls return ErrAlreadyLoaded. Calling Load after
// Destroy returns ErrDestroyed. The bytes returned by src.Load are zeroed after
// copying regardless of success or failure — including any partial buffer a
// source might return alongside an error.
func (m *Manager) Load(ctx context.Context, src KeySource) error {
	// Fast-path state check: avoid source I/O when the Manager is already loaded
	// or destroyed. A concurrent race is handled by the definitive re-check under
	// the write lock below.
	m.mu.RLock()
	state := m.state
	m.mu.RUnlock()
	if state == stateDestroyed {
		return ErrDestroyed
	}
	if state == stateLoaded {
		return ErrAlreadyLoaded
	}

	raw, err := src.Load(ctx)
	// Zero unconditionally: a source may return partial key bytes alongside a
	// non-nil error (e.g. an age decryptor that fails mid-stream). Those bytes
	// must not survive on the heap. crypto.Zero is a no-op on a nil slice.
	defer crypto.Zero(raw)
	if err != nil {
		m.logger.Error("master key load failed",
			slog.String("source", src.Name()),
			slog.Any("error", err))
		return fmt.Errorf("loading master key from %s: %w", src.Name(), err)
	}
	if len(raw) != crypto.KeySize {
		m.logger.Error("master key wrong size",
			slog.String("source", src.Name()),
			slog.Int("want", crypto.KeySize),
			slog.Int("got", len(raw)))
		return fmt.Errorf("master key: want %d bytes, got %d", crypto.KeySize, len(raw))
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Re-check state under the write lock: another goroutine may have raced.
	if m.state == stateDestroyed {
		return ErrDestroyed
	}
	if m.state == stateLoaded {
		return ErrAlreadyLoaded
	}
	m.key = make([]byte, crypto.KeySize)
	copy(m.key, raw)
	m.state = stateLoaded
	m.metrics.MasterKeyLoaded.Set(1)
	m.logger.Info("master key loaded", slog.String("source", src.Name()))
	return nil
}

// keyCopy returns an independent copy of the master key. The caller must zero
// the returned slice after use. The read lock is held only for the copy.
func (m *Manager) keyCopy() ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.state == stateDestroyed {
		return nil, ErrDestroyed
	}
	if m.state != stateLoaded {
		return nil, ErrNotLoaded
	}
	kc := make([]byte, crypto.KeySize)
	copy(kc, m.key)
	return kc, nil
}

// Sealer creates and returns a new crypto.Sealer backed by an independent copy
// of the master key. The returned Sealer remains valid after Destroy is called
// on this Manager, because it holds its own key copy.
//
// Returns ErrNotLoaded if Load has not been called, ErrDestroyed if Destroy has
// been called.
func (m *Manager) Sealer() (*crypto.Sealer, error) {
	kc, err := m.keyCopy()
	if err != nil {
		return nil, err
	}
	defer crypto.ZeroOnReturn(&kc)
	return crypto.NewSealer(kc)
}

// DeriveKey derives length bytes from the master key using HKDF-SHA256 with the
// given info string as the context. The same key + info + length always produces
// the same output (deterministic). The caller is responsible for zeroing the
// returned bytes after use.
//
// Returns ErrNotLoaded if Load has not been called, ErrDestroyed if Destroy has
// been called.
func (m *Manager) DeriveKey(info string, length int) ([]byte, error) {
	if length <= 0 {
		return nil, fmt.Errorf("DeriveKey: length must be positive, got %d", length)
	}
	kc, err := m.keyCopy()
	if err != nil {
		return nil, err
	}
	// HKDF-SHA256(IKM=master key, salt=nil, info=info). nil salt is intentional:
	// per RFC 5869 §3.1 it defaults to a zero-filled HMAC key of hash length.
	defer crypto.ZeroOnReturn(&kc)
	r := hkdf.New(sha256.New, kc, nil, []byte(info))
	derived := make([]byte, length)
	if _, err := io.ReadFull(r, derived); err != nil {
		m.logger.Error("hkdf derivation failed",
			slog.String("info", info),
			slog.Int("length", length),
			slog.Any("error", err))
		crypto.Zero(derived)
		return nil, fmt.Errorf("hkdf read: %w", err)
	}
	return derived, nil
}

// Destroy zeros the master key held by this Manager. After Destroy, all method
// calls return ErrDestroyed. Destroy is idempotent and safe to call concurrently.
func (m *Manager) Destroy() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state == stateDestroyed {
		return
	}
	crypto.Zero(m.key)
	m.key = nil
	m.state = stateDestroyed
	m.metrics.MasterKeyLoaded.Set(0)
	m.logger.Info("master key destroyed")
}

// Loaded reports whether a key is currently held in memory. Returns false before
// Load, after Destroy, and on Load failure.
func (m *Manager) Loaded() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state == stateLoaded
}
