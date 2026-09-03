package keymanager_test

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/maruina/azath/internal/crypto"
	"github.com/maruina/azath/internal/keymanager"
	"github.com/maruina/azath/internal/observability"
)

// --- helpers ---

func newTestMetrics(t *testing.T) *observability.Metrics {
	t.Helper()
	return observability.NewMetrics()
}

func masterKeyLoadedValue(t *testing.T, m *observability.Metrics) float64 {
	t.Helper()
	mfs, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "azath_master_key_loaded" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			return metric.GetGauge().GetValue()
		}
	}
	return 0
}

func mustLoad(t *testing.T, m *observability.Metrics, src keymanager.KeySource) *keymanager.Manager {
	t.Helper()
	mgr := keymanager.New(m, nil)
	if err := mgr.Load(t.Context(), src); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return mgr
}

func testFileSource(t *testing.T) keymanager.KeySource {
	t.Helper()
	return keymanager.NewFileSource(writeRawFile(t, testKey()))
}

// --- Constructor tests ---

func TestNew_NilMetrics_Panics(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Error("New(nil, nil) did not panic, want panic")
		}
	}()
	keymanager.New(nil, nil)
}

func TestNew_NilLogger_DoesNotPanic(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := keymanager.New(m, nil) // must not panic
	if mgr.Loaded() {
		t.Error("Loaded() = true before Load, want false")
	}
}

func TestNew_InitialState(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := keymanager.New(m, nil)

	if mgr.Loaded() {
		t.Error("Loaded() = true before Load, want false")
	}
	if _, err := mgr.Sealer(); !errors.Is(err, keymanager.ErrNotLoaded) {
		t.Errorf("Sealer() error = %v, want ErrNotLoaded", err)
	}
	if _, err := mgr.DeriveKey("test", 32); !errors.Is(err, keymanager.ErrNotLoaded) {
		t.Errorf("DeriveKey() error = %v, want ErrNotLoaded", err)
	}
	// Gauge must be 0 before load.
	if v := masterKeyLoadedValue(t, m); v != 0 {
		t.Errorf("MasterKeyLoaded gauge = %v before Load, want 0", v)
	}
}

// --- Load tests ---

func TestLoad_Success(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := keymanager.New(m, nil)
	if err := mgr.Load(t.Context(), testFileSource(t)); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !mgr.Loaded() {
		t.Error("Loaded() = false after Load, want true")
	}
	if v := masterKeyLoadedValue(t, m); v != 1 {
		t.Errorf("MasterKeyLoaded gauge = %v after Load, want 1", v)
	}
}

func TestLoad_ZerosSourceBytes(t *testing.T) {
	t.Parallel()
	spy := &spySource{}
	m := newTestMetrics(t)
	mgr := keymanager.New(m, nil)
	if err := mgr.Load(t.Context(), spy); err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The slice that src.Load returned must be zeroed by the Manager.
	for i, b := range spy.returned {
		if b != 0 {
			t.Errorf("source bytes[%d] = %#x after Load, want 0 (not zeroed)", i, b)
		}
	}
}

func TestLoad_AlreadyLoaded(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := mustLoad(t, m, testFileSource(t))
	err := mgr.Load(t.Context(), testFileSource(t))
	if !errors.Is(err, keymanager.ErrAlreadyLoaded) {
		t.Errorf("second Load error = %v, want ErrAlreadyLoaded", err)
	}
}

func TestLoad_AfterDestroy(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := mustLoad(t, m, testFileSource(t))
	mgr.Destroy()
	err := mgr.Load(t.Context(), testFileSource(t))
	if !errors.Is(err, keymanager.ErrDestroyed) {
		t.Errorf("Load after Destroy error = %v, want ErrDestroyed", err)
	}
}

func TestLoad_AfterDestroyWithoutLoad(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := keymanager.New(m, nil)
	mgr.Destroy() // destroy before ever loading
	err := mgr.Load(t.Context(), testFileSource(t))
	if !errors.Is(err, keymanager.ErrDestroyed) {
		t.Errorf("Load after Destroy (never loaded) = %v, want ErrDestroyed", err)
	}
}

func TestLoad_SourceReturnsPartialBytesWithError(t *testing.T) {
	t.Parallel()
	spy := &partialSpySource{err: errors.New("stream interrupted")}
	m := newTestMetrics(t)
	mgr := keymanager.New(m, nil)
	err := mgr.Load(t.Context(), spy)
	if err == nil {
		t.Fatal("Load with error source returned nil, want error")
	}
	if spy.returned == nil {
		t.Fatal("partialSpySource.Load was never called")
	}
	// Partial bytes returned alongside the error must be zeroed.
	for i, b := range spy.returned {
		if b != 0 {
			t.Errorf("partial source bytes[%d] = %#x after Load error, want 0 (not zeroed)", i, b)
		}
	}
}

