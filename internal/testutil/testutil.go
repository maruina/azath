// Package testutil provides shared test helpers.
package testutil

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/maruina/azath/internal/crypto"
)

// WriteConfig writes YAML content to a temp file and returns the path.
func WriteConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return path
}

// WriteKeyFile writes a random 32-byte key file at keyPath with mode 0o600.
// Used by tests that need a valid master key file on disk.
func WriteKeyFile(t *testing.T, keyPath string) {
	t.Helper()
	key := make([]byte, 32)
	defer crypto.Zero(key)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("generating key bytes: %v", err)
	}
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatalf("writing key file: %v", err)
	}
}
