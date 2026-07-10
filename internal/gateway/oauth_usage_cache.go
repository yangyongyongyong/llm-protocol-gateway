package gateway

import (
	"sync"
	"time"
)

// oauthUsageCacheTTL is long enough to absorb repeated UI polls while still
// fresh enough for subscription-quota display.
const oauthUsageCacheTTL = 2 * time.Minute

type oauthUsageCacheEntry struct {
	value     any
	expiresAt time.Time
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
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.entries, key)
		return nil, false
	}
	return entry.value, true
}

func (c *oauthUsageCache) set(key string, value any, ttl time.Duration) {
	if c == nil || value == nil {
		return
	}
	if ttl <= 0 {
		ttl = oauthUsageCacheTTL
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = oauthUsageCacheEntry{
		value:     value,
		expiresAt: time.Now().Add(ttl),
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
