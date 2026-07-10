package gateway

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
)

// Protocol-path matrix: Cursor quirks must not leak into Claude→Responses.
func TestChatToResponsesOptionsByAuthType(t *testing.T) {
	cursor := CursorChatToResponsesStreamOptions()
	standard := StandardChatToResponsesStreamOptions()
	if !cursor.AllowReasoningFallback {
		t.Fatal("cursor options must allow reasoning fallback")
	}
	if standard.AllowReasoningFallback {
		t.Fatal("standard/claude options must not allow reasoning fallback")
	}
	dual := "call_abc\nfc_xyz"
	if got := cursor.SanitizeCallID(dual); got != "call_abc" {
		t.Fatalf("cursor sanitize: got %q", got)
	}
	if got := standard.SanitizeCallID(dual); got != strings.TrimSpace(dual) {
		t.Fatalf("standard sanitize should only trim, got %q want %q", got, strings.TrimSpace(dual))
	}
}

func TestClaudeToResponsesTwoHopStreamLifecycle(t *testing.T) {
	// Mirrors proxyClaudeToResponses streaming: Claude SSE → Chat SSE → Responses SSE
	// with StandardChatToResponsesStreamOptions (no Cursor quirks).
	claudeSSE := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-8","content":[]}}`,
		``,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}`,
		``,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":2,"output_tokens":1}}`,
		``,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	chatBridge := &bufferResponseWriter{}
	if _, err := streamClaudeToOpenAIChatEvents(chatBridge, strings.NewReader(claudeSSE), "claude-opus-4-8", nil); err != nil {
		t.Fatalf("claude→chat: %v", err)
	}
	chatBody := chatBridge.buf.String()
	if !strings.Contains(chatBody, `"content":"你好"`) {
		t.Fatalf("expected chat content 你好, got:\n%s", chatBody)
	}

	rec := httptest.NewRecorder()
	_, err := streamOpenAIChatToResponsesEventsWithOptions(
		rec, bytes.NewReader(chatBridge.buf.Bytes()), "claude-opus-4-8", StandardChatToResponsesStreamOptions(),
	)
	if err != nil {
		t.Fatalf("chat→responses: %v body=%s", err, rec.Body.String())
	}
	body := rec.Body.String()
	for _, event := range []string{
		"event: response.created",
		"event: response.in_progress",
		"event: response.output_item.added",
		"event: response.content_part.added",
		"event: response.output_text.delta",
		"event: response.completed",
	} {
		if !strings.Contains(body, event) {
			t.Fatalf("claude→responses missing %q:\n%s", event, body)
		}
	}
	if !strings.Contains(body, `"output_text":"你好"`) {
		t.Fatalf("expected output_text 你好, got:\n%s", body)
	}
	if !strings.Contains(body, `"content":[]`) {
		t.Fatalf("expected content:[] on item.added for Responses clients, got:\n%s", body)
	}
}

func TestCursorToResponsesReasoningFallbackIsolatedFromClaudePath(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","reasoning_content":"only-reason"}}],"model":"gpt-5.3-codex"}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"model":"gpt-5.3-codex"}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"

	cursorRec := httptest.NewRecorder()
	if _, err := streamOpenAIChatToResponsesEventsWithOptions(
		cursorRec, strings.NewReader(upstream), "gpt-5.3-codex", CursorChatToResponsesStreamOptions(),
	); err != nil {
		t.Fatalf("cursor path: %v", err)
	}
	if !strings.Contains(cursorRec.Body.String(), `"output_text":"only-reason"`) {
		t.Fatalf("cursor path should promote reasoning, got:\n%s", cursorRec.Body.String())
	}

	claudeRec := httptest.NewRecorder()
	_, err := streamOpenAIChatToResponsesEventsWithOptions(
		claudeRec, strings.NewReader(upstream), "claude-opus-4-8", StandardChatToResponsesStreamOptions(),
	)
	if err == nil {
		t.Fatalf("claude/standard path must not promote reasoning-only, body=%s", claudeRec.Body.String())
	}
}
