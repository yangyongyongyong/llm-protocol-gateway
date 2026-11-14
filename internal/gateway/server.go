package gateway

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/luca/llm-protocol-gateway/internal/autostart"
	"github.com/luca/llm-protocol-gateway/internal/cursor"
	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
	"github.com/luca/llm-protocol-gateway/internal/monitoring"
	"github.com/luca/llm-protocol-gateway/internal/tunnel"
)

const (
	headerGatewayRouteID      = "X-Gateway-Route-Id"
	headerGatewayInternalTest = "X-Gateway-Internal-Test"
)

const routeTestTokenTTL = 2 * time.Minute

// routeTestSecret is generated once per gateway process. The route-test
// shortcut needs to survive a real HTTP hop (the test calls the loopback data
// plane), so a context value cannot carry the proof: context values are not
// serialized over the network. A short-lived HMAC token does, while remaining
// impossible for a client to forge. The token is scoped to the route id and
// expires even if it is copied out of a local request.
var routeTestSecret = func() []byte {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err == nil {
		return secret
	}
	// rand.Read failing is effectively impossible, but a deterministic process
	// secret is still safer than accepting an unsigned "1".
	sum := sha256.Sum256([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
	return sum[:]
}()

func routeTestToken(routeID string, now time.Time) string {
	expires := now.Add(routeTestTokenTTL).Unix()
	mac := hmac.New(sha256.New, routeTestSecret)
	_, _ = fmt.Fprintf(mac, "%s\n%d", routeID, expires)
	return fmt.Sprintf("%d.%s", expires, hex.EncodeToString(mac.Sum(nil)))
}

func validRouteTestToken(routeID, token string, now time.Time) bool {
	expiresText, signature, ok := strings.Cut(strings.TrimSpace(token), ".")
	if !ok || expiresText == "" || signature == "" {
		return false
	}
	expires, err := strconv.ParseInt(expiresText, 10, 64)
	if err != nil || expires < now.Unix() || expires > now.Add(routeTestTokenTTL+time.Minute).Unix() {
		return false
	}
	mac := hmac.New(sha256.New, routeTestSecret)
	_, _ = fmt.Fprintf(mac, "%s\n%d", routeID, expires)
	want, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	return hmac.Equal(want, mac.Sum(nil))
}

// internalTestRouteID returns the route a request names as its own test target,
// and whether the request carries a valid short-lived gateway signature.
//
// Headers alone are client-chosen, so the route id is not enough: without the
// signed token any caller could spend a provider's credential with no key
// attribution. The token travels in the header because the test performs a real
// loopback HTTP request; it never travels in curl diagnostics.
func internalTestRouteID(r *http.Request) (string, bool) {
	routeID := strings.TrimSpace(r.Header.Get(headerGatewayRouteID))
	if routeID == "" || !internalTestRequest(r, routeID) {
		return "", false
	}
	return routeID, true
}

func internalTestRequest(r *http.Request, routeID string) bool {
	return validRouteTestToken(routeID, r.Header.Get(headerGatewayInternalTest), time.Now())
}

type StateSaver interface {
	Save(domain.GatewayState) error
}

type APIKeyStore interface {
	CreateAPIKey(domain.APIKey) error
	UpdateAPIKey(domain.APIKey) error
	DeleteAPIKey(id string) error
	TouchAPIKey(id string, lastUsedAt string) error
}

// APIKeyReader 读库里现存的密钥行。网关互联换发对端凭据时要先看清这个 owner
// 名下还有没有别的行（旧凭据、中断留下的残行），好一并清掉——一个对端只能有
// 一把有效凭据。
type APIKeyReader interface {
	APIKeys() ([]domain.APIKey, error)
}

// ProviderOAuthSaver persists just the OAuth token columns of one provider,
// avoiding a full-state rewrite on every token refresh. authJSON carries the
// extra credential fields a newer subscription needs (a ZCode minted key with
// its team project, a Trae device identity); every subscription writes the
// whole credential, so a refresh never blanks what the last write stored.
type ProviderOAuthSaver interface {
	UpdateProviderOAuth(providerID, accessToken, refreshToken, expiresAt, scope, accountLabel, userID, authDomain, authJSON string) error
}

type RequestLogStore interface {
	AppendRequestLog(monitor.RequestLog) error
	AppendRequestLogWithRetention(monitor.RequestLog, int) error
	// AppendRequestLogs writes many rows in one transaction (batched successes).
	AppendRequestLogs([]monitor.RequestLog) error
	ListRequestLogs(int) ([]monitor.RequestLog, error)
	QueryRequestLogs(monitor.RequestLogQuery) (monitor.RequestLogPage, error)
	RequestLogByID(int64) (monitor.RequestLog, bool, error)
	PruneRequestLogs(int) error
}

type UsageDailyStore interface {
	monitor.UsageDailyStore
}

type RTKStatsStore interface {
	monitor.RTKStatsStore
}

type Server struct {
	router             *Router
	logs               *monitor.Store
	stateSaver         StateSaver
	apiKeyStore        APIKeyStore
	apiKeyToucher      *apiKeyToucher
	providerOAuthSaver ProviderOAuthSaver
	requestLogStore    RequestLogStore
	requestLogBatcher  *requestLogBatcher
	usageDailyStore    UsageDailyStore
	rtkStatsStore      RTKStatsStore
	rtk                *rtkCompressor
	scheduledTaskStore ScheduledTaskStore
	checkinStore       CheckinStore
	// probeNow is the clock the probe scheduler reads (startMonitorProber). Nil
	// means time.Now; tests inject a fake stamp per tick.
	probeNow func() time.Time
	// checkinMu guards checkinRunning, the set of providers whose check-in is in
	// flight: the loop and a press of the same account must not both spend its
	// one-shot refresh token.
	checkinMu      sync.Mutex
	checkinRunning map[string]bool
	// workBuddyRunMu guards the task-center runs: workBuddyRuns holds every run
	// the console started (its events and per-task results), and
	// workBuddyProviderRuns points one provider at its latest run, which is how
	// a second click is turned away while the first is still going.
	workBuddyRunMu          sync.Mutex
	workBuddyRuns           map[string]*workBuddyRun
	workBuddyProviderRuns   map[string]string
	tunnels                 *tunnel.Manager
	pendingClaudeOAuth      *claudeOAuthPendingStore
	pendingCursorOAuth      *cursorOAuthPendingStore
	pendingChatGPTOAuth     *chatgptOAuthPendingStore
	subscriptionOAuthFlows  *subscriptionOAuthStore
	qoderSignIns            *qoderSignInStore
	cursorBridge            *cursor.Bridge
	listenAddr              string
	requestLogRetentionDays int
	// monitoring is nil until StartMonitoring runs; every caller checks it, so a
	// server built without a monitoring store keeps working unchanged.
	monitoringStore  monitoringStore
	monitoring       *monitoring.Service
	monitoringEngine *monitoring.Engine
	monitoringSrc    *monitoringSource
	// monitorHub fans monitoring increments out to /__monitor/stream
	// subscribers. Built lazily by monitorStreamHub (sync.Once, so the route
	// and StartMonitoring may race to first use it).
	monitorHub     *monitorStreamHub
	monitorHubOnce sync.Once
	// monitorTrafficAt throttles live request frames; guarded by monitorTrafficMu
	// because every finished request writes it.
	monitorTrafficMu sync.Mutex
	monitorTrafficAt time.Time
	// monitorIsolationMu serializes the forced circuit-break endpoints' read-
	// check-write of the isolation marker and the Disabled flag together, so two
	// clicks cannot leave a disabled account with no marker (or vice versa).
	monitorIsolationMu sync.Mutex
	// log2xxBodies mirrors GatewayState.Log2xxBodies; guarded by log2xxBodiesMu
	// because it is read on every request-log write and toggled from a handler.
	log2xxBodies   bool
	log2xxBodiesMu sync.RWMutex
	adminAuth      AdminAuthStore
	userStore      UserStore
	roles          RoleStore
	// peerUpstreamSyncMu serializes downstream-side parent inventory refreshes:
	// the manual "测试连接" button and the periodic background poll must not
	// reconcile the same peer member group at the same time.
	peerUpstreamSyncMu sync.Mutex
	// peerCredentialRenewMu coalesces concurrent 401 recoveries for one peer
	// group: the first successful renew updates every member row, later callers
	// re-read the new token instead of each rotating the credential again.
	peerCredentialRenewMu sync.Mutex
	// authThrottle counts failed console logins per source IP and account.
	authThrottle      *authThrottle
	oauthUsageCache   *oauthUsageCache
	requestStatsCache *requestStatsCache
	// oauthUsageFetchMu serializes upstream usage fetches per provider so
	// concurrent UI polls share one in-flight request after cache miss.
	oauthUsageFetchMu sync.Map
	// chatgptDigestSessions keeps prompt-cache key continuity for Claude→
	// Responses conversations whose client sends no session signal at all.
	chatgptDigestSessions *chatgptDigestSessionStore
	// workBuddyAfterTools remembers, per provider and model, a chat that only
	// went through once an empty assistant reply stood between the tool
	// results and the user's message (WorkBuddy 11153); that model's next
	// chats carry the reply up front. Runtime-only, as magpie's plugin keeps
	// it for the run.
	workBuddyAfterTools sync.Map
	// externalURLOpener opens a vetted release page in the user's browser. The
	// Wails WebView blocks window.open, so desktop actions go back through the
	// native process instead.
	externalURLOpener func(string) error
	// desktopAutostart is the macOS login-time record behind the Settings row.
	// It is an interface so handlers can be tested without touching the real
	// ~/Library/LaunchAgents. The system record remains the source of truth.
	desktopAutostart autostart.Manager
	// desktopUpdater is the optional App-owned self-update service. It is nil
	// in headless builds; handlers then return an unavailable status.
	desktopUpdater desktopUpdater
	// webExposedChange is invoked when the UI toggles LAN/Web exposure so the
	// process owner (CLI runtime / desktop app) can rebind the HTTP listener.
	webExposedChange func(enabled bool) error

	// providerAvailability tracks providers that a live upstream request found
	// unavailable (hard quota/auth, or repeated soft 429/5xx/transport failures),
	// keyed by provider ID. Runtime-only, never persisted; cleared as soon as a
	// live request or probe succeeds again. Soft failures require consecutive
	// hits before appearing here (see classifyProviderFailover).
	providerAvailabilityMu sync.Mutex
	providerAvailability   map[string]providerUnavailableState
	providerSoftFailures   map[string]providerSoftFailureState

	// Session affinity (see provider_affinity.go): which provider each
	// conversation is sticky to. Runtime-only like the availability tracker —
	// a restart drops the pins and each key's sticky provider takes over.
	sessionPins *sessionPinStore
	// servingProviders：每个 Key 最近一次真正返回响应的 Provider（控制台「在服务」）。
	// 与 ActiveProviderID 分开：后者是粘滞位，会话粘性命中时不会被改写。
	servingProviders *servingProviderTracker

	// Scheduling (see provider_schedule.go / provider_load.go): the round-robin
	// cursors and the recent per-provider workload. Both runtime-only by
	// design; neither may put a write on the request path.
	rotate        *rotateCounters
	providerLoads *providerLoadTracker

	// Admission lanes (see provider_lanes.go): per-provider in-flight budgets.
	providerLanes *providerLanes

	// Per-key quota (see key_quota.go): the prompt tokens already committed by
	// requests in flight. Runtime-only, like the scheduler's cursors — the window's
	// completed usage lives in the monitor store's day buckets.
	keyQuota *quotaReservations

	// Cost ledger (see pricing.go): the price table cache and the store behind it.
	priceStore PriceStore
	prices     *priceTable

	// userActivity tracks each console user's latest API request time.
	// In-memory values are exact; StartUserActivityFlush persists them at most
	// once per userActivityPersistMinGap per user (see user_activity.go).
	userActivityMu sync.Mutex
	userActivity   map[string]*userActivityEntry

	// hostMetrics is the process-wide host sampler: one ring of recent samples
	// in memory, flushed to SQLite in batches and streamed to the machine page
	// over SSE. Created once under hostMetricsOnce: two concurrent first polls
	// used to race and leave one sampler's goroutine running unobserved.
	hostMetrics     *hostMetricsSampler
	hostMetricsOnce sync.Once
	hostMetricStore hostMetricsStore
	hostStreams     *hostMetricsStreamHub
	hostStreamsOnce sync.Once
	hostPumpOnce    sync.Once

	// requestCounter increments once per completed model call (see the log
	// middleware); the machine page's diagnosis turns it into a request rate.
	requestCounter   atomic.Int64
	activityBaseline atomic.Int64

	// Quota keeper ("额度保活", see quota_keeper.go). The three fields below are
	// runtime-only: in-flight claims stop a task or provider from being probed
	// twice, and quotaLastProbeAt remembers probes this loop sent (they never
	// reach request_logs, so the quiet window cannot read them from the table).
	quotaKeeperMu      sync.Mutex
	quotaTaskInFlight  map[string]bool
	quotaProbeInFlight map[string]bool
	quotaLastProbeAt   map[string]time.Time
	quotaLastPruneAt   time.Time

	// gatewayID 是本网关的稳定身份（见 internal/gatewayid）：父子透传用它判断
	// "这次请求是不是已经过我自己"。启动时由 SetGatewayID 注入；空串表示尚未
	// 初始化，此时环路检测放行而不是误拒所有透传流量。
	gatewayID string
	// gatewayIdentity 是同一身份的完整描述（来源、是否派生于机器标识），
	// 只用于控制台展示；gatewayID 才是环路检测读的那个值。
	gatewayIdentity *domain.GatewayIdentity
}

func NewServer(router *Router, logs *monitor.Store, stateSaver ...StateSaver) *Server {
	server := &Server{
		router:                  router,
		logs:                    logs,
		pendingClaudeOAuth:      newClaudeOAuthPendingStore(),
		pendingCursorOAuth:      newCursorOAuthPendingStore(),
		pendingChatGPTOAuth:     newChatGPTOAuthPendingStore(),
		subscriptionOAuthFlows:  newSubscriptionOAuthStore(),
		qoderSignIns:            newQoderSignInStore(),
		oauthUsageCache:         newOAuthUsageCache(),
		requestStatsCache:       newRequestStatsCache(),
		sessionPins:             newSessionPinStore(),
		servingProviders:        newServingProviderTracker(),
		rotate:                  &rotateCounters{indices: map[string]uint64{}},
		providerLoads:           newProviderLoadTracker(),
		providerLanes:           newProviderLanes(),
		keyQuota:                newQuotaReservations(),
		prices:                  &priceTable{rows: map[string]domain.ModelPrice{}},
		rtk:                     newRTKCompressor(),
		requestLogRetentionDays: 7,
		authThrottle:            newAuthThrottle(),
		desktopAutostart:        autostart.New(),
	}
	if days := router.State().RequestLogRetentionDays; days > 0 {
		server.requestLogRetentionDays = days
	}
	server.log2xxBodies = router.State().Log2xxBodies
	if len(stateSaver) > 0 {
		server.stateSaver = stateSaver[0]
		if store, ok := stateSaver[0].(APIKeyStore); ok {
			server.apiKeyStore = store
			// Coalesce last_used_at writes off the request hot path.
			server.apiKeyToucher = newAPIKeyToucher(store, router, 2*time.Second)
		}
		if ps, ok := stateSaver[0].(PriceStore); ok {
			server.priceStore = ps
		}
		if pos, ok := stateSaver[0].(ProviderOAuthSaver); ok {
			server.providerOAuthSaver = pos
		}
		if rls, ok := stateSaver[0].(RequestLogStore); ok {
			server.requestLogStore = rls
			// Successful requests batch into one commit per minute; failures are
			// written through immediately (see requestLogBatcher).
			server.requestLogBatcher = newRequestLogBatcher(rls, server.RequestLogRetentionDays, func(message, context string) {
				logs.AddApp("warn", message, context)
			}, requestLogBatchInterval, requestLogBatchMax)
		}
		if uds, ok := stateSaver[0].(UsageDailyStore); ok {
			server.usageDailyStore = uds
			logs.SetUsageDailyStore(uds)
		}
		if rss, ok := stateSaver[0].(RTKStatsStore); ok {
			server.rtkStatsStore = rss
		}
		if auth, ok := stateSaver[0].(AdminAuthStore); ok {
			server.adminAuth = auth
		}
		if users, ok := stateSaver[0].(UserStore); ok {
			server.userStore = users
		}
		if roles, ok := stateSaver[0].(RoleStore); ok {
			server.roles = roles
		}
		if tasks, ok := stateSaver[0].(ScheduledTaskStore); ok {
			server.scheduledTaskStore = tasks
		}
		if records, ok := stateSaver[0].(CheckinStore); ok {
			server.checkinStore = records
		}
		if ms, ok := stateSaver[0].(monitoringStore); ok {
			server.monitoringStore = ms
			// Decorate from the start, not only once StartMonitoring runs: the
			// stream and every writer share this one field, and a write that
			// slips past the hub would leave a connected console stale.
			server.EnableMonitorStream()
		}
		if pins, ok := stateSaver[0].(sessionPinSaver); ok {
			server.sessionPins.setSaver(pins)
		}
		if hs, ok := stateSaver[0].(hostMetricsStore); ok {
			server.hostMetricStore = hs
		}
	}
	return server
}

func (s *Server) RequestLogRetentionDays() int {
	if s.requestLogRetentionDays <= 0 {
		return 7
	}
	return s.requestLogRetentionDays
}

func (s *Server) SetRequestLogRetentionDays(days int) int {
	if days <= 0 {
		days = 7
	}
	if days > 365 {
		days = 365
	}
	s.requestLogRetentionDays = days
	return days
}

// Log2xxBodies reports whether successful requests persist their bodies.
func (s *Server) Log2xxBodies() bool {
	s.log2xxBodiesMu.RLock()
	defer s.log2xxBodiesMu.RUnlock()
	return s.log2xxBodies
}

// SetLog2xxBodies toggles body logging for successful requests.
func (s *Server) SetLog2xxBodies(enabled bool) {
	s.log2xxBodiesMu.Lock()
	s.log2xxBodies = enabled
	s.log2xxBodiesMu.Unlock()
}

// SetTunnelManager attaches the cloudflared lifecycle manager used by the
// /__public endpoints. It is optional so tests can construct a Server without one.
func (s *Server) SetTunnelManager(manager *tunnel.Manager) {
	s.tunnels = manager
}

// SetListenAddr records the gateway's HTTP listen address (host:port) so OAuth
// callbacks can target the Claude Code-compatible /callback route.
func (s *Server) SetListenAddr(addr string) {
	s.listenAddr = strings.TrimSpace(addr)
}

// SetWebExposedChangeHandler registers a callback used by PATCH /__settings/web-exposed
// to rebind the process listen address (127.0.0.1 vs 0.0.0.0).
func (s *Server) SetWebExposedChangeHandler(fn func(enabled bool) error) {
	s.webExposedChange = fn
}

// SetExternalURLOpener lets the desktop shell open a validated release page in
// the system browser. Headless gateways leave it nil.
func (s *Server) SetExternalURLOpener(fn func(string) error) {
	s.externalURLOpener = fn
}

// SaveState persists the current router state (including webExposed).
func (s *Server) SaveState() error {
	return s.saveState()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__health", s.handleHealth)
	// The running build's version: every console tab polls it to learn that a
	// release is live (see app_version.go).
	mux.HandleFunc("GET /__app/version", s.handleAppVersion)
	mux.HandleFunc("GET /__host-metrics", s.handleHostMetrics)
	mux.HandleFunc("GET /__host-metrics/stream", s.handleHostMetricsStream)
	// On-demand A/B snapshot + findings, and the downloadable diagnostics bundle
	// (see host_metrics_snapshot.go). Separate from the sampler above so a
	// manual diagnose never disturbs the chart's baseline.
	mux.HandleFunc("GET /__host-metrics/snapshot", s.handleHostMetricsSnapshot)
	mux.HandleFunc("GET /__host-diagnostics", s.handleHostDiagnostics)
	mux.HandleFunc("GET /__auth/status", s.handleAuthStatus)
	mux.HandleFunc("POST /__auth/setup", s.handleAuthSetup)
	mux.HandleFunc("POST /__auth/login", s.handleAuthLogin)
	mux.HandleFunc("POST /__auth/logout", s.handleAuthLogout)
	mux.HandleFunc("POST /__auth/password", s.handleAuthPassword)
	mux.HandleFunc("GET /__state", s.handleState)
	mux.HandleFunc("GET /__settings/paths", s.handleSettingsPaths)
	mux.HandleFunc("GET /__desktop/autostart", s.handleDesktopAutostart)
	mux.HandleFunc("PATCH /__desktop/autostart", s.handleSetDesktopAutostart)
	mux.HandleFunc("POST /__desktop/autostart/open-login-items", s.handleOpenDesktopLoginItems)
	mux.HandleFunc("GET /__desktop/update", s.handleDesktopUpdate)
	mux.HandleFunc("POST /__desktop/update/check", s.handleDesktopUpdateCheck)
	mux.HandleFunc("POST /__desktop/update/download", s.handleDesktopUpdateDownload)
	mux.HandleFunc("POST /__desktop/update/install", s.handleDesktopUpdateInstall)
	mux.HandleFunc("POST /__desktop/update/open-release", s.handleDesktopUpdateOpenRelease)
	mux.HandleFunc("PATCH /__desktop/update/settings", s.handleDesktopUpdateSettings)
	mux.HandleFunc("GET /__logs", s.handleLogs)
	mux.HandleFunc("GET /__request-stats", s.handleRequestStats)
	mux.HandleFunc("GET /__usage-analysis", s.handleUsageAnalysis)
	mux.HandleFunc("GET /__rtk/stats", s.handleRTKStats)
	mux.HandleFunc("GET /__app/logs", s.handleAppLogs)
	// UI 诊断日志：浏览器端关键事件上报（见 ui_log.go 的背景说明）。
	mux.HandleFunc("POST /__ui-log", s.handleUILog)
	mux.HandleFunc("PATCH /__app/log-level", s.handleSetLogLevel)
	mux.HandleFunc("PATCH /__settings/request-log-retention", s.handleSetRequestLogRetention)
	mux.HandleFunc("PATCH /__settings/log-2xx-bodies", s.handleSetLog2xxBodies)
	mux.HandleFunc("PATCH /__settings/usage-batch", s.handleSetUsageBatch)
	mux.HandleFunc("PATCH /__settings/host-metrics", s.handleSetHostMetrics)
	// Monitoring center: the unified event stream. The legacy /__alerts* routes
	// and their scanner are gone; multi_ip/concurrent_ip detection now runs in
	// the monitoring collector, and MigrateMonitoringV1 still folds the frozen
	// `alerts` history into monitor_events on startup.
	mux.HandleFunc("GET /__monitor/overview", s.handleMonitorOverview)
	mux.HandleFunc("GET /__monitor/stream", s.handleMonitorStream)
	mux.HandleFunc("GET /__monitor/accounts", s.handleMonitorAccounts)
	mux.HandleFunc("GET /__monitor/accounts/{providerId}", s.handleMonitorAccount)
	mux.HandleFunc("POST /__monitor/accounts/{providerId}/refresh", s.handleMonitorAccountRefresh)
	mux.HandleFunc("POST /__monitor/accounts/{providerId}/quota/refresh", s.handleMonitorQuotaRefresh)
	mux.HandleFunc("POST /__monitor/accounts/{providerId}/isolate", s.handleMonitorIsolateAccount)
	mux.HandleFunc("POST /__monitor/accounts/{providerId}/restore", s.handleMonitorRestoreAccount)
	mux.HandleFunc("GET /__monitor/events", s.handleMonitorEvents)
	mux.HandleFunc("GET /__monitor/probes", s.handleMonitorProbes)
	mux.HandleFunc("POST /__monitor/probes/run", s.handleMonitorProbeRun)
	mux.HandleFunc("GET /__monitor/probe-config", s.handleMonitorProbeConfig)
	mux.HandleFunc("PUT /__monitor/probe-config", s.handleMonitorProbeConfigUpdate)
	mux.HandleFunc("GET /__monitor/alerts", s.handleMonitorAlerts)
	mux.HandleFunc("GET /__monitor/alerts/{id}", s.handleMonitorAlertByID)
	mux.HandleFunc("POST /__monitor/alerts/{id}/ack", s.handleMonitorAlertAck)
	mux.HandleFunc("POST /__monitor/alerts/{id}/ignore", s.handleMonitorAlertIgnore)
	mux.HandleFunc("POST /__monitor/events/{eventId}/retry-push", s.handleMonitorRetryPush)
	mux.HandleFunc("GET /__monitor/channels", s.handleMonitorChannels)
	mux.HandleFunc("POST /__monitor/channels", s.handleMonitorCreateChannel)
	mux.HandleFunc("PUT /__monitor/channels/{id}", s.handleMonitorUpdateChannel)
	mux.HandleFunc("DELETE /__monitor/channels/{id}", s.handleMonitorDeleteChannel)
	mux.HandleFunc("POST /__monitor/channels/{id}/test", s.handleMonitorTestChannel)
	mux.HandleFunc("GET /__monitor/dispatch", s.handleMonitorDispatch)
	mux.HandleFunc("PUT /__monitor/dispatch", s.handleMonitorUpdateDispatch)
	mux.HandleFunc("GET /__monitor/rules", s.handleMonitorRules)
	mux.HandleFunc("PUT /__monitor/rules", s.handleMonitorUpdateRules)
	mux.HandleFunc("PATCH /__settings/web-exposed", s.handleSetWebExposed)
	// What Qoder says when it refuses, and a probe that tells a busy model from
	// a broken account.
	mux.HandleFunc("GET /__qoder/telemetry", s.handleQoderTelemetry)
	mux.HandleFunc("POST /__qoder/diag/queue", s.handleQoderQueueDiag)
	mux.HandleFunc("PATCH /__public-access", s.handleUpdatePublicAccess)
	mux.HandleFunc("PATCH /__endpoints/{id}", s.handleUpdateEndpoint)
	mux.HandleFunc("GET /__public", s.handlePublicStatus)
	mux.HandleFunc("POST /__public/start", s.handlePublicStart)
	mux.HandleFunc("POST /__public/stop", s.handlePublicStop)
	mux.HandleFunc("POST /__public/cloudflare/login/start", s.handleCloudflareLoginStart)
	mux.HandleFunc("GET /__public/cloudflare/login/status", s.handleCloudflareLoginStatus)
	mux.HandleFunc("GET /__public/cloudflare/zones", s.handleCloudflareZones)
	mux.HandleFunc("POST /__public/cloudflare/bind", s.handleCloudflareBind)
	// Machine-facing public-access toggle (bearer token, no console session) plus
	// the admin-only endpoints that mint/inspect/revoke that token.
	mux.HandleFunc("POST /__public/control", s.handlePublicAccessControl)
	mux.HandleFunc("GET /__public/control-token", s.handlePublicAccessControlTokenStatus)
	mux.HandleFunc("POST /__public/control-token", s.handleGeneratePublicAccessControlToken)
	mux.HandleFunc("POST /__public/control-token/revoke", s.handleRevokePublicAccessControlToken)
	mux.HandleFunc("GET /__provider-groups", s.handleListProviderGroups)
	mux.HandleFunc("POST /__provider-groups", s.handleCreateProviderGroup)
	mux.HandleFunc("POST /__provider-groups/reorder", s.handleReorderProviderGroups)
	mux.HandleFunc("POST /__provider-groups/assign", s.handleAssignProviderGroup)
	mux.HandleFunc("PATCH /__provider-groups/{id}", s.handleUpdateProviderGroup)
	mux.HandleFunc("DELETE /__provider-groups/{id}", s.handleDeleteProviderGroup)
	mux.HandleFunc("POST /__providers", s.handleCreateProvider)
	mux.HandleFunc("PATCH /__providers/{id}", s.handleUpdateProvider)
	mux.HandleFunc("GET /__providers/export", s.handleExportProviders)
	mux.HandleFunc("POST /__providers/export", s.handleExportProviders)
	mux.HandleFunc("POST /__providers/import", s.handleImportProviders)
	mux.HandleFunc("POST /__providers/{id}/test", s.handleTestProvider)
	mux.HandleFunc("POST /__providers/{id}/detect-protocols", s.handleDetectProviderProtocols)
	mux.HandleFunc("POST /__providers/{id}/enabled", s.handleSetProviderEnabled)
	mux.HandleFunc("POST /__providers/{id}/rtk", s.handleSetProviderRTK)
	mux.HandleFunc("POST /__providers/{id}/first-packet-timeout", s.handleSetProviderFirstPacketTimeout)
	mux.HandleFunc("POST /__providers/{id}/concurrency", s.handleSetProviderConcurrency)
	mux.HandleFunc("POST /__providers/{id}/model-context", s.handleSetProviderModelContext)
	mux.HandleFunc("GET /__prices", s.handleListPrices)
	mux.HandleFunc("POST /__prices", s.handleSavePrice)
	mux.HandleFunc("DELETE /__prices/{providerID}/{modelID}", s.handleDeletePrice)
	mux.HandleFunc("POST /__providers/{id}/self-register-token", s.handleGenerateProviderSelfRegistrationToken)
	mux.HandleFunc("POST /__providers/{id}/self-register-token/revoke", s.handleRevokeProviderSelfRegistration)
	mux.HandleFunc("PATCH /__providers/{id}/self-register", s.handleProviderSelfRegister)
	mux.HandleFunc("POST /__providers/{id}/self-check/health", s.handleProviderSelfCheckHealth)
	mux.HandleFunc("POST /__providers/{id}/self-check/chat", s.handleProviderSelfCheckChat)
	mux.HandleFunc("POST /__providers/{id}/self-check/conformance", s.handleProviderSelfCheckConformance)
	mux.HandleFunc("POST /__providers/{id}/conformance", s.handleProviderConformance)
	mux.HandleFunc("GET /__providers/{id}/auth-preview", s.handleProviderAuthPreview)
	mux.HandleFunc("POST /__providers/{id}/chat-test", s.handleProviderChatTest)
	mux.HandleFunc("POST /__providers/{id}/cache-test", s.handleProviderCacheTest)
	mux.HandleFunc("POST /__providers/{id}/thinking-test", s.handleProviderThinkingTest)
	mux.HandleFunc("POST /__providers/{id}/claude-oauth/start", s.handleClaudeOAuthStart)
	mux.HandleFunc("GET /__providers/{id}/claude-oauth/status", s.handleClaudeOAuthStatus)
	// Claude Code OAuth client allowlists http://localhost:<port>/callback only.
	mux.HandleFunc("GET /callback", s.handleClaudeOAuthCallback)
	mux.HandleFunc("GET /__claude-oauth/callback", s.handleClaudeOAuthCallback) // legacy alias
	mux.HandleFunc("POST /__providers/{id}/claude-oauth/complete", s.handleClaudeOAuthComplete)
	mux.HandleFunc("POST /__providers/{id}/claude-oauth/disconnect", s.handleClaudeOAuthDisconnect)
	mux.HandleFunc("GET /__providers/{id}/claude-oauth/usage", s.handleClaudeOAuthUsage)
	mux.HandleFunc("POST /__providers/{id}/cursor-oauth/start", s.handleCursorOAuthStart)
	mux.HandleFunc("GET /__providers/{id}/cursor-oauth/status", s.handleCursorOAuthStatus)
	mux.HandleFunc("POST /__providers/{id}/cursor-oauth/disconnect", s.handleCursorOAuthDisconnect)
	mux.HandleFunc("GET /__providers/{id}/cursor-oauth/usage", s.handleCursorOAuthUsage)
	mux.HandleFunc("POST /__providers/{id}/chatgpt-oauth/start", s.handleChatGPTOAuthStart)
	mux.HandleFunc("GET /__providers/{id}/chatgpt-oauth/status", s.handleChatGPTOAuthStatus)
	mux.HandleFunc("GET /auth/callback", s.handleChatGPTOAuthCallback)
	mux.HandleFunc("POST /__providers/{id}/chatgpt-oauth/complete", s.handleChatGPTOAuthComplete)
	mux.HandleFunc("POST /__providers/{id}/chatgpt-oauth/disconnect", s.handleChatGPTOAuthDisconnect)
	mux.HandleFunc("GET /__providers/{id}/chatgpt-oauth/usage", s.handleChatGPTOAuthUsage)
	mux.HandleFunc("POST /__providers/{id}/chatgpt-oauth/reset-credits/consume", s.handleChatGPTOAuthResetConsume)
	mux.HandleFunc("POST /__providers/{id}/qoder-pat/complete", s.handleQoderPATComplete)
	mux.HandleFunc("GET /__providers/{id}/qoder-pat/status", s.handleQoderPATStatus)
	mux.HandleFunc("POST /__providers/{id}/qoder-pat/disconnect", s.handleQoderPATDisconnect)
	mux.HandleFunc("POST /__providers/{id}/qoder-pat/reconnect", s.handleQoderPATReconnect)
	mux.HandleFunc("POST /__providers/{id}/qoder-oauth/start", s.handleQoderOAuthStart)
	mux.HandleFunc("GET /__providers/{id}/qoder-oauth/status", s.handleQoderOAuthStatus)
	// Pause and resume cover both doors: the shared subscription panel in the
	// console speaks these verbs, and a paused account of either kind stops
	// forwarding while keeping what it has.
	mux.HandleFunc("POST /__providers/{id}/qoder-oauth/pause", s.handleQoderPATDisconnect)
	mux.HandleFunc("POST /__providers/{id}/qoder-oauth/resume", s.handleQoderPATReconnect)
	mux.HandleFunc("GET /__providers/{id}/qoder-oauth/usage", s.handleQoderUsage)
	// The daily check-in: press it now, and switch the automatic one on or off.
	// Only the subscriptions whose vendor runs such an event answer; the rest
	// get 409 and the console shows no button for them.
	mux.HandleFunc("POST /__providers/{id}/checkin", s.handleProviderCheckin)
	mux.HandleFunc("POST /__providers/{id}/checkin/auto", s.handleProviderCheckinAuto)
	mux.HandleFunc("POST /__providers/{id}/workbuddy-oauth/start", s.handleWorkBuddyOAuthStart)
	mux.HandleFunc("GET /__providers/{id}/workbuddy-oauth/status", s.handleWorkBuddyOAuthStatus)
	mux.HandleFunc("POST /__providers/{id}/workbuddy-oauth/disconnect", s.handleWorkBuddyOAuthDisconnect)
	mux.HandleFunc("GET /__providers/{id}/workbuddy-oauth/usage", s.handleWorkBuddyUsage)
	// The task center: read the list, run one or every task, report activity, and
	// keep the account alive. A run outlives the request that started it (its
	// actions pace themselves), so it is polled by run id.
	mux.HandleFunc("GET /__providers/{id}/workbuddy-oauth/tasks", s.handleWorkBuddyTasks)
	mux.HandleFunc("POST /__providers/{id}/workbuddy-oauth/tasks/auto", s.handleWorkBuddyTasksAuto)
	mux.HandleFunc("GET /__providers/{id}/workbuddy-oauth/tasks/run", s.handleWorkBuddyRunStatus)
	mux.HandleFunc("POST /__providers/{id}/workbuddy-oauth/activity", s.handleWorkBuddyActivity)
	mux.HandleFunc("POST /__providers/{id}/workbuddy-oauth/keepalive", s.handleWorkBuddyKeepalive)
	mux.HandleFunc("POST /__providers/{id}/workbuddy-oauth/automation", s.handleWorkBuddyAutomation)
	mux.HandleFunc("POST /__providers/{id}/zcode-oauth/start", s.handleZCodeOAuthStart)
	mux.HandleFunc("GET /__providers/{id}/zcode-oauth/status", s.handleZCodeOAuthStatus)
	mux.HandleFunc("POST /__providers/{id}/zcode-oauth/pause", s.handleZCodeOAuthPause)
	mux.HandleFunc("POST /__providers/{id}/zcode-oauth/resume", s.handleZCodeOAuthResume)
	mux.HandleFunc("GET /__providers/{id}/zcode-oauth/usage", s.handleZCodeUsage)
	mux.HandleFunc("POST /__providers/{id}/trae-oauth/start", s.handleTraeOAuthStart)
	mux.HandleFunc("GET /__providers/{id}/trae-oauth/status", s.handleTraeOAuthStatus)
	mux.HandleFunc("POST /__providers/{id}/trae-oauth/pause", s.handleTraeOAuthPause)
	mux.HandleFunc("POST /__providers/{id}/trae-oauth/resume", s.handleTraeOAuthResume)
	mux.HandleFunc("GET /__providers/{id}/trae-oauth/usage", s.handleTraeUsage)
	mux.HandleFunc("GET /__providers/{id}/zhipu/usage", s.handleZhipuUsage)
	mux.HandleFunc("GET /__providers/{id}/deepseek/usage", s.handleDeepSeekBalance)
	mux.HandleFunc("GET /__providers/{id}/opencode-go/usage", s.handleOpenCodeGoUsage)
	mux.HandleFunc("DELETE /__providers/{id}", s.handleDeleteProvider)
	mux.HandleFunc("GET /__providers/deleted", s.handleListDeletedProviders)
	mux.HandleFunc("POST /__providers/{id}/restore", s.handleRestoreProvider)
	mux.HandleFunc("DELETE /__providers/{id}/purge", s.handlePurgeProvider)
	mux.HandleFunc("POST /__routes", s.handleCreateRoute)
	mux.HandleFunc("PATCH /__routes/{id}", s.handleUpdateRoute)
	mux.HandleFunc("DELETE /__routes/{id}", s.handleDeleteRoute)
	mux.HandleFunc("POST /__routes/{id}/test", s.handleTestRoute)
	mux.HandleFunc("GET /__apikeys", s.handleListAPIKeys)
	mux.HandleFunc("POST /__apikeys", s.handleCreateAPIKey)
	mux.HandleFunc("PATCH /__apikeys/{id}", s.handleUpdateAPIKey)
	mux.HandleFunc("DELETE /__apikeys/{id}", s.handleDeleteAPIKey)
	mux.HandleFunc("POST /__apikeys/{id}/profiles", s.handleCreateKeyProfile)
	mux.HandleFunc("PATCH /__apikeys/{id}/profiles/{pid}", s.handleUpdateKeyProfile)
	mux.HandleFunc("DELETE /__apikeys/{id}/profiles/{pid}", s.handleDeleteKeyProfile)
	mux.HandleFunc("POST /__apikeys/{id}/active-profile", s.handleSwitchKeyProfile)
	// 把运行网关的这台机器上的 Claude Code CLI / Claude Desktop 指向本网关。
	// 桌面 App 与管理页共用这一条：只有本机请求才会落盘（见 handleSetupClients）。
	mux.HandleFunc("POST /__clients/setup", s.handleSetupClients)
	mux.HandleFunc("GET /__users", s.handleListUsers)
	mux.HandleFunc("POST /__users", s.handleCreateUser)
	mux.HandleFunc("PATCH /__users/{id}", s.handleUpdateUser)
	mux.HandleFunc("DELETE /__users/{id}", s.handleDeleteUser)
	mux.HandleFunc("POST /__users/{id}/reset-password", s.handleResetUserPassword)
	// 对端网关（父子对接）。注册是唯一不需要已有凭据的入口，靠来源地址与
	// 用户名密码把关；其余端点只认对端凭据。整段前缀在 admin_auth 里被旁路
	// 掉会话鉴权，见 withAdminAuth。
	mux.HandleFunc("POST /__peer/register", s.handlePeerRegister)
	// 流量 bearer 失效时，下游拿独立的 gwrefresh- 凭据静默换发一组新凭据。
	// 这个入口不接受普通 bearer，也不参与模型转发。
	mux.HandleFunc("POST /__peer/renew", s.handlePeerRenew)
	// 老记录第一次升级到双凭据时，用当前仍有效的流量 bearer 换取续签凭据。
	// 仅当该账号还没有续签凭据时允许，之后只能用 gwrefresh- 走 /renew。
	mux.HandleFunc("POST /__peer/refresh-token", s.handlePeerIssueRefreshToken)
	// 注册前的连通性探测：表单上「测试连接」问的就是它。不需要凭据，也不写
	// 任何状态——此刻还没有账号可言。
	mux.HandleFunc("GET /__peer/probe", s.handlePeerProbe)
	mux.HandleFunc("GET /__peer/me", s.handlePeerMe)
	mux.HandleFunc("GET /__peer/providers", s.handlePeerProviders)
	mux.HandleFunc("POST /__peer/chat/completions", s.handlePeerChatCompletions)
	// 管理侧：列出/吊销已注册的下游网关。
	mux.HandleFunc("GET /__peers", s.handleListPeers)
	mux.HandleFunc("GET /__peers/owners", s.handlePeerOwnerOptions)
	mux.HandleFunc("PATCH /__peers/{id}/owner", s.handleSetPeerOwner)
	mux.HandleFunc("DELETE /__peers/{id}", s.handleRevokePeer)
	mux.HandleFunc("PATCH /__settings/peer-registration-cidrs", s.handleSetPeerRegistrationCIDRs)
	mux.HandleFunc("PATCH /__settings/peer-max-hops", s.handleSetPeerMaxHops)
	// 修改一个已注册下游的授权（按层授权的编辑入口）：注册时的预置授权只是一次
	// 快照，之后上游随时可以改，立即生效。
	mux.HandleFunc("PATCH /__peers/{id}/grants", s.handleSetPeerGrants)
	// 下游侧：本机注册到上游网关，与上面那组是同一张页面的两半——上半是
	// "谁连到我"，下半是"我连到谁"。凭据（gwpeer-…）落在 provider 的
	// apiKeySource 上，所以这一组本质是普通的 provider 管理动作，只是包一层，
	// 免得运维要在"输入 Provider"里手填一个 /__peer 地址。
	mux.HandleFunc("GET /__peer-upstreams", s.handleListPeerUpstreams)
	// 表单上的「测试连接」：只探连通性与注册来源限制，不建账号、不换凭据。
	mux.HandleFunc("POST /__peer-upstreams/probe", s.handleProbePeerUpstream)
	mux.HandleFunc("POST /__peer-upstreams", s.handleRegisterPeerUpstream)
	mux.HandleFunc("POST /__peer-upstreams/{id}/refresh", s.handleRefreshPeerUpstream)
	mux.HandleFunc("POST /__peer-upstreams/{id}/reconnect", s.handleReconnectPeerUpstream)
	// 断开一条上游＝删掉它这一组的全部成员行（见 handleDisconnectPeerUpstream）。
	mux.HandleFunc("DELETE /__peer-upstreams/{id}", s.handleDisconnectPeerUpstream)
	// RBAC: roles, the permission catalog, resource grant targets, and the
	// signed-in user's own profile (display name + avatar).
	mux.HandleFunc("GET /__roles", s.handleListRoles)
	mux.HandleFunc("GET /__roles/options", s.handleListRoleOptions)
	mux.HandleFunc("GET /__roles/catalog", s.handleRoleCatalog)
	mux.HandleFunc("GET /__roles/grant-targets", s.handleGrantTargets)
	mux.HandleFunc("POST /__roles", s.handleCreateRole)
	mux.HandleFunc("PATCH /__roles/{id}", s.handleUpdateRole)
	mux.HandleFunc("POST /__roles/{id}/copy", s.handleCopyRole)
	mux.HandleFunc("DELETE /__roles/{id}", s.handleDeleteRole)
	mux.HandleFunc("PATCH /__profile", s.handleProfileUpdate)
	mux.HandleFunc("GET /__quota-tasks", s.handleListScheduledTasks)
	mux.HandleFunc("POST /__quota-tasks", s.handleCreateScheduledTask)
	mux.HandleFunc("POST /__quota-tasks/preview", s.handlePreviewScheduledTask)
	mux.HandleFunc("PATCH /__quota-tasks/{id}", s.handleUpdateScheduledTask)
	mux.HandleFunc("DELETE /__quota-tasks/{id}", s.handleDeleteScheduledTask)
	mux.HandleFunc("POST /__quota-tasks/{id}/run", s.handleRunScheduledTask)
	mux.HandleFunc("GET /__quota-tasks/{id}/runs", s.handleListScheduledTaskRuns)
	mux.HandleFunc("GET /v1/models", s.handleOpenAIModels)
	mux.HandleFunc("GET /openai/v1/models", s.handleOpenAIModels)
	mux.HandleFunc("GET /anthropic/v1/models", s.handleClaudeModels)
	mux.HandleFunc("POST /v1/chat/completions", s.handleOpenAIChat)
	mux.HandleFunc("POST /chat/completions", s.handleOpenAIChat)
	mux.HandleFunc("POST /v1/embeddings", s.handleOpenAIEmbeddings)
	mux.HandleFunc("POST /openai/v1/embeddings", s.handleOpenAIEmbeddings)
	mux.HandleFunc("POST /v1/audio/transcriptions", s.handleOpenAIAudioTranscriptions)
	mux.HandleFunc("POST /openai/v1/audio/transcriptions", s.handleOpenAIAudioTranscriptions)
	mux.HandleFunc("POST /v1/audio/translations", s.handleOpenAIAudioTranslations)
	mux.HandleFunc("POST /openai/v1/audio/translations", s.handleOpenAIAudioTranslations)
	// Gemini 原生协议：模型名在路径里（Google 的形状），ServeMux 无法表达
	// "/{model}:{action}" 的尾巴，所以两条前缀都交给同一个处理器自己解析。
	mux.HandleFunc("POST /v1beta/", s.handleGeminiGenerateContent)
	mux.HandleFunc("POST /v1/models/", s.handleGeminiGenerateContent)
	mux.HandleFunc("POST /v1/images/generations", s.handleOpenAIImagesGenerations)
	mux.HandleFunc("POST /v1/images/edits", s.handleOpenAIImagesEdits)
	mux.HandleFunc("POST /v1/videos/generations", s.handleOpenAIVideosGenerations)
	mux.HandleFunc("POST /v1/videos/tasks", s.handleOpenAIVideosTask)
	mux.HandleFunc("GET /v1/videos/{taskID}", s.handleOpenAIVideoTaskByID)
	// The two agent tools: web search and page fetch, which answer without the
	// {code,msg,data} envelope the account surface uses (see workbuddy_agenttool.go).
	mux.HandleFunc("POST /v1/search", s.handleWorkBuddySearch)
	mux.HandleFunc("POST /v1/webfetch", s.handleWorkBuddyWebFetch)
	mux.HandleFunc("POST /openai/v1/images/generations", s.handleOpenAIImagesGenerations)
	mux.HandleFunc("POST /openai/v1/responses", s.handleOpenAIResponses)
	// Real OpenAI Responses API endpoint Codex CLI calls for "remote
	// compaction" on long sessions (see responses_compact.go for why this
	// exists — without it, Codex 404s and the session breaks mid-task).
	mux.HandleFunc("POST /openai/v1/responses/compact", s.handleOpenAIResponsesCompact)
	mux.HandleFunc("POST /anthropic/v1/messages", s.handleClaudeMessages)
	mux.HandleFunc("POST /anthropic/v1/messages/count_tokens", s.handleClaudeCountTokens)
	// Claude Desktop 把 /v1/messages 直接拼在 inferenceGatewayBaseUrl 后面，
	// 它写进配置的地址不带 /anthropic（3p 模式的约定，magpie 也是在根路径上
	// 提供这个端点）。少了这两条，桌面端能列出模型却一发消息就 404。
	mux.HandleFunc("POST /v1/messages", s.handleClaudeMessages)
	mux.HandleFunc("POST /v1/messages/count_tokens", s.handleClaudeCountTokens)
	api := withCORS(mux)
	// 任何 POST 正文都有上限：限制在读取处（ReadAll 之前），超限时各协议自己回 413。
	api = withBodyLimit(api)
	// 环路闸门放在最内层（紧贴 mux）：它只需要看请求头，越早拒绝越省。
	// 必须包在 mux 外层而不是塞进每个 handler——模型端点和控制台端点都会
	// 经过这里，漏掉任何一个就等于留了一条能把自己绕死的路径。
	api = s.withGatewayLoopGuard(api)
	// 宿主分离必须在鉴权外层：API 域名上的控制台接口应当"不存在"（404），
	// 而不是先被鉴权回 401——那等于告诉外面"这里有个管理接口，只是你没登录"。
	// UI 域名上的模型协议同理，不该先走一遍管理鉴权。
	return withHostSeparatedServing(s.withAdminAuth(api), findWebDistDir(), s.publicHostRole)
}

// publicHostRole returns "api" / "ui" when the request Host matches the
// dedicated custom-domain hostnames; otherwise "" (combined local/LAN mode).
func (s *Server) publicHostRole(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return ""
	}
	settings := s.router.State().PublicAccess
	apiHost := ""
	uiHost := ""
	if settings.ExposeAPI {
		apiHost = cleanPublicHost(settings.CustomDomain)
	}
	if settings.ExposeUI {
		uiHost = cleanPublicHost(settings.UIDomain)
	}
	if apiHost != "" && strings.EqualFold(host, apiHost) {
		return "api"
	}
	if uiHost != "" && strings.EqualFold(host, uiHost) {
		return "ui"
	}
	return ""
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now().Format(time.RFC3339)})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	identity := s.requestIdentity(r)
	state := s.router.State()
	// Soft-deleted providers live on in storage (see Router.DeleteProvider)
	// but never appear in the normal console listing; admins use the
	// dedicated GET /__providers/deleted trash view to find and restore them.
	state.Providers = activeProviders(state.Providers)
	// 「在服务」= 最近一次真正返回响应的 Provider，而不是粘滞位（见
	// serving_provider.go：会话粘性命中的那一轮不改写 ActiveProviderID，
	// 控制台以前只看粘滞位，于是日志显示备选在服务、界面却仍标首选）。
	for index := range state.APIKeys {
		state.APIKeys[index].ServingProviderID = s.servingProviderFor(state.APIKeys[index])
		state.APIKeys[index].ServingProviderIDs = s.servingProvidersFor(state.APIKeys[index])
	}
	state.Providers = redactProvidersForClient(state.Providers)
	for index := range state.Providers {
		enrichProviderAdapterCurl(&state.Providers[index])
		s.applyProviderAvailability(&state.Providers[index])
	}
	// Attach live tunnel status/URLs for everyone — including role=user.
	// Without this, regular users fall back to LAN addresses on the API-key
	// page because tunnelRunning/livePublicURL stay empty.
	state.PublicAccess = s.withTunnelRuntime(state.PublicAccess)
	state.CursorBridge = s.cursorBridgeRuntime()
	// 服务器自己的"今天"（API 密钥今日计数、定时任务、纯日期的日界）按 time.Local
	// 算；控制台据此把「今日」换算到同一个时区，两个页面才不会各说一天。
	state.ServerTimeZone = serverTimeZoneName()
	// 身份对所有角色可见：普通用户配"父网关 ID"时也要能复制自己这一侧的 ID。
	// 它不含机器标识本身，只有派生后的哈希，所以不存在泄漏问题。
	state.GatewayIdentity = s.GatewayIdentityForConsole()
	p := s.requestPrincipal(r)
	if p.scopeOf() != domain.ScopeAll {
		writeJSON(w, http.StatusOK, s.stateForUser(identity, state))
		return
	}
	if !p.IsAdmin {
		// Provider 分组只属于管理员控制台；ScopeAll 的自定义角色也不能看到。
		stripProviderGroupsForUser(&state)
	}
	state.RequestLogRetentionDays = s.RequestLogRetentionDays()
	state.Log2xxBodies = s.Log2xxBodies()
	// Send the effective values, not the raw 0-means-default ones: both fields
	// are omitempty, so a stored 0 would vanish from the payload and the console
	// would show its hardcoded placeholders instead of what the worker runs with.
	effectiveBatchSize, effectiveBatchWait := monitor.NormalizeUsageBatchConfig(
		state.UsageBatchMaxSize,
		time.Duration(state.UsageBatchMaxWaitSeconds)*time.Second,
	)
	state.UsageBatchMaxSize = effectiveBatchSize
	state.UsageBatchMaxWaitSeconds = int(effectiveBatchWait / time.Second)
	state.WebExposed = s.router.WebExposed()
	paths := ResolveDataPaths()
	state.DataPaths = &paths
	writeJSON(w, http.StatusOK, state)
}