func TestLoad_SourceError(t *testing.T) {
	t.Parallel()
	errSource := errors.New("source unavailable")
	spy := &spySource{err: errSource}
	m := newTestMetrics(t)
	mgr := keymanager.New(m, nil)
	err := mgr.Load(t.Context(), spy)
	if err == nil {
		t.Fatal("Load with failing source returned nil, want error")
	}
	if !errors.Is(err, errSource) {
		t.Errorf("Load error = %v, want wrapped errSource", err)
	}
	if mgr.Loaded() {
		t.Error("Loaded() = true after failed Load, want false")
	}
	if v := masterKeyLoadedValue(t, m); v != 0 {
		t.Errorf("MasterKeyLoaded gauge = %v after failed Load, want 0", v)
	}
}

func TestLoad_WrongKeySize(t *testing.T) {
	t.Parallel()
	cases := []int{16, 31, 33}
	for _, size := range cases {
		t.Run(fmt.Sprintf("%d bytes", size), func(t *testing.T) {
			t.Parallel()
			path := writeRawFile(t, make([]byte, size))
			m := newTestMetrics(t)
			mgr := keymanager.New(m, nil)
			err := mgr.Load(t.Context(), keymanager.NewFileSource(path))
			if err == nil {
				t.Errorf("Load with %d-byte key returned nil, want error", size)
			}
			if mgr.Loaded() {
				t.Errorf("Loaded() = true after bad-size Load, want false")
			}
		})
	}
}

// --- Sealer tests ---

func TestSealer_Success(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := mustLoad(t, m, testFileSource(t))
	s, err := mgr.Sealer()
	if err != nil {
		t.Fatalf("Sealer: %v", err)
	}
	plaintext := []byte("hello azath")
	sealed, err := s.Seal(plaintext, nil)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := s.Unseal(sealed, nil)
	if err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("Unseal = %q, want %q", got, plaintext)
	}
}

func TestSealer_NotLoaded(t *testing.T) {
	t.Parallel()
	mgr := keymanager.New(newTestMetrics(t), nil)
	_, err := mgr.Sealer()
	if !errors.Is(err, keymanager.ErrNotLoaded) {
		t.Errorf("Sealer() error = %v, want ErrNotLoaded", err)
	}
}

func TestSealer_AfterDestroy(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := mustLoad(t, m, testFileSource(t))
	mgr.Destroy()
	_, err := mgr.Sealer()
	if !errors.Is(err, keymanager.ErrDestroyed) {
		t.Errorf("Sealer() after Destroy error = %v, want ErrDestroyed", err)
	}
}

func TestSealer_IndependentOfManagerDestroy(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := mustLoad(t, m, testFileSource(t))

	s, err := mgr.Sealer()
	if err != nil {
		t.Fatalf("Sealer: %v", err)
	}

	mgr.Destroy()

	// Sealer dispensed before Destroy must still work.
	plaintext := []byte("still alive")
	sealed, err := s.Seal(plaintext, nil)
	if err != nil {
		t.Fatalf("Seal after manager Destroy: %v", err)
	}
	got, err := s.Unseal(sealed, nil)
	if err != nil {
		t.Fatalf("Unseal after manager Destroy: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("Unseal = %q, want %q", got, plaintext)
	}
}

func TestSealer_MultipleCrossUnseal(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := mustLoad(t, m, testFileSource(t))

	s1, err := mgr.Sealer()
	if err != nil {
		t.Fatalf("Sealer (s1): %v", err)
	}
	s2, err := mgr.Sealer()
	if err != nil {
		t.Fatalf("Sealer (s2): %v", err)
	}

	plaintext := []byte("cross unseal test")
	sealed, err := s1.Seal(plaintext, nil)
	if err != nil {
		t.Fatalf("Seal (s1): %v", err)
	}
	got, err := s2.Unseal(sealed, nil)
	if err != nil {
		t.Fatalf("Unseal (s2): %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("s2.Unseal(s1 blob) = %q, want %q", got, plaintext)
	}
}

// --- DeriveKey tests ---

func TestDeriveKey_Deterministic(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := mustLoad(t, m, testFileSource(t))

	first, err := mgr.DeriveKey("azath-test-v1", 32)
	if err != nil {
		t.Fatalf("DeriveKey (first): %v", err)
	}
	defer crypto.Zero(first)
	second, err := mgr.DeriveKey("azath-test-v1", 32)
	if err != nil {
		t.Fatalf("DeriveKey (second): %v", err)
	}
	defer crypto.Zero(second)
	if !bytes.Equal(first, second) {
		t.Errorf("DeriveKey not deterministic:\n got: %x\nwant: %x", second, first)
	}
}

func TestDeriveKey_DifferentInfoDifferentOutput(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := mustLoad(t, m, testFileSource(t))

	keyA, err := mgr.DeriveKey("info-a", 32)
	if err != nil {
		t.Fatalf("DeriveKey (info-a): %v", err)
	}
	defer crypto.Zero(keyA)
	keyB, err := mgr.DeriveKey("info-b", 32)
	if err != nil {
		t.Fatalf("DeriveKey (info-b): %v", err)
	}
	defer crypto.Zero(keyB)
	if bytes.Equal(keyA, keyB) {
		t.Error("DeriveKey with different info strings produced identical output")
	}
}

func TestDeriveKey_InvalidLength(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := mustLoad(t, m, testFileSource(t))
	cases := []struct {
		name   string
		length int
	}{
		{"zero", 0},
		{"negative one", -1},
		{"large negative", -32},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := mgr.DeriveKey("azath-test", tc.length)
			if err == nil {
				t.Errorf("DeriveKey(length=%d) returned nil, want error", tc.length)
			}
		})
	}
}

