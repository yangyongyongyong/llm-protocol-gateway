package gateway

import (
	"net/http"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
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
