package gateway

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamOpenAIChatToResponsesEventsEmitsFullLifecycle(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-5.3-codex","choices":[{"index":0,"delta":{"role":"assistant","content":"你"}}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-5.3-codex","choices":[{"index":0,"delta":{"content":"好"}}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-5.3-codex","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"

	rec := httptest.NewRecorder()
	usage, err := streamOpenAIChatToResponsesEvents(rec, strings.NewReader(upstream), "gpt-5.3-codex")
	if err != nil {
		t.Fatalf("streamOpenAIChatToResponsesEvents: %v", err)
	}
	if usage.InputTokens != 3 || usage.OutputTokens != 1 {
		t.Fatalf("unexpected usage: %+v", usage)
	}

	body := rec.Body.String()
	for _, event := range []string{
		"event: response.created",
		"event: response.in_progress",
		"event: response.output_item.added",
		"event: response.content_part.added",
		"event: response.output_text.delta",
		"event: response.output_text.done",
		"event: response.content_part.done",
		"event: response.output_item.done",
		"event: response.completed",
	} {
		if !strings.Contains(body, event) {
			t.Fatalf("expected %q in stream, got:\n%s", event, body)
		}
	}
	if !strings.Contains(body, `"content":[]`) {
		t.Fatalf("expected empty content array on output_item.added, got:\n%s", body)
	}
	if !strings.Contains(body, `"delta":"你"`) || !strings.Contains(body, `"delta":"好"`) {
		t.Fatalf("expected text deltas in stream, got:\n%s", body)
	}
	if !strings.Contains(body, `"output_text":"你好"`) {
		t.Fatalf("expected completed output_text, got:\n%s", body)
	}
	if !strings.Contains(body, `"type":"output_text"`) || !strings.Contains(body, `"text":"你好"`) {
		t.Fatalf("expected completed output blocks, got:\n%s", body)
	}
}

func TestStreamOpenAIChatToResponsesEventsUsesReasoningFallback(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-5.3-codex","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"hello"}}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-5.3-codex","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"

	rec := httptest.NewRecorder()
	_, err := streamOpenAIChatToResponsesEventsWithOptions(rec, strings.NewReader(upstream), "gpt-5.3-codex", CursorChatToResponsesStreamOptions())
	if err != nil {
		t.Fatalf("streamOpenAIChatToResponsesEvents: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"output_text":"hello"`) {
		t.Fatalf("expected reasoning fallback in completed output_text, got:\n%s", body)
	}
}

func TestStreamOpenAIChatToResponsesStandardIgnoresReasoningOnly(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"claude-opus","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"secret"}}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"claude-opus","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"

	rec := httptest.NewRecorder()
	_, err := streamOpenAIChatToResponsesEventsWithOptions(rec, strings.NewReader(upstream), "claude-opus", StandardChatToResponsesStreamOptions())
	if err == nil {
		t.Fatalf("standard path should reject reasoning-only stream, body=%s", rec.Body.String())
	}
}

func TestStreamOpenAIChatToResponsesEventsEmitsFunctionCallLifecycle(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"shell","arguments":"{\"cmd\""}}]}}],"model":"gpt-5.3-codex"}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"pwd\"}"}}]}}],"model":"gpt-5.3-codex"}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"model":"gpt-5.3-codex"}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"

	rec := httptest.NewRecorder()
	_, err := streamOpenAIChatToResponsesEvents(rec, strings.NewReader(upstream), "gpt-5.3-codex")
	if err != nil {
		t.Fatalf("streamOpenAIChatToResponsesEvents: %v", err)
	}
	body := rec.Body.String()
	for _, event := range []string{
		"event: response.output_item.added",
		"event: response.function_call_arguments.delta",
		"event: response.function_call_arguments.done",
		"event: response.output_item.done",
		"event: response.completed",
	} {
		if !strings.Contains(body, event) {
			t.Fatalf("expected %q in stream, got:\n%s", event, body)
		}
	}
	if !strings.Contains(body, `"type":"function_call"`) || !strings.Contains(body, `"name":"shell"`) {
		t.Fatalf("expected function_call output item, got:\n%s", body)
	}
}

