package gateway

import (
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// An account loaded from the database must get the same defaults a new one does;
// otherwise every existing account keeps 0 (= unlimited) forever.
func TestNormalizeProviderGivesWorkBuddyAnInFlightCap(t *testing.T) {
	cases := []struct {
		name     string
		authType string
		wantMax  int
	}{
		{"CN", domain.AuthTypeWorkBuddy, workBuddyDefaultConcurrencyCN},
		{"intl", domain.AuthTypeWorkBuddyAI, workBuddyDefaultConcurrencyIntl},
	}
	for _, testCase := range cases {
		provider := domain.Provider{AuthType: testCase.authType}
		normalizeProvider(&provider)
		if provider.MaxConcurrentRequests != testCase.wantMax {
			t.Errorf("%s: max concurrent = %d, want %d", testCase.name, provider.MaxConcurrentRequests, testCase.wantMax)
		}
		// The excess waits for a slot instead of failing over onto another
		// account, which would spread one burst across several accounts.
		if provider.QueueWaitSeconds != workBuddyDefaultQueueWaitSeconds {
			t.Errorf("%s: queue wait = %d, want %d", testCase.name, provider.QueueWaitSeconds, workBuddyDefaultQueueWaitSeconds)
		}
	}
}

// Normalising twice must not change anything: the router normalises on load, on
// every update, and on both it has to land on the same numbers.
func TestNormalizeProviderWorkBuddyLimitsAreIdempotent(t *testing.T) {
	provider := domain.Provider{AuthType: domain.AuthTypeWorkBuddy}
	normalizeProvider(&provider)
	firstMax, firstWait := provider.MaxConcurrentRequests, provider.QueueWaitSeconds
	normalizeProvider(&provider)
	if provider.MaxConcurrentRequests != firstMax || provider.QueueWaitSeconds != firstWait {
		t.Fatalf("second pass changed the limits: %d/%d then %d/%d",
			firstMax, firstWait, provider.MaxConcurrentRequests, provider.QueueWaitSeconds)
	}
}

// An operator who set a cap of their own keeps it, and keeps their zero wait.
func TestNormalizeProviderKeepsAnExplicitCap(t *testing.T) {
	provider := domain.Provider{AuthType: domain.AuthTypeWorkBuddy, MaxConcurrentRequests: 1}
	normalizeProvider(&provider)
	if provider.MaxConcurrentRequests != 1 {
		t.Fatalf("max concurrent = %d, want the operator's 1", provider.MaxConcurrentRequests)
	}
	if provider.QueueWaitSeconds != 0 {
		t.Fatalf("queue wait = %d, want the operator's 0", provider.QueueWaitSeconds)
	}
}

// A per-minute budget counts as configuration too: the account was not left
// unbounded on purpose.
func TestNormalizeProviderKeepsARateLimitedAccountAlone(t *testing.T) {
	provider := domain.Provider{AuthType: domain.AuthTypeWorkBuddy, RequestsPerMinute: 10}
	normalizeProvider(&provider)
	if provider.MaxConcurrentRequests != 0 {
		t.Fatalf("max concurrent = %d, want 0 (the account caps by rate)", provider.MaxConcurrentRequests)
	}
}

// Every other kind of provider keeps its own meaning of 0.
func TestNormalizeProviderLeavesOtherProvidersUncapped(t *testing.T) {
	for _, authType := range []string{domain.AuthTypeAPIKey, domain.AuthTypeClaudeOAuth, domain.AuthTypeQoderCN} {
		provider := domain.Provider{AuthType: authType}
		normalizeProvider(&provider)
		if provider.MaxConcurrentRequests != 0 {
			t.Errorf("%s: max concurrent = %d, want 0", authType, provider.MaxConcurrentRequests)
		}
	}
}

// The gate is what actually enforces the cap, and an account loaded from the
// database is never re-saved — it still reads 0/0/0 forever. The default has to
// be applied where the cap is read, or it never reaches those accounts at all.
func TestProviderConcurrencyConfigDefaultsForAStoredAccount(t *testing.T) {
	cases := []struct {
		name     string
		authType string
		wantMax  int
	}{
		{"CN", domain.AuthTypeWorkBuddy, workBuddyDefaultConcurrencyCN},
		{"intl", domain.AuthTypeWorkBuddyAI, workBuddyDefaultConcurrencyIntl},
	}
	for _, testCase := range cases {
		// Deliberately unnormalised: this is the shape the store hands over.
		stored := domain.Provider{AuthType: testCase.authType}
		max, wait := providerConcurrencyConfig(stored)
		if max != testCase.wantMax {
			t.Errorf("%s: gate cap = %d, want %d", testCase.name, max, testCase.wantMax)
		}
		if wait != time.Duration(workBuddyDefaultQueueWaitSeconds)*time.Second {
			t.Errorf("%s: gate wait = %v, want %ds", testCase.name, wait, workBuddyDefaultQueueWaitSeconds)
		}
	}
}

// An operator's own numbers are never replaced at the gate either.
func TestProviderConcurrencyConfigKeepsAnExplicitCap(t *testing.T) {
	stored := domain.Provider{AuthType: domain.AuthTypeWorkBuddy, MaxConcurrentRequests: 1}
	max, wait := providerConcurrencyConfig(stored)
	if max != 1 || wait != 0 {
		t.Fatalf("gate = %d/%v, want the operator's 1/0s", max, wait)
	}
}

// A rate-limited account is configured, not unconfigured: the gate adds no cap.
func TestProviderConcurrencyConfigLeavesRateLimitedAccountAlone(t *testing.T) {
	stored := domain.Provider{AuthType: domain.AuthTypeWorkBuddy, RequestsPerMinute: 10}
	if max, _ := providerConcurrencyConfig(stored); max != 0 {
		t.Fatalf("gate cap = %d, want 0 for an account capped by rate", max)
	}
}
