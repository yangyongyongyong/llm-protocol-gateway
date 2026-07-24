package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/luca/llm-protocol-gateway/internal/cursor"
	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
	"github.com/luca/llm-protocol-gateway/internal/tunnel"
)

const (
	headerGatewayRouteID      = "X-Gateway-Route-Id"
	headerGatewayInternalTest = "X-Gateway-Internal-Test"
)

type StateSaver interface {
	Save(domain.GatewayState) error
}

type APIKeyStore interface {
	CreateAPIKey(domain.APIKey) error
	UpdateAPIKey(domain.APIKey) error
	DeleteAPIKey(id string) error
	TouchAPIKey(id string, lastUsedAt string) error
}

// ProviderOAuthSaver persists just the OAuth token columns of one provider,
// avoiding a full-state rewrite on every token refresh.
type ProviderOAuthSaver interface {
	UpdateProviderOAuth(providerID, accessToken, refreshToken, expiresAt, scope, accountLabel string) error
}

type RequestLogStore interface {
	AppendRequestLog(monitor.RequestLog) error
	AppendRequestLogWithRetention(monitor.RequestLog, int) error
	ListRequestLogs(int) ([]monitor.RequestLog, error)
	QueryRequestLogs(monitor.RequestLogQuery) (monitor.RequestLogPage, error)
	PruneRequestLogs(int) error
}

type UsageDailyStore interface {
	monitor.UsageDailyStore
}

type Server struct {
	router                  *Router
	logs                    *monitor.Store
	stateSaver              StateSaver
	apiKeyStore             APIKeyStore
	apiKeyToucher           *apiKeyToucher
	providerOAuthSaver      ProviderOAuthSaver
	requestLogStore         RequestLogStore
	usageDailyStore         UsageDailyStore
	tunnels                 *tunnel.Manager
	pendingClaudeOAuth      *claudeOAuthPendingStore
	pendingCursorOAuth      *cursorOAuthPendingStore
	pendingChatGPTOAuth     *chatgptOAuthPendingStore
	cursorBridge            *cursor.Bridge
	listenAddr              string
	requestLogRetentionDays int
	adminAuth               AdminAuthStore
	userStore               UserStore
	oauthUsageCache         *oauthUsageCache
	requestStatsCache       *requestStatsCache
	// oauthUsageFetchMu serializes upstream usage fetches per provider so
	// concurrent UI polls share one in-flight request after cache miss.
	oauthUsageFetchMu sync.Map
	// webExposedChange is invoked when the UI toggles LAN/Web exposure so the
	// process owner (CLI runtime / desktop app) can rebind the HTTP listener.
	webExposedChange func(enabled bool) error

	// selfcheckJobs holds in-memory async self-check jobs (CLI smoke tests).
	selfcheckMu   sync.Mutex
	selfcheckJobs map[string]*selfcheckJob

	// providerAvailability tracks providers that a live upstream request found
	// unavailable (hard quota/auth, or repeated soft 429/5xx/transport failures),
	// keyed by provider ID. Runtime-only, never persisted; cleared as soon as a
	// live request or probe succeeds again. Soft failures require consecutive
	// hits before appearing here (see classifyProviderFailover).
	providerAvailabilityMu sync.Mutex
	providerAvailability   map[string]providerUnavailableState
	providerSoftFailures   map[string]providerSoftFailureState

	// userActivity tracks each console user's latest API request time.
	// In-memory values are exact; StartUserActivityFlush persists them at most
	// once per userActivityPersistMinGap per user (see user_activity.go).
	userActivityMu sync.Mutex
	userActivity   map[string]*userActivityEntry
}

