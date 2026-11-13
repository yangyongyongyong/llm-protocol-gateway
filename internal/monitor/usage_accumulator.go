package monitor

import (
	"context"
	"sort"
	"strings"
	"time"
)

const usageEventBuffer = 4096

// Defaults for the async usage worker's batching. It commits one
// usage_daily_buckets transaction per batch instead of one per request, cutting
// WAL fsyncs (and page-alignment waste) under sustained load.
//
// The wait window also bounds the worst-case data-loss window on an ungraceful
// kill: the request itself already completed and returned to the client, so only
// the usage dashboard's numbers are at stake, never a proxied response. Graceful
// shutdown drains the queue (see Store.CloseUsageWorker).
//
// Both are configurable — see GatewayState.UsageBatchMaxSize / UsageBatchMaxWaitSeconds.
const (
	defaultUsageBatchMaxSize = 500
	defaultUsageBatchMaxWait = time.Minute
	// usageBatchMaxWaitCap keeps a misconfigured value from parking usage
	// numbers in memory for hours.
	usageBatchMaxWaitCap = time.Hour
	usageBatchMaxSizeCap = 10000
)

// NormalizeUsageBatchConfig clamps configured values, falling back to the
// defaults for zero/negative input. Exported so the HTTP handler stores exactly
// what the worker will run with.
func NormalizeUsageBatchConfig(maxSize int, maxWait time.Duration) (int, time.Duration) {
	if maxSize <= 0 {
		maxSize = defaultUsageBatchMaxSize
	}
	if maxSize > usageBatchMaxSizeCap {
		maxSize = usageBatchMaxSizeCap
	}
	if maxWait <= 0 {
		maxWait = defaultUsageBatchMaxWait
	}
	if maxWait > usageBatchMaxWaitCap {
		maxWait = usageBatchMaxWaitCap
	}
	return maxSize, maxWait
}

// SetUsageBatchConfig overrides the batching thresholds. Must be called before
// the worker starts (i.e. before the first EnqueueUsage) — the worker reads
// these once at startup, so a later change would race with it.
func (s *Store) SetUsageBatchConfig(maxSize int, maxWait time.Duration) {
	maxSize, maxWait = NormalizeUsageBatchConfig(maxSize, maxWait)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usageBatchMaxSize = maxSize
	s.usageBatchMaxWait = maxWait
}

func (s *Store) usageBatchConfig() (int, time.Duration) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return NormalizeUsageBatchConfig(s.usageBatchMaxSize, s.usageBatchMaxWait)
}

// UsageEvent is a lightweight usage record queued after each request completes.
//
// InputTokens is the normalized prompt total (PromptTotal): full prompt tokens
// INCLUDING cache hits. CacheTokens is cache-read/hit tokens only (PromptCache).
// Hit rate = CacheTokens / InputTokens. Legacy rows may store exclusive prompt
// counts; applyUsageEvent repairs those via normalizeUsageTokens.
type UsageEvent struct {
	Time           time.Time
	APIKeyID       string
	APIKeyName     string
	UserID         string
	ProviderID     string
	Model          string
	OutputProtocol string
	Status         int
	InputTokens    int64
	OutputTokens   int64
	CacheTokens    int64
	// CacheWriteTokens is the cache-creation part already inside InputTokens;
	// CostMicros is what the gateway's price table says the request cost, in
	// micro US dollars, and 0 with no price row means 未计价 rather than free.
	CacheWriteTokens int64
	CostMicros       int64
	// Priced says a price row existed, so a cost of 0 can be told apart from an
	// unpriced request.
	Priced bool
	// RxBytes/TxBytes are the forwarded request/response payload sizes.
	RxBytes   int64
	TxBytes   int64
	LatencyMs int64
	TTFTMs    int64
	// PeerGatewayID/PeerDirection tag cross-gateway traffic. Only ingress rows
	// (a downstream gateway sending work into this gateway) enter the
	// downstream-gateway usage dimension.
	PeerGatewayID string
	PeerDirection string
}

type usageDayStats struct {
	total        APIKeyDayStats
	byKey        map[string]*usageDayStats
	byAPIKey     map[string]*APIKeyDayStats
	byProvider   map[string]*ProviderDayStats
	byModel      map[string]*ModelDayStats
	byUser       map[string]*UserDayStats
	byProtocol   map[string]*ProtocolDayStats
	byDownstream map[string]*DownstreamGatewayDayStats
	status2xx    int64
	status4xx    int64
	status5xx    int64
	statusOther  int64
	latencySum   int64
	ttftSum      int64
	ttftCount    int64
}

