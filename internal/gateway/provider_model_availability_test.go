package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestModelUnavailableIsScopedToProviderAndModel(t *testing.T) {
	t.Parallel()
	s := &Server{}
	s.markModelUnavailable("provider-a", "deepseek-v4.1-flash", false, "model_limited", "", nil)

	until, blocked := s.modelUnavailableForRequest("provider-a", "deepseek-v4.1-flash", time.Now())
	if !blocked || until.IsZero() {
		t.Fatal("the exact provider/model pair should be blocked")
	}
	if _, blocked := s.modelUnavailableForRequest("provider-a", "glm-5.2", time.Now()); blocked {
		t.Fatal("a different model must remain available")
	}
	if _, blocked := s.modelUnavailableForRequest("provider-b", "deepseek-v4.1-flash", time.Now()); blocked {
		t.Fatal("the same model on another provider must remain available")
	}
	if _, blocked := s.modelUnavailableForRequest("provider-a", "DEEPSEEK-V4.1-FLASH", time.Now()); !blocked {
		t.Fatal("model lookup should be case-insensitive")
	}

	// A provider-wide recovery is not a model recovery.
	s.markProviderAvailable("provider-a")
	if _, blocked := s.modelUnavailableForRequest("provider-a", "deepseek-v4.1-flash", time.Now()); !blocked {
		t.Fatal("provider success must not clear a model-level mark")
	}

	s.markModelAvailable("provider-a", "glm-5.2")
	if _, blocked := s.modelUnavailableForRequest("provider-a", "deepseek-v4.1-flash", time.Now()); !blocked {
		t.Fatal("clearing another model must not clear this model")
	}
	s.markModelAvailable("provider-a", "deepseek-v4.1-flash")
	if _, blocked := s.modelUnavailableForRequest("provider-a", "deepseek-v4.1-flash", time.Now()); blocked {
		t.Fatal("exact model recovery should clear the mark")
	}
}

func TestProviderAvailabilityOverlayShowsOnlyModelRestriction(t *testing.T) {
	t.Parallel()
	s := &Server{}
	s.markModelUnavailable("p-overlay", "deepseek", false, "model_limited", "", nil)
	provider := domain.Provider{ID: "p-overlay", HealthStatus: "healthy"}
	s.applyProviderAvailability(&provider)
	if provider.HealthStatus != "healthy" {
		t.Fatalf("model restriction changed provider health to %q", provider.HealthStatus)
	}
	if len(provider.ModelAvailability) != 1 || provider.ModelAvailability[0].Model != "deepseek" {
		t.Fatalf("model overlay = %#v, want deepseek only", provider.ModelAvailability)
	}
}

func TestProviderRecoveryKeepsOriginalProbeModel(t *testing.T) {
	t.Parallel()
	s := &Server{}
	s.markProviderUnavailableForModel("p-probe", "deepseek", true, "quota", "", nil)
	s.refreshProviderUnavailableCountdown("p-probe", true, "quota", "", nil)
	state, ok := s.providerUnavailableSnapshot("p-probe")
	if !ok || state.probeModel != "deepseek" {
		t.Fatalf("provider state = %#v ok=%v, want original probe model", state, ok)
	}
}

func TestWorkBuddyModelFailureDoesNotMarkProvider(t *testing.T) {
	t.Parallel()
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{{
		ID: "wb-1", Name: "WorkBuddy", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeWorkBuddy,
	}}})
	s := NewServer(router, monitor.NewStore())

	body := []byte(`{"code":6004,"msg":"model rate limited"}`)
	if !s.applyProviderFailoverOutcomeForModel("wb-1", "deepseek-v4.1-flash", http.StatusBadRequest, body, "", nil) {
		t.Fatal("a model-level refusal should still advance to the next candidate")
	}
	if _, unavailable := s.providerNextRetry("wb-1"); unavailable {
		t.Fatal("WorkBuddy model limit must not mark the whole provider unavailable")
	}
	state, ok := s.modelUnavailableSnapshot("wb-1", "deepseek-v4.1-flash")
	if !ok || state.model != "deepseek-v4.1-flash" {
		t.Fatalf("model state = %#v ok=%v, want exact model mark", state, ok)
	}
	if _, ok := s.modelUnavailableSnapshot("wb-1", "glm-5.2"); ok {
		t.Fatal("model limit leaked to another model")
	}
}

