package gateway

import (
	"bytes"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/store"
)

// hostMetricsPollInterval is the sampler's cadence while someone is watching:
// either a console page is subscribed to the live stream or it polled within
// hostMetricsActiveWindow. The console charts one point per sample, so this is
// also the chart's resolution.
const hostMetricsPollInterval = 3 * time.Second

// hostMetricsIdleInterval is the cadence when nobody is watching. The sampler
// never stops: a gap in the chart is exactly what an operator cannot explain
// later, so idle means "slower", not "off". Sampling costs 15–50 ms per turn,
// i.e. well under a tenth of one core at this rate.
const hostMetricsIdleInterval = 15 * time.Second

// hostMetricsActiveWindow is how recently a client must have polled for the
// full-rate cadence to apply.
const hostMetricsActiveWindow = 30 * time.Second

// hostMetricsHistoryWindow is how much history the in-memory ring keeps. It
// matches the console's chart window; the database keeps the same span so a
// restart restores exactly what the page would have drawn.
const hostMetricsHistoryWindow = 60 * time.Minute

// hostMetricsRetention is how long samples stay in SQLite. 24 h covers "what
// happened last night" without letting a 3 s cadence grow unbounded (~3 MB/day).
const hostMetricsRetention = 24 * time.Hour

// HostMetrics is one snapshot of the host the gateway runs on. It is only
// collected while an admin console page is polling it (see the sampler below).
type HostMetrics struct {
	Load1             float64       `json:"load1"`
	Load5             float64       `json:"load5"`
	Load15            float64       `json:"load15"`
	CPUPercent        float64       `json:"cpuPercent"`     // 0–100, all-core average
	CPULoad           float64       `json:"cpuLoadPercent"` // load1 ÷ cores
	CPUCount          int           `json:"cpuCount"`
	CPUMHz            float64       `json:"cpuMhz,omitempty"`
	CPUPhysicalCores  int           `json:"cpuPhysicalCores,omitempty"` // 0 when the platform cannot tell
	CPUModel          string        `json:"cpuModel,omitempty"`
	CPUCores          []HostCPUCore `json:"cpuCores,omitempty"`
	CPUCoresAvailable bool          `json:"cpuCoresAvailable"`

	TempC                    float64 `json:"tempC,omitempty"`
	TempAvailable            bool    `json:"tempAvailable"`
	TempSource               string  `json:"tempSource,omitempty"`
	ThermalPressure          string  `json:"thermalPressure,omitempty"`
	ThermalPressureAvailable bool    `json:"thermalPressureAvailable"`
	ThermalPressureSource    string  `json:"thermalPressureSource,omitempty"`

	// Memory (bytes). "Used" matches `free` (total − available), so page cache
	// is counted as available; MemCached is that cache for the composition bar.
	MemTotal       uint64  `json:"memTotal,omitempty"`
	MemUsed        uint64  `json:"memUsed,omitempty"`
	MemPercent     float64 `json:"memPercent,omitempty"`
	MemAvailable   bool    `json:"memAvailable"`
	MemCached      uint64  `json:"memCached,omitempty"`
	MemAnon        uint64  `json:"memAnon,omitempty"`
	MemSlab        uint64  `json:"memSlab,omitempty"`
	HostMemHitRate float64 `json:"hostMemHitRate,omitempty"` // page-cache hit, −1 when unknown
	SwapTotal      uint64  `json:"swapTotal,omitempty"`
	SwapUsed       uint64  `json:"swapUsed,omitempty"`
	SwapAvailable  bool    `json:"swapAvailable"`

	// Root filesystem (bytes) plus every mounted volume worth showing.
	DiskTotal     uint64     `json:"diskTotal,omitempty"`
	DiskUsed      uint64     `json:"diskUsed,omitempty"`
	DiskPercent   float64    `json:"diskPercent,omitempty"`
	DiskAvailable bool       `json:"diskAvailable"`
	DiskDevice    string     `json:"diskDevice,omitempty"` // physical disk behind "/"
	Volumes       []HostDisk `json:"volumes,omitempty"`

	// Disk throughput of DiskDevice, derived from two samples.
	DiskIO        *HostDiskIO `json:"diskIO,omitempty"`
	DiskReadRate  float64     `json:"diskReadRate,omitempty"`  // bytes/s
	DiskWriteRate float64     `json:"diskWriteRate,omitempty"` // bytes/s
	DiskIOREady   bool        `json:"diskIOReady"`

	// Physical disk temperatures (SMART / NVMe), when smartctl is available.
	DiskTemps []HostDiskTemp `json:"diskTemps,omitempty"`

	// Chassis / CPU fans (SMC on macOS, hwmon on Linux).
	Fans []HostFanSpeed `json:"fans,omitempty"`

	// Network counters since boot + per-second rates across samples. Only
	// physical interfaces are summed (loopback / tunnels would double count).
	NetRxBytes       uint64  `json:"netRxBytes,omitempty"`
	NetTxBytes       uint64  `json:"netTxBytes,omitempty"`
	NetRxRate        float64 `json:"netRxRate,omitempty"` // bytes/s
	NetTxRate        float64 `json:"netTxRate,omitempty"`
	NetRxPacketsRate float64 `json:"netRxPacketsRate,omitempty"`
	NetTxPacketsRate float64 `json:"netTxPacketsRate,omitempty"`
	NetRxErrors      uint64  `json:"netRxErrors,omitempty"`
	NetTxErrors      uint64  `json:"netTxErrors,omitempty"`
	NetDrops         uint64  `json:"netDrops,omitempty"`
	NetRateReady     bool    `json:"netRateReady"` // false on the very first sample
	NetAvailable     bool    `json:"netAvailable"`
	NetInterfaces    int     `json:"netInterfaces,omitempty"`

	// PSI (Linux): share of wall time some task was stalled on CPU / memory /
	// IO. Zero is a real reading, hence the separate availability flag.
	PressureCPU       float64 `json:"pressureCpu,omitempty"`
	PressureCPUFull   float64 `json:"pressureCpuFull,omitempty"`
	PressureMem       float64 `json:"pressureMem,omitempty"`
	PressureMemFull   float64 `json:"pressureMemFull,omitempty"`
	PressureIO        float64 `json:"pressureIo,omitempty"`
	PressureIOFull    float64 `json:"pressureIoFull,omitempty"`
	PressureAvailable bool    `json:"pressureAvailable"`

	// Host / OS identity.
	Hostname      string    `json:"hostname,omitempty"`
	Platform      string    `json:"platform,omitempty"` // e.g. "linux/amd64"
	OSName        string    `json:"osName,omitempty"`   // "Ubuntu 24.04.4 LTS"
	Kernel        string    `json:"kernel,omitempty"`
	BootTime      time.Time `json:"bootTime"`
	UptimeSeconds float64   `json:"uptimeSeconds,omitempty"`

	// Gateway process.
	ProcessUptime         float64   `json:"processUptimeSeconds,omitempty"`
	ProcessStarted        time.Time `json:"processStartedAt,omitempty"`
	ProcessRestarts       int       `json:"processRestarts,omitempty"`
	Goroutines            int       `json:"goroutines,omitempty"`
	OSThreads             int       `json:"osThreads,omitempty"`
	ProcessHeapMiB        float64   `json:"processHeapMiB,omitempty"`
	ProcessSysMiB         float64   `json:"processSysMiB,omitempty"`
	ProcessRSSMiB         float64   `json:"processRssMiB,omitempty"`
	ProcessRSSFromRuntime bool      `json:"processRssFromRuntime"`
	ProcessPeakMiB        float64   `json:"processPeakMiB,omitempty"`
	ProcessThreads        int       `json:"processThreads,omitempty"`
	ProcessFDs            int       `json:"processFds,omitempty"`
	ProcessCPUPct         float64   `json:"processCpuPercent,omitempty"`
	DBFileBytes           uint64    `json:"dbFileBytes,omitempty"`
	DBFileUpdated         time.Time `json:"dbFileUpdated,omitempty"`

	// Container memory limit (cgroup v2 / v1), when one applies.
	CgroupMemLimit uint64 `json:"cgroupMemLimit,omitempty"`
	CgroupMemUsed  uint64 `json:"cgroupMemUsed,omitempty"`

	SampleCost  float64   `json:"sampleCostMs,omitempty"`
	CollectedAt time.Time `json:"collectedAt"` // omitempty has no effect on time.Time
}

