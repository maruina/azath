package keymanager

import (
	"context"
	"fmt"
	"os"

	"github.com/maruina/azath/internal/crypto"
)

// FileSource reads a raw 32-byte key from a file on disk. The key file must be
// mode 0600 and owned by the server process user. For stronger key protection,
// use a secret-manager-backed KeySource instead.
type FileSource struct {
	path string
}

// NewFileSource creates a FileSource that reads from path.
func NewFileSource(path string) *FileSource {
	return &FileSource{path: path}
}

// Load reads the key file and returns its contents. Returns an error if the
// file is missing or not exactly crypto.KeySize bytes. The caller is responsible
// for zeroing the returned slice.
func (f *FileSource) Load(_ context.Context) ([]byte, error) {
	data, err := os.ReadFile(f.path) // #nosec G304 — path comes from validated config
	if err != nil {
		return nil, fmt.Errorf("reading key file: %w", err)
	}
	if len(data) != crypto.KeySize {
		crypto.Zero(data)
		return nil, fmt.Errorf("key file: want %d bytes, got %d", crypto.KeySize, len(data))
	}
	return data, nil
}

// Name returns "file". Does not include the path to avoid leaking filesystem
// layout into logs.
func (f *FileSource) Name() string {
	return "file"
}
