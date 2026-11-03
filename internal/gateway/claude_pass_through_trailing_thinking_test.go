package gateway

import "testing"

// normalizeClaudePassThroughMessagesForTest runs a history through the native
// pass-through normaliser and returns the messages it would send upstream.
func normalizeClaudePassThroughMessagesForTest(t *testing.T, messages []any) []any {
	t.Helper()
	payload := map[string]any{
		"model":    "claude-opus-5-5",
		"messages": messages,
	}
	normalizeClaudePassThroughPayload(payload)
	normalized, ok := payload["messages"].([]any)
	if !ok {
		t.Fatalf("messages = %#v, want a slice", payload["messages"])
	}
	return normalized
}

func claudeTestBlockTypes(t *testing.T, message any) []string {
	t.Helper()
	entry, ok := message.(map[string]any)
	if !ok {
		t.Fatalf("message = %#v, want an object", message)
	}
	blocks, ok := entry["content"].([]any)
	if !ok {
		t.Fatalf("content = %#v, want blocks", entry["content"])
	}
	types := make([]string, 0, len(blocks))
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("block = %#v, want an object", raw)
		}
		types = append(types, stringValue(block["type"]))
	}
	return types
}

// The turn that made the client go silent: the model answered the previous
// request with nothing but its thinking, the client stored that as an assistant
// message, and the next request replays it. Anthropic refuses a history that
// ends an assistant message on `thinking`, and an upstream that accepts it
// answers the same way again — so it must not be sent at all.
func TestNormalizeClaudePassThroughDropsTrailingThinkingTurn(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hi"}}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "", "signature": "sig-1"},
		}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "继续"}}},
	}

	got := normalizeClaudePassThroughMessagesForTest(t, messages)
	if len(got) != 1 {
		t.Fatalf("messages = %#v, want the empty assistant turn dropped and the two user turns merged", got)
	}
	if role := stringValue(got[0].(map[string]any)["role"]); role != "user" {
		t.Fatalf("role = %q, want user", role)
	}
	if types := claudeTestBlockTypes(t, got[0]); len(types) != 2 || types[0] != "text" || types[1] != "text" {
		t.Fatalf("blocks = %v, want the two text blocks joined into one user turn", types)
	}
}

// A thinking block that a tool_use follows is the shape Anthropic requires of a
// tool loop, and the model re-reads it: it has to survive untouched.
func TestNormalizeClaudePassThroughKeepsThinkingBeforeToolUse(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "read it"}}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "", "signature": "sig-1"},
			map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Read", "input": map[string]any{"path": "a"}},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "done"},
		}},
	}

	got := normalizeClaudePassThroughMessagesForTest(t, messages)
	if len(got) != 3 {
		t.Fatalf("messages = %#v, want all three turns", got)
	}
	types := claudeTestBlockTypes(t, got[1])
	if len(types) != 2 || types[0] != "thinking" || types[1] != "tool_use" {
		t.Fatalf("assistant blocks = %v, want thinking then tool_use", types)
	}
}

// A thinking block a text block follows is equally part of the answer.
func TestNormalizeClaudePassThroughKeepsThinkingBeforeText(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hi"}}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "", "signature": "sig-1"},
			map[string]any{"type": "text", "text": "PONG"},
		}},
	}

	got := normalizeClaudePassThroughMessagesForTest(t, messages)
	if len(got) != 2 {
		t.Fatalf("messages = %#v, want both turns", got)
	}
	types := claudeTestBlockTypes(t, got[1])
	if len(types) != 2 || types[0] != "thinking" || types[1] != "text" {
		t.Fatalf("assistant blocks = %v, want thinking then text", types)
	}
}

// redacted_thinking carries ciphertext a later turn replays: dropping the turn
// would lose it, so one empty thinking placeholder keeps its place instead.
func TestNormalizeClaudePassThroughKeepsRedactedThinkingPlaceholder(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hi"}}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "redacted_thinking", "data": "EmwKAhgBEgyOpaqueCiphertext=="},
		}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "继续"}}},
	}

	got := normalizeClaudePassThroughMessagesForTest(t, messages)
	if len(got) != 3 {
		t.Fatalf("messages = %#v, want the assistant turn kept as a placeholder", got)
	}
	types := claudeTestBlockTypes(t, got[1])
	if len(types) != 1 || types[0] != "thinking" {
		t.Fatalf("assistant blocks = %v, want one empty thinking placeholder", types)
	}
}

// A history with nothing to trim must go out exactly as it came back.
func TestNormalizeClaudePassThroughLeavesCleanHistoryAlone(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hi"}}},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "on it"}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "继续"}}},
	}

	got := normalizeClaudePassThroughMessagesForTest(t, messages)
	if len(got) != 3 {
		t.Fatalf("messages = %#v, want all three turns untouched", got)
	}
	for index, want := range []string{"user", "assistant", "user"} {
		if role := stringValue(got[index].(map[string]any)["role"]); role != want {
			t.Fatalf("message %d role = %q, want %q", index, role, want)
		}
	}
}

// A request that is nothing but empty assistant turns stays as the client sent
// it: Anthropic refuses a request with no messages at all, which is a worse
// answer than the one the client already has.
func TestNormalizeClaudePassThroughKeepsThinkingOnlyRequest(t *testing.T) {
	messages := []any{
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "", "signature": "sig-1"},
		}},
	}

	got := normalizeClaudePassThroughMessagesForTest(t, messages)
	if len(got) != 1 {
		t.Fatalf("messages = %#v, want the only turn left in place", got)
	}
	if types := claudeTestBlockTypes(t, got[0]); len(types) != 1 || types[0] != "thinking" {
		t.Fatalf("blocks = %v, want the thinking block kept", types)
	}
}