// serverTimeZoneName reports the gateway's local zone as an IANA name. A
// container on TZ=UTC reports "UTC"; an unnamed fixed zone (a bare numeric
// offset) falls back to its formatted offset so the console can still build the
// right day bounds.
//
// TZ 优先于 time.Local：两者在进程启动时通常一致，但测试只能通过 TZ 注入时区
// （直接写 time.Local 会与其它 goroutine 的 time.Now 构成 data race），
// 且与 serverTimeZoneLabel 保持同一口径。
func serverTimeZoneName() string {
	if name := strings.TrimSpace(os.Getenv("TZ")); name != "" {
		if _, err := time.LoadLocation(name); err == nil {
			return name
		}
	}
	return timeZoneNameFor(time.Local)
}

// timeZoneNameFor 是 serverTimeZoneName 的纯函数部分：只依赖传入的时区，
// 不碰全局状态，测试可以直接喂一个 FixedZone 验证回退分支。
func timeZoneNameFor(loc *time.Location) string {
	if loc == nil {
		return time.Now().Format("Z07:00")
	}
	name := loc.String()
	if name == "" || name == "Local" {
		return time.Now().In(loc).Format("Z07:00")
	}
	return name
}

func (s *Server) handleSettingsPaths(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, ResolveDataPaths())
}

// activeProviders filters out soft-deleted providers (see
// Router.DeleteProvider) for normal listings; deleted providers are only
// exposed via the dedicated trash endpoints.
func activeProviders(providers []domain.Provider) []domain.Provider {
	out := make([]domain.Provider, 0, len(providers))
	for _, provider := range providers {
		if provider.Deleted {
			continue
		}
		out = append(out, provider)
	}
	return out
}

// redactProvidersForClient deep-copies providers and scrubs secret fields
// (currently the Claude OAuth access/refresh tokens) before they are sent to
// the frontend. Internal code paths (SQLite save/load, OAuth handlers) must
// keep using the router's real domain.Provider values, never this copy.
func redactProvidersForClient(providers []domain.Provider) []domain.Provider {
	redacted := make([]domain.Provider, len(providers))
	for index, provider := range providers {
		redacted[index] = redactProviderForClient(provider)
	}
	return redacted
}

func redactProviderForClient(provider domain.Provider) domain.Provider {
	if provider.ClaudeOAuth != nil {
		original := provider.ClaudeOAuth
		provider.ClaudeOAuth = &domain.ClaudeOAuthCredential{
			ExpiresAt:    original.ExpiresAt,
			Scope:        original.Scope,
			AccountLabel: original.AccountLabel,
			Connected:    strings.TrimSpace(original.AccessToken) != "" && strings.TrimSpace(original.RefreshToken) != "",
		}
	}
	if provider.CursorOAuth != nil {
		original := provider.CursorOAuth
		provider.CursorOAuth = &domain.CursorOAuthCredential{
			ExpiresAt:    original.ExpiresAt,
			AccountLabel: original.AccountLabel,
			Connected:    strings.TrimSpace(original.AccessToken) != "" && strings.TrimSpace(original.RefreshToken) != "",
		}
	}
	if provider.ChatGPTOAuth != nil {
		original := provider.ChatGPTOAuth
		provider.ChatGPTOAuth = &domain.ChatGPTOAuthCredential{
			ExpiresAt:    original.ExpiresAt,
			AccountLabel: original.AccountLabel,
			Connected:    strings.TrimSpace(original.AccessToken) != "" && strings.TrimSpace(original.RefreshToken) != "",
		}
	}
	if provider.QoderPAT != nil {
		original := provider.QoderPAT
		hasToken := strings.TrimSpace(original.RefreshToken) != "" || strings.TrimSpace(original.PAT) != ""
		mode := original.SignInMode
		if mode == "" {
			mode = qoderSignInModePAT
		}
		provider.QoderPAT = &domain.QoderCredential{
			ExpiresAt:    original.ExpiresAt,
			AccountLabel: original.AccountLabel,
			Email:        original.Email,
			SignInMode:   mode,
			DeviceChat:   original.DeviceChat,
			// Connected means "can forward right now": a credential present AND
			// not paused by an explicit disconnect. The job token itself
			// expiring does not affect this — it renews on demand.
			Connected:      hasToken && !original.Disconnected,
			Disconnected:   original.Disconnected,
			HasStoredToken: hasToken,
		}
	}
	if provider.WorkBuddy != nil {
		original := provider.WorkBuddy
		provider.WorkBuddy = &domain.WorkBuddyCredential{
			ExpiresAt:    original.ExpiresAt,
			AccountLabel: original.AccountLabel,
			// The account id and domain are identifiers, not secrets, and the
			// console shows them so an operator can tell two WorkBuddy accounts
			// apart.
			UserID:    original.UserID,
			Domain:    original.Domain,
			Connected: strings.TrimSpace(original.AccessToken) != "" && strings.TrimSpace(original.RefreshToken) != "",
		}
	}
	if provider.ZCode != nil {
		original := provider.ZCode
		provider.ZCode = &domain.ZCodeCredential{
			// The key itself never leaves the server; the console sees which
			// realm and project it was minted from, and what plan it bills on.
			ExpiresAt:      original.ExpiresAt,
			AccountLabel:   original.AccountLabel,
			Site:           original.Site,
			Base:           original.Base,
			OrganizationID: original.OrganizationID,
			ProjectID:      original.ProjectID,
			Plan:           original.Plan,
			Connected:      strings.TrimSpace(original.APIKey) != "" && !original.Disconnected,
			Disconnected:   original.Disconnected,
		}
	}
	if provider.Trae != nil {
		original := provider.Trae
		provider.Trae = &domain.TraeCredential{
			ExpiresAt:    original.ExpiresAt,
			AccountLabel: original.AccountLabel,
			UserID:       original.UserID,
			DeviceID:     original.DeviceID,
			MachineID:    original.MachineID,
			APIHost:      original.APIHost,
			AuthHost:     original.AuthHost,
			Region:       original.Region,
			ChatFunction: original.ChatFunction,
			Connected:    strings.TrimSpace(original.AccessToken) != "" && !original.Disconnected,
			Disconnected: original.Disconnected,
		}
	}
	// Regenerate curl against the real BaseURL for UI display.
	enrichProviderAdapterCurl(&provider)
	return provider
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	identity := s.requestIdentity(r)
	if idStr := strings.TrimSpace(r.URL.Query().Get("id")); idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id <= 0 {
			writeOpenAIError(w, http.StatusBadRequest, "invalid request log id")
			return
		}
		s.FlushRequestLogs()
		if s.requestLogStore == nil {
			writeOpenAIError(w, http.StatusNotFound, "request log store not available")
			return
		}
		logItem, found, err := s.requestLogStore.RequestLogByID(id)
		if err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "failed to query request log: "+err.Error())
			return
		}
		if !found {
			writeOpenAIError(w, http.StatusNotFound, "request log not found")
			return
		}
		if s.requestPrincipal(r).scopeOf() != domain.ScopeAll {
			owned := s.ownedKeyIDs(identity.UserID)
			allowed := false
			for _, k := range owned {
				if k == logItem.APIKeyID {
					allowed = true
					break
				}
			}
			if !allowed {
				writeOpenAIError(w, http.StatusNotFound, "request log not found")
				return
			}
		}
		items := []monitor.RequestLog{logItem}
		s.fillRequestLogKeyNames(items)
		s.fillRequestLogUserNames(items)
		writeJSONGzip(w, r, http.StatusOK, items[0])
		return
	}

	query := monitor.RequestLogQuery{
		Status:        strings.TrimSpace(r.URL.Query().Get("status")),
		APIKeyName:    strings.TrimSpace(r.URL.Query().Get("apiKeyName")),
		ProviderID:    strings.TrimSpace(r.URL.Query().Get("providerId")),
		Model:         strings.TrimSpace(r.URL.Query().Get("model")),
		SessionID:     strings.TrimSpace(r.URL.Query().Get("sessionId")),
		Page:          atoiDefault(r.URL.Query().Get("page"), 1),
		PageSize:      atoiDefault(r.URL.Query().Get("pageSize"), 100),
		IncludeBodies: parseBoolQuery(r.URL.Query().Get("includeBodies")),
	}
	if s.requestPrincipal(r).scopeOf() != domain.ScopeAll {
		// Restricted scopes only see logs of their own keys (empty set matches nothing).
		query.APIKeyIDs = s.ownedKeyIDs(identity.UserID)
	} else if ownerUserID := strings.TrimSpace(r.URL.Query().Get("ownerUserId")); ownerUserID != "" {
		// Admin-only "所属用户" log filter; empty param means no filter (see all users).
		query.APIKeyIDs = s.ownedKeyIDsForOwnerFilter(ownerUserID)
	}
	if keyID := strings.TrimSpace(r.URL.Query().Get("apiKeyId")); keyID != "" {
		query.APIKeyIDs = intersectKeyIDs(query.APIKeyIDs, []string{keyID})
	}
	if from := strings.TrimSpace(r.URL.Query().Get("from")); from != "" {
		if parsed, err := time.Parse(time.RFC3339, from); err == nil {
			query.From = parsed
		} else if parsed, err := time.ParseInLocation("2006-01-02", from, time.Local); err == nil {
			query.From = parsed
		}
	}
	if to := strings.TrimSpace(r.URL.Query().Get("to")); to != "" {
		if parsed, err := time.Parse(time.RFC3339, to); err == nil {
			query.To = parsed
		} else if parsed, err := time.ParseInLocation("2006-01-02", to, time.Local); err == nil {
			query.To = parsed.Add(24 * time.Hour)
		}
	}
	// beforeTime / beforeId freeze a pagination snapshot: the console captures
	// the newest log on page 1 so offset paging stays stable as new logs arrive.
	if beforeTime := strings.TrimSpace(r.URL.Query().Get("beforeTime")); beforeTime != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, beforeTime); err == nil {
			query.BeforeTime = parsed
		} else if parsed, err := time.Parse(time.RFC3339, beforeTime); err == nil {
			query.BeforeTime = parsed
		}
	}
	if beforeID := strings.TrimSpace(r.URL.Query().Get("beforeId")); beforeID != "" {
		if parsed, err := strconv.ParseInt(beforeID, 10, 64); err == nil && parsed > 0 {
			query.BeforeID = parsed
		}
	}
	// 密钥改名后，历史日志仍存着旧名字：按不变的 ID 过滤，才能筛出改名前的记录。
	query = resolveLogKeyNameFilter(s.router.State().APIKeys, query)
	// 成功请求是攒批落库的；先落盘再查，控制台才不会漏掉刚刚发生的请求。
	s.FlushRequestLogs()
	if s.requestLogStore != nil {
		page, err := s.requestLogStore.QueryRequestLogs(query)
		if err == nil {
			if !query.IncludeBodies {
				stripRequestLogBodies(page.Items)
			}
			s.fillRequestLogKeyNames(page.Items)
			s.fillRequestLogUserNames(page.Items)
			writeJSONGzip(w, r, http.StatusOK, page)
			return
		}
		s.logs.AddApp("warn", "query request logs failed", err.Error())
	}
	page := s.logs.Query(query)
	if !query.IncludeBodies {
		stripRequestLogBodies(page.Items)
	}
	s.fillRequestLogKeyNames(page.Items)
	s.fillRequestLogUserNames(page.Items)
	writeJSONGzip(w, r, http.StatusOK, page)
}

func atoiDefault(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func parseBoolQuery(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func stripRequestLogBodies(logs []monitor.RequestLog) {
	for i := range logs {
		logs[i].RequestBody = ""
		logs[i].ResponseBody = ""
	}
}

func (s *Server) handleRequestStats(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	localNow := now.Local()
	from := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, localNow.Location())
	to := from.AddDate(0, 0, 1)
	if raw := strings.TrimSpace(r.URL.Query().Get("from")); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			from = parsed
		} else if parsed, err := time.ParseInLocation("2006-01-02", raw, localNow.Location()); err == nil {
			from = parsed
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("to")); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			to = parsed
			if strings.Contains(raw, "23:59:59") {
				to = time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, to.Location()).AddDate(0, 0, 1)
			}
		} else if parsed, err := time.ParseInLocation("2006-01-02", raw, localNow.Location()); err == nil {
			to = parsed.AddDate(0, 0, 1)
		}
	}
	cacheKey := from.Format(time.RFC3339) + "|" + to.Format(time.RFC3339)
	// Hourly buckets only exist in the in-memory log ring (recent 500 after a
	// restart). Ranges inside retention are recomputed from complete persisted
	// logs; longer ranges use every retained log and only report hourly coverage.
	hourlyCoveredByRetention := to.Sub(from) <= time.Duration(s.RequestLogRetentionDays())*24*time.Hour
	identity := s.requestIdentity(r)
	if s.requestPrincipal(r).scopeOf() != domain.ScopeAll {
		// Per-user stats: computed from own keys only; cached per user.
		keyIDs := s.ownedKeyIDsForUsage(identity.UserID)
		userCacheKey := cacheKey + "|user:" + identity.UserID
		if cached, ok := s.requestStatsCache.get(userCacheKey); ok {
			w.Header().Set("Cache-Control", "private, max-age=5")
			writeJSON(w, http.StatusOK, cached)
			return
		}
		snapshot := s.logs.UsageStatsRangeForKeys(now, from, to, keyIDs)
		if s.requestLogStore != nil && len(keyIDs) > 0 {
			rangeTotal := int64(0)
			if snapshot.Range != nil {
				rangeTotal = snapshot.Range.Total.RequestCount
			}
			needRange := snapshot.Range == nil || rangeTotal == 0
			hourlyTotal := hourlyStatsTotal(snapshot.Hourly)
			needHourly := hourlyTotal == 0 || hourlyTotal < rangeTotal
			needDaily := len(snapshot.Daily) == 0
			needStatus := statusStatsTotal(snapshot.Status) == 0
			if needRange || needHourly || needDaily || needStatus {
				if dbLogs := s.loadRequestLogsForStatsForKeys(from, to, keyIDs, requestStatsLogLimit); len(dbLogs) > 0 {
					logsComplete := len(dbLogs) < requestStatsLogLimit
					// Beyond retention the logs cover only part of the range, so they may
					// fill hourly but must never replace the permanent totals.
					backfillTotals := needHourly && hourlyCoveredByRetention && logsComplete
					if needHourly {
						snapshot.Hourly = monitor.AggregateHourly(dbLogs, from, to)
					}
					if needDaily || backfillTotals || (len(snapshot.Daily) == 1 && snapshot.Daily[0].RequestCount == 0) {
						snapshot.Daily = mergeDailyTrafficBytes(monitor.AggregateDaily(dbLogs, from, to), snapshot.Daily)
					}
					if needStatus || backfillTotals {
						snapshot.Status = monitor.AggregateStatus(dbLogs, from, to)
					}
					if needRange || backfillTotals || (!needHourly && int64(len(dbLogs)) > rangeTotal) {
						periodLabel := from.Format("2006-01-02") + " ~ " + to.Add(-time.Nanosecond).Format("2006-01-02")
						rStats := monitor.AggregateLogsInRange(dbLogs, from, to, periodLabel)
						snapshot.Range = &rStats
					}
				}
			}
		}
		s.normalizeUsageModelIdentities(&snapshot)
		s.fillUsageKeyNames(&snapshot)
		s.fillUsageDownstreamGatewayNames(&snapshot)
		// This branch is already scoped to the user's own keys; the second value
		// on provider cards is therefore the same breakdown as ByProvider.
		snapshot.Today.MyByProvider = append([]monitor.ProviderDayStats(nil), snapshot.Today.ByProvider...)
		s.requestStatsCache.set(userCacheKey, snapshot)
		w.Header().Set("Cache-Control", "private, max-age=5")
		writeJSON(w, http.StatusOK, snapshot)
		return
	}
	cacheKey += "|owner:" + strings.TrimSpace(identity.UserID)
	if cached, ok := s.requestStatsCache.get(cacheKey); ok {
		w.Header().Set("Cache-Control", "private, max-age=5")
		writeJSON(w, http.StatusOK, cached)
		return
	}
	snapshot := s.logs.UsageStatsRange(now, from, to)
	ownKeyIDs := s.ownedKeyIDsForUsage(identity.UserID)
	snapshot.Today.MyByProvider = s.logs.TodayProviderStatsForKeys(now, ownKeyIDs)
	if s.requestLogStore != nil {
		rangeTotal := int64(0)
		if snapshot.Range != nil {
			rangeTotal = snapshot.Range.Total.RequestCount
		}
		needRange := snapshot.Range == nil || rangeTotal == 0
		hourlyTotal := hourlyStatsTotal(snapshot.Hourly)
		needHourly := hourlyTotal == 0 || hourlyTotal < rangeTotal
		needDaily := len(snapshot.Daily) == 0
		needStatus := statusStatsTotal(snapshot.Status) == 0
		if needRange || needHourly || needDaily || needStatus {
			if dbLogs := s.loadRequestLogsForStats(from, to, requestStatsLogLimit); len(dbLogs) > 0 {
				logsComplete := len(dbLogs) < requestStatsLogLimit
				// Beyond retention the logs cover only part of the range, so they may
				// fill hourly but must never replace the permanent totals.
				backfillTotals := needHourly && hourlyCoveredByRetention && logsComplete
				if needHourly {
					snapshot.Hourly = monitor.AggregateHourly(dbLogs, from, to)
				}
				if needDaily || backfillTotals || (len(snapshot.Daily) == 1 && snapshot.Daily[0].RequestCount == 0) {
					snapshot.Daily = mergeDailyTrafficBytes(monitor.AggregateDaily(dbLogs, from, to), snapshot.Daily)
				}
				if needStatus || backfillTotals {
					snapshot.Status = monitor.AggregateStatus(dbLogs, from, to)
				}
				if needRange || backfillTotals || (!needHourly && int64(len(dbLogs)) > rangeTotal) {
					periodLabel := from.Format("2006-01-02") + " ~ " + to.Add(-time.Nanosecond).Format("2006-01-02")
					rStats := monitor.AggregateLogsInRange(dbLogs, from, to, periodLabel)
					s.fillUsageUserBreakdown(&rStats, dbLogs, from, to)
					snapshot.Range = &rStats
				}
			}
		}
	}
	s.normalizeUsageModelIdentities(&snapshot)
	s.fillUsageKeyNames(&snapshot)
	s.fillUsageUserNames(&snapshot)
	s.fillUsageDownstreamGatewayNames(&snapshot)
	s.requestStatsCache.set(cacheKey, snapshot)
	w.Header().Set("Cache-Control", "private, max-age=5")
	writeJSON(w, http.StatusOK, snapshot)
}

// mergeDailyTrafficBytes keeps the permanent byte counters when a day's
// request/token numbers are recomputed from request logs, which do not store
// payload sizes.
func mergeDailyTrafficBytes(points, stored []monitor.DailyRequestPoint) []monitor.DailyRequestPoint {
	traffic := make(map[string][2]int64, len(stored))
	for _, point := range stored {
		if point.RxBytes > 0 || point.TxBytes > 0 {
			traffic[point.Date] = [2]int64{point.RxBytes, point.TxBytes}
		}
	}
	for i := range points {
		if bytes, ok := traffic[points[i].Date]; ok {
			points[i].RxBytes = bytes[0]
			points[i].TxBytes = bytes[1]
		}
	}
	return points
}

func hourlyStatsTotal(buckets []monitor.HourlyBucketStats) int64 {
	var total int64
	for _, b := range buckets {
		total += b.RequestCount
	}
	return total
}

func statusStatsTotal(buckets []monitor.StatusBucketStats) int64 {
	var total int64
	for _, b := range buckets {
		total += b.RequestCount
	}
	return total
}

func (s *Server) fillUsageUserBreakdown(rangeStats *monitor.PeriodStatsSnapshot, logs []monitor.RequestLog, from, to time.Time) {
	if rangeStats == nil || len(logs) == 0 {
		return
	}
	keyToUser := make(map[string]string)
	for _, key := range s.router.State().APIKeys {
		keyToUser[key.ID] = ownerUserIDForStats(key.OwnerUserID)
	}
	byUser := make(map[string]*monitor.UserDayStats)
	for _, log := range logs {
		if (!from.IsZero() && log.Time.Before(from)) || (!to.IsZero() && !log.Time.Before(to)) {
			continue
		}
		userID := keyToUser[log.APIKeyID]
		if userID == "" {
			userID = legacyAdminUserID
		}
		stats, ok := byUser[userID]
		if !ok {
			stats = &monitor.UserDayStats{UserID: userID}
			byUser[userID] = stats
		}
		stats.RequestCount++
		stats.InputTokens += log.InputTokens
		stats.OutputTokens += log.OutputTokens
		stats.CacheTokens += log.CacheTokens
		stats.CacheWriteTokens += log.CacheWriteTokens
		stats.CostMicros += log.CostMicros
		if log.Priced {
			stats.PricedCount++
		}
	}
	out := make([]monitor.UserDayStats, 0, len(byUser))
	for _, stats := range byUser {
		out = append(out, *stats)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RequestCount > out[j].RequestCount
	})
	rangeStats.ByUser = out
}

// fillUsageDownstreamGatewayNames resolves stable peer gateway ids to the
// downstream account's current display name. One ListUsers call serves the
// whole snapshot; request logs themselves deliberately store only the stable id.
func (s *Server) fillUsageDownstreamGatewayNames(snapshot *monitor.UsageStatsSnapshot) {
	if snapshot == nil || s.userStore == nil {
		return
	}
	if len(snapshot.Today.ByDownstreamGateway) == 0 && len(snapshot.Month.ByDownstreamGateway) == 0 &&
		(snapshot.Range == nil || len(snapshot.Range.ByDownstreamGateway) == 0) {
		return
	}
	users, err := s.userStore.ListUsers()
	if err != nil {
		return
	}
	names := make(map[string]string)
	for _, user := range users {
		if !user.IsPeer() {
			continue
		}
		gatewayID := strings.TrimSpace(user.GatewayID)
		if gatewayID == "" {
			continue
		}
		names[gatewayID] = firstNonEmpty(user.DisplayName, user.Username, gatewayID)
	}
	fill := func(rows []monitor.DownstreamGatewayDayStats) {
		for i := range rows {
			if name := names[strings.TrimSpace(rows[i].GatewayID)]; name != "" {
				rows[i].GatewayName = name
			}
		}
	}
	fill(snapshot.Today.ByDownstreamGateway)
	fill(snapshot.Month.ByDownstreamGateway)
	if snapshot.Range != nil {
		fill(snapshot.Range.ByDownstreamGateway)
	}
}

// fillUsageUserNames resolves display names for every byUser breakdown in the
// snapshot. IDs are stable; names are resolved fresh so user renames reflect
// immediately (subject only to the short response cache).
func (s *Server) fillUsageUserNames(snapshot *monitor.UsageStatsSnapshot) {
	if snapshot == nil {
		return
	}
	snapshot.Today.ByUser = s.fillUserNames(snapshot.Today.ByUser)
	snapshot.Month.ByUser = s.fillUserNames(snapshot.Month.ByUser)
	if snapshot.Range != nil {
		snapshot.Range.ByUser = s.fillUserNames(snapshot.Range.ByUser)
	}
}

