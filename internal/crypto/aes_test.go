package crypto_test

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	"github.com/maruina/azath/internal/crypto"
)

// newTestSealer creates a Sealer with a deterministic 32-byte key for testing.
func newTestSealer(t *testing.T) *crypto.Sealer {
	t.Helper()
	key := make([]byte, crypto.KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	return newTestSealerFromKey(t, key)
}

// newTestSealerFromKey creates a Sealer with a specific key.
func newTestSealerFromKey(t *testing.T, key []byte) *crypto.Sealer {
	t.Helper()
	s, err := crypto.NewSealer(key)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	return s
}

// --- Construction tests ---

func TestNewSealer_ValidKey(t *testing.T) {
	t.Parallel()
	key := make([]byte, crypto.KeySize)
	s, err := crypto.NewSealer(key)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	if s == nil {
		t.Fatal("NewSealer returned nil Sealer")
	}
}

func TestNewSealer_WrongKeySize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		size int
	}{
		{"zero", 0},
		{"16 bytes (AES-128)", 16},
		{"24 bytes (AES-192)", 24},
		{"31 bytes", 31},
		{"33 bytes", 33},
		{"64 bytes", 64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := crypto.NewSealer(make([]byte, tc.size))
			if err == nil {
				t.Fatalf("NewSealer(%d bytes) expected error, got nil", tc.size)
			}
		})
	}
}

func TestNewSealer_NilKey(t *testing.T) {
	t.Parallel()
	_, err := crypto.NewSealer(nil)
	if err == nil {
		t.Fatal("NewSealer(nil) expected error, got nil")
	}
}

func TestNewSealer_CopiesKey(t *testing.T) {
	t.Parallel()
	key := make([]byte, crypto.KeySize)
	key[0] = 0xAA
	s := newTestSealerFromKey(t, key)

	key[0] = 0x00

	// Sealer must still work with the original key (tag derived from 0xAA key).
	plaintext := []byte("hello")
	sealed, err := s.Seal(plaintext, nil)
	if err != nil {
		t.Fatalf("Seal after caller key mutation: %v", err)
	}
	got, err := s.Unseal(sealed, nil)
	if err != nil {
		t.Fatalf("Unseal after caller key mutation: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("round-trip mismatch: got %q, want %q", got, plaintext)
	}
}

// --- Round-trip tests ---

func TestSealUnseal_RoundTrip(t *testing.T) {
	t.Parallel()
	s := newTestSealer(t)
	plaintext := []byte("the quick brown fox")
	aad := []byte("device-uuid-1234")

	sealed, err := s.Seal(plaintext, aad)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	got, err := s.Unseal(sealed, aad)
	if err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("Unseal() got %q, want %q", got, plaintext)
	}
}

func TestSealUnseal_EmptyPlaintext(t *testing.T) {
	t.Parallel()
	s := newTestSealer(t)
	sealed, err := s.Seal([]byte{}, nil)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if len(sealed) != crypto.MinSealedLen {
		t.Errorf("sealed len = %d, want %d (MinSealedLen)", len(sealed), crypto.MinSealedLen)
	}
	got, err := s.Unseal(sealed, nil)
	if err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %q, want empty", got)
	}
}

func TestSealUnseal_LargePlaintext(t *testing.T) {
	t.Parallel()
	s := newTestSealer(t)
	plaintext := make([]byte, 1<<20) // 1 MB
	for i := range plaintext {
		plaintext[i] = byte(i)
	}
	sealed, err := s.Seal(plaintext, nil)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := s.Unseal(sealed, nil)
	if err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if len(got) != len(plaintext) {
		t.Fatalf("Unseal() len = %d, want %d", len(got), len(plaintext))
	}
	if !bytes.Equal(got, plaintext) {
		for i, b := range got {
			if b != plaintext[i] {
				t.Errorf("Unseal() first byte mismatch at [%d]: got %#x, want %#x", i, b, plaintext[i])
				break
			}
		}
	}
}

func TestSealUnseal_NilAAD(t *testing.T) {
	t.Parallel()
	s := newTestSealer(t)
	sealed, err := s.Seal([]byte("data"), nil)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err = s.Unseal(sealed, nil); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
}

func TestSealUnseal_EmptyAAD(t *testing.T) {
	t.Parallel()
	s := newTestSealer(t)
	sealed, err := s.Seal([]byte("data"), []byte{})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// nil and empty AAD are treated identically by AES-GCM.
	if _, err = s.Unseal(sealed, nil); err != nil {
		t.Fatalf("Unseal with nil AAD after empty-AAD Seal: %v", err)
	}
}

// --- Failure / rejection tests ---

