package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// quotaFixture is one key with one quota shape, in front of an upstream that can be
// held open. Holding it open is the point: a quota that only counts finished
// requests cannot be tested without proving what a concurrent burst sees.
type quotaFixture struct {
	server     *Server
	logs       *monitor.Store
	upstream   *atomic.Int64
	enter      chan struct{}
	proceed    chan struct{}
	keyID      string
	token      string
	releaseAll sync.Once
}

func newQuotaFixture(t *testing.T, quota func(*domain.APIKey), gated bool) *quotaFixture {
	t.Helper()
	hits := &atomic.Int64{}
	enter := make(chan struct{}, 8)
	proceed := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if gated {
			enter <- struct{}{}
			<-proceed
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-quota", "object": "chat.completion",
			"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": "done"}}},
			"usage":   map[string]any{"prompt_tokens": 4, "completion_tokens": 1, "total_tokens": 5},
		})
	}))
	t.Cleanup(upstream.Close)

	provider := domain.Provider{ID: "p-quota", Name: "p-quota", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeAPIKey, BaseURL: upstream.URL + "/v1", APIKeySource: "sk-a"}
	route := domain.Route{ID: "r-quota", Name: "r-quota", ProviderID: provider.ID,
		OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true}
	apiKey := domain.APIKey{ID: "k-quota", Name: "k-quota", Key: "sk-gw-quota", RouteID: route.ID,
		Enabled: true, StreamEnabled: true}
	quota(&apiKey)

	logs := monitor.NewStore()
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{provider},
		Routes:    []domain.Route{route},
		APIKeys:   []domain.APIKey{apiKey},
	})
	server := NewServer(router, logs)
	return &quotaFixture{server: server, logs: logs, upstream: hits, enter: enter, proceed: proceed,
		keyID: apiKey.ID, token: apiKey.Key}
}

// seed puts completed usage into a key's day bucket the same way a finished request
// would, so the quota reads exactly what production reads.
func (f *quotaFixture) seed(t *testing.T, at time.Time, tokens int64, costMicros int64) {
	t.Helper()
	f.logs.ApplyUsageEventSync(monitor.UsageEvent{
		Time: at, APIKeyID: f.keyID, APIKeyName: f.keyID, ProviderID: "p-quota", Model: "mock-model",
		OutputProtocol: "OpenAI Chat", Status: 200, InputTokens: tokens, CostMicros: costMicros,
		Priced: costMicros > 0,
	})
}

func (f *quotaFixture) send(t *testing.T, content string) (int, string, http.Header) {
	t.Helper()
	payload := `{"model":"mock-model","messages":[{"role":"user","content":"` + content + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), rec.Header()
}

func (f *quotaFixture) waitUpstream(t *testing.T) {
	t.Helper()
	select {
	case <-f.enter:
	case <-time.After(3 * time.Second):
		t.Fatal("the upstream never received the request")
	}
}

func (f *quotaFixture) unblock() { f.releaseAll.Do(func() { close(f.proceed) }) }

func dayWindowQuota(tokens int64) func(*domain.APIKey) {
	return func(key *domain.APIKey) { key.QuotaWindow = "day"; key.QuotaTokens = tokens }
}

// A day window that has already spent its budget refuses the request before the
// upstream is contacted, and tells the client when to come back.
func TestKeyQuotaRefusesExhaustedWindow(t *testing.T) {
	fixture := newQuotaFixture(t, dayWindowQuota(1000), false)
	t.Cleanup(fixture.unblock)
	fixture.seed(t, time.Now(), 995, 0)

	status, body, header := fixture.send(t, "hi")
	if status != http.StatusTooManyRequests {
		t.Fatalf("exhausted key: got %d (%s), want 429", status, body)
	}
	if fixture.upstream.Load() != 0 {
		t.Fatalf("a quota refusal must not spend the upstream account: %d hits", fixture.upstream.Load())
	}
	if header.Get("Retry-After") == "" {
		t.Fatal("a quota refusal must say when to retry")
	}
	resetAt := header.Get("X-Quota-Reset-At")
	if _, err := time.Parse(time.RFC3339, resetAt); err != nil {
		t.Fatalf("reset header must be a timestamp, got %q (%v)", resetAt, err)
	}
	if !strings.Contains(body, "token 额度") {
		t.Fatalf("client message must name the exhausted budget: %s", body)
	}

	// Same key, budget still available: the request goes through and holds nothing
	// after it finishes.
	roomy := newQuotaFixture(t, dayWindowQuota(1000), false)
	t.Cleanup(roomy.unblock)
	roomy.seed(t, time.Now(), 10, 0)
	if status, _, _ := roomy.send(t, "hi"); status != http.StatusOK {
		t.Fatalf("under budget: got %d, want 200", status)
	}
	if pending := roomy.server.keyQuota.pending(roomy.keyID); pending != 0 {
		t.Fatalf("reservation leaked after a completed request: %d", pending)
	}
}