func TestStreamOpenAIChatToResponsesEventsSanitizesDualToolCallID(t *testing.T) {
	// Cursor bridge has been observed emitting call_id as "call_xxx\nfc_yyy".
	upstream := strings.Join([]string{
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_abc123\\nfc_deadbeef\",\"type\":\"function\",\"function\":{\"name\":\"view_image\",\"arguments\":\"{\\\"path\\\":\\\"/tmp/x.png\\\"}\"}}]}}],\"model\":\"gpt-5.3-codex\"}",
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"model":"gpt-5.3-codex"}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"

	rec := httptest.NewRecorder()
	_, err := streamOpenAIChatToResponsesEventsWithOptions(rec, strings.NewReader(upstream), "gpt-5.3-codex", CursorChatToResponsesStreamOptions())
	if err != nil {
		t.Fatalf("streamOpenAIChatToResponsesEvents: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"call_id":"call_abc123"`) {
		t.Fatalf("expected sanitized call_id call_abc123, got:\n%s", body)
	}
	if strings.Contains(body, "fc_deadbeef") {
		t.Fatalf("expected fc_ dual-id suffix to be stripped, got:\n%s", body)
	}
	if strings.Contains(body, `\n`) && strings.Contains(body, "call_abc123") && strings.Contains(body, "fc_") {
		t.Fatalf("call_id still contains newline dual-id form:\n%s", body)
	}
}

func TestStreamResponsesToOpenAIChatEventsReadsCompletedOutput(t *testing.T) {
	upstream := "" +
		"event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_1","object":"response","model":"gpt-5.3-codex","status":"in_progress"}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-5.3-codex","status":"completed","output_text":"你好","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"你好"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"

	rec := httptest.NewRecorder()
	usage, err := streamResponsesToOpenAIChatEvents(rec, strings.NewReader(upstream), "gpt-5.3-codex")
	if err != nil {
		t.Fatalf("streamResponsesToOpenAIChatEvents: %v body=%q", err, rec.Body.String())
	}
	if usage.InputTokens != 1 || usage.OutputTokens != 1 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"content":"你好"`) {
		t.Fatalf("expected completed output_text converted to chat delta, got:\n%s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected DONE marker, got:\n%s", body)
	}
}

func TestStreamOpenAIChatToResponsesEventsRejectsEmptyStream(t *testing.T) {
	rec := httptest.NewRecorder()
	_, err := streamOpenAIChatToResponsesEvents(rec, strings.NewReader("data: [DONE]\n\n"), "gpt-5.3-codex")
	if err == nil {
		t.Fatal("expected error for empty stream")
	}
}

func TestStreamOpenAIChatToResponsesEventsHandlesUpstreamError(t *testing.T) {
	upstream := `data: {"error":{"message":"boom","type":"server_error"}}` + "\n\n"
	rec := httptest.NewRecorder()
	_, err := streamOpenAIChatToResponsesEvents(rec, strings.NewReader(upstream), "gpt-5.3-codex")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected upstream error, got %v body=%q", err, rec.Body.String())
	}
}

func TestStreamOpenAIChatToResponsesEventsIntegrationShape(t *testing.T) {
	// Simulates cursor-bridge style SSE with role+content chunks.
	upstream := io.MultiReader(strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","content":"Hi"}}],"model":"gpt-5.3-codex"}`,
		`data: {"choices":[{"delta":{"content":"!"}}],"model":"gpt-5.3-codex"}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"))

	rec := httptest.NewRecorder()
	_, err := streamOpenAIChatToResponsesEvents(rec, upstream, "gpt-5.3-codex")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := rec.Body.String(); !strings.Contains(got, `"output_text":"Hi!"`) {
		t.Fatalf("expected final output_text Hi!, got:\n%s", got)
	}
}
