package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// 同一账号的多条 Provider 行：一个订阅按"每分钟 N 次"卖给账号，不是卖给行。
// 没有 laneGroup 时每行各拿一份预算，N 行就能打出 N 倍的量，账号自己回 429。
func TestProviderLaneGroupsShareOneAccountBudget(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	lanes := newProviderLanes()
	// Two rows of one account: same LaneGroup, so the same key.
	a, b := providerLaneKey(domain.Provider{ID: "a", LaneGroup: "acct"}), providerLaneKey(domain.Provider{ID: "b", LaneGroup: "acct"})
	if a != b {
		t.Fatalf("rows of one account must share a key: %q vs %q", a, b)
	}
	lane := lanes.laneFor(a, 0, 2)
	for i, id := range []string{"a", "b"} {
		if other := lanes.laneFor(providerLaneKey(domain.Provider{ID: id, LaneGroup: "acct"}), 0, 2); other != lane {
			t.Fatalf("row %s got a lane of its own", id)
		}
		release, err := lane.acquireSlot(context.Background(), 0, now)
		if err != nil {
			t.Fatalf("request %d on %s should be admitted, got %v", i+1, id, err)
		}
		release()
	}
	// The account's budget is spent, so the next request is refused whichever
	// row it arrives on.
	if _, err := lane.acquireSlot(context.Background(), 0, now); !errors.Is(err, errProviderSaturated) {
		t.Fatalf("the account's budget is spent, the next request must be refused, got %v", err)
	}
}

// 没分组的 Provider 保持原样：各行一份预算。
func TestProviderLaneWithoutGroupKeepsSeparateBudgets(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	lanes := newProviderLanes()
	for _, id := range []string{"a", "b"} {
		lane := lanes.laneFor(id, 0, 1)
		for i := 0; i < 2; i++ {
			_, err := lane.acquireSlot(context.Background(), 0, now)
			if i == 0 && err != nil {
				t.Fatalf("%s should have a budget of its own, got %v", id, err)
			}
			if i == 1 && !errors.Is(err, errProviderSaturated) {
				t.Fatalf("%s should be limited by its own budget, got %v", id, err)
			}
		}
	}
	if lanes.laneFor(providerLaneKey(domain.Provider{ID: "a"}), 0, 1) == lanes.laneFor(providerLaneKey(domain.Provider{ID: "b"}), 0, 1) {
		t.Fatalf("ungrouped providers must not share a lane")
	}
}

// 并发上限同样是账号级的：两行合起来只能同时有两个请求在飞。
func TestProviderLaneGroupSharesConcurrencyCap(t *testing.T) {
	t.Parallel()
	server := laneGroupServer(t, domain.Provider{ID: "a", LaneGroup: "acct", MaxConcurrentRequests: 2, QueueWaitSeconds: 0},
		domain.Provider{ID: "b", LaneGroup: "acct", MaxConcurrentRequests: 2, QueueWaitSeconds: 0})

	releases := make([]func(), 0, 2)
	for _, id := range []string{"a", "b"} {
		provider, err := server.router.ProviderByID(id)
		if err != nil {
			t.Fatal(err)
		}
		release, err := server.acquireProviderSlot(context.Background(), provider)
		if err != nil {
			t.Fatalf("%s should get one of the account's two slots, got %v", id, err)
		}
		releases = append(releases, release)
	}
	provider, err := server.router.ProviderByID("a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.acquireProviderSlot(context.Background(), provider); !errors.Is(err, errProviderSaturated) {
		t.Fatalf("the account's two slots are taken, a third request must be refused, got %v", err)
	}
	for _, release := range releases {
		release()
	}
	release, err := server.acquireProviderSlot(context.Background(), provider)
	if err != nil {
		t.Fatalf("a freed account slot should admit again, got %v", err)
	}
	release()
}

