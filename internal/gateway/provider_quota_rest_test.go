package gateway

import (
	"testing"
	"time"
)

// withSpentClaudeWindow puts a usage reading in the server's cache: fiveHour
// percent used, reopening at resetAt.
func withSpentClaudeWindow(s *Server, providerID string, fiveHour float64, resetAt time.Time, now time.Time) {
	s.oauthUsageCache.entries[s.oauthUsageCacheKeyFor("claude", providerID)] = oauthUsageCacheEntry{
		value: ClaudeOAuthUsageReport{
			Available: true,
			FiveHour: &ClaudeOAuthUsageBucket{
				Utilization: fiveHour,
				ResetsAt:    resetAt.Format(time.RFC3339),
			},
		},
		fetchedAt: now,
	}
}

// A 429 on an account whose 5h window is spent is a quota refusal, however terse
// its body: the rest must reach the moment the window reopens, not stop at the
// rate cap. Holding it to the cap is what asked the same account again four
// minutes later and doubled the ladder — the log's consecutive=3, 4, 5.
func TestCooldownTTLReachesASpentWindowReset(t *testing.T) {
	now := time.Now()
	s := &Server{oauthUsageCache: newOAuthUsageCache()}
	resetAt := now.Add(3 * time.Hour)
	withSpentClaudeWindow(s, "c1", 100, resetAt, now)

	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit. Please try again later."}}`)
	ttl := s.cooldownTTL("c1", "", body, false, now)
	if ttl < 2*time.Hour {
		t.Fatalf("expected the rest to reach the window's reset (~3h), got %v", ttl)
	}
	if ttl > providerQuotaRestCap {
		t.Fatalf("expected the rest bounded by providerQuotaRestCap, got %v", ttl)
	}
}

// A window with allowance left is not a spent quota: the rate ladder stays, so a
// momentary throttle does not bench a healthy account for hours.
func TestCooldownTTLKeepsRateLadderWhenNothingIsSpent(t *testing.T) {
	now := time.Now()
	s := &Server{oauthUsageCache: newOAuthUsageCache()}
	withSpentClaudeWindow(s, "c2", 12, now.Add(3*time.Hour), now)

	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	ttl := s.cooldownTTL("c2", "", body, false, now)
	if ttl > providerRateRestCap {
		t.Fatalf("a throttle must stay inside the rate cap, got %v", ttl)
	}
}

// A resets_at far beyond the quota cap (a weekly window named by a terse 429)
// must not park the account for a week: the cap bounds it.
func TestCooldownTTLBoundsASpentWindowByQuotaCap(t *testing.T) {
	now := time.Now()
	s := &Server{oauthUsageCache: newOAuthUsageCache()}
	withSpentClaudeWindow(s, "c3", 100, now.Add(7*24*time.Hour), now)

	ttl := s.cooldownTTL("c3", "", []byte(`{"error":{"type":"rate_limit_error"}}`), false, now)
	if ttl > providerQuotaRestCap {
		t.Fatalf("expected the rest bounded by providerQuotaRestCap, got %v", ttl)
	}
	if ttl < providerRateRestCap {
		t.Fatalf("expected a spent window to outlast the rate cap, got %v", ttl)
	}
}

// claudeSpentWindowReset reports only a window that is actually used up and still
// has a future reset to wait for.
func TestClaudeSpentWindowReset(t *testing.T) {
	now := time.Now()
	s := &Server{oauthUsageCache: newOAuthUsageCache()}

	if _, spent := s.claudeSpentWindowReset("none", now); spent {
		t.Fatal("expected no reading to mean nothing is spent")
	}
	withSpentClaudeWindow(s, "fresh", 40, now.Add(time.Hour), now)
	if _, spent := s.claudeSpentWindowReset("fresh", now); spent {
		t.Fatal("40% used is not a spent window")
	}
	withSpentClaudeWindow(s, "done", 100, now.Add(time.Hour), now)
	at, spent := s.claudeSpentWindowReset("done", now)
	if !spent {
		t.Fatal("100% used is a spent window")
	}
	if until := time.Until(at); until < 50*time.Minute {
		t.Fatalf("expected the reset moment returned, got %v away", until)
	}
	withSpentClaudeWindow(s, "past", 100, now.Add(-time.Hour), now)
	if _, spent := s.claudeSpentWindowReset("past", now); spent {
		t.Fatal("a reset in the past is not a future window")
	}
}