func NewServer(router *Router, logs *monitor.Store, stateSaver ...StateSaver) *Server {
	server := &Server{
		router:                  router,
		logs:                    logs,
		pendingClaudeOAuth:      newClaudeOAuthPendingStore(),
		pendingCursorOAuth:      newCursorOAuthPendingStore(),
		pendingChatGPTOAuth:     newChatGPTOAuthPendingStore(),
		oauthUsageCache:         newOAuthUsageCache(),
		requestStatsCache:       newRequestStatsCache(),
		requestLogRetentionDays: 7,
	}
	if days := router.State().RequestLogRetentionDays; days > 0 {
		server.requestLogRetentionDays = days
	}
	if len(stateSaver) > 0 {
		server.stateSaver = stateSaver[0]
		if store, ok := stateSaver[0].(APIKeyStore); ok {
			server.apiKeyStore = store
			// Coalesce last_used_at writes off the request hot path.
			server.apiKeyToucher = newAPIKeyToucher(store, router, 2*time.Second)
		}
		if pos, ok := stateSaver[0].(ProviderOAuthSaver); ok {
			server.providerOAuthSaver = pos
		}
		if rls, ok := stateSaver[0].(RequestLogStore); ok {
			server.requestLogStore = rls
		}
		if uds, ok := stateSaver[0].(UsageDailyStore); ok {
			server.usageDailyStore = uds
			logs.SetUsageDailyStore(uds)
		}
		if auth, ok := stateSaver[0].(AdminAuthStore); ok {
			server.adminAuth = auth
		}
		if users, ok := stateSaver[0].(UserStore); ok {
			server.userStore = users
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

// SaveState persists the current router state (including webExposed).
func (s *Server) SaveState() error {
	return s.saveState()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__health", s.handleHealth)
	mux.HandleFunc("GET /__auth/status", s.handleAuthStatus)
	mux.HandleFunc("POST /__auth/setup", s.handleAuthSetup)
	mux.HandleFunc("POST /__auth/login", s.handleAuthLogin)
	mux.HandleFunc("POST /__auth/logout", s.handleAuthLogout)
	mux.HandleFunc("POST /__auth/password", s.handleAuthPassword)
	mux.HandleFunc("GET /__state", s.handleState)
	mux.HandleFunc("GET /__settings/paths", s.handleSettingsPaths)
	mux.HandleFunc("GET /__logs", s.handleLogs)
	mux.HandleFunc("GET /__request-stats", s.handleRequestStats)
	mux.HandleFunc("GET /__app/logs", s.handleAppLogs)
	mux.HandleFunc("PATCH /__app/log-level", s.handleSetLogLevel)
	mux.HandleFunc("PATCH /__settings/request-log-retention", s.handleSetRequestLogRetention)
	mux.HandleFunc("PATCH /__settings/web-exposed", s.handleSetWebExposed)
	mux.HandleFunc("PATCH /__public-access", s.handleUpdatePublicAccess)
	mux.HandleFunc("PATCH /__endpoints/{id}", s.handleUpdateEndpoint)
	mux.HandleFunc("GET /__public", s.handlePublicStatus)
	mux.HandleFunc("POST /__public/start", s.handlePublicStart)
	mux.HandleFunc("POST /__public/stop", s.handlePublicStop)
	mux.HandleFunc("POST /__public/cloudflare/login/start", s.handleCloudflareLoginStart)
	mux.HandleFunc("GET /__public/cloudflare/login/status", s.handleCloudflareLoginStatus)
	mux.HandleFunc("GET /__public/cloudflare/zones", s.handleCloudflareZones)
	mux.HandleFunc("POST /__public/cloudflare/bind", s.handleCloudflareBind)
	mux.HandleFunc("POST /__providers", s.handleCreateProvider)
	mux.HandleFunc("PATCH /__providers/{id}", s.handleUpdateProvider)
	mux.HandleFunc("GET /__providers/export", s.handleExportProviders)
	mux.HandleFunc("POST /__providers/export", s.handleExportProviders)
	mux.HandleFunc("POST /__providers/import", s.handleImportProviders)
	mux.HandleFunc("POST /__providers/{id}/test", s.handleTestProvider)
	mux.HandleFunc("POST /__providers/{id}/enabled", s.handleSetProviderEnabled)
	mux.HandleFunc("POST /__providers/{id}/self-register-token", s.handleGenerateProviderSelfRegistrationToken)
	mux.HandleFunc("POST /__providers/{id}/self-register-token/revoke", s.handleRevokeProviderSelfRegistration)
	mux.HandleFunc("PATCH /__providers/{id}/self-register", s.handleProviderSelfRegister)
	mux.HandleFunc("POST /__providers/{id}/self-check/health", s.handleProviderSelfCheckHealth)
	mux.HandleFunc("POST /__providers/{id}/self-check/chat", s.handleProviderSelfCheckChat)
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
	mux.HandleFunc("GET /__providers/{id}/zhipu/usage", s.handleZhipuUsage)
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
	mux.HandleFunc("GET /__users", s.handleListUsers)
	mux.HandleFunc("POST /__users", s.handleCreateUser)
	mux.HandleFunc("PATCH /__users/{id}", s.handleUpdateUser)
	mux.HandleFunc("DELETE /__users/{id}", s.handleDeleteUser)
	mux.HandleFunc("POST /__users/{id}/reset-password", s.handleResetUserPassword)
	mux.HandleFunc("GET /__selfcheck/tools", s.handleSelfcheckTools)
	mux.HandleFunc("POST /__selfcheck", s.handleSelfcheckStart)
	mux.HandleFunc("GET /__selfcheck/{jobId}", s.handleSelfcheckStatus)
	mux.HandleFunc("POST /__selfcheck/{jobId}/retry/{caseId}", s.handleSelfcheckRetry)
	mux.HandleFunc("GET /v1/models", s.handleOpenAIModels)
	mux.HandleFunc("GET /openai/v1/models", s.handleOpenAIModels)
	mux.HandleFunc("GET /anthropic/v1/models", s.handleClaudeModels)
	mux.HandleFunc("POST /v1/chat/completions", s.handleOpenAIChat)
	mux.HandleFunc("POST /chat/completions", s.handleOpenAIChat)
	mux.HandleFunc("POST /v1/images/generations", s.handleOpenAIImagesGenerations)
	mux.HandleFunc("POST /openai/v1/images/generations", s.handleOpenAIImagesGenerations)
	mux.HandleFunc("POST /openai/v1/responses", s.handleOpenAIResponses)
	mux.HandleFunc("POST /anthropic/v1/messages", s.handleClaudeMessages)
	mux.HandleFunc("POST /anthropic/v1/messages/count_tokens", s.handleClaudeCountTokens)
	api := withCORS(mux)
	return s.withAdminAuth(withHostSeparatedServing(api, findWebDistDir(), s.publicHostRole))
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
	if !identity.isAdmin() {
		writeJSON(w, http.StatusOK, s.stateForUser(identity, state))
		return
	}
	state.RequestLogRetentionDays = s.RequestLogRetentionDays()
	state.WebExposed = s.router.WebExposed()
	paths := ResolveDataPaths()
	state.DataPaths = &paths
	writeJSON(w, http.StatusOK, state)
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
	// Regenerate curl against the real BaseURL for UI display.
	enrichProviderAdapterCurl(&provider)
	return provider
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	identity := s.requestIdentity(r)
	query := monitor.RequestLogQuery{
		Status:        strings.TrimSpace(r.URL.Query().Get("status")),
		APIKeyName:    strings.TrimSpace(r.URL.Query().Get("apiKeyName")),
		ProviderID:    strings.TrimSpace(r.URL.Query().Get("providerId")),
		Page:          atoiDefault(r.URL.Query().Get("page"), 1),
		PageSize:      atoiDefault(r.URL.Query().Get("pageSize"), 100),
		IncludeBodies: parseBoolQuery(r.URL.Query().Get("includeBodies")),
	}
	if !identity.isAdmin() {
		// Normal users only see logs of their own keys (empty set matches nothing).
		query.APIKeyIDs = s.ownedKeyIDs(identity.UserID)
	} else if ownerUserID := strings.TrimSpace(r.URL.Query().Get("ownerUserId")); ownerUserID != "" {
		// Admin-only "所属用户" log filter; empty param means no filter (see all users).
		query.APIKeyIDs = s.ownedKeyIDsForOwnerFilter(ownerUserID)
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
	// beforeId freezes a pagination snapshot: the console captures the newest
	// log id when leaving page 1 so offset paging stays stable as new logs arrive.
	if beforeID := strings.TrimSpace(r.URL.Query().Get("beforeId")); beforeID != "" {
		if parsed, err := strconv.ParseInt(beforeID, 10, 64); err == nil && parsed > 0 {
			query.BeforeID = parsed
		}
	}
	if s.requestLogStore != nil {
		page, err := s.requestLogStore.QueryRequestLogs(query)
		if err == nil {
			if !query.IncludeBodies {
				stripRequestLogBodies(page.Items)
			}
			s.fillRequestLogUserNames(page.Items)
			writeJSON(w, http.StatusOK, page)
			return
		}
		s.logs.AddApp("warn", "query request logs failed", err.Error())
	}
	page := s.logs.Query(query)
	if !query.IncludeBodies {
		stripRequestLogBodies(page.Items)
	}
	s.fillRequestLogUserNames(page.Items)
	writeJSON(w, http.StatusOK, page)
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
	to := from.Add(24 * time.Hour)
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
		} else if parsed, err := time.ParseInLocation("2006-01-02", raw, localNow.Location()); err == nil {
			to = parsed.Add(24 * time.Hour)
		}
	}
	cacheKey := from.Format(time.RFC3339) + "|" + to.Format(time.RFC3339)
	identity := s.requestIdentity(r)
	if !identity.isAdmin() {
		// Per-user stats: computed from own keys only; cached per user.
		keyIDs := s.ownedKeyIDs(identity.UserID)
		userCacheKey := cacheKey + "|user:" + identity.UserID
		if cached, ok := s.requestStatsCache.get(userCacheKey); ok {
			w.Header().Set("Cache-Control", "private, max-age=5")
			writeJSON(w, http.StatusOK, cached)
			return
		}
		snapshot := s.logs.UsageStatsRangeForKeys(now, from, to, keyIDs)
		s.requestStatsCache.set(userCacheKey, snapshot)
		w.Header().Set("Cache-Control", "private, max-age=5")
		writeJSON(w, http.StatusOK, snapshot)
		return
	}
	if cached, ok := s.requestStatsCache.get(cacheKey); ok {
		w.Header().Set("Cache-Control", "private, max-age=5")
		writeJSON(w, http.StatusOK, cached)
		return
	}
	snapshot := s.logs.UsageStatsRange(now, from, to)
	s.fillUsageUserNames(&snapshot)
	s.requestStatsCache.set(cacheKey, snapshot)
	w.Header().Set("Cache-Control", "private, max-age=5")
	writeJSON(w, http.StatusOK, snapshot)
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

// usageBucketSchema* gate a one-time backfill when the daily-bucket dimensions
// change (e.g. adding the output_protocol bucket). Existing deployments' buckets
// lack new dimensions; bumping the version forces exactly one rebuild-from-logs
// after upgrade, then the fast bucket-load path resumes on later startups.
const (
	usageBucketSchemaSettingKey = "usageDailyBucketSchema"
	usageBucketSchemaVersion    = "2" // v2 adds the output_protocol dimension
)

// RebuildUsageStats loads persisted daily aggregates, or replays request logs once
// to backfill when the usage_daily tables are empty or the bucket schema changed.
func (s *Server) RebuildUsageStats() {
	retention := s.RequestLogRetentionDays()
	since := time.Now().AddDate(0, 0, -retention)

	settings, _ := s.usageDailyStore.(interface {
		Setting(string) string
		SetSetting(string, string) error
	})
	needsBackfill := settings != nil && settings.Setting(usageBucketSchemaSettingKey) != usageBucketSchemaVersion

	if s.usageDailyStore != nil {
		days, last, err := s.usageDailyStore.LoadUsageSince(since)
		if err != nil {
			s.logs.AddApp("warn", "load usage daily aggregates failed", err.Error())
		} else if !needsBackfill && len(days) > 0 && s.usageDailyAggregatesMatchLogs(days) {
			s.logs.ResetUsageStats()
			s.logs.BootstrapUsageDays(days, last)
			return
		}
		if err := s.usageDailyStore.ClearUsageDaily(); err != nil {
			s.logs.AddApp("warn", "clear usage daily aggregates failed", err.Error())
		}
	}

	s.rebuildUsageStatsFromLogs(since)
	if settings != nil {
		if err := settings.SetSetting(usageBucketSchemaSettingKey, usageBucketSchemaVersion); err != nil {
			s.logs.AddApp("warn", "persist usage bucket schema version failed", err.Error())
		}
	}
}

func (s *Server) usageDailyAggregatesMatchLogs(days map[string]monitor.UsageDayBuckets) bool {
	var agg int64
	for _, day := range days {
		agg += day.Total.RequestCount
	}
	if agg == 0 {
		return false
	}
	counter, ok := s.usageDailyStore.(interface {
		CountRequestLogs() (int64, error)
	})
	if !ok {
		return true
	}
	logCount, err := counter.CountRequestLogs()
	if err != nil || logCount == 0 {
		return true
	}
	// Allow small drift from retention pruning vs loaded window.
	if agg > logCount {
		return false
	}
	return agg >= logCount-5
}

func (s *Server) rebuildUsageStatsFromLogs(since time.Time) {
	s.logs.ResetUsageStats()
	logs := s.logs.List(memoryLogCap)
	if s.requestLogStore != nil {
		if all := s.loadRequestLogsForStats(since, time.Now().Add(24*time.Hour), memoryLogCap); len(all) > len(logs) {
			logs = all
		}
	}
	if len(logs) == 0 {
		return
	}
	state := s.router.State()
	keysByID := make(map[string]domain.APIKey, len(state.APIKeys))
	keysByName := make(map[string]domain.APIKey, len(state.APIKeys))
	for _, key := range state.APIKeys {
		keysByID[key.ID] = key
		if name := strings.TrimSpace(key.Name); name != "" {
			keysByName[strings.ToLower(name)] = key
		}
	}
	for _, log := range logs {
		resolved := resolveRequestLogModelForUsage(s.router, log, keysByID, keysByName)
		usageUserID := ""
		if key, ok := lookupLogAPIKey(log, keysByID, keysByName); ok {
			usageUserID = ownerUserIDForStats(key.OwnerUserID)
		}
		s.logs.ApplyUsageEventSync(monitor.UsageEvent{
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
		})
	}
}

const memoryLogCap = 50000

// 请求/响应体日志截断上限（字节）。
//   - 2xx 正常请求：只留精简体，够定位模型/参数即可，避免正常流量把库撑大。
//   - 非 2xx 错误请求：放大上限，尽量保留完整请求/响应体，方便事后排查
//     （如 thinking 签名 400、上游报错等需要看到完整上下文）。
//
// SQLite TEXT 列本身无硬长度限制，这里的上限用于防止单条日志异常超长
// （如超大 base64 图片/附件）拖垮内存缓存与 DB 写入。
const (
	logBodyCapOK    = 8 * 1024   // 2xx 请求体上限：8 KiB
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
	s.recordRequestLogEx(started, matchedKey, gatewayKeyMatched, routeID, providerID, model, action, protocolFlow, path, status, usage, 0, "", "", "", nil, requestBody, responseBody)
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
		timing = requestTimingFrom(r.Context())
	}
	s.recordRequestLogEx(started, matchedKey, gatewayKeyMatched, routeID, providerID, model, action, protocolFlow, path, status, usage, ttftMs, clientHost, clientIP, accessSource, timing, requestBody, responseBody)
}

func (s *Server) recordRequestLogEx(started time.Time, matchedKey domain.APIKey, gatewayKeyMatched bool, routeID, providerID, model, action, protocolFlow, path string, status int, usage TokenUsage, ttftMs int64, clientHost, clientIP, accessSource string, timing *requestTiming, requestBody, responseBody []byte) {
	if usage.InputTokens == 0 && usage.OutputTokens == 0 {
		usage = EstimateTokenUsage(requestBody, responseBody)
	}
	latency := time.Since(started).Milliseconds()
	if ttftMs <= 0 {
		// Non-stream (or unknown TTFT): treat total latency as a coarse TTFT.
		ttftMs = latency
	}
	prepMs, preUpstreamMs, upstreamTtfbMs, gatewayOverheadMs, convertOutMs, postMs, timingFlags := timing.snapshot(ttftMs, latency)
	// 非 2xx 错误：保留更完整的请求体，方便排查；2xx 正常请求只留精简体。
	requestBodyCap := logBodyCapOK
	if status < 200 || status >= 300 {
		requestBodyCap = logBodyCapError
	}
	entry := monitor.RequestLog{
		Time:                  started,
		RouteID:               routeID,
		ProviderID:            providerID,
		Model:                 model,
		Action:                action,
		ProtocolFlow:          protocolFlow,
		Path:                  path,
		Status:                status,
		InputTokens:           usage.InputTokens,
		OutputTokens:          usage.OutputTokens,
		CacheTokens:           usage.CacheTokens,
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
		RequestBody:           truncateForLog(requestBody, requestBodyCap),
	}
	if gatewayKeyMatched {
		entry.APIKeyID = matchedKey.ID
		entry.APIKeyName = matchedKey.Name
	} else if strings.HasPrefix(action, "test_") {
		entry.APIKeyName = "Route Test"
	}
	if status >= 400 || extractResponseErrorMessage(responseBody) != "" {
		entry.ResponseBody = truncateForLog(responseBody, logBodyCapError)
		entry.ErrorDescription = extractResponseErrorMessage(responseBody)
		if entry.ErrorDescription == "" && status >= 400 {
			entry.ErrorDescription = summarizeUpstreamHTTPError(status, responseBody)
		}
	}
	s.logs.Add(entry)
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
		LatencyMs:      latency,
		TTFTMs:         ttftMs,
	})
	if s.requestLogStore != nil {
		if err := s.requestLogStore.AppendRequestLogWithRetention(entry, s.RequestLogRetentionDays()); err != nil {
			s.logs.AddApp("warn", "failed to persist request log", err.Error())
		}
	}
}

