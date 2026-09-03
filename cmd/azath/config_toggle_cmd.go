package main

import (
	"errors"
	"fmt"

	"github.com/maruina/azath/internal/config"
	"github.com/maruina/azath/internal/fsutil"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newConfigDisableDeviceCmd() *cobra.Command {
	var name, cfgPath string

	cmd := &cobra.Command{
		Use:   "disable-device",
		Short: "Disable a device in the config",
		Long: `Sets disabled: true on the device identified by --name.
Comments and formatting in the config file are preserved.
Idempotent — safe to run on an already-disabled device.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return toggleDeviceDisabled(cmd, cfgPath, name, true)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "device name (required)")
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (required)")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("config")

	return cmd
}

func newConfigEnableDeviceCmd() *cobra.Command {
	var name, cfgPath string

	cmd := &cobra.Command{
		Use:   "enable-device",
		Short: "Enable a device in the config",
		Long: `Sets disabled: false on the device identified by --name, or removes
the disabled field if it was the only entry.
Comments and formatting in the config file are preserved.
Idempotent — safe to run on an already-enabled device.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return toggleDeviceDisabled(cmd, cfgPath, name, false)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "device name (required)")
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (required)")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("config")

	return cmd
}

// toggleDeviceDisabled reads cfgPath, finds the device by name, sets its
// disabled field to disabled (true or false), validates the result, and writes
// atomically. It uses the yaml.v3 node API to preserve comments and formatting.
func toggleDeviceDisabled(cmd *cobra.Command, cfgPath string, name string, disabled bool) error {
	rawBytes, err := readConfig(cfgPath)
	if err != nil {
		return err
	}

	newBytes, err := setDeviceDisabledNode(rawBytes, name, disabled)
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

	action := "enabled"
	if disabled {
		action = "disabled"
	}
	if _, err = fmt.Fprintf(cmd.OutOrStdout(), "device %q %s\n", name, action); err != nil {
		return fmt.Errorf("writing output: %w", err)
	}
	return nil
}

// setDeviceDisabledNode finds a device by name in the YAML document and sets
// its disabled field to the given value. If disabled is false and the device
// mapping has a disabled key, it removes the key entirely — a missing
// disabled field is equivalent to disabled: false.
func setDeviceDisabledNode(data []byte, name string, disabled bool) ([]byte, error) {
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

	var found bool
	for _, device := range devicesSeq.Content {
		if device.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(device.Content); j += 2 {
			if device.Content[j].Value == "name" && device.Content[j+1].Value == name {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		// Found the matching device. Now update or remove the disabled field.
		if disabled {
			setOrAddDisabledField(device, true)
		} else {
			removeDisabledField(device)
		}
		break
	}

	if !found {
		return nil, fmt.Errorf("device %q not found in config", name)
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("marshaling YAML: %w", err)
	}
	return out, nil
}

// setOrAddDisabledField updates an existing disabled field or appends one.
func setOrAddDisabledField(device *yaml.Node, value bool) {
	val := "true"
	if !value {
		val = "false"
	}
	for j := 0; j+1 < len(device.Content); j += 2 {
		if device.Content[j].Value == "disabled" {
			device.Content[j+1].Value = val
			return
		}
	}
	// No existing disabled field — append one.
	device.Content = append(device.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "disabled"},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: val},
	)
}

// removeDisabledField removes the disabled key and its value from the device
// mapping, if present. A missing disabled field is equivalent to disabled: false.
func removeDisabledField(device *yaml.Node) {
	for j := 0; j+1 < len(device.Content); j += 2 {
		if device.Content[j].Value == "disabled" {
			// Remove both the key and value nodes.
			device.Content = append(device.Content[:j], device.Content[j+2:]...)
			return
		}
	}
}
