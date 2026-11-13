package monitor

import (
	"sort"
	"strings"
	"sync"
	"time"
)

type RequestLog struct {
	// ID is the request_logs row id (monotonic autoincrement). Exposed so the
	// console can freeze a stable pagination snapshot (beforeId) — offset paging
	// on a time-desc list otherwise shifts as new logs arrive.
	ID         int64     `json:"id,omitempty"`
	Time       time.Time `json:"time"`
	APIKeyID   string    `json:"apiKeyId,omitempty"`
	APIKeyName string    `json:"apiKeyName,omitempty"`
	// UserName is a transient display field resolved at query time from the
	// owning API key's user id (not persisted). Renames reflect immediately.
	UserName   string `json:"userName,omitempty"`
	RouteID    string `json:"routeId"`
	ProviderID string `json:"providerId"`
	Model      string `json:"model"`
	// ReasoningEffort labels the thinking/reasoning setting the upstream
	// actually received: an OpenAI-style effort ("medium"), a Claude thinking
	// mode ("adaptive" / "enabled·8000"), or "" when the request carried none.
	ReasoningEffort  string `json:"reasoningEffort,omitempty"`
	Action           string `json:"action"`
	ProtocolFlow     string `json:"protocolFlow"`
	Path             string `json:"path"`
	Status           int    `json:"status"`
	InputTokens      int64  `json:"inputTokens"` // prompt total (inclusive of cache)
	OutputTokens     int64  `json:"outputTokens"`
	CacheTokens      int64  `json:"cacheTokens"`                // cache-read/hit portion only
	CacheWriteTokens int64  `json:"cacheWriteTokens,omitempty"` // cache-creation portion, billed inside input
	// CostMicros is what the gateway's own price table says this request cost,
	// in micro US dollars. Priced says whether a price row existed: an
	// unpriced request is 未计价 in the console, never a $0.00 that looks like a
	// free model. Costs are computed at request time so a later price edit does
	// not rewrite what a past request cost.
	CostMicros    int64 `json:"costMicros,omitempty"`
	Priced        bool  `json:"priced,omitempty"`
	LatencyMillis int64 `json:"latencyMs"`
	TTFTMillis    int64 `json:"ttftMs,omitempty"`
	// Observational latency breakdown (ms). Zero means unset / unavailable.
	PrepMillis            int64  `json:"prepMs,omitempty"`
	PreUpstreamMillis     int64  `json:"preUpstreamMs,omitempty"`
	UpstreamTTFBMillis    int64  `json:"upstreamTtfbMs,omitempty"`
	GatewayOverheadMillis int64  `json:"gatewayOverheadMs,omitempty"`
	ConvertOutMillis      int64  `json:"convertOutMs,omitempty"`
	PostMillis            int64  `json:"postMs,omitempty"`
	TimingFlags           string `json:"timingFlags,omitempty"`
	ClientHost            string `json:"clientHost,omitempty"`
	ClientIP              string `json:"clientIp,omitempty"`
	AccessSource          string `json:"accessSource,omitempty"` // lan | public | local
	// 父子网关透传的链路标记。三者都空表示这是一次纯本地请求，和引入父子
	// 功能之前完全一样——界面据此决定要不要画链路，老日志也就自然兼容。
	//
	// Ingress 是把这次请求送进来的对端网关 ID（空 = 客户端直连本机）。
	Ingress string `json:"ingress,omitempty"`
	// Egress 是本机把这次请求转发出去的对端网关 ID（空 = 本机直连 provider）。
	Egress string `json:"egress,omitempty"`
	// PeerGatewayID 是这次请求要对账的那个对端网关：有 Ingress 取 Ingress，
	// 否则取 Egress。单独存一列是为了让"某个子网关上个月调了多少"是一条
	// 带索引的 GROUP BY，而不是在两列上写 coalesce 扫全表。
	PeerGatewayID string `json:"peerGatewayId,omitempty"`
	// PeerDirection 是 ingress 或 egress。同一个对端既可能把流量送进来，也可能
	// 接走流量，只凭 PeerGatewayID 分不清方向，而这两种用量要分开算。
	PeerDirection string `json:"peerDirection,omitempty"`
	// PeerOrigin 是这次透传的发起方网关 ID（X-Gateway-Origin）。三级链路里
	// 中间那一跳的 Ingress/Egress 都只是"相邻方"，只有它能指向真正的起点。
	PeerOrigin string `json:"peerOrigin,omitempty"`
	// PeerPath 是这次请求已经过的网关 ID 链（X-Gateway-Path 的值，逗号分隔，
	// 从发起方开始，不含本机）。两级链路里 Ingress/PeerOrigin 已经足够；三级
	// 及以上只有它能回答"请求到底绕了几道"。界面的链路提示用它拼完整链。
	PeerPath         string          `json:"peerPath,omitempty"`
	ErrorDescription string          `json:"errorDescription,omitempty"`
	RequestBody      string          `json:"requestBody,omitempty"`
	ResponseBody     string          `json:"responseBody,omitempty"`
	Analysis         RequestAnalysis `json:"analysis"`
}

const (
	AccessSourceLAN    = "lan"
	AccessSourcePublic = "public"
	AccessSourceLocal  = "local"
	// AccessSourceMonitorProbe 标出监控中心自己发的流量。它必须从账号健康统计里
	// 排除，否则探测失败会把自己算成一次线上故障，越监控越红。
	AccessSourceMonitorProbe = "monitor_probe"
)

// Alert is one raised alert record. Unlike AppLog (in-memory, capped, lost on
// restart) alerts are persisted to SQLite so a leak signal survives a restart.
type Alert struct {
	ID       int64     `json:"id"`
	Time     time.Time `json:"time"`
	Rule     string    `json:"rule"`
	Severity string    `json:"severity"`
	// APIKeyID is the stable key id; APIKeyName is a mutable display label.
	APIKeyID   string `json:"apiKeyId"`
	APIKeyName string `json:"apiKeyName"`
	// IPs is the distinct client-IP set observed in the window. Cooldown
	// suppression compares against this set: a brand-new IP means a new event.
	IPs           []string `json:"ips"`
	IPCount       int      `json:"ipCount"`
	WindowMinutes int      `json:"windowMinutes"`
	RequestCount  int      `json:"requestCount"`
	// ConcurrentAt is the instant peak concurrency was observed. Set only by the
	// overlap rule; zero for window-based rules.
	ConcurrentAt time.Time `json:"concurrentAt,omitempty"`
	Status       string    `json:"status"`
	PushStatus   string    `json:"pushStatus,omitempty"`
	PushError    string    `json:"pushError,omitempty"`
}

