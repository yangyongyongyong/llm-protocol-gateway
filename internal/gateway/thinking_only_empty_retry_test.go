package gateway

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsThinkingOnlyEmptyStreamError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errThinkingOnlyEmptyResponse, true},
		{fmt.Errorf("wrapped: %w", errThinkingOnlyEmptyResponse), true},
		{fmt.Errorf("openai stream ended without any chunks"), false},
		{fmt.Errorf("connection reset"), false},
	}
	for _, tc := range cases {
		if got := isThinkingOnlyEmptyStreamError(tc.err); got != tc.want {
			t.Fatalf("err=%v got=%v want=%v", tc.err, got, tc.want)
		}
	}
}

func TestIsThinkingOnlyEmptyOutput(t *testing.T) {
	cases := []struct {
		name   string
		status string
		output []map[string]any
		want   bool
	}{
		{"completed nil output", "completed", nil, true},
		{"completed only reasoning", "completed", []map[string]any{{"type": "reasoning"}}, true},
		{"completed with message", "completed", []map[string]any{{"type": "reasoning"}, {"type": "message"}}, false},
		{"completed with function_call", "completed", []map[string]any{{"type": "function_call"}}, false},
		{"incomplete (max_tokens) only reasoning", "incomplete", []map[string]any{{"type": "reasoning"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isThinkingOnlyEmptyOutput(tc.status, tc.output); got != tc.want {
				t.Fatalf("got=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestClaudeToResponsesResponseDirectThinkingOnlyEmptyIsRetryable(t *testing.T) {
	claudeResp := map[string]any{
		"id":    "msg_thinking_only",
		"model": "claude-sonnet-5",
		"content": []any{
			map[string]any{"type": "thinking", "thinking": "", "signature": "sig-empty"},
		},
		"stop_reason": "end_turn",
		"usage":       map[string]any{"input_tokens": 100, "output_tokens": 2},
	}
	body, _ := json.Marshal(claudeResp)
	out, usage, err := claudeToResponsesResponseDirect(body, "claude-sonnet-5", nil)
	if out != nil {
		t.Fatalf("expected nil body on retryable error, got %s", out)
	}
	if !isThinkingOnlyEmptyStreamError(err) {
		t.Fatalf("expected thinking-only-empty error, got %v", err)
	}
	if usage.OutputTokens != 2 {
		t.Fatalf("usage not populated on error path: %+v", usage)
	}
}

func TestClaudeToResponsesResponseDirectWithTextIsNotFlagged(t *testing.T) {
	claudeResp := map[string]any{
		"id":    "msg_ok",
		"model": "claude-sonnet-5",
		"content": []any{
			map[string]any{"type": "thinking", "thinking": "draft", "signature": "sig-1"},
			map[string]any{"type": "text", "text": "hello"},
		},
		"stop_reason": "end_turn",
		"usage":       map[string]any{"input_tokens": 100, "output_tokens": 10},
	}
	body, _ := json.Marshal(claudeResp)
	out, _, err := claudeToResponsesResponseDirect(body, "claude-sonnet-5", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected non-empty response body")
	}
}

func TestClaudeToResponsesResponseDirectMaxTokensThinkingOnlyIsNotFlagged(t *testing.T) {
	// Truncated by max_tokens while still thinking: a real, different problem
	// (context/budget), not the silent-completed-empty bug. Must not be
	// treated as retryable here (a bigger budget is needed, not a retry).
	claudeResp := map[string]any{
		"id":    "msg_truncated",
		"model": "claude-sonnet-5",
		"content": []any{
			map[string]any{"type": "thinking", "thinking": "still going", "signature": "sig-2"},
		},
		"stop_reason": "max_tokens",
		"usage":       map[string]any{"input_tokens": 100, "output_tokens": 500},
	}
	body, _ := json.Marshal(claudeResp)
	_, _, err := claudeToResponsesResponseDirect(body, "claude-sonnet-5", nil)
	if err != nil {
		t.Fatalf("max_tokens truncation must not be flagged as thinking-only-empty: %v", err)
	}
}

func TestStreamClaudeToResponsesEventsDirectThinkingOnlyEmptyIsRetryable(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-5"}}`,
		``,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		``,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-abc"}}`,
		``,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		``,
		`data: {"type":"message_stop"}`,
		``,
		``,
	}, "\n")

	rec := httptest.NewRecorder()
	usage, err := streamClaudeToResponsesEventsDirect(rec, strings.NewReader(sse), "claude-sonnet-5", nil)
	if !isThinkingOnlyEmptyStreamError(err) {
		t.Fatalf("expected thinking-only-empty error, got %v", err)
	}
	if usage.OutputTokens != 3 {
		t.Fatalf("usage not populated: %+v", usage)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("client must not receive any bytes on retryable thinking-only stream, got %q", rec.Body.String())
	}
}

func TestStreamClaudeToResponsesEventsDirectWithTextIsNotRetryable(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-5"}}`,
		``,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		``,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-abc"}}`,
		``,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		``,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"hello"}}`,
		``,
		`data: {"type":"content_block_stop","index":1}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":8}}`,
		``,
		`data: {"type":"message_stop"}`,
		``,
		``,
	}, "\n")

	rec := httptest.NewRecorder()
	usage, err := streamClaudeToResponsesEventsDirect(rec, strings.NewReader(sse), "claude-sonnet-5", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if usage.OutputTokens != 8 {
		t.Fatalf("usage=%+v", usage)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "response.created") {
		t.Fatalf("expected buffered response.created to flush once visible, body=%q", body)
	}
	if !strings.Contains(body, "response.completed") {
		t.Fatalf("expected response.completed, body=%q", body)
	}
	if !strings.Contains(body, "hello") {
		t.Fatalf("expected text content to flow through, body=%q", body)
	}
}
