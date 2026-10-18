package main

import (
	"encoding/json"
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
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "validate <path>",
		Short: "Validate a server or client config file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			printValid := func(cfgType string) error {
				if jsonOutput {
					if err := json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
						"valid": true,
						"type":  cfgType,
					}); err != nil {
						return fmt.Errorf("writing json output: %w", err)
					}
					return nil
				}
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), "config is valid"); err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				return nil
			}

			cfg, err := config.Load(path)
			if err == nil {
				if _, valErr := config.Validate(cfg); valErr == nil {
					return printValid("server")
				} else {
					if jsonOutput {
						_ = json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
							"valid": false,
							"error": valErr.Error(),
						})
					}
					return fmt.Errorf("config validation failed: %w", valErr)
				}
			}

			// If server config loading failed, check if this is a valid client config.
			if _, clientErr := config.LoadClientConfig(path); clientErr == nil {
				return printValid("client")
			}

			if jsonOutput {
				_ = json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
					"valid": false,
					"error": err.Error(),
				})
			}
			return fmt.Errorf("loading config: %w", err)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output result in JSON format")
	return cmd
}