// Completed usage is the easy half. The reason the reservation exists is that a
// request in flight has contributed nothing to any bucket yet: without it, a burst
// of concurrent clients would each read "0 used" and all get through.
func TestKeyQuotaReservesInFlightRequests(t *testing.T) {
	fixture := newQuotaFixture(t, dayWindowQuota(1000), true)
	t.Cleanup(fixture.unblock)

	first := make(chan int, 1)
	go func() {
		// ~600 estimated prompt tokens: a body of 2400 ascii bytes at 4 chars/token.
		status, _, _ := fixture.send(t, strings.Repeat("a", 2400))
		first <- status
	}()
	fixture.waitUpstream(t)

	second := strings.Repeat("b", 2400)
	status, body, _ := fixture.send(t, second)
	if status != http.StatusTooManyRequests {
		t.Fatalf("second concurrent request: got %d (%s), want 429", status, body)
	}
	if hits := fixture.upstream.Load(); hits != 1 {
		t.Fatalf("the refused burst must not reach the upstream a second time, got %d hits", hits)
	}

	fixture.unblock()
	if code := <-first; code != http.StatusOK {
		t.Fatalf("the request that fitted the window: got %d, want 200", code)
	}

	// Once the in-flight request is done, its reservation is gone and the window
	// accounts for it through the completed usage instead.
	if pending := fixture.server.keyQuota.pending(fixture.keyID); pending != 0 {
		t.Fatalf("reservation not released: %d still held", pending)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if used := fixture.logs.KeyWindowUsage(fixture.keyID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour)); used.Tokens > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	after := fixture.logs.KeyWindowUsage(fixture.keyID, time.Now().Add(-24*time.Hour), time.Now().Add(24*time.Hour))
	if after.Tokens == 0 {
		t.Fatal("the finished request never landed in the window it was charged to")
	}
}

// The window is a calendar window, not a lifetime total: yesterday must not be
// charged to today, and must still be charged to the month.
func TestKeyQuotaWindowBoundaries(t *testing.T) {
	now := time.Now()
	yesterday := now.AddDate(0, 0, -1)

	dayKey := newQuotaFixture(t, dayWindowQuota(1000), false)
	t.Cleanup(dayKey.unblock)
	dayKey.seed(t, yesterday, 5000, 0)
	if status, body, _ := dayKey.send(t, "hi"); status != http.StatusOK {
		t.Fatalf("yesterday's spend must not follow a day window: got %d (%s)", status, body)
	}

	monthKey := newQuotaFixture(t, func(key *domain.APIKey) {
		key.QuotaWindow = "month"
		key.QuotaTokens = 1000
	}, false)
	t.Cleanup(monthKey.unblock)
	monthKey.seed(t, yesterday, 1200, 0)
	if status, _, _ := monthKey.send(t, "hi"); status != http.StatusTooManyRequests {
		t.Fatalf("a month window still owes yesterday's tokens: got %d, want 429", status)
	}
}

