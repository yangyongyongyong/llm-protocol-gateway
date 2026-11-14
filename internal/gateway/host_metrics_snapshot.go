package gateway

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/store"
)

// hostMetricsResolutionStep is how long an on-demand snapshot waits between
// its two samples. The background sampler runs every 10 s, which is fine for a
// chart but useless for "why is it slow right now"; a short window still lets
// per-second rates (CPU, disk, network) mean something.
const hostMetricsResolutionStep = 500 * time.Millisecond

// The console's 「诊断」 and 「诊断包」 are one-shot snapshots. They deliberately do
// not go through the shared sampler, so they never move its baseline or extend
// its idle timer.

// handleHostMetricsSnapshot serves two samples and, with diagnose=1, the
// findings derived from them.
func (s *Server) handleHostMetricsSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.router.HostMetricsDisabled() {
		writeOpenAIError(w, http.StatusConflict, "机器状态动态采集已关闭")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	first := collectHostMetrics(nil)
	time.Sleep(hostMetricsResolutionStep)
	second := collectHostMetrics(first.prev)
	window := second.metrics.CollectedAt.Sub(first.metrics.CollectedAt)
	result := map[string]any{
		"metrics":    second.metrics,
		"previous":   first.metrics,
		"intervalMs": window.Milliseconds(),
	}
	if r.URL.Query().Get("diagnose") == "1" {
		result["diagnosis"] = s.diagnoseHost(first.metrics, second.metrics, window)
	}
	writeJSON(w, http.StatusOK, result)
}

