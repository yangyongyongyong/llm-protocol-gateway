package gateway

// The monitoring data source: everything one monitoring tick needs, read from
// the live router, the request log and the existing per-vendor quota readers.
//
// It is deliberately read-only towards accounts. Quota reads spend a request on
// the vendor's own usage endpoint; nothing here refreshes a credential on its
// own beyond what those readers already do when a vendor answers 401.

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
	"github.com/luca/llm-protocol-gateway/internal/monitoring"
)

// monitoringStore is the persistence slice the monitoring runtime needs: the
// collector/engine/dispatcher surfaces plus config and baseline recomputation.
// *store.Store implements all of them; without it the console never starts.
type monitoringStore interface {
	monitoring.ConfigStore
	monitoring.StateStore
	monitoring.ChannelStore
	monitoring.BaselineComputer
	monitoring.SampleSink
	TTFTBaselines(loc *time.Location) (map[string]int64, error)
	RecentRenewalOutcomes(providerID string, limit int) (monitoring.RenewalOutcome, error)
	// Reads the /__monitor/* API answers from.
	MonitorRequestSummary(from, to time.Time) (monitoring.RequestSummary, error)
	QueryMonitorEvents(monitoring.EventQuery) (monitoring.EventPage, error)
	QueryMonitorAlerts(monitoring.AlertQuery) (monitoring.AlertPage, error)
	MonitorEventByID(int64) (monitoring.Event, bool, error)
	MonitorEventIDsByFingerprint(string, int) ([]int64, error)
	AppendMonitorTimeline(string, monitoring.TimelineEntry) error
	LatestProviderMetricSamples() (map[string]monitoring.MetricSample, error)
	// Probe history: the rule needs consecutive failures, the scheduler needs
	// the daily budget, and neither may reset because the process restarted.
	MonitorProbeOutcome(providerID string, day string) (monitoring.ProbeOutcome, int, error)
	InsertMonitorProbeRuns([]monitoring.ProbeRunRecord) error
	LatestMonitorProbeRuns(providerID string, limit int) ([]monitoring.ProbeRunRecord, error)
}

// windowStatsStore is implemented by the store's request-log aggregation. It is
// an optional assertion so a Server built without that store keeps working.
type windowStatsStore interface {
	WindowStatsByProvider(from, to time.Time) (map[string]map[int]monitoring.WindowStats, error)
}

type multiIPDetector interface {
	DetectMultiIPKeys(since time.Time, threshold int) ([]monitor.MultiIPHit, error)
	DetectConcurrentIPKeys(since time.Time, threshold int) ([]monitor.ConcurrentIPHit, error)
}

type monitoringSource struct {
	server *Server
	// modelFailures counts consecutive failed model-list syncs per provider.
	// Written by runModelMonitor, read on every tick.
	modelMu       sync.Mutex
	modelFailures map[string]int
	now           func() time.Time
	// quotaFetch is the vendor usage read. It is a field so the console's
	// quota-refresh endpoint can be exercised without reaching a live vendor.
	quotaFetch func(ctx context.Context, p domain.Provider, now time.Time) (*monitoring.QuotaSnapshot, error)
}

func newMonitoringSource(s *Server) *monitoringSource {
	m := &monitoringSource{server: s, modelFailures: map[string]int{}, now: time.Now}
	m.quotaFetch = m.quotaForProvider
	return m
}

