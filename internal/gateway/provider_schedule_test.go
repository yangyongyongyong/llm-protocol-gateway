package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestRotateChainAt(t *testing.T) {
	t.Parallel()
	chain := []string{"p1", "p2", "p3"}
	if got := strings.Join(rotateChainAt(chain, ""), ","); got != "p1,p2,p3" {
		t.Fatalf("empty active should keep order, got %v", got)
	}
	if got := strings.Join(rotateChainAt(chain, "p3"), ","); got != "p3,p1,p2" {
		t.Fatalf("rotate to tail, got %v", got)
	}
	if got := strings.Join(rotateChainAt(chain, "gone"), ","); got != "p1,p2,p3" {
		t.Fatalf("unknown active should keep order, got %v", got)
	}
	// The returned slice must not alias the caller's chain.
	rotated := rotateChainAt(chain, "p2")
	if strings.Join(rotated, ",") != "p2,p3,p1" {
		t.Fatalf("rotate to middle, got %v", rotated)
	}
	if strings.Join(chain, ",") != "p1,p2,p3" {
		t.Fatalf("source chain mutated: %v", chain)
	}
}

func TestFailoverAttemptOrderMovesUnavailableLast(t *testing.T) {
	t.Parallel()
	s := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	chain := []string{"p1", "p2", "p3"}
	order := func() string {
		return strings.Join(s.failoverAttemptOrder(scheduleInput{chain: chain, keyID: "k"}), ",")
	}
	if got := order(); got != "p1,p2,p3" {
		t.Fatalf("all healthy should keep the configured order, got %v", got)
	}
	s.markProviderUnavailable("p1", true, "test", "", nil)
	if got := order(); got != "p2,p3,p1" {
		t.Fatalf("unavailable preferred should go last, got %v", got)
	}
	// Even when everything is cooled down the chain is still tried in order:
	// the mark comes from a real failure and may be stale.
	s.markProviderUnavailable("p2", true, "test", "", nil)
	s.markProviderUnavailable("p3", true, "test", "", nil)
	if got := order(); got != "p1,p2,p3" {
		t.Fatalf("all unavailable should keep trying in order, got %v", got)
	}
}

func TestNormalizeScheduleMode(t *testing.T) {
	t.Parallel()
	for _, accepted := range []string{"", "order", "rotate", "least_used", "quota", "spread", " Quota ", "ORDER", " Spread "} {
		mode, err := normalizeScheduleMode(accepted)
		if err != nil {
			t.Fatalf("%q rejected: %v", accepted, err)
		}
		if accepted == "" && mode != "" {
			t.Fatalf("empty must stay empty")
		}
	}
	for _, rejected := range []string{"weighted", "round_robin", "smart", "least-used"} {
		if _, err := normalizeScheduleMode(rejected); err == nil {
			t.Fatalf("%q should be rejected", rejected)
		}
	}
}

func TestFailoverAttemptOrderRotate(t *testing.T) {
	t.Parallel()
	s := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	chain := []string{"p1", "p2", "p3"}
	step := func(advance bool) string {
		return strings.Join(s.failoverAttemptOrder(scheduleInput{chain: chain, mode: scheduleModeRotate, keyID: "k1", advance: advance}), ",")
	}
	// The pre-flight attribution must not consume a step.
	if got := step(false); got != "p1,p2,p3" {
		t.Fatalf("first turn, got %v", got)
	}
	if got := step(true); got != "p1,p2,p3" {
		t.Fatalf("first sent turn, got %v", got)
	}
	if got := step(true); got != "p2,p3,p1" {
		t.Fatalf("second sent turn, got %v", got)
	}
	if got := step(true); got != "p3,p1,p2" {
		t.Fatalf("third sent turn, got %v", got)
	}
	if got := step(true); got != "p1,p2,p3" {
		t.Fatalf("the cursor should wrap, got %v", got)
	}
	// Cursors are per key, so a second key starts at its own beginning.
	if got := strings.Join(s.failoverAttemptOrder(scheduleInput{chain: chain, mode: scheduleModeRotate, keyID: "k2", advance: true}), ","); got != "p1,p2,p3" {
		t.Fatalf("second key should start fresh, got %v", got)
	}
}

