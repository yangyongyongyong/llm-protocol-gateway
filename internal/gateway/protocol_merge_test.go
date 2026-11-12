package gateway

import (
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 已知结论覆盖，未知结论保留。
//
// 这条规则的理由是探测里的超时是网络现象，不是上游说「我没有这个协议」。一次跨境
// 抖动不该让卡片上的协议徽标无声消失，更不该让它从此不再参与同协议直通的挑选。
func TestMergeProtocolSupport(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		previous []string
		results  []protocolDetection
		want     []string
	}{
		{
			name:     "首次探测：只有支持的进去",
			previous: nil,
			results: []protocolDetection{
				{Protocol: domain.ProtocolClaude, Supported: true},
				{Protocol: domain.ProtocolOpenAIChat, Supported: false},
				{Protocol: domain.ProtocolOpenAIResponses, Supported: true},
			},
			want: []string{"claude", "openai_responses"},
		},
		{
			name:     "超时不清掉上次的结论",
			previous: []string{"claude", "openai_chat"},
			results: []protocolDetection{
				{Protocol: domain.ProtocolClaude, Supported: true},
				{Protocol: domain.ProtocolOpenAIChat, Unknown: true},
			},
			want: []string{"claude", "openai_chat"},
		},
		{
			name:     "404 才移除",
			previous: []string{"claude", "openai_chat"},
			results: []protocolDetection{
				{Protocol: domain.ProtocolClaude, Supported: true},
				{Protocol: domain.ProtocolOpenAIChat, Supported: false},
			},
			want: []string{"claude"},
		},
		{
			name:     "新发现的协议加进来",
			previous: []string{"claude"},
			results: []protocolDetection{
				{Protocol: domain.ProtocolClaude, Supported: true},
				{Protocol: domain.ProtocolOpenAIResponses, Supported: true},
			},
			want: []string{"claude", "openai_responses"},
		},
		{
			name:     "全都没结论时原样保留",
			previous: []string{"claude", "openai_chat"},
			results: []protocolDetection{
				{Protocol: domain.ProtocolClaude, Unknown: true},
				{Protocol: domain.ProtocolOpenAIChat, Unknown: true},
			},
			want: []string{"claude", "openai_chat"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeProtocolSupport(tc.previous, tc.results)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
