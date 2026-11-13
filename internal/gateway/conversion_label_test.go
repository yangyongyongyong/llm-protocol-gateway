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
		// 两端相同时只写一个名字，不写 "X -> X"。
		{
			name:     "same protocol has no arrow",
			client:   domain.ProtocolOpenAIChat,
			upstream: domain.ProtocolOpenAIChat,
			want:     "OpenAI Chat",
		},
		{
			name:     "same responses has no arrow",
			client:   domain.ProtocolOpenAIResponses,
			upstream: domain.ProtocolOpenAIResponses,
			want:     "OpenAI Responses",
		},
		{
			name:     "same claude has no arrow",
			client:   domain.ProtocolClaude,
			upstream: domain.ProtocolClaude,
			want:     "Claude",
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

// 回归测试：标签只看两端协议，与 action 无关。
//
// 这条盯的是一个真实踩过的坑：曾经的实现在 action=="pass_through" 时只写客户端
// 协议，理由是"直通即两端相同"。那是错的——pass_through 表示「不许转换」，完全
// 可能收到与上游不同协议的请求（上游只认 Chat，客户端偏发 Responses）。那时日志
// 只剩一个 "OpenAI Responses"，把「上游其实是 Chat」这个排查 400 最关键的事实
// 吞掉了。
func TestConversionLabelIgnoresActionAndKeepsUpstreamVisible(t *testing.T) {
	// pass_through 且两端不同：必须完整写出两段。
	if got := conversionLabel(domain.ProtocolOpenAIResponses, domain.ProtocolOpenAIChat); got != "OpenAI Responses -> OpenAI Chat" {
		t.Fatalf("pass_through 下两端不同也必须写全，得到 %q", got)
	}
	// 路由决策里 pass_through + 协议不同，标签同样不能被简化成一个名字。
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
			Mode:           domain.RouteModePassThrough,
			Enabled:        true,
		}},
	})
	decision, err := router.DecideForProtocol("r1", domain.ProtocolOpenAIResponses)
	if err != nil {
		t.Fatalf("DecideForProtocol: %v", err)
	}
	if decision.Action != "pass_through" {
		t.Fatalf("这条路由应当判成 pass_through，实际 %q", decision.Action)
	}
	if decision.ConversionLabel != "OpenAI Responses -> OpenAI Chat" {
		t.Fatalf("协议流 = %q，want OpenAI Responses -> OpenAI Chat", decision.ConversionLabel)
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
