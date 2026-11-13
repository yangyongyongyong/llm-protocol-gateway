package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// claudeToChatServer stands up a server whose route sends a Claude Messages
// request to a WorkBuddy-style OpenAI-Chat upstream, and returns the server
// plus a pointer to the body the upstream last received.
func claudeToChatServer(t *testing.T, key domain.APIKey, providerDefaultDepth string) (*Server, *[]byte) {
	t.Helper()
	var received []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"id":"c1","model":"deepseek-v4.1-flash","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(upstream.Close)

	state := domain.GatewayState{
		Providers: []domain.Provider{{
			ID:                   "wb",
			Name:                 "wb",
			Protocol:             domain.ProtocolOpenAIChat,
			BaseURL:              upstream.URL,
			AuthType:             domain.AuthTypeWorkBuddy,
			DefaultThinkingDepth: providerDefaultDepth,
			WorkBuddy: &domain.WorkBuddyCredential{
				AccessToken: "test-token",
				ExpiresAt:   time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		}},
		Routes: []domain.Route{{
			ID:               "wb-claude",
			Name:             "wb-claude",
			ProviderID:       "wb",
			OutputProtocol:   domain.ProtocolClaude,
			OutputEndpointID: "claude-output",
			Enabled:          true,
		}},
		APIKeys: []domain.APIKey{key},
	}
	return NewServer(NewRouter(state), monitor.NewStore()), &received
}

func claudeCodeBody(thinking map[string]any) []byte {
	payload := map[string]any{
		"model":      "deepseek-v4.1-flash",
		"max_tokens": 64000,
		"stream":     false,
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
	}
	if thinking != nil {
		payload["thinking"] = thinking
	}
	body, _ := json.Marshal(payload)
	return body
}

func postClaude(t *testing.T, server *Server, token string, body []byte) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

// 用户的 Key 设了思考深度 max，而客户端请求里没带档位：上游必须收到 max，
// 否则 Key 上的设置等于没配。
func TestClaudePathAppliesKeyThinkingDepth(t *testing.T) {
	key := domain.APIKey{ID: "k1", Name: "k1", Key: "sk-test", RouteID: "wb-claude", ThinkingDepthOverride: "max", Enabled: true}
	server, received := claudeToChatServer(t, key, "")

	postClaude(t, server, "sk-test", claudeCodeBody(map[string]any{"type": "adaptive", "display": "omitted"}))

	var payload map[string]any
	if err := json.Unmarshal(*received, &payload); err != nil {
		t.Fatalf("upstream body unreadable: %v (%s)", err, *received)
	}
	if payload["reasoning_effort"] != "max" {
		t.Fatalf("reasoning_effort = %#v, want max (from the key)", payload["reasoning_effort"])
	}
}

// 没有 Key 覆盖时，客户端自己要求的 max 也要到达上游：这是 Claude Code
// 「思考深度」最常走的那条路。
func TestClaudePathCarriesClientEffort(t *testing.T) {
	key := domain.APIKey{ID: "k2", Name: "k2", Key: "sk-test2", RouteID: "wb-claude", Enabled: true}
	server, received := claudeToChatServer(t, key, "")

	body := `{"model":"deepseek-v4.1-flash","max_tokens":64000,"stream":false,` +
		`"thinking":{"type":"adaptive","display":"omitted"},"output_config":{"effort":"max"},` +
		`"messages":[{"role":"user","content":"hi"}]}`
	postClaude(t, server, "sk-test2", []byte(body))

	var payload map[string]any
	if err := json.Unmarshal(*received, &payload); err != nil {
		t.Fatalf("upstream body unreadable: %v (%s)", err, *received)
	}
	if payload["reasoning_effort"] != "max" {
		t.Fatalf("reasoning_effort = %#v, want max (from the client)", payload["reasoning_effort"])
	}
}

// Key 覆盖同样遵循「只改本来在思考的请求」：Claude Code 关掉思考的分类请求
// 不能被重新打开。
func TestClaudePathKeepsThinkingOffUnderKeyOverride(t *testing.T) {
	key := domain.APIKey{ID: "k3", Name: "k3", Key: "sk-test3", RouteID: "wb-claude", ThinkingDepthOverride: "max", Enabled: true}
	server, received := claudeToChatServer(t, key, "")

	postClaude(t, server, "sk-test3", claudeCodeBody(map[string]any{"type": "disabled"}))

	var payload map[string]any
	if err := json.Unmarshal(*received, &payload); err != nil {
		t.Fatalf("upstream body unreadable: %v (%s)", err, *received)
	}
	if effort, exists := payload["reasoning_effort"]; exists && effort != "" {
		t.Fatalf("a thinking-off request was given reasoning_effort %#v", effort)
	}
}
