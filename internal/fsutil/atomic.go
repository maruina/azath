// Package fsutil provides atomic filesystem write utilities.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write writes data to path atomically: a temp file in the same directory is
// created (0600), written, fsynced, renamed over the target, and the parent
// directory is fsynced to make the new directory entry durable.
// A crash at any point leaves either the old or new file fully intact.
func Write(path string, data []byte) (retErr error) {
	dir := filepath.Dir(path)
	tmp, createErr := os.CreateTemp(dir, ".azath-*.tmp")
	if createErr != nil {
		return fmt.Errorf("creating temp file: %w", createErr)
	}
	// Remove temp file on any error; on success it has been renamed away.
	defer func() {
		if retErr != nil {
			_ = os.Remove(tmp.Name())
		}
	}()

	// Enforce 0600 regardless of process umask.
	if chmodErr := tmp.Chmod(0o600); chmodErr != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting temp file permissions: %w", chmodErr)
	}
	if _, writeErr := tmp.Write(data); writeErr != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing temp file: %w", writeErr)
	}
	if syncErr := tmp.Sync(); syncErr != nil {
		_ = tmp.Close()
		return fmt.Errorf("syncing temp file: %w", syncErr)
	}
	if closeErr := tmp.Close(); closeErr != nil {
		return fmt.Errorf("closing temp file: %w", closeErr)
	}
	if renameErr := os.Rename(tmp.Name(), path); renameErr != nil {
		return fmt.Errorf("renaming temp file: %w", renameErr)
	}
	// Fsync the parent directory so the new directory entry is durable.
	// Without this, a crash after Rename but before the OS flushing the
	// directory write-back can silently lose the file.
	dir2, openErr := os.Open(dir) // #nosec G304 — dir is filepath.Dir of a caller-validated path
	if openErr != nil {
		return fmt.Errorf("opening dir for fsync: %w", openErr)
	}
	if syncErr := dir2.Sync(); syncErr != nil {
		_ = dir2.Close()
		return fmt.Errorf("fsyncing dir: %w", syncErr)
	}
	if closeErr := dir2.Close(); closeErr != nil {
		return fmt.Errorf("closing dir: %w", closeErr)
	}
	return nil
}
