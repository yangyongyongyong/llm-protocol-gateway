//go:build linux

package gateway

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func readHostFans() []HostFanSpeed {
	entries, err := os.ReadDir("/sys/class/hwmon")
	if err != nil {
		return nil
	}
	var out []HostFanSpeed
	id := 0
	for _, e := range entries {
		dir := filepath.Join("/sys/class/hwmon", e.Name())
		labelChip := strings.TrimSpace(readFileQuiet(filepath.Join(dir, "name")))
		for i := 1; i <= 16; i++ {
			inputPath := filepath.Join(dir, "fan"+strconv.Itoa(i)+"_input")
			raw := strings.TrimSpace(readFileQuiet(inputPath))
			if raw == "" {
				continue
			}
			rpm, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				continue
			}
			minRPM, _ := strconv.ParseFloat(strings.TrimSpace(readFileQuiet(filepath.Join(dir, "fan"+strconv.Itoa(i)+"_min"))), 64)
			maxRPM, _ := strconv.ParseFloat(strings.TrimSpace(readFileQuiet(filepath.Join(dir, "fan"+strconv.Itoa(i)+"_max"))), 64)
			name := strings.TrimSpace(readFileQuiet(filepath.Join(dir, "fan"+strconv.Itoa(i)+"_label")))
			if name == "" {
				if labelChip != "" {
					name = labelChip + " fan" + strconv.Itoa(i)
				} else {
					name = "Fan " + strconv.Itoa(i)
				}
			}
			out = append(out, HostFanSpeed{
				ID:      id,
				Name:    name,
				RPM:     rpm,
				MinRPM:  minRPM,
				MaxRPM:  maxRPM,
				Percent: roundTemp(fanSpeedPercent(rpm, minRPM, maxRPM)),
				Source:  "hwmon",
			})
			id++
		}
	}
	return out
}
