package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestProviderIndexKeepsDuplicateRowsAndRowModels(t *testing.T) {
	t.Parallel()
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{ID: "shared", Name: "shared", Protocol: domain.ProtocolOpenAIChat}},
		Routes:    []domain.Route{{ID: "r1", Name: "r1", ProviderID: "shared", OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true}},
	})
	key, err := router.AddAPIKey(domain.APIKey{
		Name:                   "rows",
		RouteID:                "r1",
		ModelOverride:          "primary-model",
		FallbackProviderIDs:    []string{"shared", "shared"},
		FallbackModels:         []string{"second-model", "third-model"},
		FallbackModelOverrides: map[string]string{"shared": "stale-map-model"},
		ScheduleMode:           scheduleModeProviderIndex,
	})
	if err != nil {
		t.Fatalf("add key: %v", err)
	}
	if got := strings.Join(key.FallbackProviderIDs, ","); got != "shared,shared" {
		t.Fatalf("duplicate rows were collapsed: %v", key.FallbackProviderIDs)
	}
	if got := strings.Join(key.FallbackModels, ","); got != "second-model,third-model" {
		t.Fatalf("row models = %v", key.FallbackModels)
	}
	entries := apiKeyProviderEntries("shared", key)
	if len(entries) != 3 {
		t.Fatalf("entries = %#v, want 3", entries)
	}
	if entries[0].ModelOverride != "primary-model" || entries[1].ModelOverride != "second-model" || entries[2].ModelOverride != "third-model" {
		t.Fatalf("entry models = %#v", entries)
	}
}

func TestProviderIndexRoutesPNAndPN1MToExactRow(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var models []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		model, _ := payload["model"].(string)
		mu.Lock()
		models = append(models, model)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-row", "object": "chat.completion",
			"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": model}}},
			"usage":   map[string]any{"prompt_tokens": 2, "completion_tokens": 1, "total_tokens": 3},
		})
	}))
	defer upstream.Close()

	provider := domain.Provider{
		ID: "shared", Name: "shared", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeAPIKey, BaseURL: upstream.URL + "/v1", APIKeySource: "literal:sk-test",
		DefaultModel: "third-model",
		Models: []domain.Model{
			{ID: "primary-model", ProviderID: "shared", Protocol: domain.ProtocolOpenAIChat},
			{ID: "second-model", ProviderID: "shared", Protocol: domain.ProtocolOpenAIChat},
			{ID: "third-model", ProviderID: "shared", Protocol: domain.ProtocolOpenAIChat},
		},
	}
	route := domain.Route{ID: "r1", Name: "r1", ProviderID: "shared", OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true}
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{provider},
		Routes:    []domain.Route{route},
		APIKeys: []domain.APIKey{{
			ID: "k-rows", Name: "K rows", Key: "sk-gw-rows", RouteID: route.ID,
			ModelOverride:       "primary-model",
			FallbackProviderIDs: []string{"shared", "shared"},
			FallbackModels:      []string{"second-model", "third-model"},
			ScheduleMode:        scheduleModeProviderIndex,
			Enabled:             true, StreamEnabled: true,
		}},
	})
	server := NewServer(router, monitor.NewStore())

	call := func(model string) *httptest.ResponseRecorder {
		body := `{"model":"` + model + `","stream":false,"messages":[{"role":"user","content":"hi"}]}`
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer sk-gw-rows")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		return rec
	}

	if rec := call("p1"); rec.Code != http.StatusOK {
		t.Fatalf("p1 status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := call("p2"); rec.Code != http.StatusOK {
		t.Fatalf("p2 status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := call("p3[1m]"); rec.Code != http.StatusOK {
		t.Fatalf("p3[1m] status=%d body=%s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	gotModels := append([]string(nil), models...)
	mu.Unlock()
	if len(gotModels) != 3 || gotModels[0] != "primary-model" || gotModels[1] != "second-model" || gotModels[2] != "third-model" {
		t.Fatalf("upstream models = %v", gotModels)
	}

	if rec := call("p4"); rec.Code != http.StatusBadRequest {
		t.Fatalf("p4 status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
}

func TestProviderIndexModelAliasesAreGeneratedForFiveRows(t *testing.T) {
	t.Parallel()
	key := domain.APIKey{
		ScheduleMode:        scheduleModeProviderIndex,
		FallbackProviderIDs: []string{"a", "b", "c", "d", "e", "f"},
	}
	got := providerIndexModelAliases(key, providerIndexCatalogLimit)
	want := []string{"p1", "p1[1m]", "p2", "p2[1m]", "p3", "p3[1m]", "p4", "p4[1m]", "p5", "p5[1m]"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("aliases=%v want=%v", got, want)
	}
	key.ScheduleMode = scheduleModeOrder
	if got := providerIndexModelAliases(key, providerIndexCatalogLimit); len(got) != 0 {
		t.Fatalf("non-provider-index mode should not publish p aliases: %v", got)
	}
}

func TestProviderIndexDesktopHashAliasResolvesToPN(t *testing.T) {
	t.Parallel()
	provider := domain.Provider{ID: "shared", Protocol: domain.ProtocolClaude, DefaultModel: "m1"}
	route := domain.Route{ID: "r1", ProviderID: "shared", OutputProtocol: domain.ProtocolClaude, Enabled: true}
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{provider},
		Routes:    []domain.Route{route},
	})
	key := domain.APIKey{ScheduleMode: scheduleModeProviderIndex, FallbackProviderIDs: []string{"shared", "shared"}}
	hashed := desktopModelAlias("p3") + claudeContextTag1M
	model, logModel := resolveConsumerModel(router, route, key, true, hashed)
	if model != "p3" || logModel != "p3" {
		t.Fatalf("desktop hash alias resolved to %q/%q, want p3", model, logModel)
	}
}
