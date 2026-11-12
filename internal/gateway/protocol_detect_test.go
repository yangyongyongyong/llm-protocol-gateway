package gateway

import (
	"net/http"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// 基地址带 /anthropic 的，兄弟根是砍掉它的那一层；其余情况兄弟是 + /anthropic。
func TestProtocolProbeCandidatesFromAnthropicBase(t *testing.T) {
	t.Parallel()
	got := protocolProbeCandidates("https://opencode.ai/inference/go/anthropic/v1", domain.ProtocolClaude)
	byProtocol := map[domain.Protocol]string{}
	for _, c := range got {
		byProtocol[c.Protocol] = c.URL
	}
	if url := byProtocol[domain.ProtocolClaude]; url != "https://opencode.ai/inference/go/anthropic/v1/messages" {
		t.Fatalf("claude URL = %q", url)
	}
	if url := byProtocol[domain.ProtocolOpenAIChat]; url != "https://opencode.ai/inference/go/v1/chat/completions" {
		t.Fatalf("openai chat URL = %q", url)
	}
	if url := byProtocol[domain.ProtocolOpenAIResponses]; url != "https://opencode.ai/inference/go/v1/responses" {
		t.Fatalf("responses URL = %q", url)
	}
}

func TestProtocolProbeCandidatesFromOpenAIBase(t *testing.T) {
	t.Parallel()
	got := protocolProbeCandidates("https://api.example.com/v1", domain.ProtocolOpenAIChat)
	byProtocol := map[domain.Protocol]string{}
	for _, c := range got {
		byProtocol[c.Protocol] = c.URL
	}
	if url := byProtocol[domain.ProtocolOpenAIChat]; url != "https://api.example.com/v1/chat/completions" {
		t.Fatalf("chat URL = %q", url)
	}
	if url := byProtocol[domain.ProtocolClaude]; url != "https://api.example.com/v1/anthropic/messages" {
		t.Fatalf("claude URL = %q", url)
	}
}

// OpenCode Go 的 Claude 端点和 OpenAI 在同一个根下（/v1/messages），不是
// /anthropic 兄弟路径。同根候选必须在列表里，否则这条能力永远探不出来。
func TestProtocolProbeCandidatesIncludeSameRootClaude(t *testing.T) {
	t.Parallel()
	got := protocolProbeCandidates("https://opencode.ai/zen/go/v1", domain.ProtocolOpenAIChat)
	var urls []string
	for _, candidate := range got {
		if candidate.Protocol == domain.ProtocolClaude {
			urls = append(urls, candidate.URL)
		}
	}
	want := "https://opencode.ai/zen/go/v1/messages"
	for _, url := range urls {
		if url == want {
			return
		}
	}
	t.Fatalf("claude candidates = %v, want %q", urls, want)
}

// 404/405 是唯一的否定证据：实现了该协议的上游对不合格请求会回 401/403/400。
func TestClassifyProtocolProbe(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status int
		want   bool
		why    string
	}{
		{http.StatusOK, true, "能跑通当然存在"},
		{http.StatusUnauthorized, true, "鉴权先拦，说明路由在"},
		{http.StatusForbidden, true, "同上"},
		{http.StatusBadRequest, true, "参数不合格，路由在"},
		{http.StatusTooManyRequests, true, "限流，路由在"},
		{http.StatusNotFound, false, "路由不存在"},
		{http.StatusMethodNotAllowed, false, "方法不认，等于没有"},
		{http.StatusNotImplemented, false, "没实现"},
		{0, false, "连不上不能当成支持"},
	}
	for _, tc := range cases {
		if got := classifyProtocolProbe(tc.status); got != tc.want {
			t.Fatalf("classifyProtocolProbe(%d) = %v, want %v (%s)", tc.status, got, tc.want, tc.why)
		}
	}
}

// 协议直通只能优化备选顺序，不能越过显式首选。首选需要转换、备选可直通时，
// 仍应先试首选；首选失败后才轮到同协议备选。
func TestPreferNativeProtocolKeepsPrimaryFirst(t *testing.T) {
	t.Parallel()
	server := NewServer(NewRouter(domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "p-claude", Name: "claude", Protocol: domain.ProtocolClaude},
			{ID: "p-openai", Name: "openai", Protocol: domain.ProtocolOpenAIChat},
		},
	}), monitor.NewStore())

	got := server.preferNativeProtocol([]string{"p-claude", "p-openai"}, domain.ProtocolOpenAIChat, "")
	if len(got) != 2 || got[0] != "p-claude" || got[1] != "p-openai" {
		t.Fatalf("chain = %v, want the configured primary first", got)
	}
}

// 三节点链里只调整备选：首个备选需要转换，第二个备选可直通时，把后者提到
// 备选首位，但 chain[0] 永远不动。
func TestPreferNativeProtocolPromotesOnlyFallbackTail(t *testing.T) {
	t.Parallel()
	server := NewServer(NewRouter(domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "p-primary", Name: "primary", Protocol: domain.ProtocolOpenAIChat},
			{ID: "p-fallback", Name: "fallback", Protocol: domain.ProtocolOpenAIChat},
			{ID: "p-native", Name: "native", Protocol: domain.ProtocolOpenAIChat, AuthType: domain.AuthTypeAPIKey, ProtocolSupport: []string{"openai_chat", "openai_responses"}},
		},
	}), monitor.NewStore())

	got := server.preferNativeProtocol([]string{"p-primary", "p-fallback", "p-native"}, domain.ProtocolOpenAIResponses, "")
	if strings.Join(got, ",") != "p-primary,p-native,p-fallback" {
		t.Fatalf("chain = %v, want primary unchanged and native fallback next", got)
	}
}

// 这三种情况必须原样返回，免得把管理员的意图踩掉。
func TestPreferNativeProtocolLeavesOrderAlone(t *testing.T) {
	t.Parallel()
	server := NewServer(NewRouter(domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "p-claude", Name: "claude", Protocol: domain.ProtocolClaude},
			{ID: "p-openai", Name: "openai", Protocol: domain.ProtocolOpenAIChat},
		},
	}), monitor.NewStore())

	cases := []struct {
		name  string
		chain []string
		proto domain.Protocol
		pin   string
	}{
		{"已经是同协议开头", []string{"p-openai", "p-claude"}, domain.ProtocolOpenAIChat, ""},
		{"没有同协议节点", []string{"p-claude"}, domain.ProtocolOpenAIChat, ""},
		{"会话粘性钉住了", []string{"p-claude", "p-openai"}, domain.ProtocolOpenAIChat, "p-claude"},
		{"协议未知", []string{"p-claude", "p-openai"}, "", ""},
	}
	for _, tc := range cases {
		got := server.preferNativeProtocol(tc.chain, tc.proto, tc.pin)
		if len(got) != len(tc.chain) || got[0] != tc.chain[0] {
			t.Fatalf("%s: chain = %v, want unchanged %v", tc.name, got, tc.chain)
		}
	}
}