func TestFailoverAttemptOrderLeastUsed(t *testing.T) {
	t.Parallel()
	s := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	chain := []string{"p1", "p2", "p3"}
	s.noteProviderUsage("p1", TokenUsage{InputTokens: 5000, OutputTokens: 500})
	s.noteProviderUsage("p2", TokenUsage{InputTokens: 100, OutputTokens: 20})
	// p3 has carried nothing, so it leads; the quiet-but-not-empty p2 comes
	// before the loaded p1.
	if got := strings.Join(s.failoverAttemptOrder(scheduleInput{chain: chain, mode: scheduleModeLeastUsed, keyID: "k"}), ","); got != "p3,p2,p1" {
		t.Fatalf("got %v", got)
	}
	// A warm prompt cache is not work: p3's tokens are all cache reads.
	s.noteProviderUsage("p3", TokenUsage{InputTokens: 9000, CacheTokens: 9000, OutputTokens: 10})
	if got := strings.Join(s.failoverAttemptOrder(scheduleInput{chain: chain, mode: scheduleModeLeastUsed, keyID: "k"}), ","); got != "p3,p2,p1" {
		t.Fatalf("cached traffic must not look like load, got %v", got)
	}
}

func TestFailoverAttemptOrderQuota(t *testing.T) {
	t.Parallel()
	s := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	s.oauthUsageCache.set("claude:busy", ClaudeOAuthUsageReport{
		Available: true, FetchedAt: time.Now().Format(time.RFC3339),
		FiveHour: &ClaudeOAuthUsageBucket{Utilization: 95},
	})
	s.oauthUsageCache.set("claude:fresh", ClaudeOAuthUsageReport{
		Available: true, FetchedAt: time.Now().Format(time.RFC3339),
		FiveHour: &ClaudeOAuthUsageBucket{Utilization: 10},
		SevenDay: &ClaudeOAuthUsageBucket{Utilization: 40},
	})
	s.oauthUsageCache.set("claude:stale", ClaudeOAuthUsageReport{
		Available: true, FetchedAt: time.Now().Add(-time.Hour).Format(time.RFC3339),
		FiveHour: &ClaudeOAuthUsageBucket{Utilization: 0},
	})
	s.oauthUsageCache.set("deepseek:out", DeepSeekBalanceReport{Available: true, FetchedAt: time.Now().Format(time.RFC3339)})
	chain := []string{"busy", "unknown", "fresh", "stale"}
	if got := strings.Join(s.failoverAttemptOrder(scheduleInput{chain: chain, mode: scheduleModeQuota, keyID: "k"}), ","); got != "fresh,unknown,stale,busy" {
		t.Fatalf("got %v", got)
	}
	// No window numbers: the vendor's own availability flag is all there is, and
	// an account that says it can no longer serve sits behind everything else.
	remaining := append([]string{"out"}, chain...)
	if got := strings.Join(s.failoverAttemptOrder(scheduleInput{chain: remaining, mode: scheduleModeQuota, keyID: "k"}), ","); got != "fresh,unknown,stale,busy,out" {
		t.Fatalf("got %v", got)
	}
	// A fully exhausted account is last, ahead only of the cooled-down ones.
	s.oauthUsageCache.set("claude:empty", ClaudeOAuthUsageReport{
		Available: true, FetchedAt: time.Now().Format(time.RFC3339),
		FiveHour: &ClaudeOAuthUsageBucket{Utilization: 100},
	})
	if got := strings.Join(s.failoverAttemptOrder(scheduleInput{chain: []string{"fresh", "empty"}, mode: scheduleModeQuota, keyID: "k"}), ","); got != "fresh,empty" {
		t.Fatalf("got %v", got)
	}
}

// fixtureOption tweaks the key the fixture builds, so a test can ask for a
// scheduling mode without re-declaring the whole wiring.
type fixtureOption func(*domain.APIKey)

func withScheduleMode(mode string) fixtureOption {
	return func(key *domain.APIKey) { key.ScheduleMode = mode }
}

type failoverFixture struct {
	server      *Server
	router      *Router
	preferred   *atomic.Int64
	backup      *atomic.Int64
	preferredID string
	backupID    string
	preferredUp *atomic.Int64
	backupUp    *atomic.Int64
}