// HostCPUCore is one logical CPU's utilisation for the page's per-core strip.
type HostCPUCore struct {
	ID      int     `json:"id"`
	Percent float64 `json:"percent"`
}

// HostDisk is one filesystem ("/" for the root, "/boot", …) with its usage.
type HostDisk struct {
	Mount   string  `json:"mount"`
	Device  string  `json:"device,omitempty"`
	FSType  string  `json:"fstype,omitempty"`
	Total   uint64  `json:"total"`
	Used    uint64  `json:"used"`
	Percent float64 `json:"percent"`
}

// HostDiskIO is the root disk's activity between the last two samples.
type HostDiskIO struct {
	Device    string  `json:"device"`
	ReadRate  float64 `json:"readRate"`  // bytes/s
	WriteRate float64 `json:"writeRate"` // bytes/s
	Util      float64 `json:"util"`      // % of wall time with requests in flight
	AwaitMs   float64 `json:"awaitMs"`   // ms per completed request
}

// HostDiskTemp is one physical disk's temperature reading for the machine page.
type HostDiskTemp struct {
	Device   string  `json:"device"`             // e.g. disk0 / nvme0n1
	Model    string  `json:"model,omitempty"`    // SMART / MediaName
	TempC    float64 `json:"tempC"`              // Celsius
	Internal *bool   `json:"internal,omitempty"` // darwin: built-in vs external
	Source   string  `json:"source,omitempty"`   // smartctl/nvme, smartctl/ata, …
}

