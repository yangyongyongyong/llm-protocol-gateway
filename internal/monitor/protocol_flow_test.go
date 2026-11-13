package monitor

import "testing"

// protocol_flow 的方向在 2026-11-13 改过：以前写 "<上游> -> <客户端>"，现在写
// "<客户端> -> <上游>"，直通则只写一个协议名。协议用量统计按这个字符串取
// 「输出协议」（客户端那一侧），所以要盯住左侧取值。
//
// 旧方向没法在解析时兼容——"Claude -> OpenAI Chat" 在两种格式下都是合法表达，
// 含义却相反。旧行靠一次数据订正（protocol_flow_direction 迁移）翻正，不是靠
// 这里猜。这条测试锁的是「解析只认新格式」这个约定。
func TestOutputProtocolFromFlow(t *testing.T) {
	cases := []struct {
		name string
		flow string
		want string
	}{
		// 转换：客户端协议在左。
		{"convert responses to chat", "OpenAI Responses -> OpenAI Chat", "OpenAI Responses"},
		{"convert claude to chat", "Claude -> OpenAI Chat", "Claude"},
		{"convert chat to claude", "OpenAI Chat -> Claude", "OpenAI Chat"},
		{"convert responses to claude", "OpenAI Responses -> Claude", "OpenAI Responses"},

		// 直通：只写一个协议名。
		{"passthrough chat", "OpenAI Chat", "OpenAI Chat"},
		{"passthrough responses", "OpenAI Responses", "OpenAI Responses"},
		{"passthrough claude", "Claude", "Claude"},

		// 认不出来的不该硬编一个协议出来。
		{"empty", "", ""},
		{"unrelated", "embeddings", ""},
		{"unknown both sides", "Foo -> Bar", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OutputProtocolFromFlow(tc.flow); got != tc.want {
				t.Fatalf("OutputProtocolFromFlow(%q) = %q, want %q", tc.flow, got, tc.want)
			}
		})
	}
}
