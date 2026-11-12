package gateway

import (
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// providerLoadBucketSize and providerLoadBuckets define the window the
	// "least_used" mode reads: the last six complete minutes. Long enough that
	// one busy conversation does not jump back to being "the quiet account"
	// after a single request, short enough that an account that stopped being
	// used starts taking traffic again within minutes rather than hours.
	providerLoadBucketSize = time.Minute
	providerLoadBuckets    = 6
	// quotaHeadroomMaxAge bounds how old a cached quota snapshot may be before
	// the scheduler stops trusting it for ordering.
	quotaHeadroomMaxAge = 15 * time.Minute
)

// providerLoadTracker keeps a per-provider rolling workload in memory.
//
// It is deliberately not read from the daily aggregates: those are written
// through a batched path and are per day, while the scheduler needs "what has
// this account carried in the last few minutes" without a database query on
// the request path. The counters are runtime-only, so a restart levels the
// playing field again — which is the right behavior, not a bug to fix.
type providerLoadTracker struct {
	mu     sync.Mutex
	series map[string]*providerLoadSeries
	now    func() time.Time
}

type providerLoadSeries struct {
	buckets [providerLoadBuckets]int64
	minutes [providerLoadBuckets]int64
}

func newProviderLoadTracker() *providerLoadTracker {
	return &providerLoadTracker{series: map[string]*providerLoadSeries{}}
}

func (t *providerLoadTracker) nowMinute() int64 {
	if t.now != nil {
		return t.now().Unix() / 60
	}
	return time.Now().Unix() / 60
}

// add records one request's worth of work against a provider.
func (t *providerLoadTracker) add(providerID string, work int64) {
	if t == nil || providerID == "" || work <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	series := t.series[providerID]
	if series == nil {
		series = &providerLoadSeries{}
		t.series[providerID] = series
	}
	minute := t.nowMinute()
	slot := int(minute % providerLoadBuckets)
	if series.minutes[slot] != minute {
		series.minutes[slot] = minute
		series.buckets[slot] = 0
	}
	series.buckets[slot] += work
	if len(t.series) > providerLoadSeriesCap {
		t.pruneLocked(minute)
	}
}

// providerLoadSeriesCap stops the tracker from growing with the number of
// providers an operator has ever pointed a key at.
const providerLoadSeriesCap = 512

// pruneLocked drops series whose whole window has gone quiet. Cheapest
// correct-enough policy: forget anything whose newest bucket is older than the
// window, which bounds the map at "providers active in the last six minutes".
func (t *providerLoadTracker) pruneLocked(minute int64) {
	for providerID, series := range t.series {
		newest := int64(0)
		for _, tracked := range series.minutes {
			if tracked > newest {
				newest = tracked
			}
		}
		if minute-newest >= providerLoadBuckets {
			delete(t.series, providerID)
		}
	}
}

// workload is the summed work of a provider inside the window. A provider that
// never served anything scores 0, which is what makes a freshly added account
// get used first instead of last.
func (t *providerLoadTracker) workload(providerID string) int64 {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	series, ok := t.series[providerID]
	if !ok {
		return 0
	}
	minute := t.nowMinute()
	var total int64
	for slot, tracked := range series.minutes {
		if tracked == 0 || minute-tracked >= providerLoadBuckets {
			continue
		}
		total += series.buckets[slot]
	}
	return total
}

// noteProviderUsage records one answered request against its provider for the
// "least_used" mode. Uncached input plus output is the work an account actually
// did, so a conversation hitting a warm prompt cache does not make that account
// look loaded.
func (s *Server) noteProviderUsage(providerID string, usage TokenUsage) {
	if s == nil || s.providerLoads == nil {
		return
	}
	work := usage.InputTokens - usage.CacheTokens + usage.OutputTokens
	if work <= 0 {
		// A refusal, an empty reply or an upstream that hides usage still cost
		// one request. Counting it keeps a chatty-but-tiny client from making an
		// account look idle.
		work = 1
	}
	s.providerLoads.add(providerID, work)
}

func (s *Server) providerWorkload(providerID string) int64 {
	if s == nil {
		return 0
	}
	return s.providerLoads.workload(providerID)
}

