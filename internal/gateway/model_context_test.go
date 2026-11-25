package gateway

import "testing"

func TestResolveModelContextLengthClaude(t *testing.T) {
	cases := []struct {
		id       string
		reported int
		want     int
	}{
		{"claude-opus-5", 0, contextLength1M},
		{"claude-opus-5", 128000, contextLength1M},
		{"claude-opus-4-8", 0, contextLength1M},
		{"claude-opus-4-8", 128000, contextLength1M},
		{"claude-opus-4-8[1m]", 0, contextLength1M},
		{"opus", 0, contextLength1M},
		{"claude-sonnet-5", 0, contextLength1M},
		{"sonnet", 0, contextLength1M},
		{"claude-sonnet-4-6", 0, contextLength1M},
		{"claude-opus-4-7", 0, contextLength1M},
		{"claude-fable-5", 0, contextLength1M},
		{"claude-haiku-4-5-20251001", 0, contextLength200K},
		{"haiku", 0, contextLength200K},
		{"claude-sonnet-4-5-20250929", 0, contextLength200K},
		{"claude-opus-4-5-20251101", 0, contextLength200K},
		{"claude-4.5-opus-high", 0, contextLength200K},
		{"claude-4-sonnet", 0, contextLength200K},
		{"gpt-5.5", 0, contextLength1M},
		{"gpt-5.5", 256000, contextLength1M},
		{"gpt-5.6-terra", 0, contextLength1050K},
		{"gpt-5.6-luna", 0, contextLength1050K},
		{"gpt-6.1-terra", 0, contextLength500K},
		{"gpt-4o", 0, contextLength272K},
		{"gpt-4.1", 128000, contextLength272K},
		{"gpt-5.2", 0, contextLength272K},
		{"gpt-5-mini", 0, contextLength272K},
		{"claude-opus-4-8", 2_000_000, 2_000_000},
	}
	for _, tc := range cases {
		if got := resolveModelContextLength(tc.id, tc.reported); got != tc.want {
			t.Fatalf("%q reported=%d: got %d want %d", tc.id, tc.reported, got, tc.want)
		}
	}
}

func TestResolveModelContextLengthGLM(t *testing.T) {
	// Specs from https://docs.bigmodel.cn/cn/guide/start/model-overview
	cases := []struct {
		id   string
		want int
	}{
		{"glm-5.2", contextLength1M},
		{"GLM-5.2", contextLength1M},
		{"glm-5.2[1m]", contextLength1M},
		{"glm_5.2", contextLength1M},
		{"glm-4-long", contextLength1M},
		{"glm-5.1", contextLength200K},
		{"glm-5", contextLength200K},
		{"glm-5-turbo", contextLength200K},
		{"glm-5v-turbo", contextLength200K},
		{"glm-4.7", contextLength200K},
		{"glm-4.7-flash", contextLength200K},
		{"glm-4.6", contextLength200K},
		{"glm-4.6v", contextLengthDefault},
		{"glm-4.5-air", contextLengthDefault},
		{"glm-4.5-flash", contextLengthDefault},
		{"glm-4-flash-250414", contextLengthDefault},
		{"codegeex-4", contextLengthDefault},
	}
	for _, tc := range cases {
		if got := resolveModelContextLength(tc.id, 0); got != tc.want {
			t.Fatalf("%q: got %d want %d", tc.id, got, tc.want)
		}
		// Under-reported upstream values should still be corrected for known IDs.
		if got := resolveModelContextLength(tc.id, 128000); got != tc.want && tc.want > 128000 {
			t.Fatalf("%q reported=128000: got %d want %d", tc.id, got, tc.want)
		}
	}
}

func TestDefaultClaudeMaxTokens(t *testing.T) {
	if got := defaultClaudeMaxTokens("claude-opus-5"); got != 128_000 {
		t.Fatalf("claude-opus-5: got %d want 128000", got)
	}
	if got := defaultClaudeMaxTokens("claude-fable-5"); got != 128_000 {
		t.Fatalf("claude-fable-5: got %d want 128000", got)
	}
	if got := defaultClaudeMaxTokens("claude-haiku-4-5-20251001"); got != 64_000 {
		t.Fatalf("haiku: got %d want 64000", got)
	}
}

func TestKnownOpenAIContextLengthCoversWholeGPTFamily(t *testing.T) {
	// Codex 自带目录把普通 GPT 口径定为 272K；中转经常只给模型 id、不给
	// context 字段，所以 gpt-* 都必须命中这个兜底，不能只覆盖 gpt-5.x。
	for _, id := range []string{"gpt-4o", "gpt-4.1", "gpt-4.1-mini", "gpt-5", "gpt-5.2", "gpt-5-mini", "relay/gpt-5.2"} {
		if got := resolveModelContextLength(id, 0); got != contextLength272K {
			t.Fatalf("%q: got %d want %d", id, got, contextLength272K)
		}
	}
	// 上游明确报出更大的窗口时，取更大的那个。
	if got := resolveModelContextLength("gpt-4o", 400000); got != 400000 {
		t.Fatalf("reported larger window was lost: %d", got)
	}
}