func (m *monitoringSource) Providers(ctx context.Context, now time.Time) ([]monitoring.ProviderObservation, error) {
	state := m.server.router.State()
	out := make([]monitoring.ProviderObservation, 0, len(state.Providers))
	probeDay := ""
	if m.server.monitoringStore != nil {
		probeDay = monitorProbeDay(m.server, now)
	}
	for _, p := range state.Providers {
		if p.Deleted {
			continue
		}
		obs := monitoring.ProviderObservation{
			ID:           p.ID,
			Name:         p.Name,
			Vendor:       providerVendorKey(p),
			AuthType:     p.AuthType,
			HealthStatus: p.HealthStatus,
			Capabilities: monitoring.Capabilities{
				OAuth:   providerHasExpiry(p),
				Quota:   providerHasQuotaHandler(p),
				Models:  providerHasModelSync(p),
				Checkin: providerHasCheckin(p),
			},
		}
		if expiry := providerCredentialExpiry(p); !expiry.IsZero() {
			obs.ExpiresAt = expiry
		}
		if m.server.monitoringStore != nil {
			if renewal, err := m.server.monitoringStore.RecentRenewalOutcomes(p.ID, 10); err == nil {
				obs.Renewal = renewal
			}
			if probe, _, err := m.server.monitoringStore.MonitorProbeOutcome(p.ID, probeDay); err == nil {
				obs.Probe = probe
			}
		}
		obs.ModelFailures = m.modelFailureCount(p.ID)
		out = append(out, obs)
	}
	return out, nil
}

// Keys reports each API key's budget with the usage already consumed inside the
// current window. Windows reset on the gateway's local calendar, the same
// boundary the per-key quota gate enforces.
func (m *monitoringSource) Keys(ctx context.Context, now time.Time) ([]monitoring.KeyObservation, error) {
	state := m.server.router.State()
	out := make([]monitoring.KeyObservation, 0, len(state.APIKeys))
	for _, key := range state.APIKeys {
		obs := monitoring.KeyObservation{
			ID: key.ID, Name: key.Name, QuotaWindow: key.QuotaWindow,
			TokenLimit: key.QuotaTokens, CostLimitMicros: key.QuotaCostMicros,
		}
		if (key.QuotaTokens > 0 || key.QuotaCostMicros > 0) && m.server.logs != nil {
			if window, err := normalizeQuotaWindow(key.QuotaWindow); err == nil && window != quotaWindowUnlimited {
				from, to := window.bounds(now)
				used := m.server.logs.KeyWindowUsage(key.ID, from, to)
				obs.TokenUsed = used.Tokens
				obs.CostUsedMicros = used.CostMicros
			}
		}
		out = append(out, obs)
	}
	return out, nil
}

func (m *monitoringSource) Windows(ctx context.Context, now time.Time) (map[string]map[int]monitoring.WindowStats, error) {
	store, ok := m.server.stateSaver.(windowStatsStore)
	if !ok {
		return nil, errors.New("request log store does not aggregate windows")
	}
	return store.WindowStatsByProvider(now.Add(-15*time.Minute), now)
}

func (m *monitoringSource) Baselines(ctx context.Context, now time.Time) (map[string]int64, error) {
	if m.server.monitoringStore == nil {
		return nil, errors.New("monitoring store unavailable")
	}
	return m.server.monitoringStore.TTFTBaselines(m.dispatchLocation())
}

func (m *monitoringSource) Quota(ctx context.Context, obs monitoring.ProviderObservation) (*monitoring.QuotaSnapshot, error) {
	p, err := m.server.router.ProviderByID(obs.ID)
	if err != nil {
		return nil, nil
	}
	now := m.now().UTC()
	snapshot, err := m.quotaFetch(ctx, p, now)
	if err != nil {
		// Never persist the vendor's raw answer: it can echo request headers.
		return &monitoring.QuotaSnapshot{
			Source: obs.Vendor, Ratio: -1, ObservedAt: now, Error: classifyQuotaError(err),
		}, nil
	}
	return snapshot, nil
}

func (m *monitoringSource) MultiIP(ctx context.Context, now time.Time) ([]monitoring.MultiIPHit, error) {
	detector, ok := m.server.stateSaver.(multiIPDetector)
	if !ok {
		return nil, errors.New("request log store has no multi-IP detector")
	}
	hits, err := detector.DetectMultiIPKeys(now.Add(-10*time.Minute), 5)
	if err != nil {
		return nil, err
	}
	out := make([]monitoring.MultiIPHit, 0, len(hits))
	for _, hit := range hits {
		out = append(out, monitoring.MultiIPHit{
			APIKeyID: hit.APIKeyID, APIKeyName: hit.APIKeyName, IPs: hit.IPs,
			IPCount: hit.IPCount, RequestCount: hit.RequestCount,
		})
	}
	return out, nil
}