// loadRequestLogsForStats pages through persisted request logs up to limit.
// QueryRequestLogs caps pageSize at 500, so a single ListRequestLogs(5000) call
// would silently truncate and drop older alias-form model rows from rankings.
func (s *Server) loadRequestLogsForStats(from, to time.Time, limit int) []monitor.RequestLog {
	if s.requestLogStore == nil {
		return nil
	}
	// 与 handleLogs 同理：先把攒批中的成功请求落库，统计才不会少算刚发生的请求。
	s.FlushRequestLogs()
	if limit <= 0 {
		limit = 5000
	}
	out := make([]monitor.RequestLog, 0, limit)
	pageSize := 500
	for page := 1; len(out) < limit; page++ {
		result, err := s.requestLogStore.QueryRequestLogs(monitor.RequestLogQuery{
			From: from, To: to, Page: page, PageSize: pageSize, Status: "all",
		})
		if err != nil || len(result.Items) == 0 {
			break
		}
		out = append(out, result.Items...)
		if len(result.Items) < pageSize || len(out) >= result.Total {
			break
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (s *Server) loadRequestLogsForStatsForKeys(from, to time.Time, keyIDs []string, limit int) []monitor.RequestLog {
	if s.requestLogStore == nil || len(keyIDs) == 0 {
		return nil
	}
	s.FlushRequestLogs()
	if limit <= 0 {
		limit = 5000
	}
	out := make([]monitor.RequestLog, 0, limit)
	pageSize := 500
	for page := 1; len(out) < limit; page++ {
		result, err := s.requestLogStore.QueryRequestLogs(monitor.RequestLogQuery{
			From: from, To: to, Page: page, PageSize: pageSize, Status: "all", APIKeyIDs: keyIDs,
		})
		if err != nil || len(result.Items) == 0 {
			break
		}
		out = append(out, result.Items...)
		if len(result.Items) < pageSize || len(out) >= result.Total {
			break
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// usageBucketSchema* gate a one-time backfill when the daily-bucket dimensions
// change (e.g. adding the output_protocol bucket). Existing deployments' buckets
// lack new dimensions; bumping the version forces exactly one rebuild-from-logs
// after upgrade, then the fast bucket-load path resumes on later startups.
const (
	usageBucketSchemaSettingKey = "usageDailyBucketSchema"
	usageBucketSchemaVersion    = "4" // v4 adds downstream-gateway dimensions; v3 added per-key dimensions and timing/status
)

// RebuildUsageStats loads persisted daily aggregates (kept permanently), or
// backfills key dimensions from complete retained log days on schema upgrades.
// Permanent totals are never replaced with a truncated request-log sample.
func (s *Server) RebuildUsageStats() {
	retention := s.RequestLogRetentionDays()
	cutoff := time.Now().AddDate(0, 0, -retention)
	// The retention boundary may already be a partial day. Rebuild only whole
	// retained days so upgrading cannot replace a permanent day with partial logs.
	logSince := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, cutoff.Location()).AddDate(0, 0, 1)

	settings, _ := s.usageDailyStore.(interface {
		Setting(string) string
		SetSetting(string, string) error
	})
	needsBackfill := settings != nil && settings.Setting(usageBucketSchemaSettingKey) != usageBucketSchemaVersion

	if s.usageDailyStore != nil {
		days, last, err := s.usageDailyStore.LoadUsageSince(time.Time{})
		if err != nil {
			s.logs.AddApp("warn", "load usage daily aggregates failed", err.Error())
			return
		} else if !needsBackfill && len(days) > 0 {
			s.logs.ResetUsageStats()
			s.logs.BootstrapUsageDays(days, last)
			return
		}
		// Read all source logs before any database mutation. Retained logs may
		// lack bytes or old requests, so existing permanent totals stay authoritative.
		if len(days) == 0 {
			logSince = time.Time{}
		}
		deltas, replayErr := s.rebuildUsageStatsFromLogs(logSince, monitor.NewStore())
		backfill, ok := s.usageDailyStore.(interface {
			BackfillUsageKeyDimensions(time.Time, []monitor.UsagePersistDelta) error
		})
		if replayErr != nil || !ok {
			s.logs.ResetUsageStats()
			s.logs.BootstrapUsageDays(days, last)
			s.logs.AddApp("warn", "usage dimension backfill unavailable", fmt.Sprint(replayErr))
			return
		}
		if err := backfill.BackfillUsageKeyDimensions(logSince, deltas); err != nil {
			s.logs.ResetUsageStats()
			s.logs.BootstrapUsageDays(days, last)
			s.logs.AddApp("warn", "usage dimension backfill failed", err.Error())
			return
		}
		preserved, preservedLast, loadErr := s.usageDailyStore.LoadUsageSince(time.Time{})
		if loadErr != nil {
			s.logs.AddApp("warn", "reload usage daily aggregates failed", loadErr.Error())
			return
		}
		s.logs.ResetUsageStats()
		s.logs.BootstrapUsageDays(preserved, preservedLast)
		if settings != nil {
			if err := settings.SetSetting(usageBucketSchemaSettingKey, usageBucketSchemaVersion); err != nil {
				s.logs.AddApp("warn", "persist usage bucket schema version failed", err.Error())
			}
		}
		return
	}

	s.logs.ResetUsageStats()
	if _, err := s.rebuildUsageStatsFromLogs(time.Time{}, s.logs); err != nil {
		s.logs.AddApp("warn", "rebuild usage from logs failed", err.Error())
	}
}

func (s *Server) rebuildUsageStatsFromLogs(since time.Time, target *monitor.Store) ([]monitor.UsagePersistDelta, error) {
	var deltas []monitor.UsagePersistDelta
	state := s.router.State()
	keysByID := make(map[string]domain.APIKey, len(state.APIKeys))
	keysByName := make(map[string]domain.APIKey, len(state.APIKeys))
	for _, key := range state.APIKeys {
		keysByID[key.ID] = key
		if name := strings.TrimSpace(key.Name); name != "" {
			keysByName[strings.ToLower(name)] = key
		}
	}
	replay := func(log monitor.RequestLog) {
		if !since.IsZero() && log.Time.Before(since) {
			return
		}
		resolved := resolveRequestLogModelForUsage(s.router, log, keysByID, keysByName)
		usageUserID := ""
		if key, ok := lookupLogAPIKey(log, keysByID, keysByName); ok {
			usageUserID = ownerUserIDForStats(key.OwnerUserID)
		}
		// A replayed row carries its own cost when it was priced at the time; a
		// row written before the ledger existed is priced now, from the token
		// counts the log still holds. Nothing invents a cost for a model with no
		// price row.
		costMicros, priced := log.CostMicros, log.Priced
		if !priced {
			costMicros, priced = s.costOf(log.ProviderID, resolved, TokenUsage{
				InputTokens:      log.InputTokens,
				OutputTokens:     log.OutputTokens,
				CacheTokens:      log.CacheTokens,
				CacheWriteTokens: log.CacheWriteTokens,
			})
		}
		deltas = append(deltas, target.ApplyUsageEventSync(monitor.UsageEvent{
			Time:           log.Time,
			APIKeyID:       log.APIKeyID,
			APIKeyName:     log.APIKeyName,
			UserID:         usageUserID,
			ProviderID:     log.ProviderID,
			Model:          resolved,
			OutputProtocol: monitor.OutputProtocolFromFlow(log.ProtocolFlow),
			Status:         log.Status,
			InputTokens:    log.InputTokens,
			OutputTokens:   log.OutputTokens,
			CacheTokens:    log.CacheTokens,
			LatencyMs:      log.LatencyMillis,
			TTFTMs:         log.TTFTMillis,
			// The stored row is the authority on cache-write tokens; without it the
			// day would silently lose the most expensive tier.
			CacheWriteTokens: log.CacheWriteTokens,
			CostMicros:       costMicros,
			Priced:           priced,
			PeerGatewayID:    log.PeerGatewayID,
			PeerDirection:    log.PeerDirection,
		}))
	}
	if s.requestLogStore == nil {
		for _, log := range s.logs.List(memoryLogCap) {
			replay(log)
		}
		return deltas, nil
	}
	s.FlushRequestLogs()
	until := time.Now().AddDate(0, 0, 1)
	for page := 1; ; page++ {
		result, err := s.requestLogStore.QueryRequestLogs(monitor.RequestLogQuery{From: since, To: until, Page: page, PageSize: 500, Status: "all"})
		if err != nil {
			return nil, err
		}
		for _, log := range result.Items {
			replay(log)
		}
		if len(result.Items) < 500 || page*500 >= result.Total {
			break
		}
	}
	return deltas, nil
}

const memoryLogCap = 50000

// requestStatsLogLimit bounds the request-log scan used to complete stats for
// ranges inside the retention window. Hitting the cap means the scan was
// truncated, so totals must keep using the permanent daily buckets.
const requestStatsLogLimit = 5000

// 请求/响应体日志截断上限（字节）。
//   - 2xx 正常请求：只留精简体，够定位模型/参数即可，避免正常流量把库撑大。
//   - 非 2xx 错误请求：放大上限，尽量保留完整请求/响应体，方便事后排查
//     （如 thinking 签名 400、上游报错等需要看到完整上下文）。
//
// SQLite TEXT 列本身无硬长度限制，这里的上限用于防止单条日志异常超长
// （如超大 base64 图片/附件）拖垮内存缓存与 DB 写入。
const (
	logBodyCapOK    = 128 * 1024 // 2xx 请求体上限：128 KiB（满足日常长提示词与对话审计）
	logBodyCapError = 256 * 1024 // 非 2xx 请求体/响应体上限：256 KiB
)

// statusClientClosedRequest is nginx's 499 (Client Closed Request): the
// downstream client disconnected/aborted before the (often slow, reasoning)
// upstream finished, so the request context was canceled. This is a client
// action, not a gateway/upstream fault — recording it as 499 (not 502) and
// logging at info keeps the error log from being flooded with benign
// cancellations.
const statusClientClosedRequest = 499

// isClientCanceled reports whether a request-flow error is due to the downstream
// client going away. net/http cancels the request context when the underlying
// connection closes; since upstream calls use r.Context() directly (no
// gateway-imposed deadline), a non-nil r.Context().Err() means the client
// canceled/disconnected. errors.Is covers wrapped context sentinels too.
func isClientCanceled(r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	if ctx := r.Context(); ctx != nil && ctx.Err() != nil {
		return true
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (s *Server) recordRequestLog(started time.Time, matchedKey domain.APIKey, gatewayKeyMatched bool, routeID, providerID, model, action, protocolFlow, path string, status int, usage TokenUsage, requestBody, responseBody []byte) {
	s.recordRequestLogEx(nil, started, matchedKey, gatewayKeyMatched, routeID, providerID, model, action, protocolFlow, path, status, usage, 0, "", "", "", nil, "", requestBody, responseBody)
}

func (s *Server) recordRequestLogFromRequest(r *http.Request, started time.Time, matchedKey domain.APIKey, gatewayKeyMatched bool, routeID, providerID, model, action, protocolFlow, path string, status int, usage TokenUsage, requestBody, responseBody []byte) {
	s.recordRequestLogFromRequestTTFT(r, started, matchedKey, gatewayKeyMatched, routeID, providerID, model, action, protocolFlow, path, status, usage, 0, requestBody, responseBody)
}

func (s *Server) recordRequestLogFromRequestTTFT(r *http.Request, started time.Time, matchedKey domain.APIKey, gatewayKeyMatched bool, routeID, providerID, model, action, protocolFlow, path string, status int, usage TokenUsage, ttftMs int64, requestBody, responseBody []byte) {
	clientHost := ""
	clientIP := ""
	accessSource := monitor.AccessSourceLocal
	var timing *requestTiming
	if r != nil {
		clientHost = requestClientHost(r)
		clientIP = requestClientIP(r)
		accessSource = classifyAccessSource(clientHost, s.router.State().PublicAccess.PublicBaseURL)
		if strings.HasPrefix(matchedKey.Name, selfcheckKeyPrefix) {
			// CLI 深检是拿临时密钥打真实数据面的，不标出来就会把一轮自检
			// 记成账号自己的失败与用量。临时密钥名由网关生成，外部客户端
			// 伪造不了这个前缀。
			accessSource = monitor.AccessSourceMonitorProbe
		}
		timing = requestTimingFrom(r.Context())
	}
	s.recordRequestLogEx(r, started, matchedKey, gatewayKeyMatched, routeID, providerID, model, action, protocolFlow, path, status, usage, ttftMs, clientHost, clientIP, accessSource, timing, reasoningEffortFrom(r.Context()), requestBody, responseBody)
}

// recordRequestLogEx stores one request. `reasoning` is the depth the gateway
// injected, if any; when it is empty the label is read off the request body so a
// client-specified effort still lands in the log.
func (s *Server) recordRequestLogEx(r *http.Request, started time.Time, matchedKey domain.APIKey, gatewayKeyMatched bool, routeID, providerID, model, action, protocolFlow, path string, status int, usage TokenUsage, ttftMs int64, clientHost, clientIP, accessSource string, timing *requestTiming, reasoning string, requestBody, responseBody []byte) {
	if reasoning == "" {
		reasoning = requestReasoningLabel(requestBody)
	}
	responseErrorMessage := extractResponseErrorMessage(responseBody)
	treatAsFailure := status < 200 || status >= 300 || responseErrorMessage != ""
	// 失败回包不带 usage，按正文长度估一个数会把 200KB 的 429 记成几十万 token
	// 并计费，而上游其实一分钱没收。只有真正调用到模型的失败（客户端中途断开
	// 的流）才保留上游报的 usage。
	if usage.InputTokens == 0 && usage.OutputTokens == 0 && !treatAsFailure {
		usage = EstimateTokenUsage(requestBody, responseBody)
	}
	latency := time.Since(started).Milliseconds()
	if ttftMs <= 0 {
		// Non-stream (or unknown TTFT): treat total latency as a coarse TTFT.
		ttftMs = latency
	}
	prepMs, preUpstreamMs, upstreamTtfbMs, gatewayOverheadMs, convertOutMs, postMs, timingFlags := timing.snapshot(ttftMs, latency)
	logSuccessBodies := s.Log2xxBodies()
	requestBodyCap := 0
	switch {
	case treatAsFailure:
		requestBodyCap = logBodyCapError
	case logSuccessBodies:
		requestBodyCap = logBodyCapOK
	}
	// Cost is priced here, at the moment the request finished: editing a price
	// later must not rewrite what a past request cost.
	costMicros, priced := s.costOf(providerID, model, usage)
	entry := monitor.RequestLog{
		Time:                  started,
		Analysis:              captureRequestAnalysis(r, matchedKey.ID, requestBody),
		RouteID:               routeID,
		ProviderID:            providerID,
		Model:                 model,
		ReasoningEffort:       reasoning,
		Action:                action,
		ProtocolFlow:          protocolFlow,
		Path:                  path,
		Status:                status,
		InputTokens:           usage.InputTokens,
		OutputTokens:          usage.OutputTokens,
		CacheTokens:           usage.CacheTokens,
		CacheWriteTokens:      usage.CacheWriteTokens,
		CostMicros:            costMicros,
		Priced:                priced,
		LatencyMillis:         latency,
		TTFTMillis:            ttftMs,
		PrepMillis:            prepMs,
		PreUpstreamMillis:     preUpstreamMs,
		UpstreamTTFBMillis:    upstreamTtfbMs,
		GatewayOverheadMillis: gatewayOverheadMs,
		ConvertOutMillis:      convertOutMs,
		PostMillis:            postMs,
		TimingFlags:           timingFlags,
		ClientHost:            clientHost,
		ClientIP:              clientIP,
		AccessSource:          accessSource,
		// 落库前脱敏：请求正文里的 api_key / token 等字段绝不能经管理界面外泄。
		RequestBody: truncateForLog(scrubSensitiveRequestBody(requestBody), requestBodyCap),
	}
	// Attach what the scheduler did to the same JSON blob the context stats
	// already ride in, so a request log can answer "why this account".
	// r is nil on the synthetic log paths (retention backfill), which have no
	// scheduler to report.
	if r != nil {
		scheduleTraceOf(r.Context()).applyTo(&entry.Analysis)
		// 父子透传的位置标记：纯本地请求是零值，applyPeerTrace 直接返回，
		// 所以这一段不会改变既有日志的任何一个字段。
		applyPeerTrace(&entry, peerTraceFor(r, timing))
	}
	if gatewayKeyMatched {
		entry.APIKeyID = matchedKey.ID
		entry.APIKeyName = matchedKey.Name
	} else if strings.HasPrefix(action, "test_") {
		entry.APIKeyName = "Route Test"
	}
	if status >= 400 || responseErrorMessage != "" {
		// 上游常把凭据回显在错误里（"Incorrect API key provided: sk-…"），工具
		// 结果也会带回下游配置里的 key；落库前按与请求正文相同的规则脱敏。
		// 摘要（ErrorDescription）经管理界面直接展示，同样要抹。
		entry.ResponseBody = truncateForLog(scrubSensitiveRequestBody(responseBody), logBodyCapError)
		entry.ErrorDescription = scrubSensitiveMessage(responseErrorMessage)
		if entry.ErrorDescription == "" && status >= 400 {
			entry.ErrorDescription = scrubSensitiveMessage(summarizeUpstreamHTTPError(status, responseBody))
		}
	} else if logSuccessBodies {
		entry.ResponseBody = truncateForLog(scrubSensitiveRequestBody(responseBody), logBodyCapOK)
	}
	s.logs.Add(entry)
	// Live tail: only provider/model/status/timing leave this function; the
	// bodies stay in the request log.
	s.publishMonitorTraffic(entry)
	usageUserID := ""
	if gatewayKeyMatched {
		usageUserID = ownerUserIDForStats(matchedKey.OwnerUserID)
	}
	s.logs.EnqueueUsage(monitor.UsageEvent{
		Time:           started,
		APIKeyID:       entry.APIKeyID,
		APIKeyName:     entry.APIKeyName,
		UserID:         usageUserID,
		ProviderID:     providerID,
		Model:          model,
		OutputProtocol: monitor.OutputProtocolFromFlow(protocolFlow),
		Status:         status,
		InputTokens:    usage.InputTokens,
		OutputTokens:   usage.OutputTokens,
		CacheTokens:    usage.CacheTokens,
		// The daily aggregate needs the cache-write half and the cost separately:
		// both are billed at their own rate and neither can be recovered from
		// InputTokens alone.
		CacheWriteTokens: usage.CacheWriteTokens,
		CostMicros:       costMicros,
		Priced:           priced,
		// 链路标记来自刚写好的请求日志：只有 ingress 才会进入下游网关统计。
		PeerGatewayID: entry.PeerGatewayID,
		PeerDirection: entry.PeerDirection,
		// Forwarded payload sizes. For streaming responses the logged body is
		// capped at passThroughLogBufferMax, so prefer the true streamed count
		// recorded during the copy; fall back to the buffered length otherwise.
		RxBytes:   int64(len(requestBody)),
		TxBytes:   max(timing.streamedTx(), int64(len(responseBody))),
		LatencyMs: latency,
		TTFTMs:    ttftMs,
	})
	s.requestCounter.Add(1)
	if s.requestLogStore != nil {
		if s.requestLogBatcher != nil {
			s.requestLogBatcher.Append(entry, treatAsFailure)
		} else if err := s.requestLogStore.AppendRequestLogWithRetention(entry, s.RequestLogRetentionDays()); err != nil {
			s.logs.AddApp("warn", "failed to persist request log", err.Error())
		}
	}
}

// RecordRequestLogForTest records one finished request the way a handler does on
// its way out, so lifecycle tests outside this package (internal/app) can play
// "a request finishes now" without driving a full protocol flow.
func (s *Server) RecordRequestLogForTest(r *http.Request, started time.Time, key domain.APIKey, routeID, providerID, model, path string, status int, usage TokenUsage) {
	s.recordRequestLogFromRequestTTFT(r, started, key, true, routeID, providerID, model, "test", "openai", path, status, usage, 0, nil, nil)
}

// FlushRequestLogs persists any batched successful request logs immediately.
// Called before reading the log page (so the console never misses a recent
// request) and on graceful shutdown.
func (s *Server) FlushRequestLogs() {
	s.requestLogBatcher.Flush()
}

// CloseRequestLogBatcher flushes and stops the background batcher.
func (s *Server) CloseRequestLogBatcher() {
	s.requestLogBatcher.Close()
}

// FlushDeferredWrites lands everything the gateway persists lazily, then stops
// the background flushers. Call on graceful shutdown, before closing the DB.
//
// Every deferred writer must be represented here — each one buys lower disk
// wear by holding data in memory, and that trade is only sound if a clean
// shutdown still writes it out.
//
// Each step is bounded by ctx and, when it overruns, reported and abandoned
// rather than waited on forever: every one of these writes queues behind the
// single SQLite writer connection, which a long maintenance statement can hold
// for minutes. Unlanded rows cost dashboard numbers; an unbounded wait costs the
// restart itself, since systemd answers TimeoutStopSec with SIGKILL and that
// discards the whole in-memory backlog instead of just the last batch.
func (s *Server) FlushDeferredWrites(ctx context.Context) error {
	// Batched success rows (up to requestLogBatchInterval of them).
	if err := s.requestLogBatcher.CloseContext(ctx); err != nil {
		slog.Warn("deferred write did not land before the shutdown deadline", "writer", "request_logs", "error", err)
		return err
	}
	// Queued usage deltas (up to one batch window).
	if err := s.logs.CloseUsageWorkerContext(ctx); err != nil {
		slog.Warn("deferred write did not land before the shutdown deadline", "writer", "usage_deltas", "error", err)
		return err
	}
	// last_used_at values skipped by the 5-min-per-key throttle.
	if err := s.apiKeyToucher.CloseContext(ctx); err != nil {
		slog.Warn("deferred write did not land before the shutdown deadline", "writer", "api_key_last_used", "error", err)
		return err
	}
	// Host metric samples buffered for the once-a-minute flush.
	if s.hostMetrics != nil {
		s.hostMetrics.Stop()
	}
	// last_active_at values skipped by the 5-min-per-user throttle. The flush
	// goroutine's ctx is never cancelled, so force a flush directly here — in a
	// goroutine we are willing to walk away from, for the same reason as above.
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.flushUserActivity(true)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		slog.Warn("deferred write did not land before the shutdown deadline", "writer", "user_last_active", "error", ctx.Err())
		return ctx.Err()
	}
}

func truncateForLog(data []byte, limit int) string {
	if len(data) == 0 {
		return ""
	}
	// limit<=0 表示「这类请求不记正文」（见 recordRequestLogEx 里的 2xx 分支），
	// 必须返回空串而不是只剩一个截断标记的字符串。
	if limit <= 0 {
		return ""
	}
	if len(data) <= limit {
		return string(data)
	}
	// 按 UTF-8 边界回退，避免从多字节字符中间切断产生非法序列
	// （历史上曾导致下游 JSON/UTF-8 解析报错）。
	cut := limit
	for cut > 0 && !utf8.RuneStart(data[cut]) {
		cut--
	}
	return string(data[:cut]) + "…(truncated)"
}

func (s *Server) handleAppLogs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"level": s.logs.Level(), "logs": s.logs.ListApp(200)})
}

func (s *Server) handleSetLogLevel(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Level string `json:"level"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid log level json: "+err.Error())
		return
	}
	level := s.logs.SetLevel(payload.Level)
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "log level changed", level)
	writeJSON(w, http.StatusOK, map[string]any{"level": level})
}

func (s *Server) handleSetRequestLogRetention(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Days int `json:"days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid retention json: "+err.Error())
		return
	}
	days := s.SetRequestLogRetentionDays(payload.Days)
	s.router.SetRequestLogRetentionDays(days)
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	if s.requestLogStore != nil {
		_ = s.requestLogStore.PruneRequestLogs(days)
	}
	// 每日用量汇总永久保留，不随请求明细保留天数一起裁剪。
	s.logs.AddApp("info", "request log retention updated", fmt.Sprintf("days=%d", days))
	writeJSON(w, http.StatusOK, map[string]any{"requestLogRetentionDays": days})
}

// handleSetLog2xxBodies toggles whether successful requests persist their
// request/response bodies into request_logs. Off by default because those
// bodies dominate per-request disk writes; failures always keep theirs.
func (s *Server) handleSetLog2xxBodies(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid log2xxBodies json: "+err.Error())
		return
	}
	if payload.Enabled == nil {
		writeOpenAIError(w, http.StatusBadRequest, "enabled is required")
		return
	}
	s.SetLog2xxBodies(*payload.Enabled)
	s.router.SetLog2xxBodies(*payload.Enabled)
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "2xx body logging updated", fmt.Sprintf("log2xxBodies=%v", *payload.Enabled))
	writeJSON(w, http.StatusOK, map[string]any{"log2xxBodies": s.Log2xxBodies()})
}

// handleSetUsageBatch tunes how the async usage worker batches its commits.
// Takes effect on the next gateway start: the worker reads the thresholds once
// when it launches, so changing them live would race with it.
// handleSetHostMetrics toggles the machine-status sampler. The setting is
// persisted with the rest of the gateway state, so a restart keeps the choice.
func (s *Server) handleSetHostMetrics(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Enabled == nil {
		writeOpenAIError(w, http.StatusBadRequest, `body must be {"enabled": true|false}`)
		return
	}
	enabled := *payload.Enabled
	previous := s.router.HostMetricsDisabled()
	s.router.SetHostMetricsDisabled(!enabled)
	if err := s.saveState(); err != nil {
		s.router.SetHostMetricsDisabled(previous)
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save host metrics setting: "+err.Error())
		return
	}
	s.hostSampler().SetEnabled(enabled)
	if s.logs != nil {
		s.logs.AddApp("info", "host metrics setting updated", fmt.Sprintf("enabled=%v", enabled))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":             enabled,
		"hostMetricsDisabled": !enabled,
	})
}

func (s *Server) handleSetUsageBatch(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		MaxSize        *int `json:"maxSize"`
		MaxWaitSeconds *int `json:"maxWaitSeconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid usage batch json: "+err.Error())
		return
	}
	if payload.MaxSize == nil && payload.MaxWaitSeconds == nil {
		writeOpenAIError(w, http.StatusBadRequest, "at least one of maxSize/maxWaitSeconds is required")
		return
	}
	state := s.router.State()
	maxSize := state.UsageBatchMaxSize
	maxWaitSeconds := state.UsageBatchMaxWaitSeconds
	if payload.MaxSize != nil {
		maxSize = *payload.MaxSize
	}
	if payload.MaxWaitSeconds != nil {
		maxWaitSeconds = *payload.MaxWaitSeconds
	}
	// Reuse monitor's clamping so the stored value matches what the worker will
	// actually run with (and 0 keeps meaning "use the default").
	normalizedSize, normalizedWait := monitor.NormalizeUsageBatchConfig(maxSize, time.Duration(maxWaitSeconds)*time.Second)
	maxSize = normalizedSize
	maxWaitSeconds = int(normalizedWait / time.Second)

	s.router.SetUsageBatchConfig(maxSize, maxWaitSeconds)
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "usage batch config updated", fmt.Sprintf("maxSize=%d maxWaitSeconds=%d (restart to apply)", maxSize, maxWaitSeconds))
	writeJSON(w, http.StatusOK, map[string]any{
		"usageBatchMaxSize":        maxSize,
		"usageBatchMaxWaitSeconds": maxWaitSeconds,
	})
}

func (s *Server) handleSetWebExposed(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid webExposed json: "+err.Error())
		return
	}
	if payload.Enabled == nil {
		writeOpenAIError(w, http.StatusBadRequest, "enabled is required")
		return
	}
	enabled := *payload.Enabled
	if s.webExposedChange != nil {
		if err := s.webExposedChange(enabled); err != nil {
			// Still report current preference; rebind may require restart when GATEWAY_ADDR is set.
			s.router.SetWebExposed(enabled)
			_ = s.saveState()
			writeJSON(w, http.StatusOK, map[string]any{
				"webExposed": enabled,
				"warning":    err.Error(),
			})
			return
		}
	} else {
		s.router.SetWebExposed(enabled)
		if err := s.saveState(); err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
			return
		}
	}
	s.logs.AddApp("info", "web exposure updated", fmt.Sprintf("webExposed=%v", enabled))
	writeJSON(w, http.StatusOK, map[string]any{"webExposed": s.router.WebExposed()})
}

func (s *Server) handleUpdatePublicAccess(w http.ResponseWriter, r *http.Request) {
	var settings domain.PublicAccessSettings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid public access json: "+err.Error())
		return
	}
	// 请求的"角色"完全由 Host 决定，所以绑定的域名必须是干净的裸主机名：
	// 带路径/端口/通配符会让判定含糊，两个角色用同一个名字则根本分不开。
	if msg := PublicHostError(settings.CustomDomain, settings.UIDomain); msg != "" {
		writeOpenAIError(w, http.StatusBadRequest, msg)
		return
	}
	updated := s.router.UpdatePublicAccess(settings)
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "public access settings updated", fmt.Sprintf("mode=%s status=%s domain=%s", updated.Mode, updated.Status, updated.CustomDomain))
	writeJSON(w, http.StatusOK, s.withTunnelRuntime(updated))
}

func (s *Server) handleUpdateEndpoint(w http.ResponseWriter, r *http.Request) {
	endpointID := r.PathValue("id")
	var payload struct {
		StreamEnabled *bool `json:"streamEnabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid endpoint json: "+err.Error())
		return
	}
	if payload.StreamEnabled == nil {
		writeOpenAIError(w, http.StatusBadRequest, "streamEnabled is required")
		return
	}
	updated, err := s.router.UpdateEndpointStreamEnabled(endpointID, *payload.StreamEnabled)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "endpoint stream setting updated", fmt.Sprintf("endpoint=%s streamEnabled=%v", updated.ID, updated.StreamEnabled))
	writeJSON(w, http.StatusOK, updated)
}

// jsonHasKey reports whether the top-level JSON object in body contains key.
// Used to distinguish an explicitly-provided false from an omitted field.
func jsonHasKey(body []byte, key string) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return false
	}
	_, ok := raw[key]
	return ok
}

// rejectIfStreamDisabledForKey returns true when the client asked for streaming
// but the matched API key has streamEnabled=false. Streaming is bound to the
// API key; requests without a matched key (e.g. internal tests) are allowed.
func (s *Server) rejectIfStreamDisabledForKey(w http.ResponseWriter, keyMatched bool, key domain.APIKey, stream bool, clientProtocol domain.Protocol) bool {
	if !stream || !keyMatched || key.StreamEnabled {
		return false
	}
	message := "该 API Key 已关闭流式响应；请将 stream 设为 false，或在 API Key 设置中开启「允许流式响应」"
	switch clientProtocol {
	case domain.ProtocolClaude:
		writeClaudeError(w, http.StatusBadRequest, message)
	default:
		writeOpenAIError(w, http.StatusBadRequest, message)
	}
	return true
}

// handlePublicStatus returns the persisted public-access settings plus the live
// cloudflared tunnel runtime state.
func (s *Server) handlePublicStatus(w http.ResponseWriter, r *http.Request) {
	settings := s.router.State().PublicAccess
	writeJSON(w, http.StatusOK, s.withTunnelRuntime(settings))
}

// handlePublicStart starts a cloudflared tunnel using the current (or provided)
// public-access settings and returns the updated status.
func (s *Server) handlePublicStart(w http.ResponseWriter, r *http.Request) {
	if s.tunnels == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "tunnel manager is not configured")
		return
	}
	// Optionally accept a settings body to update-then-start in one call.
	if r.Body != nil {
		var incoming domain.PublicAccessSettings
		if err := json.NewDecoder(r.Body).Decode(&incoming); err == nil && (incoming.Mode != "" || incoming.CustomDomain != "" || incoming.Enabled) {
			incoming.Enabled = true
			s.router.UpdatePublicAccess(incoming)
			_ = s.saveState()
		}
	}

	settings := s.ensureSplitCustomDomains(r.Context())
	tunnelSettings := s.tunnelSettingsFromPublicAccess(settings)

	state, err := s.tunnels.Start(tunnelSettings)
	s.applyTunnelURL(state)
	settings = s.router.State().PublicAccess
	if err != nil {
		s.logs.AddApp("warn", "public access start failed", err.Error())
		writeJSON(w, http.StatusOK, s.withTunnelRuntime(settings))
		return
	}
	s.logs.AddApp("info", "public access started", fmt.Sprintf("mode=%s url=%s", state.Mode, state.PublicURL))
	writeJSON(w, http.StatusOK, s.withTunnelRuntime(settings))
}

// RestorePublicAccess restarts the cloudflared tunnel when persisted settings
// indicate public access was left enabled (e.g. after a gateway restart).
func (s *Server) RestorePublicAccess() {
	if s.tunnels == nil {
		return
	}
	settings := s.router.State().PublicAccess
	if !settings.Enabled {
		return
	}
	if settings.Mode == domain.PublicAccessModeCustomDomain {
		apiDomain := ""
		uiDomain := ""
		if settings.ExposeAPI {
			apiDomain = cleanPublicHost(settings.CustomDomain)
		}
		if settings.ExposeUI {
			uiDomain = cleanPublicHost(settings.UIDomain)
		}
		if tunnel.CustomDomainConfigReusable(settings.TunnelConfigFile, settings.CredentialsFile, apiDomain, uiDomain, s.localGatewayPort()) {
			slog.Info("public access restore reusing tunnel config", "config", settings.TunnelConfigFile, "api", apiDomain, "ui", uiDomain)
		} else {
			settings = s.ensureSplitCustomDomains(context.Background())
		}
	}
	tunnelSettings := s.tunnelSettingsFromPublicAccess(settings)
	state, err := s.tunnels.Start(tunnelSettings)
	s.applyTunnelURL(state)
	if err != nil {
		s.logs.AddApp("warn", "public access auto-restore failed", err.Error())
		slog.Warn("public access auto-restore failed", "error", err)
		return
	}
	s.logs.AddApp("info", "public access auto-restored", fmt.Sprintf("mode=%s api=%s ui=%s", state.Mode, settings.CustomDomain, settings.UIDomain))
	slog.Info("public access auto-restored", "mode", state.Mode, "api", settings.CustomDomain, "ui", settings.UIDomain)
}

func (s *Server) tunnelSettingsFromPublicAccess(settings domain.PublicAccessSettings) tunnel.Settings {
	apiDomain := ""
	uiDomain := ""
	if settings.Mode == domain.PublicAccessModeCustomDomain {
		if settings.ExposeAPI {
			apiDomain = cleanPublicHost(settings.CustomDomain)
		}
		if settings.ExposeUI {
			uiDomain = cleanPublicHost(settings.UIDomain)
		}
	} else {
		apiDomain = cleanPublicHost(settings.CustomDomain)
		uiDomain = cleanPublicHost(settings.UIDomain)
	}
	return tunnel.Settings{
		Enabled:         true,
		Mode:            tunnelModeFromDomain(settings.Mode),
		CustomDomain:    apiDomain,
		UIDomain:        uiDomain,
		TunnelName:      settings.TunnelName,
		TunnelToken:     settings.TunnelToken,
		CredentialsFile: settings.CredentialsFile,
		ConfigFile:      settings.TunnelConfigFile,
	}
}

// ensureSplitCustomDomains re-provisions the named-tunnel config for the
// currently enabled public surfaces (API and/or UI).
func (s *Server) ensureSplitCustomDomains(ctx context.Context) domain.PublicAccessSettings {
	settings := s.router.State().PublicAccess
	if settings.Mode != domain.PublicAccessModeCustomDomain {
		return settings
	}
	apiDomain := ""
	uiDomain := ""
	if settings.ExposeAPI {
		apiDomain = cleanPublicHost(settings.CustomDomain)
	}
	if settings.ExposeUI {
		uiDomain = cleanPublicHost(settings.UIDomain)
	}
	if apiDomain == "" && uiDomain == "" {
		return settings
	}
	if apiDomain != "" && uiDomain != "" && strings.EqualFold(apiDomain, uiDomain) {
		return settings
	}
	if s.tunnels == nil {
		return settings
	}
	setup := s.tunnels.Cloudflare()
	if !setup.IsAuthorized() {
		return settings
	}
	provisioned, err := setup.Provision(ctx, apiDomain, uiDomain, settings.TunnelName, s.localGatewayPort())
	if err != nil {
		s.logs.AddApp("warn", "custom-domain reprovision skipped", err.Error())
		return settings
	}
	settings.TunnelName = provisioned.TunnelName
	settings.CredentialsFile = provisioned.CredentialsFile
	settings.TunnelConfigFile = provisioned.ConfigFile
	s.router.UpdatePublicAccess(settings)
	_ = s.saveState()
	return s.router.State().PublicAccess
}

// handlePublicStop stops the running cloudflared tunnel.
func (s *Server) handlePublicStop(w http.ResponseWriter, r *http.Request) {
	if s.tunnels == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "tunnel manager is not configured")
		return
	}
	state := s.tunnels.Stop()
	// Mark public access as intentionally stopped so it is not auto-restored on
	// the next gateway restart. Preserve mode/domain/token/config for re-enable.
	current := s.router.State().PublicAccess
	current.Enabled = false
	current.RuntimeURL = ""
	s.router.UpdatePublicAccess(current)
	_ = s.saveState()
	settings := s.router.State().PublicAccess
	s.logs.AddApp("info", "public access stopped", fmt.Sprintf("status=%s", state.Status))
	writeJSON(w, http.StatusOK, s.withTunnelRuntime(settings))
}

// applyTunnelURL records a live tunnel URL into the router settings so that
// endpoint public URLs are derived from it.
func (s *Server) applyTunnelURL(state tunnel.State) {
	if state.PublicURL == "" && state.UIPublicURL == "" {
		return
	}
	current := s.router.State().PublicAccess
	current.Enabled = true
	if state.PublicURL != "" {
		current.RuntimeURL = state.PublicURL
	} else if state.UIPublicURL != "" {
		current.RuntimeURL = state.UIPublicURL
	}
	s.router.UpdatePublicAccess(current)
	_ = s.saveState()
}

// withTunnelRuntime attaches the live tunnel manager snapshot to a settings copy.
func (s *Server) withTunnelRuntime(settings domain.PublicAccessSettings) domain.PublicAccessSettings {
	if s.tunnels == nil {
		return settings
	}
	snap := s.tunnels.Snapshot()
	settings.Tunnel = &domain.TunnelRuntime{
		Status:      string(snap.Status),
		Mode:        string(snap.Mode),
		PublicURL:   snap.PublicURL,
		UIPublicURL: snap.UIPublicURL,
		Message:     snap.Message,
		StartedAt:   snap.StartedAt,
		PID:         snap.PID,
	}
	// Keep persisted status aligned with the live tunnel so UI badges don't
	// stay on "configured_pending_tunnel" after RestorePublicAccess succeeds.
	switch snap.Status {
	case tunnel.StatusRunning:
		settings.Status = "runtime_url_recorded"
		settings.StatusMessage = snap.Message
		settings.PublicBaseURL = ""
		settings.UIPublicBaseURL = ""
		if settings.Mode == domain.PublicAccessModeCustomDomain {
			if settings.ExposeAPI && settings.CustomDomain != "" {
				settings.PublicBaseURL = "https://" + cleanPublicHost(settings.CustomDomain)
			}
			if settings.ExposeUI && settings.UIDomain != "" {
				settings.UIPublicBaseURL = "https://" + cleanPublicHost(settings.UIDomain)
			}
			if settings.PublicBaseURL != "" {
				settings.RuntimeURL = settings.PublicBaseURL
			} else if settings.UIPublicBaseURL != "" {
				settings.RuntimeURL = settings.UIPublicBaseURL
			}
		} else if snap.PublicURL != "" {
			settings.PublicBaseURL = snap.PublicURL
			settings.RuntimeURL = snap.PublicURL
		}
	case tunnel.StatusStarting:
		if settings.Status == "" || settings.Status == "disabled" || settings.Status == "configured_pending_tunnel" {
			settings.Status = "waiting_for_tunnel"
		}
	case tunnel.StatusError:
		settings.Status = "error"
		if snap.Message != "" {
			settings.StatusMessage = snap.Message
		}
	}
	return settings
}

// tunnelModeFromDomain maps the domain public-access mode to a tunnel mode.
func tunnelModeFromDomain(mode domain.PublicAccessMode) tunnel.Mode {
	if mode == domain.PublicAccessModeCustomDomain {
		return tunnel.ModeCustom
	}
	return tunnel.ModeQuick
}

func (s *Server) localGatewayPort() int {
	addr := strings.TrimSpace(s.listenAddr)
	if addr == "" {
		return 18093
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 18093
	}
	parsed, err := strconv.Atoi(port)
	if err != nil || parsed <= 0 {
		return 18093
	}
	return parsed
}

