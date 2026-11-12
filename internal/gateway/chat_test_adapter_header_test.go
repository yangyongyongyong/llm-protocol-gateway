package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// 「对话测试」必须和真实转发发一样的头。
//
// 这条测试盯的是一个真实踩过的坑：OpenCode Go 要求每个请求带
// x-opencode-session，缺了直接 400 MissingSessionID。真实转发路径
// （proxyOpenAIChat）会应用 per-provider 的 requestAdapter.headers，而对话测试
// 那条通用发送路径漏了这一步 —— 于是「填了 key 也测不过」，但真实流量其实是好的。
func TestProviderChatTestSendsRequestAdapterHeaders(t *testing.T) {
	t.Parallel()
	gotSession := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case gotSession <- r.Header.Get("x-opencode-session"):
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	provider := domain.Provider{
		ID: "oc-go", Name: "OpenCode Go", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeAPIKey, BaseURL: upstream.URL + "/v1",
		APIKeySource: "literal:sk-test", AuthHeader: "Authorization",
		DefaultModel: "beam-free",
		RequestAdapter: &domain.RequestAdapter{
			Headers: map[string]string{"x-opencode-session": "lpg-gateway-session"},
		},
	}
	server := NewServer(NewRouter(domain.GatewayState{Providers: []domain.Provider{provider}}), monitor.NewStore())

	result, _ := server.testProviderChat(
		httptest.NewRequest(http.MethodPost, "/__providers/oc-go/chat-test", nil),
		"oc-go",
		providerChatTestRequest{UserPrompt: "say OK"},
		time.Now(),
	)
	if ok, _ := result["success"].(bool); !ok {
		body, _ := json.Marshal(result)
		t.Fatalf("对话测试应当成功：%s", body)
	}

	select {
	case session := <-gotSession:
		if session != "lpg-gateway-session" {
			t.Fatalf("x-opencode-session = %q，说明 requestAdapter 头没发出去", session)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("上游没被调用")
	}
}