func (m *monitoringSource) ConcurrentIP(ctx context.Context, now time.Time) ([]monitoring.ConcurrentIPHit, error) {
	detector, ok := m.server.stateSaver.(multiIPDetector)
	if !ok {
		return nil, errors.New("request log store has no concurrent-IP detector")
	}
	hits, err := detector.DetectConcurrentIPKeys(now.Add(-10*time.Minute), 4)
	if err != nil {
		return nil, err
	}
	out := make([]monitoring.ConcurrentIPHit, 0, len(hits))
	for _, hit := range hits {
		out = append(out, monitoring.ConcurrentIPHit{
			APIKeyID: hit.APIKeyID, APIKeyName: hit.APIKeyName, At: hit.At,
			IPs: hit.IPs, IPCount: hit.IPCount, RequestCount: hit.RequestCount,
		})
	}
	return out, nil
}

func (m *monitoringSource) LastRenewal(ctx context.Context, providerID string) (monitoring.RenewalOutcome, error) {
	if m.server.monitoringStore == nil {
		return monitoring.RenewalOutcome{}, nil
	}
	return m.server.monitoringStore.RecentRenewalOutcomes(providerID, 10)
}

func (m *monitoringSource) ModelFailures(ctx context.Context, providerID string) (int, error) {
	return m.modelFailureCount(providerID), nil
}

func (m *monitoringSource) modelFailureCount(providerID string) int {
	m.modelMu.Lock()
	defer m.modelMu.Unlock()
	return m.modelFailures[providerID]
}

func (m *monitoringSource) setModelFailure(providerID string, failed bool) {
	m.modelMu.Lock()
	defer m.modelMu.Unlock()
	if failed {
		m.modelFailures[providerID]++
		return
	}
	delete(m.modelFailures, providerID)
}

func (m *monitoringSource) dispatchLocation() *time.Location {
	if m.server.monitoringStore == nil {
		return monitoring.DefaultDispatchConfig().Location()
	}
	return monitoring.NewConfig(m.server.monitoringStore).Dispatch().Location()
}

// quotaForProvider maps only what each vendor's existing reader already returns.
// A vendor without a quota reader keeps the capability off, so quota_low never
// invents a number for it.
func (m *monitoringSource) quotaForProvider(ctx context.Context, p domain.Provider, now time.Time) (*monitoring.QuotaSnapshot, error) {
	switch {
	case p.ClaudeOAuth != nil:
		report, err := fetchClaudeOAuthUsage(ctx, p.ClaudeOAuth.AccessToken)
		if err != nil {
			return nil, err
		}
		if !report.Available {
			return nil, errors.New(firstNonEmpty(report.Error, "claude usage unavailable"))
		}
		snapshot := quotaFromUsedPercent("claude", maxClaudeUtilization(report), claudeWindowReset(report), now)
		snapshot.Windows = claudeQuotaWindows(report)
		return snapshot, nil
	case p.ChatGPTOAuth != nil:
		report, err := fetchChatGPTOAuthUsage(ctx, p)
		if err != nil {
			return nil, err
		}
		if !report.Available {
			return nil, errors.New(firstNonEmpty(report.Error, "chatgpt usage unavailable"))
		}
		return quotaFromUsedPercent("chatgpt", maxChatGPTUtilization(report), time.Time{}, now), nil
	case p.CursorOAuth != nil:
		report, err := fetchCursorOAuthUsage(ctx, p.CursorOAuth.AccessToken)
		if err != nil {
			return nil, err
		}
		if !report.Available {
			return nil, errors.New(firstNonEmpty(report.Error, "cursor usage unavailable"))
		}
		return quotaFromUsedPercent("cursor", maxCursorUtilization(report), time.Time{}, now), nil
	case p.QoderPAT != nil:
		_, quota := m.server.qoderQuotaFor(ctx, p)
		if quota.Error != "" {
			return nil, errors.New(quota.Error)
		}
		return quotaFromWindows("qoder", qoderWindowViews(quota.Windows), parseQuotaTime(quota.Until), now), nil
	case p.WorkBuddy != nil:
		quota := m.server.workBuddyQuota(ctx, p)
		if quota.Error != "" {
			return nil, errors.New(quota.Error)
		}
		return quotaFromWindows("workbuddy", workBuddyWindowViews(quota.Windows), workBuddyCreditsDeadline(quota.Windows), now), nil
	case isOpenCodeGoProvider(p):
		apiKey := resolveProviderAuth(p)
		if strings.TrimSpace(apiKey) == "" {
			return nil, errors.New("OpenCode Go 未配置 API Key")
		}
		report, err := fetchOpenCodeGoUsage(ctx, p.BaseURL, apiKey)
		if err != nil {
			return nil, err
		}
		if !report.Available {
			return nil, errors.New(firstNonEmpty(report.Error, "OpenCode Go 额度不可用"))
		}
		return openCodeGoQuotaSnapshot(report, now), nil
	default:
		return nil, nil
	}
}