func newUsageDayStats() *usageDayStats {
	return &usageDayStats{
		total:        APIKeyDayStats{APIKeyName: "全部"},
		byKey:        make(map[string]*usageDayStats),
		byAPIKey:     make(map[string]*APIKeyDayStats),
		byProvider:   make(map[string]*ProviderDayStats),
		byModel:      make(map[string]*ModelDayStats),
		byUser:       make(map[string]*UserDayStats),
		byProtocol:   make(map[string]*ProtocolDayStats),
		byDownstream: make(map[string]*DownstreamGatewayDayStats),
	}
}

func (s *Store) startUsageWorker() {
	s.usageOnce.Do(func() {
		s.usageEvents = make(chan UsageEvent, usageEventBuffer)
		s.usageStop = make(chan struct{})
		s.usageStopped = make(chan struct{})
		go s.usageWorker()
	})
}

// CloseUsageWorker persists whatever the worker has queued and waits for it to
// finish. Safe to call when the worker never started, and safe to call twice.
// Graceful shutdown uses CloseUsageWorkerContext, which bounds that wait.
//
// Deliberately signals via a stop channel rather than closing usageEvents:
// EnqueueUsage runs on request goroutines, and a send on a closed channel would
// panic if a request landed mid-shutdown.
func (s *Store) CloseUsageWorker() {
	_ = s.CloseUsageWorkerContext(context.Background())
}

// CloseUsageWorkerContext is CloseUsageWorker with a deadline on the wait. The
// worker's flush commits through the single SQLite writer connection, which a
// long maintenance statement can hold for minutes; shutting down must not wait
// that out, or systemd SIGKILLs the process and the restart loses far more than
// this batch.
func (s *Store) CloseUsageWorkerContext(ctx context.Context) error {
	s.mu.RLock()
	stop := s.usageStop
	stopped := s.usageStopped
	s.mu.RUnlock()
	if stop == nil || stopped == nil {
		return nil
	}
	s.usageCloseOnce.Do(func() { close(stop) })
	select {
	case <-stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) usageWorker() {
	defer close(s.usageStopped)

	maxSize, maxWait := s.usageBatchConfig()
	batch := make([]UsagePersistDelta, 0, maxSize)
	timer := time.NewTimer(maxWait)
	defer timer.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		s.persistUsageDeltas(batch)
		batch = batch[:0]
	}

	for {
		select {
		case event := <-s.usageEvents:
			batch = append(batch, s.applyUsageEventLocked(event))
			if len(batch) >= maxSize {
				flush()
				timer.Reset(maxWait)
			}
		case <-timer.C:
			flush()
			timer.Reset(maxWait)
		case <-s.usageStop:
			// Drain what producers already queued, then persist and exit.
			for {
				select {
				case event := <-s.usageEvents:
					batch = append(batch, s.applyUsageEventLocked(event))
				default:
					flush()
					return
				}
			}
		}
	}
}

// EnqueueUsage records usage asynchronously so request forwarding is not blocked.
func (s *Store) EnqueueUsage(event UsageEvent) {
	s.startUsageWorker()
	select {
	case s.usageEvents <- event:
	default:
		go func() { s.usageEvents <- event }()
	}
}

// ApplyUsageEventSync applies one usage event immediately (startup rebuild / tests).
func (s *Store) ApplyUsageEventSync(event UsageEvent) UsagePersistDelta {
	delta := s.applyUsageEventLocked(event)
	s.persistUsageDelta(delta)
	return delta
}

