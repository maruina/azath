package main

import "testing"

func TestEndpointTarget(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ input, want string }{
		{"https://example.com:443", "example.com:443"},
		{"127.0.0.1:7800", "127.0.0.1:7800"},
		{"https://[::1]:443", "[::1]:443"},
		{"http://example.com:443", ""},
		{"https://user:secret@example.com:443", ""},
		{"https://example.com:443/path", ""},
		{"https://example.com:443?x=1", ""},
		{"https://example.com:443#x", ""},
		{"https://example.com", ""},
		{"example.com:0", ""},
		{"example.com:65536", ""},
	} {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got, err := endpointTarget(tt.input)
			if tt.want == "" {
				if err == nil {
					t.Fatal("invalid endpoint accepted")
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
