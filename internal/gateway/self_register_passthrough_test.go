package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// TestSelfRegisterProviderPassesThroughClaudeByteForByte 锁住自助注册 Provider
// 的核心承诺：控制台把协议选成 Claude，网关就按 Claude 原样透传，绝不转换。
//
// 这条路径以前是走不通的——控制台把 self_register 硬锁成 openai_chat，于是
// 一个原生说 Claude 的本地服务每次请求都要被转一遍协议（body 重写、SSE 重新
// 编码），既慢又容易在 thinking/tool_use 这类形状上失真。现在协议由控制台选，
// 同协议必须一个字节都不改。
func TestSelfRegisterProviderPassesThroughClaudeByteForByte(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("x-api-key")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude-sonnet-5","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()

	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "sr1", Name: "内网穿透自助注册", Protocol: domain.ProtocolClaude,
			AuthType: domain.AuthTypeSelfRegister, BaseURL: upstream.URL + "/v1/messages",
			APIKeySource: "literal:upstream-secret", AuthHeader: "x-api-key",
		}},
		Routes: []domain.Route{{
			ID: "r1", Name: "R1", ProviderID: "sr1",
			OutputProtocol: domain.ProtocolClaude, Mode: domain.RouteModeAuto, Enabled: true,
		}},
		APIKeys: []domain.APIKey{{
			ID: "k1", Name: "K1", Key: "sk-gw", RouteID: "r1", Enabled: true,
		}},
	})
	server := NewServer(router, monitor.NewStore())

	// 刻意带上 Claude 独有的字段（thinking、tool_use、cache_control）：走转换
	// 路径时这些最容易被丢掉或改写，透传路径必须原样送到上游。
	body := `{"model":"claude-sonnet-5","max_tokens":256,` +
		`"thinking":{"type":"enabled","budget_tokens":1024},` +
		`"system":[{"type":"text","text":"be brief","cache_control":{"type":"ephemeral"}}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	request := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(body))
	request.Header.Set("x-api-key", "sk-gw")
	request.Header.Set("anthropic-version", "2023-06-01")
	recorder := httptest.NewRecorder()
	server.handleClaudeMessages(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("upstream path = %q, want /v1/messages", gotPath)
	}
	// 上游密钥来自 apiKeySource，鉴权头按 Provider 的 authHeader 走。
	if gotAuth != "upstream-secret" {
		t.Fatalf("upstream x-api-key = %q, want the provider's own credential", gotAuth)
	}
	// 字节级透传：请求体解析后与客户端发出的完全一致（网关只允许补 model 等
	// 极少数规范化动作，这里连这些都不该发生）。
	var sent, received map[string]any
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("client body unparsable: %v", err)
	}
	if err := json.Unmarshal([]byte(gotBody), &received); err != nil {
		t.Fatalf("upstream body unparsable: %v\n%s", err, gotBody)
	}
	sentJSON, _ := json.Marshal(sent)
	receivedJSON, _ := json.Marshal(received)
	if string(sentJSON) != string(receivedJSON) {
		t.Fatalf("request was rewritten, not passed through:\nsent:     %s\nreceived: %s", sentJSON, receivedJSON)
	}
}
