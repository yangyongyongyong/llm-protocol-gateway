package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/config"
	"github.com/luca/llm-protocol-gateway/internal/cursor"
	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/gateway"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
	"github.com/luca/llm-protocol-gateway/internal/netutil"
	"github.com/luca/llm-protocol-gateway/internal/store"
	"github.com/luca/llm-protocol-gateway/internal/tunnel"
)

const DefaultPort = 18093

const (
	// gatewayReadHeaderTimeout bounds how long a client may take to send its
	// request line and headers. Past this it is a slow-loris probe, not a
	// client.
	gatewayReadHeaderTimeout = 10 * time.Second
	// gatewayReadTimeout bounds the whole request read, body included. It is
	// generous on purpose: a Claude Code turn can carry tens of MB of tool
	// output or a batch of images, and a bound tight enough for small JSON
	// would cut legitimate uploads on a slow link.
	gatewayReadTimeout = 5 * time.Minute
	// gatewayIdleTimeout closes keep-alive connections a client has stopped
	// using, so a half-open TCP connection cannot pin a goroutine and a file
	// descriptor for the life of the process.
	gatewayIdleTimeout = 60 * time.Second
)

// newHTTPServer builds the public listener. WriteTimeout is deliberately left
// unset: responses stream for as long as the model thinks, and a write deadline
// would cut a long answer off mid-token. The upstream-side idle guard
// (upstreamIdleTimeout) is what catches a stream that genuinely stops.
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: gatewayReadHeaderTimeout,
		ReadTimeout:       gatewayReadTimeout,
		IdleTimeout:       gatewayIdleTimeout,
	}
}

// Config controls how the gateway runtime binds and behaves.
type Config struct {
	// Addr overrides the listen address entirely (host:port). When empty,
	// WebExposed decides between 127.0.0.1 and 0.0.0.0 on Port.
	Addr string
	// Port is used when Addr is empty. Defaults to 18093.
	Port int
	// WebExposed when true binds 0.0.0.0 (LAN/tunnel reachable). When false,
	// binds 127.0.0.1 only. Ignored when Addr is set via GATEWAY_ADDR / Addr.
	// Used only when WebExposedSet is true; otherwise the persisted SQLite
	// value (or false for a fresh install) is used.
	WebExposed bool
	// WebExposedSet marks WebExposed as an explicit override for this start.
	WebExposedSet bool
	// PreferEnvAddr when true (headless CLI) lets GATEWAY_ADDR override bind host.
	PreferEnvAddr bool
	// Version is the build version this runtime serves; the console polls it to
	// learn that a newer release is live. Empty for a development build.
	Version string
}

// Runtime owns the gateway HTTP server lifecycle so both the headless CLI and
// the macOS desktop app can share the same start/stop/rebind logic.
type Runtime struct {
	mu sync.Mutex

	db                *store.Store
	logs              *monitor.Store
	router            *gateway.Router
	server            *gateway.Server
	tunnelManager     *tunnel.Manager
	httpServer        *http.Server
	port              int
	webExposed        bool
	addrOverride      string // non-empty when PreferEnvAddr / Config.Addr forces host:port
	started           bool
	onListenChange    func(addr string)
	stopMaintenance   chan struct{}      // closed on Stop to end the storage maintenance loop
	maintenanceCancel context.CancelFunc // cancels the one-shot startup maintenance
}

func New() *Runtime {
	return &Runtime{}
}

func (rt *Runtime) Server() *gateway.Server {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.server
}

func (rt *Runtime) Router() *gateway.Router {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.router
}

func (rt *Runtime) Logs() *monitor.Store {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.logs
}

func (rt *Runtime) Port() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.port <= 0 {
		return DefaultPort
	}
	return rt.port
}

func (rt *Runtime) WebExposed() bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.webExposed
}

func (rt *Runtime) ListenAddr() string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.listenAddrLocked()
}

func (rt *Runtime) LocalURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", rt.Port())
}

func (rt *Runtime) SetOnListenChange(fn func(addr string)) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.onListenChange = fn
}

