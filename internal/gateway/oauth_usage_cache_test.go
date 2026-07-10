package gateway

import (
	"testing"
	"time"
)

func TestOAuthUsageCacheTTL(t *testing.T) {
	t.Parallel()
	cache := newOAuthUsageCache()
	cache.set("claude:p1", ClaudeOAuthUsageReport{Available: true, FetchedAt: "now"}, 50*time.Millisecond)

	if got, ok := cache.get("claude:p1"); !ok {
		t.Fatal("expected cache hit")
	} else if report, ok := got.(ClaudeOAuthUsageReport); !ok || !report.Available {
		t.Fatalf("unexpected cached value: %#v", got)
	}

	time.Sleep(70 * time.Millisecond)
	if _, ok := cache.get("claude:p1"); ok {
		t.Fatal("expected cache miss after TTL")
	}
}

func TestOAuthUsageCacheInvalidate(t *testing.T) {
	t.Parallel()
	cache := newOAuthUsageCache()
	cache.set("cursor:p1", CursorOAuthUsageReport{Available: true}, time.Minute)
	cache.invalidate("cursor:p1")
	if _, ok := cache.get("cursor:p1"); ok {
		t.Fatal("expected invalidated entry to miss")
	}
}
