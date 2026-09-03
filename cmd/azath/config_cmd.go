package main

import (
	"fmt"

	"github.com/maruina/azath/internal/config"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Configuration management commands",
	}
	cmd.AddCommand(newConfigValidateCmd())
	cmd.AddCommand(newConfigNewDeviceCmd())
	cmd.AddCommand(newConfigDisableDeviceCmd())
	cmd.AddCommand(newConfigEnableDeviceCmd())
	return cmd
}

func newConfigValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <path>",
		Short: "Validate a config file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(args[0])
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			if _, err := config.Validate(cfg); err != nil {
				return fmt.Errorf("config validation failed: %w", err)
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), "config is valid"); err != nil {
				return fmt.Errorf("writing output: %w", err)
			}
			return nil
		},
	}
}
