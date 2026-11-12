package gateway

import (
	"net/http"
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

// 链上存在同协议节点时，把它提到最前——直通代替转换。
func TestPreferNativeProtocolPromotesMatchingNode(t *testing.T) {
	t.Parallel()
	server := NewServer(NewRouter(domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "p-claude", Name: "claude", Protocol: domain.ProtocolClaude},
			{ID: "p-openai", Name: "openai", Protocol: domain.ProtocolOpenAIChat},
		},
	}), monitor.NewStore())

	got := server.preferNativeProtocol([]string{"p-claude", "p-openai"}, domain.ProtocolOpenAIChat, "")
	if len(got) != 2 || got[0] != "p-openai" || got[1] != "p-claude" {
		t.Fatalf("chain = %v, want openai first", got)
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
