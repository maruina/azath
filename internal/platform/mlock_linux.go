//go:build linux

// Package platform provides OS-specific memory safety operations.
package platform

import (
	"log/slog"

	"golang.org/x/sys/unix"
)

// LockMemory calls mlockall(MCL_CURRENT|MCL_FUTURE) to prevent the process
// address space from being swapped to disk, and sets RLIMIT_CORE to zero to
// disable core dumps. Both operations protect key material in memory.
//
// Failures are non-fatal: the function logs a warning and continues.
// Returns true only if mlockall succeeded — the return value does not reflect
// RLIMIT_CORE status; a RLIMIT_CORE failure is logged separately as a warning.
func LockMemory(logger *slog.Logger) (mlocked bool) {
	if err := unix.Mlockall(unix.MCL_CURRENT | unix.MCL_FUTURE); err != nil {
		logger.Warn("mlockall failed — key material may be swapped to disk",
			slog.Any("error", err))
	} else {
		mlocked = true
	}

	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0}); err != nil {
		logger.Warn("setrlimit RLIMIT_CORE=0 failed — core dumps not disabled",
			slog.Any("error", err))
	}

	return mlocked
}