func (rt *Runtime) listenAddrLocked() string {
	if strings.TrimSpace(rt.addrOverride) != "" {
		return strings.TrimSpace(rt.addrOverride)
	}
	port := rt.port
	if port <= 0 {
		port = DefaultPort
	}
	host := "127.0.0.1"
	if rt.webExposed {
		host = "0.0.0.0"
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// Start opens the DB, builds the gateway, and begins listening.
func (rt *Runtime) Start(cfg Config) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.started {
		return fmt.Errorf("gateway runtime already started")
	}

	db, err := store.OpenDefault()
	if err != nil {
		return err
	}
	rt.db = db
	slog.Info("database store", "path", db.Path())

	jsonPath, err := config.DefaultConfigPath()
	if err != nil {
		_ = db.Close()
		return err
	}
	if migrated, err := db.MigrateFromJSON(jsonPath, config.DefaultState()); err != nil {
		_ = db.Close()
		return err
	} else if migrated {
		slog.Info("legacy config migrated to sqlite", "json", jsonPath, "db", db.Path())
	}

	// The monitoring center reads history the console used to own (check-ins,
	// Telegram settings, alert rows). Importing before the state load means the
	// first monitoring tick already sees the pre-existing incidents, and a
	// failure here must never block startup: the gateway still runs with the
	// legacy alert scanner.
	if err := db.MigrateMonitoringV1(); err != nil {
		slog.Warn("monitoring migration failed", "error", err)
	}

	state, err := db.Load(config.DefaultState())
	if err != nil {
		_ = db.Close()
		return err
	}
	slog.Info("config loaded", "path", db.Path(), "providers", len(state.Providers), "routes", len(state.Routes), "models", len(state.Models), "apiKeys", len(state.APIKeys))

	logs := monitor.NewStore()
	if state.LogLevel != "" {
		logs.SetLevel(state.LogLevel)
	}
	// Must be set before any request enqueues usage: the worker reads these
	// thresholds once when it starts.
	logs.SetUsageBatchConfig(state.UsageBatchMaxSize, time.Duration(state.UsageBatchMaxWaitSeconds)*time.Second)
	retentionDays := state.RequestLogRetentionDays
	if retentionDays <= 0 {
		retentionDays = 7
	}
	// Defer prune/restore off the listen critical path so public URL / health
	// come up as soon as the HTTP server is ready (previously ~0.5–1s of SQLite
	// log IO blocked Start before ListenAndServe).
	state.LogLevel = ""

	rt.port = cfg.Port
	if rt.port <= 0 {
		rt.port = DefaultPort
	}
	if cfg.WebExposedSet {
		rt.webExposed = cfg.WebExposed
	} else if db.HasSetting("webExposed") {
		rt.webExposed = state.WebExposed
	} else if cfg.PreferEnvAddr {
		// Headless first run: keep LAN/LaunchAgent behaviour.
		rt.webExposed = true
	} else {
		// Desktop first run: loopback-only until the user opts in.
		rt.webExposed = false
	}
	if cfg.PreferEnvAddr {
		if envAddr := strings.TrimSpace(os.Getenv("GATEWAY_ADDR")); envAddr != "" {
			rt.addrOverride = envAddr
		}
	}
	if strings.TrimSpace(cfg.Addr) != "" {
		rt.addrOverride = strings.TrimSpace(cfg.Addr)
	}
	if rt.addrOverride != "" {
		if _, portStr, err := net.SplitHostPort(rt.addrOverride); err == nil {
			if parsed, convErr := strconv.Atoi(portStr); convErr == nil && parsed > 0 {
				rt.port = parsed
			}
		}
		// Infer exposure from override host for state consistency.
		host, _, _ := net.SplitHostPort(rt.addrOverride)
		if host == "0.0.0.0" || host == "::" || host == "[::]" {
			rt.webExposed = true
		} else if host == "127.0.0.1" || host == "localhost" || host == "::1" {
			rt.webExposed = false
		}
	}

	state.WebExposed = rt.webExposed
	router := gateway.NewRouter(state)
	// The console compares its own build version against this one to offer the
	// upgrade prompt, so it must be set before the first request is served.
	gateway.SetBuildVersion(cfg.Version)
	server := gateway.NewServer(router, logs, db)
	server.RestoreSleepPrevention()
	// 网关身份要在第一条请求之前就位：环路检测拿它判断"链路里是不是我自己"。
	// 身份从机器标识派生，重装、清库都不变（用户明确要求"同一台电脑重新安装
	// app 要身份一样"）；读不到机器标识时退化成库里存的随机值。salt 用库文件
	// 路径的目录，这样同一台机器上跑两套网关是两个身份，它们互相转发不会被
	// 自己的环检测误判成环。
	if identity := gateway.ResolveGatewayIdentity(db, filepath.Dir(db.Path())); identity.ID != "" {
		server.SetGatewayIdentity(domain.GatewayIdentity{
			ID:                identity.ID,
			Name:              identity.Name,
			Source:            identity.Source,
			MachineIDReadable: identity.MachineIDReadable,
		})
		slog.Info("gateway identity", "id", identity.ID, "source", identity.Source, "machineIdReadable", identity.MachineIDReadable)
	} else {
		// 身份缺失时环路检测放行——这不该让网关起不来，但必须留痕，否则
		// 将来排查"为什么环没被拦住"会找不到原因。
		slog.Warn("gateway identity unavailable; gateway loop detection is disabled for this run")
	}
	// 采样器跟着进程活，而不是跟着第一个访问机器状态页的请求：否则重启之后
	// 要等到有人打开那个页面才恢复采样，中间那段曲线就是空的（SQLite 里也
	// 没有点可回填，因为根本没采）。
	server.StartHostMetrics()
	// 上次自检若被 kill/重启打断，selfcheck-* 密钥会残留；启动时扫一遍。
	server.SweepSelfcheckLeftovers()
	bridge := cursor.NewBridge(cursor.FindRepoRoot())
	server.SetCursorBridge(bridge)
	bridge.StartHealthWatch(context.Background())
	server.SetWebExposedChangeHandler(func(enabled bool) error {
		return rt.SetWebExposed(enabled)
	})

	addr := rt.listenAddrLocked()
	server.SetListenAddr(addr)

	lanHost := netutil.PrimaryLANIPv4()
	if lanHost == "" {
		lanHost = "127.0.0.1"
	}
	router.SetEndpointAdvertise(lanHost, rt.port)
	slog.Info("endpoint advertise", "lanHost", lanHost, "port", rt.port, "webExposed", rt.webExposed)

	tunnelManager := tunnel.NewManager(rt.port)
	server.SetTunnelManager(tunnelManager)

	// Fail-fast: bind the listen port synchronously BEFORE bringing up the
	// tunnel. If another gateway instance already owns the port (e.g. a second
	// supervisor spawned a duplicate), abort startup now — cmd/gateway exits
	// non-zero. Otherwise the loser would keep running its tunnel manager and
	// register a SECOND cloudflared for the same hostname, so Cloudflare splits
	// public traffic across two tunnels of differing quality and latency turns
	// erratic. Binding before RestorePublicAccess guarantees a duplicate never
	// starts a phantom tunnel.
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("cannot bind gateway listen address %s (another gateway instance already running?): %w", addr, err)
	}

	httpServer := newHTTPServer(addr, server.Handler())

	rt.logs = logs
	rt.router = router
	rt.server = server
	rt.tunnelManager = tunnelManager
	rt.httpServer = httpServer

	go func() {
		slog.Info("gateway listening", "addr", addr)
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("gateway failed", "error", err)
		}
	}()

	// Restore the public tunnel immediately so user.lucadesign.uk comes back
	// ASAP after restart. Log hydrate / usage rebuild run afterwards and must
	// not delay tunnel bring-up.
	go func() {
		server.RestorePublicAccess()
	}()
	// Startup maintenance runs on a cancellable context. It rewrites through the
	// single SQLite writer connection, and a deploy restarting the process must be
	// able to abandon it instead of queueing its own shutdown writes behind it.
	maintenanceCtx, cancelMaintenance := context.WithCancel(context.Background())
	rt.maintenanceCancel = cancelMaintenance
	go func() {
		if err := db.PruneRequestLogsContext(maintenanceCtx, retentionDays); err != nil {
			slog.Warn("request log prune failed", "error", err)
		}
		if err := db.PruneMonitoringDataContext(maintenanceCtx, time.Now()); err != nil {
			slog.Warn("monitoring data prune failed", "error", err)
		}
		// Reclaim disk space that pruning frees. A legacy (NONE/FULL) database
		// needs one whole-file VACUUM to switch modes, which MaintainStorage
		// defers unless the file is small or GATEWAY_ALLOW_VACUUM opts in.
		if err := db.MaintainStorageContext(maintenanceCtx); err != nil {
			slog.Warn("storage maintenance failed", "error", err)
		}
		// 每日用量汇总永久保留；仅清理请求明细，不裁剪 usage_daily。
		if persisted, err := db.ListRequestLogs(1000); err != nil {
			slog.Warn("request log restore failed", "error", err)
		} else if len(persisted) > 0 {
			logs.Bootstrap(persisted)
			slog.Info("request logs restored", "count", len(persisted))
		}
		server.SyncConnectedCursorProvidersWithEmptyModels()
		server.RebuildUsageStats()
		server.StartOAuthUsageBackgroundRefresh(context.Background())
		server.StartChatGPTTokenBackgroundRefresh(context.Background())
		server.StartClaudeTokenBackgroundRefresh(context.Background())
		server.StartProviderFailoverRecovery(context.Background())
		server.StartUserActivityFlush(context.Background())
		server.StartQuotaKeeper(context.Background())
		server.StartDailyCheckin(context.Background())
		server.StartWorkBuddyAutomation(context.Background())
		server.BackfillClaudeOAuthAccountLabels()
		server.BackfillQoderAccountLabels()
		server.StartCursorModelBackgroundRefresh(context.Background())
		server.StartPeerUpstreamBackgroundRefresh(context.Background())
		// The monitoring center replaces the legacy alert scanner: multi-IP and
		// concurrent-IP rules are evaluated by its collector, which also folds
		// the frozen `alerts` history into the event stream on startup.
		server.StartMonitoring(context.Background())
		rt.startStorageMaintenance(maintenanceCtx, db, retentionDays)
	}()

	rt.started = true
	return nil
}