// providerQuotaHeadroom answers "how much of this account's window is still
// usable" as 0-1, from the same snapshots the quota cards read. It never calls
// upstream: a scheduler decision must not add a request to the account it is
// judging.
func (s *Server) providerQuotaHeadroom(providerID string) (float64, bool) {
	if s == nil || s.oauthUsageCache == nil {
		return 0, false
	}
	for _, prefix := range quotaReportPrefixes {
		cached, ok := s.oauthUsageCache.getAllowStale(s.oauthUsageCacheKeyFor(prefix, providerID))
		if !ok {
			continue
		}
		if !quotaReportIsFresh(cached) {
			continue
		}
		switch report := cached.(type) {
		case ClaudeOAuthUsageReport:
			if !report.Available {
				continue
			}
			return utilizationHeadroom(clauseUtilizations(report))
		case ChatGPTOAuthUsageReport:
			if !report.Available {
				continue
			}
			values := make([]float64, 0, len(report.Buckets))
			for _, bucket := range report.Buckets {
				values = append(values, bucket.Utilization)
			}
			return utilizationHeadroom(values)
		case CursorOAuthUsageReport:
			if !report.Available {
				continue
			}
			values := make([]float64, 0, len(report.Buckets))
			for _, bucket := range report.Buckets {
				values = append(values, bucket.Utilization)
			}
			return utilizationHeadroom(values)
		case ZhipuUsageReport:
			if !report.Available {
				continue
			}
			values := make([]float64, 0, 2)
			if report.FiveHour != nil {
				values = append(values, report.FiveHour.Utilization)
			}
			if report.Weekly != nil {
				values = append(values, report.Weekly.Utilization)
			}
			return utilizationHeadroom(values)
		case OpenCodeGoUsageReport:
			if !report.Available {
				continue
			}
			values := make([]float64, 0, 3)
			for _, bucket := range []*OpenCodeGoUsageBucket{report.FiveHour, report.Weekly, report.Monthly} {
				if bucket != nil {
					values = append(values, bucket.Utilization)
				}
			}
			return utilizationHeadroom(values)
		case DeepSeekBalanceReport:
			if !report.Available || report.Unsupported {
				continue
			}
			// A balance account has no window: the vendor's own availability
			// flag is all the signal there is.
			if report.IsAvailable {
				return 1, true
			}
			return 0, true
		}
	}
	return 0, false
}

var quotaReportPrefixes = []string{"claude", "chatgpt", "cursor", "zhipu", "deepseek", "opencode-go"}

func clauseUtilizations(report ClaudeOAuthUsageReport) []float64 {
	values := make([]float64, 0, 4)
	for _, bucket := range []*ClaudeOAuthUsageBucket{report.FiveHour, report.SevenDay, report.SevenDayOpus, report.SevenDaySonnet} {
		if bucket != nil {
			values = append(values, bucket.Utilization)
		}
	}
	return values
}

// utilizationHeadroom turns the most-used window into headroom: an account at
// 90% of its five-hour window has 10% left to give, no matter how empty its
// weekly window looks.
func utilizationHeadroom(utilizations []float64) (float64, bool) {
	if len(utilizations) == 0 {
		return 0, false
	}
	copied := append([]float64(nil), utilizations...)
	sort.Float64s(copied)
	max := copied[len(copied)-1]
	if max < 0 {
		max = 0
	}
	if max > 100 {
		max = 100
	}
	return (100 - max) / 100, true
}

// quotaReportIsFresh keeps a snapshot from a long-dead account out of the
// ordering. FetchedAt is written by every report builder.
func quotaReportIsFresh(cached any) bool {
	var stamp string
	switch report := cached.(type) {
	case ClaudeOAuthUsageReport:
		stamp = report.FetchedAt
	case ChatGPTOAuthUsageReport:
		stamp = report.FetchedAt
	case CursorOAuthUsageReport:
		stamp = report.FetchedAt
	case ZhipuUsageReport:
		stamp = report.FetchedAt
	case OpenCodeGoUsageReport:
		stamp = report.FetchedAt
	case DeepSeekBalanceReport:
		stamp = report.FetchedAt
	default:
		return false
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(stamp))
	if err != nil {
		return false
	}
	return time.Since(parsed) <= quotaHeadroomMaxAge
}