func TestUnseal_AADMismatch(t *testing.T) {
	t.Parallel()
	s := newTestSealer(t)
	sealed, err := s.Seal([]byte("secret"), []byte("aad-a"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// Instance tag matched, but GCM auth fails due to AAD mismatch.
	_, err = s.Unseal(sealed, []byte("aad-b"))
	if !errors.Is(err, crypto.ErrDecrypt) {
		t.Errorf("err = %v, want ErrDecrypt", err)
	}
}

func TestUnseal_Tampered(t *testing.T) {
	t.Parallel()
	s := newTestSealer(t)
	sealed, err := s.Seal([]byte("secret"), nil)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	cases := []struct {
		name    string
		offset  int
		wantErr error
	}{
		// Blob layout: [0:4] instance tag | [4:16] nonce | [16:] ciphertext+GCM tag
		{"instance tag (offset 0)", 0, crypto.ErrWrongInstance},
		{"nonce (offset 4)", 4, crypto.ErrDecrypt},
		{"ciphertext (offset 16)", 16, crypto.ErrDecrypt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tampered := bytes.Clone(sealed)
			tampered[tc.offset] ^= 0xFF
			_, err := s.Unseal(tampered, nil)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestUnseal_TruncatedBlob(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		size int
	}{
		{"zero", 0},
		{"one byte", 1},
		{"16 bytes", 16},
		{"MinSealedLen-1", crypto.MinSealedLen - 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newTestSealer(t)
			blob := make([]byte, tc.size)
			_, err := s.Unseal(blob, nil)
			if !errors.Is(err, crypto.ErrShortBlob) {
				t.Errorf("err = %v, want ErrShortBlob", err)
			}
		})
	}
}

func TestUnseal_MinSealedLen_NotShortBlob(t *testing.T) {
	t.Parallel()
	// A blob of exactly MinSealedLen bytes must NOT return ErrShortBlob —
	// it reaches the instance tag check and AES-GCM decryption path.
	s := newTestSealer(t)
	blob := make([]byte, crypto.MinSealedLen) // all-zero: tag won't match
	_, err := s.Unseal(blob, nil)
	if errors.Is(err, crypto.ErrShortBlob) {
		t.Errorf("Unseal(MinSealedLen bytes) returned ErrShortBlob, want ErrWrongInstance")
	}
	if err == nil {
		t.Errorf("Unseal(MinSealedLen zero bytes) succeeded, want error")
	}
}

func TestUnseal_NilBlob(t *testing.T) {
	t.Parallel()
	s := newTestSealer(t)
	_, err := s.Unseal(nil, nil)
	if !errors.Is(err, crypto.ErrShortBlob) {
		t.Errorf("err = %v, want ErrShortBlob", err)
	}
}

func TestUnseal_WrongKey(t *testing.T) {
	t.Parallel()
	keyA := make([]byte, crypto.KeySize)
	keyB := make([]byte, crypto.KeySize)
	for i := range keyB {
		keyB[i] = 0xFF
	}
	sA := newTestSealerFromKey(t, keyA)
	sB := newTestSealerFromKey(t, keyB)

	sealed, err := sA.Seal([]byte("secret"), nil)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Unsealing with a different key must fail with ErrWrongInstance
	// (different instance tags derived from different keys).
	_, err = sB.Unseal(sealed, nil)
	if !errors.Is(err, crypto.ErrWrongInstance) {
		t.Errorf("err = %v, want ErrWrongInstance", err)
	}
}

// --- Instance tag tests ---

func TestInstanceTag_Deterministic(t *testing.T) {
	t.Parallel()
	key := make([]byte, crypto.KeySize)
	s1 := newTestSealerFromKey(t, key)
	s2 := newTestSealerFromKey(t, key)

	tag1, tag2 := s1.InstanceTag(), s2.InstanceTag()
	if tag1 != tag2 {
		t.Errorf("InstanceTag() mismatch for same key: s1=%x, s2=%x", tag1, tag2)
	}
}

func TestInstanceTag_DifferentKeys(t *testing.T) {
	t.Parallel()
	keyA := make([]byte, crypto.KeySize)
	keyB := make([]byte, crypto.KeySize)
	keyB[0] = 0x01

	sA := newTestSealerFromKey(t, keyA)
	sB := newTestSealerFromKey(t, keyB)

	tagA, tagB := sA.InstanceTag(), sB.InstanceTag()
	if tagA == tagB {
		t.Errorf("InstanceTag() = %x for both keys, want distinct values", tagA)
	}
}

func TestInstanceTag_ZeroAfterDestroy(t *testing.T) {
	t.Parallel()
	s := newTestSealer(t)
	before := s.InstanceTag()

	// Verify the tag is non-zero before Destroy.
	var zero [4]byte
	if before == zero {
		t.Fatal("InstanceTag before Destroy is all-zero — test setup wrong")
	}

	s.Destroy()

	if got := s.InstanceTag(); got != zero {
		t.Errorf("InstanceTag() after Destroy = %x, want zero", got)
	}
}

// --- Nonce uniqueness ---

func TestSeal_NonceUniqueness(t *testing.T) {
	t.Parallel()
	const n = 1000
	s := newTestSealer(t)
	nonces := make(map[[12]byte]struct{}, n)

	for i := range n {
		sealed, err := s.Seal([]byte("x"), nil)
		if err != nil {
			t.Fatalf("Seal[%d]: %v", i, err)
		}
		// Nonce is at bytes [4:16].
		var nonce [12]byte
		copy(nonce[:], sealed[4:16])
		if _, dup := nonces[nonce]; dup {
			t.Fatalf("nonce collision at seal %d", i)
		}
		nonces[nonce] = struct{}{}
	}
}

// --- Destroy / lifecycle tests ---

func TestDestroy_ZerosInstanceTag(t *testing.T) {
	t.Parallel()
	s := newTestSealer(t)

	before := crypto.InstanceTagBytesForTesting(s)
	var zero [4]byte
	if before == zero {
		t.Fatal("instanceTag is all-zero before Destroy — test setup wrong")
	}

	s.Destroy()

	// Verify the underlying memory is zeroed, not just the public API return value.
	if got := crypto.InstanceTagBytesForTesting(s); got != zero {
		t.Errorf("instanceTag bytes after Destroy = %x, want zero", got)
	}
}

func TestDestroy_ZerosKey(t *testing.T) {
	t.Parallel()
	s := newTestSealer(t)

	// keyBeforeDestroy holds the slice header (ptr, len, cap) before Destroy.
	// The ptr keeps the backing array reachable even after s.key is nilled.
	keyBeforeDestroy := crypto.KeyBytesForTesting(s)

	allZero := true
	for _, b := range keyBeforeDestroy {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Fatal("key is all zeros before Destroy — test setup is wrong")
	}

	s.Destroy()

	if got := crypto.KeyBytesForTesting(s); got != nil {
		t.Errorf("KeyBytesForTesting after Destroy = %v, want nil", got)
	}
	for i, b := range keyBeforeDestroy {
		if b != 0 {
			t.Errorf("key byte[%d] = %#x after Destroy, want 0x00", i, b)
		}
	}
}

func TestDestroy_SealAfterDestroy(t *testing.T) {
	t.Parallel()
	s := newTestSealer(t)
	s.Destroy()

	_, err := s.Seal([]byte("x"), nil)
	if !errors.Is(err, crypto.ErrDestroyed) {
		t.Errorf("Seal after Destroy: err = %v, want ErrDestroyed", err)
	}
}

func TestDestroy_UnsealAfterDestroy(t *testing.T) {
	t.Parallel()
	s := newTestSealer(t)
	// Seal before Destroy to get a valid blob.
	sealed, err := s.Seal([]byte("x"), nil)
	if err != nil {
		t.Fatalf("Seal (setup): %v", err)
	}

	s.Destroy()

	_, err = s.Unseal(sealed, nil)
	if !errors.Is(err, crypto.ErrDestroyed) {
		t.Errorf("Unseal after Destroy: err = %v, want ErrDestroyed", err)
	}
}

func TestDestroy_Idempotent(t *testing.T) {
	t.Parallel()
	s := newTestSealer(t)
	// Must not panic.
	s.Destroy()
	s.Destroy()
	s.Destroy()
}

// --- Error message safety ---

func TestErrors_NoKeyMaterial(t *testing.T) {
	t.Parallel()
	// Verify exact sentinel strings — protects against accidental changes that
	// could embed internal state or key material in error messages.
	cases := []struct {
		err  error
		want string
	}{
		{crypto.ErrDestroyed, "sealer has been destroyed"},
		{crypto.ErrWrongInstance, "wrong sealer instance"},
		{crypto.ErrDecrypt, "decryption failed"},
		{crypto.ErrShortBlob, "sealed data too short"},
	}
	for _, tc := range cases {
		if tc.err.Error() != tc.want {
			t.Errorf("%T.Error() = %q, want %q", tc.err, tc.err.Error(), tc.want)
		}
	}
}

// --- Concurrency tests ---

func TestSeal_Concurrent(t *testing.T) {
	t.Parallel()
	s := newTestSealer(t)
	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			_, err := s.Seal([]byte("concurrent"), nil)
			if err != nil {
				t.Errorf("Seal[goroutine %d] = %v, want nil", i, err)
			}
		}(i)
	}
	wg.Wait()
}

func TestUnseal_Concurrent(t *testing.T) {
	t.Parallel()
	s := newTestSealer(t)
	sealed, err := s.Seal([]byte("concurrent"), nil)
	if err != nil {
		t.Fatalf("Seal (setup): %v", err)
	}
	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			_, err := s.Unseal(sealed, nil)
			if err != nil {
				t.Errorf("Unseal[goroutine %d]: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
}

func TestDestroy_ConcurrentWithSeal(t *testing.T) {
	t.Parallel()
	const goroutines = 100

	// Run multiple times to increase race-detector coverage.
	for range 5 {
		s := newTestSealer(t)
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for i := range goroutines {
			go func(i int) {
				defer wg.Done()
				if i%2 == 0 {
					s.Destroy()
				} else {
					_, _ = s.Seal([]byte("x"), nil)
				}
			}(i)
		}
		wg.Wait()
		// Final state: must be destroyed.
		_, err := s.Seal([]byte("x"), nil)
		if !errors.Is(err, crypto.ErrDestroyed) {
			t.Errorf("Seal after concurrent Destroy: err = %v, want ErrDestroyed", err)
		}
	}
}

func TestDestroy_ConcurrentDouble(t *testing.T) {
	t.Parallel()
	s := newTestSealer(t)
	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			s.Destroy() // must not panic
		}()
	}
	wg.Wait()
}
