package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/maruina/azath/internal/config"
	"github.com/maruina/azath/internal/fsutil"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newConfigNewDeviceCmd() *cobra.Command {
	var name, cfgPath string

	cmd := &cobra.Command{
		Use:   "new-device",
		Short: "Add a new device to an existing config file",
		Long: `Generates a UUID v4 and appends a new device entry to the config file.
Comments and formatting in the config file are preserved.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Read raw bytes once — used for both validation and YAML node manipulation.
			// Reading twice would create a TOCTOU window.
			rawBytes, err := readConfig(cfgPath)
			if err != nil {
				return err
			}

			id := uuid.New().String()

			newBytes, err := appendDeviceNode(rawBytes, name, id)
			if err != nil {
				return fmt.Errorf("updating config: %w", err)
			}

			// Validate the result by parsing the updated bytes.
			updated, err := config.LoadFromBytes(newBytes)
			if err != nil {
				return fmt.Errorf("parsing updated config: %w", err)
			}
			if _, err = config.Validate(updated); err != nil {
				return fmt.Errorf("updated config is invalid: %w", err)
			}

			if err = fsutil.Write(cfgPath, newBytes); err != nil {
				return fmt.Errorf("writing config: %w", err)
			}

			if _, err = fmt.Fprintln(cmd.OutOrStdout(), id); err != nil {
				return fmt.Errorf("writing output: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "device name (required)")
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (required)")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("config")

	return cmd
}

// readConfig reads the config file, validates it, and returns the raw bytes.
// Reading and validating in one step avoids a TOCTOU window between reading
// for validation and reading again for node manipulation.
func readConfig(path string) ([]byte, error) {
	rawBytes, err := os.ReadFile(path) // #nosec G304 — path is from a CLI flag
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	cfg, err := config.LoadFromBytes(rawBytes)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	if _, err = config.Validate(cfg); err != nil {
		return nil, fmt.Errorf("existing config is invalid: %w", err)
	}
	return rawBytes, nil
}

// appendDeviceNode appends a new device entry to the YAML document in data.
// It uses the yaml.v3 node API rather than struct unmarshal/marshal because
// struct round-trips strip all comments. The node API preserves the original
// comments, ordering, and indentation.
func appendDeviceNode(data []byte, name, id string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing YAML: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, errors.New("unexpected YAML document structure")
	}

	root := doc.Content[0]
	var devicesSeq *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "devices" {
			devicesSeq = root.Content[i+1]
			break
		}
	}
	if devicesSeq == nil {
		return nil, fmt.Errorf("devices key not found in config")
	}
	if devicesSeq.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("devices key must be a YAML sequence, got node kind %v", devicesSeq.Kind)
	}

	newDevice := &yaml.Node{
		Kind: yaml.MappingNode,
		Tag:  "!!map",
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "name"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: name},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "uuid"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: id},
		},
	}
	devicesSeq.Content = append(devicesSeq.Content, newDevice)

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("marshaling YAML: %w", err)
	}
	return out, nil
}
