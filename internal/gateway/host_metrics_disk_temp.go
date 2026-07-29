//go:build darwin || linux

package gateway

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	diskTempMu     sync.Mutex
	diskTempCached []HostDiskTemp
	diskTempAt     time.Time
	diskTempFailAt time.Time
)

const (
	diskTempMinInterval = 30 * time.Second
	diskTempRetryAfter  = 2 * time.Minute
)

var (
	reSmartTempLine  = regexp.MustCompile(`(?i)^Temperature(?:\s+Sensor\s+\d+)?:\s+(-?\d+(?:\.\d+)?)\s*C`)
	reSmartModelLine = regexp.MustCompile(`(?i)^(?:Model Number|Device Model|Product):\s+(.+)$`)
	reATATempAttr    = regexp.MustCompile(`(?i)^\s*194\s+Temperature_Celsius\b`)
	reDarwinPhysDisk = regexp.MustCompile(`(?m)^(/dev/disk\d+)\s+\((internal|external),\s*physical\):`)
	reSmartctlScan   = regexp.MustCompile(`(?m)^(/dev/\S+)\s+-d\s+(\S+)`)
)

func applyDiskTemps(m *HostMetrics) {
	if m == nil {
		return
	}
	if temps := collectDiskTemps(); len(temps) > 0 {
		m.DiskTemps = temps
	}
}

func collectDiskTemps() []HostDiskTemp {
	diskTempMu.Lock()
	defer diskTempMu.Unlock()
	if len(diskTempCached) > 0 && !diskTempAt.IsZero() && time.Since(diskTempAt) < diskTempMinInterval {
		return cloneDiskTemps(diskTempCached)
	}
	if len(diskTempCached) == 0 && !diskTempFailAt.IsZero() && time.Since(diskTempFailAt) < diskTempRetryAfter {
		return nil
	}

	smartctl := findSmartctl()
	if smartctl == "" {
		diskTempFailAt = time.Now()
		return nil
	}
	devices := listSMARTDiskDevices()
	if len(devices) == 0 {
		diskTempFailAt = time.Now()
		return nil
	}

	out := make([]HostDiskTemp, 0, len(devices))
	for _, d := range devices {
		temp, ok := readSMARTDiskTemp(smartctl, d)
		if !ok {
			continue
		}
		out = append(out, temp)
	}
	if len(out) == 0 {
		diskTempFailAt = time.Now()
		return nil
	}
	diskTempCached = out
	diskTempAt = time.Now()
	diskTempFailAt = time.Time{}
	return cloneDiskTemps(out)
}

func cloneDiskTemps(in []HostDiskTemp) []HostDiskTemp {
	if len(in) == 0 {
		return nil
	}
	out := make([]HostDiskTemp, len(in))
	copy(out, in)
	return out
}

func findSmartctl() string {
	for _, c := range []string{
		"/opt/homebrew/bin/smartctl",
		"/usr/local/bin/smartctl",
		"/usr/sbin/smartctl",
	} {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	if p, err := exec.LookPath("smartctl"); err == nil {
		return p
	}
	return ""
}

type smartDiskDevice struct {
	Path      string // /dev/disk0
	Device    string // disk0
	TypeArg   string // optional nvme / sat
	Internal  *bool
	ModelHint string
}

func listSMARTDiskDevices() []smartDiskDevice {
	if runtime.GOOS == "darwin" {
		return listDarwinPhysicalDisks()
	}
	return listLinuxSMARTDisks()
}

func listDarwinPhysicalDisks() []smartDiskDevice {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// LaunchAgent PATH often omits /usr/sbin; use the absolute diskutil path.
	out, err := exec.CommandContext(ctx, "/usr/sbin/diskutil", "list", "physical").CombinedOutput()
	if err != nil {
		return nil
	}
	matches := reDarwinPhysDisk.FindAllStringSubmatch(string(out), -1)
	devices := make([]smartDiskDevice, 0, len(matches))
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		path := m[1]
		internal := strings.EqualFold(m[2], "internal")
		devices = append(devices, smartDiskDevice{
			Path:     path,
			Device:   strings.TrimPrefix(path, "/dev/"),
			Internal: &internal,
			TypeArg:  "nvme",
		})
	}
	return devices
}

func listLinuxSMARTDisks() []smartDiskDevice {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	smartctl := findSmartctl()
	if smartctl != "" {
		out, err := exec.CommandContext(ctx, smartctl, "--scan").CombinedOutput()
		if err == nil {
			matches := reSmartctlScan.FindAllStringSubmatch(string(out), -1)
			devices := make([]smartDiskDevice, 0, len(matches))
			seen := map[string]bool{}
			for _, m := range matches {
				if len(m) < 3 {
					continue
				}
				path := m[1]
				base := filepath.Base(path)
				// Skip partition-looking names (sda1) and keep whole disks / nvme controllers.
				if isLinuxPartitionName(base) {
					continue
				}
				if seen[path] {
					continue
				}
				seen[path] = true
				devices = append(devices, smartDiskDevice{
					Path:    path,
					Device:  base,
					TypeArg: m[2],
				})
			}
			if len(devices) > 0 {
				return devices
			}
		}
	}
	return listLinuxBlockDevicesFallback()
}

