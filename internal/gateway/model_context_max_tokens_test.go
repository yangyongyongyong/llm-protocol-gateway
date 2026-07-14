package gateway

import (
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestNormalizeMaxOutputTokens(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   int
		want int
	}{
		{0, 0},
		{-1, 0},
		{4096, 4096},
		{200_000, 200_000},
		{200_001, 200_000},
	}
	for _, tc := range cases {
		if got := normalizeMaxOutputTokens(tc.in); got != tc.want {
			t.Fatalf("normalizeMaxOutputTokens(%d)=%d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestEffectiveClaudeMaxTokensOverride(t *testing.T) {
	t.Parallel()
	auto := effectiveClaudeMaxTokens("claude-opus-4-6", 0)
	if auto <= 0 {
		t.Fatalf("auto budget should be >0, got %d", auto)
	}
	if got := effectiveClaudeMaxTokens("claude-opus-4-6", 12_345); got != 12_345 {
		t.Fatalf("override ignored: got %d", got)
	}
	if got := effectiveClaudeMaxTokens("unknown-claude-model-xyz", 0); got <= 0 {
		t.Fatalf("unknown model should still get conservative default, got %d", got)
	}
}

func TestFillModelTokenBudgets(t *testing.T) {
	t.Parallel()
	model := domain.Model{ID: "claude-sonnet-4-5"}
	fillModelTokenBudgets(&model)
	if model.ContextLength <= 0 {
		t.Fatalf("contextLength unset: %+v", model)
	}
	if model.MaxOutputTokens <= 0 {
		t.Fatalf("maxOutputTokens unset: %+v", model)
	}
}
