package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
	"github.com/luca/llm-protocol-gateway/internal/store"
)

func pricedServer(t *testing.T) (*Server, *Router, *store.Store) {
	t.Helper()
	provider := domain.Provider{ID: "p1", Name: "p1", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://a.example/v1"}
	route := domain.Route{ID: "r1", Name: "r1", ProviderID: provider.ID, OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true}
	key := domain.APIKey{ID: "k1", Name: "k1", Key: "sk-gw-pricing", RouteID: route.ID, Enabled: true, StreamEnabled: true}
	db, err := store.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Saving marks the database initialized: an untouched store refuses every
	// admin endpoint, including on loopback, because it expects a first-run setup.
	state := domain.GatewayState{
		Providers: []domain.Provider{provider},
		Routes:    []domain.Route{route},
		APIKeys:   []domain.APIKey{key},
	}
	if err := db.Save(state); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(state)
	server := NewServer(router, monitor.NewStore(), db)
	return server, router, db
}

// asLocalAdmin marks a request as coming from the gateway host itself, which is
// how the console talks to a local gateway and what the admin bypass checks.
func asLocalAdmin(req *http.Request) *http.Request {
	req.Host = "127.0.0.1:18093"
	req.RemoteAddr = "127.0.0.1:5555"
	return req
}

func putPrice(t *testing.T, s *Server, price domain.ModelPrice) {
	t.Helper()
	body, _ := json.Marshal(price)
	req := asLocalAdmin(httptest.NewRequest(http.MethodPost, "/__prices", strings.NewReader(string(body))))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save price %s/%s -> %d: %s", price.ProviderID, price.ModelID, rec.Code, rec.Body.String())
	}
}

// The prompt total already contains the cache reads and the cache writes, so
// pricing the whole input at the uncached rate and then adding the cache tiers
// would bill those tokens twice. This test pins the subtraction with numbers that
// make a double count obvious.
func TestCostOfSubtractsCacheTiers(t *testing.T) {
	t.Parallel()
	s, _, _ := pricedServer(t)
	putPrice(t, s, domain.ModelPrice{
		ProviderID: "p1", ModelID: "gpt-x",
		InputUSDPerMTok: 3, OutputUSDPerMTok: 15, CacheReadUSDPerMTok: 0.3, CacheWriteUSDPerMTok: 3.75,
	})
	usage := TokenUsage{InputTokens: 1000, OutputTokens: 100, CacheTokens: 600, CacheWriteTokens: 200}
	cost, priced := s.costOf("p1", "gpt-x", usage)
	if !priced {
		t.Fatal("expected the model row to price this")
	}
	// plain 200*3 + out 100*15 + read 600*0.3 + write 200*3.75 = 3030 / 1e6 USD
	if cost != 3030 {
		t.Fatalf("got %d micros, want 3030 (double counting would give %d)", cost, 3030+600*3/1000+200*3/1000)
	}
}

// A day of requests with no price row is 未计价, not free: priced=false has to
// survive all the way to the log so the console can say so.
func TestCostOfUnpricedModel(t *testing.T) {
	t.Parallel()
	s, _, _ := pricedServer(t)
	cost, priced := s.costOf("p1", "gpt-x", TokenUsage{InputTokens: 100, OutputTokens: 10})
	if priced || cost != 0 {
		t.Fatalf("no row must mean unpriced, got %d/%v", cost, priced)
	}
}

// The provider default row covers models without one; an exact row wins.
func TestCostOfProviderDefaultRow(t *testing.T) {
	t.Parallel()
	s, _, _ := pricedServer(t)
	putPrice(t, s, domain.ModelPrice{ProviderID: "p1", ModelID: "*", InputUSDPerMTok: 2, OutputUSDPerMTok: 10})
	putPrice(t, s, domain.ModelPrice{ProviderID: "p1", ModelID: "cheap", InputUSDPerMTok: 0.1, OutputUSDPerMTok: 0.2})
	if cost, _ := s.costOf("p1", "anything-else", TokenUsage{InputTokens: 1000000, OutputTokens: 1000000}); cost != 12000000 {
		t.Fatalf("default row should apply: got %d micros", cost)
	}
	if cost, _ := s.costOf("p1", "cheap", TokenUsage{InputTokens: 1000000, OutputTokens: 1000000}); cost != 300000 {
		t.Fatalf("exact row should win over the default: got %d micros", cost)
	}
	if _, ok := s.costOf("other-provider", "anything", TokenUsage{InputTokens: 1}); ok {
		t.Fatal("another provider must not inherit this provider's default")
	}
}

