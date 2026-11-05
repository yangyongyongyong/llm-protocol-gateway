package gateway

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// qoderCooldownServer is a server whose pool is the providers given, so the
// pool-size rule can be exercised.
func qoderCooldownServer(providers ...domain.Provider) *Server {
	return &Server{router: NewRouter(domain.GatewayState{Providers: providers})}
}

func qoderCooldownProvider(id string) domain.Provider {
	return domain.Provider{
		ID:       id,
		Name:     id,
		Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeQoderPAT,
		QoderPAT: &domain.QoderCredential{UID: "uid-" + id, AccessToken: "token"},
	}
}

// A pool of one rests for seconds: cooling the only account for minutes turns one
// throttled request into an outage.
func TestQoderSingleAccountPoolRestsBriefly(t *testing.T) {
	s := qoderCooldownServer(qoderCooldownProvider("q1"))
	if !s.applyProviderFailoverOutcome("q1", http.StatusTooManyRequests, nil, "", nil) {
		t.Fatal("a throttle is worth the next attempt")
	}
	state, ok := s.providerUnavailableSnapshot("q1")
	if !ok {
		t.Fatal("the account was not rested at all")
	}
	rest := time.Until(state.nextRetry)
	if rest > 10*time.Second {
		t.Fatalf("a one-account pool rested %s; the ladder's minutes are what this avoids", rest.Round(time.Second))
	}
	if state.hard {
		t.Fatal("one throttle is not proof the account is broken")
	}
}

// An exhausted balance is a fact about the account, not a fault: it rests for a
// fixed period and is never escalated to a hard mark, so it comes back on its own
// when the balance does.
func TestQoderQuotaRefusalRestsLongAndSoft(t *testing.T) {
	s := qoderCooldownServer(qoderCooldownProvider("q1"))
	body := []byte(`{"code":112,"message":"额度已用完：credits used up"}`)
	_ = s.applyProviderFailoverOutcome("q1", http.StatusTooManyRequests, body, "", nil)
	state, ok := s.providerUnavailableSnapshot("q1")
	if !ok {
		t.Fatal("an exhausted account must rest")
	}
	rest := time.Until(state.nextRetry)
	if rest < qoderQuotaRest-time.Minute {
		t.Fatalf("rest = %s, want about %s", rest.Round(time.Second), qoderQuotaRest)
	}
	if state.hard {
		t.Fatal("an empty balance must not be marked as a broken account")
	}
	// It is paced, not punished: no soft-failure strike was recorded.
	if count := s.softFailureCount("q1"); count != 0 {
		t.Fatalf("quota refusal left %d soft strikes", count)
	}
}

// With a pool to fall back on, a failure waits: it is charged only if some
// account answers this request. A request no account can serve parks nobody.
func TestQoderFailureIsHeldUntilTheRequestSettles(t *testing.T) {
	s := qoderCooldownServer(qoderCooldownProvider("q1"), qoderCooldownProvider("q2"))
	ctx := withQoderBlame(context.Background())

	if !s.applyProviderFailoverOutcomeCtx(ctx, "q1", http.StatusTooManyRequests, nil, "", nil) {
		t.Fatal("a held failure still means try the next account")
	}
	if _, marked := s.providerUnavailableSnapshot("q1"); marked {
		t.Fatal("a failure on probation must not rest the account yet")
	}
	if count := s.softFailureCount("q1"); count != 0 {
		t.Fatalf("a failure on probation must not be counted yet, got %d", count)
	}
	// Some account answered the request, so the refusal was q1's after all.
	qoderBlameBucketFrom(ctx).flush(s)
	if count := s.softFailureCount("q1"); count != 1 {
		t.Fatalf("the settled failure was not charged, count = %d", count)
	}
}

// A request that every account refuses charges nobody: the failure was the
// request's, and cooling the whole pool for it is how one bad prompt takes the
// service down.
func TestQoderFailuresAreDroppedWhenNothingServesTheRequest(t *testing.T) {
	s := qoderCooldownServer(qoderCooldownProvider("q1"), qoderCooldownProvider("q2"))
	ctx := withQoderBlame(context.Background())
	_ = s.applyProviderFailoverOutcomeCtx(ctx, "q1", http.StatusTooManyRequests, nil, "", nil)
	_ = s.applyProviderFailoverOutcomeCtx(ctx, "q2", http.StatusTooManyRequests, nil, "", nil)
	// Nothing answered, so the ledger is simply never flushed.
	for _, id := range []string{"q1", "q2"} {
		if count := s.softFailureCount(id); count != 0 {
			t.Fatalf("%s carries %d strikes from a request nobody could serve", id, count)
		}
		if _, marked := s.providerUnavailableSnapshot(id); marked {
			t.Fatalf("%s was rested for a request-wide failure", id)
		}
	}
}

// Without a ledger — a direct call, or a provider that is not Qoder — the
// generic ladder keeps its say, unchanged.
func TestNonQoderFailuresKeepTheGenericLadder(t *testing.T) {
	plain := domain.Provider{
		ID: "glm", Name: "glm", Protocol: domain.ProtocolOpenAIChat, AuthType: "api_key",
		BaseURL: "https://example.invalid",
	}
	s := qoderCooldownServer(plain)
	if !s.applyProviderFailoverOutcome("glm", http.StatusTooManyRequests, nil, "", nil) {
		t.Fatal("a throttle is worth the next attempt")
	}
	// The generic path counts the failure immediately and waits for the
	// threshold before resting anything.
	if count := s.softFailureCount("glm"); count != 1 {
		t.Fatalf("soft count = %d, want the generic ladder's immediate strike", count)
	}
	if _, marked := s.providerUnavailableSnapshot("glm"); marked {
		t.Fatal("one strike must not rest a provider on the generic ladder")
	}
}

// A direct call with no ledger at all still records: an unanswered failure is
// treated as real rather than silently dropped.
func TestQoderFailureWithoutALedgerFallsBackToTheLadder(t *testing.T) {
	s := qoderCooldownServer(qoderCooldownProvider("q1"), qoderCooldownProvider("q2"))
	if !s.applyProviderFailoverOutcome("q1", http.StatusTooManyRequests, nil, "", nil) {
		t.Fatal("a throttle is worth the next attempt")
	}
	if count := s.softFailureCount("q1"); count != 1 {
		t.Fatalf("soft count = %d, want the failure recorded", count)
	}
}
