package crypto

// Zero clears b so key material cannot be recovered from process memory.
// The //go:noinline directive prevents the compiler from eliding the clearing
// as a dead store when the buffer is not read afterward.
//
//go:noinline
func Zero(b []byte) {
	clear(b)
}

// ZeroOnReturn zeros the byte slice pointed to by b when the enclosing
// function returns. Intended for use with defer:
//
//	key := make([]byte, 32)
//	defer crypto.ZeroOnReturn(&key)
//
// Taking *[]byte (rather than []byte) ensures the defer captures the pointer,
// not a snapshot of the slice header, so the value at return time is zeroed.
//
// WARNING: only the slice value present at return time is zeroed. If the
// variable is reassigned to a new backing array after the defer is registered
// (e.g., via append past capacity), only the new array is zeroed; the original
// backing array is not. Do not reassign slices that hold key material after
// deferring ZeroOnReturn.
//
// If *b is nil at return time (e.g., from a conditional assignment that did
// not run), zeroing is silently skipped. Ensure the slice is always allocated
// before deferring if zeroing is required unconditionally.
func ZeroOnReturn(b *[]byte) {
	if b != nil {
		Zero(*b)
	}
}
