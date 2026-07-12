package gateway

import "testing"

func TestResolveModelContextLengthClaude(t *testing.T) {
	cases := []struct {
		id       string
		reported int
		want     int
	}{
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
		{"gpt-5.5", 0, contextLengthDefault},
		{"gpt-5.5", 256000, 256000},
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