// handleHostDiagnostics downloads the same samples plus the gateway process
// context (runtime, GC, database file), so a report can be attached to a bug
// without asking for server log access.
func (s *Server) handleHostDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.router.HostMetricsDisabled() {
		writeOpenAIError(w, http.StatusConflict, "机器状态动态采集已关闭")
		return
	}
	first := collectHostMetrics(nil)
	time.Sleep(hostMetricsResolutionStep)
	second := collectHostMetrics(first.prev)
	window := second.metrics.CollectedAt.Sub(first.metrics.CollectedAt)
	bundle := s.hostDiagnosticsBundle(r.Context(), first.metrics, second.metrics, window)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="gateway-host-%s.json"`, time.Now().Format("20060102-150405")))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, bundle)
}

func (s *Server) hostDiagnosticsBundle(ctx context.Context, prev, cur HostMetrics, window time.Duration) map[string]any {
	select {
	case <-ctx.Done():
		return nil
	default:
	}
	bundle := map[string]any{
		"generatedAt":  time.Now().Format(time.RFC3339),
		"version":      currentBuildVersion(),
		"runtime":      runtime.Version(),
		"metrics":      cur,
		"previous":     prev,
		"intervalMs":   window.Milliseconds(),
		"diagnosis":    s.diagnoseHost(prev, cur, window),
		"runtimeStats": runtimeStatsSummary(),
	}
	if dbPath, err := store.DefaultDBPath(); err == nil {
		if info, err := os.Stat(dbPath); err == nil {
			bundle["database"] = map[string]any{
				"path":     dbPath,
				"bytes":    info.Size(),
				"modified": info.ModTime().Format(time.RFC3339),
			}
		}
	}
	return bundle
}

// runtimeStatsSummary is the Go-side half of a bug report: GC pauses explain
// latency spikes that host CPU shows as "busy", heap numbers explain RSS.
func runtimeStatsSummary() map[string]any {
	var gc debug.GCStats
	debug.ReadGCStats(&gc)
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	recent := make([]string, 0, 10)
	for index := 0; index < len(gc.Pause) && index < 10; index++ {
		recent = append(recent, gc.Pause[index].String())
	}
	quantile := func(index int) string {
		if index < len(gc.PauseQuantiles) {
			return gc.PauseQuantiles[index].String()
		}
		return ""
	}
	return map[string]any{
		"goroutines": runtime.NumGoroutine(),
		"numGC":      mem.NumGC,
		"pauseTotal": gc.PauseTotal.String(),
		"pauseQuantiles": map[string]string{
			"p50": quantile(0), "p75": quantile(1), "p90": quantile(2), "p99": quantile(3),
		},
		"recentPauses":    recent,
		"heapAllocMiB":    float64(mem.HeapAlloc) / (1 << 20),
		"heapInuseMiB":    float64(mem.HeapInuse) / (1 << 20),
		"heapIdleMiB":     float64(mem.HeapIdle) / (1 << 20),
		"heapReleasedMiB": float64(mem.HeapReleased) / (1 << 20),
		"sysMiB":          float64(mem.Sys) / (1 << 20),
		"nextGCMiB":       float64(mem.NextGC) / (1 << 20),
	}
}

// HostDiagnosis is the machine page's verdict: a headline plus the findings
// behind it, so the numbers get a plain-language reading.
type HostDiagnosis struct {
	Verdict     string           `json:"verdict"`
	Severity    string           `json:"severity"`
	WindowMS    int64            `json:"windowMs"`
	Findings    []HostDiagnostic `json:"findings"`
	Gateway     GatewayActivity  `json:"gateway"`
	EvaluatedAt time.Time        `json:"evaluatedAt"`
}

// GatewayActivity puts the host numbers in the gateway's own context: the same
// CPU load means something different when 40 requests are streaming through.
type GatewayActivity struct {
	Version       string  `json:"version,omitempty"`
	Goroutines    int     `json:"goroutines"`
	InFlight      int64   `json:"inFlight"`
	RequestRate   float64 `json:"requestRate"` // requests/s produced in the last minute
	RequestsDelta int64   `json:"requestsDelta"`
}

type HostDiagnostic struct {
	ID       string `json:"id"`
	Severity string `json:"severity"` // ok | info | warn | critical
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Action   string `json:"action,omitempty"`
}

func (s *Server) diagnoseHost(prev, cur HostMetrics, window time.Duration) HostDiagnosis {
	windowSeconds := window.Seconds()
	if windowSeconds <= 0 {
		windowSeconds = 1
	}
	activity := s.gatewayActivity(window)
	findings := evaluateHostFindings(prev, cur, window, activity)
	sort.SliceStable(findings, func(a, b int) bool {
		return hostSeverityRank(findings[a].Severity) > hostSeverityRank(findings[b].Severity)
	})
	severity := "ok"
	for _, finding := range findings {
		if rank := hostSeverityRank(finding.Severity); rank > hostSeverityRank(severity) {
			severity = finding.Severity
		}
	}
	verdict := map[string]string{
		"ok":       "运行正常",
		"info":     "指标偏高，暂不影响服务",
		"warn":     "有需要注意的指标",
		"critical": "存在严重问题，需要立即处理",
	}[severity]
	return HostDiagnosis{
		Verdict:     verdict,
		Severity:    severity,
		WindowMS:    window.Milliseconds(),
		Findings:    findings,
		Gateway:     activity,
		EvaluatedAt: cur.CollectedAt,
	}
}

func hostSeverityRank(severity string) int {
	switch severity {
	case "critical":
		return 3
	case "warn":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}

// gatewayActivity reports the gateway's own load. The request counter is fed
// by the log middleware that already runs for every call, so diagnosing a
// spike costs nothing extra on the request path.
func (s *Server) gatewayActivity(window time.Duration) GatewayActivity {
	total := s.requestCounter.Load()
	delta := total - s.activityBaseline.Swap(total)
	rate := 0.0
	if window > 0 {
		rate = float64(delta) / window.Seconds()
	}
	return GatewayActivity{
		Version:       currentBuildVersion(),
		Goroutines:    runtime.NumGoroutine(),
		InFlight:      s.currentInFlight(),
		RequestsDelta: delta,
		RequestRate:   rate,
	}
}

// currentInFlight sums the per-provider admission lanes, i.e. model calls
// actually streaming right now (waiting in a lane queue does not count).
func (s *Server) currentInFlight() int64 {
	if s == nil || s.providerLanes == nil {
		return 0
	}
	s.providerLanes.mu.Lock()
	lanes := make([]*providerLane, 0, len(s.providerLanes.lanes))
	for _, lane := range s.providerLanes.lanes {
		lanes = append(lanes, lane)
	}
	s.providerLanes.mu.Unlock()
	var total int64
	for _, lane := range lanes {
		total += int64(lane.inFlight())
	}
	return total
}

// evaluateHostFindings turns two snapshots into findings. Every threshold is
// about *sustained* pressure — one two-second spike is noise — so the rules
// also weigh the gateway's own share and the available window.
func evaluateHostFindings(prev, cur HostMetrics, window time.Duration, activity GatewayActivity) []HostDiagnostic {
	var out []HostDiagnostic
	add := func(id, severity, title, detail, action string) {
		out = append(out, HostDiagnostic{ID: id, Severity: severity, Title: title, Detail: detail, Action: action})
	}

	if cores := cur.CPUCount; cores > 0 && cur.Load1 > float64(cores) {
		add("cpu-load", "warn", "CPU 排队等待",
			fmt.Sprintf("1 分钟负载 %.2f，已超过 %d 个逻辑核的处理能力，新请求会排队。", cur.Load1, cores),
			"看趋势确认是网关自己在跑还是同机别的进程；持续偏高就该加核或把同机服务迁走。")
	}
	if cur.CPUPercent >= 90 {
		add("cpu-usage", "warn", "CPU 使用率接近打满",
			fmt.Sprintf("采样窗口内 CPU 平均使用 %.0f%%，留给突发流量的余量很少。", cur.CPUPercent),
			"结合「网关 CPU」判断：网关占比也高时，检查并发流式请求与大上下文。")
	}
	if cur.ProcessCPUPct > 0 && cur.CPUPercent >= 50 && cur.ProcessCPUPct >= cur.CPUPercent*0.6 {
		add("process-cpu", "info", "CPU 主要由网关自己消耗",
			fmt.Sprintf("网关进程约占 %.0f%% CPU，超过整机使用量的六成。", cur.ProcessCPUPct),
			"多为流式转发与协议转换的并发开销；若同时延迟升高，优先看并发数与上游响应速度。")
	}
	if cur.MemAvailable && cur.MemPercent >= 92 {
		severity := "warn"
		if cur.MemPercent >= 97 {
			severity = "critical"
		}
		add("mem", severity, "可用内存不足",
			fmt.Sprintf("内存已用 %.0f%%（%s / %s），剩余可用 %s。", cur.MemPercent,
				formatMiB(cur.MemUsed), formatMiB(cur.MemTotal), formatMiB(cur.MemTotal-cur.MemUsed)),
			"页缓存会随压力自动回收；持续 97% 以上时 OOM killer 可能直接杀掉网关。")
	}
	if cur.SwapAvailable && cur.SwapTotal > 0 {
		ratio := float64(cur.SwapUsed) / float64(cur.SwapTotal)
		if ratio >= 0.5 && cur.SwapUsed > 256<<20 {
			add("swap", "warn", "交换分区使用偏高",
				fmt.Sprintf("已用 %s / %s，频繁换页会拉长每条请求的尾延迟。", formatMiB(cur.SwapUsed), formatMiB(cur.SwapTotal)),
				"减少同机其他服务的内存占用，或直接加内存。")
		}
	}
	if cur.DiskAvailable && cur.DiskPercent >= 95 {
		add("disk", "critical", "根分区即将写满",
			fmt.Sprintf("%s 已用 %.1f%%，继续写入会失败。", cur.DiskDevice, cur.DiskPercent),
			"先清理日志与旧备份；被删除的库文件要 VACUUM 才会把空间还给文件系统。")
	} else if cur.DiskAvailable && cur.DiskPercent >= 85 {
		add("disk", "warn", "根分区空间偏紧",
			fmt.Sprintf("%s 已用 %.1f%%，低于 15%% 的余量容易在日志暴涨时写满。", cur.DiskDevice, cur.DiskPercent),
			"预留至少 15% 空间，或调低请求日志的保留天数。")
	}
	if cur.CgroupMemLimit > 0 && cur.CgroupMemUsed > 0 {
		if ratio := float64(cur.CgroupMemUsed) / float64(cur.CgroupMemLimit) * 100; ratio >= 90 {
			add("cgroup", "warn", "容器内存接近上限",
				fmt.Sprintf("cgroup 已用 %s / %s（%.0f%%），超出后内核会直接杀掉进程。", formatMiB(cur.CgroupMemUsed), formatMiB(cur.CgroupMemLimit), ratio),
				"提高容器内存上限，或减少同容器内的并发缓冲。")
		}
	}
	if cur.TempAvailable && cur.TempC >= 85 {
		add("temp", "warn", "CPU 温度偏高",
			fmt.Sprintf("CPU 温度 %.1f°C，持续高温会触发降频。", cur.TempC),
			"检查散热与风道；多数云主机不暴露真实温度，可忽略此项。")
	}
	if cur.DiskIO != nil && cur.DiskIO.Util >= 95 {
		add("disk-io", "warn", "磁盘几乎满负荷",
			fmt.Sprintf("根盘 %s 在窗口内 %.0f%% 的时间都处于忙碌状态，写入会排队。", cur.DiskIO.Device, cur.DiskIO.Util),
			"看「磁盘吞吐」趋势判断是网关写日志还是备份 / VACUUM 在抢盘。")
	}
	if cur.PressureAvailable {
		if cur.PressureMem >= 10 {
			add("psi-mem", "warn", "进程因内存被阻塞",
				fmt.Sprintf("窗口内 %.1f%% 的时间有任务因等待内存而停住。", cur.PressureMem),
				"优先怀疑内存不足或正在大量换页。")
		}
		if cur.PressureIO >= 20 {
			add("psi-io", "warn", "进程因磁盘 IO 被阻塞",
				fmt.Sprintf("窗口内 %.1f%% 的时间有任务因等待磁盘而停住。", cur.PressureIO),
				"检查是否有备份 / VACUUM / 日志轮转在抢占磁盘。")
		}
		if cur.PressureCPU >= 25 {
			add("psi-cpu", "info", "CPU 出现争抢",
				fmt.Sprintf("窗口内 %.1f%% 的时间有任务在等 CPU。", cur.PressureCPU),
				"CPU 没打满但仍在等待，说明同机有更抢 CPU 的进程，或 cgroup 配额偏小。")
		}
	}
	if cur.NetAvailable && prev.NetAvailable && cur.NetDrops > prev.NetDrops {
		add("net-drops", "info", "物理网卡出现丢包",
			fmt.Sprintf("累计丢包 %d（期间新增 %d），已收到的数据可能不完整。", cur.NetDrops, cur.NetDrops-prev.NetDrops),
			"偶发少量丢包一般是内核接收缓冲不足；持续增长需调大缓冲或换更高带宽的实例。")
	}
	if cur.NetAvailable && prev.NetAvailable && cur.NetRxErrors+cur.NetTxErrors > prev.NetRxErrors+prev.NetTxErrors {
		add("net-errors", "info", "网卡出现错误计数",
			fmt.Sprintf("累计收发错误 %d 次（期间新增 %d），通常来自链路或驱动。",
				cur.NetRxErrors+cur.NetTxErrors,
				(cur.NetRxErrors+cur.NetTxErrors)-(prev.NetRxErrors+prev.NetTxErrors)), "")
	}
	if window > 5*time.Second {
		add("sample-slow", "warn", "单次采集耗时过长",
			fmt.Sprintf("两次采样的间隔用了 %.0f ms，机器状态页刷新会明显变慢。", float64(window.Milliseconds())),
			"macOS 上 powermetrics / smartctl 调用最慢；磁盘盘符异常时也会变慢。")
	}
	if len(out) == 0 {
		add("ok", "ok", "各项指标都在正常范围",
			fmt.Sprintf("CPU、内存、磁盘与网络均未超过告警阈值；网关当前 %d 个 goroutine，窗口内处理了 %d 个请求。",
				activity.Goroutines, activity.RequestsDelta), "")
	}
	return out
}

func formatMiB(bytes uint64) string {
	const gib = 1024 * 1024 * 1024
	if bytes >= 1<<30 {
		return strconv.FormatFloat(float64(bytes)/float64(gib), 'f', 1, 64) + " GiB"
	}
	return strconv.FormatFloat(float64(bytes)/float64(1<<20), 'f', 0, 64) + " MiB"
}