// handleCloudflareLoginStart opens the official Cloudflare tunnel authorization
// page via cloudflared and returns a fallback URL for the browser.
func (s *Server) handleCloudflareLoginStart(w http.ResponseWriter, r *http.Request) {
	if s.tunnels == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "tunnel manager is not configured")
		return
	}
	setup := s.tunnels.Cloudflare()
	if setup.IsAuthorized() {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "connected",
			"loginUrl": tunnel.CloudflareLoginURL(),
		})
		return
	}
	loginURL, err := setup.StartLogin(r.Context())
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "pending",
		"loginUrl": loginURL,
	})
}

// handleCloudflareLoginStatus reports whether cloudflared origin cert exists.
func (s *Server) handleCloudflareLoginStatus(w http.ResponseWriter, r *http.Request) {
	if s.tunnels == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "tunnel manager is not configured")
		return
	}
	authorized := s.tunnels.Cloudflare().IsAuthorized()
	status := "pending"
	if authorized {
		status = "connected"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     status,
		"authorized": authorized,
	})
}

// handleCloudflareZones lists root domains authorized via the local origin cert.
func (s *Server) handleCloudflareZones(w http.ResponseWriter, r *http.Request) {
	if s.tunnels == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "tunnel manager is not configured")
		return
	}
	setup := s.tunnels.Cloudflare()
	if !setup.IsAuthorized() {
		writeJSON(w, http.StatusOK, map[string]any{
			"authorized": false,
			"zones":      []tunnel.CloudflareZone{},
		})
		return
	}
	zones, err := setup.ListZones(r.Context())
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authorized": true,
		"zones":      zones,
	})
}

// handleCloudflareBind provisions a named tunnel after browser login, persists
// the config, and starts the custom-domain tunnel.
func (s *Server) handleCloudflareBind(w http.ResponseWriter, r *http.Request) {
	if s.tunnels == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "tunnel manager is not configured")
		return
	}
	var payload struct {
		CustomDomain string `json:"customDomain"`
		UIDomain     string `json:"uiDomain"`
		ExposeAPI    *bool  `json:"exposeApi"`
		ExposeUI     *bool  `json:"exposeUi"`
		TunnelName   string `json:"tunnelName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	customDomain := cleanPublicHost(payload.CustomDomain)
	uiDomain := cleanPublicHost(payload.UIDomain)
	exposeAPI := payload.ExposeAPI == nil || *payload.ExposeAPI
	exposeUI := payload.ExposeUI == nil || *payload.ExposeUI
	if !exposeAPI {
		customDomain = ""
	}
	if !exposeUI {
		uiDomain = ""
	}
	if exposeAPI && customDomain == "" {
		writeOpenAIError(w, http.StatusBadRequest, "customDomain is required when exposeApi is enabled")
		return
	}
	if exposeUI && uiDomain == "" {
		writeOpenAIError(w, http.StatusBadRequest, "uiDomain is required when exposeUi is enabled")
		return
	}
	if !exposeAPI && !exposeUI {
		writeOpenAIError(w, http.StatusBadRequest, "enable at least one of exposeApi or exposeUi")
		return
	}
	if customDomain != "" && uiDomain != "" && strings.EqualFold(customDomain, uiDomain) {
		writeOpenAIError(w, http.StatusBadRequest, "api domain and ui domain must be different")
		return
	}
	setup := s.tunnels.Cloudflare()
	if !setup.IsAuthorized() {
		writeOpenAIError(w, http.StatusBadRequest, "cloudflare is not authorized yet; complete browser login first")
		return
	}
	provisioned, err := setup.Provision(r.Context(), customDomain, uiDomain, strings.TrimSpace(payload.TunnelName), s.localGatewayPort())
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}

	current := s.router.State().PublicAccess
	current.Enabled = true
	current.Provider = "cloudflare"
	current.Mode = domain.PublicAccessModeCustomDomain
	current.ExposeAPI = exposeAPI
	current.ExposeUI = exposeUI
	if customDomain != "" {
		current.CustomDomain = customDomain
	}
	if uiDomain != "" {
		current.UIDomain = uiDomain
	}
	current.TunnelName = provisioned.TunnelName
	current.CredentialsFile = provisioned.CredentialsFile
	current.TunnelConfigFile = provisioned.ConfigFile
	current.TunnelToken = ""
	s.router.UpdatePublicAccess(current)
	_ = s.saveState()

	settings := s.router.State().PublicAccess
	tunnelSettings := tunnel.Settings{
		Enabled:         true,
		Mode:            tunnel.ModeCustom,
		CustomDomain:    customDomain,
		UIDomain:        uiDomain,
		TunnelName:      provisioned.TunnelName,
		CredentialsFile: provisioned.CredentialsFile,
		ConfigFile:      provisioned.ConfigFile,
	}
	state, err := s.tunnels.Start(tunnelSettings)
	s.applyTunnelURL(state)
	settings = s.router.State().PublicAccess
	if err != nil {
		s.logs.AddApp("warn", "cloudflare bind start failed", err.Error())
		writeJSON(w, http.StatusOK, map[string]any{
			"provisioned":  provisioned,
			"publicAccess": s.withTunnelRuntime(settings),
			"error":        err.Error(),
		})
		return
	}
	s.logs.AddApp("info", "cloudflare domain bound", fmt.Sprintf("api=%s ui=%s url=%s", customDomain, uiDomain, state.PublicURL))
	writeJSON(w, http.StatusOK, map[string]any{
		"provisioned":  provisioned,
		"publicAccess": s.withTunnelRuntime(settings),
	})
}

// deriveUIDomain picks a management hostname that never collides with the API
// hostname. Prefer console.<root>; if the API host is already console.*, use
// admin.<root>; if neither works, return empty so the caller can reject.
func deriveUIDomain(apiDomain string) string {
	apiDomain = cleanPublicHost(apiDomain)
	if apiDomain == "" {
		return ""
	}
	parts := strings.Split(apiDomain, ".")
	if len(parts) < 2 {
		return ""
	}
	root := strings.Join(parts[1:], ".")
	prefix := strings.ToLower(parts[0])
	candidates := []string{"console", "admin", "panel"}
	for _, candidate := range candidates {
		host := candidate + "." + root
		if !strings.EqualFold(host, apiDomain) && !strings.EqualFold(candidate, prefix) {
			return host
		}
	}
	for _, candidate := range candidates {
		host := candidate + "." + root
		if !strings.EqualFold(host, apiDomain) {
			return host
		}
	}
	return ""
}

func (s *Server) handleCreateProvider(w http.ResponseWriter, r *http.Request) {
	var provider domain.Provider
	if err := json.NewDecoder(r.Body).Decode(&provider); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid provider json: "+err.Error())
		return
	}
	// 对端 provider 的地址与凭据是一体的：baseUrl 是父网关根地址，凭据是注册
	// 那一刻签发的 gwpeer- 令牌，两者只能由「网关互联」的注册流程一起写下来。
	// 在这里放行一条手写的 authType=peer 只会得到一条既没有凭据、也找不到门的
	// 记录——它在请求路径上以 401 失败，而看到的人只会以为"上游拒绝"。
	if provider.AuthType == domain.AuthTypePeer {
		writeOpenAIError(w, http.StatusBadRequest, "authType=peer 不能手工创建：请在「网关互联」页注册到上游网关，网关会自动生成这条记录")
		return
	}
	if p := s.requestPrincipal(r); p.scopeOf() != domain.ScopeAll {
		// Restricted users own the providers they create (including clones),
		// while every other account's provider is labelled with its own
		// administrator's account rather than a generic owner.
		provider.OwnerUserID = p.UserID
		// Provider 分组是管理员侧的分类元数据，普通用户永远不能设置。
		provider.GroupID = ""
		// The RTK switch is an admin-only, host-wide capability.
		provider.RTKCompress = false
	} else {
		// Admin-created providers and keys are attributed to the administrator's
		// own account, never left unowned (which the console would label with a
		// generic "管理员" instead of the real username).
		if strings.TrimSpace(provider.OwnerUserID) == "" {
			provider.OwnerUserID = s.adminOwnerID()
		}
		if !p.IsAdmin {
			// ScopeAll 的自定义角色仍不是管理员；分组功能只给真正的管理员权限。
			provider.GroupID = ""
		} else {
			provider.GroupID = strings.TrimSpace(provider.GroupID)
			if !s.providerGroupExists(provider.GroupID) {
				writeOpenAIError(w, http.StatusBadRequest, "provider group not found")
				return
			}
		}
	}
	if provider.RTKCompress {
		if available, _ := s.rtk.status(); !available {
			writeOpenAIError(w, http.StatusConflict, "rtk CLI not found on the gateway host; install rtk or set GATEWAY_RTK_PATH, then retry")
			return
		}
	}
	created, err := s.router.AddProvider(provider)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "provider created", created.ID)
	// 新建之后立刻在后台探一遍它支持哪些协议。人不用点按钮——建完就有了结论，
	// 控制台上直接看到"这台上游支持 Claude / OpenAI 两套"。
	s.autoDetectProviderProtocols(created)
	writeJSON(w, http.StatusCreated, redactProviderForClient(created))
}

func (s *Server) handleExportProviders(w http.ResponseWriter, r *http.Request) {
	ids := ParseProviderExportIDs(r.URL.Query().Get("ids"))
	if r.Method == http.MethodPost {
		var payload struct {
			IDs []string `json:"ids"`
		}
		if r.Body != nil {
			decoder := json.NewDecoder(r.Body)
			if err := decoder.Decode(&payload); err != nil {
				if err != io.EOF {
					writeOpenAIError(w, http.StatusBadRequest, "invalid export json: "+err.Error())
					return
				}
			} else if payload.IDs != nil {
				ids = payload.IDs
			}
		}
	}
	bundle, exportErrors := s.router.ExportProviders(ids)
	if len(ids) > 0 && len(bundle.Providers) == 0 {
		writeOpenAIError(w, http.StatusNotFound, "no matching providers to export")
		return
	}
	s.logs.AddApp("info", "providers exported", fmt.Sprintf("count=%d errors=%d", len(bundle.Providers), len(exportErrors)))
	writeJSON(w, http.StatusOK, map[string]any{
		"version":    bundle.Version,
		"exportedAt": bundle.ExportedAt,
		"providers":  bundle.Providers,
		"errors":     exportErrors,
	})
}

func (s *Server) handleImportProviders(w http.ResponseWriter, r *http.Request) {
	var bundle ProvidersExportBundle
	// Allow unknown fields (e.g. export "errors") so downloaded bundles round-trip cleanly.
	if err := json.NewDecoder(r.Body).Decode(&bundle); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid import json: "+err.Error())
		return
	}
	// 分组不随 Provider 导出；导入到不同网关时，未知 groupId 必须回落到未分组，
	// 不能留下一个筛选栏里永远选不到的幽灵 ID。
	for index := range bundle.Providers {
		if !s.providerGroupExists(bundle.Providers[index].GroupID) {
			bundle.Providers[index].GroupID = ""
		}
	}
	result := s.router.ImportProviders(bundle)
	if len(result.Created) == 0 && len(result.Updated) == 0 && len(result.Errors) > 0 {
		writeJSON(w, http.StatusBadRequest, result)
		return
	}
	if len(result.Created) > 0 || len(result.Updated) > 0 {
		if err := s.saveState(); err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
			return
		}
	}
	s.logs.AddApp("info", "providers imported", fmt.Sprintf("created=%d updated=%d skipped=%d errors=%d", len(result.Created), len(result.Updated), len(result.Skipped), len(result.Errors)))
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	if !s.requireProviderOwnerForUser(w, r, r.PathValue("id")) {
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "failed to read provider json: "+err.Error())
		return
	}
	var provider domain.Provider
	if err := json.Unmarshal(body, &provider); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid provider json: "+err.Error())
		return
	}
	// GroupID 用指针单独解一次：字段缺省表示“这次不改分组”，空字符串才是
	// “移到未分组”。直接看 domain.Provider.GroupID 会把老客户端没传的字段
	// 当成清空操作。
	var groupPatch struct {
		GroupID *string `json:"groupId"`
	}
	if err := json.Unmarshal(body, &groupPatch); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid provider json: "+err.Error())
		return
	}
	// 编辑一张已有的对端记录时，控制台会把 authType=peer 原样回传（见 frontend
	// 的 providerConnectKind）。那不是"创建一个 peer"，只是"还是那条记录"，
	// 所以这里不能一刀切拒绝；真正要拦的是「把一条普通 provider 改成 peer」，
	// 那会得到一条手工伪造的对端门，凭据与地址都对不上。
	if provider.AuthType == domain.AuthTypePeer {
		existing, err := s.router.ProviderByID(r.PathValue("id"))
		if err != nil || existing.AuthType != domain.AuthTypePeer {
			writeOpenAIError(w, http.StatusBadRequest, "只有「网关互联」注册生成的对端记录才能是 authType=peer；请到该页面重新注册，不要手改连接方式")
			return
		}
	}
	// 分组校验必须放在 Provider 更新之前：UpdateProvider 会直接改内存，
	// 如果这里先更新再因无效 groupId 返回 400，就会出现“接口失败但内存已变”。
	groupID := ""
	groupIDSet := groupPatch.GroupID != nil && s.requestPrincipal(r).IsAdmin
	if groupIDSet {
		groupID = strings.TrimSpace(*groupPatch.GroupID)
		if !s.providerGroupExists(groupID) {
			writeOpenAIError(w, http.StatusBadRequest, "provider group not found")
			return
		}
	}
	updated, err := s.router.UpdateProvider(r.PathValue("id"), provider)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if groupIDSet {
		if _, err := s.router.AssignProvidersToGroup([]string{updated.ID}, groupID); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, err.Error())
			return
		}
		updated, _ = s.router.ProviderByID(updated.ID)
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "provider updated", updated.ID)
	// baseUrl 或协议变了，之前那份探测结论就不再成立：后台重探一遍。
	s.autoDetectProviderProtocols(updated)
	writeJSON(w, http.StatusOK, redactProviderForClient(updated))
}

// handleSetProviderEnabled is the admin-only provider enable/disable switch.
// Newly created providers default to enabled (Disabled zero value); while
// disabled, normal users cannot see, bind, or send traffic through the
// provider.
func (s *Server) handleSetProviderEnabled(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var payload struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Enabled == nil {
		writeOpenAIError(w, http.StatusBadRequest, "body must be {\"enabled\": true|false}")
		return
	}
	updated, err := s.router.SetProviderDisabled(r.PathValue("id"), !*payload.Enabled)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	if *payload.Enabled {
		s.logs.AddApp("info", "provider enabled", updated.ID)
	} else {
		// 禁用即停止可用性重探：清掉内存里的 unavailable 标记，避免后台继续打 /models。
		s.markProviderAvailable(updated.ID)
		s.logs.AddApp("info", "provider disabled", updated.ID)
	}
	writeJSON(w, http.StatusOK, redactProviderForClient(updated))
}

func (s *Server) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	providerID := r.PathValue("id")
	if !s.requireProviderTesterForUser(w, r, providerID) {
		return
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	result := s.fetchProviderModels(r, provider, started)
	if result.Success {
		_, _ = s.router.UpdateProviderModels(provider.ID, result.Models, "healthy")
		if err := s.saveState(); err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
			return
		}
		s.logs.AddApp("info", "provider test succeeded", fmt.Sprintf("provider=%s models=%d", provider.ID, len(result.Models)))
	} else {
		fallback := make([]domain.Model, 0)
		if strings.TrimSpace(provider.DefaultModel) != "" {
			fallback = append(fallback, domain.Model{
				ID:            strings.TrimSpace(provider.DefaultModel),
				ProviderID:    provider.ID,
				Protocol:      provider.Protocol,
				ContextLength: resolveModelContextLength(provider.DefaultModel, 0),
				InMenu:        true,
			})
		}
		healthStatus := "failed"
		if len(fallback) > 0 {
			healthStatus = "degraded"
		}
		_, _ = s.router.UpdateProviderModels(provider.ID, fallback, healthStatus)
		if err := s.saveState(); err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
			return
		}
		s.logs.AddApp("warn", "provider test failed", fmt.Sprintf("provider=%s error=%s", provider.ID, result.Error))
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleProviderAuthPreview(w http.ResponseWriter, r *http.Request) {
	if !s.requireProviderOwnerForUser(w, r, r.PathValue("id")) {
		return
	}
	provider, err := s.router.ProviderByID(r.PathValue("id"))
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	header := provider.AuthHeader
	if header == "" {
		header = "Authorization"
	}
	authValue := resolveProviderAuth(provider)
	if authValue != "" && strings.EqualFold(header, "Authorization") && !strings.HasPrefix(strings.ToLower(authValue), "bearer ") {
		authValue = "Bearer " + authValue
	}
	writeJSON(w, http.StatusOK, map[string]string{"header": header, "value": authValue})
}

// handleClaudeOAuthStart begins a Claude.ai OAuth login for the given provider.
// By default it uses a localhost callback server (same as Claude Code desktop).
// Pass {"mode":"manual"} to fall back to the platform.claude.com copy/paste flow.
func (s *Server) handleClaudeOAuthStart(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderOwnerForUser(w, r, providerID) {
		return
	}
	if _, err := s.router.ProviderByID(providerID); err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	var payload struct {
		Mode string `json:"mode"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)
	mode := strings.TrimSpace(strings.ToLower(payload.Mode))
	if mode == "" {
		mode = "localhost"
	}

	verifier, challenge, err := generateClaudePKCE()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to generate pkce: "+err.Error())
		return
	}
	state, err := generateClaudeOAuthState()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to generate oauth state: "+err.Error())
		return
	}
	pending := claudeOAuthPending{Verifier: verifier, State: state, CreatedAt: time.Now(), claim: &atomic.Bool{}}

	if mode == "manual" {
		pending.Mode = "manual"
		pending.RedirectURI = claudeOAuthRedirectURI
		s.pendingClaudeOAuth.put(providerID, pending)
		authURL := buildClaudeManualAuthorizeURL(challenge, state)
		s.logs.AddApp("info", "claude oauth manual flow started", providerID)
		writeJSON(w, http.StatusOK, map[string]any{"authUrl": authURL, "state": state, "mode": "manual"})
		return
	}

	authURL, flowID, err := s.startClaudeOAuthLocalFlow(providerID, challenge, pending)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to start claude oauth flow: "+err.Error())
		return
	}
	// The paste-in finish is offered from the start: with the gateway on a server,
	// a browser on another machine can never reach its localhost callback, and
	// waiting until something has already gone wrong reads as a broken login.
	callbackURL := s.claudeOAuthCallbackURL()
	s.pendingClaudeOAuth.setCallbackHint(flowID, claudeOAuthFlowStatus{
		PasteCallback: true,
		CallbackURL:   callbackURL,
		Instructions:  claudePasteInstructions,
	})
	s.logs.AddApp("info", "claude oauth localhost flow started", providerID)
	writeJSON(w, http.StatusOK, map[string]any{
		"authUrl":       authURL,
		"state":         state,
		"flowId":        flowID,
		"mode":          "localhost",
		"pasteCallback": true,
		"callbackUrl":   callbackURL,
		"instructions":  claudePasteInstructions,
	})
}

// handleClaudeOAuthStatus reports the progress of a localhost OAuth flow.
func (s *Server) handleClaudeOAuthStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireProviderOwnerForUser(w, r, r.PathValue("id")) {
		return
	}
	flowID := strings.TrimSpace(r.URL.Query().Get("flowId"))
	if flowID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "flowId is required")
		return
	}
	status, ok := s.pendingClaudeOAuth.status(flowID)
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "unknown or expired oauth flow")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// handleClaudeOAuthCallback completes a localhost OAuth flow when Anthropic
// redirects back to the gateway's stable callback route.
func (s *Server) handleClaudeOAuthCallback(w http.ResponseWriter, r *http.Request) {
	callbackState := strings.TrimSpace(r.URL.Query().Get("state"))
	if oauthError := strings.TrimSpace(r.URL.Query().Get("error")); oauthError != "" {
		desc := strings.TrimSpace(r.URL.Query().Get("error_description"))
		message := oauthError
		if desc != "" {
			message += ": " + desc
		}
		s.pendingClaudeOAuth.setStatusByState(callbackState, "error", message)
		http.Error(w, message, http.StatusBadRequest)
		return
	}

	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	if callbackState == "" {
		http.Error(w, "missing state", http.StatusBadRequest)
		return
	}

	providerID, pending, ok := s.pendingClaudeOAuth.getByState(callbackState)
	if !ok {
		writeClaudeOAuthUnclaimedPage(w, r)
		return
	}
	if !claimClaudeCallback(pending) {
		// The code was already spent by the pasted address; say so instead of
		// exchanging a second time, which Anthropic rejects.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body style='font-family:sans-serif;padding:32px'><h2>这次授权已经处理过了</h2><p>回调地址已被使用一次（浏览器回调与粘贴只会算一次）。账号没连上的话，请回控制台重新点一次连接。</p></body></html>"))
		return
	}
	if err := s.finishClaudeOAuthExchange(providerID, pending.FlowID, pending, code, callbackState); err != nil {
		s.logs.AddApp("error", "claude oauth callback exchange failed", err.Error())
		http.Error(w, "failed to exchange oauth code", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte("<!doctype html><html><body style='font-family:sans-serif;padding:32px'><h2>Claude 账号已连接</h2><p>可以关闭此页面并返回 Protocol Gateway。</p></body></html>"))
}

// handleClaudeOAuthComplete exchanges a pasted authorization code (optionally
// in "code#state" fragment form) for a Claude OAuth token pair, persists it
// onto the provider, and returns the redacted provider.
func (s *Server) handleClaudeOAuthComplete(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderOwnerForUser(w, r, providerID) {
		return
	}
	if _, err := s.router.ProviderByID(providerID); err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	var payload struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	code, embeddedState := parseClaudeOAuthInput(payload.Code)
	if code == "" {
		writeOpenAIError(w, http.StatusBadRequest, "code is required")
		return
	}
	state := strings.TrimSpace(payload.State)
	if state == "" {
		state = embeddedState
	}

	pending, ok := s.pendingClaudeOAuth.take(providerID)
	if !ok {
		writeOpenAIError(w, http.StatusBadRequest, "no pending claude oauth flow for this provider (it may have expired); start again")
		return
	}
	if state == "" {
		state = pending.State
	}
	if state != pending.State {
		writeOpenAIError(w, http.StatusBadRequest, "oauth state mismatch; start the flow again")
		return
	}
	if !claimClaudeCallback(pending) {
		writeOpenAIError(w, http.StatusBadRequest, "这个授权流程已经处理过了（浏览器回调先到了）：刷新看账号是否已连上，没连上就重新点一次连接")
		return
	}

	tokenState := embeddedState
	if tokenState == "" {
		tokenState = state
	}
	if err := s.finishClaudeOAuthExchange(providerID, "", pending, code, tokenState); err != nil {
		s.logs.AddApp("error", "claude oauth code exchange failed", err.Error())
		writeOpenAIError(w, http.StatusBadGateway, "failed to exchange claude oauth code: "+err.Error())
		return
	}

	updated, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, redactProviderForClient(updated))
}

// handleClaudeOAuthDisconnect clears a provider's stored Claude OAuth
// credential (logout), keeping the provider itself intact.
func (s *Server) handleClaudeOAuthDisconnect(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderOwnerForUser(w, r, providerID) {
		return
	}
	updated, err := s.router.ClearProviderClaudeOAuth(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "claude oauth disconnected", providerID)
	writeJSON(w, http.StatusOK, redactProviderForClient(updated))
}

// handleClaudeOAuthUsage returns Claude.ai subscription usage buckets (5h / 7d)
// for a connected claude_oauth provider by calling Anthropic's undocumented
// OAuth usage endpoint.
func (s *Server) handleClaudeOAuthUsage(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderAccessForUser(w, r, providerID) {
		return
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if provider.AuthType != domain.AuthTypeClaudeOAuth {
		writeJSON(w, http.StatusOK, ClaudeOAuthUsageReport{Available: false, Error: "provider is not using Claude OAuth"})
		return
	}
	if provider.Disabled {
		writeJSON(w, http.StatusOK, ClaudeOAuthUsageReport{Available: false, Error: "provider disabled"})
		return
	}
	if provider.ClaudeOAuth == nil || strings.TrimSpace(provider.ClaudeOAuth.RefreshToken) == "" {
		writeJSON(w, http.StatusOK, ClaudeOAuthUsageReport{Available: false, Error: "Claude OAuth 未连接"})
		return
	}

	cacheKey := s.oauthUsageCacheKeyFor("claude", providerID)
	forceRefresh := strings.EqualFold(r.URL.Query().Get("refresh"), "1") || strings.EqualFold(r.URL.Query().Get("refresh"), "true")
	if forceRefresh {
		s.oauthUsageCache.invalidate(cacheKey)
	} else if cached, ok := s.oauthUsageCache.getAllowStale(cacheKey); ok {
		if report, ok := cached.(ClaudeOAuthUsageReport); ok {
			s.maybeRefreshClaudeOAuthUsageAsync(providerID)
			writeJSON(w, http.StatusOK, report)
			return
		}
	}

	unlock := s.lockOAuthUsageFetch(cacheKey)
	defer unlock()
	if !forceRefresh {
		if cached, ok := s.oauthUsageCache.get(cacheKey); ok {
			if report, ok := cached.(ClaudeOAuthUsageReport); ok {
				writeJSON(w, http.StatusOK, report)
				return
			}
		}
	}

	refreshed, err := s.ensureFreshClaudeToken(provider)
	if err != nil {
		writeJSON(w, http.StatusOK, ClaudeOAuthUsageReport{Available: false, Error: err.Error()})
		return
	}
	report, err := fetchClaudeOAuthUsage(r.Context(), refreshed.ClaudeOAuth.AccessToken)
	if err != nil {
		writeJSON(w, http.StatusOK, ClaudeOAuthUsageReport{Available: false, Error: err.Error()})
		return
	}
	if report.Available {
		s.oauthUsageCache.setForAccount(cacheKey, oauthUsageAccountID(provider), report)
	}
	writeJSON(w, http.StatusOK, report)
}

// handleCursorOAuthUsage returns Cursor subscription usage (plan spend / request
// buckets) for a connected cursor_oauth provider.
func (s *Server) handleCursorOAuthUsage(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderAccessForUser(w, r, providerID) {
		return
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if provider.AuthType != domain.AuthTypeCursorOAuth {
		writeJSON(w, http.StatusOK, CursorOAuthUsageReport{Available: false, Error: "provider is not using Cursor OAuth"})
		return
	}
	if provider.Disabled {
		writeJSON(w, http.StatusOK, CursorOAuthUsageReport{Available: false, Error: "provider disabled"})
		return
	}
	if provider.CursorOAuth == nil || strings.TrimSpace(provider.CursorOAuth.RefreshToken) == "" {
		writeJSON(w, http.StatusOK, CursorOAuthUsageReport{Available: false, Error: "Cursor OAuth 未连接"})
		return
	}

	cacheKey := s.oauthUsageCacheKeyFor("cursor", providerID)
	forceRefresh := strings.EqualFold(r.URL.Query().Get("refresh"), "1") || strings.EqualFold(r.URL.Query().Get("refresh"), "true")
	if forceRefresh {
		s.oauthUsageCache.invalidate(cacheKey)
	} else if cached, ok := s.oauthUsageCache.getAllowStale(cacheKey); ok {
		if report, ok := cached.(CursorOAuthUsageReport); ok {
			s.maybeRefreshCursorOAuthUsageAsync(providerID)
			writeJSON(w, http.StatusOK, report)
			return
		}
	}

	unlock := s.lockOAuthUsageFetch(cacheKey)
	defer unlock()
	if !forceRefresh {
		if cached, ok := s.oauthUsageCache.get(cacheKey); ok {
			if report, ok := cached.(CursorOAuthUsageReport); ok {
				writeJSON(w, http.StatusOK, report)
				return
			}
		}
	}

	refreshed, err := s.ensureFreshCursorToken(provider)
	if err != nil {
		writeJSON(w, http.StatusOK, CursorOAuthUsageReport{Available: false, Error: err.Error()})
		return
	}
	report, err := fetchCursorOAuthUsage(r.Context(), refreshed.CursorOAuth.AccessToken)
	if err != nil {
		writeJSON(w, http.StatusOK, CursorOAuthUsageReport{Available: false, Error: err.Error()})
		return
	}
	if report.Available {
		s.oauthUsageCache.setForAccount(cacheKey, oauthUsageAccountID(provider), report)
	}
	writeJSON(w, http.StatusOK, report)
}

// handleChatGPTOAuthUsage returns ChatGPT/Codex quota (WHAM usage) for a
// connected chatgpt_oauth provider. Free-tier accounts are included.
func (s *Server) handleChatGPTOAuthUsage(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderAccessForUser(w, r, providerID) {
		return
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if provider.AuthType != domain.AuthTypeChatGPTOAuth {
		writeJSON(w, http.StatusOK, ChatGPTOAuthUsageReport{Available: false, Error: "provider is not using ChatGPT OAuth"})
		return
	}
	if provider.Disabled {
		writeJSON(w, http.StatusOK, ChatGPTOAuthUsageReport{Available: false, Error: "provider disabled"})
		return
	}
	if provider.ChatGPTOAuth == nil || strings.TrimSpace(provider.ChatGPTOAuth.RefreshToken) == "" {
		writeJSON(w, http.StatusOK, ChatGPTOAuthUsageReport{Available: false, Error: "ChatGPT OAuth 未连接"})
		return
	}

	cacheKey := s.oauthUsageCacheKeyFor("chatgpt", providerID)
	forceRefresh := strings.EqualFold(r.URL.Query().Get("refresh"), "1") || strings.EqualFold(r.URL.Query().Get("refresh"), "true")
	if forceRefresh {
		s.oauthUsageCache.invalidate(cacheKey)
	} else if cached, ok := s.oauthUsageCache.getAllowStale(cacheKey); ok {
		if report, ok := cached.(ChatGPTOAuthUsageReport); ok {
			s.maybeRefreshChatGPTOAuthUsageAsync(providerID)
			writeJSON(w, http.StatusOK, report)
			return
		}
	}

	unlock := s.lockOAuthUsageFetch(cacheKey)
	defer unlock()
	if !forceRefresh {
		if cached, ok := s.oauthUsageCache.get(cacheKey); ok {
			if report, ok := cached.(ChatGPTOAuthUsageReport); ok {
				writeJSON(w, http.StatusOK, report)
				return
			}
		}
	}

	refreshed, err := s.ensureFreshChatGPTToken(provider)
	if err != nil {
		writeJSON(w, http.StatusOK, ChatGPTOAuthUsageReport{Available: false, Error: err.Error()})
		return
	}
	report, err := fetchChatGPTOAuthUsage(r.Context(), refreshed)
	if err != nil {
		writeJSON(w, http.StatusOK, ChatGPTOAuthUsageReport{Available: false, Error: err.Error()})
		return
	}
	if report.Available {
		s.oauthUsageCache.setForAccount(cacheKey, oauthUsageAccountID(provider), report)
	}
	writeJSON(w, http.StatusOK, report)
}

// handleChatGPTOAuthResetConsume spends the account's soonest-expiring
// rate-limit reset card, starting its current usage windows again. The vendor
// picks nothing on its own, so no_credit is returned as a normal outcome.
func (s *Server) handleChatGPTOAuthResetConsume(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderAccessForUser(w, r, providerID) {
		return
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if provider.AuthType != domain.AuthTypeChatGPTOAuth || provider.ChatGPTOAuth == nil || strings.TrimSpace(provider.ChatGPTOAuth.RefreshToken) == "" {
		writeOpenAIError(w, http.StatusBadRequest, "provider is not connected with ChatGPT OAuth")
		return
	}
	if provider.Disabled {
		writeOpenAIError(w, http.StatusBadRequest, "provider disabled")
		return
	}
	refreshed, err := s.ensureFreshChatGPTToken(provider)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	raw, err := readChatGPTOAuthResetCredits(r.Context(), refreshed, chatgptOAuthResetCreditsURL)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "读取重置卡失败："+err.Error())
		return
	}
	credit, ok := earliestChatGPTResetCredit(buildChatGPTResetCredits(raw).Credits)
	if !ok {
		writeJSON(w, http.StatusOK, ChatGPTResetConsumeResult{Code: "no_credit"})
		return
	}
	redeemID, err := newChatGPTResetRedeemID()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	result, err := consumeChatGPTOAuthResetCredit(r.Context(), refreshed, credit.ID, redeemID)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	if result.Code == "reset" {
		// The cached snapshot still shows the old windows; drop it so the panel
		// reads the account afresh on its next poll.
		s.oauthUsageCache.invalidate(s.oauthUsageCacheKeyFor("chatgpt", providerID))
		go s.refreshChatGPTOAuthUsage(providerID)
		s.logs.AddApp("info", "chatgpt reset credit used", fmt.Sprintf("provider=%s windows=%d", providerID, result.WindowsReset))
	}
	writeJSON(w, http.StatusOK, result)
}

// handleZhipuUsage returns Zhipu (智谱 / bigmodel) coding-plan quota for an
// api_key provider whose BaseURL points at bigmodel.cn or z.ai. When the
// provider carries a team organization + project ID, the team-plan endpoint
// (?type=2 + bigmodel-organization / bigmodel-project headers) is queried;
// otherwise the personal-plan endpoint is used.
func (s *Server) handleZhipuUsage(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderAccessForUser(w, r, providerID) {
		return
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if !isZhipuBaseURL(provider.BaseURL) {
		writeJSON(w, http.StatusOK, ZhipuUsageReport{Available: false, Error: "provider is not a Zhipu (bigmodel/z.ai) provider"})
		return
	}
	if provider.Disabled {
		writeJSON(w, http.StatusOK, ZhipuUsageReport{Available: false, Error: "provider disabled"})
		return
	}
	apiKey := resolveProviderAuth(provider)
	if strings.TrimSpace(apiKey) == "" {
		writeJSON(w, http.StatusOK, ZhipuUsageReport{Available: false, Error: "provider has no API key configured (apiKeySource is empty)"})
		return
	}

	cacheKey := s.oauthUsageCacheKeyFor("zhipu", providerID)
	forceRefresh := strings.EqualFold(r.URL.Query().Get("refresh"), "1") || strings.EqualFold(r.URL.Query().Get("refresh"), "true")
	if forceRefresh {
		s.oauthUsageCache.invalidate(cacheKey)
	} else if cached, ok := s.oauthUsageCache.getAllowStale(cacheKey); ok {
		if report, ok := cached.(ZhipuUsageReport); ok {
			s.maybeRefreshZhipuUsageAsync(providerID)
			writeJSON(w, http.StatusOK, report)
			return
		}
	}

	unlock := s.lockOAuthUsageFetch(cacheKey)
	defer unlock()
	if !forceRefresh {
		if cached, ok := s.oauthUsageCache.get(cacheKey); ok {
			if report, ok := cached.(ZhipuUsageReport); ok {
				writeJSON(w, http.StatusOK, report)
				return
			}
		}
	}

	report, err := fetchZhipuUsageForProvider(r.Context(), provider, apiKey)
	if err != nil {
		writeJSON(w, http.StatusOK, ZhipuUsageReport{Available: false, Error: err.Error()})
		return
	}
	if report.resetsError != "" {
		s.logs.AddApp("info", "zhipu reset list unread", "provider="+providerID+" "+report.resetsError)
	}
	// Cache success + unsupported (非编程套餐) so card polling stays quiet.
	if report.Available || report.Unsupported {
		s.oauthUsageCache.setForAccount(cacheKey, oauthUsageAccountID(provider), report)
	}
	writeJSON(w, http.StatusOK, report)
}

// handleOpenCodeGoUsage 返回 OpenCode Go 账号的 5 小时、7 天和月额度。
// 与 Claude/Cursor 等订阅面板一样使用统一缓存，避免多标签页和卡片轮询
// 同时打上游额度接口。
func (s *Server) handleOpenCodeGoUsage(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderAccessForUser(w, r, providerID) {
		return
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if !isOpenCodeGoProvider(provider) {
		writeJSON(w, http.StatusOK, OpenCodeGoUsageReport{Available: false, Error: "provider is not an OpenCode Go provider"})
		return
	}
	if provider.Disabled {
		writeJSON(w, http.StatusOK, OpenCodeGoUsageReport{Available: false, Error: "provider disabled"})
		return
	}
	apiKey := resolveProviderAuth(provider)
	if strings.TrimSpace(apiKey) == "" {
		writeJSON(w, http.StatusOK, OpenCodeGoUsageReport{Available: false, Error: "OpenCode Go 未配置 API Key"})
		return
	}

	cacheKey := s.oauthUsageCacheKeyFor("opencode-go", providerID)
	forceRefresh := strings.EqualFold(r.URL.Query().Get("refresh"), "1") || strings.EqualFold(r.URL.Query().Get("refresh"), "true")
	if forceRefresh {
		s.oauthUsageCache.invalidate(cacheKey)
	} else if cached, ok := s.oauthUsageCache.getAllowStale(cacheKey); ok {
		if report, ok := cached.(OpenCodeGoUsageReport); ok {
			s.maybeRefreshOpenCodeGoUsageAsync(providerID)
			writeJSON(w, http.StatusOK, report)
			return
		}
	}

	unlock := s.lockOAuthUsageFetch(cacheKey)
	defer unlock()
	if !forceRefresh {
		if cached, ok := s.oauthUsageCache.get(cacheKey); ok {
			if report, ok := cached.(OpenCodeGoUsageReport); ok {
				writeJSON(w, http.StatusOK, report)
				return
			}
		}
	}

	report, err := fetchOpenCodeGoUsage(r.Context(), provider.BaseURL, apiKey)
	if err != nil {
		writeJSON(w, http.StatusOK, OpenCodeGoUsageReport{Available: false, Error: err.Error()})
		return
	}
	if report.Available {
		s.oauthUsageCache.setForAccount(cacheKey, oauthUsageAccountID(provider), report)
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleDeepSeekBalance(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderAccessForUser(w, r, providerID) {
		return
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if !isDeepSeekBaseURL(provider.BaseURL) {
		writeJSON(w, http.StatusOK, DeepSeekBalanceReport{Available: false, Error: "provider is not a DeepSeek provider"})
		return
	}
	if provider.Disabled {
		writeJSON(w, http.StatusOK, DeepSeekBalanceReport{Available: false, Error: "provider disabled"})
		return
	}
	apiKey := resolveProviderAuth(provider)
	if strings.TrimSpace(apiKey) == "" {
		writeJSON(w, http.StatusOK, DeepSeekBalanceReport{Available: false, Error: "provider has no API key configured (apiKeySource is empty)"})
		return
	}

	cacheKey := s.oauthUsageCacheKeyFor("deepseek", providerID)
	forceRefresh := strings.EqualFold(r.URL.Query().Get("refresh"), "1") || strings.EqualFold(r.URL.Query().Get("refresh"), "true")
	if forceRefresh {
		s.oauthUsageCache.invalidate(cacheKey)
	} else if cached, ok := s.oauthUsageCache.getAllowStale(cacheKey); ok {
		if report, ok := cached.(DeepSeekBalanceReport); ok {
			s.maybeRefreshDeepSeekBalanceAsync(providerID)
			writeJSON(w, http.StatusOK, report)
			return
		}
	}

	unlock := s.lockOAuthUsageFetch(cacheKey)
	defer unlock()
	if !forceRefresh {
		if cached, ok := s.oauthUsageCache.get(cacheKey); ok {
			if report, ok := cached.(DeepSeekBalanceReport); ok {
				writeJSON(w, http.StatusOK, report)
				return
			}
		}
	}

	report, err := fetchDeepSeekBalance(r.Context(), provider.BaseURL, apiKey)
	if err != nil {
		writeJSON(w, http.StatusOK, DeepSeekBalanceReport{Available: false, Error: err.Error()})
		return
	}
	// Cache success + unsupported (第三方中转无此端点) so card polling stays quiet.
	if report.Available || report.Unsupported {
		s.oauthUsageCache.setForAccount(cacheKey, oauthUsageAccountID(provider), report)
	}
	writeJSON(w, http.StatusOK, report)
}

// fetchZhipuUsageForProvider routes personal vs team plan by the presence of
// both the organization + project IDs (or an explicit codingPlanProvider ==
// "zhipu_team" tag).
func fetchZhipuUsageForProvider(ctx context.Context, provider domain.Provider, apiKey string) (ZhipuUsageReport, error) {
	org := strings.TrimSpace(provider.TeamOrganizationID)
	project := strings.TrimSpace(provider.TeamProjectID)
	team := strings.EqualFold(strings.TrimSpace(provider.CodingPlanProvider), "zhipu_team")
	base := zhipuQuotaBase(provider.BaseURL)
	var report ZhipuUsageReport
	var err error
	if team || (org != "" && project != "") {
		report, err = fetchZhipuTeamUsage(ctx, apiKey, org, project)
		base, team = "https://open.bigmodel.cn", true
	} else {
		report, err = fetchZhipuUsage(ctx, provider.BaseURL, apiKey)
	}
	if err == nil && report.Available {
		var resetsErr error
		report.Resets, resetsErr = fetchZhipuResets(ctx, base, apiKey, org, project, team)
		if resetsErr != nil {
			report.resetsError = resetsErr.Error()
		}
	}
	return report, err
}

func (s *Server) lockOAuthUsageFetch(key string) func() {
	muAny, _ := s.oauthUsageFetchMu.LoadOrStore(key, &sync.Mutex{})
	mu := muAny.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *Server) tryLockOAuthUsageFetch(key string) bool {
	muAny, _ := s.oauthUsageFetchMu.LoadOrStore(key, &sync.Mutex{})
	mu := muAny.(*sync.Mutex)
	return mu.TryLock()
}

func (s *Server) unlockOAuthUsageFetch(key string) {
	muAny, ok := s.oauthUsageFetchMu.Load(key)
	if !ok {
		return
	}
	muAny.(*sync.Mutex).Unlock()
}

func (s *Server) handleProviderChatTest(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	providerID := r.PathValue("id")
	if !s.requireProviderTesterForUser(w, r, providerID) {
		return
	}
	var payload providerChatTestRequest
	_ = json.NewDecoder(r.Body).Decode(&payload)
	result, status := s.testProviderChat(r, providerID, payload, started)
	writeJSON(w, status, result)
}

func (s *Server) handleProviderCacheTest(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	providerID := r.PathValue("id")
	if !s.requireProviderTesterForUser(w, r, providerID) {
		return
	}
	var payload providerChatTestRequest
	_ = json.NewDecoder(r.Body).Decode(&payload)
	result, status := s.testProviderCacheChat(r, providerID, payload, started)
	writeJSON(w, status, result)
}

func (s *Server) handleProviderThinkingTest(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	providerID := r.PathValue("id")
	if !s.requireProviderTesterForUser(w, r, providerID) {
		return
	}
	var payload providerChatTestRequest
	_ = json.NewDecoder(r.Body).Decode(&payload)
	result, status := s.testProviderThinkingChat(r, providerID, payload, started)
	writeJSON(w, status, result)
}

// resolveProviderEmbeddingsURL completes a base URL with the embeddings path.
// A base URL that already names the endpoint is used as given: the operator
// configured an exact path on purpose, and appending a second one 404s.
func resolveProviderEmbeddingsURL(provider domain.Provider) string {
	return resolveProviderEndpointURL(provider, "/embeddings")
}

// resolveProviderImageURL 按与 resolveProviderEndpointURL 相同的后缀判据拼出图片
// 生成端点：BaseURL 已经以 /images/generations 结尾就不再追加（用 Contains 会把
// .../images/generations/<某段> 这种地址误判成已带端点）。
func resolveProviderImageURL(provider domain.Provider) string {
	return resolveProviderEndpointURL(provider, "/images/generations")
}

// resolveProviderEndpointURL appends an OpenAI-style endpoint path to the
// provider's BaseURL, unless the BaseURL already names that endpoint (an
// operator may point the row straight at it).
func resolveProviderEndpointURL(provider domain.Provider, path string) string {
	upstreamURL := strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
	if strings.HasSuffix(strings.ToLower(upstreamURL), strings.ToLower(path)) {
		return upstreamURL
	}
	return upstreamURL + path
}

func resolveProviderChatURL(provider domain.Provider, model string) string {
	if provider.AuthType == domain.AuthTypePeer {
		// 父子透传的 provider 不是 provider，而是「上游网关的一扇门」：地址
		// 固定是它的 /__peer/chat/completions，模型写在包体里。走通用的
		// baseUrl + /chat/completions 拼接会打到上游的普通模型入口，那里不认
		// 对端凭据。
		return resolvePeerParentURL(provider) + peerEndpointPrefix + "chat/completions"
	}
	if isTraeProvider(provider) {
		// Trae has one agent endpoint per realm and no OpenAI-style path to
		// append to it.
		if upstreamURL := traeChatURL(provider); upstreamURL != "" {
			return upstreamURL
		}
	}
	resolvedModel := strings.TrimSpace(model)
	if resolvedModel == "" {
		resolvedModel = strings.TrimSpace(provider.DefaultModel)
	}
	if resolvedModel == "" {
		resolvedModel = "request-model-not-set"
	}
	upstreamURL := strings.ReplaceAll(strings.TrimSpace(provider.BaseURL), "{model}", resolvedModel)
	lowerURL := strings.ToLower(upstreamURL)
	if provider.Protocol == domain.ProtocolOpenAIChat && !strings.Contains(lowerURL, "/chat/completions") && !strings.Contains(provider.BaseURL, "{model}") {
		upstreamURL = strings.TrimRight(upstreamURL, "/") + "/chat/completions"
	}
	// Responses providers need the same path completion as the forwarding path
	// (resolveProviderResponsesURL); without it a bare base URL 404s.
	if provider.Protocol == domain.ProtocolOpenAIResponses && !strings.Contains(lowerURL, "/responses") && !strings.Contains(provider.BaseURL, "{model}") {
		upstreamURL = strings.TrimRight(upstreamURL, "/") + "/responses"
	}
	// Claude 也要补，理由与上面两条一样：真实转发路径（sendClaudeMessagesUpstream /
	// executeClaudeMessagesHTTP）都补 /messages，只有这里漏了，于是「对话测试」
	// 打到一个裸 base URL 上换回 404 —— 测出来的是路径不存在，不是 provider 有病。
	if provider.Protocol == domain.ProtocolClaude && !strings.Contains(lowerURL, "/messages") && !strings.Contains(provider.BaseURL, "{model}") {
		upstreamURL = strings.TrimRight(upstreamURL, "/") + "/messages"
	}
	return upstreamURL
}

type providerChatHTTPResult struct {
	Status       int
	LatencyMs    int64
	ResponseBody string
	RequestBody  string
	TargetURL    string
	Error        string
}

func (s *Server) executeProviderChatHTTP(r *http.Request, provider domain.Provider, model string, payload map[string]any, started time.Time) providerChatHTTPResult {
	body, err := json.Marshal(payload)
	if err != nil {
		return providerChatHTTPResult{Error: err.Error(), LatencyMs: time.Since(started).Milliseconds()}
	}
	resolvedModel, _ := payload["model"].(string)
	if isQoderProvider(provider) {
		// Qoder's upstream is its own signed endpoint, and it serves no
		// /chat/completions path: ask it the way the forward path does, so the
		// console's 对话测试 shows the real exchange instead of a 503 off a
		// path that does not exist.
		endpoint := qoderSiteOf(provider).qoderChatURL()
		refreshed, refreshErr := s.ensureFreshQoderToken(provider)
		if refreshErr != nil {
			return providerChatHTTPResult{Error: refreshErr.Error(), TargetURL: endpoint, LatencyMs: time.Since(started).Milliseconds()}
		}
		response, sendErr := s.sendQoderChat(withQoderSessionKey(r.Context(), requestSessionID(r, payload)), refreshed, resolvedModel, body)
		if response == nil {
			message := "Qoder 没有应答"
			if sendErr != nil {
				message = sendErr.Error()
			}
			return providerChatHTTPResult{Error: message, TargetURL: endpoint, RequestBody: string(body), LatencyMs: time.Since(started).Milliseconds()}
		}
		defer response.Body.Close()
		if response.StatusCode < 400 {
			// Qoder only answers as a stream; the console asked for one object.
			_ = forcedStreamAsCompletion(response, resolvedModel, workBuddyStreamAsCompletion)
		}
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 16384))
		return providerChatHTTPResult{
			Status:       response.StatusCode,
			LatencyMs:    time.Since(started).Milliseconds(),
			ResponseBody: string(responseBody),
			RequestBody:  string(body),
			TargetURL:    endpoint,
		}
	}
	if provider.AuthType == domain.AuthTypeCursorOAuth {
		baseURL, refreshed, bridgeErr := s.resolveCursorBridgeBaseURL(r.Context(), provider)
		if bridgeErr != nil {
			return providerChatHTTPResult{Error: bridgeErr.Error(), LatencyMs: time.Since(started).Milliseconds()}
		}
		provider = refreshed
		upstreamURL := strings.TrimRight(baseURL, "/") + "/chat/completions"
		request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(body))
		if err != nil {
			return providerChatHTTPResult{Error: err.Error(), TargetURL: upstreamURL, RequestBody: string(body), LatencyMs: time.Since(started).Milliseconds()}
		}
		request.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 120 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			return providerChatHTTPResult{
				Error:       err.Error(),
				TargetURL:   upstreamURL,
				RequestBody: string(body),
				LatencyMs:   time.Since(started).Milliseconds(),
			}
		}
		defer response.Body.Close()
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 16384))
		return providerChatHTTPResult{
			Status:       response.StatusCode,
			LatencyMs:    time.Since(started).Milliseconds(),
			ResponseBody: string(responseBody),
			RequestBody:  string(body),
			TargetURL:    upstreamURL,
		}
	}
	upstreamURL := resolveProviderChatURLWithAdapter(provider, resolvedModel)
	// WorkBuddy accepts only a streaming request whose first message is the
	// system prompt and OpenCode only streams its free tier; the console asked
	// for one JSON object, so the stream is rebuilt into one below.
	workBuddy := isWorkBuddyProvider(provider)
	body = chatToolCallIDs(body)
	// DeepSeek (and relays in front of it) read reasoning and reasoning_content as
	// one field and refuse a message that carries both ("duplicate field
	// reasoning"). The thinking goes upstream under one name, once.
	body = dedupeReasoningFields(body)
	var workBuddyAliases map[string]string
	if workBuddy {
		body, workBuddyAliases, _ = shapeWorkBuddyChatBody(body)
	}
	zenFree := false
	if isOpenCodeZenProvider(provider) {
		body, zenFree = prepareOpenCodeZenChatBody(provider, body)
	}
	trae := isTraeProvider(provider)
	if trae {
		refreshed, refreshErr := s.ensureFreshTraeToken(provider)
		if refreshErr != nil {
			return providerChatHTTPResult{Error: refreshErr.Error(), TargetURL: upstreamURL, LatencyMs: time.Since(started).Milliseconds()}
		}
		provider = refreshed
		shaped, shapeErr := traeChatBody(provider, body, traeFunctionsToTry(provider)[0])
		if shapeErr != nil {
			return providerChatHTTPResult{Error: shapeErr.Error(), TargetURL: upstreamURL, LatencyMs: time.Since(started).Milliseconds()}
		}
		body = shaped
	}
	send := func(requestBody []byte) (*http.Response, error) {
		// 对端 provider 是「上游网关的一扇门」，那条端点只认信封（providerId /
		// protocol / payload）。这条路径以前直发裸请求体，于是对话测试永远换来
		// 上级一句 400 "payload is required"——真实转发走 sendPeerDoor 才有信封。
		if provider.AuthType == domain.AuthTypePeer {
			return s.sendPeerDoor(r.Context(), r, provider, resolvedModel, requestBody, "application/json")
		}
		request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(requestBody))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		if workBuddy || zenFree || trae {
			request.Header.Set("Accept", "text/event-stream")
		}
		applyProviderAuth(request, provider, r.Header.Get("Authorization"))
		applyOpenCodeZenHeaders(request, provider, requestBody)
		// per-provider 自定义头。漏了这一句，「对话测试」测的就不是真实转发那条
		// 路径：OpenCode Go 要求每个请求带 x-opencode-session，配好了也发不出去，
		// 只换来 400 MissingSessionID —— 而真实流量其实是好的，最难查的一类差异。
		applyRequestAdapterHeaders(request, provider, resolvedModel)
		if trae {
			applyTraeHeaders(request, provider)
		}
		return (&http.Client{Timeout: 120 * time.Second}).Do(request)
	}
	var response *http.Response
	if workBuddy {
		// The account it retries with is the one passed in, whatever the
		// outer request went out as: a multi-account chain sends each attempt
		// with its own provider (round26 D26-12).
		response, err = s.sendWorkBuddyChat(provider, resolvedModel, body, workBuddyAliases, func(sendProvider domain.Provider, requestBody []byte) (*http.Response, error) {
			request, requestErr := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(requestBody))
			if requestErr != nil {
				return nil, requestErr
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "text/event-stream")
			applyProviderAuth(request, sendProvider, r.Header.Get("Authorization"))
			applyOpenCodeZenHeaders(request, sendProvider, requestBody)
			return (&http.Client{Timeout: 120 * time.Second}).Do(request)
		}, false)
	} else {
		response, err = send(body)
	}
	if err != nil {
		return providerChatHTTPResult{
			Error:       err.Error(),
			TargetURL:   upstreamURL,
			RequestBody: string(body),
			LatencyMs:   time.Since(started).Milliseconds(),
		}
	}
	defer response.Body.Close()
	if workBuddy {
		_ = rewriteWorkBuddyUpstreamStreamedBody(response, resolvedModel)
	}
	if zenFree {
		_ = rewriteOpenCodeZenUpstreamStreamedBody(response, resolvedModel)
	}
	if trae {
		// Trae answers its own event stream; the console reads a chat reply, so
		// convert it and rebuild the single object it asked for.
		response = s.decorateTraeResponse(response, provider, resolvedModel, traeFunctionsToTry(provider)[0])
		_ = forcedStreamAsCompletion(response, resolvedModel, workBuddyStreamAsCompletion)
	}
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 16384))
	return providerChatHTTPResult{
		Status:       response.StatusCode,
		LatencyMs:    time.Since(started).Milliseconds(),
		ResponseBody: string(responseBody),
		RequestBody:  string(body),
		TargetURL:    upstreamURL,
	}
}