// MultiIPHit is one detection result: an API key seen from IPCount distinct
// client IPs inside the scan window.
type MultiIPHit struct {
	APIKeyID     string
	APIKeyName   string
	IPs          []string
	IPCount      int
	RequestCount int
}

// ConcurrentIPHit is one detection result for the overlap rule: at instant At
// the key had IPCount distinct client IPs with in-flight requests at once.
type ConcurrentIPHit struct {
	APIKeyID     string
	APIKeyName   string
	At           time.Time
	IPs          []string
	IPCount      int
	RequestCount int
}

// AlertQuery filters and pages persisted alerts.
type AlertQuery struct {
	Status   string // "" / all | unread | read | ignored
	Page     int
	PageSize int
}

// RequestLogQuery filters and pages request logs.
type RequestLogQuery struct {
	From       time.Time
	To         time.Time
	Status     string // all | 2xx | 499 | 4xx | 5xx
	APIKeyName string // substring match against api_key_name (case-insensitive)
	// ProviderID filters to the exact input provider id (empty means no filter).
	ProviderID string
	Model      string // exact model match
	// SessionID filters to the requests of one conversation: the scheduler
	// stamps it into the log's analysis as session_id.
	SessionID string
	// APIKeyIDs restricts results to logs whose api_key_id is in this set.
	// Used for per-user data isolation, and for the admin-only "owner user"
	// log filter; nil means no restriction.
	APIKeyIDs     []string
	Page          int
	PageSize      int
	IncludeBodies bool // list views should omit heavy request/response bodies
	// BeforeID, when > 0, restricts results to rows with id <= BeforeID. The
	// console freezes this to the newest id at the moment it leaves page 1 so
	// offset pagination stays stable while new logs keep arriving.
	BeforeID int64
	// BeforeTime, when set, freezes the newest log timestamp (RFC3339/RFC3339Nano)
	// from page 1. Combined with BeforeID it matches ORDER BY time DESC, id DESC:
	// time < BeforeTime OR (time = BeforeTime AND id <= BeforeID). When only
	// BeforeTime is set, results are restricted to time <= BeforeTime.
	BeforeTime time.Time
}

type RequestLogPage struct {
	Items []RequestLog `json:"items"`
	Total int          `json:"total"`
	Page  int          `json:"page"`
}

