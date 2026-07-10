package monitor

import "testing"

func TestNormalizeUsageTokensOpenAIInclusive(t *testing.T) {
	total, cache := normalizeUsageTokens(120, 96)
	if total != 120 || cache != 96 {
		t.Fatalf("expected (120, 96), got (%d, %d)", total, cache)
	}
}

func TestNormalizeUsageTokensClaudeExclusive(t *testing.T) {
	total, cache := normalizeUsageTokens(17_900_000, 188_800_000)
	if total != 17_900_000+188_800_000 {
		t.Fatalf("expected inclusive prompt total, got %d", total)
	}
	if cache != 188_800_000 {
		t.Fatalf("unexpected cache: %d", cache)
	}
}

func TestNormalizeUsageTokensNoCache(t *testing.T) {
	total, cache := normalizeUsageTokens(500, 0)
	if total != 500 || cache != 0 {
		t.Fatalf("expected (500, 0), got (%d, %d)", total, cache)
	}
}

func TestCacheHitRateOpenAIStyle(t *testing.T) {
	rate := CacheHitRate(120, 96)
	want := 96.0 / 120.0 * 100
	if rate < want-0.01 || rate > want+0.01 {
		t.Fatalf("expected ~%.2f%%, got %.2f%%", want, rate)
	}
}

func TestCacheHitRateClaudeStyle(t *testing.T) {
	rate := CacheHitRate(17_900_000, 188_800_000)
	want := 188_800_000.0 / (17_900_000.0 + 188_800_000.0) * 100
	if rate < want-0.01 || rate > want+0.01 {
		t.Fatalf("expected ~%.2f%%, got %.2f%%", want, rate)
	}
}

func TestCacheHitRateCappedAt100(t *testing.T) {
	if got := CacheHitRate(100, 100); got != 100 {
		t.Fatalf("expected 100%%, got %.2f%%", got)
	}
	if got := CacheHitRate(0, 50); got != 100 {
		t.Fatalf("expected 100%% for cache-only prompt, got %.2f%%", got)
	}
}

func TestCacheHitRateAggregated(t *testing.T) {
	// Mixed providers: sum(cache) / sum(promptTotal) after per-event normalization.
	openAITotal, openAICache := normalizeUsageTokens(120, 96)
	claudeTotal, claudeCache := normalizeUsageTokens(678, 9218)
	aggTotal := openAITotal + claudeTotal
	aggCache := openAICache + claudeCache
	rate := CacheHitRate(aggTotal, aggCache)
	want := float64(aggCache) / float64(aggTotal) * 100
	if rate < want-0.01 || rate > want+0.01 {
		t.Fatalf("expected aggregated ~%.2f%%, got %.2f%%", want, rate)
	}
}