// A global row is the shared rate card: every provider without a row of its own
// uses it, which is how all official Claude accounts end up on one price. A
// provider row still overrides it.
func TestGlobalPriceFallback(t *testing.T) {
	t.Parallel()
	s, _, _ := pricedServer(t)
	putPrice(t, s, domain.ModelPrice{ProviderID: "*", ModelID: "claude-sonnet-5", InputUSDPerMTok: 3, OutputUSDPerMTok: 10})
	putPrice(t, s, domain.ModelPrice{ProviderID: "*", ModelID: "*", InputUSDPerMTok: 1, OutputUSDPerMTok: 2})
	putPrice(t, s, domain.ModelPrice{ProviderID: "p1", ModelID: "*", InputUSDPerMTok: 9, OutputUSDPerMTok: 9})

	if cost, ok := s.costOf("claude-official", "claude-sonnet-5", TokenUsage{InputTokens: 1000000, OutputTokens: 1000000}); !ok || cost != 13000000 {
		t.Fatalf("global exact row should price another provider: %d/%v", cost, ok)
	}
	if cost, ok := s.costOf("claude-official", "claude-opus-5-5", TokenUsage{InputTokens: 1000000, OutputTokens: 1000000}); !ok || cost != 3000000 {
		t.Fatalf("global default row should price an unlisted model: %d/%v", cost, ok)
	}
	if cost, ok := s.costOf("p1", "claude-sonnet-5", TokenUsage{InputTokens: 1000000, OutputTokens: 1000000}); !ok || cost != 18000000 {
		t.Fatalf("a provider row must win over the global exact row: %d/%v", cost, ok)
	}
	if cost, ok := s.costOf("p1", "something-else", TokenUsage{InputTokens: 1000000, OutputTokens: 1000000}); !ok || cost != 18000000 {
		t.Fatalf("the provider's own default should win: %d/%v", cost, ok)
	}
}

// The logged model can be the historical "alias -> upstream" pair; the vendor
// billed the upstream id, so that is what the price has to match.
func TestUpstreamModelForPricing(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"gpt-5":          "gpt-5",
		"fast -> gpt-5":  "gpt-5",
		"  alias -> x  ": "x",
		"-> broken":      "broken",
		"":               "",
	}
	for in, want := range cases {
		if got := upstreamModelForPricing(in); got != want {
			t.Fatalf("%q -> %q, want %q", in, got, want)
		}
	}
}

// The console's whole money view depends on the log row carrying the cost, so
// the request path has to price it before the row is written.
func TestRequestLogCarriesCost(t *testing.T) {
	f := newFailoverFixture(t, 0, 0, "")
	putPriceOn(t, f.server, domain.ModelPrice{
		ProviderID: f.preferredID, ModelID: "*",
		InputUSDPerMTok: 1, OutputUSDPerMTok: 2,
	})
	if rec := f.request(t); rec.Code != http.StatusOK {
		t.Fatalf("request failed: %d", rec.Code)
	}
	var entry *monitor.RequestLog
	logs := f.server.logs.List(5)
	for i := range logs {
		if logs[i].APIKeyID == "k-sched" {
			entry = &logs[i]
		}
	}
	if entry == nil {
		t.Fatal("no request log written")
	}
	if !entry.Priced {
		t.Fatalf("expected the row to be priced, got %#v", entry)
	}
	// The fixture upstream reports 4 prompt + 1 completion tokens: (4*1 + 1*2)/1e6.
	if entry.CostMicros != 6 {
		t.Fatalf("expected 6 micros, got %d (usage %#v)", entry.CostMicros, entry)
	}
}

func putPriceOn(t *testing.T, s *Server, price domain.ModelPrice) {
	t.Helper()
	s.invalidatePrices()
	if s.priceStore == nil {
		// The failover fixture runs without a SQLite store; seed the cache
		// directly so pricing still applies to the request path.
		s.prices.mu.Lock()
		if s.prices.rows == nil {
			s.prices.rows = map[string]domain.ModelPrice{}
		}
		s.prices.rows[priceKey(price.ProviderID, price.ModelID)] = price
		s.prices.loaded = true
		s.prices.mu.Unlock()
		return
	}
	putPrice(t, s, price)
}

