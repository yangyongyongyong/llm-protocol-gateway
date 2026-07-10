package monitor

import "time"

// UsagePersistDelta is one increment applied to daily usage buckets.
type UsagePersistDelta struct {
	Day          string
	KeyID        string
	KeyName      string
	ProviderID   string
	Model        string // empty when model bucket should not increment
	StatusClass  string // 2xx | 4xx | 5xx | other
	InputTokens  int64
	OutputTokens int64
	CacheTokens  int64
	LatencyMs    int64
	TTFTMs       int64
	LastRequest  *RequestLog
}

// UsageDayBuckets is a serializable daily usage snapshot for DB load/bootstrap.
type UsageDayBuckets struct {
	Total        APIKeyDayStats
	ByAPIKey     map[string]APIKeyDayStats
	ByProvider   map[string]ProviderDayStats
	ByModel      map[string]ModelDayStats
	Status2xx    int64
	Status4xx    int64
	Status5xx    int64
	StatusOther  int64
	LatencySum   int64
	TTFTSum      int64
	TTFTCount    int64
}

// UsageDailyStore persists per-day usage aggregates to SQLite.
type UsageDailyStore interface {
	ApplyUsageDelta(delta UsagePersistDelta) error
	LoadUsageSince(since time.Time) (map[string]UsageDayBuckets, *RequestLog, error)
	PruneUsageBefore(cutoffDay time.Time) error
	ClearUsageDaily() error
}