// Money is charged the same way, but only over requests that had a price row.
func TestKeyQuotaCostLimit(t *testing.T) {
	fixture := newQuotaFixture(t, func(key *domain.APIKey) {
		key.QuotaWindow = "day"
		key.QuotaCostMicros = 2_000_000 // $2.00
	}, false)
	t.Cleanup(fixture.unblock)
	fixture.seed(t, time.Now(), 800, 2_500_000)

	status, body, _ := fixture.send(t, "hi")
	if status != http.StatusTooManyRequests {
		t.Fatalf("over the money cap: got %d, want 429", status)
	}
	if !strings.Contains(body, "成本额度") {
		t.Fatalf("message must name the cost budget: %s", body)
	}
	// A token budget the key does not have must not accidentally gate anything:
	// QuotaTokens is 0 here, so only the cost cap applies.
	under := newQuotaFixture(t, func(key *domain.APIKey) {
		key.QuotaWindow = "day"
		key.QuotaCostMicros = 2_000_000
	}, false)
	t.Cleanup(under.unblock)
	under.seed(t, time.Now(), 1_000_000, 100)
	if status, body, _ := under.send(t, "hi"); status != http.StatusOK {
		t.Fatalf("a cheap day with huge token volume must pass a cost-only cap: got %d (%s)", status, body)
	}
}

// A refusal is written in the client's own protocol shape: a Claude SDK parses
// {"type":"error",...} and an OpenAI client parses {"error":{...}}.
func TestRejectKeyQuotaShapes(t *testing.T) {
	refusal := &quotaRefusal{Message: "额度已用尽", RetryAfterSeconds: 42, ResetAt: "2026-10-22T00:00:00+08:00", Kind: "tokens"}

	openaiRec := httptest.NewRecorder()
	rejectKeyQuota(openaiRec, domain.ProtocolOpenAIChat, refusal)
	var openaiBody map[string]any
	if err := json.Unmarshal(openaiRec.Body.Bytes(), &openaiBody); err != nil {
		t.Fatalf("openai shape is not json: %v (%s)", err, openaiRec.Body)
	}
	if inner, ok := openaiBody["error"].(map[string]any); !ok || inner["message"] != "额度已用尽" {
		t.Fatalf("openai shape: %s", openaiRec.Body.String())
	}
	if openaiRec.Code != http.StatusTooManyRequests || openaiRec.Header().Get("Retry-After") != "42" {
		t.Fatalf("openai status/headers: %d %q", openaiRec.Code, openaiRec.Header().Get("Retry-After"))
	}

	claudeRec := httptest.NewRecorder()
	rejectKeyQuota(claudeRec, domain.ProtocolClaude, refusal)
	var claudeBody map[string]any
	if err := json.Unmarshal(claudeRec.Body.Bytes(), &claudeBody); err != nil {
		t.Fatalf("claude shape is not json: %v", err)
	}
	if claudeBody["type"] != "error" {
		t.Fatalf("claude shape must carry type=error: %s", claudeRec.Body.String())
	}
	if claudeRec.Header().Get("X-Quota-Reset-At") == "" {
		t.Fatal("claude shape must carry the reset header too")
	}

	// A refusal error must still map to 429 through the shared status mapper.
	if got := upstreamErrorStatus(refusal); got != http.StatusTooManyRequests {
		t.Fatalf("upstreamErrorStatus(quota) = %d, want 429", got)
	}
	if refusal.Error() != "额度已用尽" {
		t.Fatalf("the client must see exactly the refusal message, got %q", refusal.Error())
	}
}

// quotaPatchRequest calls the handler directly (no middleware), so the console
// identity has to be injected the way withAdminAuth would inject it.
func quotaPatchRequest(body string, userID string, role domain.UserRole) *http.Request {
	request := httptest.NewRequest(http.MethodPatch, "/__apikeys/k-quota", strings.NewReader(body))
	request.SetPathValue("id", "k-quota")
	identity := adminIdentity()
	if userID != "" {
		identity = sessionIdentity{UserID: userID, Username: userID, Role: role}
	}
	return request.WithContext(context.WithValue(request.Context(), authContextKey{}, identity))
}