func extractAssistantContent(responseBody []byte) string {
	var payload struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(responseBody, &payload); err != nil || len(payload.Choices) == 0 {
		return ""
	}
	return strings.TrimSpace(payload.Choices[0].Message.Content)
}

func extractClaudeAssistantContent(responseBody []byte) string {
	var payload struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return ""
	}
	parts := make([]string, 0, len(payload.Content))
	for _, block := range payload.Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func (s *Server) executeClaudeMessagesHTTP(r *http.Request, provider domain.Provider, payload map[string]any, started time.Time) claudeOAuthHTTPResult {
	if provider.AuthType == domain.AuthTypeClaudeOAuth {
		return s.sendClaudeOAuthMessagesRequest(r.Context(), provider, payload, started)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return claudeOAuthHTTPResult{Error: err.Error(), LatencyMs: time.Since(started).Milliseconds()}
	}
	// 对端 provider 的 Claude 协议同样有这个问题：上游的 /__peer/chat/completions
	// 只认信封，裸 Messages 请求体换来的是一句 400 "payload is required"。
	if provider.AuthType == domain.AuthTypePeer {
		response, err := s.sendPeerDoor(r.Context(), r, provider, stringValue(claudeBodyField(body, "model")), body, "application/json")
		if err != nil {
			return claudeOAuthHTTPResult{Error: err.Error(), TargetURL: resolveProviderChatURL(provider, ""), RequestBody: string(body), LatencyMs: time.Since(started).Milliseconds()}
		}
		defer response.Body.Close()
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 16384))
		return claudeOAuthHTTPResult{
			Status:       response.StatusCode,
			LatencyMs:    time.Since(started).Milliseconds(),
			ResponseBody: string(responseBody),
			RequestBody:  string(body),
			TargetURL:    resolveProviderChatURL(provider, ""),
		}
	}
	upstreamURL := strings.TrimSpace(provider.BaseURL)
	lowerURL := strings.ToLower(upstreamURL)
	if upstreamURL == "" || strings.Contains(lowerURL, "anthropic.com") {
		upstreamURL = claudeMessagesURL
	} else if !strings.Contains(lowerURL, "/messages") {
		upstreamURL = strings.TrimRight(upstreamURL, "/") + "/messages"
	}

	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return claudeOAuthHTTPResult{Error: err.Error(), TargetURL: upstreamURL, RequestBody: string(body), LatencyMs: time.Since(started).Milliseconds()}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("anthropic-version", "2023-06-01")
	applyProviderAuth(request, provider, r.Header.Get("Authorization"))
	// 「对话测试」必须和真实请求发一样的东西，否则测出来的是另一条路径的行为。
	applyRequestAdapterHeaders(request, provider, stringValue(claudeBodyField(body, "model")))

	client := &http.Client{Timeout: 120 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return claudeOAuthHTTPResult{Error: err.Error(), TargetURL: upstreamURL, RequestBody: string(body), LatencyMs: time.Since(started).Milliseconds()}
	}
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 16384))
	return claudeOAuthHTTPResult{
		Status:       response.StatusCode,
		LatencyMs:    time.Since(started).Milliseconds(),
		ResponseBody: string(responseBody),
		RequestBody:  string(body),
		TargetURL:    upstreamURL,
	}
}

// testClaudeOAuthProviderChat handles the provider chat-test flow for
// claude_oauth providers: it builds a native Anthropic Messages API payload
// instead of the OpenAI-shaped one, reusing ensureFreshClaudeToken + the same
// billing-header-injection/anthropic-beta logic as the real pass-through path.
func (s *Server) testClaudeOAuthProviderChat(r *http.Request, provider domain.Provider, req providerChatTestRequest, started time.Time) (map[string]any, int) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = strings.TrimSpace(provider.DefaultModel)
	}
	if model == "" {
		model = "request-model-not-set"
	}

	systemPrompt := strings.TrimSpace(req.SystemPrompt)
	userPrompt := strings.TrimSpace(req.UserPrompt)
	if userPrompt == "" {
		userPrompt = strings.TrimSpace(req.Message)
	}
	if userPrompt == "" {
		userPrompt = "x+5等于几"
	}

	payload := map[string]any{
		"model":      model,
		"max_tokens": 4096,
		"stream":     false,
		"messages":   []map[string]any{{"role": "user", "content": userPrompt}},
	}
	if systemPrompt != "" {
		payload["system"] = systemPrompt
	}

	round := s.sendClaudeOAuthMessagesRequest(r.Context(), provider, payload, started)
	if round.Error != "" {
		s.logs.AddApp("error", "provider chat test failed", round.Error)
		return map[string]any{
			"success":     false,
			"providerId":  provider.ID,
			"model":       model,
			"targetUrl":   round.TargetURL,
			"requestBody": round.RequestBody,
			"error":       round.Error,
			"latencyMs":   round.LatencyMs,
		}, http.StatusOK
	}

	preview := strings.TrimSpace(strings.ReplaceAll(round.ResponseBody, "\n", " "))
	if len(preview) > 900 {
		preview = preview[:900]
	}
	success := round.Status >= 200 && round.Status < 300
	s.logs.AddApp("info", "provider chat test completed", fmt.Sprintf("provider=%s status=%d latency=%dms", provider.ID, round.Status, round.LatencyMs))
	return map[string]any{
		"success":      success,
		"providerId":   provider.ID,
		"model":        model,
		"status":       round.Status,
		"latencyMs":    round.LatencyMs,
		"preview":      preview,
		"responseBody": round.ResponseBody,
		"targetUrl":    round.TargetURL,
		"requestBody":  round.RequestBody,
	}, http.StatusOK
}

// handleDeleteProvider soft-deletes a provider (see Router.DeleteProvider):
// it is hidden from listings/routing but recoverable via
// POST /__providers/{id}/restore until an admin explicitly purges it.
func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderOwnerForUser(w, r, providerID) {
		return
	}
	if err := s.router.DeleteProvider(providerID); err != nil {
		s.logs.AddApp("warn", "provider delete blocked", err.Error())
		writeOpenAIError(w, http.StatusConflict, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "provider soft-deleted (recoverable)", providerID)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// handleListDeletedProviders returns the admin "trash" view of soft-deleted
// providers so an accidental delete can be found and undone. Admin-only
// (not registered in isUserAllowedPath, same tier as the Disabled kill switch).
func (s *Server) handleListDeletedProviders(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	deleted := redactProvidersForClient(s.router.DeletedProviders())
	writeJSON(w, http.StatusOK, map[string]any{"providers": deleted})
}

// handleRestoreProvider undoes a soft delete, admin-only.
func (s *Server) handleRestoreProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	providerID := r.PathValue("id")
	updated, err := s.router.RestoreProvider(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusConflict, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "provider restored", providerID)
	writeJSON(w, http.StatusOK, redactProviderForClient(updated))
}

