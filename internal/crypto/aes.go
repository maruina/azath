// Package crypto provides AES-256-GCM encryption and key-zeroing utilities
// for the azath KMS server.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/hkdf"
)

// Key and blob size constants.
const (
	// KeySize is the required AES-256 key length in bytes.
	KeySize = 32

	instanceTagSize = 4
	nonceSize       = 12
	gcmTagSize      = 16

	// MinSealedLen is the minimum length of a sealed blob (empty plaintext):
	//   4 (instance tag) + 12 (nonce) + 16 (GCM auth tag) = 32 bytes.
	MinSealedLen = instanceTagSize + nonceSize + gcmTagSize

	instanceTagInfo = "azath-instance-tag-v1"
)

// Sentinel errors. Messages are intentionally vague to avoid leaking internal state.
var (
	ErrDestroyed     = errors.New("sealer has been destroyed")
	ErrWrongInstance = errors.New("wrong sealer instance")
	ErrDecrypt       = errors.New("decryption failed")
	ErrShortBlob     = errors.New("sealed data too short")
)

// Sealer performs AES-256-GCM encryption and decryption with an instance tag.
//
// Blob format:
//
//	Offset  Length  Field
//	0       4       Instance tag (HKDF-SHA256(key, "azath-instance-tag-v1")[:4])
//	4       12      Nonce (4-byte random prefix || 8-byte monotonic counter)
//	16      N+16    Ciphertext (N bytes) + GCM authentication tag (16 bytes)
//
//	Total sealed length = len(plaintext) + 32
//	Minimum sealed length (empty plaintext) = 32 bytes (MinSealedLen)
//
// The instance tag lets callers distinguish "blob from a different KMS instance"
// from "decrypt failure" without attempting decryption. It is a routing/diagnostic
// signal, not a security feature — authentication is provided by AES-GCM.
//
// Nonce construction: each Sealer has a 4-byte random prefix (set at construction)
// combined with a monotonically incrementing 8-byte counter (randomly seeded).
// This eliminates the birthday bound of random nonces while keeping zero
// per-call overhead. Nonces are unique within a Sealer's lifetime; the random
// seed ensures negligible collision probability (~2^-64) across restarts with
// the same key.
//
// Known limitation: Go's crypto/aes stores the expanded AES key schedule in an
// unexported field that cannot be zeroed. Destroy zeros this Sealer's key copy,
// the instance tag, and the nonce prefix, and nils the AEAD reference, but the
// expanded schedule in the underlying cipher.Block may remain in memory until
// GC reclaims it.
//
// Sealer is safe for concurrent use by multiple goroutines.
type Sealer struct {
	mu          sync.RWMutex
	key         []byte
	instanceTag [instanceTagSize]byte
	aead        cipher.AEAD
	destroyed   bool
	noncePrefix [4]byte       // random 4-byte prefix, unique per Sealer instance
	nonceCtr    atomic.Uint64 // randomly seeded; incremented once per Seal call
}

