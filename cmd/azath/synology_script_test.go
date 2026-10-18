package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSynologyScriptSingleInvocation(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../deploy/synology/azath-unseal.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, exit := range []string{"0", "1"} {
		dir := t.TempDir()
		log := filepath.Join(dir, "log")
		calls := filepath.Join(dir, "calls")
		stub := filepath.Join(dir, "azath")
		if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+calls+"'\necho 'homes: mount_failed'\necho 'media: mounted'\nexit "+exit+"\n"), 0700); err != nil { // #nosec G306 -- executable test stub
			t.Fatal(err)
		}
		script := strings.Replace(string(data), `LOG="/var/log/azath-unseal.log"`, `LOG="`+log+`"`, 1)
		script = strings.Replace(script, `AZATH="/usr/local/bin/azath"`, `AZATH="`+stub+`"`, 1)
		path := filepath.Join(dir, "unseal.sh")
		if err := os.WriteFile(path, []byte(script), 0600); err != nil { // #nosec G703 -- test-owned temporary script
			t.Fatal(err)
		}
		err := exec.CommandContext(t.Context(), "sh", path).Run() // #nosec G204 -- test-owned script
		if (err == nil) != (exit == "0") {
			t.Fatalf("exit propagation: %v", err)
		}
		got, err := os.ReadFile(calls) // #nosec G304 -- test-owned file
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "synology --config /etc/azath/client-synology.yaml\n" {
			t.Fatalf("calls: %s", got)
		}
		output, err := os.ReadFile(log) // #nosec G304 -- test-owned file
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(output), "media: mounted") {
			t.Fatal("lost partial results")
		}
	}
}