// maxClaudeUtilization picks the window that actually turns requests away: the
// overage window first, then the weekly and five-hour buckets.
func maxClaudeUtilization(report ClaudeOAuthUsageReport) float64 {
	best := 0.0
	for _, bucket := range []*ClaudeOAuthUsageBucket{
		report.SevenDayOverageIncluded, report.SevenDay, report.FiveHour,
		report.SevenDaySonnet, report.SevenDayOpus,
	} {
		if bucket != nil && bucket.Utilization > best {
			best = bucket.Utilization
		}
	}
	return math.Min(100, best)
}

// claudeWindowReset names when the window maxClaudeUtilization picked resets, so
// the console can say "00:40 重置" instead of deriving a time from the schedule.
// A bucket without resets_at falls back to any other window that has one.
func claudeWindowReset(report ClaudeOAuthUsageReport) time.Time {
	var best, fallback *ClaudeOAuthUsageBucket
	for _, bucket := range []*ClaudeOAuthUsageBucket{
		report.SevenDayOverageIncluded, report.SevenDay, report.FiveHour,
		report.SevenDaySonnet, report.SevenDayOpus,
	} {
		if bucket == nil {
			continue
		}
		reset := parseClaudeResetsAt(bucket.ResetsAt)
		if !reset.IsZero() && fallback == nil {
			fallback = bucket
		}
		if best == nil || bucket.Utilization > best.Utilization {
			best = bucket
		}
	}
	if best == nil {
		return time.Time{}
	}
	if reset := parseClaudeResetsAt(best.ResetsAt); !reset.IsZero() {
		return reset
	}
	if fallback != nil {
		return parseClaudeResetsAt(fallback.ResetsAt)
	}
	return time.Time{}
}

// claudeQuotaWindows keeps Claude's buckets apart in the order an operator reads
// them: the five-hour window first (it is the one keepalive targets), then the
// weekly buckets.
func claudeQuotaWindows(report ClaudeOAuthUsageReport) []monitoring.QuotaWindow {
	buckets := []struct {
		key    string
		bucket *ClaudeOAuthUsageBucket
	}{
		{"five_hour", report.FiveHour},
		{"seven_day", report.SevenDay},
		{"seven_day_sonnet", report.SevenDaySonnet},
		{"seven_day_opus", report.SevenDayOpus},
		{"seven_day_overage_included", report.SevenDayOverageIncluded},
	}
	windows := make([]monitoring.QuotaWindow, 0, len(buckets))
	for _, item := range buckets {
		if item.bucket == nil {
			continue
		}
		windows = append(windows, monitoring.QuotaWindow{
			Key:         item.key,
			UsedPercent: math.Min(100, math.Max(0, item.bucket.Utilization)),
			ResetsAt:    parseClaudeResetsAt(item.bucket.ResetsAt),
		})
	}
	// The per-model weekly allowance the app shows as 「仅限 Fable」 lives only in
	// the limits array. Appended under its own keys: the five_hour / seven_day
	// keys above drive keepalive and cooldown decisions, so they keep meaning
	// exactly what they meant before.
	for _, limit := range report.Limits {
		key := "limit_" + limit.Kind
		if limit.Label != "" {
			key = "limit_" + limit.Kind + "_" + strings.ToLower(limit.Label)
		}
		windows = append(windows, monitoring.QuotaWindow{
			Key:         key,
			UsedPercent: math.Min(100, math.Max(0, limit.Percent)),
			ResetsAt:    parseClaudeResetsAt(limit.ResetsAt),
		})
	}
	return windows
}

