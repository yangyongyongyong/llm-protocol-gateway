package gateway

import (
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 日志里那一列「协议流」按数据实际流动方向写：客户端协议 -> 上游协议。
//
// 曾经的写法是 "<上游> -> <客户端>"，于是 Codex 发来的 Responses 请求被记成
// "OpenAI Chat -> OpenAI Responses"，看上去像网关在做反向转换，排查时把人
// 往错的方向带。
func TestConversionLabelDirection(t *testing.T) {
	cases := []struct {
		name     string
		client   domain.Protocol
		upstream domain.Protocol
		want     string
	}{
		{
			name:     "convert responses client to chat upstream",
			client:   domain.ProtocolOpenAIResponses,
			upstream: domain.ProtocolOpenAIChat,
			want:     "OpenAI Responses -> OpenAI Chat",
		},
		{
			name:     "convert claude client to chat upstream",
			client:   domain.ProtocolClaude,
			upstream: domain.ProtocolOpenAIChat,
			want:     "Claude -> OpenAI Chat",
		},
		{
			name:     "convert chat client to claude upstream",
			client:   domain.ProtocolOpenAIChat,
			upstream: domain.ProtocolClaude,
			want:     "OpenAI Chat -> Claude",
		},
		// 同协议也保留两侧：日志列显示 oc->oc，而不是只剩一个 oc。
		{
			name:     "same chat keeps both sides",
			client:   domain.ProtocolOpenAIChat,
			upstream: domain.ProtocolOpenAIChat,
			want:     "OpenAI Chat -> OpenAI Chat",
		},
		{
			name:     "same responses keeps both sides",
			client:   domain.ProtocolOpenAIResponses,
			upstream: domain.ProtocolOpenAIResponses,
			want:     "OpenAI Responses -> OpenAI Responses",
		},
		{
			name:     "same claude keeps both sides",
			client:   domain.ProtocolClaude,
			upstream: domain.ProtocolClaude,
			want:     "Claude -> Claude",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := conversionLabel(tc.client, tc.upstream); got != tc.want {
				t.Fatalf("conversionLabel(%s, %s) = %q, want %q",
					tc.client, tc.upstream, got, tc.want)
			}
		})
	}
}

// 回归测试：协议流标签必须同时写出客户端协议和上游协议。
//
// 现在直通/转换完全由本次请求协议和 provider 声明协议决定，不存在可配置
// mode。Chat provider 收到 Responses 客户端请求时必须走转换，日志仍要完整
// 写出 "OpenAI Responses -> OpenAI Chat"，不能只剩客户端一侧。
func TestConversionLabelKeepsUpstreamVisible(t *testing.T) {
	if got := conversionLabel(domain.ProtocolOpenAIResponses, domain.ProtocolOpenAIChat); got != "OpenAI Responses -> OpenAI Chat" {
		t.Fatalf("两端不同时必须写全，得到 %q", got)
	}
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{
			ID:       "p1",
			Name:     "p1",
			Protocol: domain.ProtocolOpenAIChat,
		}},
		Routes: []domain.Route{{
			ID:             "r1",
			Name:           "r1",
			ProviderID:     "p1",
			OutputProtocol: domain.ProtocolOpenAIChat,
			Enabled:        true,
		}},
	})
	decision, err := router.DecideForProtocol("r1", domain.ProtocolOpenAIResponses)
	if err != nil {
		t.Fatalf("DecideForProtocol: %v", err)
	}
	if decision.Action != "convert" {
		t.Fatalf("Chat provider 收到 Responses 请求必须转换，实际 %q", decision.Action)
	}
	if decision.ConversionLabel != "OpenAI Responses -> OpenAI Chat" {
		t.Fatalf("协议流 = %q，want OpenAI Responses -> OpenAI Chat", decision.ConversionLabel)
	}

	same, err := router.DecideForProtocol("r1", domain.ProtocolOpenAIChat)
	if err != nil {
		t.Fatalf("DecideForProtocol same protocol: %v", err)
	}
	if same.Action != "pass_through" {
		t.Fatalf("同协议必须直通，实际 %q", same.Action)
	}
	if same.ConversionLabel != "OpenAI Chat -> OpenAI Chat" {
		t.Fatalf("同协议标签 = %q，want OpenAI Chat -> OpenAI Chat", same.ConversionLabel)
	}
}

// 标签里的协议名必须能被 monitor.OutputProtocolFromFlow 反解回客户端协议，
// 否则协议用量统计会整列空掉。这条盯的是两处实现的口径对齐。
func TestConversionLabelRoundTripsThroughStats(t *testing.T) {
	label := conversionLabel(domain.ProtocolOpenAIResponses, domain.ProtocolOpenAIChat)
	if label != "OpenAI Responses -> OpenAI Chat" {
		t.Fatalf("label = %q", label)
	}
	// 断言方向：客户端协议在左边，统计按左侧取值。
	if got := outputProtocolNameForTest(label); got != "OpenAI Responses" {
		t.Fatalf("统计取到的输出协议 = %q, want OpenAI Responses", got)
	}
}

// outputProtocolNameForTest 是 monitor.OutputProtocolFromFlow 的最小复刻：
// 同一套取值规则，避免 gateway 包为了一个断言去依赖 monitor 的全部初始化。
func outputProtocolNameForTest(flow string) string {
	idx := -1
	for i := 0; i+1 < len(flow); i++ {
		if flow[i] == '-' && flow[i+1] == '>' {
			idx = i
			break
		}
	}
	if idx < 0 {
		return flow
	}
	out := flow[:idx]
	for len(out) > 0 && out[len(out)-1] == ' ' {
		out = out[:len(out)-1]
	}
	return out
}