// HostFanSpeed is one chassis/CPU fan reading for the machine page.
type HostFanSpeed struct {
	ID      int     `json:"id"`
	Name    string  `json:"name,omitempty"`
	RPM     float64 `json:"rpm"`
	MinRPM  float64 `json:"minRpm,omitempty"`
	MaxRPM  float64 `json:"maxRpm,omitempty"`
	Percent float64 `json:"percent"` // 0–100, relative to min..max (or 0..max)
	Source  string  `json:"source,omitempty"`
}

// fanSpeedPercent maps actual RPM into 0–100 using the fan's usable range.
func fanSpeedPercent(rpm, minRPM, maxRPM float64) float64 {
	if maxRPM > minRPM && minRPM >= 0 {
		return clampFloat((rpm-minRPM)/(maxRPM-minRPM)*100, 0, 100)
	}
	if maxRPM > 0 {
		return clampFloat(rpm/maxRPM*100, 0, 100)
	}
	return 0
}

// netCounters is a cumulative counter snapshot used to derive per-second rates.
type netCounters struct {
	rx, tx                    uint64
	rxPkts, txPkts            uint64
	rxErrors, txErrors, drops uint64
	interfaces                int
	at                        time.Time
	ok                        bool
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

// packetRates is netRates for packets; a counter reset just hides the rate.
func packetRates(prev, cur netCounters) (rx, tx float64, ok bool) {
	if !prev.ok || !cur.ok || prev.at.IsZero() {
		return 0, 0, false
	}
	elapsed := cur.at.Sub(prev.at).Seconds()
	if elapsed <= 0 || elapsed > 600 {
		return 0, 0, false
	}
	if cur.rxPkts < prev.rxPkts || cur.txPkts < prev.txPkts {
		return 0, 0, false
	}
	return float64(cur.rxPkts-prev.rxPkts) / elapsed, float64(cur.txPkts-prev.txPkts) / elapsed, true
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
	m.NetRxErrors = cur.rxErrors
	m.NetTxErrors = cur.txErrors
	m.NetDrops = cur.drops
	if rx, tx, ok := netRates(prev, cur); ok {
		m.NetRxRate, m.NetTxRate, m.NetRateReady = rx, tx, true
	}
	if rx, tx, ok := packetRates(prev, cur); ok {
		m.NetRxPacketsRate, m.NetTxPacketsRate = rx, tx
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

var processStartedAt = time.Now()

// hostMetricsPersistBatch caps how many rows one flush writes; a long-idle
// ring catch-up writes everything in one transaction, so the cap only exists
// to bound the statement size.
const hostMetricsPersistBatch = 400

// hostMetricsPersistEvery is how often buffered samples reach SQLite. Writing
// every sample would mean a commit per poll; batching a minute's worth in one
// transaction keeps WAL growth and fsync count negligible.
const hostMetricsPersistEvery = time.Minute

// hostMetricsStore is the persistence contract the sampler needs. *store.Store
// implements it; a Server wired without a store simply keeps history in memory.
type hostMetricsStore interface {
	AppendHostMetricSamples(samples []store.HostMetricSample) error
	HostMetricSamplesSince(fromMs int64) ([]store.HostMetricSample, error)
	PruneHostMetricSamples(beforeMs int64) (int64, error)
}

// hostMetricsSampler runs for the life of the process. It keeps a one-hour
// ring in memory (so a freshly opened page draws the whole chart immediately)
// and flushes new points to SQLite in batches (so a restart does not erase the
// curve).
type hostMetricsSampler struct {
	mu          sync.Mutex
	sample      HostMetrics
	samples     []store.HostMetricSample // ring, oldest first
	lastRequest time.Time
	// ready is closed once restore + the first sample finished. A second
	// caller waits for it instead of reading an empty ring: two console tabs
	// opening at boot used to race, and the loser got a blank chart.
	ready chan struct{}

	// CPU tick baseline for percent (platform-specific payload).
	cpuPrev any

	// persistence
	store       hostMetricsStore
	pending     []store.HostMetricSample // not yet flushed
	persistedAt time.Time
	lastPrune   time.Time

	startOnce sync.Once

	// subscribers are the live SSE clients; their count decides the cadence.
	subscribers atomic.Int64

	collecting atomic.Bool // true while the sampler loop is alive (tests / debug)
	// enabled is the persisted machine-status switch. The loop stays alive but
	// skips collection while false, so disabling it costs no host sampling work.
	enabled atomic.Bool
}

func newHostMetricsSampler(stores ...hostMetricsStore) *hostMetricsSampler {
	h := &hostMetricsSampler{ready: make(chan struct{})}
	h.enabled.Store(true)
	if len(stores) > 0 {
		h.store = stores[0]
	}
	return h
}

// Start loads the persisted history and launches the sampling loop. It is
// idempotent and safe to call from more than one place; callers block until
// the ring is usable, so nobody ever reads an empty history on a cold start.
func (h *hostMetricsSampler) Start() {
	h.startOnce.Do(func() {
		h.restore()
		// Synchronous first sample so the first paint is never empty, unless the
		// operator explicitly disabled machine sampling before startup.
		if h.enabled.Load() {
			h.refresh()
		}
		close(h.ready)
		go h.loop()
	})
	<-h.ready
}

// restore seeds the ring from SQLite. A fresh install simply finds nothing.
func (h *hostMetricsSampler) restore() {
	if h.store == nil {
		return
	}
	since := time.Now().Add(-hostMetricsHistoryWindow).UnixMilli()
	samples, err := h.store.HostMetricSamplesSince(since)
	if err != nil || len(samples) == 0 {
		return
	}
	h.mu.Lock()
	h.samples = samples
	h.mu.Unlock()
}

// Snapshot returns the latest sample and marks a console page as watching (so
// the loop samples at full rate for the next hostMetricsActiveWindow).
func (h *hostMetricsSampler) Snapshot() HostMetrics {
	h.Start()
	h.mu.Lock()
	h.lastRequest = time.Now()
	out := h.sample
	h.mu.Unlock()
	return out
}

// Latest returns the newest sample without marking a viewer. The stream pump
// reads it on every tick, and touching here would restart the active window
// forever — the idle cadence would never kick in.
func (h *hostMetricsSampler) Latest() HostMetrics {
	h.Start()
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sample
}

// History returns the samples inside the chart window, oldest first.
func (h *hostMetricsSampler) History() []store.HostMetricSample {
	h.Start()
	h.mu.Lock()
	out := make([]store.HostMetricSample, len(h.samples))
	copy(out, h.samples)
	h.mu.Unlock()
	return out
}

// Subscribe registers a live viewer; the returned function unregisters it.
func (h *hostMetricsSampler) Subscribe() (unsubscribe func()) {
	h.Start()
	h.subscribers.Add(1)
	h.touch()
	var once sync.Once
	return func() {
		once.Do(func() {
			h.subscribers.Add(-1)
			h.touch()
		})
	}
}

func (h *hostMetricsSampler) touch() {
	h.mu.Lock()
	h.lastRequest = time.Now()
	h.mu.Unlock()
}

// active reports whether full-rate sampling applies right now.
func (h *hostMetricsSampler) active() bool {
	if h.subscribers.Load() > 0 {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return time.Since(h.lastRequest) < hostMetricsActiveWindow
}

// SetEnabled pauses or resumes collection without tearing down the loop.
func (h *hostMetricsSampler) SetEnabled(enabled bool) {
	h.enabled.Store(enabled)
	h.touch()
	if enabled {
		h.Start()
	}
}

func (h *hostMetricsSampler) loop() {
	h.collecting.Store(true)
	defer h.collecting.Store(false)

	// A short tick lets the loop switch between the active and idle cadence
	// without waiting out a full idle interval after someone opens the page.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastSample := time.Now()
	for tick := range ticker.C {
		if !h.enabled.Load() {
			lastSample = tick
			continue
		}
		interval := hostMetricsIdleInterval
		if h.active() {
			interval = hostMetricsPollInterval
		}
		// Measure against the tick's own timestamp, not time.Now() after the
		// work: anchoring to "now after refresh" adds the collection cost to
		// every period and, with a 1 s tick, silently turns 3 s into 4 s.
		if tick.Sub(lastSample) >= interval {
			h.refresh()
			lastSample = tick
		}
		h.flush(false)
	}
}

func (h *hostMetricsSampler) refresh() {
	// cpuPrev 是上一次采集的 CPU tick 基线：并发 refresh 时必须在锁内取出，
	// 否则与另一路写入构成 data race（-race 在 Linux 上直接报错）。
	h.mu.Lock()
	prev := h.cpuPrev
	h.mu.Unlock()

	started := time.Now()
	next := collectHostMetrics(prev)
	next.metrics.SampleCost = float64(time.Since(started).Microseconds()) / 1000
	point := hostMetricSampleFrom(next.metrics)
	h.mu.Lock()
	h.cpuPrev = next.prev
	h.sample = next.metrics
	h.appendLocked(point)
	h.mu.Unlock()
}

// appendLocked adds one point to the ring, dropping anything that fell out of
// the window. Called with h.mu held.
func (h *hostMetricsSampler) appendLocked(point store.HostMetricSample) {
	if point.AtMs <= 0 {
		return
	}
	if n := len(h.samples); n > 0 && h.samples[n-1].AtMs >= point.AtMs {
		// Clock went backwards or two samples landed in the same millisecond:
		// replace rather than emit an out-of-order point.
		if h.samples[n-1].AtMs == point.AtMs {
			h.samples[n-1] = point
			h.pending = append(h.pending, point)
		}
		return
	}
	h.samples = append(h.samples, point)
	cutoff := point.AtMs - hostMetricsHistoryWindow.Milliseconds()
	drop := 0
	for drop < len(h.samples) && h.samples[drop].AtMs < cutoff {
		drop++
	}
	if drop > 0 {
		kept := make([]store.HostMetricSample, len(h.samples)-drop)
		copy(kept, h.samples[drop:])
		h.samples = kept
	}
	h.pending = append(h.pending, point)
}

// flush writes buffered samples to SQLite. force ignores the batching interval
// (used on shutdown and by tests).
func (h *hostMetricsSampler) flush(force bool) {
	if h.store == nil {
		return
	}
	h.mu.Lock()
	if len(h.pending) == 0 {
		h.mu.Unlock()
		return
	}
	if !force && time.Since(h.persistedAt) < hostMetricsPersistEvery {
		h.mu.Unlock()
		return
	}
	batch := make([]store.HostMetricSample, len(h.pending))
	copy(batch, h.pending)
	h.pending = h.pending[:0]
	h.persistedAt = time.Now()
	pruneDue := time.Since(h.lastPrune) >= time.Hour
	if pruneDue {
		h.lastPrune = time.Now()
	}
	h.mu.Unlock()

	for start := 0; start < len(batch); start += hostMetricsPersistBatch {
		end := start + hostMetricsPersistBatch
		if end > len(batch) {
			end = len(batch)
		}
		if err := h.store.AppendHostMetricSamples(batch[start:end]); err != nil {
			slog.Warn("host metrics: persist samples failed", "error", err)
			break
		}
	}
	if pruneDue {
		if _, err := h.store.PruneHostMetricSamples(time.Now().Add(-hostMetricsRetention).UnixMilli()); err != nil {
			slog.Warn("host metrics: prune samples failed", "error", err)
		}
	}
}

// Stop flushes whatever is buffered. The loop itself exits with the process.
func (h *hostMetricsSampler) Stop() {
	h.flush(true)
}

// hostMetricSampleFrom projects one HostMetrics onto the persisted row.
func hostMetricSampleFrom(m HostMetrics) store.HostMetricSample {
	point := store.HostMetricSample{
		AtMs:       m.CollectedAt.UnixMilli(),
		CPU:        m.CPUPercent,
		Load:       m.Load1,
		MemUsed:    m.MemUsed,
		MemTotal:   m.MemTotal,
		MemPercent: m.MemPercent,
		ProcessCPU: m.ProcessCPUPct,
		NetRx:      m.NetRxRate,
		NetTx:      m.NetTxRate,
		Goroutines: m.Goroutines,
		RSS:        m.ProcessRSSMiB,
		FDs:        m.ProcessFDs,
	}
	if m.DiskIO != nil {
		point.DiskRead, point.DiskWrite = m.DiskIO.ReadRate, m.DiskIO.WriteRate
	} else {
		point.DiskRead, point.DiskWrite = m.DiskReadRate, m.DiskWriteRate
	}
	return point
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
	if s.router.HostMetricsDisabled() {
		writeOpenAIError(w, http.StatusConflict, "机器状态动态采集已关闭")
		return
	}
	writeJSON(w, http.StatusOK, s.hostSampler().Snapshot())
}

// StartHostMetrics brings the sampler up with the process instead of with the
// first console visit. Without it the curve only exists while someone has the
// machine page open: the sampler was only ever built lazily from a request, so
// a restart left a hole exactly as wide as the gap until the next visit, and
// SQLite — which does keep every point it was handed — had nothing to restore.
func (s *Server) StartHostMetrics() {
	sampler := s.hostSampler()
	sampler.SetEnabled(!s.router.HostMetricsDisabled())
	sampler.Start()
	s.hostMetricsPump()
}

// hostSampler builds the process-wide sampler once (two concurrent first polls
// used to create two, and the loser's goroutine was never read again).
func (s *Server) hostSampler() *hostMetricsSampler {
	s.hostMetricsOnce.Do(func() {
		if s.hostMetrics == nil {
			s.hostMetrics = newHostMetricsSampler(s.hostMetricStore)
		}
	})
	return s.hostMetrics
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

func roundTemp(v float64) float64 { return math.Round(v*10) / 10 }

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

// interfaceCounter is one NIC's cumulative counters.
type interfaceCounter struct {
	Name     string
	RX, TX   uint64
	RXPkts   uint64
	TXPkts   uint64
	RXErrors uint64
	TXErrors uint64
	RXDrops  uint64
}

// virtualIfacePrefixes are excluded from throughput: loopback and
// software/tunnel devices would double-count real traffic. "ifb" is the
// traffic-shaping redirect device, which carries the mirrored copy of every
// packet it redirects.
var virtualIfacePrefixes = []string{"lo", "gif", "stf", "awdl", "llw", "utun", "ipsec", "ppp", "anpi", "ap", "ifb", "bridge", "veth", "docker", "tailscale", "tun", "tap", "vmnet", "vnic", "virbr", "lxc", "flannel", "cni", "kube"}

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
		if c.RX == 0 && c.TX == 0 && c.RXPkts == 0 && c.TXPkts == 0 {
			continue
		}
		out.rx += c.RX
		out.tx += c.TX
		out.rxPkts += c.RXPkts
		out.txPkts += c.TXPkts
		out.rxErrors += c.RXErrors
		out.txErrors += c.TXErrors
		out.drops += c.RXDrops
		out.interfaces++
	}
	out.ok = out.interfaces > 0
	return out
}