func isLinuxPartitionName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	if strings.HasPrefix(name, "nvme") {
		// nvme0n1 = disk; nvme0n1p1 = partition
		return strings.Contains(name, "p") &&
			len(name) > 0 &&
			name[len(name)-1] >= '0' && name[len(name)-1] <= '9' &&
			strings.Contains(name, "n") &&
			strings.LastIndex(name, "p") > strings.LastIndex(name, "n")
	}
	// sda1 / hda2 / vda3 / xvda1 — trailing digits after letter disk id
	i := len(name) - 1
	if i < 2 || name[i] < '0' || name[i] > '9' {
		return false
	}
	for i >= 0 && name[i] >= '0' && name[i] <= '9' {
		i--
	}
	prefix := name[:i+1]
	for _, p := range []string{"sd", "hd", "vd", "xvd"} {
		if strings.HasPrefix(prefix, p) {
			rest := prefix[len(p):]
			if rest == "" {
				return false
			}
			for _, r := range rest {
				if r < 'a' || r > 'z' {
					return false
				}
			}
			return true
		}
	}
	return false
}

func listLinuxBlockDevicesFallback() []smartDiskDevice {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil
	}
	out := make([]smartDiskDevice, 0, 4)
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "dm-") || strings.HasPrefix(name, "md") || strings.HasPrefix(name, "ram") {
			continue
		}
		if !(strings.HasPrefix(name, "nvme") || strings.HasPrefix(name, "sd") || strings.HasPrefix(name, "hd") || strings.HasPrefix(name, "vd")) {
			continue
		}
		if isLinuxPartitionName(name) {
			continue
		}
		typeArg := ""
		if strings.HasPrefix(name, "nvme") {
			typeArg = "nvme"
		}
		out = append(out, smartDiskDevice{
			Path:    "/dev/" + name,
			Device:  name,
			TypeArg: typeArg,
		})
	}
	return out
}

func readSMARTDiskTemp(smartctl string, d smartDiskDevice) (HostDiskTemp, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	args := []string{"-A", "-i"}
	if d.TypeArg != "" {
		args = append(args, "-d", d.TypeArg)
	}
	args = append(args, d.Path)
	cmd := exec.CommandContext(ctx, smartctl, args...)
	out, err := cmd.CombinedOutput()
	// smartctl often exits non-zero when some SMART attrs are unavailable;
	// still parse stdout when temperature is present.
	model, temp, source, ok := parseSmartctlDiskTemp(out)
	if !ok {
		_ = err
		return HostDiskTemp{}, false
	}
	if model == "" {
		model = d.ModelHint
	}
	return HostDiskTemp{
		Device:   d.Device,
		Model:    model,
		TempC:    temp,
		Internal: d.Internal,
		Source:   source,
	}, true
}

// parseSmartctlDiskTemp extracts model + primary temperature from smartctl -A/-i output.
func parseSmartctlDiskTemp(out []byte) (model string, temp float64, source string, ok bool) {
	lines := bytes.Split(out, []byte("\n"))
	var temps []float64
	for _, raw := range lines {
		line := strings.TrimSpace(string(raw))
		if line == "" {
			continue
		}
		if m := reSmartModelLine.FindStringSubmatch(line); len(m) == 2 && model == "" {
			model = strings.TrimSpace(m[1])
			continue
		}
		if m := reSmartTempLine.FindStringSubmatch(line); len(m) == 2 {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil && isPlausibleDiskTemp(v) {
				// Prefer the composite "Temperature:" line over sensor N lines by
				// taking the first match; sensor lines still fill in if needed.
				if strings.HasPrefix(strings.ToLower(line), "temperature:") {
					temps = append([]float64{v}, temps...)
				} else {
					temps = append(temps, v)
				}
			}
			continue
		}
		if reATATempAttr.MatchString(line) {
			fields := strings.Fields(line)
			if len(fields) >= 10 {
				// SMART attr columns: ID NAME FLAG VALUE WORST THRESH TYPE UPDATED WHEN_FAILED RAW
				if v, err := strconv.ParseFloat(fields[9], 64); err == nil && isPlausibleDiskTemp(v) {
					temps = append(temps, v)
					if source == "" {
						source = "smartctl/ata"
					}
				} else if v, err := strconv.ParseFloat(fields[len(fields)-1], 64); err == nil && isPlausibleDiskTemp(v) {
					temps = append(temps, v)
					if source == "" {
						source = "smartctl/ata"
					}
				}
			}
		}
	}
	if len(temps) == 0 {
		return model, 0, "", false
	}
	if source == "" {
		source = "smartctl/nvme"
	}
	return model, roundTemp(temps[0]), source, true
}

func isPlausibleDiskTemp(v float64) bool { return v > -20 && v < 120 }
