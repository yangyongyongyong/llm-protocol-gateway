//go:build linux

package gateway

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type linuxCPUTicks struct {
	idle, total uint64
}

// linuxSampleState carries everything needed to derive deltas between samples.
type linuxSampleState struct {
	ticks    linuxCPUTicks
	hasTicks bool
	net      netCounters
}

func collectHostMetrics(prev any) hostMetricsCollectResult {
	ncpu := runtime.NumCPU()
	m := HostMetrics{
		CPUCount:    ncpu,
		CollectedAt: time.Now(),
	}
	m.Load1, m.Load5, m.Load15 = parseProcLoadavg(readFileQuiet("/proc/loadavg"))
	ticks, err := linuxCPUTicksNow()
	var prevState linuxSampleState
	if p, ok := prev.(linuxSampleState); ok {
		prevState = p
	}
	var prevTicks *linuxCPUTicks
	if prevState.hasTicks {
		prevTicks = &prevState.ticks
	}
	next := linuxSampleState{net: prevState.net}
	if err == nil {
		if prevTicks != nil && ticks.total > prevTicks.total {
			idleDelta := ticks.idle - prevTicks.idle
			totalDelta := ticks.total - prevTicks.total
			if totalDelta > 0 {
				m.CPUPercent = clampFloat(100*(1-float64(idleDelta)/float64(totalDelta)), 0, 100)
			}
		} else if ncpu > 0 {
			m.CPUPercent = clampFloat(m.Load1/float64(ncpu)*100, 0, 100)
		}
		next.ticks = ticks
		next.hasTicks = true
	} else if ncpu > 0 {
		m.CPUPercent = clampFloat(m.Load1/float64(ncpu)*100, 0, 100)
	}
	if tempC, source, ok := linuxCPUTempC(); ok {
		m.TempC = tempC
		m.TempAvailable = true
		m.TempSource = source
	}
	if total, used, ok := parseProcMeminfo(readFileQuiet("/proc/meminfo")); ok {
		m.applyMem(total, used)
	}
	m.SwapTotal, m.SwapUsed = parseProcSwap(readFileQuiet("/proc/meminfo"))
	m.applyDiskRoot()
	applyDiskTemps(&m)
	applyFanSpeeds(&m)
	m.UptimeSeconds = linuxUptimeSeconds()
	m.applyProcess()

	cur := sumInterfaceCounters(parseProcNetDev(readFileQuiet("/proc/net/dev")))
	m.applyNet(prevState.net, cur)
	if cur.ok {
		next.net = cur
	}
	return hostMetricsCollectResult{metrics: m, prev: next}
}

func readFileQuiet(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func linuxUptimeSeconds() float64 {
	fields := strings.Fields(readFileQuiet("/proc/uptime"))
	if len(fields) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// parseProcMeminfo returns total/used bytes, matching `free`'s used column
// (total - available) so caches are not reported as used.
func parseProcMeminfo(content string) (total, used uint64, ok bool) {
	var totalKB, availKB, freeKB uint64
	var haveAvail bool
	for _, line := range strings.Split(content, "\n") {
		key, valKB, ok := parseMeminfoLine(line)
		if !ok {
			continue
		}
		switch key {
		case "MemTotal":
			totalKB = valKB
		case "MemAvailable":
			availKB = valKB
			haveAvail = true
		case "MemFree":
			freeKB = valKB
		}
	}
	if totalKB == 0 {
		return 0, 0, false
	}
	free := availKB
	if !haveAvail {
		free = freeKB
	}
	if free > totalKB {
		free = totalKB
	}
	return totalKB * 1024, (totalKB - free) * 1024, true
}

func parseProcSwap(content string) (total, used uint64) {
	var totalKB, freeKB uint64
	for _, line := range strings.Split(content, "\n") {
		key, valKB, ok := parseMeminfoLine(line)
		if !ok {
			continue
		}
		switch key {
		case "SwapTotal":
			totalKB = valKB
		case "SwapFree":
			freeKB = valKB
		}
	}
	if totalKB == 0 || freeKB > totalKB {
		return totalKB * 1024, 0
	}
	return totalKB * 1024, (totalKB - freeKB) * 1024
}

func parseMeminfoLine(line string) (key string, valueKB uint64, ok bool) {
	idx := strings.IndexByte(line, ':')
	if idx <= 0 {
		return "", 0, false
	}
	fields := strings.Fields(line[idx+1:])
	if len(fields) == 0 {
		return "", 0, false
	}
	v, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return strings.TrimSpace(line[:idx]), v, true
}

// parseProcNetDev extracts cumulative rx/tx bytes per interface.
func parseProcNetDev(content string) []interfaceCounter {
	var out []interfaceCounter
	for _, line := range strings.Split(content, "\n") {
		idx := strings.IndexByte(line, ':')
		if idx <= 0 {
			continue
		}
		name := strings.TrimSpace(line[:idx])
		fields := strings.Fields(line[idx+1:])
		if name == "" || len(fields) < 9 {
			continue
		}
		rx, err1 := strconv.ParseUint(fields[0], 10, 64)
		tx, err2 := strconv.ParseUint(fields[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, interfaceCounter{Name: name, RX: rx, TX: tx})
	}
	return out
}

// parseProcLoadavg reads the 1/5/15 minute load averages.
func parseProcLoadavg(content string) (l1, l5, l15 float64) {
	fields := strings.Fields(content)
	if len(fields) < 3 {
		return 0, 0, 0
	}
	parse := func(s string) float64 {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || v < 0 {
			return 0
		}
		return v
	}
	return parse(fields[0]), parse(fields[1]), parse(fields[2])
}

func linuxCPUTicksNow() (linuxCPUTicks, error) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return linuxCPUTicks{}, err
	}
	line := strings.SplitN(string(raw), "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return linuxCPUTicks{}, os.ErrInvalid
	}
	var vals []uint64
	for _, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			break
		}
		vals = append(vals, v)
	}
	if len(vals) < 4 {
		return linuxCPUTicks{}, os.ErrInvalid
	}
	var total uint64
	for _, v := range vals {
		total += v
	}
	idle := vals[3]
	if len(vals) > 4 {
		idle += vals[4] // iowait
	}
	return linuxCPUTicks{idle: idle, total: total}, nil
}

func linuxCPUTempC() (float64, string, bool) {
	// Prefer x86_pkg_temp / cpu preferential zones when present.
	entries, err := os.ReadDir("/sys/class/thermal")
	if err != nil {
		return 0, "", false
	}
	var fallback float64
	var haveFallback bool
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "thermal_zone") {
			continue
		}
		base := "/sys/class/thermal/" + name
		typB, _ := os.ReadFile(base + "/type")
		typ := strings.TrimSpace(string(typB))
		tempB, err := os.ReadFile(base + "/temp")
		if err != nil {
			continue
		}
		milli, err := strconv.ParseFloat(strings.TrimSpace(string(tempB)), 64)
		if err != nil || milli <= 0 {
			continue
		}
		c := milli / 1000
		lower := strings.ToLower(typ)
		if strings.Contains(lower, "pkg") || strings.Contains(lower, "cpu") || strings.Contains(lower, "x86") {
			return c, typ, true
		}
		if !haveFallback {
			fallback = c
			haveFallback = true
		}
	}
	if haveFallback {
		return fallback, "thermal_zone", true
	}
	return 0, "", false
}
