package crypto_test

import (
	"testing"

	"github.com/maruina/azath/internal/crypto"
)

func TestZero_ClearsBytes(t *testing.T) {
	t.Parallel()
	b := []byte{0xff, 0xfe, 0x01, 0x02}
	crypto.Zero(b)
	for i, v := range b {
		if v != 0 {
			t.Errorf("byte[%d] = %#x, want 0x00", i, v)
		}
	}
}

func TestZero_EmptySlice(t *testing.T) {
	t.Parallel()
	// Must not panic.
	crypto.Zero([]byte{})
}

func TestZero_NilSlice(t *testing.T) {
	t.Parallel()
	// clear(nil) is a no-op in Go 1.21+; must not panic.
	crypto.Zero(nil)
}

func TestZeroOnReturn_ZerosSlice(t *testing.T) {
	t.Parallel()
	b := []byte{1, 2, 3}
	crypto.ZeroOnReturn(&b)
	for i, v := range b {
		if v != 0 {
			t.Errorf("byte[%d] = %d, want 0", i, v)
		}
	}
}

func TestZeroOnReturn_NilPointer(t *testing.T) {
	t.Parallel()
	// Must not panic.
	crypto.ZeroOnReturn(nil)
}

func TestZeroOnReturn_NilSlice(t *testing.T) {
	t.Parallel()
	var b []byte
	// Must not panic.
	crypto.ZeroOnReturn(&b)
}

func TestZeroOnReturn_DeferPattern(t *testing.T) {
	t.Parallel()
	// Verify the defer pattern works as intended: ZeroOnReturn captures the
	// pointer so it zeros the slice that exists at return time.
	b := make([]byte, 4)
	func() {
		b[0] = 0xAB
		defer crypto.ZeroOnReturn(&b)
	}()
	for i, v := range b {
		if v != 0 {
			t.Errorf("byte[%d] = %#x after deferred zero, want 0x00", i, v)
		}
	}
}
