package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// OpenCode Go 没有 Anthropic count_tokens 端点。Claude Code 启动时的这条旁路
// 必须由网关本地返回占位，不能把 /v1/messages/count_tokens 硬拼到 Chat base URL
// 后面，否则会得到 /v1/v1/messages/count_tokens 并 404。
func TestOpenCodeGoCountTokensReturnsLocalStub(t *testing.T) {
	state := domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "opencode-go", Name: "OpenCode Go", Protocol: domain.ProtocolOpenAIChat,
			BaseURL: "https://opencode.ai/zen/go/v1", AuthType: domain.AuthTypeAPIKey,
			APIKeySource: "literal:oc_sk-test", ProtocolSupport: []string{"openai_chat", "claude"},
		}},
		Routes: []domain.Route{{
			ID: "opencode-go-claude", Name: "OpenCode Go · Claude", ProviderID: "opencode-go",
			OutputProtocol: domain.ProtocolClaude, Enabled: true,
		}},
		APIKeys: []domain.APIKey{{
			ID: "key1", Name: "key1", Key: "sk-gw-test", RouteID: "opencode-go-claude", Enabled: true,
		}},
	}
	server := NewServer(NewRouter(state), monitor.NewStore())
	request := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens",
		strings.NewReader(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("x-api-key", "sk-gw-test")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	server.handleClaudeCountTokens(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 local stub (body %s)", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"input_tokens"`) {
		t.Fatalf("stub body = %s, want input_tokens", recorder.Body.String())
	}
}

func TestOpenCodeGoCountTokensIsStubbed(t *testing.T) {
	provider := domain.Provider{
		AuthType: domain.AuthTypeAPIKey,
		Protocol: domain.ProtocolOpenAIChat,
		BaseURL:  "https://opencode.ai/zen/go/v1",
	}
	if !shouldStubClaudeCountTokens(provider) {
		t.Fatal("OpenCode Go must use the local count_tokens stub")
	}
}
