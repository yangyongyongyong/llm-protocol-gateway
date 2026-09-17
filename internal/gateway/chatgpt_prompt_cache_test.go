package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeriveChatGPTPromptCacheKey(t *testing.T) {
	// 1. session header wins
	r := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	r.Header.Set("X-Claude-Code-Session-Id", "abc123")
	if got := deriveChatGPTPromptCacheKey(r, nil); got != "session-abc123" {
		t.Fatalf("header: %q", got)
	}

	// 2. metadata.user_id session suffix
	req := map[string]any{
		"metadata": map[string]any{"user_id": "user_abc_account_def_session_9f8e7d6c"},
	}
	if got := deriveChatGPTPromptCacheKey(httptest.NewRequest(http.MethodPost, "/", nil), req); got != "session-9f8e7d6c" {
		t.Fatalf("metadata: %q", got)
	}

	// 3. cache_control anchors
	anchored := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are a coding agent."},
			map[string]any{"type": "text", "text": "tools description", "cache_control": map[string]any{"type": "ephemeral"}},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "hello"},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "first anchored turn", "cache_control": map[string]any{"type": "ephemeral"}},
			}},
		},
	}
	if got := deriveChatGPTPromptCacheKey(httptest.NewRequest(http.MethodPost, "/", nil), anchored); !strings.HasPrefix(got, "anthropic-cache-") {
		t.Fatalf("anchors: %q", got)
	}

	// no signals at all → empty
	if got := deriveChatGPTPromptCacheKey(httptest.NewRequest(http.MethodPost, "/", nil), map[string]any{}); got != "" {
		t.Fatalf("empty: %q", got)
	}
}

func TestChatGPTDigestSessionStoreReuse(t *testing.T) {
	store := newChatGPTDigestSessionStore()
	chain1 := []string{"a", "b", "c"}
	key1 := store.bindOrReuse(chain1)
	if key1 == "" {
		t.Fatal("key must not be empty")
	}
	// Next turn extends the same history → same key.
	chain2 := []string{"a", "b", "c", "d", "e"}
	if key2 := store.bindOrReuse(chain2); key2 != key1 {
		t.Fatalf("extended chain must reuse key: %q vs %q", key2, key1)
	}
	// A different conversation gets a different key.
	other := []string{"x", "y", "z"}
	if key3 := store.bindOrReuse(other); key3 == key1 {
		t.Fatal("different chain must not share key")
	}
}

func TestExtractPromptCacheKeyFromBody(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"prompt_cache_key": "session-1", "model": "gpt"})
	if got := extractPromptCacheKeyFromBody(body); got != "session-1" {
		t.Fatalf("got %q", got)
	}
	if got := extractPromptCacheKeyFromBody([]byte(`{"model":"gpt"}`)); got != "" {
		t.Fatalf("absent key must be empty, got %q", got)
	}
	if got := extractPromptCacheKeyFromBody(nil); got != "" {
		t.Fatalf("nil body must be empty, got %q", got)
	}
}

func TestClaudeSystemToInstructionsDropsBillingHeader(t *testing.T) {
	system := []any{
		map[string]any{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.220; nonce=12345"},
		map[string]any{"type": "text", "text": "You are OpenCode."},
	}
	got := claudeSystemToInstructions(system)
	if strings.Contains(got, "x-anthropic-billing-header") {
		t.Fatalf("billing header leaked: %q", got)
	}
	if got != "You are OpenCode." {
		t.Fatalf("unexpected instructions: %q", got)
	}
}

func TestClaudeMessagesToResponsesInputShapes(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "hi"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "plan", "signature": "sig"},
			map[string]any{"type": "text", "text": "done"},
		}},
	}
	input := claudeMessagesToResponsesInput(messages)
	if len(input) != 3 {
		t.Fatalf("input len = %d (user message + reasoning item + assistant message)", len(input))
	}
	first := input[0].(map[string]any)
	if first["type"] != "message" || first["role"] != "user" {
		t.Fatalf("string message must become typed message item: %#v", first)
	}
	// The reasoning item must not carry a replayed rs_* id.
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if strings.HasPrefix(stringValue(item["id"]), "rs_") {
			t.Fatalf("reasoning id leaked: %#v", item)
		}
	}
}
