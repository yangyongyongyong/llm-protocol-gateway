package monitor

import "time"

// UsagePersistDelta is one increment applied to daily usage buckets.
type UsagePersistDelta struct {
	Day            string
	KeyID          string
	KeyName        string
	UserID         string
	ProviderID     string
	Model          string // empty when model bucket should not increment
	OutputProtocol string // empty when protocol bucket should not increment
	// PeerGatewayID 是这次 ingress 流量来自哪个下游网关。空表示普通本地请求，
	// 或本机转发到上游的 egress 流量：下游网关用量只统计别人送进来的请求。
	PeerGatewayID string
	StatusClass   string // 2xx | 4xx | 5xx | other
	InputTokens   int64
	OutputTokens  int64
	CacheTokens   int64
	// CacheWriteTokens is the cache-creation half of the prompt; CostMicros is
	// the request's priced cost (micro US dollars), summed across a day's
	// requests. Both stay 0 for history aggregated before they existed.
	CacheWriteTokens int64
	CostMicros       int64
	// PricedCount is how many of the delta's requests had a price row, so the
	// console can say what share of a period the cost actually covers.
	PricedCount int64
	RxBytes     int64
	TxBytes     int64
	LatencyMs   int64
	TTFTMs      int64
	LastRequest *RequestLog
}

func pricedIncrement(priced bool) int64 {
	if priced {
		return 1
	}
	return 0
}

// UsageDayBuckets is a serializable daily usage snapshot for DB load/bootstrap.
type UsageDayBuckets struct {
	Total      APIKeyDayStats
	ByAPIKey   map[string]APIKeyDayStats
	ByProvider map[string]ProviderDayStats
	ByModel    map[string]ModelDayStats
	ByUser     map[string]UserDayStats
	ByKey      map[string]UsageDayBuckets
	ByProtocol map[string]ProtocolDayStats
	// ByDownstreamGateway 是 ingress 流量的下游网关维度；ByKey 里的同名字段
	// 用于普通用户按自己名下的 Key 做隔离统计。
	ByDownstreamGateway map[string]DownstreamGatewayDayStats
	Status2xx           int64
	Status4xx           int64
	Status5xx           int64
	StatusOther         int64
	LatencySum          int64
	TTFTSum             int64
	TTFTCount           int64
}

// UsageDailyStore persists per-day usage aggregates to SQLite.
// Daily aggregates are retained permanently; request log retention must not prune them.
type UsageDailyStore interface {
	ApplyUsageDelta(delta UsagePersistDelta) error
	// ApplyUsageDeltas persists several deltas in one transaction/commit — the
	// async usage worker batches same-tick events onto this instead of calling
	// ApplyUsageDelta once per request, cutting WAL fsyncs under sustained load.
	ApplyUsageDeltas(deltas []UsagePersistDelta) error
	// LoadUsageSince loads aggregates for day >= since. Zero since loads all history.
	LoadUsageSince(since time.Time) (map[string]UsageDayBuckets, *RequestLog, error)
	// ClearUsageSince deletes aggregates for day >= since (used when replaying
	// request logs for the still-retained detail window). Older days are kept.
	ClearUsageSince(since time.Time) error
	PruneUsageBefore(cutoffDay time.Time) error
	ClearUsageDaily() error
}