func (s *Store) applyUsageEventLocked(event UsageEvent) UsagePersistDelta {
	dayKey := event.Time.Local().Format("2006-01-02")
	keyID, keyName := normalizeAPIKeyForStats(event.APIKeyID, event.APIKeyName)
	providerID := normalizeProviderForStats(event.ProviderID)
	userID := normalizeUserForStats(event.UserID)
	inputTokens, cacheTokens := normalizeUsageTokens(event.InputTokens, event.CacheTokens)
	cacheWriteTokens := event.CacheWriteTokens
	if cacheWriteTokens < 0 {
		cacheWriteTokens = 0
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	day, ok := s.usageByDay[dayKey]
	if !ok {
		day = newUsageDayStats()
		s.usageByDay[dayKey] = day
	}

	day.total.RequestCount++
	day.total.InputTokens += inputTokens
	day.total.OutputTokens += event.OutputTokens
	day.total.CacheTokens += cacheTokens
	day.total.CacheWriteTokens += cacheWriteTokens
	day.total.CostMicros += event.CostMicros
	if event.Priced {
		day.total.PricedCount++
	}
	day.total.RxBytes += event.RxBytes
	day.total.TxBytes += event.TxBytes
	day.latencySum += event.LatencyMs

	keyStats, ok := day.byAPIKey[keyID]
	if !ok {
		keyStats = &APIKeyDayStats{APIKeyID: keyID, APIKeyName: keyName}
		day.byAPIKey[keyID] = keyStats
	}
	keyStats.RequestCount++
	keyStats.InputTokens += inputTokens
	keyStats.OutputTokens += event.OutputTokens
	keyStats.CacheTokens += cacheTokens
	keyStats.CacheWriteTokens += cacheWriteTokens
	keyStats.CostMicros += event.CostMicros
	if event.Priced {
		keyStats.PricedCount++
	}
	keyStats.RxBytes += event.RxBytes
	keyStats.TxBytes += event.TxBytes

	providerStats, ok := day.byProvider[providerID]
	if !ok {
		providerStats = &ProviderDayStats{ProviderID: providerID}
		day.byProvider[providerID] = providerStats
	}
	providerStats.RequestCount++
	providerStats.InputTokens += inputTokens
	providerStats.OutputTokens += event.OutputTokens
	providerStats.CacheTokens += cacheTokens
	providerStats.CacheWriteTokens += cacheWriteTokens
	providerStats.CostMicros += event.CostMicros
	if event.Priced {
		providerStats.PricedCount++
	}

	if gatewayID := strings.TrimSpace(event.PeerGatewayID); gatewayID != "" && strings.EqualFold(strings.TrimSpace(event.PeerDirection), "ingress") {
		downstream, ok := day.byDownstream[gatewayID]
		if !ok {
			downstream = &DownstreamGatewayDayStats{GatewayID: gatewayID}
			day.byDownstream[gatewayID] = downstream
		}
		downstream.RequestCount++
		downstream.InputTokens += inputTokens
		downstream.OutputTokens += event.OutputTokens
		downstream.CacheTokens += cacheTokens
		downstream.CacheWriteTokens += cacheWriteTokens
		downstream.CostMicros += event.CostMicros
		if event.Priced {
			downstream.PricedCount++
		}
	}

	userStats, ok := day.byUser[userID]
	if !ok {
		userStats = &UserDayStats{UserID: userID}
		day.byUser[userID] = userStats
	}
	userStats.RequestCount++
	userStats.InputTokens += inputTokens
	userStats.OutputTokens += event.OutputTokens
	userStats.CacheTokens += cacheTokens
	userStats.CacheWriteTokens += cacheWriteTokens
	userStats.CostMicros += event.CostMicros
	if event.Priced {
		userStats.PricedCount++
	}

	if model, ok := NormalizeModelForStats(event.Model); ok {
		modelStats, ok := day.byModel[model]
		if !ok {
			modelStats = &ModelDayStats{Model: model}
			day.byModel[model] = modelStats
		}
		modelStats.RequestCount++
		modelStats.InputTokens += inputTokens
		modelStats.OutputTokens += event.OutputTokens
		modelStats.CacheTokens += cacheTokens
		modelStats.CacheWriteTokens += cacheWriteTokens
		modelStats.CostMicros += event.CostMicros
		if event.Priced {
			modelStats.PricedCount++
		}
	}

	event.OutputProtocol = strings.TrimSpace(event.OutputProtocol)
	recordKeyUsage(day, keyID, event, inputTokens, cacheTokens)
	outputProtocol := event.OutputProtocol
	if outputProtocol != "" {
		protoStats, ok := day.byProtocol[outputProtocol]
		if !ok {
			protoStats = &ProtocolDayStats{Protocol: outputProtocol}
			day.byProtocol[outputProtocol] = protoStats
		}
		protoStats.RequestCount++
		protoStats.InputTokens += inputTokens
		protoStats.OutputTokens += event.OutputTokens
		protoStats.CacheTokens += cacheTokens
		protoStats.CacheWriteTokens += cacheWriteTokens
		protoStats.CostMicros += event.CostMicros
		if event.Priced {
			protoStats.PricedCount++
		}
	}

	switch statusBucket(event.Status) {
	case "2xx":
		day.status2xx++
	case "4xx":
		day.status4xx++
	case "5xx":
		day.status5xx++
	default:
		day.statusOther++
	}

	if event.TTFTMs > 0 {
		day.ttftSum += event.TTFTMs
		day.ttftCount++
	}

	if s.lastUsageRequest == nil || event.Time.After(s.lastUsageRequest.Time) {
		s.lastUsageRequest = &RequestLog{
			Time:          event.Time,
			APIKeyID:      event.APIKeyID,
			APIKeyName:    event.APIKeyName,
			ProviderID:    event.ProviderID,
			Model:         event.Model,
			Status:        event.Status,
			InputTokens:   inputTokens,
			OutputTokens:  event.OutputTokens,
			CacheTokens:   cacheTokens,
			CostMicros:    event.CostMicros,
			Priced:        event.Priced,
			LatencyMillis: event.LatencyMs,
			TTFTMillis:    event.TTFTMs,
		}
	}

	modelForBucket := ""
	if model, ok := NormalizeModelForStats(event.Model); ok {
		modelForBucket = model
	}
	var lastCopy *RequestLog
	if s.lastUsageRequest != nil {
		copied := *s.lastUsageRequest
		lastCopy = &copied
	}
	return UsagePersistDelta{
		Day:            dayKey,
		KeyID:          keyID,
		KeyName:        keyName,
		UserID:         userID,
		ProviderID:     providerID,
		Model:          modelForBucket,
		OutputProtocol: outputProtocol,
		StatusClass:    statusBucket(event.Status),
		PeerGatewayID: func() string {
			if strings.EqualFold(strings.TrimSpace(event.PeerDirection), "ingress") {
				return strings.TrimSpace(event.PeerGatewayID)
			}
			return ""
		}(),
		InputTokens:  inputTokens,
		OutputTokens: event.OutputTokens,
		CacheTokens:  cacheTokens,
		// Cost is carried per request, and PricedCount says whether it counted:
		// a day's cost sum alone cannot tell "cheap" from "mostly unpriced".
		CacheWriteTokens: cacheWriteTokens,
		CostMicros:       event.CostMicros,
		PricedCount:      pricedIncrement(event.Priced),
		RxBytes:          event.RxBytes,
		TxBytes:          event.TxBytes,
		LatencyMs:        event.LatencyMs,
		TTFTMs:           event.TTFTMs,
		LastRequest:      lastCopy,
	}
}

func (s *Store) persistUsageDelta(delta UsagePersistDelta) {
	s.mu.RLock()
	store := s.usageDailyStore
	s.mu.RUnlock()
	if store == nil {
		return
	}
	if err := store.ApplyUsageDelta(delta); err != nil {
		s.AddApp("warn", "failed to persist usage daily aggregate", err.Error())
	}
}

// persistUsageDeltas is the batched counterpart used by usageWorker: one
// transaction/commit for the whole batch instead of one per delta.
func (s *Store) persistUsageDeltas(deltas []UsagePersistDelta) {
	if len(deltas) == 0 {
		return
	}
	s.mu.RLock()
	store := s.usageDailyStore
	s.mu.RUnlock()
	if store == nil {
		return
	}
	if err := store.ApplyUsageDeltas(deltas); err != nil {
		s.AddApp("warn", "failed to persist usage daily aggregate batch", err.Error())
	}
}

// ResetUsageStats clears incremental usage counters.
func (s *Store) ResetUsageStats() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usageByDay = make(map[string]*usageDayStats)
	s.lastUsageRequest = nil
}

