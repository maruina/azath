package keymanager

// KeyBytesForTesting returns the Manager's raw key slice for verifying zeroing.
// The returned slice shares the backing array with the Manager's key. Reads of
// the returned value must complete before Destroy is called — Destroy zeros the
// backing array directly and races with any concurrent read of that array.
func KeyBytesForTesting(m *Manager) []byte {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.key
}
