package gateway

import (
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

const requestStatsCacheTTL = 5 * time.Second

type requestStatsCacheEntry struct {
	snapshot  monitor.UsageStatsSnapshot
	expiresAt time.Time
}

// requestStatsCache deduplicates repeated UI polls within a short TTL.
type requestStatsCache struct {
	mu      sync.Mutex
	entries map[string]requestStatsCacheEntry
}

func newRequestStatsCache() *requestStatsCache {
	return &requestStatsCache{entries: make(map[string]requestStatsCacheEntry)}
}

func (c *requestStatsCache) get(key string) (monitor.UsageStatsSnapshot, bool) {
	if c == nil {
		return monitor.UsageStatsSnapshot{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return monitor.UsageStatsSnapshot{}, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.entries, key)
		return monitor.UsageStatsSnapshot{}, false
	}
	return entry.snapshot, true
}

func (c *requestStatsCache) set(key string, snapshot monitor.UsageStatsSnapshot) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = requestStatsCacheEntry{
		snapshot:  snapshot,
		expiresAt: time.Now().Add(requestStatsCacheTTL),
	}
}

// clear 清空短时统计缓存，在补算历史费用或手工变更聚合后保证界面立即可见最新结果。
func (c *requestStatsCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.entries)
}