// APIKeyDayStats aggregates token usage. InputTokens is prompt total (inclusive
// of cache); CacheTokens is cache-read only. UI "in" = InputTokens - CacheTokens.
type APIKeyDayStats struct {
	APIKeyID     string `json:"apiKeyId"`
	APIKeyName   string `json:"apiKeyName"`
	RequestCount int64  `json:"requestCount"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	CacheTokens  int64  `json:"cacheTokens"`
	// CacheWriteTokens and CostMicros are absent on buckets aggregated before
	// they existed; the console shows 未计价 for a day whose cost was never
	// computed rather than reading the missing total as free.
	CacheWriteTokens int64 `json:"cacheWriteTokens,omitempty"`
	CostMicros       int64 `json:"costMicros,omitempty"`
	// PricedCount is how many of RequestCount had a price row when they ran.
	// CostMicros alone cannot tell a cheap period from a mostly-unpriced one.
	PricedCount int64 `json:"pricedCount,omitempty"`
	// Forwarded payload bytes: Rx from downstream clients, Tx back to them.
	RxBytes int64 `json:"rxBytes,omitempty"`
	TxBytes int64 `json:"txBytes,omitempty"`
}

type ProviderDayStats struct {
	ProviderID       string `json:"providerId"`
	RequestCount     int64  `json:"requestCount"`
	InputTokens      int64  `json:"inputTokens"`
	OutputTokens     int64  `json:"outputTokens"`
	CacheTokens      int64  `json:"cacheTokens"`
	CacheWriteTokens int64  `json:"cacheWriteTokens,omitempty"`
	CostMicros       int64  `json:"costMicros,omitempty"`
	PricedCount      int64  `json:"pricedCount,omitempty"`
}

// UserDayStats aggregates token/request usage for one owner user id. UserID is
// stable (keys are bucketed by their OwnerUserID); UserName is resolved for
// display at query time so renames never corrupt historical aggregates.
type UserDayStats struct {
	UserID           string `json:"userId"`
	UserName         string `json:"userName"`
	RequestCount     int64  `json:"requestCount"`
	InputTokens      int64  `json:"inputTokens"`
	OutputTokens     int64  `json:"outputTokens"`
	CacheTokens      int64  `json:"cacheTokens"`
	CacheWriteTokens int64  `json:"cacheWriteTokens,omitempty"`
	CostMicros       int64  `json:"costMicros,omitempty"`
	PricedCount      int64  `json:"pricedCount,omitempty"`
}

// DownstreamGatewayDayStats aggregates requests delivered by one downstream
// gateway (PeerDirection == "ingress"). GatewayName is resolved at response
// time; persisted buckets only store the stable gateway id.
type DownstreamGatewayDayStats struct {
	GatewayID        string `json:"gatewayId"`
	GatewayName      string `json:"gatewayName,omitempty"`
	RequestCount     int64  `json:"requestCount"`
	InputTokens      int64  `json:"inputTokens"`
	OutputTokens     int64  `json:"outputTokens"`
	CacheTokens      int64  `json:"cacheTokens"`
	CacheWriteTokens int64  `json:"cacheWriteTokens,omitempty"`
	CostMicros       int64  `json:"costMicros,omitempty"`
	PricedCount      int64  `json:"pricedCount,omitempty"`
}

// ModelDayStats aggregates request/token usage for one model id.
type ModelDayStats struct {
	Model            string `json:"model"`
	RequestCount     int64  `json:"requestCount"`
	InputTokens      int64  `json:"inputTokens"`
	OutputTokens     int64  `json:"outputTokens"`
	CacheTokens      int64  `json:"cacheTokens"`
	CacheWriteTokens int64  `json:"cacheWriteTokens,omitempty"`
	CostMicros       int64  `json:"costMicros,omitempty"`
	PricedCount      int64  `json:"pricedCount,omitempty"`
}

// ProtocolDayStats aggregates request/token usage for one output protocol
// (OpenAI Chat / OpenAI Responses / Claude — the protocol the client called).
type ProtocolDayStats struct {
	Protocol         string `json:"protocol"`
	RequestCount     int64  `json:"requestCount"`
	InputTokens      int64  `json:"inputTokens"`
	OutputTokens     int64  `json:"outputTokens"`
	CacheTokens      int64  `json:"cacheTokens"`
	CacheWriteTokens int64  `json:"cacheWriteTokens,omitempty"`
	CostMicros       int64  `json:"costMicros,omitempty"`
	PricedCount      int64  `json:"pricedCount,omitempty"`
}

type TodayStatsSnapshot struct {
	Date        string             `json:"date"`
	Total       APIKeyDayStats     `json:"total"`
	LastRequest *RequestLog        `json:"lastRequest,omitempty"`
	ByAPIKey    []APIKeyDayStats   `json:"byApiKey"`
	ByProvider  []ProviderDayStats `json:"byProvider"`
	// MyByProvider is the same provider breakdown restricted to API keys with
	// an explicit owner equal to the requesting user. Peer/downstream keys with
	// no owner are deliberately excluded from this personal view.
	MyByProvider        []ProviderDayStats          `json:"myByProvider,omitempty"`
	ByModel             []ModelDayStats             `json:"byModel"`
	ByDownstreamGateway []DownstreamGatewayDayStats `json:"byDownstreamGateway,omitempty"`
	ByUser              []UserDayStats              `json:"byUser,omitempty"`
	ByProtocol          []ProtocolDayStats          `json:"byProtocol,omitempty"`
}

type PeriodStatsSnapshot struct {
	Period              string                      `json:"period"`
	Total               APIKeyDayStats              `json:"total"`
	ByAPIKey            []APIKeyDayStats            `json:"byApiKey"`
	ByProvider          []ProviderDayStats          `json:"byProvider"`
	ByModel             []ModelDayStats             `json:"byModel"`
	ByDownstreamGateway []DownstreamGatewayDayStats `json:"byDownstreamGateway,omitempty"`
	ByUser              []UserDayStats              `json:"byUser,omitempty"`
	ByProtocol          []ProtocolDayStats          `json:"byProtocol,omitempty"`
}

type DailyRequestPoint struct {
	Date         string `json:"date"`
	RequestCount int64  `json:"requestCount"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	CacheTokens  int64  `json:"cacheTokens"`
	// CacheWriteTokens is the day's cache-creation prompt tokens: billed at a
	// different rate than a cache read, so it is not folded into either total.
	CacheWriteTokens int64 `json:"cacheWriteTokens,omitempty"`
	// CostMicros is the day's priced cost in micro US dollars. PricedCount says
	// how many of that day's requests had a price row: a small cost next to a
	// large RequestCount means the period was mostly unpriced, not cheap.
	CostMicros  int64 `json:"costMicros,omitempty"`
	PricedCount int64 `json:"pricedCount,omitempty"`
	// RxBytes/TxBytes are gateway-forwarded payload bytes for the day: Rx is what
	// downstream clients sent us, Tx is what we returned. Unlike the host-metrics
	// NIC counters these are business traffic only and are kept forever.
	RxBytes      int64 `json:"rxBytes,omitempty"`
	TxBytes      int64 `json:"txBytes,omitempty"`
	AvgLatencyMs int64 `json:"avgLatencyMs,omitempty"`
	AvgTTFTMs    int64 `json:"avgTtftMs,omitempty"`
}

type StatusBucketStats struct {
	Class        string `json:"class"` // 2xx | 4xx | 5xx | other
	RequestCount int64  `json:"requestCount"`
}

type HourlyBucketStats struct {
	Hour         int   `json:"hour"`
	RequestCount int64 `json:"requestCount"`
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
}

type UsageStatsSnapshot struct {
	Today  TodayStatsSnapshot   `json:"today"`
	Month  PeriodStatsSnapshot  `json:"month"`
	Range  *PeriodStatsSnapshot `json:"range,omitempty"`
	From   string               `json:"from,omitempty"`
	To     string               `json:"to,omitempty"`
	Daily  []DailyRequestPoint  `json:"daily,omitempty"`
	Status []StatusBucketStats  `json:"status,omitempty"`
	Hourly []HourlyBucketStats  `json:"hourly,omitempty"`
}

type AppLog struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
	Context string    `json:"context,omitempty"`
}

type Store struct {
	mu               sync.RWMutex
	logs             []RequestLog
	appLogs          []AppLog
	logLevel         string
	usageByDay       map[string]*usageDayStats
	lastUsageRequest *RequestLog
	usageEvents      chan UsageEvent
	usageOnce        sync.Once
	usageDailyStore  UsageDailyStore
	// Usage batching thresholds (0 = use defaults) and shutdown plumbing.
	usageBatchMaxSize int
	usageBatchMaxWait time.Duration
	usageStop         chan struct{}
	usageStopped      chan struct{}
	usageCloseOnce    sync.Once
}

func NewStore() *Store {
	return &Store{
		logs:       make([]RequestLog, 0, 256),
		appLogs:    make([]AppLog, 0, 256),
		logLevel:   "info",
		usageByDay: make(map[string]*usageDayStats),
	}
}

// SetUsageDailyStore wires SQLite persistence for daily usage aggregates.
func (s *Store) SetUsageDailyStore(store UsageDailyStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usageDailyStore = store
}

