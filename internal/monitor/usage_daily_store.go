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
	StatusClass    string // 2xx | 4xx | 5xx | other
	InputTokens    int64
	OutputTokens   int64
	CacheTokens    int64
	RxBytes        int64
	TxBytes        int64
	LatencyMs      int64
	TTFTMs         int64
	LastRequest    *RequestLog
}

// UsageDayBuckets is a serializable daily usage snapshot for DB load/bootstrap.
type UsageDayBuckets struct {
	Total       APIKeyDayStats
	ByAPIKey    map[string]APIKeyDayStats
	ByProvider  map[string]ProviderDayStats
	ByModel     map[string]ModelDayStats
	ByUser      map[string]UserDayStats
	ByProtocol  map[string]ProtocolDayStats
	Status2xx   int64
	Status4xx   int64
	Status5xx   int64
	StatusOther int64
	LatencySum  int64
	TTFTSum     int64
	TTFTCount   int64
}

// UsageDailyStore persists per-day usage aggregates to SQLite.
// Daily aggregates are retained permanently; request log retention must not prune them.
type UsageDailyStore interface {
	ApplyUsageDelta(delta UsagePersistDelta) error
	// LoadUsageSince loads aggregates for day >= since. Zero since loads all history.
	LoadUsageSince(since time.Time) (map[string]UsageDayBuckets, *RequestLog, error)
	// ClearUsageSince deletes aggregates for day >= since (used when replaying
	// request logs for the still-retained detail window). Older days are kept.
	ClearUsageSince(since time.Time) error
	PruneUsageBefore(cutoffDay time.Time) error
	ClearUsageDaily() error
}
