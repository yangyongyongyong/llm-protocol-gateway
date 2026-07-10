package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/config"
	"github.com/luca/llm-protocol-gateway/internal/cursor"
	"github.com/luca/llm-protocol-gateway/internal/gateway"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
	"github.com/luca/llm-protocol-gateway/internal/netutil"
	"github.com/luca/llm-protocol-gateway/internal/store"
	"github.com/luca/llm-protocol-gateway/internal/tunnel"
)

const DefaultPort = 18093

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
}

// Runtime owns the gateway HTTP server lifecycle so both the headless CLI and
// the macOS desktop app can share the same start/stop/rebind logic.
type Runtime struct {
	mu sync.Mutex

	db             *store.Store
	logs           *monitor.Store
	router         *gateway.Router
	server         *gateway.Server
	tunnelManager  *tunnel.Manager
	httpServer     *http.Server
	port           int
	webExposed     bool
	addrOverride   string // non-empty when PreferEnvAddr / Config.Addr forces host:port
	started        bool
	onListenChange func(addr string)
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
	retentionDays := state.RequestLogRetentionDays
	if retentionDays <= 0 {
		retentionDays = 7
	}
	if err := db.PruneRequestLogs(retentionDays); err != nil {
		slog.Warn("request log prune failed", "error", err)
	}
	logs.PruneUsageStatsBefore(time.Now().AddDate(0, 0, -retentionDays))
	if persisted, err := db.ListRequestLogs(1000); err != nil {
		slog.Warn("request log restore failed", "error", err)
	} else if len(persisted) > 0 {
		logs.Bootstrap(persisted)
		slog.Info("request logs restored", "count", len(persisted))
	}
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
	server := gateway.NewServer(router, logs, db)
	server.SetCursorBridge(cursor.NewBridge(cursor.FindRepoRoot()))
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

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	rt.logs = logs
	rt.router = router
	rt.server = server
	rt.tunnelManager = tunnelManager
	rt.httpServer = httpServer

	go func() {
		slog.Info("gateway listening", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("gateway failed", "error", err)
		}
	}()

	go func() {
		time.Sleep(300 * time.Millisecond)
		server.RestorePublicAccess()
		server.SyncConnectedCursorProvidersWithEmptyModels()
		server.RebuildUsageStats()
		server.StartOAuthUsageBackgroundRefresh(context.Background())
		server.StartCursorModelBackgroundRefresh(context.Background())
	}()

	rt.started = true
	return nil
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

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           rt.server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
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

// Stop shuts down the tunnel and HTTP server and closes the DB.
func (rt *Runtime) Stop(ctx context.Context) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !rt.started {
		return nil
	}
	if rt.tunnelManager != nil {
		rt.tunnelManager.Stop()
	}
	var shutdownErr error
	if rt.httpServer != nil {
		if ctx == nil {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
		}
		shutdownErr = rt.httpServer.Shutdown(ctx)
	}
	if rt.db != nil {
		_ = rt.db.Close()
	}
	rt.started = false
	rt.httpServer = nil
	return shutdownErr
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