func TestDeriveKey_NotLoaded(t *testing.T) {
	t.Parallel()
	mgr := keymanager.New(newTestMetrics(t), nil)
	_, err := mgr.DeriveKey("test", 32)
	if !errors.Is(err, keymanager.ErrNotLoaded) {
		t.Errorf("DeriveKey() error = %v, want ErrNotLoaded", err)
	}
}

func TestDeriveKey_AfterDestroy(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := mustLoad(t, m, testFileSource(t))
	mgr.Destroy()
	_, err := mgr.DeriveKey("test", 32)
	if !errors.Is(err, keymanager.ErrDestroyed) {
		t.Errorf("DeriveKey() after Destroy error = %v, want ErrDestroyed", err)
	}
}

func TestDeriveKey_LengthVariation(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := mustLoad(t, m, testFileSource(t))

	full, err := mgr.DeriveKey("azath-hmac-v1", 32)
	if err != nil {
		t.Fatalf("DeriveKey(32): %v", err)
	}
	defer crypto.Zero(full)
	half, err := mgr.DeriveKey("azath-hmac-v1", 16)
	if err != nil {
		t.Fatalf("DeriveKey(16): %v", err)
	}
	defer crypto.Zero(half)
	// HKDF is a stream: the first 16 bytes of a 32-byte derivation equal the
	// output of a 16-byte derivation with the same key + info.
	if !bytes.Equal(half, full[:len(half)]) {
		t.Errorf("DeriveKey(16) = %x, want first 16 bytes of DeriveKey(32) = %x", half, full[:len(half)])
	}
}

// --- Destroy tests ---

func TestDestroy_ZerosKey(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := mustLoad(t, m, testFileSource(t))

	// Capture a reference to the live key slice before Destroy.
	keyRef := keymanager.KeyBytesForTesting(mgr)
	if len(keyRef) == 0 {
		t.Fatal("KeyBytesForTesting returned empty slice")
	}

	mgr.Destroy()

	// keyRef now points to the underlying array; all bytes must be zero.
	for i, b := range keyRef {
		if b != 0 {
			t.Errorf("key byte %d = %#x after Destroy, want 0", i, b)
		}
	}
}

func TestDestroy_Idempotent(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := mustLoad(t, m, testFileSource(t))
	mgr.Destroy()
	mgr.Destroy() // must not panic
}

func TestDestroy_GaugeToggle(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := mustLoad(t, m, testFileSource(t))
	if v := masterKeyLoadedValue(t, m); v != 1 {
		t.Errorf("MasterKeyLoaded gauge = %v after Load, want 1", v)
	}
	mgr.Destroy()
	if v := masterKeyLoadedValue(t, m); v != 0 {
		t.Errorf("MasterKeyLoaded gauge = %v after Destroy, want 0", v)
	}
}

func TestDestroy_LoadedReturnsFalse(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := mustLoad(t, m, testFileSource(t))
	mgr.Destroy()
	if mgr.Loaded() {
		t.Error("Loaded() = true after Destroy, want false")
	}
}

// --- Concurrency tests ---

func TestConcurrent_SealerAndDestroy(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := mustLoad(t, m, testFileSource(t))

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines + 1)
	ready := make(chan struct{})

	go func() {
		defer wg.Done()
		<-ready
		mgr.Destroy()
	}()

	for range goroutines {
		go func() {
			defer wg.Done()
			<-ready
			s, err := mgr.Sealer()
			if err != nil {
				return
			}
			_, _ = s.Seal([]byte("test"), nil)
		}()
	}
	close(ready)
	wg.Wait()
}

func TestConcurrent_DeriveKeyAndDestroy(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := mustLoad(t, m, testFileSource(t))

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines + 1)
	ready := make(chan struct{})

	go func() {
		defer wg.Done()
		<-ready
		mgr.Destroy()
	}()

	for range goroutines {
		go func() {
			defer wg.Done()
			<-ready
			key, err := mgr.DeriveKey("azath-hmac-v1", 32)
			if err != nil {
				return
			}
			crypto.Zero(key)
		}()
	}
	close(ready)
	wg.Wait()
}

func TestConcurrent_MultipleDestroys(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := mustLoad(t, m, testFileSource(t))

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			mgr.Destroy() // must not panic
		}()
	}
	wg.Wait()
}

func TestConcurrent_LoadAndSealer(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := keymanager.New(m, nil)
	src := testFileSource(t)

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines + 1)

	go func() {
		defer wg.Done()
		_ = mgr.Load(t.Context(), src)
	}()

	for range goroutines {
		go func() {
			defer wg.Done()
			// May get ErrNotLoaded (race) or a valid Sealer.
			s, err := mgr.Sealer()
			if err != nil {
				return
			}
			_, _ = s.Seal([]byte("concurrent"), nil)
		}()
	}
	wg.Wait()
}