func parseClaudeResetsAt(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func maxChatGPTUtilization(report ChatGPTOAuthUsageReport) float64 {
	best := 0.0
	for _, bucket := range report.Buckets {
		if bucket.Utilization > best {
			best = bucket.Utilization
		}
	}
	return math.Min(100, best)
}

func maxCursorUtilization(report CursorOAuthUsageReport) float64 {
	best := 0.0
	for _, bucket := range report.Buckets {
		if bucket.Utilization > best {
			best = bucket.Utilization
		}
	}
	return math.Min(100, best)
}

// openCodeGoQuotaSnapshot 把 5 小时、7 天、月三个窗口折叠成监控中心使用的
// 额度快照：取已用比例最高的窗口作为账号余量，并保留每个窗口的明细。
func openCodeGoQuotaSnapshot(report OpenCodeGoUsageReport, now time.Time) *monitoring.QuotaSnapshot {
	type quotaWindow struct {
		key    string
		bucket *OpenCodeGoUsageBucket
	}
	windows := []quotaWindow{
		{key: "five_hour", bucket: report.FiveHour},
		{key: "weekly", bucket: report.Weekly},
		{key: "monthly", bucket: report.Monthly},
	}

	snapshot := &monitoring.QuotaSnapshot{Source: "opencode-go", Ratio: -1, ObservedAt: now}
	worst := -1.0
	var worstReset time.Time
	for _, window := range windows {
		if window.bucket == nil {
			continue
		}
		used := math.Min(100, math.Max(0, window.bucket.Utilization))
		reset := parseClaudeResetsAt(window.bucket.ResetsAt)
		snapshot.Windows = append(snapshot.Windows, monitoring.QuotaWindow{
			Key: window.key, UsedPercent: used, ResetsAt: reset,
		})
		if used > worst {
			worst = used
			worstReset = reset
		}
	}
	if worst < 0 {
		return snapshot
	}
	ratio := 1 - worst/100
	snapshot.Remaining = ratio * 100
	snapshot.Limit = 100
	snapshot.Ratio = ratio
	snapshot.ExpiresAt = worstReset
	return snapshot
}

// quotaFromUsedPercent folds a percentage window into a 100-unit allowance so
// Ratio keeps meaning "what is left".
func quotaFromUsedPercent(source string, usedPercent float64, expiresAt, now time.Time) *monitoring.QuotaSnapshot {
	ratio := 1 - math.Min(100, math.Max(0, usedPercent))/100
	return &monitoring.QuotaSnapshot{
		Source: source, Remaining: ratio * 100, Limit: 100, Ratio: ratio,
		ExpiresAt: expiresAt, ObservedAt: now,
	}
}

// quotaWindowView is the common shape of Qoder's and WorkBuddy's windows:
// Used is a 0-100 percentage of Limit.
type quotaWindowView struct {
	UsedPercent float64
	Limit       float64
}

func qoderWindowViews(windows []qoderQuotaWindow) []quotaWindowView {
	out := make([]quotaWindowView, 0, len(windows))
	for _, w := range windows {
		out = append(out, quotaWindowView{UsedPercent: w.Used, Limit: w.Limit})
	}
	return out
}

func workBuddyWindowViews(windows []workBuddyQuotaWin) []quotaWindowView {
	out := make([]quotaWindowView, 0, len(windows))
	for _, w := range windows {
		out = append(out, quotaWindowView{UsedPercent: w.Used, Limit: w.Limit})
	}
	return out
}

// quotaFromWindows takes the window that is most spent as the account's answer:
// the first pool to run dry is the one that stops requests.
func quotaFromWindows(source string, windows []quotaWindowView, expiresAt, now time.Time) *monitoring.QuotaSnapshot {
	var worst quotaWindowView
	found := false
	for _, w := range windows {
		if w.Limit <= 0 {
			continue
		}
		if !found || w.UsedPercent > worst.UsedPercent {
			worst, found = w, true
		}
	}
	if !found {
		return &monitoring.QuotaSnapshot{Source: source, Ratio: -1, ExpiresAt: expiresAt, ObservedAt: now}
	}
	ratio := 1 - math.Min(100, worst.UsedPercent)/100
	return &monitoring.QuotaSnapshot{
		Source: source, Remaining: ratio * worst.Limit, Limit: worst.Limit, Ratio: ratio,
		ExpiresAt: expiresAt, ObservedAt: now,
	}
}

func parseQuotaTime(raw string) time.Time {
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}
	}
	return at
}