// The price API is admin-only, validates its input, and the list endpoint is
// what the console renders the ledger from.
func TestPriceAPI(t *testing.T) {
	s, _, _ := pricedServer(t)
	putPrice(t, s, domain.ModelPrice{ProviderID: "p1", ModelID: "gpt-x", InputUSDPerMTok: 3, OutputUSDPerMTok: 15})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, asLocalAdmin(httptest.NewRequest(http.MethodGet, "/__prices", nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("list -> %d: %s", rec.Code, rec.Body.String())
	}
	var page struct {
		Prices []domain.ModelPrice `json:"prices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Prices) != 1 || page.Prices[0].InputUSDPerMTok != 3 {
		t.Fatalf("unexpected list: %s", rec.Body.String())
	}

	bad := httptest.NewRecorder()
	s.Handler().ServeHTTP(bad, asLocalAdmin(httptest.NewRequest(http.MethodPost, "/__prices",
		strings.NewReader(`{"providerId":"p1","modelId":"gpt-x","inputUsdPerMTok":-1}`))))
	if bad.Code == http.StatusOK {
		t.Fatal("a negative rate must be refused, not silently treated as free")
	}
	ghost := httptest.NewRecorder()
	s.Handler().ServeHTTP(ghost, asLocalAdmin(httptest.NewRequest(http.MethodPost, "/__prices",
		strings.NewReader(`{"providerId":"nope","modelId":"m","inputUsdPerMTok":1}`))))
	if ghost.Code != http.StatusNotFound {
		t.Fatalf("an unknown provider should be refused, got %d", ghost.Code)
	}

	del := httptest.NewRecorder()
	s.Handler().ServeHTTP(del, asLocalAdmin(httptest.NewRequest(http.MethodDelete, "/__prices/p1/gpt-x", nil)))
	if del.Code != http.StatusOK {
		t.Fatalf("delete -> %d: %s", del.Code, del.Body.String())
	}
	if _, ok := s.lookupPrice("p1", "gpt-x"); ok {
		t.Fatal("the cache must stop serving a deleted row without a restart")
	}
}

// An all-zero global row is the one save that can hide every 未计价 signal at
// once: global "*" is the last row lookupPrice tries, so it would mark every
// unpriced model as "priced at $0". It must be refused explicitly, while a
// global row with real rates and a provider-scoped zero row stay allowed.
func TestPriceAPIRejectsAllZeroGlobalRow(t *testing.T) {
	s, _, db := pricedServer(t)
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, asLocalAdmin(httptest.NewRequest(http.MethodPost, "/__prices", strings.NewReader(body))))
		return rec
	}
	zero := `{"providerId":"*","modelId":"*","inputUsdPerMTok":0,"outputUsdPerMTok":0,"cacheReadUsdPerMTok":0,"cacheWriteUsdPerMTok":0}`
	for _, body := range []string{
		zero,
		`{"providerId":"*","modelId":"claude-sonnet-5"}`,
		`{"providerId":" * ","modelId":""}`,
	} {
		rec := post(body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s -> %d, want 400: %s", body, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "global price row") {
			t.Fatalf("the refusal must say why: %s", rec.Body.String())
		}
	}
	if rows, err := db.LoadModelPrices(); err != nil || len(rows) != 0 {
		t.Fatalf("a refused row must not reach the store: %v %+v", err, rows)
	}

	// A deliberate global row with real prices still works, as does a provider
	// row that is zero on purpose (e.g. a model with no cache-write fee).
	if rec := post(`{"providerId":"*","modelId":"*","inputUsdPerMTok":1,"outputUsdPerMTok":2}`); rec.Code != http.StatusOK {
		t.Fatalf("a priced global row -> %d: %s", rec.Code, rec.Body.String())
	}
	if rec := post(`{"providerId":"p1","modelId":"free-tier","inputUsdPerMTok":0,"outputUsdPerMTok":0}`); rec.Code != http.StatusOK {
		t.Fatalf("a provider-scoped zero row -> %d: %s", rec.Code, rec.Body.String())
	}
}
