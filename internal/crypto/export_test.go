package crypto

// KeyBytesForTesting returns the Sealer's key slice for verifying key zeroing.
// The returned slice is a live reference — do not use concurrently with Destroy.
func KeyBytesForTesting(s *Sealer) []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.key
}

// InstanceTagBytesForTesting returns the raw instanceTag bytes for verifying
// memory zeroing. Bypasses the destroyed flag — do not use concurrently with Destroy.
func InstanceTagBytesForTesting(s *Sealer) [instanceTagSize]byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.instanceTag
}
