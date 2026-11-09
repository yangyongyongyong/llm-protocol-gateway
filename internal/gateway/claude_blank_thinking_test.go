package gateway

import (
	"testing"
)

// Anthropic words the "empty thinking" refusal two ways, and only the wrapper
// word differs. Matching just one of them cost a live session 15 consecutive
// 400s: the rectifier never engaged, so the client saw the raw error every time.
func TestShouldRectifyThinkingSignatureBothEmptyThinkingWordings(t *testing.T) {
	cases := []string{
		`{"type":"error","error":{"type":"invalid_request_error","message":"messages.44.content.0.thinking: each thinking block must contain non-whitespace thinking"}}`,
		`{"type":"error","error":{"type":"invalid_request_error","message":"messages.2.content.0.thinking: each thinking block must contain thinking"}}`,
	}
	for _, body := range cases {
		if !shouldRectifyThinkingSignature([]byte(body)) {
			t.Errorf("rectifier ignored: %s", body)
		}
	}
}

// And it must not start claiming unrelated 400s: the rectifier strips every
// historical thinking block, which is only worth doing for these errors.
func TestShouldRectifyThinkingSignatureIgnoresOtherErrors(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"max_tokens: must be greater than 0"}}`,
		`{"error":{"message":"prompt is too long: 300000 tokens > 200000 maximum"}}`,
		`{"error":{"message":"temperature: Extra inputs are not permitted"}}`,
		``,
	} {
		if shouldRectifyThinkingSignature([]byte(body)) {
			t.Errorf("rectifier would rewrite an unrelated error: %s", body)
		}
	}
}

// The same refusal is now prevented rather than repaired: a thinking block whose
// text is blank cannot be valid, so it is dropped before the request goes out.
// Claude Code replays exactly this shape (signature "" with thinking " ").
func TestNormalizeDropsBlankThinkingBlock(t *testing.T) {
	payload := map[string]any{
		"model": "claude-opus-5-5",
		"messages": []any{
			map[string]any{"role": "user", "content": "go on"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "thinking", "thinking": " ", "signature": ""},
				map[string]any{"type": "text", "text": "here is the answer"},
			}},
			map[string]any{"role": "user", "content": "next"},
		},
	}
	normalizeClaudePassThroughPayload(payload)

	messages, _ := payload["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %d, want the assistant turn kept (it has text)", len(messages))
	}
	assistant, _ := messages[1].(map[string]any)
	// With the thinking block gone the turn holds one text block, and the
	// normaliser collapses that to a plain string.
	if got := stringValue(assistant["content"]); got != "here is the answer" {
		t.Fatalf("assistant content = %#v, want only the text left", assistant["content"])
	}
}

// A turn that was nothing but that blank block goes away entirely: replaying it
// is what the upstream refuses, and it carries nothing else worth sending.
func TestNormalizeDropsThinkingOnlyTurnWithBlankText(t *testing.T) {
	payload := map[string]any{
		"model": "claude-opus-5-5",
		"messages": []any{
			map[string]any{"role": "user", "content": "hi"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "thinking", "thinking": "", "signature": "sig"},
			}},
			map[string]any{"role": "user", "content": "继续"},
		},
	}
	normalizeClaudePassThroughPayload(payload)

	messages, _ := payload["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %#v, want the blank-thinking turn dropped and the user turns merged", messages)
	}
	if got := stringValue(messages[0].(map[string]any)["role"]); got != "user" {
		t.Fatalf("role = %q, want user", got)
	}
}

// A thinking block with real text keeps its signature — but only where it is not
// the trailing block: Anthropic refuses an assistant message whose last block is
// thinking, so a trailing one is still trimmed (that is the separate rule
// dropTrailingClaudePassThroughThinking enforces). Followed by text, it survives.
func TestNormalizeKeepsThinkingWithTextAndSignature(t *testing.T) {
	payload := map[string]any{
		"model": "claude-opus-5-5",
		"messages": []any{
			map[string]any{"role": "user", "content": "go"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "thinking", "thinking": "let me look", "signature": "sig-1"},
				map[string]any{"type": "text", "text": "done"},
			}},
			map[string]any{"role": "user", "content": "next"},
		},
	}
	normalizeClaudePassThroughPayload(payload)

	messages, _ := payload["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %d, want all three turns", len(messages))
	}
	blocks := messages[1].(map[string]any)["content"].([]any)
	block := blocks[0].(map[string]any)
	if stringValue(block["type"]) != "thinking" || stringValue(block["thinking"]) != "let me look" || stringValue(block["signature"]) != "sig-1" {
		t.Fatalf("block = %#v, want the thinking block untouched", block)
	}
}
