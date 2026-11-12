package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestRequestSessionID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		headers map[string]string
		body    map[string]any
		want    string
	}{
		{name: "none", body: map[string]any{"model": "m"}},
		{name: "session header", headers: map[string]string{"X-Session-Id": " s-1 "}, body: map[string]any{}, want: "s-1"},
		{name: "claude code header", headers: map[string]string{"X-Claude-Code-Session-Id": "cc-9"}, want: "cc-9"},
		{name: "body session id", body: map[string]any{"session_id": "b-7"}, want: "b-7"},
		{name: "header wins over body", headers: map[string]string{"Session_id": "h-1"}, body: map[string]any{"session_id": "b-1"}, want: "h-1"},
		{name: "metadata session", body: map[string]any{"metadata": map[string]any{"session_id": "m-3"}}, want: "m-3"},
		{name: "claude code user id suffix", body: map[string]any{"metadata": map[string]any{"user_id": "user_42_session_abc"}}, want: "abc"},
		// A bare user id is shared by several conversations, so it must never
		// become one session (that would move unrelated chats together).
		{name: "bare user id is not a session", body: map[string]any{"metadata": map[string]any{"user_id": "user-42"}}},
		{name: "codex client metadata session", body: map[string]any{"client_metadata": map[string]any{"session_id": "codex-s-1"}}, want: "codex-s-1"},
		{name: "codex client metadata thread fallback", body: map[string]any{"client_metadata": map[string]any{"thread_id": "codex-t-1"}}, want: "codex-t-1"},
		{name: "prompt cache key", body: map[string]any{"prompt_cache_key": "codex-conv-1"}, want: "codex-conv-1"},
		{name: "blank values ignored", body: map[string]any{"session_id": "  ", "prompt_cache_key": "pk"}, want: "pk"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r *http.Request
			if len(tc.headers) > 0 {
				r = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
				for name, value := range tc.headers {
					r.Header.Set(name, value)
				}
			}
			if got := requestSessionID(r, tc.body); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
	// The same conversation on two different gateway keys must not share a pin.
	req := map[string]any{"session_id": "same"}
	if sessionAffinityKey(nil, req, "key-a", "p1", "") == sessionAffinityKey(nil, req, "key-b", "p1", "") {
		t.Fatal("pin key must be scoped to the gateway key")
	}
	if sessionAffinityKey(nil, req, "key-a", "p1", "") != sessionAffinityKey(nil, req, "key-a", "p1", "") {
		t.Fatal("pin key must be stable")
	}
	if got := sessionAffinityKey(nil, map[string]any{}, "key-a", "p1", ""); got != "" {
		t.Fatalf("no session id must mean no pin, got %q", got)
	}
	if sessionAffinityKey(nil, req, "key-a", "p1", "") == sessionAffinityKey(nil, req, "key-a", "p2", "") {
		t.Fatal("changing the primary provider must start a fresh session-pin namespace")
	}
	if sessionAffinityKey(nil, req, "key-a", "p1", "", "b1") == sessionAffinityKey(nil, req, "key-a", "p1", "") {
		t.Fatal("adding a fallback provider must start a fresh session-pin namespace")
	}
	if sessionAffinityKey(nil, req, "key-a", "p1", "", "b1") == sessionAffinityKey(nil, req, "key-a", "p1", "", "b2") {
		t.Fatal("changing the fallback provider must start a fresh session-pin namespace")
	}
	if sessionAffinityKey(nil, req, "key-a", "p1", "", "b1", "b2") == sessionAffinityKey(nil, req, "key-a", "p1", "", "b2", "b1") {
		t.Fatal("fallback order must be part of the session-pin namespace")
	}
	if sessionAffinityKey(nil, req, "key-a", "p1", "") != sessionAffinityKey(nil, req, "key-a", "p1", "order") {
		t.Fatal("empty and explicit order modes must share one session-pin namespace")
	}
	if sessionAffinityKey(nil, req, "key-a", "p1", "order") == sessionAffinityKey(nil, req, "key-a", "p1", "spread") {
		t.Fatal("changing the schedule mode must start a fresh session-pin namespace")
	}
	if sessionAffinityKey(nil, req, "key-a", "p1", "spread") == sessionAffinityKey(nil, req, "key-a", "p1", "rotate") {
		t.Fatal("every scheduling mode change must invalidate the old pin namespace")
	}
}

// 链路配置变化后，旧会话 pin 不能再影响新链：首选、备选或备选顺序任一变化
// 都会生成新的命名空间。
func TestChangingProviderChainInvalidatesSessionPin(t *testing.T) {
	t.Parallel()
	s := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	req := map[string]any{"session_id": "chain-change"}
	oldKey := sessionAffinityKey(nil, req, "k1", "p-primary", "", "p-old-fallback")
	newKey := sessionAffinityKey(nil, req, "k1", "p-primary", "", "p-new-fallback")
	s.setSessionPin(oldKey, "p-old-fallback")
	attempts, pinned := s.applySessionPin(newKey, []string{"p-primary", "p-new-fallback"})
	if pinned != "" || strings.Join(attempts, ",") != "p-primary,p-new-fallback" {
		t.Fatalf("old chain pin leaked into the new chain: attempts=%v pinned=%q", attempts, pinned)
	}
}

func TestMoveProviderToFront(t *testing.T) {
	t.Parallel()
	chain := []string{"a", "b", "c"}
	if got := strings.Join(moveProviderToFront(chain, "c"), ","); got != "c,a,b" {
		t.Fatalf("got %v", got)
	}
	if got := strings.Join(moveProviderToFront(chain, "a"), ","); got != "a,b,c" {
		t.Fatalf("already first must be a no-op, got %v", got)
	}
	if got := strings.Join(moveProviderToFront(chain, "ghost"), ","); got != "a,b,c" {
		t.Fatalf("unknown provider must be a no-op, got %v", got)
	}
	if got := strings.Join(moveProviderToFront(chain, ""), ","); got != "a,b,c" {
		t.Fatalf("empty provider must be a no-op, got %v", got)
	}
	if strings.Join(chain, ",") != "a,b,c" {
		t.Fatal("source chain must not be mutated")
	}
}

// Two conversations on one key each stay on the account that has their prompt
// cache, even after the key-level sticky provider moved.
func TestSessionAffinityKeepsConversationsOnTheirOwnAccount(t *testing.T) {
	f := newFailoverFixture(t, 0, 0, "")
	if rec := f.requestWithSession(t, "chat-a"); rec.Code != http.StatusOK {
		t.Fatalf("first turn failed: %d %s", rec.Code, rec.Body.String())
	}
	if f.preferred.Load() != 1 || f.backup.Load() != 0 {
		t.Fatalf("expected the preferred provider on a fresh session, preferred=%d backup=%d", f.preferred.Load(), f.backup.Load())
	}

	// A second conversation arrives while the preferred account is down: it
	// lands on the backup, and the key becomes sticky to the backup.
	f.setPreferredStatus(http.StatusServiceUnavailable)
	if rec := f.requestWithSession(t, "chat-b"); rec.Code != http.StatusOK {
		t.Fatalf("chat-b failed: %d %s", rec.Code, rec.Body.String())
	}
	if got := f.activeProviderID(t); got != f.backupID {
		t.Fatalf("expected the key to go sticky to the backup, got %q", got)
	}

	// Preferred recovers. chat-a must stay where its cache is (preferred) even
	// though the key now points at the backup, and chat-b must stay on backup.
	f.setPreferredStatus(0)
	preferredBefore, backupBefore := f.preferred.Load(), f.backup.Load()
	if rec := f.requestWithSession(t, "chat-a"); rec.Code != http.StatusOK {
		t.Fatalf("chat-a second turn failed: %d", rec.Code)
	}
	if f.preferred.Load() != preferredBefore+1 || f.backup.Load() != backupBefore {
		t.Fatalf("chat-a should have used the preferred provider: preferred=%d backup=%d", f.preferred.Load(), f.backup.Load())
	}
	if rec := f.requestWithSession(t, "chat-b"); rec.Code != http.StatusOK {
		t.Fatalf("chat-b second turn failed: %d", rec.Code)
	}
	if f.backup.Load() != backupBefore+1 {
		t.Fatalf("chat-b should have stayed on the backup, backup=%d", f.backup.Load())
	}
	if got := f.activeProviderID(t); got != f.backupID {
		t.Fatalf("a session-pinned turn must not rewrite the key's sticky provider, got %q", got)
	}
}

// When the pinned account fails mid-conversation the conversation follows the
// failover and re-pins to whoever answered, so the next turn does not walk the
// whole chain again.
func TestSessionAffinityFollowsAFailover(t *testing.T) {
	f := newFailoverFixture(t, 0, 0, "")
	if rec := f.requestWithSession(t, "chat-a"); rec.Code != http.StatusOK {
		t.Fatalf("first turn failed: %d", rec.Code)
	}
	f.setPreferredStatus(http.StatusServiceUnavailable)
	if rec := f.requestWithSession(t, "chat-a"); rec.Code != http.StatusOK {
		t.Fatalf("failover turn failed: %d", rec.Code)
	}
	if f.backup.Load() != 1 {
		t.Fatalf("expected the backup to take over, backup=%d", f.backup.Load())
	}
	f.setPreferredStatus(0)
	// The background recovery probe has now switched the key back to the
	// preferred provider. Without a session pin this conversation would follow
	// the key and lose the cache the backup account built for it.
	if _, err := f.router.SetAPIKeyActiveProvider("k-sched", ""); err != nil {
		t.Fatal(err)
	}
	// The hit counter for the preferred provider already includes the failed
	// attempt above, so compare against the counts as they stand now.
	preferredBefore, backupBefore := f.preferred.Load(), f.backup.Load()
	if rec := f.requestWithSession(t, "chat-a"); rec.Code != http.StatusOK {
		t.Fatalf("third turn failed: %d", rec.Code)
	}
	if f.preferred.Load() != preferredBefore || f.backup.Load() != backupBefore+1 {
		t.Fatalf("the conversation should follow its new pin, preferred=%d backup=%d (before %d/%d)",
			f.preferred.Load(), f.backup.Load(), preferredBefore, backupBefore)
	}
	if got := f.activeProviderID(t); got != "" {
		t.Fatalf("a session-pinned turn must not rewrite the key's sticky provider, got %q", got)
	}
}

// A request with no conversation identity keeps today's behavior: the key's
// sticky provider decides, and nothing is pinned.
func TestSessionAffinityIgnoresAnonymousRequests(t *testing.T) {
	f := newFailoverFixture(t, 0, 0, "")
	if rec := f.request(t); rec.Code != http.StatusOK {
		t.Fatalf("anonymous request failed: %d", rec.Code)
	}
	if store := f.server.pinStore(); len(store.pins) != 0 {
		t.Fatalf("anonymous requests must not create pins, got %d", len(store.pins))
	}
}

func TestApplySessionPinDropsAnUnusablePin(t *testing.T) {
	t.Parallel()
	s := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	sessionKey := sessionAffinityKey(nil, map[string]any{"session_id": "x"}, "k1", "p1", "")
	s.setSessionPin(sessionKey, "gone")
	attempts, pinned := s.applySessionPin(sessionKey, []string{"a", "b"})
	if pinned != "" || strings.Join(attempts, ",") != "a,b" {
		t.Fatalf("a pin outside the chain must be ignored, attempts=%v pinned=%q", attempts, pinned)
	}
	if store := s.pinStore(); len(store.pins) != 0 {
		t.Fatalf("an unusable pin must be dropped, got %d entries", len(store.pins))
	}
	attempts, pinned = s.applySessionPin(sessionKey, []string{"a", "b"})
	if pinned != "" {
		t.Fatal("the cleared pin must not come back")
	}
	if got, pinned := s.applySessionPin("", []string{"a"}); pinned != "" || strings.Join(got, ",") != "a" {
		t.Fatal("no session key must mean no pin")
	}
}

func TestSessionPinStoreExpiryAndCap(t *testing.T) {
	t.Parallel()
	now := time.Now()
	store := &sessionPinStore{
		pins:    map[string]sessionPin{},
		ttl:     time.Minute,
		max:     2,
		nowFunc: func() time.Time { return now },
	}
	store.set("a", "p1")
	if got := store.sessionPin("a"); got != "p1" {
		t.Fatalf("expected the fresh pin, got %q", got)
	}
	now = now.Add(2 * time.Minute)
	if got := store.sessionPin("a"); got != "" {
		t.Fatalf("an expired pin must be ignored, got %q", got)
	}
	if len(store.pins) != 0 {
		t.Fatalf("an expired pin must be dropped on read, got %d", len(store.pins))
	}
	// Past the cap nothing grows unbounded.
	for i := 0; i < 50; i++ {
		store.set(string(rune('a'+i%26))+string(rune('a'+i%23)), "p")
	}
	if len(store.pins) > 2 {
		t.Fatalf("cap not enforced: %d pins", len(store.pins))
	}
	// A nil store is what a Server built without NewServer gets: no pinning,
	// no panic.
	var nilStore *sessionPinStore
	nilStore.set("k", "p")
	if nilStore.sessionPin("k") != "" {
		t.Fatal("nil store must read as no pin")
	}
	nilStore.clear("k")
}

// The pin table is per server, so two servers (e.g. one console test harness
// and another) never see each other's conversations.
func TestSessionPinsAreNotGlobalState(t *testing.T) {
	t.Parallel()
	first := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	second := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	sessionKey := sessionAffinityKey(nil, map[string]any{"session_id": "shared"}, "same-key", "p1", "")
	first.setSessionPin(sessionKey, "p1")
	if got := second.pinStore().sessionPin(sessionKey); got != "" {
		t.Fatalf("the second server should not see the first one's pin, got %q", got)
	}
}