// workBuddyCreditsDeadline is when the soonest of WorkBuddy's dated credit
// batches expires: a batch that runs out early is lost even if the cycle has
// time left.
func workBuddyCreditsDeadline(windows []workBuddyQuotaWin) time.Time {
	var earliest time.Time
	for _, w := range windows {
		if w.RunsOut <= 0 || strings.TrimSpace(w.RunsOutAt) == "" {
			continue
		}
		at := parseQuotaTime(w.RunsOutAt)
		if at.IsZero() {
			continue
		}
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
	}
	return earliest
}

// classifyQuotaError turns a vendor answer into a fixed label; the raw text (an
// upstream body or URL) never reaches the database.
func classifyQuotaError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "401"), strings.Contains(text, "403"),
		strings.Contains(text, "unauthor"), strings.Contains(text, "invalid_grant"),
		strings.Contains(text, "login"), strings.Contains(text, "登录"),
		strings.Contains(text, "未登录"), strings.Contains(text, "失效"):
		return "auth_failed"
	case strings.Contains(text, "429"), strings.Contains(text, "rate limit"), strings.Contains(text, "限流"):
		return "rate_limited"
	case strings.Contains(text, "timeout"), strings.Contains(text, "deadline"),
		strings.Contains(text, "connection"), strings.Contains(text, "network"),
		strings.Contains(text, "no such host"), strings.Contains(text, "eof"):
		return "network"
	default:
		return "unknown"
	}
}

// providerHasExpiry reports whether the credential carries an expiry the
// monitoring rules can watch.
func providerHasExpiry(p domain.Provider) bool {
	switch p.AuthType {
	case domain.AuthTypeClaudeOAuth, domain.AuthTypeChatGPTOAuth, domain.AuthTypeCursorOAuth,
		domain.AuthTypeQoderPAT, domain.AuthTypeQoderCN, domain.AuthTypeWorkBuddy,
		domain.AuthTypeWorkBuddyAI, domain.AuthTypeTraeCN, domain.AuthTypeTraeGlobal:
		return true
	}
	return false
}

// providerCredentialExpiry reads the stored expiry without parsing or refreshing
// anything. ZCode keys have no expiry of their own (the plan's own page reports
// it), so they answer zero.
func providerCredentialExpiry(p domain.Provider) time.Time {
	raw := ""
	switch {
	case p.ClaudeOAuth != nil:
		raw = p.ClaudeOAuth.ExpiresAt
	case p.ChatGPTOAuth != nil:
		raw = p.ChatGPTOAuth.ExpiresAt
	case p.CursorOAuth != nil:
		raw = p.CursorOAuth.ExpiresAt
	case p.QoderPAT != nil:
		raw = p.QoderPAT.ExpiresAt
	case p.WorkBuddy != nil:
		raw = p.WorkBuddy.ExpiresAt
	case p.Trae != nil:
		raw = p.Trae.ExpiresAt
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}
	}
	return at
}

func providerHasQuotaHandler(p domain.Provider) bool {
	return p.ClaudeOAuth != nil || p.ChatGPTOAuth != nil || p.CursorOAuth != nil ||
		p.QoderPAT != nil || p.WorkBuddy != nil ||
		(isOpenCodeGoProvider(p) && strings.TrimSpace(resolveProviderAuth(p)) != "")
}