func (s *Store) PruneUsageStatsBefore(cutoffDay time.Time) {
	// Kept for tests / rare admin tooling. Request-log retention must NOT call
	// this — daily usage aggregates are retained permanently.
	loc := cutoffDay.Location()
	cutoff := time.Date(cutoffDay.Year(), cutoffDay.Month(), cutoffDay.Day(), 0, 0, 0, 0, loc)
	s.mu.Lock()
	store := s.usageDailyStore
	for dayKey := range s.usageByDay {
		day, err := time.ParseInLocation("2006-01-02", dayKey, loc)
		if err != nil || day.Before(cutoff) {
			delete(s.usageByDay, dayKey)
		}
	}
	s.mu.Unlock()
	if store != nil {
		_ = store.PruneUsageBefore(cutoff)
	}
}

func normalizeAPIKeyForStats(id, name string) (string, string) {
	if strings.TrimSpace(id) == "" {
		return "_anonymous", "未绑定 Key"
	}
	if strings.TrimSpace(name) == "" {
		name = id
	}
	return id, name
}

func normalizeProviderForStats(id string) string {
	if strings.TrimSpace(id) == "" {
		return "_unknown"
	}
	return strings.TrimSpace(id)
}

// normalizeUserForStats keeps the owner user id stable for bucketing. Empty
// means the request had no owner-bound key (anonymous / local test).
func normalizeUserForStats(id string) string {
	if strings.TrimSpace(id) == "" {
		return "_anonymous"
	}
	return strings.TrimSpace(id)
}

func statusBucket(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500 && status < 600:
		return "5xx"
	default:
		return "other"
	}
}

func (s *Store) periodStatsSince(since time.Time, periodLabel string) PeriodStatsSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.periodStatsSinceLocked(since, periodLabel)
}

func (s *Store) periodStatsSinceLocked(since time.Time, periodLabel string) PeriodStatsSnapshot {
	total, byAPIKey, byProvider, byModel, byUser := mergeUsageDays(s.usageByDay, since, time.Time{})
	return PeriodStatsSnapshot{
		Period:              periodLabel,
		Total:               total,
		ByAPIKey:            sortAPIKeyStats(byAPIKey),
		ByProvider:          sortProviderStats(byProvider),
		ByModel:             sortModelDayStats(byModel),
		ByDownstreamGateway: sortDownstreamGatewayStats(mergeDownstreamGatewayDays(s.usageByDay, since, time.Time{})),
		ByUser:              sortUserStats(byUser),
		ByProtocol:          sortProtocolStats(mergeProtocolDays(s.usageByDay, since, time.Time{})),
	}
}

