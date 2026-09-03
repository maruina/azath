package keymanager_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/maruina/azath/internal/crypto"
	"github.com/maruina/azath/internal/keymanager"
)

func TestFileSource_Load_Success(t *testing.T) {
	t.Parallel()
	key := testKey()
	src := keymanager.NewFileSource(writeRawFile(t, key))
	got, err := src.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !bytes.Equal(got, key) {
		t.Fatalf("Load: got %x, want %x", got, key)
	}
}

func TestFileSource_Load_MissingFile(t *testing.T) {
	t.Parallel()
	src := keymanager.NewFileSource(filepath.Join(t.TempDir(), "missing.key"))
	_, err := src.Load(t.Context())
	if err == nil {
		t.Error("Load on missing file returned nil, want error")
	}
}

func TestFileSource_Load_WrongSize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		size int
	}{
		{"zero", 0},
		{"too short 16", 16},
		{"too short 31", 31},
		{"too long 33", 33},
		{"too long 64", 64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := make([]byte, tc.size)
			path := writeRawFile(t, data)
			src := keymanager.NewFileSource(path)
			got, err := src.Load(t.Context())
			if err == nil {
				t.Errorf("Load with %d-byte file returned nil, want error", tc.size)
			}
			if got != nil {
				t.Errorf("Load with %d-byte file returned non-nil bytes, want nil", tc.size)
			}
		})
	}
}

func TestFileSource_Name(t *testing.T) {
	t.Parallel()
	src := keymanager.NewFileSource("/some/path/key")
	if got := src.Name(); got != "file" {
		t.Errorf("Name() = %q, want %q", got, "file")
	}
}

// writeRawFile writes data to a new temp file and returns the path.
func writeRawFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.key")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// testKey returns a deterministic 32-byte key (0x00..0x1F) for tests.
func testKey() []byte {
	key := make([]byte, crypto.KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

// spySource is a KeySource that retains the reference to the slice it returns,
// so tests can verify the Manager zeroed it after copying.
type spySource struct {
	returned []byte
	err      error
	name     string
}

func (s *spySource) Load(_ context.Context) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := testKey()
	s.returned = out
	return out, nil
}

func (s *spySource) Name() string {
	if s.name != "" {
		return s.name
	}
	return "spy"
}

// partialSpySource returns partial key bytes alongside an error, simulating a
// source that reads some key material before failing (e.g. an age decryptor
// that errors mid-stream). The returned slice is retained for zeroing verification.
type partialSpySource struct {
	returned []byte
	err      error
}

func (s *partialSpySource) Load(_ context.Context) ([]byte, error) {
	out := make([]byte, 16) // intentionally wrong size — not crypto.KeySize
	for i := range out {
		out[i] = byte(i + 1)
	}
	s.returned = out
	return out, s.err
}

func (s *partialSpySource) Name() string { return "partial-spy" }
