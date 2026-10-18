package main

import (
	"context"
	"io"
	"testing"

	"github.com/maruina/azath/internal/testutil"
)

func TestSynologyInvalidConfigBeforeMounts(t *testing.T) {
	t.Parallel()
	for _, shares := range []string{
		" - {name: homes, sealed_blob: /unused}\n - {name: homes, sealed_blob: /unused}\n",
		" - {name: homes, sealed_blob: /unused}\n - {name: '../invalid', sealed_blob: /unused}\n",
	} {
		t.Run(shares, func(t *testing.T) {
			t.Parallel()
			cfg := testutil.WriteConfig(t, "device:\n  name: synology\n  uuid: "+e2eDeviceUUID+"\nendpoints:\n - https://localhost:443\nshares:\n"+shares)
			cmd := newSynologyCmdWithMount(func(context.Context, string, []byte) error { t.Error("mounted invalid config"); return nil })
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"--config", cfg})
			if cmd.ExecuteContext(t.Context()) == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
}