func TestSuccessfulOtherModelDoesNotRecoverFailedModel(t *testing.T) {
	t.Parallel()
	s := &Server{}
	s.markModelUnavailable("p1", "deepseek", false, "model_limited", "", nil)

	// A real success for glm may clear a hypothetical glm mark, but not
	// deepseek. This is the invariant that prevents a background probe of one
	// model from recovering another one.
	_ = s.applyProviderFailoverOutcomeForModel("p1", "glm", http.StatusOK, nil, "", nil)
	if _, ok := s.modelUnavailableSnapshot("p1", "deepseek"); !ok {
		t.Fatal("glm success cleared deepseek")
	}
	_ = s.applyProviderFailoverOutcomeForModel("p1", "deepseek", http.StatusOK, nil, "", nil)
	if _, ok := s.modelUnavailableSnapshot("p1", "deepseek"); ok {
		t.Fatal("exact model success did not clear deepseek")
	}
}

func TestModelResetTimestampFromHumanReadableBody(t *testing.T) {
	now := time.Date(2026, 11, 14, 19, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	body := []byte("您的使用量已超出频率限制，将在 2026-11-14 20:09:02 UTC+8 重置，您也可以切换其他模型继续使用。")
	at, ok := parseHumanResetTime(string(body), now)
	if !ok {
		t.Fatal("human-readable reset time was not parsed")
	}
	want := time.Date(2026, 11, 14, 20, 9, 2, 0, time.FixedZone("UTC+8", 8*60*60))
	if !at.Equal(want) {
		t.Fatalf("reset = %s, want %s", at, want)
	}
}

func TestModelRestTTLUsesVendorResetTime(t *testing.T) {
	now := time.Date(2026, 11, 14, 19, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	body := []byte("您的使用量已超出频率限制，将在 2026-11-14 20:09:02 UTC+8 重置，您也可以切换其他模型继续使用。")
	got := modelAvailabilityRestTTL("model_limited", "", body, 0, now)
	want := 69*time.Minute + 2*time.Second
	if got != want {
		t.Fatalf("TTL = %s, want %s", got, want)
	}
}

func TestChineseModelRestrictionClassifiesAtModelScope(t *testing.T) {
	t.Parallel()
	body := []byte("您的使用量已超出频率限制，将在 2026-11-14 20:09:02 UTC+8 重置，您也可以切换其他模型继续使用。")
	class, scope, reason := (&Server{}).classifyProviderFailoverScoped("p", http.StatusBadRequest, body, nil)
	if class != failoverBusy || scope != availabilityScopeModel || reason != "model_restricted" {
		t.Fatalf("class=%v scope=%v reason=%q, want busy/model/model_restricted", class, scope, reason)
	}
}

func TestModelFailureSkipsOnlyThatModelInRequestLoop(t *testing.T) {
	var deepseekHits, glmHits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		model := ""
		// The gateway sends a JSON body; decoding just the model is enough
		// for this deterministic fake upstream.
		raw, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err == nil {
			model, _ = payload["model"].(string)
		}
		if strings.Contains(strings.ToLower(model), "deepseek") {
			deepseekHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"model not found"}}`))
			return
		}
		glmHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	provider := domain.Provider{
		ID: "p-model", Name: "p-model", Protocol: domain.ProtocolOpenAIChat,
		BaseURL: upstream.URL, AuthType: domain.AuthTypeAPIKey, APIKeySource: "sk-test",
	}
	route := domain.Route{ID: "r-model", ProviderID: provider.ID, OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true}
	key := domain.APIKey{ID: "k-model", Key: "sk-gateway", RouteID: route.ID, Enabled: true}
	server := NewServer(NewRouter(domain.GatewayState{
		Providers: []domain.Provider{provider},
		Routes:    []domain.Route{route},
		APIKeys:   []domain.APIKey{key},
	}), monitor.NewStore())

	request := func(model string) (int, error) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer sk-gateway")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		status, _, _, _, _, err := server.executeProtocolFlowWithFailover(
			rec, req, route, domain.RouteDecision{RouteID: route.ID, ProviderID: provider.ID},
			model, map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
			domain.ProtocolOpenAIChat, false, key, true,
		)
		return status, err
	}

	status, err := request("deepseek")
	if err != nil || status != http.StatusBadRequest {
		t.Fatalf("first deepseek request = %d, %v; want 400", status, err)
	}
	if deepseekHits.Load() != 1 {
		t.Fatalf("first deepseek hits = %d, want 1", deepseekHits.Load())
	}

	status, err = request("deepseek")
	if err == nil || status != http.StatusTooManyRequests {
		t.Fatalf("second deepseek request = %d, %v; want 429 without upstream hit", status, err)
	}
	if deepseekHits.Load() != 1 {
		t.Fatalf("model cooldown should skip upstream, hits = %d", deepseekHits.Load())
	}

	status, err = request("glm")
	if err != nil || status != http.StatusOK {
		t.Fatalf("glm request = %d, %v; want 200", status, err)
	}
	if glmHits.Load() != 1 {
		t.Fatalf("glm hits = %d, want 1", glmHits.Load())
	}
	if _, ok := server.modelUnavailableSnapshot(provider.ID, "deepseek"); !ok {
		t.Fatal("glm success cleared the deepseek model mark")
	}
}

func TestModelProbeUsesExactModelAndSmallBody(t *testing.T) {
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	provider := domain.Provider{
		ID: "p-probe", Name: "p-probe", Protocol: domain.ProtocolOpenAIChat,
		BaseURL: upstream.URL, AuthType: domain.AuthTypeAPIKey, APIKeySource: "sk-test",
	}
	server := NewServer(NewRouter(domain.GatewayState{Providers: []domain.Provider{provider}}), monitor.NewStore())
	if !server.probeProviderModelAvailable(t.Context(), provider, "deepseek-v4.1-flash") {
		t.Fatal("probe should have reached the fake upstream successfully")
	}
	if gotBody["model"] != "deepseek-v4.1-flash" {
		t.Fatalf("probe model = %#v, want exact failed model", gotBody["model"])
	}
	if gotBody["max_tokens"] != float64(monitorProbeMaxTokens) && gotBody["max_completion_tokens"] != float64(monitorProbeMaxTokens) {
		t.Fatalf("probe max tokens = max_tokens:%#v max_completion_tokens:%#v, want %d", gotBody["max_tokens"], gotBody["max_completion_tokens"], monitorProbeMaxTokens)
	}
	messages, _ := gotBody["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("probe messages = %#v, want one minimal user turn", gotBody["messages"])
	}
}

func TestReprobeUnavailableModelsUsesExactModel(t *testing.T) {
	var probedModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(raw, &payload)
		probedModel, _ = payload["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	provider := domain.Provider{
		ID: "p-reprobe", Name: "p-reprobe", Protocol: domain.ProtocolOpenAIChat,
		BaseURL: upstream.URL, AuthType: domain.AuthTypeAPIKey, APIKeySource: "sk-test",
	}
	server := NewServer(NewRouter(domain.GatewayState{Providers: []domain.Provider{provider}}), monitor.NewStore())
	server.markModelUnavailable(provider.ID, "deepseek", false, "model_limited", "", nil)
	server.providerAvailabilityMu.Lock()
	state := server.modelAvailability[makeModelAvailabilityKey(provider.ID, "deepseek")]
	state.nextRetry = time.Now().Add(-time.Second)
	server.modelAvailability[makeModelAvailabilityKey(provider.ID, "deepseek")] = state
	server.providerAvailabilityMu.Unlock()

	server.reprobeUnavailableModels(t.Context())
	if probedModel != "deepseek" {
		t.Fatalf("recovery probed model %q, want deepseek", probedModel)
	}
	if _, ok := server.modelUnavailableSnapshot(provider.ID, "deepseek"); ok {
		t.Fatal("successful exact-model recovery did not clear the pair")
	}
}
