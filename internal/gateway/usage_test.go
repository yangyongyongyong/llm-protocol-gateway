package gateway

import "testing"

func TestParseClaudeUsageFromSSE(t *testing.T) {
	body := []byte(`event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":22707,"output_tokens":42,"cache_read_input_tokens":18432}}

`)
	usage := ParseClaudeUsage(body)
	// Claude input_tokens is exclusive of cache; gateway normalizes to inclusive total.
	if usage.InputTokens != 22707+18432 {
		t.Fatalf("unexpected input tokens: %d", usage.InputTokens)
	}
	if usage.OutputTokens != 42 {
		t.Fatalf("unexpected output tokens: %d", usage.OutputTokens)
	}
	if usage.CacheTokens != 18432 {
		t.Fatalf("unexpected cache tokens: %d", usage.CacheTokens)
	}
}

func TestNormalizeClaudeUsageFields(t *testing.T) {
	usage := normalizeClaudeUsageFields(678, 1133, 9218, 0)
	if usage.InputTokens != 678+9218 {
		t.Fatalf("expected inclusive input, got %d", usage.InputTokens)
	}
	if usage.CacheTokens != 9218 {
		t.Fatalf("unexpected cache: %d", usage.CacheTokens)
	}
	if got := claudeExclusiveInputTokens(usage, 0); got != 678 {
		t.Fatalf("expected exclusive input 678, got %d", got)
	}
}

func TestParseOpenAIUsageFromSSE(t *testing.T) {
	body := []byte(`data: {"choices":[{"delta":{"content":"hi"}}]}

data: {"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":8,"prompt_cache_hit_tokens":96}}

data: [DONE]
`)
	usage := ParseOpenAIUsage(body)
	if usage.InputTokens != 120 {
		t.Fatalf("unexpected input tokens: %d", usage.InputTokens)
	}
	if usage.OutputTokens != 8 {
		t.Fatalf("unexpected output tokens: %d", usage.OutputTokens)
	}
	if usage.CacheTokens != 96 {
		t.Fatalf("unexpected cache tokens: %d", usage.CacheTokens)
	}
}

func TestParseOpenAIUsageFromJSONWithCachedTokensDetails(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens":200,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":150}}}`)
	usage := ParseOpenAIUsage(body)
	if usage.CacheTokens != 150 {
		t.Fatalf("unexpected cache tokens: %d", usage.CacheTokens)
	}
}