func TestAPIKeyQuotaCRUD(t *testing.T) {
	fixture := newQuotaFixture(t, func(key *domain.APIKey) {
		key.QuotaWindow = "day"
		key.QuotaTokens = 5000
		key.QuotaCostMicros = 1_000_000
	}, false)
	t.Cleanup(fixture.unblock)
	server := fixture.server
	// Hand the key to a console user so the "quota is admin-only" case is refused
	// for the reason under test, not because the caller does not own the key.
	if _, err := server.router.UpdateAPIKeyOwner("k-quota", "u-owner"); err != nil {
		t.Fatal(err)
	}

	patch := func(t *testing.T, body string, userID string, role domain.UserRole) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		server.handleUpdateAPIKey(rec, quotaPatchRequest(body, userID, role))
		return rec.Code, rec.Body.String()
	}

	current := func(t *testing.T) domain.APIKey {
		t.Helper()
		key, ok := server.router.APIKeyByID("k-quota")
		if !ok {
			t.Fatal("key vanished")
		}
		return key
	}

	// Patching something else must not clear the quota: the three fields were not
	// in the body.
	if status, body := patch(t, `{"name":"renamed"}`, "", domain.UserRoleAdmin); status != http.StatusOK {
		t.Fatalf("rename patch: %d %s", status, body)
	}
	after := current(t)
	if after.Name != "renamed" || after.QuotaWindow != "day" || after.QuotaTokens != 5000 || after.QuotaCostMicros != 1_000_000 {
		t.Fatalf("unrelated patch dropped the quota: %+v", after)
	}
	// ...and must not disable the key either: an absent "enabled" used to mean
	// false, which takes the key out of token resolution entirely.
	if !after.Enabled {
		t.Fatal("a patch without `enabled` must leave the key enabled")
	}

	// Sending only the window keeps the budgets (partial patch), and sending only a
	// budget keeps the window.
	if status, body := patch(t, `{"quotaWindow":"month"}`, "", domain.UserRoleAdmin); status != http.StatusOK {
		t.Fatalf("window patch: %d %s", status, body)
	}
	after = current(t)
	if after.QuotaWindow != "month" || after.QuotaTokens != 5000 || after.QuotaCostMicros != 1_000_000 {
		t.Fatalf("window-only patch lost the budgets: %+v", after)
	}
	if status, body := patch(t, `{"quotaTokens":9000}`, "", domain.UserRoleAdmin); status != http.StatusOK {
		t.Fatalf("token patch: %d %s", status, body)
	}
	if after = current(t); after.QuotaTokens != 9000 || after.QuotaWindow != "month" {
		t.Fatalf("token-only patch lost the window: %+v", after)
	}

	// Both budgets zero clears the window as well: a window that gates nothing is
	// noise in the console.
	if status, body := patch(t, `{"quotaTokens":0,"quotaCostMicros":0}`, "", domain.UserRoleAdmin); status != http.StatusOK {
		t.Fatalf("clear patch: %d %s", status, body)
	}
	if after = current(t); after.QuotaWindow != "" || after.QuotaTokens != 0 || after.QuotaCostMicros != 0 {
		t.Fatalf("clearing the budgets must clear the window: %+v", after)
	}

	// A limit with no window is refused instead of silently never resetting.
	if status, _ := patch(t, `{"quotaTokens":100,"quotaCostMicros":0,"quotaWindow":""}`, "", domain.UserRoleAdmin); status != http.StatusBadRequest {
		t.Fatalf("limit without a window: got %d, want 400", status)
	}
	// A mistyped window is refused rather than treated as one of the real ones.
	if status, _ := patch(t, `{"quotaWindow":"week","quotaTokens":100}`, "", domain.UserRoleAdmin); status != http.StatusBadRequest {
		t.Fatalf("unknown window: got %d, want 400", status)
	}
	// Quota is governance: the key's own owner may not raise their own budget.
	if status, _ := patch(t, `{"quotaWindow":"day","quotaTokens":999999}`, "u-owner", domain.UserRoleUser); status != http.StatusForbidden {
		t.Fatalf("non-admin quota write: got %d, want 403", status)
	}
	if after = current(t); after.QuotaWindow != "" || after.QuotaTokens != 0 {
		t.Fatalf("the refused write must have changed nothing: %+v", after)
	}
}
