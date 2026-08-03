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

// TestQoderTierMaxOutputTokens locks in the empirically-probed per-tier split
// (2026-08-03, real requests against the connected upstream): ultimate
// accepted max_tokens=200_000 with no rejection, while performance/auto/lite
// hard-rejected anything above 65536 ("Range of max_tokens should be
// [1, 65536]"); efficient wasn't conclusively above 65536 either, so it stays
// on the generic catch-all along with the other three.
func TestQoderTierMaxOutputTokens(t *testing.T) {
	t.Parallel()
	if got := resolveModelMaxOutputTokens("ultimate", contextLengthDefault); got != 200_000 {
		t.Fatalf("ultimate tier: got %d, want 200000", got)
	}
	for _, tier := range []string{"performance", "auto", "efficient", "lite"} {
		if got := resolveModelMaxOutputTokens(tier, contextLengthDefault); got != 65_536 {
			t.Fatalf("%s tier: got %d, want 65536", tier, got)
		}
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
