package platform_test

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/maruina/azath/internal/platform"
)

func TestLockMemory_DoesNotPanic(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	// Must not panic regardless of OS or privilege level.
	got := platform.LockMemory(logger)
	// On Linux without root, mlockall may fail (warning logged and false returned).
	// On non-Linux, the function returns false immediately with no logging.
	// Both are valid; consume the result to catch future signature changes.
	_ = got
}