// handlePurgeProvider permanently removes a previously soft-deleted provider.
// Admin-only and irreversible; the provider must already be soft-deleted.
func (s *Server) handlePurgeProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	providerID := r.PathValue("id")
	// Read the provider before it is removed: if the Save fails it has to go back
	// exactly as it was (soft-deleted, still in the trash), and only a copy taken
	// beforehand can do that.
	provider, err := s.router.ProviderByIDIncludingDeleted(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusConflict, err.Error())
		return
	}
	if err := s.router.PurgeProvider(providerID); err != nil {
		writeOpenAIError(w, http.StatusConflict, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		// A purge that reports success without reaching the store is worse than
		// one that fails loudly: put the provider back and let the admin retry.
		s.router.RestoreProviderSnapshot(provider)
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("warn", "provider purged permanently", providerID)
	writeJSON(w, http.StatusOK, map[string]any{"purged": true})
}

func (s *Server) handleCreateRoute(w http.ResponseWriter, r *http.Request) {
	var route domain.Route
	if err := json.NewDecoder(r.Body).Decode(&route); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid route json: "+err.Error())
		return
	}
	// Normal users may only create routes for providers assigned to them or
	// created by them (needed when binding their API keys to own providers).
	if !s.requireProviderAccessForUser(w, r, strings.TrimSpace(route.ProviderID)) {
		return
	}
	created, err := s.router.AddRoute(route)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "route created", created.ID)
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdateRoute(w http.ResponseWriter, r *http.Request) {
	routeID := r.PathValue("id")
	if !s.requireRouteAccessForUser(w, r, routeID) {
		return
	}
	var patch domain.Route
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid route json: "+err.Error())
		return
	}
	// A granted route cannot be re-pointed at a provider the caller was never
	// granted: that would put somebody else's account behind their key.
	if !s.requireRouteRetargetForUser(w, r, patch) {
		return
	}
	updated, err := s.router.UpdateRoute(routeID, patch)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "route updated", updated.ID)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteRoute(w http.ResponseWriter, r *http.Request) {
	routeID := r.PathValue("id")
	// Ownership: a normal user reaches only routes of providers they were
	// granted or created, so another user's route (or the administrator's,
	// with the keys bound to it) cannot be deleted from a user session.
	if !s.requireRouteAccessForUser(w, r, routeID) {
		return
	}
	if err := s.router.DeleteRoute(routeID); err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "route deleted", routeID)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) handleTestRoute(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	routeID := r.PathValue("id")
	// A route test spends the provider's quota, so it takes the same access as
	// the provider's own chat test: granted users and the owner may run it.
	if !s.requireRouteAccessForUser(w, r, routeID) {
		return
	}
	var payload struct {
		Model   string `json:"model"`
		Message string `json:"message"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)
	result, status := s.testRoute(r, routeID, payload.Model, payload.Message, started)
	writeJSON(w, status, result)
}

func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, keysVisibleTo(s.requestPrincipal(r), s.router.State().APIKeys))
}

func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	identity := s.requestIdentity(r)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid api key body: "+err.Error())
		return
	}
	var key domain.APIKey
	if err := json.Unmarshal(body, &key); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid api key json: "+err.Error())
		return
	}
	// Absent streamEnabled defaults to true (streaming on by default).
	if !jsonHasKey(body, "streamEnabled") {
		key.StreamEnabled = true
	}
	if identity.isAdmin() {
		// Admin may assign any owner; validate the target user exists. An
		// unassigned key belongs to the administrator's own account.
		if owner := strings.TrimSpace(key.OwnerUserID); owner != "" && s.userStore != nil {
			if _, err := s.userStore.UserByID(owner); err != nil {
				writeOpenAIError(w, http.StatusBadRequest, "owner user not found")
				return
			}
		}
		if strings.TrimSpace(key.OwnerUserID) == "" {
			key.OwnerUserID = s.adminOwnerID()
		}
	} else {
		// Normal users always own their new keys and are provider-restricted.
		key.OwnerUserID = identity.UserID
		if err := s.validateKeyProvidersForUser(s.principalFor(identity), key); err != nil {
			writeOpenAIError(w, http.StatusForbidden, err.Error())
			return
		}
	}
	created, err := s.router.AddAPIKey(key)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.apiKeyStore != nil {
		if err := s.apiKeyStore.CreateAPIKey(created); err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "failed to save api key: "+err.Error())
			return
		}
	}
	s.logs.AddApp("info", "api key created", created.ID)
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdateAPIKey(w http.ResponseWriter, r *http.Request) {
	identity := s.requestIdentity(r)
	keyID := r.PathValue("id")
	// Ownership check: restricted users may only touch their own keys.
	if s.requestPrincipal(r).scopeOf() != domain.ScopeAll {
		owned := false
		for _, key := range s.router.State().APIKeys {
			if key.ID == keyID && key.OwnerUserID == identity.UserID {
				owned = true
				break
			}
		}
		if !owned {
			writeOpenAIError(w, http.StatusForbidden, "permission denied")
			return
		}
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid api key body: "+err.Error())
		return
	}
	var patch domain.APIKey
	if err := json.Unmarshal(body, &patch); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid api key json: "+err.Error())
		return
	}
	// Absent streamEnabled defaults to true (streaming on by default).
	if !jsonHasKey(body, "streamEnabled") {
		patch.StreamEnabled = true
	}
	if !jsonHasKey(body, "fallbackProviderIds") {
		patch.FallbackProviderIDs = nil
	}
	// A key never changes hands except through the admin-only ownerUserId
	// patch below; dropping the field here stops a stale client payload from
	// silently reassigning ownership back to "" (i.e. the administrator).
	patch.OwnerUserID = ""
	if patch.FallbackProviderIDs == nil {
		patch.FallbackProviderIDs = []string{}
	}
	if !jsonHasKey(body, "fallbackModelOverrides") {
		patch.FallbackModelOverrides = nil
	} else if patch.FallbackModelOverrides == nil {
		patch.FallbackModelOverrides = map[string]string{}
	}
	// Quota is governance, not preference: only an administrator may set or raise
	// it. A key's owner deciding their own budget is exactly the escalation this
	// check exists to prevent.
	quotaSent := jsonHasKey(body, "quotaWindow") || jsonHasKey(body, "quotaTokens") || jsonHasKey(body, "quotaCostMicros")
	if quotaSent && !s.requirePermission(w, r, domain.PermKeysQuota) {
		return
	}
	// The three quota fields only mean something as a set (a limit needs a window),
	// so a partial patch carries the fields the caller did not send in from the
	// current key. The console re-sends everything; a script does not, and silently
	// clearing a budget because one number was patched would be the worst outcome.
	if current, ok := s.router.APIKeyByID(keyID); ok {
		// Same trap, worse consequence: Enabled absent used to mean "false", which
		// silently switches a key off for any caller that patches one field — and
		// a disabled key stops matching at all, so the traffic then falls through
		// to the route's default provider instead of returning 401.
		if !jsonHasKey(body, "enabled") {
			patch.Enabled = current.Enabled
		}
		if !jsonHasKey(body, "quotaWindow") {
			patch.QuotaWindow = current.QuotaWindow
		}
		if !jsonHasKey(body, "quotaTokens") {
			patch.QuotaTokens = current.QuotaTokens
		}
		if !jsonHasKey(body, "quotaCostMicros") {
			patch.QuotaCostMicros = current.QuotaCostMicros
		}
	}
	if !identity.isAdmin() {
		// Provider whitelist check for role=user (route + fallbacks).
		if err := s.validateKeyProvidersForUser(s.principalFor(identity), patch); err != nil {
			writeOpenAIError(w, http.StatusForbidden, err.Error())
			return
		}
	}
	updated, err := s.router.UpdateAPIKey(r.PathValue("id"), patch)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Owner reassignment: admin only, and only when the field is present.
	if identity.isAdmin() && jsonHasKey(body, "ownerUserId") {
		owner := strings.TrimSpace(patch.OwnerUserID)
		if owner != "" && s.userStore != nil {
			if _, err := s.userStore.UserByID(owner); err != nil {
				writeOpenAIError(w, http.StatusBadRequest, "owner user not found")
				return
			}
		}
		if reassigned, err := s.router.UpdateAPIKeyOwner(updated.ID, owner); err == nil {
			updated = reassigned
		}
	}
	if s.apiKeyStore != nil {
		if err := s.apiKeyStore.UpdateAPIKey(updated); err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "failed to save api key: "+err.Error())
			return
		}
	}
	s.logs.AddApp("info", "api key updated", updated.ID)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	identity := s.requestIdentity(r)
	keyID := r.PathValue("id")
	if s.requestPrincipal(r).scopeOf() != domain.ScopeAll {
		owned := false
		for _, key := range s.router.State().APIKeys {
			if key.ID == keyID && key.OwnerUserID == identity.UserID {
				owned = true
				break
			}
		}
		if !owned {
			writeOpenAIError(w, http.StatusForbidden, "permission denied")
			return
		}
	}
	if err := s.router.DeleteAPIKey(keyID); err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if s.apiKeyStore != nil {
		if err := s.apiKeyStore.DeleteAPIKey(keyID); err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "failed to delete api key: "+err.Error())
			return
		}
	}
	s.logs.AddApp("info", "api key deleted", keyID)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) saveState() error {
	if s.stateSaver == nil {
		return nil
	}
	state := s.router.State()
	state.LogLevel = s.logs.Level()
	state.RequestLogRetentionDays = s.RequestLogRetentionDays()
	state.Log2xxBodies = s.Log2xxBodies()
	state.WebExposed = s.router.WebExposed()
	return s.stateSaver.Save(state)
}

// persistProviderOAuth incrementally writes just the refreshed OAuth token
// columns for one provider when the store supports it, avoiding the heavy
// full-state Save (which DELETEs + re-INSERTs all providers/models/routes on
// the single shared SQLite connection and blocks request-hot-path writes).
// Falls back to saveState when the incremental saver is unavailable.
func (s *Server) persistProviderOAuth(provider domain.Provider) error {
	if s.providerOAuthSaver != nil {
		var accessToken, refreshToken, expiresAt, scope, accountLabel string
		var userID, authDomain, authJSON string
		switch {
		case provider.ClaudeOAuth != nil:
			accessToken = provider.ClaudeOAuth.AccessToken
			refreshToken = provider.ClaudeOAuth.RefreshToken
			expiresAt = provider.ClaudeOAuth.ExpiresAt
			scope = provider.ClaudeOAuth.Scope
			accountLabel = provider.ClaudeOAuth.AccountLabel
		case provider.CursorOAuth != nil:
			accessToken = provider.CursorOAuth.AccessToken
			refreshToken = provider.CursorOAuth.RefreshToken
			expiresAt = provider.CursorOAuth.ExpiresAt
			accountLabel = provider.CursorOAuth.AccountLabel
		case provider.ChatGPTOAuth != nil:
			accessToken = provider.ChatGPTOAuth.AccessToken
			refreshToken = provider.ChatGPTOAuth.RefreshToken
			expiresAt = provider.ChatGPTOAuth.ExpiresAt
			scope = provider.ChatGPTOAuth.ChatGPTAccountID
			accountLabel = provider.ChatGPTOAuth.AccountLabel
		case provider.QoderPAT != nil:
			accessToken = provider.QoderPAT.AccessToken
			refreshToken = provider.QoderPAT.RefreshToken
			expiresAt = provider.QoderPAT.ExpiresAt
			accountLabel = provider.QoderPAT.AccountLabel
			userID = provider.QoderPAT.UID
			if provider.QoderPAT.Disconnected {
				scope = domain.QoderDisconnectedScopeMarker
			}
			authJSON = credentialJSON(provider.QoderPAT)
		case provider.WorkBuddy != nil:
			accessToken = provider.WorkBuddy.AccessToken
			refreshToken = provider.WorkBuddy.RefreshToken
			expiresAt = provider.WorkBuddy.ExpiresAt
			accountLabel = provider.WorkBuddy.AccountLabel
			userID = provider.WorkBuddy.UserID
			authDomain = provider.WorkBuddy.Domain
			// SignInAt lives in oauth_json only; a refresh that skipped it would
			// leave the credential with no expiry unageable after a restart.
			authJSON = credentialJSON(provider.WorkBuddy)
		case provider.ZCode != nil:
			accessToken = provider.ZCode.APIKey
			refreshToken = provider.ZCode.BizToken
			expiresAt = provider.ZCode.ExpiresAt
			accountLabel = provider.ZCode.AccountLabel
			authJSON = credentialJSON(provider.ZCode)
		case provider.Trae != nil:
			accessToken = provider.Trae.AccessToken
			refreshToken = provider.Trae.RefreshToken
			expiresAt = provider.Trae.ExpiresAt
			accountLabel = provider.Trae.AccountLabel
			authJSON = credentialJSON(provider.Trae)
		default:
			return nil
		}
		return s.providerOAuthSaver.UpdateProviderOAuth(provider.ID, accessToken, refreshToken, expiresAt, scope, accountLabel, userID, authDomain, authJSON)
	}
	return s.saveState()
}

// handleOpenAIModels 是 OpenAI 形态的模型目录。
//
// Claude Desktop 也读这个接口，但它只保留读起来像 Anthropic 的 id——只提供
// m1/m2 的网关在它眼里就是"没有任何模型"。所以对 Desktop 的请求单独出一份
// 清单：本来就被它接受的 id 原样给，其余换成 anthropic/gw-<哈希> 别名并在
// display_name 里保留真名；上下文窗口按它判 1M 用的 max_input_tokens 一起发，
// 够 1M 的模型按 "[1m]" 的 id 列出去（见 desktop_models.go）。请求回来时再把
// 别名与 "[1m]" 反查回真实模型，两边靠同一份模型集合保证一致。
func (s *Server) handleOpenAIModels(w http.ResponseWriter, r *http.Request) {
	models := s.modelsForRequest(r)
	if isClaudeDesktopRequest(r.Header.Get("User-Agent")) {
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": desktopModelList(models)})
		return
	}
	data := make([]map[string]any, 0, len(models))
	for _, model := range models {
		contextLen := modelWindow(model)
		entry := map[string]any{
			"id":             model.ID,
			"object":         "model",
			"created":        0,
			"owned_by":       model.ProviderID,
			"context_length": contextLen,
		}
		data = append(data, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// handleClaudeModels is Claude Code's model discovery. It publishes the
// caller's real catalog plus two kinds of synthetic entries: the sonnet/opus/
// haiku tier aliases the built-in sub-agents ask for by name, and the reserved
// "default" alias. The tier aliases are limited to the tiers this caller can
// actually reach — advertising a model no provider serves is a capacity lie the
// client acts on (see claudeModelAliasEntries).
func (s *Server) handleClaudeModels(w http.ResponseWriter, r *http.Request) {
	models := s.modelsForRequest(r)
	data := make([]map[string]any, 0, len(models)+len(claudeModelAliases)+2)
	seen := map[string]bool{}
	for _, entry := range claudeModelAliasEntries(models) {
		id := stringValue(entry["id"])
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if entry["created_at"] == "" {
			entry["created_at"] = time.Now().UTC().Format(time.RFC3339)
		}
		data = append(data, entry)
	}
	// The reserved "default" alias, so a client that pins ANTHROPIC_MODEL once
	// finds it in model discovery too. Two spellings, matching Claude Code's
	// own extended-context convention: the bare one and the 1M variant.
	for _, entry := range reservedDefaultModelEntries() {
		if seen[entry["id"].(string)] {
			continue
		}
		seen[entry["id"].(string)] = true
		data = append(data, entry)
	}
	for _, model := range models {
		if seen[model.ID] {
			continue
		}
		seen[model.ID] = true
		contextLen := modelWindow(model)
		data = append(data, map[string]any{
			"id":               model.ID,
			"type":             "model",
			"display_name":     model.ID,
			"created_at":       time.Now().UTC().Format(time.RFC3339),
			"max_input_tokens": contextLen,
			"max_tokens":       resolveModelMaxOutputTokens(model.ID, contextLen),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "has_more": false})
}

func (s *Server) modelsForRequest(r *http.Request) []domain.Model {
	state := s.router.State()
	providerID := ""
	var matchedKey domain.APIKey
	var hasKey bool
	if token := extractConsumerAPIKey(r); token != "" {
		if key, ok := s.router.APIKeyByToken(token); ok {
			matchedKey = key
			hasKey = true
			if strings.TrimSpace(key.RouteID) != "" {
				if route, err := s.router.RouteByID(key.RouteID); err == nil {
					providerID = route.ProviderID
				}
			}
		}
	}
	models := make([]domain.Model, 0, len(state.Models)+8)
	seen := map[string]bool{}
	for _, model := range state.Models {
		if !model.InMenu {
			continue
		}
		if providerID != "" && model.ProviderID != providerID {
			continue
		}
		models = append(models, model)
		seen[strings.ToLower(strings.TrimSpace(model.ID))] = true
	}
	if !hasKey {
		return models
	}
	// 覆盖模型与别名也要出现在 /models，避免客户端只认目录里的 slug。
	extras := make([]string, 0, 1+len(matchedKey.ModelAliases))
	if override := strings.TrimSpace(matchedKey.ModelOverride); override != "" {
		extras = append(extras, override)
	}
	for alias := range matchedKey.ModelAliases {
		if trimmed := strings.TrimSpace(alias); trimmed != "" {
			extras = append(extras, trimmed)
		}
	}
	for _, id := range extras {
		key := strings.ToLower(strings.TrimSpace(id))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		contextLen := 0
		pid := providerID
		for _, model := range state.Models {
			if strings.EqualFold(strings.TrimSpace(model.ID), id) {
				contextLen = model.ContextLength
				if pid == "" {
					pid = model.ProviderID
				}
				break
			}
		}
		extra := domain.Model{
			ID:            id,
			ProviderID:    pid,
			ContextLength: contextLen,
			InMenu:        true,
		}
		fillModelTokenBudgets(&extra)
		models = append(models, extra)
	}
	return models
}

func (s *Server) handleOpenAIChat(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	var timingRef *requestTiming
	r, timingRef = attachRequestTiming(r, started)
	// Count client-bound bytes for daily traffic stats on every path (pass-through
	// and all conversions). recordRequestLogEx reads the tally at log time.
	w = timingRef.attachTxCounter(w)
	body, tooLarge, err := readGatewayRequestBody(r)
	if tooLarge {
		writeOpenAIError(w, http.StatusRequestEntityTooLarge, requestBodyTooLargeMessage)
		return
	}
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var req map[string]any
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
	} else {
		req = map[string]any{}
	}
	// Stash the client's own setting now: the logged body is truncated for
	// anything as large as a Claude Code prompt, so the log cannot recover it
	// later. A route/key default overwrites this further down.
	r = withReasoningEffort(r, reasoningLabelFromMap(req))
	// Remember what the client asked for before any conversion rewrites it: a Claude
	// client with stream:false expects one JSON object, and only that shape can have
	// a cut answer continued (see workbuddy_continue.go).
	if clientStream, _ := req["stream"].(bool); !clientStream {
		r = r.WithContext(withWorkBuddyAggregateAnswer(r.Context(), true))
	}

	route, err := s.router.ActiveOpenAIChatRoute()
	var matchedKey domain.APIKey
	var gatewayKeyMatched bool
	if testRouteID, ok := internalTestRouteID(r); ok {
		route, err = s.router.RouteByID(testRouteID)
	} else if token := extractConsumerAPIKey(r); token != "" {
		if key, ok := s.router.APIKeyByToken(token); ok {
			matchedKey = key
			gatewayKeyMatched = true
			if key.RouteID != "" {
				route, err = s.router.RouteByID(key.RouteID)
			}
			if s.apiKeyStore != nil {
				s.apiKeyToucher.Touch(key.ID)
			}
		} else {
			// 带了 Key 但不对：明确拒绝，不落到「没有 Key」的历史默认路由上。
			writeOpenAIError(w, http.StatusUnauthorized, "invalid api key")
			return
		}
	}
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	// 一把 Key 本来就该同时被多种客户端用：每条端点自带协议，按本次请求的
	// 协议判定直通还是转换，不再用 route 上那个「输出协议」把端点钉死。
	decision, err := s.router.DecideForProtocol(route.ID, domain.ProtocolOpenAIChat)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	if gatewayKeyMatched {
		decision = s.decisionForAPIKeyProtocol(route, matchedKey, decision, sessionAffinityKey(r, req, matchedKey.ID, route.ProviderID, matchedKey.ScheduleMode, matchedKey.FallbackProviderIDs...), domain.ProtocolOpenAIChat)
	}

	requestModel, _ := req["model"].(string)
	model, logModel := resolveConsumerModel(s.router, route, matchedKey, gatewayKeyMatched, requestModel)
	r = attachAPIKeyMaxOutputTokens(r, matchedKey, gatewayKeyMatched)
	thinkingOverride := ""
	if gatewayKeyMatched {
		thinkingOverride = matchedKey.ThinkingDepthOverride
	}

	stream, _ := req["stream"].(bool)
	if s.rejectIfStreamDisabledForKey(w, gatewayKeyMatched, matchedKey, stream, domain.ProtocolOpenAIChat) {
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, route.ProviderID, finalizeRequestLogModel(logModel, model, model), "rejected", "stream disabled", r.URL.Path, http.StatusBadRequest, TokenUsage{}, body, []byte(`stream disabled`))
		return
	}
	status := http.StatusOK
	req["model"] = model
	requestDepth, requestHasDepth := req["reasoning_effort"].(string)
	if depth := s.router.ResolveThinkingDepth(route, thinkingOverride, requestHasDepth, requestDepth); depth != "" {
		req["reasoning_effort"] = depth
		r = withReasoningEffort(r, depth)
	}
	if stream && decision.Action == "pass_through" && decision.InputProtocol == domain.ProtocolOpenAIChat {
		req["stream_options"] = map[string]any{"include_usage": true}
	}
	var usage TokenUsage
	var responseLog []byte
	var ttftMs int64
	timing := requestTimingFrom(r.Context())
	if timing != nil {
		timing.markPrepReady()
	}
	status, usage, responseLog, decision, effectiveModel, err := s.executeProtocolFlowWithFailover(wrapTTFTWriterWithTiming(w, started, &ttftMs, timing), r, route, decision, model, req, domain.ProtocolOpenAIChat, gatewayKeyMatched, matchedKey, gatewayKeyMatched)
	logModel = finalizeRequestLogModel(logModel, model, effectiveModel)
	if err != nil {
		if isClientCanceled(r, err) {
			s.logs.AddApp("info", "chat request canceled by client", err.Error())
			s.recordRequestLogFromRequestTTFT(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, decision.Action, decision.ConversionLabel, r.URL.Path, statusClientClosedRequest, usage, ttftMs, body, []byte(err.Error()))
			return
		}
		s.logs.AddApp("error", "chat request failed", err.Error())
		errStatus := upstreamErrorStatus(err)
		s.recordRequestLogFromRequestTTFT(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, decision.Action, decision.ConversionLabel, r.URL.Path, errStatus, usage, ttftMs, body, []byte(err.Error()))
		writeOpenAIError(w, errStatus, err.Error())
		return
	}
	s.recordRequestLogFromRequestTTFT(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, decision.Action, decision.ConversionLabel, r.URL.Path, status, usage, ttftMs, body, responseLog)
	s.logs.AddApp("debug", "handled chat request", fmt.Sprintf("route=%s model=%s status=%d", route.ID, logModel, status))
	slog.Info("handled chat request", "route", route.ID, "model", logModel, "action", decision.Action, "stream", stream)
}

func (s *Server) handleOpenAIResponses(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	var timingRef *requestTiming
	r, timingRef = attachRequestTiming(r, started)
	// Count client-bound bytes for daily traffic stats on every path (pass-through
	// and all conversions). recordRequestLogEx reads the tally at log time.
	w = timingRef.attachTxCounter(w)
	body, tooLarge, err := readGatewayRequestBody(r)
	if tooLarge {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": map[string]any{"message": requestBodyTooLargeMessage, "type": "invalid_request_error"}})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "failed to read request body", "type": "invalid_request_error"}})
		return
	}

	var req map[string]any
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "invalid json: " + err.Error(), "type": "invalid_request_error"}})
			return
		}
	} else {
		req = map[string]any{}
	}
	r = withReasoningEffort(r, reasoningLabelFromMap(req))
	// Remember what the client asked for before any conversion rewrites it: a Claude
	// client with stream:false expects one JSON object, and only that shape can have
	// a cut answer continued (see workbuddy_continue.go).
	if clientStream, _ := req["stream"].(bool); !clientStream {
		r = r.WithContext(withWorkBuddyAggregateAnswer(r.Context(), true))
	}
	// A client may carry a compaction token (minted by our own
	// handleOpenAIResponsesCompact) back into a normal request's input array;
	// expand it into a plain message before anything downstream sees it — see
	// responses_compact.go for why this must run first and unconditionally.
	if items, ok := req["input"].([]any); ok {
		req["input"] = expandCompactionItems(items)
	}

	var route domain.Route
	var matchedKey domain.APIKey
	var gatewayKeyMatched bool
	if testRouteID, ok := internalTestRouteID(r); ok {
		route, err = s.router.RouteByID(testRouteID)
	} else if token := extractConsumerAPIKey(r); token != "" {
		if key, ok := s.router.APIKeyByToken(token); ok {
			matchedKey = key
			gatewayKeyMatched = true
			if key.RouteID != "" {
				route, err = s.router.RouteByID(key.RouteID)
			}
			if s.apiKeyStore != nil {
				s.apiKeyToucher.Touch(key.ID)
			}
		} else {
			// 带了 Key 但不对：明确拒绝，不落到「没有 Key」时的默认路由上。
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"message": "invalid api key", "type": "authentication_error"}})
			return
		}
	}
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"message": err.Error(), "type": "authentication_error"}})
		return
	}
	decision, err := s.router.DecideForProtocol(route.ID, domain.ProtocolOpenAIResponses)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"message": err.Error(), "type": "gateway_error"}})
		return
	}
	if gatewayKeyMatched {
		decision = s.decisionForAPIKeyProtocol(route, matchedKey, decision, sessionAffinityKey(r, req, matchedKey.ID, route.ProviderID, matchedKey.ScheduleMode, matchedKey.FallbackProviderIDs...), domain.ProtocolOpenAIResponses)
	}

	requestModel, _ := req["model"].(string)
	model, logModel := resolveConsumerModel(s.router, route, matchedKey, gatewayKeyMatched, requestModel)
	r = attachAPIKeyMaxOutputTokens(r, matchedKey, gatewayKeyMatched)
	thinkingOverride := ""
	if gatewayKeyMatched {
		thinkingOverride = matchedKey.ThinkingDepthOverride
	}

	stream, _ := req["stream"].(bool)
	if s.rejectIfStreamDisabledForKey(w, gatewayKeyMatched, matchedKey, stream, domain.ProtocolOpenAIResponses) {
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, route.ProviderID, finalizeRequestLogModel(logModel, model, model), "rejected", "stream disabled", r.URL.Path, http.StatusBadRequest, TokenUsage{}, body, []byte(`stream disabled`))
		return
	}
	status := http.StatusOK
	req["model"] = model
	if depth := s.router.ResolveThinkingDepth(route, thinkingOverride, false, ""); depth != "" {
		req["reasoning"] = map[string]any{"effort": depth}
		r = withReasoningEffort(r, depth)
	}
	var usage TokenUsage
	var responseLog []byte
	var ttftMs int64
	timing := requestTimingFrom(r.Context())
	if timing != nil {
		timing.markPrepReady()
	}
	status, usage, responseLog, decision, effectiveModel, err := s.executeProtocolFlowWithFailover(wrapTTFTWriterWithTiming(w, started, &ttftMs, timing), r, route, decision, model, req, domain.ProtocolOpenAIResponses, gatewayKeyMatched, matchedKey, gatewayKeyMatched)
	logModel = finalizeRequestLogModel(logModel, model, effectiveModel)
	if err != nil {
		if isClientCanceled(r, err) {
			s.logs.AddApp("info", "responses request canceled by client", err.Error())
			s.recordRequestLogFromRequestTTFT(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, decision.Action, decision.ConversionLabel, r.URL.Path, statusClientClosedRequest, usage, ttftMs, body, []byte(err.Error()))
			return
		}
		s.logs.AddApp("error", "responses request failed", err.Error())
		errStatus := upstreamErrorStatus(err)
		s.recordRequestLogFromRequestTTFT(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, decision.Action, decision.ConversionLabel, r.URL.Path, errStatus, usage, ttftMs, body, []byte(err.Error()))
		writeJSON(w, errStatus, map[string]any{"error": map[string]any{"message": err.Error(), "type": "gateway_error"}})
		return
	}
	s.recordRequestLogFromRequestTTFT(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, decision.Action, decision.ConversionLabel, r.URL.Path, status, usage, ttftMs, body, responseLog)
	s.logs.AddApp("debug", "handled responses request", fmt.Sprintf("route=%s model=%s status=%d", route.ID, logModel, status))
	slog.Info("handled responses request", "route", route.ID, "model", logModel, "action", decision.Action, "stream", stream)
}

// handleOpenAIImagesEdits serves POST /v1/images/edits.
//
// It is the generation handler with one difference: which upstream route the render
// goes to. Everything else — key matching, route resolution, refusals — is identical,
// so delegating keeps the two from drifting.
func (s *Server) handleOpenAIImagesEdits(w http.ResponseWriter, r *http.Request) {
	s.handleOpenAIImagesGenerations(w, r.WithContext(withWorkBuddyImagesEdits(r.Context())))
}

func (s *Server) handleOpenAIImagesGenerations(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var req map[string]any
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
	} else {
		req = map[string]any{}
	}

	var route domain.Route
	var matchedKey domain.APIKey
	var gatewayKeyMatched bool
	if testRouteID, ok := internalTestRouteID(r); ok {
		route, err = s.router.RouteByID(testRouteID)
	} else if token := extractConsumerAPIKey(r); token != "" {
		if key, ok := s.router.APIKeyByToken(token); ok {
			matchedKey = key
			gatewayKeyMatched = true
			if key.RouteID != "" {
				route, err = s.router.RouteByID(key.RouteID)
			} else {
				err = fmt.Errorf("api key %q has no route configured", key.ID)
			}
			if s.apiKeyStore != nil {
				s.apiKeyToucher.Touch(key.ID)
			}
		} else {
			err = fmt.Errorf("invalid api key")
		}
	} else {
		err = fmt.Errorf("missing api key")
	}
	if err != nil {
		writeOpenAIError(w, http.StatusUnauthorized, err.Error())
		return
	}

	// 出图只有一条腿，没有 failover 兜底，也只会原样转发 OpenAI Images 请求：
	// 在动任何凭证、发任何请求之前，先把不可能的落点拒掉。路由级的检查先做，
	// Provider 级的检查留到真正落点定下来以后再校验（见下方），因为出图可能
	// 落在兜底 Provider 而不是路由默认值上。
	if refusal, ok := imagesRouteRefusal(s.router, route); ok {
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, route.ProviderID, "", "pass_through", "images/generations", r.URL.Path, refusal.status, TokenUsage{}, body, []byte(refusal.message))
		writeOpenAIError(w, refusal.status, refusal.message)
		return
	}

	// 普通用户的 Key 只能落在管理员此刻仍授权给它的 Provider 上。出图没有
	// failover 流程，授权在这里重新校验一次，不能只信创建 Key 时的结果。
	var decision domain.RouteDecision
	if gatewayKeyMatched {
		providerID := s.imagesEffectiveProvider(matchedKey, route)
		if providerID == "" {
			// Nothing on the chain is left to send to: revoked for a normal
			// user (403), disabled or deleted for an admin key (503).
			status := http.StatusServiceUnavailable
			message := "no enabled provider on the key's provider chain"
			if strings.TrimSpace(matchedKey.OwnerUserID) != "" {
				status = http.StatusForbidden
				message = fmt.Sprintf("provider %q is not assigned to your account", strings.TrimSpace(route.ProviderID))
			}
			s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, route.ProviderID, "", "pass_through", "images/generations", r.URL.Path, status, TokenUsage{}, body, []byte(message))
			writeOpenAIError(w, status, message)
			return
		}
		decision, err = s.router.DecideForProvider(route.ID, providerID)
	} else {
		decision, err = s.router.Decide(route.ID)
	}
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	// 被真正使用的 Provider（decision），不是路由默认值：出图没有兜底，
	// 禁用、已删除或协议不支持的落点必须当场拒绝。
	if refusal, ok := imagesProviderRefusal(s.router, decision.ProviderID); ok {
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, "", "pass_through", "images/generations", r.URL.Path, refusal.status, TokenUsage{}, body, []byte(refusal.message))
		writeOpenAIError(w, refusal.status, refusal.message)
		return
	}

	provider, err := s.router.ProviderByID(decision.ProviderID)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}

	// 模型别名、映射、默认值都属于真正收到请求的那个 Provider，不是路由默认值。
	requestModel, _ := req["model"].(string)
	model, logModel := resolveConsumerModelOnProvider(provider, matchedKey, gatewayKeyMatched, requestModel)
	if strings.TrimSpace(model) == "" {
		model = "gpt-image-2"
		if strings.TrimSpace(logModel) == "" || logModel == "request-model-not-set" {
			logModel = model
		}
	}
	logModel = finalizeRequestLogModel(logModel, model, model)
	req["model"] = model
	body, err = json.Marshal(req)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "failed to encode request body")
		return
	}

	// 图像生成也计入密钥额度：不拦住的话，成本上限对会用 images 的客户端只是个建议。
	// 这条路径不走 failover 流程，所以额度拒绝由本处理器自己写出响应。
	if gatewayKeyMatched && keyHasQuota(matchedKey) {
		admission := s.admitKeyQuota(matchedKey, quotaPromptEstimate(body), time.Now())
		if refusal := admission.refusal; refusal != nil {
			rejectKeyQuota(w, domain.ProtocolOpenAIChat, refusal)
			s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, "pass_through", "images/generations", r.URL.Path, http.StatusTooManyRequests, TokenUsage{}, body, []byte(refusal.Message))
			return
		}
		defer admission.release()
	}

	status, usage, responseLog, err := s.proxyOpenAIImages(w, r, provider, model, body, route.OutputProtocol)
	if err != nil {
		// A refusal at admission is the gateway's 429, not an upstream 502: the
		// account is busy, nothing was sent.
		failureStatus := http.StatusBadGateway
		if errors.Is(err, errProviderSaturated) {
			failureStatus = http.StatusTooManyRequests
		}
		s.logs.AddApp("error", "image generation request failed", err.Error())
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, "pass_through", "images/generations", r.URL.Path, failureStatus, usage, body, []byte(err.Error()))
		writeOpenAIError(w, failureStatus, err.Error())
		return
	}
	s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, "pass_through", "images/generations", r.URL.Path, status, usage, body, responseLog)
	s.logs.AddApp("debug", "handled image generation request", fmt.Sprintf("route=%s model=%s status=%d", route.ID, logModel, status))
	slog.Info("handled image generation request", "route", route.ID, "model", logModel, "status", status)
}

// openAIPassThrough is one OpenAI-compatible endpoint the gateway relays
// without converting anything: the request is already in the upstream's shape
// and only an OpenAI-compatible provider can answer it.
type openAIPassThrough struct {
	// label names the endpoint in request logs and app logs ("embeddings",
	// "audio/transcriptions").
	label string
	// upstreamPath is appended to the provider's BaseURL ("/embeddings").
	upstreamPath string
	// multipart marks a form request (audio uploads): the body is relayed byte
	// for byte with its own boundary, so the vendor sees exactly what the client
	// sent and the answer keeps its type (json, text, srt, vtt).
	multipart bool
}

// handleOpenAIEmbeddings serves POST /v1/embeddings. Embeddings have no
// failover leg and no protocol conversion — the request is OpenAI-shaped and
// only an OpenAI-compatible upstream can answer it — so this is a pass-through
// with the same admission, key quota and logging as the chat path.
func (s *Server) handleOpenAIEmbeddings(w http.ResponseWriter, r *http.Request) {
	s.serveOpenAIPassThrough(w, r, openAIPassThrough{label: "embeddings", upstreamPath: "/embeddings"})
}

// handleOpenAIAudioTranscriptions serves POST /v1/audio/transcriptions
// (speech-to-text). Same trust model as embeddings: the provider's own
// credential goes upstream, the caller's gateway key never does.
func (s *Server) handleOpenAIAudioTranscriptions(w http.ResponseWriter, r *http.Request) {
	s.serveOpenAIPassThrough(w, r, openAIPassThrough{label: "audio/transcriptions", upstreamPath: "/audio/transcriptions", multipart: true})
}

// handleOpenAIAudioTranslations serves POST /v1/audio/translations.
func (s *Server) handleOpenAIAudioTranslations(w http.ResponseWriter, r *http.Request) {
	s.serveOpenAIPassThrough(w, r, openAIPassThrough{label: "audio/translations", upstreamPath: "/audio/translations", multipart: true})
}

// serveOpenAIPassThrough is the one path all of the above share: read the
// body, resolve the key's route, honour the route's output protocol, admit
// against the key's quota and the account's lane, then relay to the provider.
func (s *Server) serveOpenAIPassThrough(w http.ResponseWriter, r *http.Request, endpoint openAIPassThrough) {
	started := time.Now()
	body, tooLarge, err := readGatewayRequestBody(r)
	if tooLarge {
		writeOpenAIError(w, http.StatusRequestEntityTooLarge, requestBodyTooLargeMessage)
		return
	}
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	requestModel, req, err := passthroughRequestModel(r.Header.Get("Content-Type"), body, endpoint.multipart)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}

	var route domain.Route
	var matchedKey domain.APIKey
	var gatewayKeyMatched bool
	if testRouteID, ok := internalTestRouteID(r); ok {
		route, err = s.router.RouteByID(testRouteID)
	} else if token := extractConsumerAPIKey(r); token != "" {
		if key, ok := s.router.APIKeyByToken(token); ok {
			matchedKey = key
			gatewayKeyMatched = true
			if key.RouteID != "" {
				route, err = s.router.RouteByID(key.RouteID)
			} else {
				err = fmt.Errorf("api key %q has no route configured", key.ID)
			}
			if s.apiKeyStore != nil {
				s.apiKeyToucher.Touch(key.ID)
			}
		} else {
			err = fmt.Errorf("invalid api key")
		}
	} else {
		err = fmt.Errorf("missing api key")
	}
	if err != nil {
		writeOpenAIError(w, http.StatusUnauthorized, err.Error())
		return
	}

	// 嵌入和音频不是「协议」，是独立的端点能力：它们没有 Claude / Responses
	// 版本可转换，所以这条链路的输出协议必须真的是 OpenAI Chat。这和聊天入口
	// 的多协议放宽是两回事——那边每条端点都自带协议，这边只有一种形状。
	if gatewayKeyMatched && route.OutputProtocol != domain.ProtocolOpenAIChat {
		writeRouteProtocolMismatch(w, route, domain.ProtocolOpenAIChat)
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, route.ProviderID, "", "rejected", route.OutputProtocol.DisplayName()+" endpoint mismatch", r.URL.Path, http.StatusBadRequest, TokenUsage{}, body, routeProtocolMismatchBody(route, domain.ProtocolOpenAIChat))
		return
	}

	decision, err := s.router.DecideForProtocol(route.ID, domain.ProtocolOpenAIChat)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}

	provider, err := s.router.ProviderByID(decision.ProviderID)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}

	model, logModel := resolveConsumerModelOnProvider(provider, matchedKey, gatewayKeyMatched, requestModel)
	upstreamBody := body
	contentType := "application/json"
	if endpoint.multipart {
		logModel = finalizeRequestLogModel(logModel, model, requestModel)
		// The form's own type (with its boundary) travels as sent; only the
		// model field is rewritten, and only when the provider maps it.
		contentType = r.Header.Get("Content-Type")
		if mapped := applyProviderModelMapping(provider, requestModel); mapped != "" && mapped != strings.TrimSpace(requestModel) {
			upstreamBody, err = multipartFormWithField(contentType, body, "model", mapped)
			if err != nil {
				writeOpenAIError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
	} else {
		if strings.TrimSpace(model) == "" {
			writeOpenAIError(w, http.StatusBadRequest, "model is required")
			return
		}
		logModel = finalizeRequestLogModel(logModel, model, model)
		req["model"] = model
		upstreamBody, err = json.Marshal(req)
		if err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "failed to encode request body")
			return
		}
	}

	if gatewayKeyMatched && keyHasQuota(matchedKey) {
		admission := s.admitKeyQuota(matchedKey, quotaPromptEstimate(body), time.Now())
		if refusal := admission.refusal; refusal != nil {
			rejectKeyQuota(w, domain.ProtocolOpenAIChat, refusal)
			s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, "pass_through", endpoint.label, r.URL.Path, http.StatusTooManyRequests, TokenUsage{}, body, []byte(refusal.Message))
			return
		}
		defer admission.release()
	}

	releaseSlot, admissionErr := s.acquireProviderSlot(r.Context(), provider)
	if admissionErr != nil {
		w.Header().Set("Retry-After", strconv.Itoa(s.providerLanes.retryAfterSecondsFor(provider, time.Now())))
		status := http.StatusBadGateway
		if errors.Is(admissionErr, errProviderSaturated) {
			status = http.StatusTooManyRequests
		}
		writeOpenAIError(w, status, admissionErr.Error())
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, "pass_through", endpoint.label, r.URL.Path, status, TokenUsage{}, body, []byte(admissionErr.Error()))
		return
	}
	defer releaseSlot()

	// Only the provider's own credential goes upstream. Forwarding the caller's
	// gateway key would hand our own secret to the vendor.
	authValue := resolveProviderAuth(provider)
	if strings.TrimSpace(authValue) == "" {
		msg := fmt.Sprintf("provider %q has no API key configured", provider.ID)
		writeOpenAIError(w, http.StatusBadGateway, msg)
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, "pass_through", endpoint.label, r.URL.Path, http.StatusBadGateway, TokenUsage{}, body, []byte(msg))
		return
	}
	if !endpoint.multipart {
		upstreamBody, err = applyRequestAdapterBody(provider, model, upstreamBody)
		if err != nil {
			writeOpenAIError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	// A WorkBuddy account answers this endpoint on its own route with the desktop
	// header family; the generic path below would post to a doubled /v2 prefix and
	// carry only the bearer (see workbuddy_endpoints.go).
	if _, isWorkBuddyEndpoint := workBuddyRenderSite(provider); isWorkBuddyEndpoint && endpoint.upstreamPath == "/embeddings" {
		status, usage, responseLog, err := s.executeWorkBuddyEndpoint(w, r, provider, workBuddyEmbeddingsPath, upstreamBody)
		if err != nil {
			s.logs.AddApp("error", endpoint.label+" request failed", err.Error())
			s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, "pass_through", endpoint.label, r.URL.Path, http.StatusBadGateway, usage, body, []byte(err.Error()))
			writeOpenAIError(w, http.StatusBadGateway, err.Error())
			return
		}
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, "pass_through", endpoint.label, r.URL.Path, status, usage, body, responseLog)
		s.logs.AddApp("debug", "handled "+endpoint.label+" request", fmt.Sprintf("route=%s model=%s status=%d", route.ID, logModel, status))
		return
	}
	upstreamURL := resolveProviderEndpointURL(provider, endpoint.upstreamPath)
	status, usage, responseLog, err := s.proxyOpenAIImagesUpstream(w, r.Context(), upstreamURL, authValue, contentType, upstreamBody)
	if err != nil {
		s.logs.AddApp("error", endpoint.label+" request failed", err.Error())
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, "pass_through", endpoint.label, r.URL.Path, http.StatusBadGateway, usage, body, []byte(err.Error()))
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, "pass_through", endpoint.label, r.URL.Path, status, usage, body, responseLog)
	level := "debug"
	if status >= http.StatusBadRequest {
		// 上游拒绝要能在日志里看见：很多上游对不认识的模型回空体 404，
		// debug 级别会让"到底是谁 404"彻底无从查起。
		level = "warn"
	}
	s.logs.AddApp(level, "handled "+endpoint.label+" request", fmt.Sprintf("route=%s model=%s status=%d", route.ID, logModel, status))
}

// passthroughRequestModel reads the client's model out of a passthrough body.
// A JSON endpoint's body is parsed into the map every later step rewrites; a
// multipart form is left untouched and only its "model" field is read, so the
// audio part goes upstream exactly as the client sent it.
func passthroughRequestModel(contentType string, body []byte, multipart bool) (string, map[string]any, error) {
	if multipart {
		model, err := multipartFormField(contentType, body, "model")
		if err != nil {
			return "", nil, err
		}
		return model, nil, nil
	}
	req := map[string]any{}
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return "", nil, fmt.Errorf("invalid json: %w", err)
		}
	}
	model, _ := req["model"].(string)
	return model, req, nil
}

// handleClaudeMessages serves POST /anthropic/v1/messages, the native
// Anthropic Messages API pass-through endpoint, mirroring handleOpenAIChat's
// route-resolution pattern (test header or consumer API key -> router.Decide).
func (s *Server) handleClaudeMessages(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	var timingRef *requestTiming
	r, timingRef = attachRequestTiming(r, started)
	// Count client-bound bytes for daily traffic stats on every path (pass-through
	// and all conversions). recordRequestLogEx reads the tally at log time.
	w = timingRef.attachTxCounter(w)
	body, tooLarge, err := readGatewayRequestBody(r)
	if tooLarge {
		writeClaudeError(w, http.StatusRequestEntityTooLarge, requestBodyTooLargeMessage)
		return
	}
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var req map[string]any
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
	} else {
		req = map[string]any{}
	}
	r = withReasoningEffort(r, reasoningLabelFromMap(req))
	// Remember what the client asked for before any conversion rewrites it: a Claude
	// client with stream:false expects one JSON object, and only that shape can have
	// a cut answer continued (see workbuddy_continue.go).
	if clientStream, _ := req["stream"].(bool); !clientStream {
		r = r.WithContext(withWorkBuddyAggregateAnswer(r.Context(), true))
	}

	var route domain.Route
	var matchedKey domain.APIKey
	var gatewayKeyMatched bool
	if testRouteID, ok := internalTestRouteID(r); ok {
		route, err = s.router.RouteByID(testRouteID)
	} else if token := extractConsumerAPIKey(r); token != "" {
		if key, ok := s.router.APIKeyByToken(token); ok {
			matchedKey = key
			gatewayKeyMatched = true
			if key.RouteID != "" {
				route, err = s.router.RouteByID(key.RouteID)
			} else {
				err = fmt.Errorf("api key %q has no route configured", key.ID)
			}
			if s.apiKeyStore != nil {
				s.apiKeyToucher.Touch(key.ID)
			}
		} else {
			err = fmt.Errorf("invalid api key")
		}
	} else {
		err = fmt.Errorf("missing api key")
	}
	if err != nil {
		writeOpenAIError(w, http.StatusUnauthorized, err.Error())
		return
	}
	decision, err := s.router.DecideForProtocol(route.ID, domain.ProtocolClaude)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	if gatewayKeyMatched {
		decision = s.decisionForAPIKeyProtocol(route, matchedKey, decision, sessionAffinityKey(r, req, matchedKey.ID, route.ProviderID, matchedKey.ScheduleMode, matchedKey.FallbackProviderIDs...), domain.ProtocolClaude)
	}

	requestModel, _ := req["model"].(string)
	model, logModel := resolveConsumerModel(s.router, route, matchedKey, gatewayKeyMatched, requestModel)
	r = attachAPIKeyMaxOutputTokens(r, matchedKey, gatewayKeyMatched)
	// 自助注册的上游是用户自己的服务：它要什么思考档位，客户端的请求已经说清楚
	// 了。网关拿自己的档位表（以及 budget<->effort 的反推）去改写，等于替一个
	// 我们并不认识的上游改主意——见下面 applyClaudeEffort 处同一判断。
	selfRegistered := s.router.providerIsSelfRegistered(route.ProviderID)
	thinkingOverride := ""
	if gatewayKeyMatched {
		thinkingOverride = matchedKey.ThinkingDepthOverride
	}

	stream, _ := req["stream"].(bool)
	if s.rejectIfStreamDisabledForKey(w, gatewayKeyMatched, matchedKey, stream, domain.ProtocolClaude) {
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, route.ProviderID, finalizeRequestLogModel(logModel, model, model), "rejected", "stream disabled", r.URL.Path, http.StatusBadRequest, TokenUsage{}, body, []byte(`stream disabled`))
		return
	}
	status := http.StatusOK
	req["model"] = model
	// Key 的思考深度与路由的 Provider 默认档位在 Claude 路径上同样要生效，
	// 判定口径与 Chat 路径一致：Key 覆盖优先，否则用客户端要求的档位。
	requestDepth := claudePayloadEffort(req)
	if depth := s.router.ResolveThinkingDepth(route, thinkingOverride, requestDepth != "", requestDepth); depth != "" && !selfRegistered {
		if applyClaudeEffort(req, model, depth) {
			r = withReasoningEffort(r, fitModelEffort(depth, model))
		}
	}
	var usage TokenUsage
	var responseLog []byte
	var ttftMs int64
	timing := requestTimingFrom(r.Context())
	if timing != nil {
		timing.markPrepReady()
	}
	status, usage, responseLog, decision, effectiveModel, err := s.executeProtocolFlowWithFailover(wrapTTFTWriterWithTiming(w, started, &ttftMs, timing), r, route, decision, model, req, domain.ProtocolClaude, gatewayKeyMatched, matchedKey, gatewayKeyMatched)
	logModel = finalizeRequestLogModel(logModel, model, effectiveModel)
	if err != nil {
		if isClientCanceled(r, err) {
			s.logs.AddApp("info", "claude messages canceled by client", err.Error())
			s.recordRequestLogFromRequestTTFT(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, decision.Action, decision.ConversionLabel, r.URL.Path, statusClientClosedRequest, usage, ttftMs, body, []byte(err.Error()))
			return
		}
		s.logs.AddApp("error", "claude messages request failed", err.Error())
		errStatus := upstreamErrorStatus(err)
		s.recordRequestLogFromRequestTTFT(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, decision.Action, decision.ConversionLabel, r.URL.Path, errStatus, usage, ttftMs, body, []byte(err.Error()))
		writeClaudeError(w, errStatus, err.Error())
		return
	}
	s.recordRequestLogFromRequestTTFT(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, decision.Action, decision.ConversionLabel, r.URL.Path, status, usage, ttftMs, body, responseLog)
	s.logs.AddApp("debug", "handled claude messages request", fmt.Sprintf("route=%s model=%s status=%d stream=%v", route.ID, logModel, status, stream))
}

// handleClaudeCountTokens proxies Anthropic's token counting endpoint, which
// Claude Code uses during startup to validate the selected model.
func (s *Server) handleClaudeCountTokens(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	body, tooLarge, err := readGatewayRequestBody(r)
	if tooLarge {
		writeClaudeError(w, http.StatusRequestEntityTooLarge, requestBodyTooLargeMessage)
		return
	}
	if err != nil {
		writeClaudeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var req map[string]any
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeClaudeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
	} else {
		req = map[string]any{}
	}

	route, matchedKey, gatewayKeyMatched, err := s.resolveClaudeConsumerRoute(r)
	if err != nil {
		writeClaudeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	decision, err := s.router.DecideForProtocol(route.ID, domain.ProtocolClaude)
	if err != nil {
		writeClaudeError(w, http.StatusBadGateway, err.Error())
		return
	}
	// count_tokens 不走 executeProtocolFlowWithFailover，也就不会经过那里对
	// 「普通用户的 Key 只能用当前仍授权的 Provider」的逐请求复核：撤销授权后，
	// 旧 Key 仍然能拿 token 计数（以及上游账号的额度）。这里补上同一条判定，
	// 并把 decision 收窄到仍被授权的 Provider。
	if gatewayKeyMatched {
		decision = s.decisionForAPIKeyProtocol(route, matchedKey, decision, sessionAffinityKey(r, req, matchedKey.ID, route.ProviderID, matchedKey.ScheduleMode, matchedKey.FallbackProviderIDs...), domain.ProtocolClaude)
		if decision.ProviderID == route.ProviderID && !s.providerAllowedForKeyOwner(matchedKey, decision.ProviderID) {
			if strings.TrimSpace(matchedKey.OwnerUserID) == "" {
				writeClaudeError(w, http.StatusServiceUnavailable, "no enabled provider on the key's provider chain")
				return
			}
			writeClaudeError(w, http.StatusForbidden, fmt.Sprintf("provider %q is not assigned to your account", decision.ProviderID))
			return
		}
	}

	requestModel, _ := req["model"].(string)
	model, logModel := resolveConsumerModel(s.router, route, matchedKey, gatewayKeyMatched, requestModel)
	if model == "" || model == "request-model-not-set" {
		writeClaudeError(w, http.StatusBadRequest, "model is required")
		return
	}
	logModel = finalizeRequestLogModel(logModel, model, model)
	req["model"] = model
	upstreamBody, err := json.Marshal(req)
	if err != nil {
		writeClaudeError(w, http.StatusBadRequest, err.Error())
		return
	}

	provider, err := s.router.ProviderByID(decision.ProviderID)
	if err != nil {
		writeClaudeError(w, http.StatusBadGateway, err.Error())
		return
	}
	// count_tokens 是 Claude Code 启动/上下文估算用的旁路，不是对话主链路。
	// 某些 OpenAI 协议上游没有 Anthropic 的 count_tokens 端点，本地返回一个
	// 计数占位即可；日志也标成本地计数，别让运维误以为真的发生了协议透传。
	action := "pass_through"
	protocolFlow := decision.ConversionLabel
	if shouldStubClaudeCountTokens(provider) {
		action = "count_tokens_stub"
		protocolFlow = fmt.Sprintf("%s -> %s", domain.ProtocolClaude.DisplayName(), domain.ProtocolClaude.DisplayName())
	}
	status, responseBody, err := s.proxyClaudeCountTokens(r, provider, upstreamBody)
	if err != nil {
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, action, protocolFlow, r.URL.Path, http.StatusBadGateway, TokenUsage{}, body, []byte(err.Error()))
		writeClaudeError(w, http.StatusBadGateway, err.Error())
		return
	}
	for key, values := range map[string]string{"Content-Type": "application/json"} {
		w.Header().Set(key, values)
	}
	w.WriteHeader(status)
	_, _ = w.Write(responseBody)
	s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, action, protocolFlow, r.URL.Path, status, TokenUsage{}, body, responseBody)
}

func (s *Server) resolveClaudeConsumerRoute(r *http.Request) (domain.Route, domain.APIKey, bool, error) {
	if testRouteID, ok := internalTestRouteID(r); ok {
		route, err := s.router.RouteByID(testRouteID)
		return route, domain.APIKey{}, false, err
	}
	token := extractConsumerAPIKey(r)
	if token == "" {
		return domain.Route{}, domain.APIKey{}, false, fmt.Errorf("missing api key")
	}
	key, ok := s.router.APIKeyByToken(token)
	if !ok {
		return domain.Route{}, domain.APIKey{}, false, fmt.Errorf("invalid api key")
	}
	if key.RouteID == "" {
		return domain.Route{}, domain.APIKey{}, false, fmt.Errorf("api key %q has no route configured", key.ID)
	}
	route, err := s.router.RouteByID(key.RouteID)
	if err != nil {
		return domain.Route{}, domain.APIKey{}, false, err
	}
	if s.apiKeyStore != nil {
		s.apiKeyToucher.Touch(key.ID)
	}
	return route, key, true, nil
}

func (s *Server) proxyClaudeCountTokens(r *http.Request, provider domain.Provider, body []byte) (int, []byte, error) {
	if shouldStubClaudeCountTokens(provider) {
		stub, _ := json.Marshal(map[string]any{"input_tokens": 1})
		return http.StatusOK, stub, nil
	}
	// Claude Code /v1/messages/count_tokens must not carry generation fields.
	// Clients sometimes include max_tokens; our Messages normalize path also
	// injects a default — both yield: "max_tokens: Extra inputs are not permitted".
	body = sanitizeClaudeCountTokensBody(body)
	isOAuth := provider.AuthType == domain.AuthTypeClaudeOAuth
	var request *http.Request
	var err error
	if isOAuth {
		refreshed, refreshErr := s.ensureFreshClaudeToken(provider)
		if refreshErr != nil {
			return 0, nil, refreshErr
		}
		provider = refreshed
		request, _, err = buildClaudeOAuthRequest(r.Context(), provider, body, r.Header.Get("anthropic-beta"), true, claudeCountTokensURL)
		if err != nil {
			return 0, nil, err
		}
	} else {
		upstreamURL := strings.TrimSpace(provider.BaseURL)
		if upstreamURL == "" {
			upstreamURL = claudeCountTokensURL
		} else if !strings.Contains(strings.ToLower(upstreamURL), "count_tokens") {
			upstreamURL = strings.TrimRight(upstreamURL, "/")
			if strings.HasSuffix(strings.ToLower(upstreamURL), "/messages") {
				upstreamURL += "/count_tokens"
			} else {
				upstreamURL += "/v1/messages/count_tokens"
			}
		}
		request, err = http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json")
		request.Header.Set("anthropic-version", "2023-06-01")
		applyProviderAuth(request, provider, r.Header.Get("Authorization"))
	}

	client := &http.Client{Timeout: 60 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	responseBody, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return 0, nil, readErr
	}
	return response.StatusCode, responseBody, nil
}

// shouldStubClaudeCountTokens 判断这台上游是否需要网关本地模拟 Anthropic
// count_tokens。这些上游没有可用的 /v1/messages/count_tokens，硬拼 URL 只会
// 得到 404；Claude Code 只需要一个能通过启动检查的输入 token 占位。
func shouldStubClaudeCountTokens(provider domain.Provider) bool {
	return provider.AuthType == domain.AuthTypeCursorOAuth ||
		isQoderProvider(provider) ||
		isWorkBuddyProvider(provider) ||
		isOpenCodeZenProvider(provider) ||
		isOpenCodeGoProvider(provider) ||
		isZCodeProvider(provider) ||
		isTraeProvider(provider) ||
		(provider.Protocol == domain.ProtocolOpenAIChat && strings.TrimSpace(provider.BaseURL) == "")
}

type providerTestResult struct {
	Success   bool           `json:"success"`
	Provider  string         `json:"providerId"`
	ModelsURL string         `json:"modelsUrl"`
	Status    int            `json:"status,omitempty"`
	LatencyMs int64          `json:"latencyMs"`
	Models    []domain.Model `json:"models"`
	Error     string         `json:"error,omitempty"`
	Preview   string         `json:"preview,omitempty"`
}

func (s *Server) fetchProviderModels(r *http.Request, provider domain.Provider, started time.Time) providerTestResult {
	if provider.AuthType == domain.AuthTypePeer {
		// 对端 provider 不是一台"上游服务"，而是父网关的一扇门：它的模型清单
		// 来自 /__peer/providers，父网关根本不提供 baseUrl + /models 这个端点。
		// 走下面通用的模型发现只会打到父网关的一个 404，然后把这个本来完全
		// 正常的 provider 标成"异常"、清空它可用的模型清单。
		return s.fetchPeerUpstreamModels(r, provider, started)
	}
	// {model} 模板（Azure OpenAI deployment 最典型）不是可枚举的模型目录：
	// deployment 已经写在路径里，请求 /models 要么 400，要么返回的不是这个
	// deployment。此时“获取模型”应沿用 Provider 上已经配置的模型目录，不能把
	// 一个对话完全正常的 Provider 标成 degraded。
	if provider.AuthType == domain.AuthTypeAPIKey && strings.Contains(provider.BaseURL, "{model}") {
		if models := declaredProviderModels(provider); len(models) > 0 {
			return providerTestResult{
				Success:   true,
				Provider:  provider.ID,
				ModelsURL: provider.BaseURL,
				Status:    http.StatusOK,
				LatencyMs: time.Since(started).Milliseconds(),
				Models:    models,
				Preview:   "Base URL 使用 {model} 模板，沿用已配置的模型目录",
			}
		}
	}
	if provider.AuthType == domain.AuthTypeCursorOAuth {
		baseURL, refreshed, err := s.resolveCursorBridgeBaseURL(r.Context(), provider)
		if err != nil {
			return providerTestResult{Success: false, Provider: provider.ID, LatencyMs: time.Since(started).Milliseconds(), Error: err.Error(), Models: []domain.Model{}}
		}
		provider = refreshed
		modelsURL := strings.TrimRight(baseURL, "/") + "/models?refresh=1"
		request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, modelsURL, nil)
		if err != nil {
			return providerTestResult{Success: false, Provider: provider.ID, ModelsURL: modelsURL, LatencyMs: time.Since(started).Milliseconds(), Error: err.Error(), Models: []domain.Model{}}
		}
		request.Header.Set("Accept", "application/json")
		client := &http.Client{Timeout: 30 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			return providerTestResult{Success: false, Provider: provider.ID, ModelsURL: modelsURL, LatencyMs: time.Since(started).Milliseconds(), Error: err.Error(), Models: []domain.Model{}}
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(response.Body, 256*1024))
		preview := strings.TrimSpace(strings.ReplaceAll(string(body), "\n", " "))
		if len(preview) > 900 {
			preview = preview[:900]
		}
		models, parseErr := parseModelsResponse(body, provider)
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return providerTestResult{Success: false, Provider: provider.ID, ModelsURL: modelsURL, Status: response.StatusCode, LatencyMs: time.Since(started).Milliseconds(), Error: fmt.Sprintf("models endpoint returned HTTP %d", response.StatusCode), Preview: preview, Models: []domain.Model{}}
		}
		if parseErr != nil {
			return providerTestResult{Success: false, Provider: provider.ID, ModelsURL: modelsURL, Status: response.StatusCode, LatencyMs: time.Since(started).Milliseconds(), Error: parseErr.Error(), Preview: preview, Models: []domain.Model{}}
		}
		return providerTestResult{Success: true, Provider: provider.ID, ModelsURL: modelsURL, Status: response.StatusCode, LatencyMs: time.Since(started).Milliseconds(), Models: models, Preview: preview}
	}

	if isQoderProvider(provider) {
		refreshed, err := s.ensureFreshQoderToken(provider)
		if err != nil {
			return providerTestResult{Success: false, Provider: provider.ID, LatencyMs: time.Since(started).Milliseconds(), Error: err.Error(), Models: []domain.Model{}}
		}
		provider = refreshed
		// Qoder's models are the ones its listing gives this account, and the
		// listing is also where a request's per-model configuration comes from,
		// so syncing is the fetch — not a /models route, which Qoder has none of.
		synced, syncErr := s.syncQoderProviderModels(provider.ID)
		if syncErr != nil {
			return providerTestResult{Success: false, Provider: provider.ID, LatencyMs: time.Since(started).Milliseconds(), Error: syncErr.Error(), Models: []domain.Model{}}
		}
		return providerTestResult{
			Success:   true,
			Provider:  provider.ID,
			LatencyMs: time.Since(started).Milliseconds(),
			Models:    synced.Models,
			Preview:   "qoder 账号自己的模型列表（/algo/api/v2/model/list）",
		}
	}

	if isZCodeProvider(provider) {
		site, _ := zcodeSiteForProvider(provider)
		if provider.ZCode == nil || provider.ZCode.Disconnected {
			return providerTestResult{Success: false, Provider: provider.ID, LatencyMs: time.Since(started).Milliseconds(), Error: "ZCode 尚未登录", Models: []domain.Model{}}
		}
		// The plan's models come from ZCode's release config, not from its
		// Anthropic endpoint, which serves no model list.
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		models, err := fetchZCodeModels(ctx, site)
		if err != nil {
			models = zcodeFallbackModels(provider.ID)
		}
		for index := range models {
			models[index].ProviderID = provider.ID
		}
		return providerTestResult{Success: true, Provider: provider.ID, ModelsURL: zcodeSiteHost + zcodeConfigsPath, LatencyMs: time.Since(started).Milliseconds(), Models: models, Preview: "ZCode " + site.label + " 发布配置"}
	}

	if isTraeProvider(provider) {
		if provider.Trae == nil || provider.Trae.Disconnected {
			return providerTestResult{Success: false, Provider: provider.ID, LatencyMs: time.Since(started).Milliseconds(), Error: "Trae 尚未登录", Models: []domain.Model{}}
		}
		refreshed, err := s.ensureFreshTraeToken(provider)
		if err != nil {
			return providerTestResult{Success: false, Provider: provider.ID, LatencyMs: time.Since(started).Milliseconds(), Error: err.Error(), Models: []domain.Model{}}
		}
		site, _ := traeSiteOf(refreshed)
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		models, err := s.fetchTraeModels(ctx, refreshed)
		if err != nil {
			// Trae answers no list for an account whose session just lapsed,
			// and an empty card is worse than the realm's known models.
			models = traeFallbackModels(site, provider.ID)
		}
		for index := range models {
			models[index].ProviderID = provider.ID
		}
		return providerTestResult{Success: true, Provider: provider.ID, ModelsURL: site.api + traeModelsBatchPath, LatencyMs: time.Since(started).Milliseconds(), Models: models, Preview: site.label + " 账号的模型列表"}
	}

	if isWorkBuddyProvider(provider) {
		site, _ := workBuddySiteForAuthType(provider.AuthType)
		refreshed, err := s.ensureFreshWorkBuddyToken(provider)
		if err != nil {
			return providerTestResult{Success: false, Provider: provider.ID, LatencyMs: time.Since(started).Milliseconds(), Error: err.Error(), Models: []domain.Model{}}
		}
		provider = refreshed
		// The plan's model list comes from its product config, not a /models
		// route: WorkBuddy's OpenAI-compatible /v2 does not serve one.
		modelsURL := site.api() + workBuddyConfigPath
		if provider.WorkBuddy == nil {
			return providerTestResult{Success: false, Provider: provider.ID, ModelsURL: modelsURL, LatencyMs: time.Since(started).Milliseconds(), Error: "WorkBuddy 尚未登录", Models: []domain.Model{}}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		models, err := fetchWorkBuddyModels(ctx, site, *provider.WorkBuddy)
		if err != nil {
			return providerTestResult{Success: false, Provider: provider.ID, ModelsURL: modelsURL, Status: http.StatusBadGateway, LatencyMs: time.Since(started).Milliseconds(), Error: err.Error(), Models: []domain.Model{}}
		}
		for index := range models {
			models[index].ProviderID = provider.ID
		}
		return providerTestResult{Success: true, Provider: provider.ID, ModelsURL: modelsURL, Status: http.StatusOK, LatencyMs: time.Since(started).Milliseconds(), Models: models}
	}

	if provider.AuthType == domain.AuthTypeChatGPTOAuth {
		if provider.ChatGPTOAuth == nil || strings.TrimSpace(provider.ChatGPTOAuth.RefreshToken) == "" {
			return providerTestResult{Success: false, Provider: provider.ID, LatencyMs: time.Since(started).Milliseconds(), Error: "ChatGPT OAuth 未连接", Models: []domain.Model{}}
		}
		refreshed, err := s.ensureFreshChatGPTToken(provider)
		if err != nil {
			return providerTestResult{Success: false, Provider: provider.ID, LatencyMs: time.Since(started).Milliseconds(), Error: err.Error(), Models: []domain.Model{}}
		}
		provider = refreshed
		modelsURL := chatgptCodexBaseURL + "/codex/models?client_version=" + chatgptCodexCLIVersion
		models, fetchErr := fetchChatGPTOAuthModels(r.Context(), provider)
		if fetchErr != nil {
			models = defaultChatGPTOAuthModels(provider.ID)
			return providerTestResult{
				Success:   true,
				Provider:  provider.ID,
				ModelsURL: modelsURL,
				LatencyMs: time.Since(started).Milliseconds(),
				Models:    models,
				Preview:   "chatgpt oauth fallback model list: " + fetchErr.Error(),
			}
		}
		return providerTestResult{
			Success:   true,
			Provider:  provider.ID,
			ModelsURL: modelsURL,
			LatencyMs: time.Since(started).Milliseconds(),
			Models:    models,
			Preview:   fmt.Sprintf("chatgpt oauth models=%d", len(models)),
		}
	}

	modelsURL, err := deriveModelsURL(provider)
	if err != nil {
		return providerTestResult{Success: false, Provider: provider.ID, LatencyMs: time.Since(started).Milliseconds(), Error: err.Error(), Models: []domain.Model{}}
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, modelsURL, nil)
	if err != nil {
		return providerTestResult{Success: false, Provider: provider.ID, ModelsURL: modelsURL, LatencyMs: time.Since(started).Milliseconds(), Error: err.Error(), Models: []domain.Model{}}
	}
	request.Header.Set("Accept", "application/json")
	// per-provider 自定义头也要带上：要求路由头才能列模型的上游（OpenCode Go
	// 的 x-opencode-session）在配置里写好了却发不出去，模型清单永远是空的。
	applyRequestAdapterHeaders(request, provider, "")
	if provider.AuthType == domain.AuthTypeClaudeOAuth {
		refreshed, refreshErr := s.ensureFreshClaudeToken(provider)
		if refreshErr != nil {
			return providerTestResult{Success: false, Provider: provider.ID, ModelsURL: modelsURL, LatencyMs: time.Since(started).Milliseconds(), Error: refreshErr.Error(), Models: []domain.Model{}}
		}
		provider = refreshed
		if provider.ClaudeOAuth == nil || strings.TrimSpace(provider.ClaudeOAuth.AccessToken) == "" {
			return providerTestResult{Success: false, Provider: provider.ID, ModelsURL: modelsURL, LatencyMs: time.Since(started).Milliseconds(), Error: "provider has no Claude OAuth access token", Models: []domain.Model{}}
		}
		request.Header.Set("Authorization", "Bearer "+provider.ClaudeOAuth.AccessToken)
		request.Header.Set("anthropic-version", "2023-06-01")
		request.Header.Set("anthropic-beta", mergeAnthropicBetaValue(""))
		request.Header.Set("User-Agent", "axios/1.13.6")
	} else {
		applyProviderAuth(request, provider, r.Header.Get("Authorization"))
		// new-api style relays serve /v1/models with OpenAI-style auth and only
		// read Authorization: Bearer — a provider configured with Anthropic's
		// x-api-key header would 401 ("未提供令牌") on the models fetch while
		// the /v1/messages chat path works fine. Send both headers here; extra
		// auth headers are ignored by upstreams that already found theirs.
		if provider.AuthType == domain.AuthTypeAPIKey && !strings.EqualFold(provider.AuthHeader, "Authorization") {
			if key := resolveProviderAuth(provider); key != "" {
				if !strings.HasPrefix(strings.ToLower(key), "bearer ") {
					key = "Bearer " + key
				}
				request.Header.Set("Authorization", key)
			}
		}
	}

	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return providerTestResult{Success: false, Provider: provider.ID, ModelsURL: modelsURL, LatencyMs: time.Since(started).Milliseconds(), Error: err.Error(), Models: []domain.Model{}}
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 256*1024))
	preview := strings.TrimSpace(strings.ReplaceAll(string(body), "\n", " "))
	if len(preview) > 900 {
		preview = preview[:900]
	}
	models, parseErr := parseModelsResponse(body, provider)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return providerTestResult{Success: false, Provider: provider.ID, ModelsURL: modelsURL, Status: response.StatusCode, LatencyMs: time.Since(started).Milliseconds(), Error: fmt.Sprintf("models endpoint returned HTTP %d", response.StatusCode), Preview: preview, Models: []domain.Model{}}
	}
	if parseErr != nil {
		return providerTestResult{Success: false, Provider: provider.ID, ModelsURL: modelsURL, Status: response.StatusCode, LatencyMs: time.Since(started).Milliseconds(), Error: parseErr.Error(), Preview: preview, Models: []domain.Model{}}
	}
	return providerTestResult{Success: true, Provider: provider.ID, ModelsURL: modelsURL, Status: response.StatusCode, LatencyMs: time.Since(started).Milliseconds(), Models: models, Preview: preview}
}

// declaredProviderModels 返回 Provider 配置里已有的模型；旧记录只有一个
// DefaultModel 时也把它当作目录，避免模板型端点因为无法枚举而看起来“没有模型”。
func declaredProviderModels(provider domain.Provider) []domain.Model {
	if len(provider.Models) > 0 {
		out := make([]domain.Model, 0, len(provider.Models))
		for _, model := range provider.Models {
			if strings.TrimSpace(model.ID) == "" {
				continue
			}
			if strings.TrimSpace(model.ProviderID) == "" {
				model.ProviderID = provider.ID
			}
			if model.Protocol == "" {
				model.Protocol = provider.Protocol
			}
			out = append(out, model)
		}
		if len(out) > 0 {
			return out
		}
	}
	if modelID := strings.TrimSpace(provider.DefaultModel); modelID != "" {
		return []domain.Model{{
			ID:            modelID,
			ProviderID:    provider.ID,
			Protocol:      provider.Protocol,
			ContextLength: resolveModelContextLength(modelID, 0),
			InMenu:        true,
		}}
	}
	return nil
}

func deriveModelsURL(provider domain.Provider) (string, error) {
	base := strings.TrimSpace(provider.BaseURL)
	if base == "" {
		return "", fmt.Errorf("provider baseUrl is required")
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	parsed.RawQuery = ""
	path := parsed.Path
	lowerPath := strings.ToLower(path)
	if strings.Contains(strings.ToLower(parsed.Host), "anthropic.com") {
		parsed.Path = "/v1/models"
		return parsed.String(), nil
	}
	if deploymentIndex := strings.Index(lowerPath, "/deployments/"); deploymentIndex >= 0 && strings.Contains(lowerPath, "/chat/completions") {
		parsed.Path = strings.TrimRight(path[:deploymentIndex], "/") + "/models"
		return parsed.String(), nil
	}
	replacements := []struct {
		old string
		new string
	}{
		{"/chat/completions", "/models"},
		{"/responses", "/models"},
		{"/messages", "/models"},
	}
	for _, replacement := range replacements {
		if strings.Contains(lowerPath, replacement.old) {
			index := strings.LastIndex(lowerPath, replacement.old)
			parsed.Path = path[:index] + replacement.new
			return parsed.String(), nil
		}
	}
	if strings.HasSuffix(path, "/") {
		parsed.Path = strings.TrimRight(path, "/") + "/models"
	} else {
		parsed.Path = strings.TrimRight(path, "/") + "/models"
	}
	return parsed.String(), nil
}

func parseModelsResponse(body []byte, provider domain.Provider) ([]domain.Model, error) {
	// 窗口有四个名字在流通：OpenAI 习惯的 context_length、Anthropic 风格的
	// max_input_tokens、magpie/Crush 那套的 context_window，以及个别上游的
	// context_length 缺席。只认第一个的话，一条「上游是另一个网关」的链路
	// （对端按 max_input_tokens 说自己有 1M）会把 1M 整个丢掉，下游看到的
	// 就只剩默认值。都读到，取最大的那个当声明值。
	var payload struct {
		Data []struct {
			ID             string `json:"id"`
			ContextLength  int    `json:"context_length"`
			ContextWindow  int    `json:"context_window"`
			MaxInputTokens int    `json:"max_input_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	models := make([]domain.Model, 0, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		reported := max(item.ContextLength, max(item.ContextWindow, item.MaxInputTokens))
		contextLength := resolveModelContextLength(id, reported)
		models = append(models, domain.Model{ID: id, ProviderID: provider.ID, Protocol: provider.Protocol, ContextLength: contextLength, InMenu: true})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("models endpoint returned no usable model ids")
	}
	return models, nil
}

