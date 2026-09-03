// Package gate implements approval gates for azath Unseal operations.
package gate

import "context"

// Device identifies a device requesting unseal.
type Device struct {
	Name string
	UUID string
}

// Decision is the outcome of a gate check.
type Decision int

const (
	Denied Decision = iota
	Approved
	Pending
)

// Gate checks whether an Unseal request should be approved.
// All implementations must be safe for concurrent use.
type Gate interface {
	// Check evaluates whether the device should be allowed to unseal.
	// Returns Approved, Denied, or Pending along with any error.
	// On error, the caller must treat the result as denied (fail-closed).
	Check(ctx context.Context, device Device) (Decision, error)

	// Close shuts down the gate and releases resources.
	Close() error
}
