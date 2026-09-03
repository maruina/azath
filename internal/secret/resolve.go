// Package secret resolves op:// secret references to their plaintext values.
//
// The returned slice from Resolve must be zeroed by the caller after use.
// OPResolver zeroes its internal subprocess buffer; the returned copy is
// the caller's responsibility. EnvResolver returns a []byte copy of the
// env var string — the copy can be zeroed, but the Go env block backing
// the original string cannot (accepted dev-mode limitation).
package secret

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/maruina/azath/internal/crypto"
)

// Resolver resolves an op:// secret reference to its plaintext bytes.
type Resolver interface {
	// Resolve returns the secret bytes for ref. The returned slice is
	// caller-owned and must be zeroed after use via crypto.ZeroOnReturn or
	// crypto.Zero.
	Resolve(ctx context.Context, ref string) ([]byte, error)
}

// OPResolver resolves secrets by shelling out to the 1Password CLI (`op read`).
// It requires `op` to be available in PATH and the user to be authenticated.
type OPResolver struct{}

// Resolve calls `op read <ref>` and returns the stdout bytes.
// ref is passed as a separate argument — no shell interpolation (security invariant #7).
// Error messages do not include ref to avoid leaking vault/item path structure.
func (OPResolver) Resolve(ctx context.Context, ref string) ([]byte, error) {
	// #nosec G204 — ref comes from config validation which enforces the op:// format;
	// it is passed as a separate exec.Command argument, not interpolated into a shell string.
	cmd := exec.CommandContext(ctx, "op", "read", ref) //nolint:gosec
	out, err := cmd.Output()
	if err != nil {
		crypto.Zero(out)
		// Strip *exec.ExitError.Stderr — op writes diagnostic messages to stderr
		// that may include vault/item names. Return only the exit code.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("resolving secret: op read exited %d", exitErr.ExitCode())
		}
		return nil, fmt.Errorf("resolving secret: op read failed: %w", err)
	}
	if len(out) == 0 {
		return nil, errors.New("resolving secret: op read returned empty output")
	}
	// op read appends a trailing newline; strip it so callers get raw bytes.
	// Clone into a caller-owned copy, then zero the subprocess buffer.
	result := bytes.Clone(bytes.TrimRight(out, "\n"))
	crypto.Zero(out)
	return result, nil
}

// EnvResolver resolves secrets from environment variables.
// It maps the field segment of an op:// reference to an env var named
// AZATH_<FIELD_UPPER> — e.g. op://vault/azath/seal-token → AZATH_SEAL_TOKEN.
// Use for --dev mode where 1Password CLI is not available.
//
// Note: the env var string backing cannot be zeroed from Go's env block;
// use only in development, never in production.
type EnvResolver struct{}

// Resolve reads the env var corresponding to the field segment of ref.
// Returns an error if the env var is not set or empty. The returned slice
// is a copy and must be zeroed by the caller after use.
// ctx is unused: env-var reads are synchronous and cannot be cancelled.
func (EnvResolver) Resolve(_ context.Context, ref string) ([]byte, error) {
	field, err := fieldSegment(ref)
	if err != nil {
		return nil, fmt.Errorf("env resolver: %w", err)
	}
	envName := "AZATH_" + strings.ToUpper(strings.ReplaceAll(field, "-", "_"))
	val := os.Getenv(envName)
	if val == "" {
		return nil, fmt.Errorf("env var %s is not set (required for %s)", envName, ref)
	}
	return []byte(val), nil
}

// fieldSegment extracts the third path segment from an op:// reference.
// op://vault/item/field → "field"
func fieldSegment(ref string) (string, error) {
	rest, ok := strings.CutPrefix(ref, "op://")
	if !ok {
		return "", errors.New("reference must start with op://")
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 ||
		strings.TrimSpace(parts[0]) == "" ||
		strings.TrimSpace(parts[1]) == "" ||
		strings.TrimSpace(parts[2]) == "" ||
		strings.Contains(parts[2], "/") {
		return "", fmt.Errorf("reference %q is not in op://<vault>/<item>/<field> format", ref)
	}
	return parts[2], nil
}