func (s *Store) periodStatsInRange(from, to time.Time, periodLabel string) PeriodStatsSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.periodStatsInRangeLocked(from, to, periodLabel)
}

func (s *Store) periodStatsInRangeLocked(from, to time.Time, periodLabel string) PeriodStatsSnapshot {
	total, byAPIKey, byProvider, byModel, byUser := mergeUsageDays(s.usageByDay, from, to)
	return PeriodStatsSnapshot{
		Period:              periodLabel,
		Total:               total,
		ByAPIKey:            sortAPIKeyStats(byAPIKey),
		ByProvider:          sortProviderStats(byProvider),
		ByModel:             sortModelDayStats(byModel),
		ByDownstreamGateway: sortDownstreamGatewayStats(mergeDownstreamGatewayDays(s.usageByDay, from, to)),
		ByUser:              sortUserStats(byUser),
		ByProtocol:          sortProtocolStats(mergeProtocolDays(s.usageByDay, from, to)),
	}
}

func (s *Store) todayStatsFromCounters(now time.Time) TodayStatsSnapshot {
	localNow := now.Local()
	dayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, localNow.Location())
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.todayStatsFromCountersLocked(dayStart)
}

func (s *Store) todayStatsFromCountersLocked(dayStart time.Time) TodayStatsSnapshot {
	total, byAPIKey, byProvider, byModel, byUser := mergeUsageDays(s.usageByDay, dayStart, time.Time{})
	var lastRequest *RequestLog
	if s.lastUsageRequest != nil && !s.lastUsageRequest.Time.Before(dayStart) {
		copied := *s.lastUsageRequest
		lastRequest = &copied
	}
	return TodayStatsSnapshot{
		Date:                dayStart.Format("2006-01-02"),
		Total:               total,
		LastRequest:         lastRequest,
		ByAPIKey:            sortAPIKeyStats(byAPIKey),
		ByProvider:          sortProviderStats(byProvider),
		ByModel:             sortModelDayStats(byModel),
		ByDownstreamGateway: sortDownstreamGatewayStats(mergeDownstreamGatewayDays(s.usageByDay, dayStart, time.Time{})),
		ByUser:              sortUserStats(byUser),
		ByProtocol:          sortProtocolStats(mergeProtocolDays(s.usageByDay, dayStart, time.Time{})),
	}
}

func (s *Store) dailyStatsInRange(from, to time.Time) []DailyRequestPoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dailyStatsInRangeLocked(from, to)
}

func (s *Store) dailyStatsInRangeLocked(from, to time.Time) []DailyRequestPoint {
	loc := from.Location()

	byDay := map[string]*DailyRequestPoint{}
	latencySum := map[string]int64{}
	ttftSum := map[string]int64{}
	ttftCount := map[string]int64{}
	for day := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc); day.Before(to); day = day.AddDate(0, 0, 1) {
		label := day.Format("2006-01-02")
		byDay[label] = &DailyRequestPoint{Date: label}
	}

	fromDay := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	toDay := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, loc)
	if to.Hour() > 0 || to.Minute() > 0 || to.Second() > 0 || to.Nanosecond() > 0 {
		toDay = toDay.AddDate(0, 0, 1)
	}
	for dayKey, dayStats := range s.usageByDay {
		day, err := time.ParseInLocation("2006-01-02", dayKey, loc)
		if err != nil || day.Before(fromDay) || !day.Before(toDay) {
			continue
		}
		point, ok := byDay[dayKey]
		if !ok {
			point = &DailyRequestPoint{Date: dayKey}
			byDay[dayKey] = point
		}
		point.RequestCount = dayStats.total.RequestCount
		point.InputTokens = dayStats.total.InputTokens
		point.OutputTokens = dayStats.total.OutputTokens
		point.CacheTokens = dayStats.total.CacheTokens
		point.CacheWriteTokens = dayStats.total.CacheWriteTokens
		point.CostMicros = dayStats.total.CostMicros
		point.PricedCount = dayStats.total.PricedCount
		point.RxBytes = dayStats.total.RxBytes
		point.TxBytes = dayStats.total.TxBytes
		latencySum[dayKey] = dayStats.latencySum
		ttftSum[dayKey] = dayStats.ttftSum
		ttftCount[dayKey] = dayStats.ttftCount
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

func (s *Store) statusStatsInRange(from, to time.Time) []StatusBucketStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.statusStatsInRangeLocked(from, to)
}

