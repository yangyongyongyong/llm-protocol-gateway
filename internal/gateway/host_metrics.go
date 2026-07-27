package gateway

import (
	"bytes"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// hostMetricsPollInterval is how often the sampler refreshes while at least one
// console client is actively requesting /__host-metrics.
const hostMetricsPollInterval = 10 * time.Second

// hostMetricsIdleStopAfter stops the background sampler when no client has
// polled for this long (≈3 missed polls). Keeps CPU near zero when the
// homepage is closed or the tab is backgrounded.
const hostMetricsIdleStopAfter = 30 * time.Second

// HostMetrics is a lightweight snapshot of host CPU load / temperature for the
// admin homepage. On recent macOS, Celsius may be unavailable; thermalPressure
// (Nominal/Fair/…) is still exposed via powermetrics.
type HostMetrics struct {
	Load1                    float64 `json:"load1"`
	Load5                    float64 `json:"load5"`
	Load15                   float64 `json:"load15"`
	CPUPercent               float64 `json:"cpuPercent"` // 0–100, all-core average
	CPUCount                 int     `json:"cpuCount"`
	TempC                    float64 `json:"tempC,omitempty"`
	TempAvailable            bool    `json:"tempAvailable"`
	TempSource               string  `json:"tempSource,omitempty"`
	ThermalPressure          string  `json:"thermalPressure,omitempty"`
	ThermalPressureAvailable bool    `json:"thermalPressureAvailable"`
	ThermalPressureSource    string  `json:"thermalPressureSource,omitempty"`

	// Memory (bytes). MemAvailable=false when the platform has no cheap probe.
	MemTotal     uint64  `json:"memTotal,omitempty"`
	MemUsed      uint64  `json:"memUsed,omitempty"`
	MemPercent   float64 `json:"memPercent,omitempty"`
	MemAvailable bool    `json:"memAvailable"`
	SwapTotal    uint64  `json:"swapTotal,omitempty"`
	SwapUsed     uint64  `json:"swapUsed,omitempty"`

	// Root filesystem (bytes).
	DiskTotal     uint64  `json:"diskTotal,omitempty"`
	DiskUsed      uint64  `json:"diskUsed,omitempty"`
	DiskPercent   float64 `json:"diskPercent,omitempty"`
	DiskAvailable bool    `json:"diskAvailable"`

	// Network counters since boot + per-second rates across samples.
	NetRxBytes    uint64  `json:"netRxBytes,omitempty"`
	NetTxBytes    uint64  `json:"netTxBytes,omitempty"`
	NetRxRate     float64 `json:"netRxRate,omitempty"` // bytes/s
	NetTxRate     float64 `json:"netTxRate,omitempty"` // bytes/s
	NetRateReady  bool    `json:"netRateReady"`        // false on the very first sample
	NetAvailable  bool    `json:"netAvailable"`
	NetInterfaces int     `json:"netInterfaces,omitempty"`

	// Host / process context.
	Hostname       string  `json:"hostname,omitempty"`
	Platform       string  `json:"platform,omitempty"` // e.g. "darwin/arm64"
	UptimeSeconds  float64 `json:"uptimeSeconds,omitempty"`
	ProcessUptime  float64 `json:"processUptimeSeconds,omitempty"`
	Goroutines     int     `json:"goroutines,omitempty"`
	ProcessHeapMiB float64 `json:"processHeapMiB,omitempty"`

	CollectedAt time.Time `json:"collectedAt"`
}

// netCounters is a cumulative byte snapshot used to derive per-second rates.
type netCounters struct {
	rx, tx     uint64
	interfaces int
	at         time.Time
	ok         bool
}

// netRates converts two cumulative snapshots into bytes/s. Counter resets
// (reboot, interface flap) yield ok=false instead of a bogus spike.
func netRates(prev, cur netCounters) (rxRate, txRate float64, ok bool) {
	if !prev.ok || !cur.ok || prev.at.IsZero() {
		return 0, 0, false
	}
	elapsed := cur.at.Sub(prev.at).Seconds()
	if elapsed <= 0 || elapsed > 600 {
		return 0, 0, false
	}
	if cur.rx < prev.rx || cur.tx < prev.tx {
		return 0, 0, false
	}
	return float64(cur.rx-prev.rx) / elapsed, float64(cur.tx-prev.tx) / elapsed, true
}

// applyNet fills the network fields from two snapshots.
func (m *HostMetrics) applyNet(prev, cur netCounters) {
	if !cur.ok {
		return
	}
	m.NetAvailable = true
	m.NetRxBytes = cur.rx
	m.NetTxBytes = cur.tx
	m.NetInterfaces = cur.interfaces
	if rx, tx, ok := netRates(prev, cur); ok {
		m.NetRxRate = rx
		m.NetTxRate = tx
		m.NetRateReady = true
	}
}

// applyMem fills memory fields, deriving the percentage.
func (m *HostMetrics) applyMem(total, used uint64) {
	if total == 0 {
		return
	}
	if used > total {
		used = total
	}
	m.MemTotal = total
	m.MemUsed = used
	m.MemPercent = clampFloat(float64(used)/float64(total)*100, 0, 100)
	m.MemAvailable = true
}

// applyProcess adds hostname / platform / runtime context shared by all OSes.
func (m *HostMetrics) applyProcess() {
	if name, err := os.Hostname(); err == nil {
		m.Hostname = name
	}
	m.Platform = runtime.GOOS + "/" + runtime.GOARCH
	m.Goroutines = runtime.NumGoroutine()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	m.ProcessHeapMiB = float64(ms.HeapAlloc) / (1024 * 1024)
	m.ProcessUptime = time.Since(processStartedAt).Seconds()
}

var processStartedAt = time.Now()

// diskUsageRoot reports total/used bytes of the filesystem holding "/".
func (m *HostMetrics) applyDiskRoot() {
	total, used, ok := statfsRoot()
	if !ok || total == 0 {
		return
	}
	m.DiskTotal = total
	m.DiskUsed = used
	m.DiskPercent = clampFloat(float64(used)/float64(total)*100, 0, 100)
	m.DiskAvailable = true
}

type hostMetricsSampler struct {
	mu          sync.Mutex
	sample      HostMetrics
	lastRequest time.Time
	loopRunning bool

	// CPU tick baseline for percent (platform-specific payload).
	cpuPrev any

	collecting atomic.Bool // true while background loop is alive (tests / debug)
}

func newHostMetricsSampler() *hostMetricsSampler {
	return &hostMetricsSampler{}
}

// Snapshot returns the latest sample and records that a client is watching.
// The first call (or a call after idle stop) starts a 3s refresh loop that
// exits once clients stop requesting.
func (h *hostMetricsSampler) Snapshot() HostMetrics {
	h.mu.Lock()
	h.lastRequest = time.Now()
	needStart := !h.loopRunning
	empty := h.sample.CollectedAt.IsZero()
	if needStart {
		h.loopRunning = true
	}
	out := h.sample
	h.mu.Unlock()

	if needStart {
		// Synchronous first sample so the UI is not empty for 3s.
		h.refresh()
		go h.loop()
		h.mu.Lock()
		out = h.sample
		h.mu.Unlock()
	} else if empty {
		h.refresh()
		h.mu.Lock()
		out = h.sample
		h.mu.Unlock()
	}
	return out
}

func (h *hostMetricsSampler) loop() {
	h.collecting.Store(true)
	defer h.collecting.Store(false)

	ticker := time.NewTicker(hostMetricsPollInterval)
	defer ticker.Stop()
	for range ticker.C {
		h.mu.Lock()
		idle := time.Since(h.lastRequest) >= hostMetricsIdleStopAfter
		if idle {
			h.loopRunning = false
			h.mu.Unlock()
			return
		}
		h.mu.Unlock()
		h.refresh()
	}
}

func (h *hostMetricsSampler) refresh() {
	next := collectHostMetrics(h.cpuPrev)
	h.mu.Lock()
	h.cpuPrev = next.prev
	h.sample = next.metrics
	h.mu.Unlock()
}

type hostMetricsCollectResult struct {
	metrics HostMetrics
	prev    any
}

func (s *Server) handleHostMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// Host telemetry (load / temperature / network) is admin-only.
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.hostMetrics == nil {
		s.hostMetrics = newHostMetricsSampler()
	}
	writeJSON(w, http.StatusOK, s.hostMetrics.Snapshot())
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func parsePowermetricsCPUTemp(out []byte) (float64, bool) {
	for _, line := range bytes.Split(out, []byte("\n")) {
		s := strings.TrimSpace(string(line))
		lower := strings.ToLower(s)
		if !strings.Contains(lower, "temperature") || !strings.Contains(lower, "cpu") {
			continue
		}
		fields := strings.Fields(s)
		for i := 0; i < len(fields); i++ {
			f := fields[i]
			if i+1 < len(fields) && (strings.EqualFold(fields[i+1], "C") || strings.EqualFold(fields[i+1], "°C")) {
				v, err := strconv.ParseFloat(strings.TrimSuffix(f, ","), 64)
				if err == nil && v > 0 && v < 150 {
					return v, true
				}
			}
			if strings.HasSuffix(strings.ToLower(f), "c") && len(f) > 1 {
				num := strings.TrimSuffix(strings.TrimSuffix(f, "C"), "c")
				v, err := strconv.ParseFloat(num, 64)
				if err == nil && v > 0 && v < 150 {
					return v, true
				}
			}
		}
	}
	return 0, false
}

func parsePowermetricsThermalPressure(out []byte) (string, bool) {
	// "Current pressure level: Nominal"
	for _, line := range bytes.Split(out, []byte("\n")) {
		s := strings.TrimSpace(string(line))
		lower := strings.ToLower(s)
		if !strings.Contains(lower, "pressure") {
			continue
		}
		if i := strings.LastIndex(s, ":"); i >= 0 && i+1 < len(s) {
			level := strings.TrimSpace(s[i+1:])
			if level != "" {
				return level, true
			}
		}
	}
	return "", false
}

// thermalPressureLabelZH maps powermetrics English levels to short Chinese UI text.
func thermalPressureLabelZH(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "nominal":
		return "正常"
	case "fair":
		return "偏高"
	case "serious":
		return "较高"
	case "critical":
		return "过高"
	default:
		if level == "" {
			return "—"
		}
		return level
	}
}

// interfaceCounter is one NIC's cumulative byte counters.
type interfaceCounter struct {
	Name string
	RX   uint64
	TX   uint64
}

// virtualIfacePrefixes are excluded from throughput: loopback and
// software/tunnel devices would double-count real traffic.
var virtualIfacePrefixes = []string{"lo", "gif", "stf", "awdl", "llw", "utun", "ipsec", "ppp", "anpi", "ap", "bridge", "veth", "docker", "tailscale", "tun", "tap", "vmnet", "vnic"}

func isVirtualInterface(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return true
	}
	for _, p := range virtualIfacePrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// sumInterfaceCounters aggregates physical interfaces into one snapshot.
func sumInterfaceCounters(list []interfaceCounter) netCounters {
	out := netCounters{at: time.Now()}
	for _, c := range list {
		if isVirtualInterface(c.Name) {
			continue
		}
		if c.RX == 0 && c.TX == 0 {
			continue
		}
		out.rx += c.RX
		out.tx += c.TX
		out.interfaces++
	}
	out.ok = out.interfaces > 0
	return out
}