// storageMaintenanceInterval 控制磁盘回收周期。日志保留窗口为天级，回收无需
// 频繁执行；6 小时一次足以在坏 Provider 故障风暴后及时把空闲空间还给 OS，
// 同时避免无谓 IO。
const storageMaintenanceInterval = 6 * time.Hour

// startStorageMaintenance 周期性回收 request_logs 删行后遗留的空闲页（分批
// incremental_vacuum）。首次维护已在启动 goroutine 内同步跑过，这里只负责稳态
// 周期回收。随 rt.stopMaintenance（Stop 中关闭）和 maintenanceCtx（Stop 中取消）
// 退出，避免泄漏 goroutine，也避免关停时排在一个长维护语句后面出不来。
func (rt *Runtime) startStorageMaintenance(ctx context.Context, db *store.Store, retentionDays int) {
	rt.mu.Lock()
	if rt.stopMaintenance != nil {
		// 已在运行（理论上不会重复启动），先关旧的。
		close(rt.stopMaintenance)
	}
	stop := make(chan struct{})
	rt.stopMaintenance = stop
	rt.mu.Unlock()

	go func() {
		ticker := time.NewTicker(storageMaintenanceInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := db.PruneRequestLogsContext(ctx, retentionDays); err != nil {
					slog.Warn("periodic request log prune failed", "error", err)
				}
				if err := db.PruneMonitoringDataContext(ctx, time.Now()); err != nil {
					slog.Warn("periodic monitoring prune failed", "error", err)
				}
				if err := db.MaintainStorageContext(ctx); err != nil {
					slog.Warn("periodic storage maintenance failed", "error", err)
				}
			}
		}
	}()
}

