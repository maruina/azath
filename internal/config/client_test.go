package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maruina/azath/internal/config"
)

const validClientYAML = `
device:
  name: synology
  uuid: 550e8400-e29b-41d4-a716-446655440000
endpoints:
  - https://azath-lan.example:443
shares:
  - name: homes
    sealed_blob: /etc/azath/secrets/homes.key.sealed
  - name: media
    sealed_blob: /etc/azath/secrets/media.key.sealed
`

func TestLoadClientConfig_Valid(t *testing.T) {
	t.Parallel()
	tmp := filepath.Join(t.TempDir(), "client.yaml")
	if err := os.WriteFile(tmp, []byte(validClientYAML), 0600); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}

	cfg, err := config.LoadClientConfig(tmp)
	if err != nil {
		t.Fatalf("LoadClientConfig failed: %v", err)
	}
	if cfg.Device.Name != "synology" {
		t.Errorf("device name = %q, want 'synology'", cfg.Device.Name)
	}
	if len(cfg.Shares) != 2 {
		t.Errorf("len(shares) = %d, want 2", len(cfg.Shares))
	}
}

func TestLoadClientConfigFromBytes_Validation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "missing device name",
			yaml:    "device:\n  uuid: 550e8400-e29b-41d4-a716-446655440000\nendpoints:\n  - https://azath:443\n",
			wantErr: "client.device.name is required",
		},
		{
			name:    "missing device uuid",
			yaml:    "device:\n  name: synology\nendpoints:\n  - https://azath:443\n",
			wantErr: "client.device.uuid is required",
		},
		{
			name:    "missing endpoints",
			yaml:    "device:\n  name: synology\n  uuid: 550e8400-e29b-41d4-a716-446655440000\n",
			wantErr: "client.endpoints must contain at least one endpoint",
		},
		{
			name:    "non-canonical uuid",
			yaml:    "device:\n  name: synology\n  uuid: 550E8400-E29B-41D4-A716-446655440000\nendpoints:\n  - https://azath:443\n",
			wantErr: "device UUID must be canonical",
		},
		{
			name:    "duplicate share name",
			yaml:    "device:\n  name: synology\n  uuid: 550e8400-e29b-41d4-a716-446655440000\nendpoints:\n  - https://azath:443\nshares:\n  - name: homes\n    sealed_blob: /a\n  - name: homes\n    sealed_blob: /b\n",
			wantErr: "duplicate share name",
		},
		{
			name:    "invalid share name",
			yaml:    "device:\n  name: synology\n  uuid: 550e8400-e29b-41d4-a716-446655440000\nendpoints:\n  - https://azath:443\nshares:\n  - name: ../homes\n    sealed_blob: /a\n",
			wantErr: "invalid share name or blob path",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := config.LoadClientConfigFromBytes([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestEndpointTarget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"https://127.0.0.1:7800", "127.0.0.1:7800", false},
		{"https://example.com:443", "example.com:443", false},
		{"example.com:443", "example.com:443", false},
		{"http://example.com:80", "", true},
		{"https://example.com", "", true},
		{"https://example.com:443/path", "", true},
		{"https://example.com:99999", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got, err := config.EndpointTarget(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("EndpointTarget(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
