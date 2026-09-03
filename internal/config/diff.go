package config

import (
	"fmt"
	"slices"

	"github.com/google/uuid"
)

// Diff compares the device lists of two configs and returns a human-readable
// description of each mismatch. Returns nil if the device lists are identical.
// Output is sorted by device UUID for deterministic ordering.
func Diff(a, b *Config) []string {
	aMap := indexDevices(a.Devices)
	bMap := indexDevices(b.Devices)

	var diffs []string

	for uuid, da := range aMap {
		db, ok := bMap[uuid]
		if !ok {
			diffs = append(diffs, fmt.Sprintf("device %q (%q) only in first config", uuid, da.Name))
			continue
		}
		if da.Name != db.Name {
			diffs = append(diffs, fmt.Sprintf("device %q name mismatch: %q (first) vs %q (second)", uuid, da.Name, db.Name))
		}
	}

	for uuid, db := range bMap {
		if _, ok := aMap[uuid]; !ok {
			diffs = append(diffs, fmt.Sprintf("device %q (%q) only in second config", uuid, db.Name))
		}
	}

	if len(diffs) == 0 {
		return nil
	}
	slices.Sort(diffs)
	return diffs
}

func indexDevices(devices []DeviceConfig) map[string]DeviceConfig {
	m := make(map[string]DeviceConfig, len(devices))
	for _, d := range devices {
		key := d.UUID
		if p, err := uuid.Parse(d.UUID); err == nil {
			key = p.String() // canonical lowercase form
		}
		m[key] = d
	}
	return m
}
