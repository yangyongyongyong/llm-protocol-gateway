package gateway

import (
	"testing"
	"time"
)

func TestOAuthUsageCacheFreshAndStale(t *testing.T) {
	t.Parallel()
	cache := newOAuthUsageCache()
	cache.set("claude:p1", ClaudeOAuthUsageReport{Available: true, FetchedAt: "now"})

	if got, ok := cache.get("claude:p1"); !ok {
		t.Fatal("expected fresh cache hit")
	} else if report, ok := got.(ClaudeOAuthUsageReport); !ok || !report.Available {
		t.Fatalf("unexpected cached value: %#v", got)
	}

	entry, ok := cache.getEntry("claude:p1")
	if !ok {
		t.Fatal("expected entry")
	}
	entry.fetchedAt = time.Now().Add(-oauthUsageFreshTTL - time.Second)
	cache.mu.Lock()
	cache.entries["claude:p1"] = entry
	cache.mu.Unlock()

	if _, ok := cache.get("claude:p1"); ok {
		t.Fatal("expected fresh miss after fresh TTL")
	}
	if got, ok := cache.getAllowStale("claude:p1"); !ok {
		t.Fatal("expected stale hit")
	} else if report, ok := got.(ClaudeOAuthUsageReport); !ok || !report.Available {
		t.Fatalf("unexpected stale value: %#v", got)
	}
}

func TestOAuthUsageCacheInvalidate(t *testing.T) {
	t.Parallel()
	cache := newOAuthUsageCache()
	cache.set("cursor:p1", CursorOAuthUsageReport{Available: true})
	cache.invalidate("cursor:p1")
	if _, ok := cache.getAllowStale("cursor:p1"); ok {
		t.Fatal("expected invalidated entry to miss")
	}
}
