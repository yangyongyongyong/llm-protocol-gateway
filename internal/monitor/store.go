package monitor

import (
	"sort"
	"strings"
	"sync"
	"time"
)

type RequestLog struct {
	Time             time.Time `json:"time"`
	APIKeyID         string    `json:"apiKeyId,omitempty"`
	APIKeyName       string    `json:"apiKeyName,omitempty"`
	// UserName is a transient display field resolved at query time from the
	// owning API key's user id (not persisted). Renames reflect immediately.
	UserName         string    `json:"userName,omitempty"`
	RouteID          string    `json:"routeId"`
	ProviderID       string    `json:"providerId"`
	Model            string    `json:"model"`
	Action           string    `json:"action"`
	ProtocolFlow     string    `json:"protocolFlow"`
	Path             string    `json:"path"`
	Status           int       `json:"status"`
	InputTokens      int64     `json:"inputTokens"`  // prompt total (inclusive of cache)
	OutputTokens     int64     `json:"outputTokens"`
	CacheTokens      int64     `json:"cacheTokens"`  // cache-read/hit portion only
	LatencyMillis    int64     `json:"latencyMs"`
	TTFTMillis       int64     `json:"ttftMs,omitempty"`
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
	ErrorDescription      string `json:"errorDescription,omitempty"`
	RequestBody           string `json:"requestBody,omitempty"`
	ResponseBody          string `json:"responseBody,omitempty"`
}

const (
	AccessSourceLAN    = "lan"
	AccessSourcePublic = "public"
	AccessSourceLocal  = "local"
)

// RequestLogQuery filters and pages request logs.
type RequestLogQuery struct {
	From          time.Time
	To            time.Time
	Status        string // all | 2xx | 4xx | 5xx
	APIKeyName    string // substring match against api_key_name (case-insensitive)
	// APIKeyIDs restricts results to logs whose api_key_id is in this set.
	// Used for per-user data isolation; nil means no restriction.
	APIKeyIDs     []string
	Page          int
	PageSize      int
	IncludeBodies bool // list views should omit heavy request/response bodies
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
}

type ProviderDayStats struct {
	ProviderID   string `json:"providerId"`
	RequestCount int64  `json:"requestCount"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	CacheTokens  int64  `json:"cacheTokens"`
}

// UserDayStats aggregates token/request usage for one owner user id. UserID is
// stable (keys are bucketed by their OwnerUserID); UserName is resolved for
// display at query time so renames never corrupt historical aggregates.
type UserDayStats struct {
	UserID       string `json:"userId"`
	UserName     string `json:"userName"`
	RequestCount int64  `json:"requestCount"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	CacheTokens  int64  `json:"cacheTokens"`
}

// ModelDayStats aggregates request/token usage for one model id.
type ModelDayStats struct {
	Model        string `json:"model"`
	RequestCount int64  `json:"requestCount"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	CacheTokens  int64  `json:"cacheTokens"`
}

type TodayStatsSnapshot struct {
	Date        string             `json:"date"`
	Total       APIKeyDayStats     `json:"total"`
	LastRequest *RequestLog        `json:"lastRequest,omitempty"`
	ByAPIKey    []APIKeyDayStats   `json:"byApiKey"`
	ByProvider  []ProviderDayStats `json:"byProvider"`
	ByModel     []ModelDayStats    `json:"byModel"`
	ByUser      []UserDayStats     `json:"byUser,omitempty"`
}

type PeriodStatsSnapshot struct {
	Period     string             `json:"period"`
	Total      APIKeyDayStats     `json:"total"`
	ByAPIKey   []APIKeyDayStats   `json:"byApiKey"`
	ByProvider []ProviderDayStats `json:"byProvider"`
	ByModel    []ModelDayStats    `json:"byModel"`
	ByUser     []UserDayStats     `json:"byUser,omitempty"`
}

type DailyRequestPoint struct {
	Date         string `json:"date"`
	RequestCount int64  `json:"requestCount"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	CacheTokens  int64  `json:"cacheTokens"`
	AvgLatencyMs int64  `json:"avgLatencyMs,omitempty"`
	AvgTTFTMs    int64  `json:"avgTtftMs,omitempty"`
}

type StatusBucketStats struct {
	Class        string `json:"class"` // 2xx | 4xx | 5xx | other
	RequestCount int64  `json:"requestCount"`
}

type UsageStatsSnapshot struct {
	Today   TodayStatsSnapshot  `json:"today"`
	Month   PeriodStatsSnapshot `json:"month"`
	Range   *PeriodStatsSnapshot `json:"range,omitempty"`
	From    string              `json:"from,omitempty"`
	To      string              `json:"to,omitempty"`
	Daily   []DailyRequestPoint `json:"daily,omitempty"`
	Status  []StatusBucketStats `json:"status,omitempty"`
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
		if keyIDSet != nil {
			if _, ok := keyIDSet[item.APIKeyID]; !ok {
				continue
			}
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
	case "4xx":
		return status >= 400 && status < 500
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

// UsageStatsRangeForKeys computes usage stats from the in-memory log list,
// restricted to the given API key IDs (per-user isolation for role=user).
// It bypasses the day-bucket counters because those are not keyed per user.
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
		keySet[id] = struct{}{}
	}

	s.mu.RLock()
	logs := make([]RequestLog, 0, len(s.logs))
	for _, item := range s.logs {
		if _, ok := keySet[item.APIKeyID]; ok {
			logs = append(logs, item)
		}
	}
	s.mu.RUnlock()

	today := s.TodayStatsFromLogs(logs, now)
	month := aggregateLogsSince(logs, monthStart, monthStart.Format("2006-01"))
	rangeStats := aggregateLogsInRange(logs, from, to, from.Format("2006-01-02")+" ~ "+to.Add(-time.Nanosecond).Format("2006-01-02"))
	return UsageStatsSnapshot{
		Today:  today,
		Month:  month,
		Range:  &rangeStats,
		From:   from.Format(time.RFC3339),
		To:     to.Format(time.RFC3339),
		Daily:  aggregateDaily(logs, from, to),
		Status: aggregateStatus(logs, from, to),
	}
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
	}
}

func aggregateLogsInRange(logs []RequestLog, from, to time.Time, periodLabel string) PeriodStatsSnapshot {
	total := APIKeyDayStats{APIKeyName: "全部"}
	byKey := map[string]*APIKeyDayStats{}
	filtered := make([]RequestLog, 0, len(logs))
	for index := range logs {
		log := logs[index]
		if log.Time.Before(from) || !log.Time.Before(to) {
			continue
		}
		filtered = append(filtered, log)
		total.RequestCount++
		total.InputTokens += log.InputTokens
		total.OutputTokens += log.OutputTokens
		total.CacheTokens += log.CacheTokens
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
		ByProvider: aggregateByProvider(filtered, time.Time{}),
	}
}

func aggregateDaily(logs []RequestLog, from, to time.Time) []DailyRequestPoint {
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
		if log.Time.Before(from) || !log.Time.Before(to) {
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

func aggregateStatus(logs []RequestLog, from, to time.Time) []StatusBucketStats {
	buckets := map[string]int64{"2xx": 0, "4xx": 0, "5xx": 0, "other": 0}
	for index := range logs {
		log := logs[index]
		if log.Time.Before(from) || !log.Time.Before(to) {
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
		Date:        dateLabel,
		Total:       total,
		LastRequest: lastRequest,
		ByAPIKey:    byAPIKey,
		ByProvider:  aggregateByProvider(logs, dayStart),
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