func truncateForLog(data []byte, limit int) string {
	if len(data) == 0 {
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
	s.logs.PruneUsageStatsBefore(time.Now().AddDate(0, 0, -days))
	s.logs.AddApp("info", "request log retention updated", fmt.Sprintf("days=%d", days))
	writeJSON(w, http.StatusOK, map[string]any{"requestLogRetentionDays": days})
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
	if identity := s.requestIdentity(r); !identity.isAdmin() {
		// Normal users own the providers they create (including clones);
		// OAuth connect/disconnect on owned providers is allowed too.
		provider.OwnerUserID = identity.UserID
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
	var provider domain.Provider
	if err := json.NewDecoder(r.Body).Decode(&provider); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid provider json: "+err.Error())
		return
	}
	updated, err := s.router.UpdateProvider(r.PathValue("id"), provider)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "provider updated", updated.ID)
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
		s.logs.AddApp("info", "provider disabled", updated.ID)
	}
	writeJSON(w, http.StatusOK, redactProviderForClient(updated))
}

func (s *Server) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	providerID := r.PathValue("id")
	if !s.requireProviderOwnerForUser(w, r, providerID) {
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
	pending := claudeOAuthPending{Verifier: verifier, State: state, CreatedAt: time.Now()}

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
	s.logs.AddApp("info", "claude oauth localhost flow started", providerID)
	writeJSON(w, http.StatusOK, map[string]any{"authUrl": authURL, "state": state, "flowId": flowID, "mode": "localhost"})
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
		http.Error(w, "oauth flow expired or unknown state; start again from Protocol Gateway", http.StatusBadRequest)
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
	code, embeddedState := parseClaudeOAuthCode(payload.Code)
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
	if provider.ClaudeOAuth == nil || strings.TrimSpace(provider.ClaudeOAuth.RefreshToken) == "" {
		writeJSON(w, http.StatusOK, ClaudeOAuthUsageReport{Available: false, Error: "Claude OAuth 未连接"})
		return
	}

	cacheKey := "claude:" + providerID
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
		s.oauthUsageCache.set(cacheKey, report)
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
	if provider.CursorOAuth == nil || strings.TrimSpace(provider.CursorOAuth.RefreshToken) == "" {
		writeJSON(w, http.StatusOK, CursorOAuthUsageReport{Available: false, Error: "Cursor OAuth 未连接"})
		return
	}

	cacheKey := "cursor:" + providerID
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
		s.oauthUsageCache.set(cacheKey, report)
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
	if provider.ChatGPTOAuth == nil || strings.TrimSpace(provider.ChatGPTOAuth.RefreshToken) == "" {
		writeJSON(w, http.StatusOK, ChatGPTOAuthUsageReport{Available: false, Error: "ChatGPT OAuth 未连接"})
		return
	}

	cacheKey := "chatgpt:" + providerID
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
		s.oauthUsageCache.set(cacheKey, report)
	}
	writeJSON(w, http.StatusOK, report)
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
	apiKey := resolveProviderAuth(provider)
	if strings.TrimSpace(apiKey) == "" {
		writeJSON(w, http.StatusOK, ZhipuUsageReport{Available: false, Error: "provider has no API key configured (apiKeySource is empty)"})
		return
	}

	cacheKey := "zhipu:" + providerID
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
	// Cache success + unsupported (非编程套餐) so card polling stays quiet.
	if report.Available || report.Unsupported {
		s.oauthUsageCache.set(cacheKey, report)
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
	if team || (org != "" && project != "") {
		return fetchZhipuTeamUsage(ctx, apiKey, org, project)
	}
	return fetchZhipuUsage(ctx, provider.BaseURL, apiKey)
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
	if !s.requireProviderOwnerForUser(w, r, providerID) {
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
	if !s.requireProviderOwnerForUser(w, r, providerID) {
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
	if !s.requireProviderOwnerForUser(w, r, providerID) {
		return
	}
	var payload providerChatTestRequest
	_ = json.NewDecoder(r.Body).Decode(&payload)
	result, status := s.testProviderThinkingChat(r, providerID, payload, started)
	writeJSON(w, status, result)
}

func resolveProviderImageURL(provider domain.Provider) string {
	upstreamURL := strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
	if !strings.Contains(strings.ToLower(upstreamURL), "/images/generations") {
		upstreamURL += "/images/generations"
	}
	return upstreamURL
}

func resolveProviderChatURL(provider domain.Provider, model string) string {
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
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return providerChatHTTPResult{Error: err.Error(), TargetURL: upstreamURL, RequestBody: string(body), LatencyMs: time.Since(started).Milliseconds()}
	}
	request.Header.Set("Content-Type", "application/json")
	if authValue := resolveProviderAuth(provider); authValue != "" {
		header := provider.AuthHeader
		if header == "" {
			header = "Authorization"
		}
		if strings.EqualFold(header, "Authorization") && !strings.HasPrefix(strings.ToLower(authValue), "bearer ") {
			authValue = "Bearer " + authValue
		}
		request.Header.Set(header, authValue)
	} else if incomingAuth := r.Header.Get("Authorization"); incomingAuth != "" {
		request.Header.Set("Authorization", incomingAuth)
	}

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
		userPrompt = "1+1等于几"
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
	if err := s.router.PurgeProvider(providerID); err != nil {
		writeOpenAIError(w, http.StatusConflict, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
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
	var patch domain.Route
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid route json: "+err.Error())
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
	var payload struct {
		Model   string `json:"model"`
		Message string `json:"message"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)
	result, status := s.testRoute(r, routeID, payload.Model, payload.Message, started)
	writeJSON(w, status, result)
}

func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	identity := s.requestIdentity(r)
	writeJSON(w, http.StatusOK, keysVisibleTo(identity, s.router.State().APIKeys))
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
		// Admin may assign any owner; validate the target user exists.
		if owner := strings.TrimSpace(key.OwnerUserID); owner != "" && s.userStore != nil {
			if _, err := s.userStore.UserByID(owner); err != nil {
				writeOpenAIError(w, http.StatusBadRequest, "owner user not found")
				return
			}
		}
	} else {
		// Normal users always own their new keys and are provider-restricted.
		key.OwnerUserID = identity.UserID
		if err := s.validateKeyProvidersForUser(identity, key); err != nil {
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
	// Ownership check: normal users may only touch their own keys.
	if !identity.isAdmin() {
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
	} else if patch.FallbackProviderIDs == nil {
		patch.FallbackProviderIDs = []string{}
	}
	if !jsonHasKey(body, "fallbackModelOverrides") {
		patch.FallbackModelOverrides = nil
	} else if patch.FallbackModelOverrides == nil {
		patch.FallbackModelOverrides = map[string]string{}
	}
	if !identity.isAdmin() {
		// Provider whitelist check for role=user (route + fallbacks).
		if err := s.validateKeyProvidersForUser(identity, patch); err != nil {
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
	if !identity.isAdmin() {
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
	state.WebExposed = s.router.WebExposed()
	return s.stateSaver.Save(state)
}

// persistProviderOAuth incrementally writes just the refreshed OAuth token
// columns for one provider when the store supports it, avoiding the heavy
// full-state Save (which DELETEs + re-INSERTs all providers/models/routes on
// the single shared SQLite connection and blocks request-hot-path writes).
// Falls back to saveState when the incremental saver is unavailable.
func (s *Server) persistProviderOAuth(providerID string, claude *domain.ClaudeOAuthCredential, cursor *domain.CursorOAuthCredential, chatgpt *domain.ChatGPTOAuthCredential) error {
	if s.providerOAuthSaver != nil {
		var accessToken, refreshToken, expiresAt, scope, accountLabel string
		switch {
		case claude != nil:
			accessToken = claude.AccessToken
			refreshToken = claude.RefreshToken
			expiresAt = claude.ExpiresAt
			scope = claude.Scope
			accountLabel = claude.AccountLabel
		case cursor != nil:
			accessToken = cursor.AccessToken
			refreshToken = cursor.RefreshToken
			expiresAt = cursor.ExpiresAt
			accountLabel = cursor.AccountLabel
		case chatgpt != nil:
			accessToken = chatgpt.AccessToken
			refreshToken = chatgpt.RefreshToken
			expiresAt = chatgpt.ExpiresAt
			scope = chatgpt.ChatGPTAccountID
			accountLabel = chatgpt.AccountLabel
		default:
			return nil
		}
		return s.providerOAuthSaver.UpdateProviderOAuth(providerID, accessToken, refreshToken, expiresAt, scope, accountLabel)
	}
	return s.saveState()
}

func (s *Server) handleOpenAIModels(w http.ResponseWriter, r *http.Request) {
	models := s.modelsForRequest(r)
	data := make([]map[string]any, 0, len(models))
	for _, model := range models {
		contextLen := resolveModelContextLength(model.ID, model.ContextLength)
		data = append(data, map[string]any{
			"id":             model.ID,
			"object":         "model",
			"created":        0,
			"owned_by":       model.ProviderID,
			"context_length": contextLen,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (s *Server) handleClaudeModels(w http.ResponseWriter, r *http.Request) {
	models := s.modelsForRequest(r)
	data := make([]map[string]any, 0, len(models)+len(claudeModelAliases))
	seen := map[string]bool{}
	for _, entry := range claudeModelAliasEntries() {
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
	for _, model := range models {
		if seen[model.ID] {
			continue
		}
		seen[model.ID] = true
		contextLen := resolveModelContextLength(model.ID, model.ContextLength)
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
	r, _ = attachRequestTiming(r, started)
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

	route, err := s.router.ActiveOpenAIChatRoute()
	var matchedKey domain.APIKey
	var gatewayKeyMatched bool
	if testRouteID := strings.TrimSpace(r.Header.Get(headerGatewayRouteID)); testRouteID != "" && r.Header.Get(headerGatewayInternalTest) == "1" {
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
		}
	}
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	internalTest := strings.TrimSpace(r.Header.Get(headerGatewayInternalTest)) == "1"
	if gatewayKeyMatched && !internalTest && route.OutputProtocol != domain.ProtocolOpenAIChat {
		mismatchBody := routeProtocolMismatchBody(route, domain.ProtocolOpenAIChat)
		writeRouteProtocolMismatch(w, route, domain.ProtocolOpenAIChat)
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, route.ProviderID, "", "rejected", route.OutputProtocol.DisplayName()+" endpoint mismatch", r.URL.Path, http.StatusBadRequest, TokenUsage{}, body, mismatchBody)
		return
	}
	decision, err := s.router.Decide(route.ID)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	if gatewayKeyMatched {
		decision = s.decisionForAPIKey(route, matchedKey, decision)
	}

	requestModel, _ := req["model"].(string)
	model, _ := resolveConsumerModel(s.router, route, matchedKey, gatewayKeyMatched, requestModel)
	r = attachAPIKeyMaxOutputTokens(r, matchedKey, gatewayKeyMatched)
	thinkingOverride := ""
	if gatewayKeyMatched {
		thinkingOverride = matchedKey.ThinkingDepthOverride
	}

	stream, _ := req["stream"].(bool)
	if s.rejectIfStreamDisabledForKey(w, gatewayKeyMatched, matchedKey, stream, domain.ProtocolOpenAIChat) {
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, route.ProviderID, model, "rejected", "stream disabled", r.URL.Path, http.StatusBadRequest, TokenUsage{}, body, []byte(`stream disabled`))
		return
	}
	status := http.StatusOK
	req["model"] = model
	requestDepth, requestHasDepth := req["reasoning_effort"].(string)
	if depth := s.router.ResolveThinkingDepth(route, thinkingOverride, requestHasDepth, requestDepth); depth != "" {
		req["reasoning_effort"] = depth
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
	if err != nil {
		if isClientCanceled(r, err) {
			s.logs.AddApp("info", "chat request canceled by client", err.Error())
			s.recordRequestLogFromRequestTTFT(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, effectiveModel, decision.Action, decision.ConversionLabel, r.URL.Path, statusClientClosedRequest, usage, ttftMs, body, []byte(err.Error()))
			return
		}
		s.logs.AddApp("error", "chat request failed", err.Error())
		s.recordRequestLogFromRequestTTFT(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, effectiveModel, decision.Action, decision.ConversionLabel, r.URL.Path, http.StatusBadGateway, usage, ttftMs, body, []byte(err.Error()))
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.recordRequestLogFromRequestTTFT(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, effectiveModel, decision.Action, decision.ConversionLabel, r.URL.Path, status, usage, ttftMs, body, responseLog)
	s.logs.AddApp("debug", "handled chat request", fmt.Sprintf("route=%s model=%s status=%d", route.ID, effectiveModel, status))
	slog.Info("handled chat request", "route", route.ID, "model", effectiveModel, "action", decision.Action, "stream", stream)
}

func (s *Server) handleOpenAIResponses(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	r, _ = attachRequestTiming(r, started)
	body, err := io.ReadAll(r.Body)
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

	var route domain.Route
	var matchedKey domain.APIKey
	var gatewayKeyMatched bool
	if testRouteID := strings.TrimSpace(r.Header.Get(headerGatewayRouteID)); testRouteID != "" && r.Header.Get(headerGatewayInternalTest) == "1" {
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
		}
	}
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"message": err.Error(), "type": "authentication_error"}})
		return
	}
	internalTest := strings.TrimSpace(r.Header.Get(headerGatewayInternalTest)) == "1"
	if gatewayKeyMatched && !internalTest && route.OutputProtocol != domain.ProtocolOpenAIResponses {
		mismatchBody := routeProtocolMismatchBody(route, domain.ProtocolOpenAIResponses)
		writeRouteProtocolMismatch(w, route, domain.ProtocolOpenAIResponses)
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, route.ProviderID, "", "rejected", route.OutputProtocol.DisplayName()+" endpoint mismatch", r.URL.Path, http.StatusBadRequest, TokenUsage{}, body, mismatchBody)
		return
	}
	decision, err := s.router.Decide(route.ID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"message": err.Error(), "type": "gateway_error"}})
		return
	}
	if gatewayKeyMatched {
		decision = s.decisionForAPIKey(route, matchedKey, decision)
	}

	requestModel, _ := req["model"].(string)
	model, _ := resolveConsumerModel(s.router, route, matchedKey, gatewayKeyMatched, requestModel)
	r = attachAPIKeyMaxOutputTokens(r, matchedKey, gatewayKeyMatched)
	thinkingOverride := ""
	if gatewayKeyMatched {
		thinkingOverride = matchedKey.ThinkingDepthOverride
	}

	stream, _ := req["stream"].(bool)
	if s.rejectIfStreamDisabledForKey(w, gatewayKeyMatched, matchedKey, stream, domain.ProtocolOpenAIResponses) {
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, route.ProviderID, model, "rejected", "stream disabled", r.URL.Path, http.StatusBadRequest, TokenUsage{}, body, []byte(`stream disabled`))
		return
	}
	status := http.StatusOK
	req["model"] = model
	if depth := s.router.ResolveThinkingDepth(route, thinkingOverride, false, ""); depth != "" {
		req["reasoning"] = map[string]any{"effort": depth}
	}
	var usage TokenUsage
	var responseLog []byte
	var ttftMs int64
	timing := requestTimingFrom(r.Context())
	if timing != nil {
		timing.markPrepReady()
	}
	status, usage, responseLog, decision, effectiveModel, err := s.executeProtocolFlowWithFailover(wrapTTFTWriterWithTiming(w, started, &ttftMs, timing), r, route, decision, model, req, domain.ProtocolOpenAIResponses, gatewayKeyMatched, matchedKey, gatewayKeyMatched)
	if err != nil {
		if isClientCanceled(r, err) {
			s.logs.AddApp("info", "responses request canceled by client", err.Error())
			s.recordRequestLogFromRequestTTFT(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, effectiveModel, decision.Action, decision.ConversionLabel, r.URL.Path, statusClientClosedRequest, usage, ttftMs, body, []byte(err.Error()))
			return
		}
		s.logs.AddApp("error", "responses request failed", err.Error())
		s.recordRequestLogFromRequestTTFT(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, effectiveModel, decision.Action, decision.ConversionLabel, r.URL.Path, http.StatusBadGateway, usage, ttftMs, body, []byte(err.Error()))
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"message": err.Error(), "type": "gateway_error"}})
		return
	}
	s.recordRequestLogFromRequestTTFT(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, effectiveModel, decision.Action, decision.ConversionLabel, r.URL.Path, status, usage, ttftMs, body, responseLog)
	s.logs.AddApp("debug", "handled responses request", fmt.Sprintf("route=%s model=%s status=%d", route.ID, effectiveModel, status))
	slog.Info("handled responses request", "route", route.ID, "model", effectiveModel, "action", decision.Action, "stream", stream)
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
	if testRouteID := strings.TrimSpace(r.Header.Get(headerGatewayRouteID)); testRouteID != "" && r.Header.Get(headerGatewayInternalTest) == "1" {
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

	decision, err := s.router.Decide(route.ID)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}

	requestModel, _ := req["model"].(string)
	model, _ := resolveConsumerModel(s.router, route, matchedKey, gatewayKeyMatched, requestModel)
	if strings.TrimSpace(model) == "" {
		model = "gpt-image-2"
	}
	req["model"] = model
	body, err = json.Marshal(req)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "failed to encode request body")
		return
	}

	provider, err := s.router.ProviderByID(decision.ProviderID)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}

	status, usage, responseLog, err := s.proxyOpenAIImages(w, r, provider, model, body, gatewayKeyMatched)
	if err != nil {
		s.logs.AddApp("error", "image generation request failed", err.Error())
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, model, "pass_through", "images/generations", r.URL.Path, http.StatusBadGateway, usage, body, []byte(err.Error()))
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, model, "pass_through", "images/generations", r.URL.Path, status, usage, body, responseLog)
	s.logs.AddApp("debug", "handled image generation request", fmt.Sprintf("route=%s model=%s status=%d", route.ID, model, status))
	slog.Info("handled image generation request", "route", route.ID, "model", model, "status", status)
}

// handleClaudeMessages serves POST /anthropic/v1/messages, the native
// Anthropic Messages API pass-through endpoint, mirroring handleOpenAIChat's
// route-resolution pattern (test header or consumer API key -> router.Decide).
func (s *Server) handleClaudeMessages(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	r, _ = attachRequestTiming(r, started)
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
	if testRouteID := strings.TrimSpace(r.Header.Get(headerGatewayRouteID)); testRouteID != "" && r.Header.Get(headerGatewayInternalTest) == "1" {
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
	internalTest := strings.TrimSpace(r.Header.Get(headerGatewayInternalTest)) == "1"
	if gatewayKeyMatched && !internalTest && route.OutputProtocol != domain.ProtocolClaude {
		mismatchBody := routeProtocolMismatchBody(route, domain.ProtocolClaude)
		writeRouteProtocolMismatch(w, route, domain.ProtocolClaude)
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, route.ProviderID, "", "rejected", route.OutputProtocol.DisplayName()+" endpoint mismatch", r.URL.Path, http.StatusBadRequest, TokenUsage{}, body, mismatchBody)
		return
	}
	decision, err := s.router.Decide(route.ID)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	if gatewayKeyMatched {
		decision = s.decisionForAPIKey(route, matchedKey, decision)
	}

	requestModel, _ := req["model"].(string)
	model, _ := resolveConsumerModel(s.router, route, matchedKey, gatewayKeyMatched, requestModel)
	r = attachAPIKeyMaxOutputTokens(r, matchedKey, gatewayKeyMatched)

	stream, _ := req["stream"].(bool)
	if s.rejectIfStreamDisabledForKey(w, gatewayKeyMatched, matchedKey, stream, domain.ProtocolClaude) {
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, route.ProviderID, model, "rejected", "stream disabled", r.URL.Path, http.StatusBadRequest, TokenUsage{}, body, []byte(`stream disabled`))
		return
	}
	status := http.StatusOK
	req["model"] = model
	var usage TokenUsage
	var responseLog []byte
	var ttftMs int64
	timing := requestTimingFrom(r.Context())
	if timing != nil {
		timing.markPrepReady()
	}
	status, usage, responseLog, decision, effectiveModel, err := s.executeProtocolFlowWithFailover(wrapTTFTWriterWithTiming(w, started, &ttftMs, timing), r, route, decision, model, req, domain.ProtocolClaude, gatewayKeyMatched, matchedKey, gatewayKeyMatched)
	if err != nil {
		if isClientCanceled(r, err) {
			s.logs.AddApp("info", "claude messages canceled by client", err.Error())
			s.recordRequestLogFromRequestTTFT(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, effectiveModel, decision.Action, decision.ConversionLabel, r.URL.Path, statusClientClosedRequest, usage, ttftMs, body, []byte(err.Error()))
			return
		}
		s.logs.AddApp("error", "claude messages request failed", err.Error())
		s.recordRequestLogFromRequestTTFT(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, effectiveModel, decision.Action, decision.ConversionLabel, r.URL.Path, http.StatusBadGateway, usage, ttftMs, body, []byte(err.Error()))
		writeClaudeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.recordRequestLogFromRequestTTFT(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, effectiveModel, decision.Action, decision.ConversionLabel, r.URL.Path, status, usage, ttftMs, body, responseLog)
	s.logs.AddApp("debug", "handled claude messages request", fmt.Sprintf("route=%s model=%s status=%d stream=%v", route.ID, effectiveModel, status, stream))
}

// handleClaudeCountTokens proxies Anthropic's token counting endpoint, which
// Claude Code uses during startup to validate the selected model.
func (s *Server) handleClaudeCountTokens(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	body, err := io.ReadAll(r.Body)
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
	internalTest := strings.TrimSpace(r.Header.Get(headerGatewayInternalTest)) == "1"
	if gatewayKeyMatched && !internalTest && route.OutputProtocol != domain.ProtocolClaude {
		writeRouteProtocolMismatch(w, route, domain.ProtocolClaude)
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, route.ProviderID, "", "rejected", route.OutputProtocol.DisplayName()+" endpoint mismatch", r.URL.Path, http.StatusBadRequest, TokenUsage{}, body, routeProtocolMismatchBody(route, domain.ProtocolClaude))
		return
	}
	decision, err := s.router.Decide(route.ID)
	if err != nil {
		writeClaudeError(w, http.StatusBadGateway, err.Error())
		return
	}

	requestModel, _ := req["model"].(string)
	model, _ := resolveConsumerModel(s.router, route, matchedKey, gatewayKeyMatched, requestModel)
	if model == "" || model == "request-model-not-set" {
		writeClaudeError(w, http.StatusBadRequest, "model is required")
		return
	}
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
	status, responseBody, err := s.proxyClaudeCountTokens(r, provider, upstreamBody)
	if err != nil {
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, model, "pass_through", decision.ConversionLabel, r.URL.Path, http.StatusBadGateway, TokenUsage{}, body, []byte(err.Error()))
		writeClaudeError(w, http.StatusBadGateway, err.Error())
		return
	}
	for key, values := range map[string]string{"Content-Type": "application/json"} {
		w.Header().Set(key, values)
	}
	w.WriteHeader(status)
	_, _ = w.Write(responseBody)
	s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, model, "pass_through", decision.ConversionLabel, r.URL.Path, status, TokenUsage{}, body, responseBody)
}

func (s *Server) resolveClaudeConsumerRoute(r *http.Request) (domain.Route, domain.APIKey, bool, error) {
	if testRouteID := strings.TrimSpace(r.Header.Get(headerGatewayRouteID)); testRouteID != "" && r.Header.Get(headerGatewayInternalTest) == "1" {
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
	// Cursor OAuth (and other empty-baseURL OpenAI-chat providers) have no Anthropic
	// count_tokens upstream. Return a lightweight stub so Claude Code startup checks pass.
	if provider.AuthType == domain.AuthTypeCursorOAuth || (provider.Protocol == domain.ProtocolOpenAIChat && strings.TrimSpace(provider.BaseURL) == "") {
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
	var payload struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
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
		contextLength := resolveModelContextLength(id, item.ContextLength)
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
	if errorValue, ok := payload["error"]; ok {
		if item, ok := errorValue.(map[string]any); ok {
			return formatAPIErrorMessage(item)
		}
		return strings.TrimSpace(fmt.Sprint(errorValue))
	}
	if detail := strings.TrimSpace(stringValue(payload["detail"])); detail != "" {
		return detail
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

func buildRouteTestReproduceCurl(gatewayURL, routeID, requestBody string) string {
	escapedBody := strings.ReplaceAll(requestBody, "'", `'\''`)
	return fmt.Sprintf(
		"curl -sv '%s' \\\n  -H 'Content-Type: application/json' \\\n  -H '%s: %s' \\\n  -H '%s: 1' \\\n  -d '%s'",
		gatewayURL,
		headerGatewayRouteID,
		routeID,
		headerGatewayInternalTest,
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
		"mode":             route.Mode,
		"model":            model,
		"status":           status,
		"requestBody":      requestBody,
		"responseBody":     string(responseBody),
		"responseHeaders":  responseHeaders,
		"errorMessage":     errorMessage,
		"reproduceCurl":    buildRouteTestReproduceCurl(gatewayURL, route.ID, requestBody),
	}
	return diagnostics
}

func (s *Server) testRoute(r *http.Request, routeID string, requestModel string, message string, started time.Time) (map[string]any, int) {
	route, err := s.router.RouteByID(routeID)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error(), "latencyMs": time.Since(started).Milliseconds()}, http.StatusNotFound
	}
	decision, err := s.router.Decide(route.ID)
	if err != nil {
		return map[string]any{"success": false, "routeId": route.ID, "error": err.Error(), "latencyMs": time.Since(started).Milliseconds()}, http.StatusBadGateway
	}
	model := s.router.ResolveModel(route, "", requestModel)
	if provider, err := s.router.ProviderForRoute(route); err == nil {
		model = applyProviderModelFallback(provider, model)
	}
	if strings.TrimSpace(model) == "" {
		model = "request-model-not-set"
	}
	if strings.TrimSpace(message) == "" {
		message = "ping from Protocol Gateway route test"
	}

	provider, err := s.router.ProviderForRoute(route)
	if err != nil {
		return map[string]any{"success": false, "routeId": route.ID, "error": err.Error(), "latencyMs": time.Since(started).Milliseconds()}, http.StatusBadGateway
	}
	upstreamURL := ""
	if provider.Protocol == domain.ProtocolOpenAIChat {
		upstreamURL = resolveProviderChatURL(provider, model)
	} else if strings.TrimSpace(provider.BaseURL) != "" {
		upstreamURL = strings.TrimSpace(provider.BaseURL)
	}

	gatewayURL, err := gatewayOutputURL(s.router.State(), route.OutputProtocol)
	if err != nil {
		return map[string]any{"success": false, "routeId": route.ID, "error": err.Error(), "latencyMs": time.Since(started).Milliseconds()}, http.StatusBadGateway
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
	request.Header.Set(headerGatewayInternalTest, "1")

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

func gatewayOutputURL(state domain.GatewayState, protocol domain.Protocol) (string, error) {
	for _, endpoint := range state.Endpoints {
		if endpoint.Protocol != protocol {
			continue
		}
		base := strings.TrimRight(fmt.Sprintf("http://%s:%d%s", endpoint.ListenHost, endpoint.ListenPort, endpoint.BasePath), "/")
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
	stream, _ := openAIReq["stream"].(bool)
	upstreamBody, err := json.Marshal(openAIReq)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}

	model = applyProviderModelMapping(provider, model)
	upstreamBody, bodyErr := applyRequestAdapterBody(provider, model, upstreamBody)
	if bodyErr != nil {
		return 0, TokenUsage{}, nil, bodyErr
	}

	var upstreamURL string
	if provider.AuthType == domain.AuthTypeCursorOAuth {
		baseURL, refreshed, bridgeErr := s.resolveCursorBridgeBaseURL(r.Context(), provider)
		if bridgeErr != nil {
			return 0, TokenUsage{}, nil, bridgeErr
		}
		provider = refreshed
		upstreamURL = strings.TrimRight(baseURL, "/") + "/chat/completions"
	} else {
		upstreamURL = resolveProviderChatURLWithAdapter(provider, model)
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(upstreamBody))
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if stream {
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}
	if provider.AuthType != domain.AuthTypeCursorOAuth {
		if authValue := resolveProviderAuth(provider); authValue != "" {
			header := provider.AuthHeader
			if header == "" {
				header = "Authorization"
			}
			if strings.EqualFold(header, "Authorization") && !strings.HasPrefix(strings.ToLower(authValue), "bearer ") {
				authValue = "Bearer " + authValue
			}
			request.Header.Set(header, authValue)
		} else if !skipIncomingAuth {
			if incomingAuth := r.Header.Get("Authorization"); incomingAuth != "" {
				request.Header.Set("Authorization", incomingAuth)
			}
		}
		applyRequestAdapterHeaders(request, provider, model)
	}

	client := &http.Client{Timeout: 0}
	response, err := client.Do(request)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	defer response.Body.Close()

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
		w.WriteHeader(response.StatusCode)
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			responseBody, readErr := io.ReadAll(response.Body)
			if readErr != nil {
				return response.StatusCode, TokenUsage{}, nil, readErr
			}
			var payload map[string]any
			_ = json.Unmarshal(responseBody, &payload)
			claudeBody, _, convErr := openAIErrorValueToClaude(errorValueOrBody(payload, response.StatusCode, responseBody), model)
			if convErr != nil || len(claudeBody) == 0 {
				_, writeErr := w.Write(responseBody)
				return response.StatusCode, TokenUsage{}, responseBody, writeErr
			}
			_, writeErr := w.Write(claudeBody)
			return response.StatusCode, TokenUsage{}, claudeBody, writeErr
		}
		usage, streamErr := streamOpenAIChatToClaudeEvents(w, response.Body, model)
		return response.StatusCode, usage, nil, streamErr
	}

	responseBody, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return 0, TokenUsage{}, nil, readErr
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

	claudeBody, usage, err := openAIChatResponseToClaude(responseBody, model)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(claudeBody)))
	w.WriteHeader(http.StatusOK)
	if _, writeErr := w.Write(claudeBody); writeErr != nil {
		return http.StatusOK, usage, claudeBody, writeErr
	}
	return http.StatusOK, usage, nil, nil
}

func (s *Server) proxyOpenAIChat(w http.ResponseWriter, r *http.Request, provider domain.Provider, model string, body []byte, skipIncomingAuth bool) (int, TokenUsage, []byte, error) {
	stream := requestBodyWantsStream(body)
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
		return writePassThroughResponse(w, response, stream, ParseOpenAIUsage)
	}

	model = applyProviderModelMapping(provider, model)
	body, bodyErr := applyRequestAdapterBody(provider, model, body)
	if bodyErr != nil {
		return 0, TokenUsage{}, nil, bodyErr
	}
	upstreamURL := resolveProviderChatURLWithAdapter(provider, model)
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", r.Header.Get("Accept"))
	if authValue := resolveProviderAuth(provider); authValue != "" {
		header := provider.AuthHeader
		if header == "" {
			header = "Authorization"
		}
		if strings.EqualFold(header, "Authorization") && !strings.HasPrefix(strings.ToLower(authValue), "bearer ") {
			authValue = "Bearer " + authValue
		}
		request.Header.Set(header, authValue)
	} else if !skipIncomingAuth {
		if incomingAuth := r.Header.Get("Authorization"); incomingAuth != "" {
			request.Header.Set("Authorization", incomingAuth)
		}
	}
	applyRequestAdapterHeaders(request, provider, model)

	response, err := doHTTPWithTiming(r.Context(), &http.Client{Timeout: 0}, request)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	defer response.Body.Close()
	return writePassThroughResponse(w, response, stream, ParseOpenAIUsage)
}

func (s *Server) proxyOpenAIImages(w http.ResponseWriter, r *http.Request, provider domain.Provider, model string, body []byte, skipIncomingAuth bool) (int, TokenUsage, []byte, error) {
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
		if apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); apiKey != "" {
			return s.proxyOpenAIImagesUpstream(w, r.Context(), "https://api.openai.com/v1/images/generations", apiKey, body)
		}
		if err != nil {
			return 0, TokenUsage{}, nil, fmt.Errorf("%w (Cursor OAuth 本地 bridge 暂无法生成图片；可设置 OPENAI_API_KEY 作为 Images API 回退)", err)
		}
		return status, TokenUsage{}, responseBody, fmt.Errorf("cursor bridge image generation failed with HTTP %d", status)
	}

	model = applyProviderModelMapping(provider, model)
	body, bodyErr := applyRequestAdapterBody(provider, model, body)
	if bodyErr != nil {
		return 0, TokenUsage{}, nil, bodyErr
	}
	upstreamURL := resolveProviderImageURL(provider)
	authValue := resolveProviderAuth(provider)
	if authValue == "" && skipIncomingAuth {
		authValue = strings.TrimPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
	}
	return s.proxyOpenAIImagesUpstream(w, r.Context(), upstreamURL, authValue, body)
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

func (s *Server) proxyOpenAIImagesUpstream(w http.ResponseWriter, ctx context.Context, upstreamURL string, authValue string, body []byte) (int, TokenUsage, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
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
	return writePassThroughResponse(w, response, false, func([]byte) TokenUsage { return TokenUsage{} })
}

// proxyClaudeMessages forwards a Claude Messages API request to the upstream
// provider (Anthropic directly for claude_oauth providers, or provider.BaseURL
// for a regular Claude API-key provider), streaming the response back
// unchanged. For claude_oauth providers this refreshes the access token if
// needed, injects the billing marker, and forces the anthropic-beta header.
func (s *Server) proxyClaudeMessages(w http.ResponseWriter, r *http.Request, provider domain.Provider, body []byte) (int, TokenUsage, []byte, error) {
	// Claude Code 会自带 max_tokens，但网关可能已做模型覆盖；预算必须按实际上游模型。
	body = rewriteClaudeUpstreamMaxTokens(body, provider, maxOutputTokensOverrideFrom(r.Context()))
	response, err := s.sendClaudeMessagesUpstream(r, provider, body)
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
		}
	}

	defer response.Body.Close()
	return writePassThroughResponse(w, response, requestBodyWantsStream(body), ParseClaudeUsage)
}

// sendClaudeMessagesUpstream builds and sends one Claude Messages request to the
// resolved upstream (Anthropic for claude_oauth providers, provider.BaseURL for
// regular Claude API-key providers), returning the raw response.
func (s *Server) sendClaudeMessagesUpstream(r *http.Request, provider domain.Provider, body []byte) (*http.Response, error) {
	isOAuth := provider.AuthType == domain.AuthTypeClaudeOAuth

	var request *http.Request
	var err error
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
	} else {
		upstreamURL := strings.TrimSpace(provider.BaseURL)
		if upstreamURL == "" {
			upstreamURL = claudeMessagesURL
		}
		request, err = http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", r.Header.Get("Accept"))
		request.Header.Set("anthropic-version", "2023-06-01")
		applyProviderAuth(request, provider, r.Header.Get("Authorization"))
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
	rectifiedBody, result := rectifyClaudeThinkingBody(body)
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
	isOAuth := provider.AuthType == domain.AuthTypeClaudeOAuth
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
	stream, _ := claudeReq["stream"].(bool)
	upstreamBody, err := json.Marshal(claudeReq)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	accept := "application/json"
	if stream {
		accept = "text/event-stream"
	}
	response, err := s.doClaudeProviderRequest(r.Context(), r, provider, upstreamBody, accept)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	defer response.Body.Close()

	if stream {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(response.StatusCode)
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			responseBody, readErr := io.ReadAll(response.Body)
			if readErr != nil {
				return response.StatusCode, TokenUsage{}, nil, readErr
			}
			openAIBody, _, convErr := claudeResponseToOpenAIChat(responseBody, model, clientToolNames)
			if convErr != nil || len(openAIBody) == 0 {
				_, writeErr := w.Write(responseBody)
				return response.StatusCode, TokenUsage{}, responseBody, writeErr
			}
			_, writeErr := w.Write(openAIBody)
			return response.StatusCode, TokenUsage{}, openAIBody, writeErr
		}
		usage, streamErr := streamClaudeToOpenAIChatEvents(w, response.Body, model, clientToolNames)
		return response.StatusCode, usage, nil, streamErr
	}

	responseBody, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return 0, TokenUsage{}, nil, readErr
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		openAIBody, _, convErr := claudeResponseToOpenAIChat(responseBody, model, clientToolNames)
		if convErr != nil || len(openAIBody) == 0 {
			w.WriteHeader(response.StatusCode)
			_, writeErr := w.Write(responseBody)
			return response.StatusCode, TokenUsage{}, responseBody, writeErr
		}
		w.WriteHeader(response.StatusCode)
		_, writeErr := w.Write(openAIBody)
		return response.StatusCode, TokenUsage{}, openAIBody, writeErr
	}

	openAIBody, usage, err := claudeResponseToOpenAIChat(responseBody, model, clientToolNames)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(openAIBody)))
	w.WriteHeader(http.StatusOK)
	if _, writeErr := w.Write(openAIBody); writeErr != nil {
		return http.StatusOK, usage, openAIBody, writeErr
	}
	return http.StatusOK, usage, openAIBody, nil
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
}

func resolveProviderAuth(provider domain.Provider) string {
	source := strings.TrimSpace(provider.APIKeySource)
	if source == "" {
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
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-api-key")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
