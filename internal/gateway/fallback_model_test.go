package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestFallbackModelOverrideOptionalAcrossProtocols(t *testing.T) {
	t.Parallel()
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "p-openai", Name: "p-openai", Protocol: domain.ProtocolOpenAIChat},
			{ID: "p-openai-2", Name: "p-openai-2", Protocol: domain.ProtocolOpenAIChat},
			{ID: "p-claude", Name: "p-claude", Protocol: domain.ProtocolClaude},
		},
		Routes: []domain.Route{
			{ID: "r1", Name: "r1", ProviderID: "p-openai", OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true},
		},
	})

	key, err := router.AddAPIKey(domain.APIKey{
		Name: "same-protocol", RouteID: "r1",
		FallbackProviderIDs: []string{"p-openai-2"},
	})
	if err != nil {
		t.Fatalf("same-protocol fallback should not require a model override: %v", err)
	}
	if len(key.FallbackModelOverrides) != 0 {
		t.Fatalf("unexpected overrides: %#v", key.FallbackModelOverrides)
	}

	if _, err := router.AddAPIKey(domain.APIKey{
		Name: "cross-protocol-missing", RouteID: "r1",
		FallbackProviderIDs: []string{"p-claude"},
	}); err != nil {
		// 跨协议备选没锁模型不再是错误：它自己的模型目录会给出默认模型
		// （见 resolveConsumerModelOnProvider / applyProviderModelFallback）。
		t.Fatalf("cross-protocol fallback without a model override should be accepted: %v", err)
	}

	if _, err := router.AddAPIKey(domain.APIKey{
		Name: "cross-protocol-pinned", RouteID: "r1",
		FallbackProviderIDs:    []string{"p-claude"},
		FallbackModelOverrides: map[string]string{"p-claude": "claude-x"},
	}); err != nil {
		t.Fatalf("cross-protocol fallback with a model override should be accepted: %v", err)
	}

	// Switching the fallback to a different protocol without a pinned model is
	// accepted too: the chain must not be blocked by "设为首选" across protocols.
	if _, err := router.UpdateAPIKey(key.ID, domain.APIKey{
		Name:                "same-protocol",
		FallbackProviderIDs: []string{"p-claude"},
	}); err != nil {
		t.Fatalf("update to a cross-protocol fallback without a model override should be accepted: %v", err)
	}
}

// 跨协议备选没有 fallbackModelOverrides 时也必须能接管：网关自动转换协议，
// 模型按备选自己的目录/默认模型解析，不把“锁模型”当成可用性的前置条件。
func TestFailoverCrossProtocolWithoutModelOverrideUsesFallbackDefault(t *testing.T) {
	modelCh := make(chan string, 1)
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("fallback path = %q, want /v1/messages", r.URL.Path)
		}
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if model, _ := payload["model"].(string); model != "" {
			modelCh <- model
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_backup", "type": "message", "role": "assistant", "model": "claude-backup",
			"content":     []any{map[string]any{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer live.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream unavailable"}}`))
	}))
	defer down.Close()

	preferred := domain.Provider{
		ID: "p-chat", Name: "p-chat", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeAPIKey, BaseURL: down.URL + "/v1", APIKeySource: "sk-a", HealthStatus: "healthy",
	}
	backup := domain.Provider{
		ID: "p-claude", Name: "p-claude", Protocol: domain.ProtocolClaude,
		AuthType: domain.AuthTypeAPIKey, BaseURL: live.URL + "/v1", APIKeySource: "sk-b", HealthStatus: "healthy",
		DefaultModel: "claude-backup",
		Models:       []domain.Model{{ID: "claude-backup", ProviderID: "p-claude", Protocol: domain.ProtocolClaude}},
	}
	route := domain.Route{ID: "r-cross", Name: "r-cross", ProviderID: preferred.ID, OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true}
	apiKey := domain.APIKey{
		ID: "k-cross", Name: "k-cross", Key: "sk-gw-test-cross-protocol", RouteID: route.ID,
		Enabled: true, StreamEnabled: true,
		FallbackProviderIDs: []string{backup.ID},
	}
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{preferred, backup},
		Routes:    []domain.Route{route},
		APIKeys:   []domain.APIKey{apiKey},
	})
	server := NewServer(router, monitor.NewStore())

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-requested","stream":false,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+apiKey.Key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected the cross-protocol fallback to serve without a locked model, got %d: %s", rec.Code, rec.Body.String())
	}
	select {
	case got := <-modelCh:
		if got != "claude-backup" {
			t.Fatalf("fallback should receive its own default model, got %q", got)
		}
	default:
		t.Fatal("cross-protocol fallback provider was never called")
	}
}

func TestResolveConsumerModelOnProviderUsesAttemptProviderCatalog(t *testing.T) {
	t.Parallel()
	backup := domain.Provider{
		ID: "p-backup", Protocol: domain.ProtocolOpenAIChat,
		DefaultModel: "backup-default",
		Models:       []domain.Model{{ID: "backup-default", ProviderID: "p-backup"}},
	}
	got, _ := resolveConsumerModelOnProvider(backup, domain.APIKey{}, false, "primary-only-model")
	if got != "backup-default" {
		t.Fatalf("expected the backup's default model, got %q", got)
	}
}

// TestFailoverSameProtocolReusesRequestModel pins the user-facing rule: a
// backup that speaks the same protocol inherits the request model, so a key
// without a fallback model override still fails over instead of skipping the
// backup.
func TestFailoverSameProtocolReusesRequestModel(t *testing.T) {
	modelCh := make(chan string, 1)
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if model, _ := payload["model"].(string); model != "" {
			modelCh <- model
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-backup", "object": "chat.completion",
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 4, "completion_tokens": 1, "total_tokens": 5},
		})
	}))
	defer live.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream unavailable"}}`))
	}))
	defer down.Close()

	preferred := domain.Provider{
		ID: "p-pref", Name: "p-pref", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeAPIKey, BaseURL: down.URL + "/v1", APIKeySource: "sk-a", HealthStatus: "healthy",
	}
	backup := domain.Provider{
		ID: "p-back", Name: "p-back", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeAPIKey, BaseURL: live.URL + "/v1", APIKeySource: "sk-b", HealthStatus: "healthy",
	}
	route := domain.Route{ID: "r-fb", Name: "r-fb", ProviderID: preferred.ID, OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true}
	apiKey := domain.APIKey{
		ID: "k-fb", Name: "k-fb", Key: "sk-gw-test-fallback-model", RouteID: route.ID,
		Enabled: true, StreamEnabled: true,
		FallbackProviderIDs: []string{backup.ID},
	}
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{preferred, backup},
		Routes:    []domain.Route{route},
		APIKeys:   []domain.APIKey{apiKey},
	})
	server := NewServer(router, monitor.NewStore())

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-requested","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+apiKey.Key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected the backup to serve after the primary failed, got %d: %s", rec.Code, rec.Body.String())
	}
	select {
	case got := <-modelCh:
		if got != "gpt-requested" {
			t.Fatalf("backup should receive the request model, got %q", got)
		}
	default:
		t.Fatal("backup provider was never called")
	}
}