// BootstrapUsageDays loads persisted daily aggregates into memory at startup.
func (s *Store) BootstrapUsageDays(days map[string]UsageDayBuckets, last *RequestLog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for dayKey, buckets := range days {
		day := newUsageDayStats()
		for id, child := range buckets.ByKey {
			day.byKey[id] = usageDayFromBuckets(child)
		}
		day.total = buckets.Total
		day.total.APIKeyName = "全部"
		day.status2xx = buckets.Status2xx
		day.status4xx = buckets.Status4xx
		day.status5xx = buckets.Status5xx
		day.statusOther = buckets.StatusOther
		day.latencySum = buckets.LatencySum
		day.ttftSum = buckets.TTFTSum
		day.ttftCount = buckets.TTFTCount
		for id, stats := range buckets.ByAPIKey {
			copied := stats
			day.byAPIKey[id] = &copied
		}
		for id, stats := range buckets.ByProvider {
			copied := stats
			day.byProvider[id] = &copied
		}
		for id, stats := range buckets.ByModel {
			copied := stats
			day.byModel[id] = &copied
		}
		for id, stats := range buckets.ByUser {
			copied := stats
			day.byUser[id] = &copied
		}
		for id, stats := range buckets.ByProtocol {
			copied := stats
			day.byProtocol[id] = &copied
		}
		for id, stats := range buckets.ByDownstreamGateway {
			copied := stats
			day.byDownstream[id] = &copied
		}
		s.usageByDay[dayKey] = day
	}
	if last != nil {
		copied := *last
		s.lastUsageRequest = &copied
	}
}

const memoryLogCap = 50000

func (s *Store) Add(log RequestLog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append([]RequestLog{log}, s.logs...)
	if len(s.logs) > memoryLogCap {
		s.logs = s.logs[:memoryLogCap]
	}
}

func (s *Store) Bootstrap(logs []RequestLog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(logs) == 0 {
		return
	}
	s.logs = append(logs, s.logs...)
	if len(s.logs) > memoryLogCap {
		s.logs = s.logs[:memoryLogCap]
	}
}

func (s *Store) List(limit int) []RequestLog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.logs) {
		limit = len(s.logs)
	}
	out := make([]RequestLog, limit)
	copy(out, s.logs[:limit])
	return out
}

func (s *Store) Query(query RequestLogQuery) RequestLogPage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = 100
	}
	if pageSize > 500 {
		pageSize = 500
	}
	page := query.Page
	if page <= 0 {
		page = 1
	}
	keyNameFilter := strings.ToLower(strings.TrimSpace(query.APIKeyName))
	var keyIDSet map[string]struct{}
	if query.APIKeyIDs != nil {
		keyIDSet = make(map[string]struct{}, len(query.APIKeyIDs))
		for _, id := range query.APIKeyIDs {
			keyIDSet[id] = struct{}{}
		}
	}
	filtered := make([]RequestLog, 0, len(s.logs))
	for _, item := range s.logs {
		if !query.From.IsZero() && item.Time.Before(query.From) {
			continue
		}
		if !query.To.IsZero() && !item.Time.Before(query.To) {
			continue
		}
		if !statusClassMatches(query.Status, item.Status) {
			continue
		}
		if keyNameFilter != "" && !strings.Contains(strings.ToLower(item.APIKeyName), keyNameFilter) {
			continue
		}
		if providerID := strings.TrimSpace(query.ProviderID); providerID != "" && item.ProviderID != providerID {
			continue
		}
		if model := strings.TrimSpace(query.Model); model != "" && item.Model != model {
			continue
		}
		if session := strings.TrimSpace(query.SessionID); session != "" && item.Analysis.SessionID != session {
			continue
		}
		if keyIDSet != nil {
			if _, ok := keyIDSet[item.APIKeyID]; !ok {
				continue
			}
		}
		if !query.BeforeTime.IsZero() && query.BeforeID > 0 {
			if item.Time.After(query.BeforeTime) {
				continue
			}
			if item.Time.Equal(query.BeforeTime) && item.ID > query.BeforeID {
				continue
			}
		} else if !query.BeforeTime.IsZero() {
			if item.Time.After(query.BeforeTime) {
				continue
			}
		} else if query.BeforeID > 0 && item.ID > query.BeforeID {
			continue
		}
		filtered = append(filtered, item)
	}
	total := len(filtered)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	items := make([]RequestLog, end-start)
	copy(items, filtered[start:end])
	return RequestLogPage{Items: items, Total: total, Page: page}
}

func statusClassMatches(class string, status int) bool {
	switch strings.ToLower(strings.TrimSpace(class)) {
	case "", "all":
		return true
	case "2xx":
		return status >= 200 && status < 300
	case "499":
		// 499 是客户端主动取消，单独成类；不能混进“4xx 限流/异常”，否则正常的
		// Stop/切换会话会被统计成错误。
		return status == 499
	case "4xx":
		return status >= 400 && status < 500 && status != 499
	case "5xx":
		return status >= 500 && status < 600
	default:
		return true
	}
}

