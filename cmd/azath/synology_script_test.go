package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSynologyScriptContinuesAfterFailedShare(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../deploy/synology/azath-unseal.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := string(data)
	script = strings.Replace(script, `LOG="/var/log/azath-unseal.log"`, `LOG="`+filepath.Join(dir, "log")+`"`, 1)
	script = strings.Replace(script, `AZATH="/usr/local/bin/azath"`, "AZATH=false", 1)
	script = strings.Replace(script, "ATTEMPTS=5", "ATTEMPTS=1", 1)
	script = strings.Replace(script, "# unseal_share documents || status=1", "AZATH=true\nunseal_share documents || status=1", 1)
	path := filepath.Join(dir, "unseal.sh")
	if writeErr := os.WriteFile(path, []byte(script), 0600); writeErr != nil { // #nosec G703 — path is inside t.TempDir
		t.Fatal(writeErr)
	}
	err = exec.CommandContext(t.Context(), "sh", path).Run() // #nosec G204 — runs the test-owned script in t.TempDir
	if err == nil {
		t.Fatal("script must report aggregate failure")
	}
	log, err := os.ReadFile(filepath.Join(dir, "log")) // #nosec G304 — reads the test-owned log in t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"failed (exit 1)", "documents: unsealed.", "complete (exit 1)"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("missing %q in %s", want, log)
		}
	}
}
