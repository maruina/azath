package server

// SealTokenForTesting returns the seal token held by the server for white-box testing.
func SealTokenForTesting(s *KMSServer) []byte {
	return s.sealToken
}