// SetWebExposed rebinds the HTTP listener between loopback-only and all interfaces.
func (rt *Runtime) SetWebExposed(enabled bool) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !rt.started || rt.server == nil || rt.router == nil {
		return fmt.Errorf("gateway runtime is not started")
	}
	if rt.addrOverride != "" && strings.TrimSpace(os.Getenv("GATEWAY_ADDR")) != "" {
		// Explicit GATEWAY_ADDR wins; still persist the preference for next start without override.
		rt.webExposed = enabled
		rt.router.SetWebExposed(enabled)
		_ = rt.server.SaveState()
		return fmt.Errorf("GATEWAY_ADDR is set; restart without it for webExposed rebind to take effect")
	}

	if rt.webExposed == enabled && rt.httpServer != nil {
		rt.router.SetWebExposed(enabled)
		_ = rt.server.SaveState()
		return nil
	}

	// Closing Web while a public tunnel is running would black-hole remote traffic.
	if !enabled && rt.tunnelManager != nil {
		snap := rt.tunnelManager.Snapshot()
		if snap.Status == tunnel.StatusRunning || snap.Status == tunnel.StatusStarting {
			_ = rt.tunnelManager.Stop()
			slog.Info("public tunnel stopped because web exposure was disabled")
		}
	}

	old := rt.httpServer
	rt.webExposed = enabled
	rt.addrOverride = ""
	rt.router.SetWebExposed(enabled)
	addr := rt.listenAddrLocked()
	rt.server.SetListenAddr(addr)

	httpServer := newHTTPServer(addr, rt.server.Handler())
	rt.httpServer = httpServer

	if old != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = old.Shutdown(shutdownCtx)
		cancel()
	}

	go func() {
		slog.Info("gateway listening", "addr", httpServer.Addr, "webExposed", enabled)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("gateway failed after rebind", "error", err)
		}
	}()

	if err := rt.server.SaveState(); err != nil {
		return err
	}
	if rt.onListenChange != nil {
		rt.onListenChange(addr)
	}
	return nil
}