func extractResponseErrorMessage(responseBody []byte) string {
	text := strings.TrimSpace(string(responseBody))
	if text == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		text := strings.TrimSpace(string(responseBody))
		if strings.Contains(text, "data:") {
			return ""
		}
		if len(text) > 500 {
			return text[:500]
		}
		return text
	}
	// OpenAI Responses 的成功响应带 "error": null。键存在但值是 nil 不是错误，
	// 以前 fmt.Sprint(nil) 会得到字符串 "<nil>"，让一条 200 在日志里显示成红色。
	if errorValue, ok := payload["error"]; ok && errorValue != nil {
		if item, ok := errorValue.(map[string]any); ok {
			return formatAPIErrorMessage(item)
		}
		if message := strings.TrimSpace(fmt.Sprint(errorValue)); message != "" && message != "<nil>" {
			return message
		}
	}
	if detail := strings.TrimSpace(stringValue(payload["detail"])); detail != "" {
		return detail
	}
	// WorkBuddy and several OpenAI-compatible relays return their real error
	// as top-level {"code":6004,"msg":"..."}. The code is what separates an
	// error from a successful {"code":0,"msg":"ok"} envelope.
	if topMessage := firstNonEmpty(stringValue(payload["msg"]), stringValue(payload["message"])); topMessage != "" {
		code := strings.TrimSpace(stringValue(payload["code"]))
		success, hasSuccess := payload["success"].(bool)
		failedCode := code != "" && code != "0" && code != "200"
		if failedCode || (hasSuccess && !success) {
			if failedCode {
				return code + ": " + topMessage
			}
			return topMessage
		}
	}
	if payload["type"] == "error" {
		if errorValue, ok := payload["error"].(map[string]any); ok {
			return formatAPIErrorMessage(errorValue)
		}
	}
	return ""
}

func formatAPIErrorMessage(item map[string]any) string {
	message, errType := errorMessageFromValue(item, "")
	switch {
	case errType != "" && message != "" && !strings.EqualFold(message, "error") && errType != "api_error":
		return errType + ": " + message
	case message != "" && message != "upstream request failed":
		return message
	case errType != "" && errType != "api_error":
		return errType
	case message != "":
		return message
	default:
		return ""
	}
}

func responseHeaderMap(headers http.Header) map[string]string {
	result := make(map[string]string, len(headers))
	for key, values := range headers {
		result[key] = strings.Join(values, ", ")
	}
	return result
}

func buildRouteTestReproduceCurl(gatewayURL string, protocol domain.Protocol, requestBody string) string {
	escapedBody := strings.ReplaceAll(requestBody, "'", `'\''`)
	authHeader := "Authorization: Bearer <bound-api-key>"
	if protocol == domain.ProtocolClaude {
		authHeader = "x-api-key: <bound-api-key>"
	}
	return fmt.Sprintf(
		"# The console test uses a short-lived server signature; replay with an API key bound to this route.\ncurl -sv '%s' \\\n  -H 'Content-Type: application/json' \\\n  -H '%s' \\\n  -d '%s'",
		gatewayURL,
		authHeader,
		escapedBody,
	)
}

func buildRouteTestDiagnostics(route domain.Route, provider domain.Provider, decision domain.RouteDecision, model, gatewayURL, upstreamURL, requestBody string, response *http.Response, responseBody []byte) map[string]any {
	status := 0
	responseHeaders := map[string]string{}
	if response != nil {
		status = response.StatusCode
		responseHeaders = responseHeaderMap(response.Header)
	}
	errorMessage := extractResponseErrorMessage(responseBody)
	diagnostics := map[string]any{
		"routeId":          route.ID,
		"routeName":        route.Name,
		"providerId":       provider.ID,
		"providerProtocol": provider.Protocol,
		"outputProtocol":   route.OutputProtocol,
		"providerBaseUrl":  provider.BaseURL,
		"upstreamUrl":      upstreamURL,
		"gatewayUrl":       gatewayURL,
		"action":           decision.Action,
		"protocolFlow":     decision.ConversionLabel,
		"model":            model,
		"status":           status,
		"requestBody":      requestBody,
		"responseBody":     string(responseBody),
		"responseHeaders":  responseHeaders,
		"errorMessage":     errorMessage,
		"reproduceCurl":    buildRouteTestReproduceCurl(gatewayURL, route.OutputProtocol, requestBody),
	}
	return diagnostics
}

func (s *Server) testRoute(r *http.Request, routeID string, requestModel string, message string, started time.Time) (map[string]any, int) {
	route, err := s.router.RouteByID(routeID)
	if err != nil {
		return map[string]any{"success": false, "model": strings.TrimSpace(requestModel), "error": err.Error(), "latencyMs": time.Since(started).Milliseconds()}, http.StatusNotFound
	}
	model := s.router.ResolveModel(route, "", requestModel)
	if provider, err := s.router.ProviderForRoute(route); err == nil {
		model = applyProviderModelFallback(provider, model)
	}
	if strings.TrimSpace(model) == "" {
		model = "request-model-not-set"
	}
	decision, err := s.router.Decide(route.ID)
	if err != nil {
		return map[string]any{
			"success": false, "routeId": route.ID, "model": model,
			"error": err.Error(), "latencyMs": time.Since(started).Milliseconds(),
		}, http.StatusBadGateway
	}
	if strings.TrimSpace(message) == "" {
		message = "ping from Protocol Gateway route test"
	}

	provider, err := s.router.ProviderForRoute(route)
	if err != nil {
		return map[string]any{
			"success": false, "routeId": route.ID, "model": model,
			"error": err.Error(), "latencyMs": time.Since(started).Milliseconds(),
		}, http.StatusBadGateway
	}
	upstreamURL := ""
	if provider.Protocol == domain.ProtocolOpenAIChat {
		upstreamURL = resolveProviderChatURL(provider, model)
	} else if strings.TrimSpace(provider.BaseURL) != "" {
		upstreamURL = strings.TrimSpace(provider.BaseURL)
	}

	gatewayURL, err := s.gatewayOutputURL(route.OutputProtocol)
	if err != nil {
		return map[string]any{
			"success": false, "routeId": route.ID, "providerId": provider.ID,
			"model": model, "error": err.Error(), "latencyMs": time.Since(started).Milliseconds(),
		}, http.StatusBadGateway
	}

	payload := map[string]any{
		"model":    model,
		"stream":   false,
		"messages": []map[string]any{{"role": "user", "content": message}},
	}
	switch route.OutputProtocol {
	case domain.ProtocolClaude:
		payload = map[string]any{
			"model":      model,
			"max_tokens": 1024,
			"stream":     false,
			"messages":   []map[string]any{{"role": "user", "content": message}},
		}
	case domain.ProtocolOpenAIResponses:
		payload = map[string]any{
			"model":  model,
			"stream": false,
			"input":  message,
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return map[string]any{"success": false, "routeId": route.ID, "error": err.Error(), "latencyMs": time.Since(started).Milliseconds()}, http.StatusBadRequest
	}
	requestBody := string(body)

	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, gatewayURL, bytes.NewReader(body))
	if err != nil {
		return map[string]any{"success": false, "routeId": route.ID, "error": err.Error(), "latencyMs": time.Since(started).Milliseconds()}, http.StatusBadRequest
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(headerGatewayRouteID, route.ID)
	request.Header.Set(headerGatewayInternalTest, routeTestToken(route.ID, time.Now()))

	client := &http.Client{Timeout: 60 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		diagnostics := buildRouteTestDiagnostics(route, provider, decision, model, gatewayURL, upstreamURL, requestBody, nil, nil)
		diagnostics["transportError"] = err.Error()
		diagJSON, _ := json.Marshal(diagnostics)
		s.logs.AddApp("error", "route test transport failed", string(diagJSON))
		return map[string]any{
			"success":      false,
			"routeId":      route.ID,
			"providerId":   provider.ID,
			"model":        model,
			"action":       decision.Action,
			"protocolFlow": decision.ConversionLabel,
			"gatewayUrl":   gatewayURL,
			"upstreamUrl":  upstreamURL,
			"requestBody":  requestBody,
			"error":        err.Error(),
			"diagnostics":  diagnostics,
			"latencyMs":    time.Since(started).Milliseconds(),
		}, http.StatusOK
	}
	defer response.Body.Close()
	readLimit := int64(4096)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		readLimit = 32768
	}
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, readLimit))
	preview := strings.TrimSpace(strings.ReplaceAll(string(responseBody), "\n", " "))
	if len(preview) > 900 {
		preview = preview[:900]
	}
	latency := time.Since(started).Milliseconds()
	success := response.StatusCode >= 200 && response.StatusCode < 300
	usage := ParseOpenAIUsage(responseBody)
	if usage.InputTokens == 0 && usage.OutputTokens == 0 {
		usage = ParseClaudeUsage(responseBody)
	}
	if usage.InputTokens == 0 && usage.OutputTokens == 0 {
		usage = ParseResponsesUsage(responseBody)
	}
	s.recordRequestLogFromRequest(r, started, domain.APIKey{}, false, route.ID, provider.ID, model, "test_"+decision.Action, decision.ConversionLabel, gatewayURL, response.StatusCode, usage, body, responseBody)
	result := map[string]any{
		"success":      success,
		"routeId":      route.ID,
		"providerId":   provider.ID,
		"model":        model,
		"action":       decision.Action,
		"protocolFlow": decision.ConversionLabel,
		"status":       response.StatusCode,
		"latencyMs":    latency,
		"preview":      preview,
		"responseBody": string(responseBody),
		"gatewayUrl":   gatewayURL,
		"upstreamUrl":  upstreamURL,
		"requestBody":  requestBody,
	}
	if !success {
		diagnostics := buildRouteTestDiagnostics(route, provider, decision, model, gatewayURL, upstreamURL, requestBody, response, responseBody)
		result["diagnostics"] = diagnostics
		if errorMessage := extractResponseErrorMessage(responseBody); errorMessage != "" {
			result["error"] = errorMessage
		} else {
			result["error"] = fmt.Sprintf("route test failed with HTTP %d", response.StatusCode)
		}
		diagJSON, _ := json.Marshal(diagnostics)
		s.logs.AddApp("error", "route test failed", string(diagJSON))
	} else {
		s.logs.AddApp("info", "route test completed", fmt.Sprintf("route=%s gateway=%s status=%d latency=%dms", route.ID, gatewayURL, response.StatusCode, latency))
	}
	return result, http.StatusOK
}

// gatewayOutputURL is the address this process itself should call for an
// output protocol. It must use the actual listener, not endpoint.ListenHost:
// ListenHost is the address advertised to LAN clients (and can be a LAN IP
// while the App is bound to 127.0.0.1). Using the advertised host made the
// console route test dial a port that was not listening and report a useless
// transport failure.
func (s *Server) gatewayOutputURL(protocol domain.Protocol) (string, error) {
	state := s.router.State()
	for _, endpoint := range state.Endpoints {
		if endpoint.Protocol != protocol {
			continue
		}
		host := "127.0.0.1"
		if addr := strings.TrimSpace(s.listenAddr); addr != "" {
			if parsedHost, _, err := net.SplitHostPort(addr); err == nil && parsedHost != "" && parsedHost != "0.0.0.0" && parsedHost != "::" {
				host = parsedHost
			}
		}
		base := strings.TrimRight("http://"+net.JoinHostPort(host, strconv.Itoa(s.localGatewayPort()))+endpoint.BasePath, "/")
		switch protocol {
		case domain.ProtocolOpenAIChat:
			return base + "/chat/completions", nil
		case domain.ProtocolOpenAIResponses:
			return base + "/responses", nil
		case domain.ProtocolClaude:
			// Endpoint BasePath is /anthropic; the real route is /anthropic/v1/messages.
			return base + "/v1/messages", nil
		}
	}
	return "", fmt.Errorf("no output endpoint configured for protocol %s", protocol)
}

func (s *Server) proxyOpenAIToClaudeMessages(w http.ResponseWriter, r *http.Request, provider domain.Provider, model string, claudeReq map[string]any, skipIncomingAuth bool) (int, TokenUsage, []byte, error) {
	openAIReq, err := claudeRequestToOpenAIChat(claudeReq, model)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	// The client's own tools[] decides how its tool names are spelled on the way
	// back: a model that answers "bash" for a tool the client registered as
	// "Bash" would otherwise reach the client as a tool it does not know.
	declaredTools := claudeDeclaredToolNames(claudeReq["tools"])
	// Remember the stop sequences the Chat hop now carries, so a turn that ends
	// on one is reported back as stop_sequence rather than a plain stop.
	r, clientStopSequences := rememberConvertedStopSequences(r, openAIReq["stop"])
	stream, _ := openAIReq["stream"].(bool)
	// These subscriptions only answer as a stream: WorkBuddy refuses a non-stream
	// chat, Trae and Qoder are their agent's own event streams, and OpenCode
	// serves its free tier to a streaming request only. They go upstream streamed
	// whatever the client asked for, and a client that wanted a single object
	// gets the stream rebuilt into one below.
	forcedStream := !stream && (isWorkBuddyProvider(provider) || isTraeProvider(provider) ||
		isQoderProvider(provider) || openCodeZenFreeFor(provider, model))
	if forcedStream {
		openAIReq["stream"] = true
	}
	upstreamBody, err := json.Marshal(openAIReq)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}

	model = applyProviderModelMapping(provider, model)
	upstreamBody, bodyErr := applyRequestAdapterBody(provider, model, upstreamBody)
	if bodyErr != nil {
		return 0, TokenUsage{}, nil, bodyErr
	}
	accept := "application/json"
	if stream || forcedStream {
		accept = "text/event-stream"
	}

	var response *http.Response
	if provider.AuthType == domain.AuthTypeCursorOAuth {
		// The bridge keeps its own address and no provider auth, as before.
		baseURL, refreshed, bridgeErr := s.resolveCursorBridgeBaseURL(r.Context(), provider)
		if bridgeErr != nil {
			return 0, TokenUsage{}, nil, bridgeErr
		}
		provider = refreshed
		request, reqErr := http.NewRequestWithContext(r.Context(), http.MethodPost,
			strings.TrimRight(baseURL, "/")+"/chat/completions", bytes.NewReader(upstreamBody))
		if reqErr != nil {
			return 0, TokenUsage{}, nil, reqErr
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", accept)
		response, err = doHTTPWithTiming(r.Context(), &http.Client{Timeout: 0}, request)
	} else {
		// One sender for every provider, because that is where each subscription's
		// own quirks live: WorkBuddy's forced stream, leading system turn, prompt
		// words its channel check turns away and account headers; Trae's and
		// Qoder's signed agent call, whose answer it hands back OpenAI-shaped so the
		// converters below read what they already know; Zen's free-tier body.
		response, err = s.doOpenAIProviderRequest(r.Context(), r, provider,
			resolveProviderChatURLWithAdapter(provider, model), upstreamBody, accept, skipIncomingAuth)
	}
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	defer response.Body.Close()

	if forcedStream && response.StatusCode >= 200 && response.StatusCode < 400 {
		_ = forcedStreamAsCompletion(response, model, workBuddyStreamAsCompletion)
	}

	if stream {
		for key, values := range response.Header {
			if strings.EqualFold(key, "Content-Length") {
				continue
			}
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			responseBody, readErr := io.ReadAll(response.Body)
			if readErr != nil {
				return response.StatusCode, TokenUsage{}, nil, readErr
			}
			var payload map[string]any
			_ = json.Unmarshal(responseBody, &payload)
			claudeBody, _, convErr := openAIErrorValueToClaude(errorValueOrBody(payload, response.StatusCode, responseBody), model)
			if convErr != nil || len(claudeBody) == 0 {
				claudeBody = responseBody
			}
			if _, behindLoop := w.(*failoverResponseWriter); behindLoop {
				// Behind the failover loop nothing may be committed here: a
				// refusal held back is what lets the next account answer, and
				// writing the status first is what pinned a busy model (Qoder's
				// 10605 is a 403 JSON even when a stream was asked for) to the
				// first account with twelve candidates untried.
				return response.StatusCode, TokenUsage{}, claudeBody,
					fmt.Errorf("%s", summarizeUpstreamHTTPError(response.StatusCode, claudeBody))
			}
			hw := newSSEHeartbeatResponseWriter(w)
			hw.WriteHeader(response.StatusCode)
			_, writeErr := hw.Write(claudeBody)
			return response.StatusCode, TokenUsage{}, claudeBody, writeErr
		}
		// Behind the failover loop the answer streams through the same holding
		// writer the converting paths use: the opening frames and any refusal
		// stay off the wire until the turn says something, so an account that
		// answers "busy" (Qoder frames 10605 as an error event inside a 200
		// stream) can still be replaced by the next one. The heartbeat starts
		// with it, on first content.
		if fw, ok := w.(*failoverResponseWriter); ok {
			streamed := newConverterStreamWriter(fw)
			usage, streamErr := streamOpenAIChatToClaudeEvents(streamed, response.Body, model, declaredTools, clientStopSequences...)
			streamBody := streamed.Finish()
			if streamed.Committed() {
				return response.StatusCode, usage, streamBody, streamErr
			}
			if streamErr != nil {
				// Nothing reached the client: report it so the chain can move on,
				// and keep the body for the last attempt.
				return response.StatusCode, usage, streamBody, streamErr
			}
			return response.StatusCode, usage, streamBody, nil
		}
		hw := newSSEHeartbeatResponseWriter(w)
		hw.WriteHeader(response.StatusCode)
		// Chat→Claude 直写流（未走 finishConvertedProxy），同样需要心跳防反代空闲断流。
		stopHeartbeat := startSSEHeartbeat(hw, sseHeartbeatInterval)
		defer stopHeartbeat()
		// The converted stream is teed into the log copy the caller records:
		// this path writes the answer itself, so it must report what it sent.
		logged := newStreamLogWriter(hw)
		usage, streamErr := streamOpenAIChatToClaudeEvents(logged, response.Body, model, declaredTools, clientStopSequences...)
		return response.StatusCode, usage, logged.Bytes(), streamErr
	}

	responseBody, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return 0, TokenUsage{}, nil, readErr
	}

	if up := response.Header.Get("Retry-After"); up != "" {
		w.Header().Set("Retry-After", up)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var payload map[string]any
		_ = json.Unmarshal(responseBody, &payload)
		claudeBody, _, convErr := openAIErrorValueToClaude(errorValueOrBody(payload, response.StatusCode, responseBody), model)
		if convErr != nil || len(claudeBody) == 0 {
			w.WriteHeader(response.StatusCode)
			_, writeErr := w.Write(responseBody)
			return response.StatusCode, TokenUsage{}, responseBody, writeErr
		}
		w.WriteHeader(response.StatusCode)
		_, writeErr := w.Write(claudeBody)
		return response.StatusCode, TokenUsage{}, claudeBody, writeErr
	}

	claudeBody, usage, err := openAIChatResponseToClaude(responseBody, model, declaredTools, clientStopSequences...)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(claudeBody)))
	w.WriteHeader(http.StatusOK)
	if _, writeErr := w.Write(claudeBody); writeErr != nil {
		return http.StatusOK, usage, claudeBody, writeErr
	}
	return http.StatusOK, usage, claudeBody, nil
}