func (s *Store) statusStatsInRangeLocked(from, to time.Time) []StatusBucketStats {
	buckets := map[string]int64{"2xx": 0, "4xx": 0, "5xx": 0, "other": 0}
	fromDay := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, from.Location())
	toDay := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, from.Location())
	if to.Hour() > 0 || to.Minute() > 0 || to.Second() > 0 || to.Nanosecond() > 0 {
		toDay = toDay.AddDate(0, 0, 1)
	}
	for dayKey, dayStats := range s.usageByDay {
		day, err := time.ParseInLocation("2006-01-02", dayKey, from.Location())
		if err != nil || day.Before(fromDay) || !day.Before(toDay) {
			continue
		}
		buckets["2xx"] += dayStats.status2xx
		buckets["4xx"] += dayStats.status4xx
		buckets["5xx"] += dayStats.status5xx
		buckets["other"] += dayStats.statusOther
	}
	order := []string{"2xx", "4xx", "5xx", "other"}
	out := make([]StatusBucketStats, 0, len(order))
	for _, class := range order {
		out = append(out, StatusBucketStats{Class: class, RequestCount: buckets[class]})
	}
	return out
}

func mergeProtocolDays(byDay map[string]*usageDayStats, from, to time.Time) map[string]*ProtocolDayStats {
	loc := from.Location()
	if to.IsZero() {
		to = time.Date(9999, 1, 1, 0, 0, 0, 0, loc)
	}
	fromDay := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	toDay := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, loc)
	if to.Hour() > 0 || to.Minute() > 0 || to.Second() > 0 || to.Nanosecond() > 0 {
		toDay = toDay.AddDate(0, 0, 1)
	}
	byProtocol := make(map[string]*ProtocolDayStats)
	for dayKey, dayStats := range byDay {
		day, err := time.ParseInLocation("2006-01-02", dayKey, loc)
		if err != nil || day.Before(fromDay) || !day.Before(toDay) {
			continue
		}
		for id, stats := range dayStats.byProtocol {
			out, ok := byProtocol[id]
			if !ok {
				copied := *stats
				byProtocol[id] = &copied
				continue
			}
			out.RequestCount += stats.RequestCount
			out.InputTokens += stats.InputTokens
			out.OutputTokens += stats.OutputTokens
			out.CacheTokens += stats.CacheTokens
			out.CacheWriteTokens += stats.CacheWriteTokens
			out.CostMicros += stats.CostMicros
			out.PricedCount += stats.PricedCount
		}
	}
	return byProtocol
}

func mergeUsageDays(byDay map[string]*usageDayStats, from, to time.Time) (APIKeyDayStats, map[string]*APIKeyDayStats, map[string]*ProviderDayStats, map[string]*ModelDayStats, map[string]*UserDayStats) {
	loc := from.Location()
	if to.IsZero() {
		to = time.Date(9999, 1, 1, 0, 0, 0, 0, loc)
	}
	fromDay := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	toDay := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, loc)
	if to.Hour() > 0 || to.Minute() > 0 || to.Second() > 0 || to.Nanosecond() > 0 {
		toDay = toDay.AddDate(0, 0, 1)
	}

	total := APIKeyDayStats{APIKeyName: "全部"}
	byAPIKey := make(map[string]*APIKeyDayStats)
	byProvider := make(map[string]*ProviderDayStats)
	byModel := make(map[string]*ModelDayStats)
	byUser := make(map[string]*UserDayStats)

	for dayKey, dayStats := range byDay {
		day, err := time.ParseInLocation("2006-01-02", dayKey, loc)
		if err != nil || day.Before(fromDay) || !day.Before(toDay) {
			continue
		}
		total.RequestCount += dayStats.total.RequestCount
		total.InputTokens += dayStats.total.InputTokens
		total.OutputTokens += dayStats.total.OutputTokens
		total.CacheTokens += dayStats.total.CacheTokens
		total.CacheWriteTokens += dayStats.total.CacheWriteTokens
		total.CostMicros += dayStats.total.CostMicros
		total.PricedCount += dayStats.total.PricedCount

		for id, stats := range dayStats.byAPIKey {
			out, ok := byAPIKey[id]
			if !ok {
				copied := *stats
				out = &copied
				byAPIKey[id] = out
				continue
			}
			out.RequestCount += stats.RequestCount
			out.InputTokens += stats.InputTokens
			out.OutputTokens += stats.OutputTokens
			out.CacheTokens += stats.CacheTokens
			out.CacheWriteTokens += stats.CacheWriteTokens
			out.CostMicros += stats.CostMicros
			out.PricedCount += stats.PricedCount
		}
		for id, stats := range dayStats.byProvider {
			out, ok := byProvider[id]
			if !ok {
				copied := *stats
				out = &copied
				byProvider[id] = out
				continue
			}
			out.RequestCount += stats.RequestCount
			out.InputTokens += stats.InputTokens
			out.OutputTokens += stats.OutputTokens
			out.CacheTokens += stats.CacheTokens
			out.CacheWriteTokens += stats.CacheWriteTokens
			out.CostMicros += stats.CostMicros
			out.PricedCount += stats.PricedCount
		}
		for model, stats := range dayStats.byModel {
			out, ok := byModel[model]
			if !ok {
				copied := *stats
				out = &copied
				byModel[model] = out
				continue
			}
			out.RequestCount += stats.RequestCount
			out.InputTokens += stats.InputTokens
			out.OutputTokens += stats.OutputTokens
			out.CacheTokens += stats.CacheTokens
			out.CacheWriteTokens += stats.CacheWriteTokens
			out.CostMicros += stats.CostMicros
			out.PricedCount += stats.PricedCount
		}
		for id, stats := range dayStats.byUser {
			out, ok := byUser[id]
			if !ok {
				copied := *stats
				out = &copied
				byUser[id] = out
				continue
			}
			out.RequestCount += stats.RequestCount
			out.InputTokens += stats.InputTokens
			out.OutputTokens += stats.OutputTokens
			out.CacheTokens += stats.CacheTokens
			out.CacheWriteTokens += stats.CacheWriteTokens
			out.CostMicros += stats.CostMicros
			out.PricedCount += stats.PricedCount
		}
	}
	return total, byAPIKey, byProvider, byModel, byUser
}

