package gateway

import (
	"sync"
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

func TestOAuthUsageNeedsRefreshCoalesce(t *testing.T) {
	t.Parallel()
	cache := newOAuthUsageCache()
	if !cache.needsRefresh("cursor:p1") {
		t.Fatal("missing entry should need refresh")
	}
	cache.set("cursor:p1", CursorOAuthUsageReport{Available: true})
	if cache.needsRefresh("cursor:p1") {
		t.Fatal("fresh entry should not need refresh")
	}

	entry, ok := cache.getEntry("cursor:p1")
	if !ok {
		t.Fatal("expected entry")
	}
	entry.fetchedAt = time.Now().Add(-oauthUsageFreshTTL - time.Second)
	cache.mu.Lock()
	cache.entries["cursor:p1"] = entry
	cache.mu.Unlock()
	if !cache.needsRefresh("cursor:p1") {
		t.Fatal("stale entry should need refresh")
	}
}

func TestTryLockOAuthUsageFetch(t *testing.T) {
	t.Parallel()
	s := &Server{oauthUsageFetchMu: sync.Map{}}
	if !s.tryLockOAuthUsageFetch("cursor:p1") {
		t.Fatal("first tryLock should succeed")
	}
	if s.tryLockOAuthUsageFetch("cursor:p1") {
		t.Fatal("second tryLock should fail while held")
	}
	s.unlockOAuthUsageFetch("cursor:p1")
	if !s.tryLockOAuthUsageFetch("cursor:p1") {
		t.Fatal("tryLock should succeed after unlock")
	}
	s.unlockOAuthUsageFetch("cursor:p1")
}
