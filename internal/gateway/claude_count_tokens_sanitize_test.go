package gateway

import (
	"encoding/json"
	"testing"
)

func TestSanitizeClaudeCountTokensBodyStripsMaxTokens(t *testing.T) {
	body := []byte(`{
		"model":"claude-sonnet-5",
		"max_tokens":1024,
		"stream":true,
		"temperature":0.2,
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"name":"bash","input_schema":{"type":"object"}}]
	}`)
	out := sanitizeClaudeCountTokensBody(body)
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"max_tokens", "stream", "temperature"} {
		if _, exists := payload[key]; exists {
			t.Fatalf("expected %s stripped for count_tokens, got %#v", key, payload[key])
		}
	}
	if payload["model"] != "claude-sonnet-5" {
		t.Fatalf("model=%#v", payload["model"])
	}
	if _, ok := payload["messages"]; !ok {
		t.Fatal("messages must be preserved")
	}
	if _, ok := payload["tools"]; !ok {
		t.Fatal("tools must be preserved")
	}
}

func TestNormalizeThenSanitizeCountTokensDoesNotKeepInjectedBudget(t *testing.T) {
	// Reproduces the 2026-07-20 log: client body has no max_tokens, but
	// normalizeClaudePassThroughPayload injects a default before OAuth send.
	payload := map[string]any{
		"model": "claude-sonnet-5",
		"messages": []any{
			map[string]any{"role": "user", "content": "hi"},
		},
		"tools": []any{
			map[string]any{"name": "bash", "input_schema": map[string]any{"type": "object"}},
		},
	}
	normalizeClaudePassThroughPayload(payload)
	if _, ok := payload["max_tokens"]; !ok {
		t.Fatal("normalize should inject max_tokens for Messages path")
	}
	sanitizeClaudeCountTokensPayload(payload)
	if _, ok := payload["max_tokens"]; ok {
		t.Fatalf("count_tokens sanitize must strip injected max_tokens, got %#v", payload["max_tokens"])
	}
}