func providerHasModelSync(p domain.Provider) bool {
	switch p.AuthType {
	case domain.AuthTypeCursorOAuth, domain.AuthTypeQoderPAT, domain.AuthTypeQoderCN,
		domain.AuthTypeZCode, domain.AuthTypeZCodeBigModel, domain.AuthTypeTraeCN,
		domain.AuthTypeTraeGlobal, domain.AuthTypeWorkBuddy, domain.AuthTypeWorkBuddyAI:
		return true
	}
	return false
}

func providerHasCheckin(p domain.Provider) bool {
	_, ok := checkinVendorOf(p)
	return ok
}

// providerVendorKey names the vendor a provider belongs to, for the console's
// brand icons and grouping. It mirrors the frontend preset table: a subscription
// is its auth type, an API key is recognized by the vendor's host.
func providerVendorKey(p domain.Provider) string {
	switch p.AuthType {
	case domain.AuthTypeClaudeOAuth:
		return "claude"
	case domain.AuthTypeCursorOAuth:
		return "cursor"
	case domain.AuthTypeChatGPTOAuth:
		return "chatgpt"
	case domain.AuthTypeQoderPAT, domain.AuthTypeQoderCN:
		return "qoder"
	case domain.AuthTypeWorkBuddy, domain.AuthTypeWorkBuddyAI:
		return "workbuddy"
	case domain.AuthTypeZCode, domain.AuthTypeZCodeBigModel:
		return "zcode"
	case domain.AuthTypeTraeCN, domain.AuthTypeTraeGlobal:
		return "trae"
	case domain.AuthTypeOpenCodeZenFree:
		return "opencode"
	}
	return vendorKeyForHost(p.BaseURL)
}

// vendorHostKeys maps a host suffix to the vendor key of the frontend catalog.
// Longest suffix first so "api.minimaxi.com" is not shadowed by "minimax".
var vendorHostKeys = []struct{ suffix, key string }{
	{"minimaxi.com", "minimax"},
	{"minimax.io", "minimax"},
	{"api.anthropic.com", "anthropic"},
	{"api.openai.com", "openai"},
	{"chatgpt.com", "chatgpt"},
	{"googleapis.com", "google"},
	{"api.deepseek.com", "deepseek"},
	{"api.x.ai", "xai"},
	{"api.moonshot.", "moonshot"},
	{"api.kimi.", "moonshot"},
	{"kimi.com", "moonshot"},
	{"bigmodel.cn", "zhipu"},
	{"api.z.ai", "zai"},
	{"api.stepfun.", "stepfun"},
	{"dashscope", "qwen"},
	{"aliyuncs.com", "qwen"},
	{"volces.com", "volcengine"},
	{"lkeap.cloud.tencent.com", "tencent-cloud"},
	{"modelarts-maas.com", "huaweicloud"},
	{"qianfan.baidubce.com", "baidu-qianfan"},
	{"copilot.tencent.com", "workbuddy"},
	{"workbuddy.ai", "workbuddy"},
	{"api.mistral.ai", "mistral"},
	{"api.groq.com", "groq"},
	{"openrouter.ai", "openrouter"},
	{"aihubmix.com", "aihubmix"},
	{"302.ai", "302ai"},
	{"siliconflow", "siliconflow"},
	{"together.xyz", "together"},
	{"fireworks.ai", "fireworks"},
	{"api.nvidia.com", "nvidia"},
	{"modelscope.cn", "modelscope"},
	{"opencode.ai", "opencode"},
	{"localhost", "local"},
	{"127.0.0.1", "local"},
}

func vendorKeyForHost(baseURL string) string {
	host := strings.ToLower(strings.TrimSpace(baseURL))
	if index := strings.Index(host, "://"); index >= 0 {
		host = host[index+3:]
	}
	if index := strings.Index(host, "/"); index >= 0 {
		host = host[:index]
	}
	sorted := append([]struct{ suffix, key string }(nil), vendorHostKeys...)
	sort.SliceStable(sorted, func(i, j int) bool { return len(sorted[i].suffix) > len(sorted[j].suffix) })
	for _, entry := range sorted {
		if strings.Contains(host, entry.suffix) {
			return entry.key
		}
	}
	return ""
}