// 排队的请求也按整个账号先到先得：同组另一行放回来的槽，不能插到队尾前面。
func TestProviderLaneGroupQueuesFirstComeFirstServed(t *testing.T) {
	t.Parallel()
	server := laneGroupServer(t, domain.Provider{ID: "a", LaneGroup: "acct", MaxConcurrentRequests: 1, QueueWaitSeconds: 5},
		domain.Provider{ID: "b", LaneGroup: "acct", MaxConcurrentRequests: 1, QueueWaitSeconds: 5})
	holder, err := server.router.ProviderByID("a")
	if err != nil {
		t.Fatal(err)
	}
	release, err := server.acquireProviderSlot(context.Background(), holder)
	if err != nil {
		t.Fatalf("holder should take the only slot: %v", err)
	}

	order := make([]string, 0, 3)
	var mu sync.Mutex
	done := make(chan struct{}, 3)
	for _, id := range []string{"a", "b", "a"} {
		provider, err := server.router.ProviderByID(id)
		if err != nil {
			t.Fatal(err)
		}
		go func(id string, provider domain.Provider) {
			got, err := server.acquireProviderSlot(context.Background(), provider)
			if err == nil {
				mu.Lock()
				order = append(order, id)
				mu.Unlock()
				got()
			}
			done <- struct{}{}
		}(id, provider)
		// Stagger the arrivals so the queue order is deterministic.
		time.Sleep(20 * time.Millisecond)
	}
	release()
	for i := 0; i < 3; i++ {
		<-done
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(order, ",") != "a,b,a" {
		t.Fatalf("waiters served out of arrival order: %v", order)
	}
}

// Retry-After 必须读账号的 lane，而不是一行之自己（分组后那行根本没有 lane）。
func TestProviderLaneGroupRetryAfterUsesTheAccountLane(t *testing.T) {
	t.Parallel()
	server := laneGroupServer(t, domain.Provider{ID: "a", LaneGroup: "acct", MaxConcurrentRequests: 1, QueueWaitSeconds: 0},
		domain.Provider{ID: "b", LaneGroup: "acct", MaxConcurrentRequests: 1, QueueWaitSeconds: 0})
	provider, err := server.router.ProviderByID("a")
	if err != nil {
		t.Fatal(err)
	}
	release, err := server.acquireProviderSlot(context.Background(), provider)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if got := server.providerLanes.retryAfterSecondsFor(provider, time.Now()); got != providerRetryAfterSeconds {
		t.Fatalf("a full lane with no rate limit nudges by the constant, got %d", got)
	}
	// The account's lane is what a rate refusal reads, too.
	if server.providerLanes.retryAfterSeconds(providerLaneKey(provider), time.Now()) < 1 {
		t.Fatalf("a saturated lane should tell the client to retry after at least a second")
	}
}

// 控制台把 Provider 放回独立预算：传 "laneGroup":"" 时必须真的解除分组，
// 省略字段时保持原分组（PATCH 语义）。
func TestHandleSetProviderConcurrencyLaneGroup(t *testing.T) {
	t.Parallel()
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{
		{ID: "p1", Name: "p1", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://example.com/v1"},
	}})
	server := &Server{router: router, providerLanes: newProviderLanes()}

	call := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/__providers/p1/concurrency", strings.NewReader(body))
		request.SetPathValue("id", "p1")
		request = request.WithContext(context.WithValue(request.Context(), authContextKey{}, adminIdentity()))
		recorder := httptest.NewRecorder()
		server.handleSetProviderConcurrency(recorder, request)
		return recorder
	}

	if rec := call(`{"maxConcurrentRequests":2,"laneGroup":" acct "}`); rec.Code != http.StatusOK {
		t.Fatalf("set status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := router.State().Providers[0].LaneGroup; got != "acct" {
		t.Fatalf("laneGroup stored as %q, want the trimmed acct", got)
	}

	// Omitting the field keeps the grouping.
	if rec := call(`{"maxConcurrentRequests":3}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := router.State().Providers[0].LaneGroup; got != "acct" {
		t.Fatalf("an omitted laneGroup must keep the grouping, got %q", got)
	}

	// "" puts the provider back on its own budget.
	if rec := call(`{"maxConcurrentRequests":3,"laneGroup":""}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := router.State().Providers[0].LaneGroup; got != "" {
		t.Fatalf(`laneGroup "" should ungroup, got %q`, got)
	}

	long := strings.Repeat("x", providerLaneGroupMaxLen+1)
	if rec := call(`{"maxConcurrentRequests":1,"laneGroup":"` + long + `"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("an over-long laneGroup should be refused, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestProviderLaneKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		provider domain.Provider
		want     string
	}{
		{domain.Provider{ID: "p1"}, "p1"},
		{domain.Provider{ID: "p1", LaneGroup: "  acct "}, "group:acct"},
		{domain.Provider{ID: "p1", LaneGroup: "   "}, "p1"},
		{domain.Provider{ID: "p1", LaneGroup: "Acme"}, "group:acme"},
		{domain.Provider{ID: "p1", LaneGroup: " ACME "}, "group:acme"},
	}
	for _, tc := range cases {
		if got := providerLaneKey(tc.provider); got != tc.want {
			t.Fatalf("providerLaneKey(%+v) = %q, want %q", tc.provider, got, tc.want)
		}
	}
	if !providerGrouped(domain.Provider{ID: "p1", LaneGroup: "acct"}) || providerGrouped(domain.Provider{ID: "p1"}) {
		t.Fatalf("providerGrouped must follow the laneGroup field")
	}
}

func laneGroupServer(t *testing.T, providers ...domain.Provider) *Server {
	t.Helper()
	for i := range providers {
		providers[i].Name = providers[i].ID
		providers[i].Protocol = domain.ProtocolOpenAIChat
		if providers[i].BaseURL == "" {
			providers[i].BaseURL = "https://example.com/v1"
		}
	}
	router := NewRouter(domain.GatewayState{Providers: providers})
	for _, provider := range providers {
		if stored, err := router.ProviderByID(provider.ID); err != nil || stored.LaneGroup != provider.LaneGroup {
			t.Fatalf("provider %s was not stored with its lane group: %+v (%v)", provider.ID, stored, err)
		}
	}
	return &Server{router: router, providerLanes: newProviderLanes()}
}

// 分组后的账号被自家的限速拒绝时，failover 写回的 Retry-After 必须是账号
// 那条 lane 的真实窗口剩余秒数。按行号查 lane 在分组后一无所获，只能退回
// 1 秒的常量——客户端于是每隔一秒重试一次，一直撞在同一个分钟窗口上。
func TestFailoverRetryAfterReadsTheAccountLane(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	t.Cleanup(upstream.Close)

	state := domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "acct-a", Name: "acct-a", Protocol: domain.ProtocolClaude, BaseURL: upstream.URL,
			AuthType: "api_key", APIKeySource: "static", LaneGroup: "acct",
			// Rate-only with a short queue: the request waits, the budget is
			// still spent when the wait runs out, and the refusal then has to
			// name the window it is waiting on.
			RequestsPerMinute: 1, QueueWaitSeconds: 1,
		}},
		Routes: []domain.Route{{
			ID: "acct-route", Name: "acct-route", ProviderID: "acct-a",
			OutputProtocol: domain.ProtocolClaude, Enabled: true,
		}},
	}
	server := NewServer(NewRouter(state), monitor.NewStore())
	route, err := server.router.RouteByID("acct-route")
	if err != nil {
		t.Fatalf("route lookup: %v", err)
	}
	provider, err := server.router.ProviderByID("acct-a")
	if err != nil {
		t.Fatalf("provider lookup: %v", err)
	}
	// Spend the account's one request this minute just now, so the rest of the
	// window is long.
	release, err := server.acquireProviderSlot(context.Background(), provider)
	if err != nil {
		t.Fatalf("the account's first request should be admitted: %v", err)
	}
	release()

	key := domain.APIKey{ID: "acct-key", RouteID: route.ID}
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	rec := httptest.NewRecorder()
	status, _, _, _, _, execErr := server.executeProtocolFlowWithFailover(
		rec, req, route, domain.RouteDecision{RouteID: route.ID, ProviderID: route.ProviderID},
		"m", map[string]any{"model": "m", "max_tokens": 16, "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
		domain.ProtocolClaude, false, key, true,
	)
	if !errors.Is(execErr, errProviderSaturated) {
		t.Fatalf("err=%v, want the account's rate refusal", execErr)
	}
	if status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", status)
	}
	got := rec.Header().Get("Retry-After")
	wait, convErr := strconv.Atoi(got)
	if convErr != nil || wait <= providerRetryAfterSeconds {
		t.Fatalf("Retry-After = %q, want the ACCOUNT's rate window, not the %ds constant", got, providerRetryAfterSeconds)
	}
}

// 同组各行写着不同的上限时，账号的预算不能跟着"最后到的那一行"来回跳：
// 无论哪一行先请求、按什么顺序来，账号的额度都是确定的。
// (D29-3)
func TestProviderLaneGroupLimitsStableAcrossRows(t *testing.T) {
	t.Parallel()
	providers := []domain.Provider{
		{ID: "a", Name: "a", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://example.com/v1", LaneGroup: "acct", MaxConcurrentRequests: 1, RequestsPerMinute: 2},
		{ID: "b", Name: "b", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://example.com/v1", LaneGroup: "acct", MaxConcurrentRequests: 3, RequestsPerMinute: 7},
	}
	for _, order := range [][]string{{"a", "b"}, {"b", "a"}} {
		lanes := newProviderLanes()
		for _, id := range order {
			provider := providers[0]
			if id == "b" {
				provider = providers[1]
			}
			lanes.laneForProvider(provider, providers)
		}
		key := providerLaneKey(providers[0])
		lanes.mu.Lock()
		lane := lanes.lanes[key]
		lanes.mu.Unlock()
		if lane == nil {
			t.Fatalf("order %v: the account has no lane", order)
		}
		lane.mu.Lock()
		max, rpm := lane.max, lane.rpm
		lane.mu.Unlock()
		// The account's budget is the tightest limit any member declares: row a
		// asks for 1 concurrent and 2/min, so the account carries 1 and 2
		// whoever arrives last. A generous row must not talk the account up.
		if max != 1 || rpm != 2 {
			t.Fatalf("order %v: account lane sized %d concurrent, %d/min; want 1, 2 (smallest declared, not the last row's)", order, max, rpm)
		}
	}
}

// 组里有一行 0/0 时，它不能借"没有上限"绕过账号的预算：这条请求仍要
// 扣账号的并发和每分钟额度。(D29-4)
func TestProviderLaneGroupMemberWithoutLimitsIsBoundedByTheAccount(t *testing.T) {
	t.Parallel()
	server := laneGroupServer(t,
		domain.Provider{ID: "a", LaneGroup: "acct", MaxConcurrentRequests: 1, RequestsPerMinute: 1, QueueWaitSeconds: 0},
		domain.Provider{ID: "free", LaneGroup: "acct", MaxConcurrentRequests: 0, RequestsPerMinute: 0, QueueWaitSeconds: 0})
	limited, err := server.router.ProviderByID("a")
	if err != nil {
		t.Fatal(err)
	}
	free, err := server.router.ProviderByID("free")
	if err != nil {
		t.Fatal(err)
	}
	release, err := server.acquireProviderSlot(context.Background(), limited)
	if err != nil {
		t.Fatalf("the account's one slot should admit: %v", err)
	}
	if _, err := server.acquireProviderSlot(context.Background(), free); !errors.Is(err, errProviderSaturated) {
		t.Fatalf("a 0/0 member must still be bounded by the account, got %v", err)
	}
	release()
	// The concurrency slot is free again, but the minute budget is spent.
	if _, err := server.acquireProviderSlot(context.Background(), free); !errors.Is(err, errProviderSaturated) {
		t.Fatalf("a 0/0 member must spend the account's rate budget, got %v", err)
	}
}

// 同一行也不能因为单行 0/0 而把账号的 0/0 行拉成无限：组里有一行限了，
// 0/0 那行请求时也要走 lane。
func TestProviderLaneGroupMemberWithoutLimitsCountsInFlight(t *testing.T) {
	t.Parallel()
	server := laneGroupServer(t,
		domain.Provider{ID: "a", LaneGroup: "acct", MaxConcurrentRequests: 2, QueueWaitSeconds: 0},
		domain.Provider{ID: "free", LaneGroup: "acct", MaxConcurrentRequests: 0, QueueWaitSeconds: 0})
	free, err := server.router.ProviderByID("free")
	if err != nil {
		t.Fatal(err)
	}
	release, err := server.acquireProviderSlot(context.Background(), free)
	if err != nil {
		t.Fatal(err)
	}
	if got := server.providerInFlight(free); got != 1 {
		t.Fatalf("a grouped row's in-flight should read back from the account lane, got %d", got)
	}
	release()
	if got := server.providerInFlight(free); got != 0 {
		t.Fatalf("release should return the slot, got %d in flight", got)
	}
}

// 分组后的行在调度器"最少在用"里必须看到账号的真实在飞数，而不是永远 0。
// (D29-6)
func TestProviderInFlightReadsGroupedLane(t *testing.T) {
	t.Parallel()
	server := laneGroupServer(t,
		domain.Provider{ID: "a", LaneGroup: "acct", MaxConcurrentRequests: 4, QueueWaitSeconds: 0},
		domain.Provider{ID: "b", LaneGroup: "acct", MaxConcurrentRequests: 4, QueueWaitSeconds: 0},
		domain.Provider{ID: "solo", MaxConcurrentRequests: 4, QueueWaitSeconds: 0})
	a, err := server.router.ProviderByID("a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := server.router.ProviderByID("b")
	if err != nil {
		t.Fatal(err)
	}
	release, err := server.acquireProviderSlot(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	releaseSolo, err := server.acquireProviderSlot(context.Background(), mustProvider(t, server, "solo"))
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []domain.Provider{a, b} {
		if got := server.providerInFlight(provider); got != 1 {
			t.Fatalf("row %s shares the account's one request in flight, got %d", provider.ID, got)
		}
	}
	if got := server.providerInFlight(mustProvider(t, server, "solo")); got != 1 {
		t.Fatalf("an ungrouped row reads its own lane, got %d", got)
	}
	release()
	releaseSolo()
	if got := server.providerInFlight(b); got != 0 {
		t.Fatalf("after release the account should have nothing in flight, got %d", got)
	}
}

// 控制台改一行时，账号的预算按组里最紧的那条声明走：把 b 调到更宽不放大
// 账号，调到更窄才收窄，改回 0/0 则回到 a 的额度——不会因为任何一行
// "最后到达"而清零。
func TestSetProviderConcurrencyResizesTheAccountLane(t *testing.T) {
	t.Parallel()
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{
		{ID: "a", Name: "a", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://example.com/v1", LaneGroup: "acct", MaxConcurrentRequests: 2},
		{ID: "b", Name: "b", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://example.com/v1", LaneGroup: "acct", MaxConcurrentRequests: 0},
	}})
	server := &Server{router: router, providerLanes: newProviderLanes()}
	call := func(body string) {
		request := httptest.NewRequest(http.MethodPost, "/__providers/b/concurrency", strings.NewReader(body))
		request.SetPathValue("id", "b")
		request = request.WithContext(context.WithValue(request.Context(), authContextKey{}, adminIdentity()))
		recorder := httptest.NewRecorder()
		server.handleSetProviderConcurrency(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	accountMax := func() int {
		provider, err := router.ProviderByID("a")
		if err != nil {
			t.Fatal(err)
		}
		lane, _ := server.providerLanes.laneForProvider(provider, router.State().Providers)
		lane.mu.Lock()
		defer lane.mu.Unlock()
		return lane.max
	}
	if got := accountMax(); got != 2 {
		t.Fatalf("account max = %d, want 2", got)
	}
	call(`{"maxConcurrentRequests":5}`)
	if got := accountMax(); got != 2 {
		t.Fatalf("a looser row must not widen the account: got %d, want 2", got)
	}
	call(`{"maxConcurrentRequests":1}`)
	if got := accountMax(); got != 1 {
		t.Fatalf("a tighter row must tighten the account: got %d, want 1", got)
	}
	call(`{"maxConcurrentRequests":0}`)
	if got := accountMax(); got != 2 {
		t.Fatalf("clearing row b must fall back to row a's 2, got %d", got)
	}
}

// 混组的规则：A 声明 5/min，B 没有声明（0/0），账号就是 5/min，B 也在 5 次
// 上被限住；这条混合只能记一次 warn，让运维看得到 B 为什么被限。
func TestProviderLaneGroupMixedMembersUseTheSmallestDeclaredLimit(t *testing.T) {
	t.Parallel()
	logs := monitor.NewStore()
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{
		{ID: "a", Name: "a", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://example.com/v1", LaneGroup: "acct", RequestsPerMinute: 5, QueueWaitSeconds: 1},
		{ID: "b", Name: "b", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://example.com/v1", LaneGroup: "acct", QueueWaitSeconds: 1},
	}})
	server := &Server{router: router, providerLanes: newProviderLanes(), logs: logs}
	a := mustProvider(t, server, "a")
	b := mustProvider(t, server, "b")
	// A short wait keeps the refusal below: a rate-full lane is otherwise given
	// the rest of its minute, which would make this a minute-long test.
	for i := 0; i < 5; i++ {
		release, err := server.acquireProviderSlot(context.Background(), a)
		if err != nil {
			t.Fatalf("request %d on a should be admitted, got %v", i+1, err)
		}
		release()
	}
	if _, err := server.acquireProviderSlot(context.Background(), b); !errors.Is(err, errProviderSaturated) {
		t.Fatalf("the group's 5/min is spent by row a, so row b must be refused, got %v", err)
	}
	warnings := 0
	for _, entry := range logs.ListApp(0) {
		if entry.Level == "warn" && entry.Message == "lane group mixes limited and unlimited providers" {
			warnings++
			if !strings.Contains(entry.Context, `"acct"`) || !strings.Contains(entry.Context, "5/min") {
				t.Fatalf("the warning must name the group and the applied limit, got %q", entry.Context)
			}
		}
	}
	if warnings != 1 {
		t.Fatalf("the mixed-group warning should be written once, got %d", warnings)
	}
}

// 全部成员都是 0/0：账号不限，走原来的免费路径，也不会报混合。
func TestProviderLaneGroupAllMembersUnlimitedStayFree(t *testing.T) {
	t.Parallel()
	server := laneGroupServer(t,
		domain.Provider{ID: "a", LaneGroup: "acct"},
		domain.Provider{ID: "b", LaneGroup: "acct"})
	for _, id := range []string{"a", "b"} {
		provider := mustProvider(t, server, id)
		for i := 0; i < 3; i++ {
			release, err := server.acquireProviderSlot(context.Background(), provider)
			if err != nil {
				t.Fatalf("%s should be free, got %v", id, err)
			}
			release()
		}
	}
}

func mustProvider(t *testing.T, server *Server, id string) domain.Provider {
	t.Helper()
	provider, err := server.router.ProviderByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

// 组内排队：额度按最紧的成员算，但"能等多久"取组内最大——否则同一账号的
// 另一行写了排队 5 秒，请求走到这一行仍然刚到就拒。
func TestProviderLaneGroupQueueWaitTakesTheLongest(t *testing.T) {
	t.Parallel()
	impatient := domain.Provider{ID: "a", Protocol: domain.ProtocolOpenAIChat, LaneGroup: "acct", MaxConcurrentRequests: 1, QueueWaitSeconds: 0}
	patientRow := domain.Provider{ID: "b", Protocol: domain.ProtocolOpenAIChat, LaneGroup: "acct", MaxConcurrentRequests: 1, QueueWaitSeconds: 5}
	other := domain.Provider{ID: "c", Protocol: domain.ProtocolOpenAIChat, LaneGroup: "other", MaxConcurrentRequests: 1, QueueWaitSeconds: 9}
	ungrouped := domain.Provider{ID: "d", Protocol: domain.ProtocolOpenAIChat, MaxConcurrentRequests: 1, QueueWaitSeconds: 0}
	members := []domain.Provider{impatient, patientRow, other, ungrouped}

	if got := providerLaneQueueWait(impatient, members); got != 5*time.Second {
		t.Fatalf("a grouped row inherited %v, want the group's longest 5s", got)
	}
	// 别的组不参与：other 组是 9 秒，不能串到 acct。
	if got := providerLaneQueueWait(patientRow, members); got != 5*time.Second {
		t.Fatalf("a sibling's wait = %v, want its own 5s", got)
	}
	// 未分组的行仍只看自己。
	if got := providerLaneQueueWait(ungrouped, members); got != 0 {
		t.Fatalf("an ungrouped row inherited %v, want its own 0", got)
	}
}
