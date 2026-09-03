package secret_test

import (
	"strings"
	"testing"

	"github.com/maruina/azath/internal/crypto"
	"github.com/maruina/azath/internal/secret"
)

func TestEnvResolver_Success(t *testing.T) {
	// t.Setenv cannot be combined with t.Parallel.
	t.Setenv("AZATH_SEAL_TOKEN", "test-token-value")
	r := secret.EnvResolver{}
	got, err := r.Resolve(t.Context(), "op://vault/azath/seal-token")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	t.Cleanup(func() { crypto.Zero(got) })
	if string(got) != "test-token-value" {
		t.Errorf("Resolve(%q) = %q, want %q", "op://vault/azath/seal-token", string(got), "test-token-value")
	}
}

func TestEnvResolver_MissingEnvVar(t *testing.T) {
	// t.Setenv cannot be combined with t.Parallel.
	// Explicitly unset the var to avoid flaky behaviour if someone has it in their shell.
	t.Setenv("AZATH_NONEXISTENT_FIELD", "")
	r := secret.EnvResolver{}
	_, err := r.Resolve(t.Context(), "op://vault/azath/nonexistent-field")
	if err == nil {
		t.Fatal("expected error for missing env var, got nil")
	}
	if !strings.Contains(err.Error(), "AZATH_NONEXISTENT_FIELD") {
		t.Errorf("error %q should mention env var name", err.Error())
	}
}

func TestEnvResolver_BadRef(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		ref     string
		wantMsg string
	}{
		{"no op:// prefix", "vault/azath/token", "must start with op://"},
		{"only two segments", "op://vault/item", "not in op://<vault>/<item>/<field> format"},
		{"empty field", "op://vault/item/", "not in op://<vault>/<item>/<field> format"},
		{"whitespace-only field", "op://vault/item/   ", "not in op://<vault>/<item>/<field> format"},
		{"extra segment", "op://vault/item/field/extra", "not in op://<vault>/<item>/<field> format"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := secret.EnvResolver{}
			_, err := r.Resolve(t.Context(), tc.ref)
			if err == nil {
				t.Fatalf("Resolve(%q) = nil, want error", tc.ref)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

func TestEnvResolver_ReturnedBytesAreCopy(t *testing.T) {
	// t.Setenv cannot be combined with t.Parallel.
	t.Setenv("AZATH_MY_SECRET", "secret-value")
	r := secret.EnvResolver{}
	got, err := r.Resolve(t.Context(), "op://vault/azath/my-secret")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Mutate the returned bytes and verify it does not affect a second call.
	crypto.Zero(got)
	got2, err := r.Resolve(t.Context(), "op://vault/azath/my-secret")
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	t.Cleanup(func() { crypto.Zero(got2) })
	if string(got2) != "secret-value" {
		t.Errorf("Resolve(%q) second call = %q, want %q", "op://vault/azath/my-secret", string(got2), "secret-value")
	}
}