func aggregateByProvider(logs []RequestLog, since time.Time) []ProviderDayStats {
	byProvider := map[string]*ProviderDayStats{}

	for index := range logs {
		log := logs[index]
		if log.Time.Before(since) {
			continue
		}
		providerID := strings.TrimSpace(log.ProviderID)
		if providerID == "" {
			providerID = "_unknown"
		}
		stats, ok := byProvider[providerID]
		if !ok {
			stats = &ProviderDayStats{ProviderID: providerID}
			byProvider[providerID] = stats
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

	out := make([]ProviderDayStats, 0, len(byProvider))
	for _, stats := range byProvider {
		out = append(out, *stats)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RequestCount == out[j].RequestCount {
			return out[i].ProviderID < out[j].ProviderID
		}
		return out[i].RequestCount > out[j].RequestCount
	})
	return out
}

func modelUsageTotalTokens(stats ModelDayStats) int64 {
	return stats.InputTokens + stats.OutputTokens
}

// NormalizeModelForStats returns a real upstream model id suitable for usage
// counters. Placeholders and unresolved aliases are rejected.
func NormalizeModelForStats(raw string) (string, bool) {
	model := CanonicalModelForUsage(raw)
	if model == "" || model == "_unknown" {
		return "", false
	}
	switch strings.ToLower(model) {
	case "your-model", "request-model-not-set":
		return "", false
	}
	return model, true
}

func sortModelDayStats(byModel map[string]*ModelDayStats) []ModelDayStats {
	out := make([]ModelDayStats, 0, len(byModel))
	for _, stats := range byModel {
		out = append(out, *stats)
	}
	sort.Slice(out, func(i, j int) bool {
		ti, tj := modelUsageTotalTokens(out[i]), modelUsageTotalTokens(out[j])
		if ti == tj {
			if out[i].RequestCount == out[j].RequestCount {
				return out[i].Model < out[j].Model
			}
			return out[i].RequestCount > out[j].RequestCount
		}
		return ti > tj
	})
	return out
}

// MergeModelDayStats folds model rows through canonical, combines the rows
// that land on the same id, and keeps the ranking order used elsewhere.
func MergeModelDayStats(items []ModelDayStats, canonical func(string) string) []ModelDayStats {
	if canonical == nil || len(items) == 0 {
		return items
	}
	byModel := make(map[string]*ModelDayStats, len(items))
	for _, item := range items {
		key := canonical(item.Model)
		if key == "" {
			key = item.Model
		}
		stats, ok := byModel[key]
		if !ok {
			stats = &ModelDayStats{Model: key}
			byModel[key] = stats
		}
		stats.RequestCount += item.RequestCount
		stats.InputTokens += item.InputTokens
		stats.OutputTokens += item.OutputTokens
		stats.CacheTokens += item.CacheTokens
		stats.CacheWriteTokens += item.CacheWriteTokens
		stats.CostMicros += item.CostMicros
		stats.PricedCount += item.PricedCount
	}
	return sortModelDayStats(byModel)
}

// OutputProtocolFromFlow extracts the client-facing (output) protocol from a
// request log's protocol_flow.
//
// 现行格式是 "<客户端> -> <上游>"（如 "OpenAI Responses -> OpenAI Chat"），客户端
// 协议在左边；直通只写一个协议名（如 "OpenAI Chat"）。
//
// 历史遗留：2026-11-13 之前写入的行是反的（"上游 -> 客户端"）。两种方向在字符
// 上无法区分——"Claude -> OpenAI Chat" 在新旧格式下都是合法且等长的表达，只是
// 含义相反。因此这里统一按新格式取左侧，并对旧行做一次数据订正，而不是在解析
// 时猜方向：猜错一次就会把协议用量算到另一条协议头上。
//
// 旧行都是当时 pass_through 分支写出来的假转换标签（19 行，全部 400 失败、零
// token），订正脚本见 store 的 protocol_flow_direction 迁移。
func OutputProtocolFromFlow(flow string) string {
	flow = strings.TrimSpace(flow)
	if name := protocolFlowName(flow); name != "" {
		return name
	}
	idx := strings.LastIndex(flow, "->")
	if idx < 0 {
		return ""
	}
	return protocolFlowName(flow[:idx])
}

// protocolFlowName 把一个片段规整成已知的协议显示名；不是已知协议时返回空串。
func protocolFlowName(fragment string) string {
	switch out := strings.TrimSpace(fragment); out {
	case "OpenAI Chat", "OpenAI Responses", "Claude":
		return out
	default:
		return ""
	}
}

func protocolUsageTotalTokens(s ProtocolDayStats) int64 { return s.InputTokens + s.OutputTokens }

func sortProtocolStats(byProtocol map[string]*ProtocolDayStats) []ProtocolDayStats {
	out := make([]ProtocolDayStats, 0, len(byProtocol))
	for _, stats := range byProtocol {
		out = append(out, *stats)
	}
	sort.Slice(out, func(i, j int) bool {
		ti, tj := protocolUsageTotalTokens(out[i]), protocolUsageTotalTokens(out[j])
		if ti == tj {
			if out[i].RequestCount == out[j].RequestCount {
				return out[i].Protocol < out[j].Protocol
			}
			return out[i].RequestCount > out[j].RequestCount
		}
		return ti > tj
	})
	return out
}

// aggregateByProtocol builds per-output-protocol stats from in-memory request
// logs (used by the per-user path, which lacks per-key day buckets).
func aggregateByProtocol(logs []RequestLog, since time.Time) []ProtocolDayStats {
	byProto := map[string]*ProtocolDayStats{}
	for index := range logs {
		log := logs[index]
		if log.Time.Before(since) {
			continue
		}
		proto := OutputProtocolFromFlow(log.ProtocolFlow)
		if proto == "" {
			continue
		}
		stats, ok := byProto[proto]
		if !ok {
			stats = &ProtocolDayStats{Protocol: proto}
			byProto[proto] = stats
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
	return sortProtocolStats(byProto)
}

// CanonicalModelForUsage returns the upstream/real model id for ranking.
// Historical logs may store "alias -> real-model"; prefer the real side.
func CanonicalModelForUsage(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "_unknown"
	}
	if left, right, ok := strings.Cut(raw, "->"); ok {
		if real := strings.TrimSpace(right); real != "" {
			return real
		}
		if alias := strings.TrimSpace(left); alias != "" {
			return alias
		}
	}
	return raw
}

// aggregateByModel ranks models by total token usage (input+output+cache),
// then by request count, then by model name. Alias forms are collapsed onto
// the resolved real model name. Empty / unknown model rows are omitted.
func aggregateByModel(logs []RequestLog, since time.Time) []ModelDayStats {
	byModel := map[string]*ModelDayStats{}

	for index := range logs {
		log := logs[index]
		if log.Time.Before(since) {
			continue
		}
		model := CanonicalModelForUsage(log.Model)
		if model == "" || model == "_unknown" {
			continue
		}
		stats, ok := byModel[model]
		if !ok {
			stats = &ModelDayStats{Model: model}
			byModel[model] = stats
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

	out := make([]ModelDayStats, 0, len(byModel))
	for _, stats := range byModel {
		out = append(out, *stats)
	}
	sort.Slice(out, func(i, j int) bool {
		ti, tj := modelUsageTotalTokens(out[i]), modelUsageTotalTokens(out[j])
		if ti == tj {
			if out[i].RequestCount == out[j].RequestCount {
				return out[i].Model < out[j].Model
			}
			return out[i].RequestCount > out[j].RequestCount
		}
		return ti > tj
	})
	return out
}

func aggregateLogsSince(logs []RequestLog, since time.Time, periodLabel string) PeriodStatsSnapshot {
	total := APIKeyDayStats{APIKeyName: "全部"}
	byKey := map[string]*APIKeyDayStats{}

	for index := range logs {
		log := logs[index]
		if log.Time.Before(since) {
			continue
		}
		total.RequestCount++
		total.InputTokens += log.InputTokens
		total.OutputTokens += log.OutputTokens
		total.CacheTokens += log.CacheTokens
		total.CacheWriteTokens += log.CacheWriteTokens
		total.CostMicros += log.CostMicros
		if log.Priced {
			total.PricedCount++
		}

		keyID := log.APIKeyID
		keyName := log.APIKeyName
		if keyID == "" {
			keyID = "_anonymous"
			keyName = "未绑定 Key"
		}
		stats, ok := byKey[keyID]
		if !ok {
			stats = &APIKeyDayStats{APIKeyID: keyID, APIKeyName: keyName}
			byKey[keyID] = stats
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

	byAPIKey := make([]APIKeyDayStats, 0, len(byKey))
	for _, stats := range byKey {
		byAPIKey = append(byAPIKey, *stats)
	}
	sort.Slice(byAPIKey, func(i, j int) bool {
		if byAPIKey[i].RequestCount == byAPIKey[j].RequestCount {
			return byAPIKey[i].APIKeyName < byAPIKey[j].APIKeyName
		}
		return byAPIKey[i].RequestCount > byAPIKey[j].RequestCount
	})

	return PeriodStatsSnapshot{
		Period:     periodLabel,
		Total:      total,
		ByAPIKey:   byAPIKey,
		ByProvider: aggregateByProvider(logs, since),
	}
}

func (s *Store) PeriodStats(now time.Time, since time.Time, periodLabel string) PeriodStatsSnapshot {
	return s.periodStatsSince(since, periodLabel)
}

func (s *Store) UsageStats(now time.Time) UsageStatsSnapshot {
	localNow := now.Local()
	dayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, localNow.Location())
	return s.UsageStatsRange(now, dayStart, dayStart.Add(24*time.Hour))
}

// UsageStatsRangeForKeys computes usage stats restricted to the given API key IDs
// (per-user isolation for role=user).
//
// Totals / by-api-key / daily token counts come from persisted day-bucket
// counters (same source as admin stats), filtered by key ID — NOT from the
// in-memory request-log ring buffer. Using only s.logs made "today" look right
// while "yesterday" under-counted once older rows aged out of memory.
//
// Provider, model, protocol, status and timing use persisted per-key day
// dimensions. Only hourly details depend on retained request logs.
func (s *Store) UsageStatsRangeForKeys(now time.Time, from, to time.Time, keyIDs []string) UsageStatsSnapshot {
	localNow := now.Local()
	dayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, localNow.Location())
	monthStart := time.Date(localNow.Year(), localNow.Month(), 1, 0, 0, 0, 0, localNow.Location())
	if from.IsZero() {
		from = dayStart
	}
	if to.IsZero() || !to.After(from) {
		to = from.Add(24 * time.Hour)
	}
	keySet := make(map[string]struct{}, len(keyIDs))
	for _, id := range keyIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		keySet[id] = struct{}{}
	}

	s.mu.RLock()
	todayPeriod := periodStatsForKeysLocked(s.usageByDay, dayStart, time.Time{}, keySet, dayStart.Format("2006-01-02"))
	month := periodStatsForKeysLocked(s.usageByDay, monthStart, time.Time{}, keySet, monthStart.Format("2006-01"))
	rangeStats := periodStatsForKeysLocked(s.usageByDay, from, to, keySet, from.Format("2006-01-02")+" ~ "+to.Add(-time.Nanosecond).Format("2006-01-02"))
	daily := dailyStatsForKeysLocked(s.usageByDay, from, to, keySet)
	lastRequest := lastUsageRequestForKeysLocked(s.lastUsageRequest, s.logs, keySet, dayStart)

	selectedDays := keyDimensionDays(s.usageByDay, keySet)
	keyPeriodDimensions(selectedDays, dayStart, dayStart.AddDate(0, 0, 1), &todayPeriod)
	keyPeriodDimensions(selectedDays, monthStart, time.Time{}, &month)
	keyPeriodDimensions(selectedDays, from, to, &rangeStats)
	selectedStore := &Store{usageByDay: selectedDays}
	status := selectedStore.statusStatsInRangeLocked(from, to)
	for i := range daily {
		if day := selectedDays[daily[i].Date]; day != nil && daily[i].RequestCount > 0 {
			samples := day.status2xx + day.status4xx + day.status5xx + day.statusOther
			if samples > 0 {
				daily[i].AvgLatencyMs = day.latencySum / samples
			}
			if day.ttftCount > 0 {
				daily[i].AvgTTFTMs = day.ttftSum / day.ttftCount
			}
		}
	}

	// Hourly details remain bounded by request-log retention.
	logs := make([]RequestLog, 0)
	if len(keySet) > 0 {
		for _, item := range s.logs {
			if _, ok := keySet[item.APIKeyID]; ok {
				logs = append(logs, item)
			}
		}
	}
	s.mu.RUnlock()

	today := TodayStatsSnapshot{
		Date:                dayStart.Format("2006-01-02"),
		Total:               todayPeriod.Total,
		LastRequest:         lastRequest,
		ByAPIKey:            todayPeriod.ByAPIKey,
		ByProvider:          todayPeriod.ByProvider,
		ByModel:             todayPeriod.ByModel,
		ByDownstreamGateway: todayPeriod.ByDownstreamGateway,
		ByUser:              todayPeriod.ByUser,
		ByProtocol:          todayPeriod.ByProtocol,
	}

	return UsageStatsSnapshot{
		Today:  today,
		Month:  month,
		Range:  &rangeStats,
		From:   from.Format(time.RFC3339),
		To:     to.Format(time.RFC3339),
		Daily:  daily,
		Status: status,
		Hourly: AggregateHourly(logs, from, to),
	}
}

func filterLogsInRange(logs []RequestLog, from, to time.Time) []RequestLog {
	if len(logs) == 0 {
		return nil
	}
	out := make([]RequestLog, 0, len(logs))
	for i := range logs {
		log := logs[i]
		if !from.IsZero() && log.Time.Before(from) {
			continue
		}
		if !to.IsZero() && !log.Time.Before(to) {
			continue
		}
		out = append(out, log)
	}
	return out
}

func lastUsageRequestForKeysLocked(last *RequestLog, logs []RequestLog, keySet map[string]struct{}, dayStart time.Time) *RequestLog {
	if last != nil && !last.Time.Before(dayStart) {
		if _, ok := keySet[last.APIKeyID]; ok {
			copied := *last
			return &copied
		}
	}
	for i := range logs {
		log := logs[i]
		if log.Time.Before(dayStart) {
			continue
		}
		if _, ok := keySet[log.APIKeyID]; !ok {
			continue
		}
		copied := log
		return &copied
	}
	return nil
}

func (s *Store) UsageStatsRange(now time.Time, from, to time.Time) UsageStatsSnapshot {
	localNow := now.Local()
	dayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, localNow.Location())
	monthStart := time.Date(localNow.Year(), localNow.Month(), 1, 0, 0, 0, 0, localNow.Location())
	if from.IsZero() {
		from = dayStart
	}
	if to.IsZero() || !to.After(from) {
		to = from.Add(24 * time.Hour)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	today := s.todayStatsFromCountersLocked(dayStart)
	month := s.periodStatsSinceLocked(monthStart, monthStart.Format("2006-01"))
	rangeStats := s.periodStatsInRangeLocked(from, to, from.Format("2006-01-02")+" ~ "+to.Add(-time.Nanosecond).Format("2006-01-02"))
	return UsageStatsSnapshot{
		Today:  today,
		Month:  month,
		Range:  &rangeStats,
		From:   from.Format(time.RFC3339),
		To:     to.Format(time.RFC3339),
		Daily:  s.dailyStatsInRangeLocked(from, to),
		Status: s.statusStatsInRangeLocked(from, to),
		Hourly: s.hourlyStatsInRangeLocked(from, to),
	}
}

func AggregateLogsInRange(logs []RequestLog, from, to time.Time, periodLabel string) PeriodStatsSnapshot {
	total := APIKeyDayStats{APIKeyName: "全部"}
	byKey := map[string]*APIKeyDayStats{}
	filtered := make([]RequestLog, 0, len(logs))
	for index := range logs {
		log := logs[index]
		if (!from.IsZero() && log.Time.Before(from)) || (!to.IsZero() && !log.Time.Before(to)) {
			continue
		}
		filtered = append(filtered, log)
		total.RequestCount++
		total.InputTokens += log.InputTokens
		total.OutputTokens += log.OutputTokens
		total.CacheTokens += log.CacheTokens
		total.CacheWriteTokens += log.CacheWriteTokens
		total.CostMicros += log.CostMicros
		if log.Priced {
			total.PricedCount++
		}
		keyID := log.APIKeyID
		keyName := log.APIKeyName
		if keyID == "" {
			keyID = "_anonymous"
			keyName = "未绑定 Key"
		}
		stats, ok := byKey[keyID]
		if !ok {
			stats = &APIKeyDayStats{APIKeyID: keyID, APIKeyName: keyName}
			byKey[keyID] = stats
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
	byAPIKey := make([]APIKeyDayStats, 0, len(byKey))
	for _, stats := range byKey {
		byAPIKey = append(byAPIKey, *stats)
	}
	sort.Slice(byAPIKey, func(i, j int) bool {
		if byAPIKey[i].RequestCount == byAPIKey[j].RequestCount {
			return byAPIKey[i].APIKeyName < byAPIKey[j].APIKeyName
		}
		return byAPIKey[i].RequestCount > byAPIKey[j].RequestCount
	})
	return PeriodStatsSnapshot{
		Period:              periodLabel,
		Total:               total,
		ByAPIKey:            byAPIKey,
		ByProvider:          aggregateByProvider(filtered, time.Time{}),
		ByModel:             aggregateByModel(filtered, time.Time{}),
		ByDownstreamGateway: aggregateDownstreamGatewayStats(filtered, time.Time{}, time.Time{}),
		ByProtocol:          aggregateByProtocol(filtered, time.Time{}),
	}
}

// aggregateDownstreamGatewayStats 从请求明细还原下游网关用量。它只在日志回放
// / 明细补算路径使用，长期统计走持久化的 downstream_gateway 桶；两边共用
// 同一套 ingress 判定，避免“今天”和“本月”口径不同。
func aggregateDownstreamGatewayStats(logs []RequestLog, from, to time.Time) []DownstreamGatewayDayStats {
	byGateway := make(map[string]*DownstreamGatewayDayStats)
	for _, log := range logs {
		if (!from.IsZero() && log.Time.Before(from)) || (!to.IsZero() && !log.Time.Before(to)) {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(log.PeerDirection), "ingress") {
			continue
		}
		gatewayID := strings.TrimSpace(log.PeerGatewayID)
		if gatewayID == "" {
			continue
		}
		stats := byGateway[gatewayID]
		if stats == nil {
			stats = &DownstreamGatewayDayStats{GatewayID: gatewayID}
			byGateway[gatewayID] = stats
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
	return sortDownstreamGatewayStats(byGateway)
}

func AggregateDaily(logs []RequestLog, from, to time.Time) []DailyRequestPoint {
	loc := from.Location()
	byDay := map[string]*DailyRequestPoint{}
	latencySum := map[string]int64{}
	ttftSum := map[string]int64{}
	ttftCount := map[string]int64{}
	for day := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc); day.Before(to); day = day.AddDate(0, 0, 1) {
		label := day.Format("2006-01-02")
		byDay[label] = &DailyRequestPoint{Date: label}
	}
	for index := range logs {
		log := logs[index]
		if (!from.IsZero() && log.Time.Before(from)) || (!to.IsZero() && !log.Time.Before(to)) {
			continue
		}
		label := log.Time.In(loc).Format("2006-01-02")
		point, ok := byDay[label]
		if !ok {
			point = &DailyRequestPoint{Date: label}
			byDay[label] = point
		}
		point.RequestCount++
		point.InputTokens += log.InputTokens
		point.OutputTokens += log.OutputTokens
		point.CacheTokens += log.CacheTokens
		point.CacheWriteTokens += log.CacheWriteTokens
		point.CostMicros += log.CostMicros
		if log.Priced {
			point.PricedCount++
		}
		latencySum[label] += log.LatencyMillis
		if log.TTFTMillis > 0 {
			ttftSum[label] += log.TTFTMillis
			ttftCount[label]++
		}
	}
	out := make([]DailyRequestPoint, 0, len(byDay))
	for _, point := range byDay {
		if point.RequestCount > 0 {
			point.AvgLatencyMs = latencySum[point.Date] / point.RequestCount
			if ttftCount[point.Date] > 0 {
				point.AvgTTFTMs = ttftSum[point.Date] / ttftCount[point.Date]
			}
		}
		out = append(out, *point)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

func AggregateStatus(logs []RequestLog, from, to time.Time) []StatusBucketStats {
	buckets := map[string]int64{"2xx": 0, "4xx": 0, "5xx": 0, "other": 0}
	for index := range logs {
		log := logs[index]
		if (!from.IsZero() && log.Time.Before(from)) || (!to.IsZero() && !log.Time.Before(to)) {
			continue
		}
		switch {
		case log.Status >= 200 && log.Status < 300:
			buckets["2xx"]++
		case log.Status >= 400 && log.Status < 500:
			buckets["4xx"]++
		case log.Status >= 500 && log.Status < 600:
			buckets["5xx"]++
		default:
			buckets["other"]++
		}
	}
	order := []string{"2xx", "4xx", "5xx", "other"}
	out := make([]StatusBucketStats, 0, len(order))
	for _, class := range order {
		out = append(out, StatusBucketStats{Class: class, RequestCount: buckets[class]})
	}
	return out
}

// AggregateHourly aggregates request counts and token usage by hour of the day
// (0..23) within the provided [from, to) time window.
func AggregateHourly(logs []RequestLog, from, to time.Time) []HourlyBucketStats {
	buckets := make([]HourlyBucketStats, 24)
	for i := 0; i < 24; i++ {
		buckets[i].Hour = i
	}
	loc := time.Local
	if !from.IsZero() {
		loc = from.Location()
	}
	for i := range logs {
		log := logs[i]
		if !from.IsZero() && log.Time.Before(from) {
			continue
		}
		if !to.IsZero() && !log.Time.Before(to) {
			continue
		}
		h := log.Time.In(loc).Hour()
		if h >= 0 && h < 24 {
			buckets[h].RequestCount++
			buckets[h].InputTokens += log.InputTokens
			buckets[h].OutputTokens += log.OutputTokens
		}
	}
	return buckets
}

func (s *Store) hourlyStatsInRangeLocked(from, to time.Time) []HourlyBucketStats {
	return AggregateHourly(s.logs, from, to)
}

func (s *Store) TodayStatsFromLogs(logs []RequestLog, now time.Time) TodayStatsSnapshot {
	localNow := now.Local()
	dayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, localNow.Location())
	dateLabel := dayStart.Format("2006-01-02")

	total := APIKeyDayStats{APIKeyID: "", APIKeyName: "全部"}
	byKey := map[string]*APIKeyDayStats{}
	var lastRequest *RequestLog

	for index := range logs {
		log := logs[index]
		if log.Time.Before(dayStart) {
			continue
		}
		if lastRequest == nil {
			copied := log
			lastRequest = &copied
		}

		total.RequestCount++
		total.InputTokens += log.InputTokens
		total.OutputTokens += log.OutputTokens
		total.CacheTokens += log.CacheTokens
		total.CacheWriteTokens += log.CacheWriteTokens
		total.CostMicros += log.CostMicros
		if log.Priced {
			total.PricedCount++
		}

		keyID := log.APIKeyID
		keyName := log.APIKeyName
		if keyID == "" {
			keyID = "_anonymous"
			keyName = "未绑定 Key"
		}
		stats, ok := byKey[keyID]
		if !ok {
			stats = &APIKeyDayStats{APIKeyID: keyID, APIKeyName: keyName}
			byKey[keyID] = stats
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

	byAPIKey := make([]APIKeyDayStats, 0, len(byKey))
	for _, stats := range byKey {
		byAPIKey = append(byAPIKey, *stats)
	}
	sort.Slice(byAPIKey, func(i, j int) bool {
		if byAPIKey[i].RequestCount == byAPIKey[j].RequestCount {
			return byAPIKey[i].APIKeyName < byAPIKey[j].APIKeyName
		}
		return byAPIKey[i].RequestCount > byAPIKey[j].RequestCount
	})

	return TodayStatsSnapshot{
		Date:                dateLabel,
		Total:               total,
		LastRequest:         lastRequest,
		ByAPIKey:            byAPIKey,
		ByProvider:          aggregateByProvider(logs, dayStart),
		ByDownstreamGateway: aggregateDownstreamGatewayStats(logs, dayStart, time.Time{}),
	}
}

func (s *Store) TodayStats(now time.Time) TodayStatsSnapshot {
	return s.todayStatsFromCounters(now)
}

func (s *Store) AddApp(level string, message string, context string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !levelEnabled(level, s.logLevel) {
		return
	}
	// 上游错误、OAuth 续期失败、工具输出都可能回显凭据；在唯一的入口处抹掉，
	// 新加的日志行不会漏。
	message = scrubAppLogText(message)
	context = scrubAppLogText(context)
	s.appLogs = append([]AppLog{{Time: time.Now(), Level: level, Message: message, Context: context}}, s.appLogs...)
	if len(s.appLogs) > 500 {
		s.appLogs = s.appLogs[:500]
	}
}

func (s *Store) ListApp(limit int) []AppLog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.appLogs) {
		limit = len(s.appLogs)
	}
	out := make([]AppLog, limit)
	copy(out, s.appLogs[:limit])
	return out
}

func (s *Store) SetLevel(level string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch level {
	case "debug", "info", "warn", "error":
		s.logLevel = level
	default:
		s.logLevel = "info"
	}
	return s.logLevel
}

func (s *Store) Level() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.logLevel
}

func levelEnabled(level string, threshold string) bool {
	return levelRank(level) >= levelRank(threshold)
}

func levelRank(level string) int {
	switch level {
	case "debug":
		return 10
	case "info":
		return 20
	case "warn":
		return 30
	case "error":
		return 40
	default:
		return 20
	}
}