// Per-step shutdown caps.
//
// The whole sequence has to finish far inside the ~90s systemd grants before it
// escalates SIGTERM to SIGKILL. A kill is not just uglier than a slow exit: it
// discards every queued write (not just the last batch), leaves a long statement
// mid-flight so its work rolls back and the next start repeats it from scratch,
// and stalls the deploy with 90s of silence that reads as a hang.
const (
	shutdownDrainBudget   = 5 * time.Second
	shutdownFlushBudget   = 10 * time.Second
	shutdownDBCloseBudget = 3 * time.Second
)

// DefaultShutdownBudget is the deadline a headless caller should give Stop:
// every step cap plus slack for the runtime lock to change hands.
const DefaultShutdownBudget = shutdownDrainBudget + shutdownFlushBudget + shutdownDBCloseBudget + 2*time.Second

// ShutdownHardLimit is the backstop a supervisor process should arm: Stop bounds
// each step it takes, but nothing inside it can interrupt a wait on the runtime
// lock itself. Exiting by ourselves at this point is strictly better than being
// SIGKILLed at systemd's TimeoutStopSec, which lands no data and looks like a
// hung deploy.
const ShutdownHardLimit = DefaultShutdownBudget + 5*time.Second

// Stop shuts down the tunnel and HTTP server and closes the DB.
//
// Each step runs under its own slice of ctx and gives up when that expires, so
// Stop always returns. Abandoning a step costs the rows it had not landed;
// waiting on it costs the restart.
func (rt *Runtime) Stop(ctx context.Context) error {
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), DefaultShutdownBudget)
		defer cancel()
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !rt.started {
		return nil
	}
	// Cancel the maintenance pass first: it holds the single SQLite writer
	// connection that every step below needs, and it is the reason a restart
	// during startup used to block until the kill.
	if rt.maintenanceCancel != nil {
		rt.maintenanceCancel()
	}
	if rt.stopMaintenance != nil {
		close(rt.stopMaintenance)
		rt.stopMaintenance = nil
	}
	if rt.tunnelManager != nil {
		rt.tunnelManager.Stop()
	}
	if rt.server != nil {
		rt.server.StopCursorBridge()
		rt.server.CloseSleepPrevention()
	}
	// Drain the HTTP server BEFORE flushing: Shutdown waits (up to the ctx
	// deadline) for in-flight handlers, and a handler that finishes during that
	// drain persists its row and usage delta on the way out. Flushing first
	// would close those sinks under it and drop exactly the requests that were
	// still being served.
	var shutdownErr error
	if rt.httpServer != nil {
		drainCtx, cancelDrain := context.WithTimeout(ctx, shutdownDrainBudget)
		shutdownErr = rt.httpServer.Shutdown(drainCtx)
		cancelDrain()
		if shutdownErr != nil {
			slog.Warn("http drain gave up with requests still in flight", "error", shutdownErr)
		}
	}
	if rt.server != nil {
		// Deferred/batched writers hold data in memory to save disk wear; land
		// it all once nothing is serving, and before the DB closes so a restart
		// loses nothing.
		flushCtx, cancelFlush := context.WithTimeout(ctx, shutdownFlushBudget)
		defer cancelFlush()
		if err := rt.server.FlushDeferredWrites(flushCtx); err != nil && shutdownErr == nil {
			shutdownErr = err
		}
	}
	if rt.db != nil {
		closeCtx, cancelClose := context.WithTimeout(ctx, shutdownDBCloseBudget)
		defer cancelClose()
		if err := closeStore(closeCtx, rt.db); err != nil && shutdownErr == nil {
			shutdownErr = err
		}
	}
	rt.started = false
	rt.httpServer = nil
	return shutdownErr
}

// closeStore closes the DB without waiting indefinitely: Close blocks until every
// in-flight statement returns, and the statement we already decided not to wait
// for is the one that would keep blocking it. SQLite's WAL is crash-safe, so
// leaving the handle to the process exit loses nothing beyond what the deadline
// already cost.
func closeStore(ctx context.Context, db *store.Store) error {
	done := make(chan error, 1)
	go func() { done <- db.Close() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		slog.Warn("database close gave up; letting the process exit release it", "error", ctx.Err())
		return ctx.Err()
	}
}

// WaitHealthy polls /__health until ok or timeout.
func (rt *Runtime) WaitHealthy(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := rt.LocalURL() + "/__health"
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("gateway health check timed out at %s", url)
}

// PortInUse reports whether the default (or configured) TCP port is already listening.
func PortInUse(port int) bool {
	if port <= 0 {
		port = DefaultPort
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return true
	}
	_ = ln.Close()
	return false
}