// mergeUsageDaysForKeys sums only the by-api-key buckets that belong to keySet.
// Used for role=user stats so historical days match admin by-user / by-key views.
func mergeUsageDaysForKeys(byDay map[string]*usageDayStats, from, to time.Time, keySet map[string]struct{}) (APIKeyDayStats, map[string]*APIKeyDayStats) {
	loc := from.Location()
	if to.IsZero() {
		to = time.Date(9999, 1, 1, 0, 0, 0, 0, loc)
	}
	fromDay := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	toDay := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, loc)
	if to.Hour() > 0 || to.Minute() > 0 || to.Second() > 0 || to.Nanosecond() > 0 {
		toDay = toDay.AddDate(0, 0, 1)
	}

	total := APIKeyDayStats{APIKeyName: "全部"}
	byAPIKey := make(map[string]*APIKeyDayStats)
	if len(keySet) == 0 {
		return total, byAPIKey
	}

	for dayKey, dayStats := range byDay {
		day, err := time.ParseInLocation("2006-01-02", dayKey, loc)
		if err != nil || day.Before(fromDay) || !day.Before(toDay) {
			continue
		}
		for id, stats := range dayStats.byAPIKey {
			if _, ok := keySet[id]; !ok {
				continue
			}
			total.RequestCount += stats.RequestCount
			total.InputTokens += stats.InputTokens
			total.OutputTokens += stats.OutputTokens
			total.CacheTokens += stats.CacheTokens
			total.CacheWriteTokens += stats.CacheWriteTokens
			total.CostMicros += stats.CostMicros
			total.PricedCount += stats.PricedCount
			total.RxBytes += stats.RxBytes
			total.TxBytes += stats.TxBytes

			out, ok := byAPIKey[id]
			if !ok {
				copied := *stats
				byAPIKey[id] = &copied
				continue
			}
			out.RequestCount += stats.RequestCount
			out.InputTokens += stats.InputTokens
			out.OutputTokens += stats.OutputTokens
			out.CacheTokens += stats.CacheTokens
			out.CacheWriteTokens += stats.CacheWriteTokens
			out.CostMicros += stats.CostMicros
			out.PricedCount += stats.PricedCount
			out.RxBytes += stats.RxBytes
			out.TxBytes += stats.TxBytes
		}
	}
	return total, byAPIKey
}

func periodStatsForKeysLocked(byDay map[string]*usageDayStats, from, to time.Time, keySet map[string]struct{}, periodLabel string) PeriodStatsSnapshot {
	total, byAPIKey := mergeUsageDaysForKeys(byDay, from, to, keySet)
	return PeriodStatsSnapshot{
		Period:              periodLabel,
		Total:               total,
		ByAPIKey:            sortAPIKeyStats(byAPIKey),
		ByDownstreamGateway: sortDownstreamGatewayStats(mergeDownstreamGatewayDays(byDay, from, to)),
	}
}