func (s *Server) proxyOpenAIChat(w http.ResponseWriter, r *http.Request, provider domain.Provider, model string, body []byte, skipIncomingAuth bool) (int, TokenUsage, []byte, error) {
	stream := requestBodyWantsStream(body)
	if isTraeProvider(provider) {
		// Trae's request and reply are its own, so the whole exchange goes
		// through a path of its own rather than through the relay's shaping.
		return s.proxyTraeChat(w, r, provider, model, body, skipIncomingAuth)
	}
	if isQoderProvider(provider) {
		// Qoder is the same case: its endpoint takes a signed request in its own
		// codec and answers in its own envelopes, and sendQoderChat hands the
		// converted OpenAI stream back in its place.
		return s.proxyQoderChat(w, r, provider, model, body, skipIncomingAuth)
	}
	if provider.AuthType == domain.AuthTypeCursorOAuth {
		baseURL, refreshed, err := s.resolveCursorBridgeBaseURL(r.Context(), provider)
		if err != nil {
			return 0, TokenUsage{}, nil, err
		}
		provider = refreshed
		upstreamURL := strings.TrimRight(baseURL, "/") + "/chat/completions"
		request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(body))
		if err != nil {
			return 0, TokenUsage{}, nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", r.Header.Get("Accept"))
		response, err := doHTTPWithTiming(r.Context(), &http.Client{Timeout: 0}, request)
		if err != nil {
			return 0, TokenUsage{}, nil, err
		}
		defer response.Body.Close()
		return writePassThroughResponse(w, response, stream, usageStreamOpenAI, ParseOpenAIUsage, openAIChatStreamInterrupter{model: model})
	}

	model = applyProviderModelMapping(provider, model)
	body, bodyErr := applyRequestAdapterBody(provider, model, body)
	if bodyErr != nil {
		return 0, TokenUsage{}, nil, bodyErr
	}
	// WorkBuddy refuses a non-stream chat ("Non-stream chat request is
	// currently not supported") and OpenCode serves its free tier to a
	// streaming request only, so both go upstream as a stream; a client that
	// asked for a single JSON object gets the stream rebuilt into one before
	// the response is written.
	workBuddy := isWorkBuddyProvider(provider)
	// A call that came with no id would otherwise be replayed as "" forever,
	// which strict upstreams refuse for the whole conversation.
	body = chatToolCallIDs(body)
	// DeepSeek (and relays in front of it) read reasoning and reasoning_content as
	// one field and refuse a message that carries both ("duplicate field
	// reasoning"). A client that keeps its thinking in reasonon... whichever of the
	// two it sent is kept, once.
	body = dedupeReasoningFields(body)
	var workBuddyAliases map[string]string
	if workBuddy {
		body, workBuddyAliases, _ = shapeWorkBuddyChatBody(body)
	}
	zenFree := false
	if isOpenCodeZenProvider(provider) {
		body, zenFree = prepareOpenCodeZenChatBody(provider, body)
	}
	forcedStream := (workBuddy || zenFree) && !stream
	upstreamURL := resolveProviderChatURLWithAdapter(provider, model)
	incomingAuth := ""
	if !skipIncomingAuth {
		incomingAuth = r.Header.Get("Authorization")
	}
	send := func(requestBody []byte) (*http.Response, error) {
		if provider.AuthType == domain.AuthTypePeer {
			// 对端 provider 是父网关的一扇门：这条直通路径不经过
			// doOpenAIProviderRequest，出口要自己接上。
			return s.sendPeerDoor(r.Context(), r, provider, model, requestBody, r.Header.Get("Accept"))
		}
		request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(requestBody))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		if forcedStream {
			request.Header.Set("Accept", "text/event-stream")
		} else {
			request.Header.Set("Accept", r.Header.Get("Accept"))
		}
		applyProviderAuth(request, provider, incomingAuth)
		applyOpenCodeZenHeaders(request, provider, requestBody)
		applyRequestAdapterHeaders(request, provider, model)
		return doHTTPWithTiming(r.Context(), &http.Client{Timeout: 0}, request)
	}
	var response *http.Response
	var err error
	if workBuddy {
		// Same as above: each retry carries the provider it was decided for.
		response, err = s.sendWorkBuddyChat(provider, model, body, workBuddyAliases, func(sendProvider domain.Provider, requestBody []byte) (*http.Response, error) {
			request, requestErr := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(requestBody))
			if requestErr != nil {
				return nil, requestErr
			}
			request.Header.Set("Content-Type", "application/json")
			if forcedStream {
				request.Header.Set("Accept", "text/event-stream")
			} else {
				request.Header.Set("Accept", r.Header.Get("Accept"))
			}
			applyProviderAuth(request, sendProvider, incomingAuth)
			applyOpenCodeZenHeaders(request, sendProvider, requestBody)
			applyRequestAdapterHeaders(request, sendProvider, model)
			return doHTTPWithTiming(r.Context(), &http.Client{Timeout: 0}, request)
		}, forcedStream)
	} else {
		response, err = send(body)
	}
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	defer response.Body.Close()
	if workBuddy && forcedStream {
		_ = rewriteWorkBuddyUpstreamStreamedBody(response, model)
	}
	if zenFree && forcedStream {
		_ = rewriteOpenCodeZenUpstreamStreamedBody(response, model)
	}
	return writePassThroughResponse(w, response, stream, usageStreamOpenAI, ParseOpenAIUsage, openAIChatStreamInterrupter{model: model})
}

// proxyOpenAIImages forwards one Images request to a provider. It could not be
// called "OpenAIImages" in a way that said this out loud: the route decides
// which protocol the client speaks, and a route set to Claude is not an Images
// endpoint even when its provider happens to speak OpenAI Chat — the answer
// would come back in a shape the client cannot read.
func (s *Server) proxyOpenAIImages(w http.ResponseWriter, r *http.Request, provider domain.Provider, model string, body []byte, routeProtocol domain.Protocol) (int, TokenUsage, []byte, error) {
	// Image generation spends the same account budget as chat: a render is the
	// most expensive request there is, and skipping the lane let a burst of
	// images blow through the per-minute budget the operator set.
	releaseSlot, admissionErr := s.acquireProviderSlot(r.Context(), provider)
	if admissionErr != nil {
		w.Header().Set("Retry-After", strconv.Itoa(s.providerLanes.retryAfterSecondsFor(provider, time.Now())))
		if errors.Is(admissionErr, errProviderSaturated) {
			return http.StatusTooManyRequests, TokenUsage{}, nil, admissionErr
		}
		return 0, TokenUsage{}, nil, admissionErr
	}
	defer releaseSlot()
	if routeProtocol != domain.ProtocolOpenAIChat {
		return 0, TokenUsage{}, nil, fmt.Errorf("route speaks %s, which has no images/generations endpoint", routeProtocol.DisplayName())
	}
	if provider.AuthType == domain.AuthTypeCursorOAuth {
		status, responseBody, err := s.fetchCursorBridgeImages(r, provider, body)
		if err == nil && status >= 200 && status < 300 {
			for key, values := range map[string]string{"Content-Type": "application/json"} {
				w.Header().Set(key, values)
			}
			w.WriteHeader(status)
			if len(responseBody) > 0 {
				_, _ = w.Write(responseBody)
			}
			return status, TokenUsage{}, responseBody, nil
		}
		// No process-wide OPENAI_API_KEY rescue: that billed images to an account
		// the caller never chose. The gateway's own provider list is the only
		// place an OpenAI key should come from.
		if err != nil {
			return 0, TokenUsage{}, nil, fmt.Errorf("%w (Cursor OAuth 本地 bridge 暂无法生成图片)", err)
		}
		return status, TokenUsage{}, responseBody, fmt.Errorf("cursor bridge image generation failed with HTTP %d", status)
	}

	model = applyProviderModelMapping(provider, model)
	body, bodyErr := applyRequestAdapterBody(provider, model, body)
	if bodyErr != nil {
		return 0, TokenUsage{}, nil, bodyErr
	}
	// A WorkBuddy account renders on its own route with the desktop header family: the
	// bearer alone is not enough, and the generic path below posts to a doubled /v2
	// prefix (see workbuddy_endpoints.go).
	if _, isWorkBuddyRender := workBuddyRenderSite(provider); isWorkBuddyRender {
		// A media route carries its own upstream path and body (video submit, task
		// poll); an image render does not, and decides by the edits marker.
		if media, ok := workBuddyMediaRouteOf(r.Context()); ok {
			return s.executeWorkBuddyEndpoint(w, r, provider, media.path, body)
		}
		return s.executeWorkBuddyEndpoint(w, r, provider, workBuddyImagePathFor(r.Context()), body)
	}
	// 只用 Provider 自己的凭证。以前这里会在 Provider 没有 Key 时把调用方的
	// Authorization 原样转给厂商——那是网关自己的 Key，发给上游等于泄露。
	authValue := resolveProviderAuth(provider)
	if strings.TrimSpace(authValue) == "" {
		return 0, TokenUsage{}, nil, fmt.Errorf("provider %q has no API key configured", provider.ID)
	}
	upstreamURL := resolveProviderImageURL(provider)
	return s.proxyOpenAIImagesUpstream(w, r.Context(), upstreamURL, authValue, "application/json", body)
}

func (s *Server) fetchCursorBridgeImages(r *http.Request, provider domain.Provider, body []byte) (int, []byte, error) {
	baseURL, refreshed, err := s.resolveCursorBridgeBaseURL(r.Context(), provider)
	if err != nil {
		return 0, nil, err
	}
	_ = refreshed
	upstreamURL := strings.TrimRight(baseURL, "/") + "/images/generations"
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 0}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, nil, err
	}
	return response.StatusCode, responseBody, nil
}

// proxyOpenAIImagesUpstream relays one JSON or form body to an OpenAI-compatible
// upstream and hands the answer back as it came. The name is historical: images,
// embeddings and audio all go through it.
// imageGenerationTimeout bounds one image generation. A render sends nothing for
// minutes, so an idle guard would kill it early (hence Timeout: 0 below); with
// no bound at all a stuck vendor holds the request — and its account slot —
// until the client gives up, which is what this caps.
const imageGenerationTimeout = 10 * time.Minute

// isImagesUpstreamURL reports whether the endpoint is a render: it must wait
// for a deferred answer, unlike embeddings or audio's fast round trip.
func isImagesUpstreamURL(upstreamURL string) bool {
	return strings.Contains(strings.ToLower(upstreamURL), "/images/")
}

func (s *Server) proxyOpenAIImagesUpstream(w http.ResponseWriter, ctx context.Context, upstreamURL string, authValue string, contentType string, body []byte) (int, TokenUsage, []byte, error) {
	// Only a request that is a render (an /images/... endpoint) gets the bound;
	// embeddings and audio keep the caller's context as it is.
	if isImagesUpstreamURL(upstreamURL) {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, imageGenerationTimeout)
		defer cancel()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/json"
	}
	request.Header.Set("Content-Type", contentType)
	if strings.HasPrefix(strings.ToLower(contentType), "multipart/form-data") {
		// A transcript can come back as text, SRT or VTT; let the endpoint say.
		request.Header.Set("Accept", "*/*")
	} else {
		request.Header.Set("Accept", "application/json")
	}
	if authValue != "" {
		if !strings.HasPrefix(strings.ToLower(authValue), "bearer ") {
			authValue = "Bearer " + authValue
		}
		request.Header.Set("Authorization", authValue)
	}
	client := &http.Client{Timeout: 0}
	response, err := client.Do(request)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	defer response.Body.Close()
	return writePassThroughResponse(w, response, false, usageStreamUnfoldable, func([]byte) TokenUsage { return TokenUsage{} })
}

// proxyClaudeMessages forwards a Claude Messages API request to the upstream
// provider (Anthropic directly for claude_oauth providers, or provider.BaseURL
// for a regular Claude API-key provider), streaming the response back
// unchanged. For claude_oauth providers this refreshes the access token if
// needed, injects the billing marker, and forces the anthropic-beta header.
func (s *Server) proxyClaudeMessages(w http.ResponseWriter, r *http.Request, provider domain.Provider, body []byte) (int, TokenUsage, []byte, error) {
	// Claude Code 会自带 max_tokens，但网关可能已做模型覆盖；预算必须按实际上游模型。
	clientBody := body
	body = rewriteClaudeUpstreamMaxTokens(body, provider, maxOutputTokensOverrideFrom(r.Context()))
	response, err := s.sendClaudeMessagesUpstream(r, provider, body)
	if !claudeBodyRewritten(clientBody, body) {
		// 整流没有实质改动：日志留客户端原文，别记一份键序被打乱的副本。
		body = clientBody
	}
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}

	// Thinking 签名整流：若上游因跨账号/跨 Provider 回传的历史 thinking 签名校验
	// 失败而返回该类 400，则剥离历史 thinking 块及残留 signature 后对同一 Provider
	// 重试一次（仅一次，对客户端透明）。历史 thinking 只是草稿纸，剥离不损失有效
	// 上下文。非命中错误 / 非 JSON 请求 / 整流无改动时，均按原样透传，零副作用。
	if response.StatusCode == http.StatusBadRequest {
		if peeked, ok := s.maybeRectifyClaudeThinking(r, provider, body, response); ok {
			response = peeked
		} else if peeked, ok := s.maybeRectifySamplingRefusal(r, provider, body, response, func(retryBody []byte) (*http.Response, error) {
			return s.sendClaudeMessagesUpstream(r, provider, retryBody)
		}); ok {
			// 新一代 Claude 拒绝 temperature / top_p / top_k：去掉后原样重发一次。
			response = peeked
		}
	}

	// OAuth 伪装会把第三方小写/snake_case 工具名改成 Claude Code 的 TitleCase
	// （bash → Bash）。客户端只认自己声明的名字，所以这里必须把它们改回去，
	// 否则客户端会答 "No such tool available: Bash"。Claude Code 自己的工具名
	// 本来就是 TitleCase 或 mcp__ 前缀，别名表为空，这段不产生任何开销以外的行为。
	if provider.AuthType == domain.AuthTypeClaudeOAuth {
		if aliases := claudeRequestToolAliases(body); len(aliases) > 0 {
			wrapToolNameRestore(response, aliases, renameClaudeToolUseNamesJSON, renameClaudeToolUseNamesLine)
		}
	}

	defer response.Body.Close()
	return writePassThroughResponse(w, response, requestBodyWantsStream(body), usageStreamClaude, ParseClaudeUsage, s.claudePassThroughInterrupter(r, provider, body))
}

// claudePassThroughInterrupter is the hook a Claude pass-through stream writes
// through: it closes a stream the upstream cut, and reports a stream the
// upstream finished with no visible output at all.
//
// The second case is the reason it exists. On the pass-through path every byte
// reaches the client as it comes, so message_start is already out by the time
// the turn turns out to be empty: the turn cannot be retried and cannot be
// rewritten, and the client shows an empty answer as if the model had simply
// stopped talking. Nothing else in the pipeline can tell that apart from a
// real reply, so it is said here, once, where an operator can see it. A
// history that keeps replaying such a turn is what
// dropTrailingClaudePassThroughThinking removes on the way in.
func (s *Server) claudePassThroughInterrupter(r *http.Request, provider domain.Provider, body []byte) claudeStreamInterrupter {
	if provider.AuthType != domain.AuthTypeClaudeOAuth {
		// Only the Anthropic OAuth upstream is known to answer this way, and a
		// Claude-compatible relay reports its own failures as errors.
		return claudeStreamInterrupter{}
	}
	model := stringValue(claudeBodyField(body, "model"))
	return claudeStreamInterrupter{onEmptyTurn: func() {
		markTimingFlag(r.Context(), timingFlagThinkingOnlyEmptyTurn)
		if s.logs != nil {
			s.logs.AddApp("warn", "upstream thinking-only empty turn",
				fmt.Sprintf("provider=%s model=%s: the upstream closed the turn with no visible output; the client received an empty answer", provider.ID, model))
		}
	}}
}

// sendClaudeMessagesUpstream builds and sends one Claude Messages request to the
// resolved upstream (Anthropic for claude_oauth providers, provider.BaseURL for
// regular Claude API-key providers), returning the raw response.
func (s *Server) sendClaudeMessagesUpstream(r *http.Request, provider domain.Provider, body []byte) (*http.Response, error) {
	if provider.AuthType == domain.AuthTypePeer {
		// 对端 provider 是父网关的一扇门，不是某个具体的 Claude 兼容上游：
		// 下面所有整流（stop_sequences 剪裁、thinking 关闭写法、被拒签名记忆）
		// 都是围着某个真实上游的校验习惯转的，对门后面的真实上游一无所知。
		// 报文原样进信封，由父网关按它自己选中的那个 provider 去整形。
		return s.sendPeerDoor(r.Context(), r, provider, stringValue(claudeBodyField(body, "model")), body, r.Header.Get("Accept"))
	}
	isOAuth := provider.AuthType == domain.AuthTypeClaudeOAuth
	// 这个上游拒过的 thinking 签名先剥掉：一次拒签之后，对话的后续每一轮都不必
	// 再花一次 400 去重新学同一件事（对标 magpie thinking_bound.go 的记忆）。
	body = stripRefusedClaudeThinkingBody(body, provider)
	// 直通 Claude 兼容上游（智谱 GLM 等）的校验与 Anthropic 不同：stop_sequences
	// 非字符串项直接 400、空串会让流在第一个 token 前就停止、超过 4 条被拒；
	// 关闭 thinking 的写法也随模型族而变（见 normalizeClaudeThinkingOff）。
	// Claude 自家的 OAuth 路径已在 buildClaudeOAuthRequest 里整流过。
	if !isOAuth {
		body = sanitizeClaudeStopSequencesBody(body)
		body = normalizeClaudeEndpointThinkingOffBody(body, applyProviderModelMapping(provider, stringValue(claudeBodyField(body, "model"))))
	}

	var err error
	var request *http.Request
	if isOAuth {
		// Claude OAuth requests authenticate with a bearer access token (plus the
		// billing marker + anthropic-beta header), never the x-api-key header
		// used by ordinary Claude API-key providers.
		neededRefresh := provider.ClaudeOAuth != nil && claudeTokenNeedsRefresh(provider.ClaudeOAuth)
		refreshed, refreshErr := s.ensureFreshClaudeToken(provider)
		if refreshErr != nil {
			return nil, refreshErr
		}
		if neededRefresh {
			markTimingFlag(r.Context(), timingFlagOAuthRefresh)
			markTimingFlag(r.Context(), timingFlagSaveState)
		}
		provider = refreshed
		request, _, err = buildClaudeOAuthRequest(r.Context(), provider, body, r.Header.Get("anthropic-beta"), true, claudeMessagesURL)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", r.Header.Get("Accept"))
	} else if site, isWorkBuddy := workBuddySiteForAuthType(provider.AuthType); isWorkBuddy && workBuddyMessagesApplies(provider, workBuddyMessagesBodyModel(body), requestBodyWantsStream(body)) {
		// The China build serves its Claude models on the Messages route; that is the
		// only place `output_config.effort` is honoured (see workbuddy_messages.go).
		// Every other model keeps the chat path below, which is where the reference
		// measured it working.
		body = sanitizeWorkBuddyMessagesBody(body)
		request, err = newWorkBuddyMessagesRequest(r.Context(), provider, workBuddyMessagesURL(site), body)
		if err != nil {
			return nil, err
		}
		client := workBuddyChatClient(provider)
		return client.Do(workBuddyRequestExit(request))
	} else {
		upstreamURL := strings.TrimSpace(provider.BaseURL)
		if upstreamURL == "" {
			upstreamURL = claudeMessagesURL
		} else if !strings.Contains(strings.ToLower(upstreamURL), "/messages") {
			// A Claude API-key provider's BaseURL is usually a base (…/v1), not
			// the full endpoint. Mirrors executeClaudeMessagesHTTP: without this
			// the pass-through POSTs to the bare base and new-api style relays
			// answer 404 "Invalid URL (POST /v1)".
			upstreamURL = strings.TrimRight(upstreamURL, "/") + "/messages"
		}
		request, err = http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", r.Header.Get("Accept"))
		request.Header.Set("anthropic-version", "2023-06-01")
		// per-provider 的自定义请求头（requestAdapter.headers）。Claude 这条直通
		// 路径以前漏了这一步：OpenCode Go 这类上游要求额外的路由头
		// （x-opencode-session），配了也发不出去，只会换来一句 400 MissingSessionID。
		applyRequestAdapterHeaders(request, provider, stringValue(claudeBodyField(body, "model")))
		// Pass the client's anthropic-beta through (some relays require specific
		// flags), and auto-enable the 1M-context beta for 1M-capable models:
		// new-api style relays otherwise reject with 400
		// "1m 上下文已经全量可用，请启用 1m 上下文后重试".
		beta := strings.TrimSpace(r.Header.Get("anthropic-beta"))
		if isClaude1MContextModel(stringFromRequestBody(body)) {
			beta = mergeAnthropicBetaFlags(beta, claudeContext1MBeta)
		}
		if beta != "" {
			request.Header.Set("anthropic-beta", beta)
		}
		applyProviderAuth(request, provider, r.Header.Get("Authorization"))
		applyOpenCodeGoClaudeHeaders(request, provider, body)
	}

	return doHTTPWithTiming(r.Context(), &http.Client{Timeout: 0}, request)
}

// maybeRectifyClaudeThinking inspects a 400 response for the thinking-signature
// error family; if matched, it strips historical thinking blocks / signatures
// from the request and retries once against the same provider. It returns the
// retry response (and true) when a retry was actually issued; otherwise it
// rebuilds the original 400 response body so the caller can forward it intact.
func (s *Server) maybeRectifyClaudeThinking(r *http.Request, provider domain.Provider, body []byte, response *http.Response) (*http.Response, bool) {
	return s.maybeRectifyClaudeThinkingResend(r, provider, body, response, func(rectified []byte) (*http.Response, error) {
		return s.sendClaudeMessagesUpstream(r, provider, rectified)
	})
}

// maybeRectifyClaudeThinkingResend is the resend-agnostic core of
// maybeRectifyClaudeThinking. The passthrough path resends via
// sendClaudeMessagesUpstream; converted paths (e.g. Responses<->Claude) must
// resend via doClaudeProviderRequest with their own Accept header, so they
// inject a matching resend closure here instead.
func (s *Server) maybeRectifyClaudeThinkingResend(r *http.Request, provider domain.Provider, body []byte, response *http.Response, resend func([]byte) (*http.Response, error)) (*http.Response, bool) {
	errBody, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	// Always restore a readable body on the original response for the no-retry paths.
	response.Body = io.NopCloser(bytes.NewReader(errBody))
	if readErr != nil {
		return response, false
	}
	if !shouldRectifyThinkingSignature(errBody) {
		return response, false
	}
	// 先按 400 指到的位置精准剥离（对标 magpie thinking_bound.go）：只删被拒
	// 的那一块及其后的 thinking，并记住这些签名，下一轮首次就剥掉。精准剥离
	// 命中不了（错误没指向具体块）时才回退到整请求整流，保留原有覆盖面。
	rectifiedBody, result, boundApplied := rectifyClaudeThinkingBoundBody(body, errBody, provider)
	if boundApplied {
		s.logs.AddApp("info", "thinking signature refused, cutting from the named block", fmt.Sprintf(
			"provider=%s removed_thinking=%d removed_redacted=%d",
			provider.ID, result.removedThinkingBlocks, result.removedRedactedThinkingBlocks))
		markTimingFlag(r.Context(), timingFlagThinkingRectify)
		retryResp, retryErr := resend(rectifiedBody)
		if retryErr != nil {
			s.logs.AddApp("warn", "thinking rectifier retry transport error", retryErr.Error())
			dumpThinkingRectify(provider.ID, body, rectifiedBody, errBody, nil, retryErr, result)
			return response, false
		}
		dumpThinkingRectify(provider.ID, body, rectifiedBody, errBody, retryResp, nil, result)
		return retryResp, true
	}
	rectifiedBody, result = rectifyClaudeThinkingBody(body)
	if !result.applied {
		s.logs.AddApp("warn", "thinking rectifier matched but nothing to strip", fmt.Sprintf("provider=%s", provider.ID))
		return response, false
	}
	s.logs.AddApp("info", "thinking signature rectifier retrying", fmt.Sprintf(
		"provider=%s removed_thinking=%d removed_redacted=%d removed_sig=%d top_level=%t",
		provider.ID, result.removedThinkingBlocks, result.removedRedactedThinkingBlocks,
		result.removedSignatureFields, result.removedTopLevelThinking))
	markTimingFlag(r.Context(), timingFlagThinkingRectify)
	retryResp, retryErr := resend(rectifiedBody)
	if retryErr != nil {
		s.logs.AddApp("warn", "thinking rectifier retry transport error", retryErr.Error())
		dumpThinkingRectify(provider.ID, body, rectifiedBody, errBody, nil, retryErr, result)
		return response, false
	}
	dumpThinkingRectify(provider.ID, body, rectifiedBody, errBody, retryResp, nil, result)
	return retryResp, true
}

func (s *Server) doClaudeProviderRequest(ctx context.Context, r *http.Request, provider domain.Provider, body []byte, accept string) (*http.Response, error) {
	if provider.AuthType == domain.AuthTypePeer {
		// 对端 provider 是父网关的一扇门：报文进信封，由父网关交给它自己
		// 选中的 provider（下面那些整流都是围着某个真实上游的校验习惯转的，
		// 对门后面是谁一无所知）。
		return s.sendPeerDoor(ctx, r, provider, stringValue(claudeBodyField(body, "model")), body, accept)
	}
	isOAuth := provider.AuthType == domain.AuthTypeClaudeOAuth
	// 同上：这个上游拒过的 thinking 签名不再重复提交。
	body = stripRefusedClaudeThinkingBody(body, provider)
	var request *http.Request
	var err error
	if isOAuth {
		neededRefresh := provider.ClaudeOAuth != nil && claudeTokenNeedsRefresh(provider.ClaudeOAuth)
		refreshed, refreshErr := s.ensureFreshClaudeToken(provider)
		if refreshErr != nil {
			return nil, refreshErr
		}
		if neededRefresh {
			markTimingFlag(ctx, timingFlagOAuthRefresh)
			markTimingFlag(ctx, timingFlagSaveState)
		}
		provider = refreshed
		request, _, err = buildClaudeOAuthRequest(ctx, provider, body, r.Header.Get("anthropic-beta"), false, claudeMessagesURL)
		if err != nil {
			return nil, err
		}
	} else {
		upstreamURL := strings.TrimSpace(provider.BaseURL)
		if upstreamURL == "" || strings.Contains(strings.ToLower(upstreamURL), "anthropic.com") {
			upstreamURL = claudeMessagesURL
		} else if !strings.Contains(strings.ToLower(upstreamURL), "/messages") {
			upstreamURL = strings.TrimRight(upstreamURL, "/") + "/messages"
		}
		request, err = http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("anthropic-version", "2023-06-01")
		applyProviderAuth(request, provider, r.Header.Get("Authorization"))
		// 转换路径同样要带上 per-provider 自定义头，否则「同一台上游，直通能通、
		// 从别的协议转进来就 400」——排查起来最难的一类差异。
		applyRequestAdapterHeaders(request, provider, stringValue(claudeBodyField(body, "model")))
		applyOpenCodeGoClaudeHeaders(request, provider, body)
	}
	if accept != "" {
		request.Header.Set("Accept", accept)
	} else {
		request.Header.Set("Accept", "application/json")
	}
	return doHTTPWithTiming(ctx, &http.Client{Timeout: 0}, request)
}

func (s *Server) proxyClaudeToOpenAIChat(w http.ResponseWriter, r *http.Request, provider domain.Provider, model string, openAIReq map[string]any, skipIncomingAuth bool) (int, TokenUsage, []byte, error) {
	_ = skipIncomingAuth
	// 确保转换与 max_tokens 预算使用 Provider 映射后的实际上游模型，而非客户端请求名。
	model = applyProviderModelMapping(provider, model)
	clientToolNames := extractOpenAIToolNames(openAIReq)
	claudeReq, err := openAIChatToClaudeRequest(openAIReq, model, maxOutputTokensOverrideFrom(r.Context()))
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	r, clientStopSequences := rememberConvertedStopSequences(r, claudeReq["stop_sequences"])
	stream, _ := claudeReq["stream"].(bool)
	upstreamBody, err := json.Marshal(claudeReq)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	accept := "application/json"
	if stream {
		accept = "text/event-stream"
	}
	send := func() (*http.Response, error) {
		response, err := s.doClaudeProviderRequest(r.Context(), r, provider, upstreamBody, accept)
		if err != nil || response == nil || response.StatusCode != http.StatusBadRequest {
			return response, err
		}
		// Thinking 签名整流：Chat→Claude 历史回放也可能触发 cannot be modified。
		if rectified, ok := s.maybeRectifyClaudeThinkingResend(r, provider, upstreamBody, response, func(b []byte) (*http.Response, error) {
			return s.doClaudeProviderRequest(r.Context(), r, provider, b, accept)
		}); ok {
			return rectified, nil
		}
		return response, nil
	}
	convert := func(body []byte, modelName string) ([]byte, TokenUsage, error) {
		return claudeResponseToOpenAIChat(body, modelName, clientToolNames, clientStopSequences...)
	}
	streamConvert := func(w http.ResponseWriter, reader io.Reader, modelName string) (TokenUsage, error) {
		return streamClaudeToOpenAIChatEvents(w, reader, modelName, clientToolNames, clientStopSequences...)
	}
	return s.finishConvertedProxyWithEmptyStreamRetry(w, r, model, stream, send, convert, streamConvert)
}

func applyProviderAuth(request *http.Request, provider domain.Provider, incomingAuth string) {
	if authValue := resolveProviderAuth(provider); authValue != "" {
		header := provider.AuthHeader
		if header == "" {
			header = "Authorization"
		}
		if strings.EqualFold(header, "Authorization") && !strings.HasPrefix(strings.ToLower(authValue), "bearer ") {
			authValue = "Bearer " + authValue
		}
		request.Header.Set(header, authValue)
	} else if incomingAuth != "" {
		request.Header.Set("Authorization", incomingAuth)
	}
	// A WorkBuddy request is always signed by its own account, so these
	// overwrite whatever the generic path above put in Authorization.
	applyWorkBuddyHeaders(request, provider)
	// A ZCode plan's key goes in the Anthropic header and the bearer both, which
	// the generic path above only sets one of.
	applyZCodeHeaders(request, provider)
}

func resolveProviderAuth(provider domain.Provider) string {
	// Qoder carries its bearer in the credential rather than APIKeySource. This
	// is a pure lookup: the exchange itself happens in ensureFreshQoderToken,
	// since this function has no context to make a network call with.
	if isQoderProvider(provider) {
		if provider.QoderPAT != nil {
			return strings.TrimSpace(provider.QoderPAT.AccessToken)
		}
		return ""
	}
	// WorkBuddy likewise carries its bearer in the credential; the token itself
	// is renewed by ensureFreshWorkBuddyToken on the forward path.
	if isWorkBuddyProvider(provider) {
		if provider.WorkBuddy != nil {
			return strings.TrimSpace(provider.WorkBuddy.AccessToken)
		}
		return ""
	}
	// A ZCode provider's key is the one its sign-in minted, never one typed into
	// the form, so it lives in the credential as well.
	if isZCodeProvider(provider) {
		if provider.ZCode != nil && !provider.ZCode.Disconnected {
			return strings.TrimSpace(provider.ZCode.APIKey)
		}
		return ""
	}
	source := strings.TrimSpace(provider.APIKeySource)
	if source == "" {
		// Zen's free tier is served to OpenCode's anonymous key, so a Zen
		// provider with no key configured still goes out authenticated. An
		// operator who did paste one has it used instead, which is what reaches
		// the paid models.
		if provider.AuthType == domain.AuthTypeOpenCodeZenFree {
			return openCodeZenAnonymousKey
		}
		return ""
	}
	if strings.HasPrefix(source, "env:") {
		return strings.TrimSpace(os.Getenv(strings.TrimPrefix(source, "env:")))
	}
	if strings.HasPrefix(source, "literal:") {
		return strings.TrimSpace(strings.TrimPrefix(source, "literal:"))
	}
	if strings.HasPrefix(source, "keychain:") {
		// Future improvement: resolve secrets from the macOS Keychain here so they
		// never need to be stored on disk. apiKeySource is persisted verbatim today.
		return ""
	}
	return source
}

func (s *Server) writeConversionPlaceholder(w http.ResponseWriter, model string, decision domain.RouteDecision, stream bool) []byte {
	message := fmt.Sprintf("Protocol conversion is not implemented yet: %s.", decision.ConversionLabel)
	if stream {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusNotImplemented)
		payload := map[string]any{
			"id":      "chatcmpl-conversion-placeholder",
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   model,
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": message}, "finish_reason": "stop"}},
		}
		bytes, _ := json.Marshal(payload)
		fmt.Fprintf(w, "data: %s\n\n", bytes)
		fmt.Fprint(w, "data: [DONE]\n\n")
		return []byte(fmt.Sprintf("data: %s\n\ndata: [DONE]\n\n", bytes))
	}
	body := map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "conversion_not_implemented",
		},
	}
	writeJSON(w, http.StatusNotImplemented, body)
	encoded, _ := json.Marshal(body)
	return encoded
}

func (s *Server) writeMockOpenAIResponse(w http.ResponseWriter, model string, decision domain.RouteDecision) {
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      "chatcmpl-prototype",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": fmt.Sprintf("Protocol Gateway MVP: %s via %s.", decision.Action, decision.ConversionLabel),
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 22, "total_tokens": 32},
	})
}

func (s *Server) writeMockOpenAIStream(w http.ResponseWriter, model string, decision domain.RouteDecision) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)
	chunks := []string{
		"Protocol Gateway MVP: ",
		decision.Action,
		" via ",
		decision.ConversionLabel,
		".",
	}
	for _, chunk := range chunks {
		payload := map[string]any{
			"id":      "chatcmpl-prototype",
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   model,
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": chunk}, "finish_reason": nil}},
		}
		bytes, _ := json.Marshal(payload)
		fmt.Fprintf(w, "data: %s\n\n", bytes)
		if flusher != nil {
			flusher.Flush()
		}
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// writeJSONGzip is writeJSON with transparent gzip for callers whose payload
// grows with the number of retained requests.
//
// 用量分析要一次取回整个区间的每一条请求（前端在本地做筛选、分位数与导出），
// 保留期内上万行时体积以 MB 计，实测 9504 行 6.3 MB；其中绝大部分等待时间是
// 传输而不是查询。同一份 JSON 用 gzip 只占 1/10，客户端无需任何改动——浏览器
// 的 fetch 会自动解压。不支持 gzip 的客户端拿到的仍是原来的明文 JSON。
func writeJSONGzip(w http.ResponseWriter, r *http.Request, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Add("Vary", "Accept-Encoding")
	if r == nil || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(value)
		return
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.WriteHeader(status)
	// 5 级：这类重复键名极多的 JSON 压缩比本身就有 9~10 倍，再往上只多几个
	// 百分点却要多花几十毫秒 CPU——等待主要花在传输上，没必要在这换。
	zw, err := gzip.NewWriterLevel(w, 5)
	if err != nil {
		return
	}
	if err := json.NewEncoder(zw).Encode(value); err != nil {
		_ = zw.Close()
		return
	}
	_ = zw.Close()
}

// acceptsGzip reports whether the client asked for gzip, honouring an explicit
// "gzip;q=0" opt-out instead of just looking for the substring.
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(part, ";")
		if !strings.EqualFold(strings.TrimSpace(fields[0]), "gzip") {
			continue
		}
		for _, param := range fields[1:] {
			param = strings.TrimSpace(param)
			if !strings.HasPrefix(strings.ToLower(param), "q=") {
				continue
			}
			if q, err := strconv.ParseFloat(strings.TrimSpace(param[2:]), 64); err == nil && q == 0 {
				return false
			}
		}
		return true
	}
	return false
}

func writeOpenAIError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": "gateway_error"}})
}

func writeClaudeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "gateway_error",
			"message": message,
		},
	})
}

func extractConsumerAPIKey(r *http.Request) string {
	if key := strings.TrimSpace(r.Header.Get("x-api-key")); key != "" {
		return key
	}
	// Google's SDKs authenticate with x-goog-api-key (or ?key=, see
	// extractGeminiAPIKey): a Gemini client pointed at this gateway should not
	// have to learn a different header.
	if key := strings.TrimSpace(r.Header.Get("x-goog-api-key")); key != "" {
		return key
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

// imagesRefusal is the status/message pair of an images request refused before
// any credential is read or request sent.
type imagesRefusal struct {
	status  int
	message string
}

// imagesRouteRefusal rejects a route whose output can never serve images, before
// the handler reads credentials or builds an upstream URL. The route's default
// provider is checked too, but the provider actually used is validated
// separately by imagesProviderRefusal once the key's binding is resolved.
func imagesRouteRefusal(router *Router, route domain.Route) (imagesRefusal, bool) {
	refusal := imagesRefusal{}
	if !route.Enabled {
		refusal.status = http.StatusServiceUnavailable
		refusal.message = fmt.Sprintf("route %q is disabled", route.ID)
		return refusal, true
	}
	refusal, ok := imagesProviderRefusal(router, route.ProviderID)
	if ok {
		if refusal.status == http.StatusServiceUnavailable {
			refusal.message = fmt.Sprintf("route %q default provider is disabled", route.ID)
		}
		return refusal, true
	}
	return refusal, false
}

// imagesProviderRefusal rejects a provider that cannot serve images at all:
// disabled, soft-deleted or speaking a protocol with no images/generations
// endpoint.
func imagesProviderRefusal(router *Router, providerID string) (imagesRefusal, bool) {
	refusal := imagesRefusal{}
	provider, err := router.ProviderByID(providerID)
	if err != nil {
		refusal.status = http.StatusBadGateway
		refusal.message = err.Error()
		return refusal, true
	}
	if provider.Disabled || provider.Deleted {
		refusal.status = http.StatusServiceUnavailable
		refusal.message = fmt.Sprintf("provider %q is disabled", provider.ID)
		return refusal, true
	}
	if provider.Protocol != domain.ProtocolOpenAIChat {
		refusal.status = http.StatusBadGateway
		refusal.message = fmt.Sprintf("provider %q speaks %s, which has no images/generations endpoint", provider.ID, provider.Protocol.DisplayName())
		return refusal, true
	}
	return refusal, false
}