// NewSealer creates a Sealer with an independent copy of key.
// key must be exactly KeySize (32) bytes. The caller may zero its own copy
// of key after this call returns — the Sealer holds its own copy.
func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("key must be %d bytes, got %d", KeySize, len(key))
	}

	// Copy key — Sealer owns this copy for independent zeroing.
	ownKey := make([]byte, KeySize)
	copy(ownKey, key)

	// Derive instance tag: HKDF-SHA256(key, nil salt, info), read 4 bytes.
	// nil salt is intentional: per RFC 5869 §3.1 it defaults to a zero-filled
	// HMAC key of hash length, which is still a secure PRF. The instance tag
	// is a routing hint, not a secret — this is acceptable.
	tagReader := hkdf.New(sha256.New, ownKey, nil, []byte(instanceTagInfo))
	var tag [instanceTagSize]byte
	if _, err := io.ReadFull(tagReader, tag[:]); err != nil {
		Zero(ownKey)
		return nil, fmt.Errorf("hkdf read for instance tag: %w", err)
	}

	block, err := aes.NewCipher(ownKey)
	if err != nil {
		Zero(ownKey)
		return nil, fmt.Errorf("creating AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		Zero(ownKey)
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	// Seed the nonce counter with 12 random bytes: 4 for the per-instance prefix
	// and 8 for the counter starting value. Random seeding means that even if
	// the same key is reloaded after a restart, nonce collision probability is ~2^-64.
	var nonceSeed [nonceSize]byte
	if _, err := rand.Read(nonceSeed[:]); err != nil {
		Zero(ownKey)
		return nil, fmt.Errorf("seeding nonce: %w", err)
	}

	s := &Sealer{
		key:         ownKey,
		instanceTag: tag,
		aead:        aead,
	}
	copy(s.noncePrefix[:], nonceSeed[:4])
	s.nonceCtr.Store(binary.LittleEndian.Uint64(nonceSeed[4:]))
	return s, nil
}

// Seal encrypts plaintext with the given additional authenticated data and
// returns the sealed blob. Each call advances the nonce counter atomically.
func (s *Sealer) Seal(plaintext, aad []byte) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.destroyed {
		return nil, ErrDestroyed
	}
	// Capture aead under the read lock. Destroy holds the write lock when it
	// nils aead, so a concurrent Seal that passed the destroyed check cannot
	// observe a nil aead here — the write lock cannot be granted while we hold
	// the read lock.
	aead := s.aead

	// Nonce = 4-byte instance prefix || 8-byte monotonic counter.
	// The atomic increment ensures uniqueness across concurrent Seal calls.
	ctr := s.nonceCtr.Add(1)
	var nonce [nonceSize]byte
	copy(nonce[:4], s.noncePrefix[:])
	binary.LittleEndian.PutUint64(nonce[4:], ctr)

	out := make([]byte, 0, instanceTagSize+nonceSize+len(plaintext)+gcmTagSize)
	out = append(out, s.instanceTag[:]...)
	out = append(out, nonce[:]...)
	out = aead.Seal(out, nonce[:], plaintext, aad)
	return out, nil
}

// Unseal decrypts a sealed blob produced by Seal.
//
// On success it returns the plaintext. On failure the returned error is one of:
//
//   - ErrDestroyed: this Sealer has been destroyed.
//   - ErrShortBlob: the blob is too short to contain the required header.
//   - ErrWrongInstance: the blob's instance tag does not match this Sealer's key.
//     Both ErrShortBlob and ErrWrongInstance mean the blob belongs to a different
//     KMS instance; callers should log reason=wrong_instance and return random bytes.
//   - ErrDecrypt: the instance tag matched but GCM authentication failed
//     (tampered ciphertext or wrong AAD). Callers should log reason=decrypt_error.
func (s *Sealer) Unseal(sealed, aad []byte) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.destroyed {
		return nil, ErrDestroyed
	}

	if len(sealed) < MinSealedLen {
		return nil, ErrShortBlob
	}

	// Check instance tag before attempting AES-GCM decryption.
	if subtle.ConstantTimeCompare(sealed[:instanceTagSize], s.instanceTag[:]) != 1 {
		return nil, ErrWrongInstance
	}

	// Capture aead under the read lock for the same reason as in Seal.
	aead := s.aead

	nonce := sealed[instanceTagSize : instanceTagSize+nonceSize]
	ciphertext := sealed[instanceTagSize+nonceSize:]

	plaintext, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		// Do not wrap aead.Open error — it may contain internal details.
		return nil, ErrDecrypt
	}
	return plaintext, nil
}

// Destroy zeros the key material held by this Sealer. After Destroy, all
// calls to Seal and Unseal return ErrDestroyed. Destroy is idempotent and
// safe to call concurrently with Seal and Unseal.
func (s *Sealer) Destroy() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.destroyed {
		return
	}
	Zero(s.key)
	s.key = nil                             // nil after zeroing makes the destroyed state unambiguous
	s.aead = nil                            // prevents use of stale expanded key schedule
	s.instanceTag = [instanceTagSize]byte{} // zero key-derived tag from memory
	s.noncePrefix = [4]byte{}               // zero nonce prefix
	s.nonceCtr.Store(0)                     // zero counter (not secret, but consistent with zeroing all state)
	s.destroyed = true
}

// InstanceTag returns a copy of the 4-byte instance tag derived from the key.
// Returns the zero value after Destroy.
func (s *Sealer) InstanceTag() [instanceTagSize]byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.destroyed {
		return [instanceTagSize]byte{}
	}
	return s.instanceTag
}
