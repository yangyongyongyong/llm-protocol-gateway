package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func customCatalogFixture(t *testing.T) (*Server, string) {
	t.Helper()
	providers := []domain.Provider{
		{
			ID: "p1", Name: "P1", Protocol: domain.ProtocolOpenAIChat,
			Models: []domain.Model{
				{ID: "m1", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, ContextLength: 128000, InMenu: true},
			},
		},
		{
			ID: "p2", Name: "P2", Protocol: domain.ProtocolOpenAIChat,
			Models: []domain.Model{
				{ID: "m-fallback", ProviderID: "p2", Protocol: domain.ProtocolOpenAIChat, ContextLength: 200000, InMenu: true},
			},
		},
	}
	route := domain.Route{ID: "r1", Name: "R1", ProviderID: "p1", OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true}
	key := domain.APIKey{
		ID: "k-catalog", Name: "catalog", Key: "sk-gw-catalog", RouteID: route.ID, Enabled: true,
		ScheduleMode:        scheduleModeProviderIndex,
		ModelOverride:       "m1",
		ModelAliases:        map[string]string{"fast": "m-fallback", "alias-only": "m-target"},
		FallbackProviderIDs: []string{"p2", "p2"},
		FallbackModels:      []string{"m-fallback", "m-row-2"},
	}
	router := NewRouter(domain.GatewayState{
		Providers: providers,
		Routes:    []domain.Route{route},
		APIKeys:   []domain.APIKey{key},
	})
	return NewServer(router, monitor.NewStore()), key.Key
}

func TestModelDiscoveryInjectsEveryKeyCustomModelName(t *testing.T) {
	server, token := customCatalogFixture(t)
	openai := getModels(t, server, token, "curl/8.7.1")
	openaiByID := map[string]map[string]any{}
	for _, entry := range openai {
		openaiByID[entry["id"].(string)] = entry
	}
	want := []string{
		"default", "default[1m]",
		"p1", "p1[1m]", "p2", "p2[1m]", "p3", "p3[1m]",
		"m1", "fast", "m-fallback", "alias-only", "m-target", "m-row-2",
	}
	for _, id := range want {
		if _, ok := openaiByID[id]; !ok {
			t.Fatalf("OpenAI model discovery missing %q; got %v", id, openaiByID)
		}
	}
	if got, _ := openaiByID["default"]["context_length"].(float64); got != contextLength272K {
		t.Fatalf("default context_length=%v want %d", got, contextLength272K)
	}
	if got, _ := openaiByID["default[1m]"]["context_length"].(float64); got != contextLength1M {
		t.Fatalf("default[1m] context_length=%v want %d", got, contextLength1M)
	}

	request := httptest.NewRequest(http.MethodGet, "/anthropic/v1/models", nil)
	request.Header.Set("x-api-key", token)
	recorder := httptest.NewRecorder()
	server.handleClaudeModels(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("claude models status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	claudeIDs := map[string]bool{}
	for _, entry := range payload.Data {
		claudeIDs[entry.ID] = true
	}
	for _, id := range want {
		if !claudeIDs[id] {
			t.Fatalf("Claude model discovery missing %q; got %v", id, claudeIDs)
		}
	}
}
