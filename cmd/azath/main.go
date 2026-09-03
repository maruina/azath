package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Injected at build time via -ldflags.
var (
	version = "dev"
	commit  = "none"
)

func newRootCmd(ver, rev string) *cobra.Command {
	root := &cobra.Command{
		Use:   "azath",
		Short: "KMS server for Talos disk encryption and Synology volume unsealing",
		Long: `azath is a KMS server that implements the Talos KMS gRPC protocol.
It seals and unseals key material using Telegram approval and config-driven device disablement.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(
		newServeCmd(),
		newClientCmd(),
		newSealCmd(),
		newConfigCmd(),
	)

	root.Version = fmt.Sprintf("%s (commit: %s)", ver, rev)

	return root
}

func main() {
	if err := newRootCmd(version, commit).Execute(); err != nil {
		// --help and -h cause cobra to return pflag.ErrHelp; exit 0, not 1.
		if errors.Is(err, pflag.ErrHelp) {
			os.Exit(0)
		}
		// SilenceErrors: true suppresses cobra's own error output; print here.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
