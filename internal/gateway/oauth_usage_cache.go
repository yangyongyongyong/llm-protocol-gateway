package gateway

import (
	"sync"
	"time"
)

const (
	// oauthUsageFreshTTL: serve cache without upstream fetch while fresh.
	oauthUsageFreshTTL = 3 * time.Minute
	// oauthUsageStaleTTL: still return stale cache instantly; refresh async after fresh window.
	oauthUsageStaleTTL = 1 * time.Hour
)

type oauthUsageCacheEntry struct {
	value     any
	fetchedAt time.Time
}

// oauthUsageCache stores recent Claude/Cursor usage reports keyed by provider.
type oauthUsageCache struct {
	mu      sync.Mutex
	entries map[string]oauthUsageCacheEntry
}

func newOAuthUsageCache() *oauthUsageCache {
	return &oauthUsageCache{entries: make(map[string]oauthUsageCacheEntry)}
}

func (c *oauthUsageCache) get(key string) (any, bool) {
	entry, ok := c.getEntry(key)
	if !ok || !c.isFresh(entry) {
		return nil, false
	}
	return entry.value, true
}

func (c *oauthUsageCache) getAllowStale(key string) (any, bool) {
	entry, ok := c.getEntry(key)
	if !ok {
		return nil, false
	}
	return entry.value, true
}

func (c *oauthUsageCache) getEntry(key string) (oauthUsageCacheEntry, bool) {
	if c == nil {
		return oauthUsageCacheEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return oauthUsageCacheEntry{}, false
	}
	if time.Since(entry.fetchedAt) > oauthUsageStaleTTL {
		delete(c.entries, key)
		return oauthUsageCacheEntry{}, false
	}
	return entry, true
}

func (c *oauthUsageCache) isFresh(entry oauthUsageCacheEntry) bool {
	return time.Since(entry.fetchedAt) <= oauthUsageFreshTTL
}

func (c *oauthUsageCache) needsRefresh(key string) bool {
	entry, ok := c.getEntry(key)
	if !ok {
		return true
	}
	return !c.isFresh(entry)
}

func (c *oauthUsageCache) set(key string, value any) {
	if c == nil || value == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = oauthUsageCacheEntry{
		value:     value,
		fetchedAt: time.Now(),
	}
}

func (c *oauthUsageCache) invalidate(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}