func dailyStatsForKeysLocked(byDay map[string]*usageDayStats, from, to time.Time, keySet map[string]struct{}) []DailyRequestPoint {
	loc := from.Location()
	byDayOut := map[string]*DailyRequestPoint{}
	for day := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc); day.Before(to); day = day.AddDate(0, 0, 1) {
		label := day.Format("2006-01-02")
		byDayOut[label] = &DailyRequestPoint{Date: label}
	}
	if len(keySet) == 0 {
		out := make([]DailyRequestPoint, 0, len(byDayOut))
		for _, point := range byDayOut {
			out = append(out, *point)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
		return out
	}

	fromDay := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	toDay := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, loc)
	if to.Hour() > 0 || to.Minute() > 0 || to.Second() > 0 || to.Nanosecond() > 0 {
		toDay = toDay.AddDate(0, 0, 1)
	}
	for dayKey, dayStats := range byDay {
		day, err := time.ParseInLocation("2006-01-02", dayKey, loc)
		if err != nil || day.Before(fromDay) || !day.Before(toDay) {
			continue
		}
		point, ok := byDayOut[dayKey]
		if !ok {
			point = &DailyRequestPoint{Date: dayKey}
			byDayOut[dayKey] = point
		}
		for id, stats := range dayStats.byAPIKey {
			if _, allowed := keySet[id]; !allowed {
				continue
			}
			point.RequestCount += stats.RequestCount
			point.InputTokens += stats.InputTokens
			point.OutputTokens += stats.OutputTokens
			point.CacheTokens += stats.CacheTokens
			point.CacheWriteTokens += stats.CacheWriteTokens
			point.CostMicros += stats.CostMicros
			point.PricedCount += stats.PricedCount
			point.RxBytes += stats.RxBytes
			point.TxBytes += stats.TxBytes
		}
	}
	out := make([]DailyRequestPoint, 0, len(byDayOut))
	for _, point := range byDayOut {
		out = append(out, *point)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

func sortAPIKeyStats(byKey map[string]*APIKeyDayStats) []APIKeyDayStats {
	out := make([]APIKeyDayStats, 0, len(byKey))
	for _, stats := range byKey {
		out = append(out, *stats)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RequestCount == out[j].RequestCount {
			return out[i].APIKeyName < out[j].APIKeyName
		}
		return out[i].RequestCount > out[j].RequestCount
	})
	return out
}

// normalizeUsageTokens converts raw (input, cache) into normalized prompt totals.
//
// OpenAI semantics: input already includes cache (cache ⊆ input).
// Claude / proxy semantics: input is non-cached only; prompt total = input + cache.
//
// Returns (promptTotal, cacheHits) for aggregation and hit-rate math.
func normalizeUsageTokens(input, cache int64) (int64, int64) {
	if input < 0 {
		input = 0
	}
	if cache < 0 {
		cache = 0
	}
	if cache == 0 {
		return input, 0
	}
	if input >= cache {
		return input, cache
	}
	return input + cache, cache
}

// CacheHitRate returns cacheHits/promptTotal as a percentage in [0, 100].
func CacheHitRate(promptTotal, cacheHits int64) float64 {
	promptTotal, cacheHits = normalizeUsageTokens(promptTotal, cacheHits)
	if promptTotal <= 0 {
		return 0
	}
	rate := float64(cacheHits) / float64(promptTotal) * 100
	if rate > 100 {
		return 100
	}
	if rate < 0 {
		return 0
	}
	return rate
}

func sortDownstreamGatewayStats(byGateway map[string]*DownstreamGatewayDayStats) []DownstreamGatewayDayStats {
	out := make([]DownstreamGatewayDayStats, 0, len(byGateway))
	for _, stats := range byGateway {
		out = append(out, *stats)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RequestCount == out[j].RequestCount {
			return out[i].GatewayID < out[j].GatewayID
		}
		return out[i].RequestCount > out[j].RequestCount
	})
	return out
}

func mergeDownstreamGatewayDays(byDay map[string]*usageDayStats, from, to time.Time) map[string]*DownstreamGatewayDayStats {
	loc := from.Location()
	if to.IsZero() {
		to = time.Date(9999, 1, 1, 0, 0, 0, 0, loc)
	}
	fromDay := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	toDay := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, loc)
	if to.Hour() > 0 || to.Minute() > 0 || to.Second() > 0 || to.Nanosecond() > 0 {
		toDay = toDay.AddDate(0, 0, 1)
	}
	out := make(map[string]*DownstreamGatewayDayStats)
	for dayKey, dayStats := range byDay {
		day, err := time.ParseInLocation("2006-01-02", dayKey, loc)
		if err != nil || day.Before(fromDay) || !day.Before(toDay) {
			continue
		}
		for id, stats := range dayStats.byDownstream {
			target, ok := out[id]
			if !ok {
				copied := *stats
				out[id] = &copied
				continue
			}
			target.RequestCount += stats.RequestCount
			target.InputTokens += stats.InputTokens
			target.OutputTokens += stats.OutputTokens
			target.CacheTokens += stats.CacheTokens
			target.CacheWriteTokens += stats.CacheWriteTokens
			target.CostMicros += stats.CostMicros
			target.PricedCount += stats.PricedCount
		}
	}
	return out
}

func sortProviderStats(byProvider map[string]*ProviderDayStats) []ProviderDayStats {
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

func sortUserStats(byUser map[string]*UserDayStats) []UserDayStats {
	out := make([]UserDayStats, 0, len(byUser))
	for _, stats := range byUser {
		out = append(out, *stats)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RequestCount == out[j].RequestCount {
			return out[i].UserID < out[j].UserID
		}
		return out[i].RequestCount > out[j].RequestCount
	})
	return out
}