// newFailoverFixture wires two OpenAI-Chat upstreams behind one route and one
// API key with a fallback chain. preferredStatus/backupStatus are what each
// upstream answers with; 0 means a normal 200 completion, and a test can flip
// them later with setPreferredStatus/setBackupStatus.
func newFailoverFixture(t *testing.T, preferredStatus, backupStatus int, activeProvider string, opts ...fixtureOption) *failoverFixture {
	t.Helper()
	preferredHits := &atomic.Int64{}
	backupHits := &atomic.Int64{}
	preferredUp := &atomic.Int64{}
	backupUp := &atomic.Int64{}
	preferredUp.Store(int64(preferredStatus))
	backupUp.Store(int64(backupStatus))
	upstream := func(name string, hits *atomic.Int64, up *atomic.Int64) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			// A couple of milliseconds of work, so an attempt's recorded
			// duration is a real number instead of a rounding artifact.
			time.Sleep(3 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			if failStatus := int(up.Load()); failStatus != 0 {
				w.WriteHeader(failStatus)
				_, _ = w.Write([]byte(`{"error":{"message":"` + name + ` unavailable"}}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "chatcmpl-" + name, "object": "chat.completion",
				"choices": []any{map[string]any{
					"index": 0, "finish_reason": "stop",
					"message": map[string]any{"role": "assistant", "content": name},
				}},
				"usage": map[string]any{"prompt_tokens": 4, "completion_tokens": 1, "total_tokens": 5},
			})
		}))
	}
	preferredServer := upstream("preferred", preferredHits, preferredUp)
	backupServer := upstream("backup", backupHits, backupUp)
	t.Cleanup(preferredServer.Close)
	t.Cleanup(backupServer.Close)

	preferred := domain.Provider{
		ID: "p-pref", Name: "p-pref", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeAPIKey, BaseURL: preferredServer.URL + "/v1", APIKeySource: "sk-a", HealthStatus: "healthy",
	}
	backup := domain.Provider{
		ID: "p-back", Name: "p-back", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeAPIKey, BaseURL: backupServer.URL + "/v1", APIKeySource: "sk-b", HealthStatus: "healthy",
	}
	route := domain.Route{ID: "r-sched", Name: "r-sched", ProviderID: preferred.ID, OutputProtocol: domain.ProtocolOpenAIChat, Mode: domain.RouteModeAuto, Enabled: true}
	apiKey := domain.APIKey{
		ID: "k-sched", Name: "k-sched", Key: "sk-gw-test-schedule", RouteID: route.ID,
		Enabled: true, StreamEnabled: true,
		FallbackProviderIDs: []string{backup.ID},
		ActiveProviderID:    activeProvider,
	}
	for _, apply := range opts {
		apply(&apiKey)
	}
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{preferred, backup},
		Routes:    []domain.Route{route},
		APIKeys:   []domain.APIKey{apiKey},
	})
	return &failoverFixture{
		server:      NewServer(router, monitor.NewStore()),
		router:      router,
		preferred:   preferredHits,
		backup:      backupHits,
		preferredID: preferred.ID,
		backupID:    backup.ID,
		preferredUp: preferredUp,
		backupUp:    backupUp,
	}
}

// setPreferredStatus / setBackupStatus flip what an upstream answers with, so a
// test can take an account down and bring it back between turns of one
// conversation. 0 restores a normal completion.
func (f *failoverFixture) setPreferredStatus(status int) { f.preferredUp.Store(int64(status)) }
func (f *failoverFixture) setBackupStatus(status int)    { f.backupUp.Store(int64(status)) }

func (f *failoverFixture) request(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	return f.requestWithSession(t, "")
}

// requestWithSession sends one turn of a conversation. An empty session sends
// no conversation identity at all, which is how a client that never names its
// session behaves.
func (f *failoverFixture) requestWithSession(t *testing.T, session string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-requested","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-gw-test-schedule")
	req.Header.Set("Content-Type", "application/json")
	if session != "" {
		req.Header.Set("X-Session-Id", session)
	}
	rec := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(rec, req)
	return rec
}

func (f *failoverFixture) activeProviderID(t *testing.T) string {
	t.Helper()
	key, ok := f.router.APIKeyByToken("sk-gw-test-schedule")
	if !ok {
		t.Fatal("api key disappeared")
	}
	return key.ActiveProviderID
}

// A key parked on a backup must still reach the preferred provider inside the
// same request once that backup fails. The old loop started at the sticky index
// and walked only forward, so the recovered primary was unreachable until the
// background probe switched back up to two minutes later.
func TestFailoverRetriesProvidersAheadOfSticky(t *testing.T) {
	f := newFailoverFixture(t, 0, http.StatusServiceUnavailable, "p-back")
	rec := f.request(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the preferred provider to serve after the sticky backup failed, got %d: %s", rec.Code, rec.Body.String())
	}
	if f.preferred.Load() != 1 || f.backup.Load() != 1 {
		t.Fatalf("expected one hit each, preferred=%d backup=%d", f.preferred.Load(), f.backup.Load())
	}
	if got := f.activeProviderID(t); got != "" {
		t.Fatalf("success on the preferred provider should clear the sticky backup, got %q", got)
	}
}

// A provider currently marked unavailable is tried last, so a single transient
// failure on the preferred provider does not make every request pay for it.
func TestFailoverTriesHealthyBackupBeforeUnavailablePreferred(t *testing.T) {
	f := newFailoverFixture(t, 0, 0, "")
	f.server.markProviderUnavailable(f.preferredID, true, "test", "", nil)
	rec := f.request(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the healthy backup to serve, got %d: %s", rec.Code, rec.Body.String())
	}
	if f.preferred.Load() != 0 {
		t.Fatalf("unavailable preferred must not be tried first, hits=%d", f.preferred.Load())
	}
	if f.backup.Load() != 1 {
		t.Fatalf("expected exactly one backup hit, got %d", f.backup.Load())
	}
	if got := f.activeProviderID(t); got != f.backupID {
		t.Fatalf("the serving backup should become sticky, got %q", got)
	}
}

// The preferred provider answers normally even though it is marked unavailable:
// the mark is a hint from a past failure, not a hard block, so the chain is
// still tried in the configured order when nothing else is healthy.
func TestFailoverStillTriesAllWhenEverythingIsMarkedUnavailable(t *testing.T) {
	f := newFailoverFixture(t, 0, 0, "")
	f.server.markProviderUnavailable(f.preferredID, true, "test", "", nil)
	f.server.markProviderUnavailable(f.backupID, true, "test", "", nil)
	rec := f.request(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the chain to still be tried, got %d: %s", rec.Code, rec.Body.String())
	}
	if f.preferred.Load() != 1 || f.backup.Load() != 0 {
		t.Fatalf("expected the first chain entry to serve, preferred=%d backup=%d", f.preferred.Load(), f.backup.Load())
	}
	if _, ok := f.server.providerNextRetry(f.preferredID); ok {
		t.Fatal("a successful live request must clear the unavailable mark")
	}
}

// A plain 500 is a request-shaped failure, not a provider-shaped one: it must
// not burn the fallback chain.
func TestFailoverDoesNotAdvanceOnNonFailoverStatus(t *testing.T) {
	f := newFailoverFixture(t, http.StatusInternalServerError, 0, "")
	rec := f.request(t)
	if rec.Code == http.StatusOK {
		t.Fatal("expected the 500 to reach the client")
	}
	if f.backup.Load() != 0 {
		t.Fatalf("a 500 must not advance the chain, backup hits=%d", f.backup.Load())
	}
}

// Round-robin spreads a key's own traffic over its chain instead of parking
// everything on the preferred account until it fails.
func TestScheduleModeRotateAlternatesRequests(t *testing.T) {
	f := newFailoverFixture(t, 0, 0, "", withScheduleMode(scheduleModeRotate))
	first := f.request(t)
	second := f.request(t)
	third := f.request(t)
	if !strings.Contains(first.Body.String(), "preferred") {
		t.Fatalf("first turn should use the preferred provider: %s", first.Body.String())
	}
	if !strings.Contains(second.Body.String(), "backup") {
		t.Fatalf("second turn should rotate to the backup: %s", second.Body.String())
	}
	if !strings.Contains(third.Body.String(), "preferred") {
		t.Fatalf("third turn should wrap back to the preferred: %s", third.Body.String())
	}
	if f.preferred.Load() != 2 || f.backup.Load() != 1 {
		t.Fatalf("expected 2/1 hits, preferred=%d backup=%d", f.preferred.Load(), f.backup.Load())
	}
}

// least_used follows the work each account actually carried: uncached input
// plus output, so an account is not punished for serving cache hits.
func TestScheduleModeLeastUsedSpreadsLoad(t *testing.T) {
	f := newFailoverFixture(t, 0, 0, "", withScheduleMode(scheduleModeLeastUsed))
	first := f.request(t)
	second := f.request(t)
	third := f.request(t)
	if !strings.Contains(first.Body.String(), "preferred") {
		t.Fatalf("with both accounts idle the chain order decides: %s", first.Body.String())
	}
	if !strings.Contains(second.Body.String(), "backup") {
		t.Fatalf("the loaded account should step aside: %s", second.Body.String())
	}
	if !strings.Contains(third.Body.String(), "preferred") {
		t.Fatalf("once both are equal the chain order returns: %s", third.Body.String())
	}
	if f.preferred.Load() != 2 || f.backup.Load() != 1 {
		t.Fatalf("expected 2/1 hits, preferred=%d backup=%d", f.preferred.Load(), f.backup.Load())
	}
}

// A conversation's session pin still outranks the strategy: rotate may want the
// next account, but moving a conversation off the account that holds its prompt
// cache costs more than the imbalance saves.
func TestSessionPinOutranksScheduleMode(t *testing.T) {
	f := newFailoverFixture(t, 0, 0, "", withScheduleMode(scheduleModeRotate))
	if rec := f.requestWithSession(t, "chat-a"); rec.Code != http.StatusOK {
		t.Fatalf("first turn failed: %d", rec.Code)
	}
	for i := 0; i < 4; i++ {
		if rec := f.requestWithSession(t, "chat-a"); rec.Code != http.StatusOK {
			t.Fatalf("turn %d failed", i)
		}
	}
	if f.preferred.Load() != 5 || f.backup.Load() != 0 {
		t.Fatalf("the conversation should have stayed on one account, preferred=%d backup=%d", f.preferred.Load(), f.backup.Load())
	}
}

// spread mode gives every conversation an account of its own: the second
// conversation opening on the same chain must not land on the account that is
// already holding the first one.
func TestScheduleModeSpreadGivesEachConversationItsOwnAccount(t *testing.T) {
	f := newFailoverFixture(t, 0, 0, "", withScheduleMode(scheduleModeSpread))
	// chat-a opens first and takes the head of the chain.
	if rec := f.requestWithSession(t, "chat-a"); rec.Code != http.StatusOK {
		t.Fatalf("chat-a turn failed: %d", rec.Code)
	}
	if !strings.Contains(f.requestWithSession(t, "chat-a").Body.String(), "preferred") {
		t.Fatal("chat-a should have kept the account it was given")
	}
	// chat-b arrives with the preferred account already holding one
	// conversation, so it goes to the one holding none.
	second := f.requestWithSession(t, "chat-b")
	if !strings.Contains(second.Body.String(), "backup") {
		t.Fatalf("the second conversation should have taken the free account: %s", second.Body.String())
	}
	for i := 0; i < 3; i++ {
		if rec := f.requestWithSession(t, "chat-b"); rec.Code != http.StatusOK {
			t.Fatalf("chat-b turn %d failed", i)
		}
	}
	// Two conversations, two accounts, one each.
	if f.preferred.Load() != 2 || f.backup.Load() != 4 {
		t.Fatalf("expected a 2/4 split, preferred=%d backup=%d", f.preferred.Load(), f.backup.Load())
	}
}

// Once the pinning account fails, the conversation moves to another account and
// stays there — the failure is a move, not a return to the old account on the
// next turn.
func TestScheduleModeSpreadFollowsTheConversationAcrossFailover(t *testing.T) {
	f := newFailoverFixture(t, 0, 0, "", withScheduleMode(scheduleModeSpread))
	if rec := f.requestWithSession(t, "chat-a"); rec.Code != http.StatusOK {
		t.Fatalf("first turn failed: %d", rec.Code)
	}
	f.setPreferredStatus(http.StatusTooManyRequests)
	if rec := f.requestWithSession(t, "chat-a"); rec.Code != http.StatusOK {
		t.Fatalf("the conversation should have failed over, got %d: %s", rec.Code, rec.Body.String())
	}
	// The account is down for new turns too, but chat-a now belongs to the
	// backup and must not even try the failed one again.
	f.setPreferredStatus(0)
	before := f.preferred.Load()
	if rec := f.requestWithSession(t, "chat-a"); rec.Code != http.StatusOK {
		t.Fatalf("third turn failed: %d", rec.Code)
	}
	if f.preferred.Load() != before {
		t.Fatalf("the conversation went back to the failed account, hits=%d", f.preferred.Load())
	}
	// A different conversation is free to take the recovered account.
	other := f.requestWithSession(t, "chat-c")
	if !strings.Contains(other.Body.String(), "preferred") {
		t.Fatalf("a new conversation should take the free account: %s", other.Body.String())
	}
}

// A conversation held by the spread mode keeps its account longer than the
// default window, because that account is what the mode promised it.
func TestSpreadModeSessionPinOutlivesTheDefaultWindow(t *testing.T) {
	t.Parallel()
	s := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	s.setSessionPinFor("session-spread", "p1", s.sessionPinTTL(scheduleModeSpread))
	pin, ok := s.pinStore().pins["session-spread"]
	if !ok {
		t.Fatal("the pin was not stored")
	}
	held := time.Until(pin.expiresAt)
	if held <= sessionAffinityTTL {
		t.Fatalf("spread pins must outlive the default window, held %v", held)
	}
	if s.sessionPinTTL(scheduleModeRotate) != sessionAffinityTTL {
		t.Fatal("the other modes must keep the default window")
	}
}

// Without a conversation identity spread cannot tell conversations apart, so
// it falls back to the load signals instead of never balancing at all.
func TestScheduleModeSpreadWithoutSessionFallsBackToLoad(t *testing.T) {
	t.Parallel()
	s := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	s.noteProviderUsage("p1", TokenUsage{InputTokens: 4000, OutputTokens: 200})
	chain := []string{"p1", "p2"}
	if got := strings.Join(s.failoverAttemptOrder(scheduleInput{chain: chain, mode: scheduleModeSpread, keyID: "k"}), ","); got != "p2,p1" {
		t.Fatalf("the loaded account should step aside, got %v", got)
	}
}

// spread counts conversations, not requests: a conversation that has answered
// many turns still occupies one slot.
func TestSpreadChainCountsSessionsNotTurns(t *testing.T) {
	t.Parallel()
	s := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	chain := []string{"p1", "p2", "p3"}
	s.setSessionPin("s-a", "p1")
	s.setSessionPin("s-b", "p2")
	if got := strings.Join(s.spreadChain(chain), ","); got != "p3,p1,p2" {
		t.Fatalf("the account holding no conversation leads, got %v", got)
	}
	s.setSessionPin("s-c", "p3")
	s.setSessionPin("s-d", "p1")
	// p1 now holds two; the tie between p2 and p3 keeps the chain order.
	if got := strings.Join(s.spreadChain(chain), ","); got != "p2,p3,p1" {
		t.Fatalf("got %v", got)
	}
	// Expired conversations stop occupying a slot.
	store := s.pinStore()
	store.mu.Lock()
	store.pins["s-c"] = sessionPin{providerID: "p3", expiresAt: time.Now().Add(-time.Minute)}
	store.mu.Unlock()
	if got := strings.Join(s.spreadChain(chain), ","); got != "p3,p2,p1" {
		t.Fatalf("an expired conversation must free its account, got %v", got)
	}
}

// An attempt that never got an answer must not leave the conversation anchored
// to that account: the next turn has to choose again from scratch.
func TestScheduleModeSpreadDropsTheReservationWhenNothingServed(t *testing.T) {
	f := newFailoverFixture(t, 0, 0, "", withScheduleMode(scheduleModeSpread))
	f.setPreferredStatus(http.StatusServiceUnavailable)
	f.setBackupStatus(http.StatusServiceUnavailable)
	if rec := f.requestWithSession(t, "chat-down"); rec.Code == http.StatusOK {
		t.Fatal("expected the turn to fail when both accounts are down")
	}
	sessionKey := sessionAffinityKey(nil, map[string]any{"session_id": "chat-down"}, "k-sched", f.preferredID, scheduleModeSpread, f.backupID)
	if pinned := f.server.pinStore().sessionPin(sessionKey); pinned != "" {
		t.Fatalf("a failed turn must not pin the conversation, got %q", pinned)
	}
	// Both accounts recover: the conversation is free to be placed again.
	f.setPreferredStatus(0)
	if rec := f.requestWithSession(t, "chat-down"); rec.Code != http.StatusOK {
		t.Fatalf("the next turn should be served, got %d: %s", rec.Code, rec.Body.String())
	}
	if pinned := f.server.pinStore().sessionPin(sessionKey); pinned == "" {
		t.Fatal("the served turn must pin the conversation")
	}
}

// The last candidate refusing the turn is not "the account that answered":
// no mode may pin the conversation to it, or the next turn would be sent
// straight back to an account that just refused.
func TestAServedRefusalDoesNotPinTheConversation(t *testing.T) {
	f := newFailoverFixture(t, 0, 0, "")
	f.setPreferredStatus(http.StatusServiceUnavailable)
	f.setBackupStatus(http.StatusServiceUnavailable)
	if rec := f.requestWithSession(t, "chat-refused"); rec.Code == http.StatusOK {
		t.Fatal("expected the turn to fail")
	}
	sessionKey := sessionAffinityKey(nil, map[string]any{"session_id": "chat-refused"}, "k-sched", f.preferredID, "", f.backupID)
	if pinned := f.server.pinStore().sessionPin(sessionKey); pinned != "" {
		t.Fatalf("a refusal must not pin the conversation, got %q", pinned)
	}
}
