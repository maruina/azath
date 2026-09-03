//go:build !linux

package platform

import "log/slog"

// LockMemory is a no-op on non-Linux platforms. Memory locking via mlockall
// is a Linux-specific capability; on macOS and other platforms this function
// returns false without attempting any syscall.
func LockMemory(_ *slog.Logger) (mlocked bool) {
	return false
}
