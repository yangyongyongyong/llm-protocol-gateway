package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// Models narrate before calling a tool ("Let me read the file first"). When
// Chat → Responses dropped that preamble, the client's next turn replayed an
// empty assistant message in place of reasoning it actually produced.

func TestChatToResponsesKeepsPreambleBeforeToolCalls(t *testing.T) {
	body := []byte(`{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,` +
		`"message":{"role":"assistant","content":"Let me read the file first.",` +
		`"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]},` +
		`"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	out, _, err := openAIChatToResponsesResponse(body, "m")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	items, _ := payload["output"].([]any)
	if len(items) != 2 {
		t.Fatalf("output has %d items, want message + function_call: %s", len(items), out)
	}
	first, _ := items[0].(map[string]any)
	if got := stringValue(first["type"]); got != "message" {
		t.Fatalf("first item type = %q, want message", got)
	}
	content, _ := first["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("message content = %#v", first["content"])
	}
	part, _ := content[0].(map[string]any)
	if got := stringValue(part["text"]); got != "Let me read the file first." {
		t.Fatalf("preamble text = %q", got)
	}
	second, _ := items[1].(map[string]any)
	if got := stringValue(second["type"]); got != "function_call" {
		t.Fatalf("second item type = %q, want function_call", got)
	}
	if got := stringValue(second["call_id"]); got != "call_1" {
		t.Fatalf("call_id = %q, want call_1", got)
	}
}

func TestChatToResponsesToolOnlyStillHasOneItem(t *testing.T) {
	body := []byte(`{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,` +
		`"message":{"role":"assistant","content":null,` +
		`"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},` +
		`"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	out, _, err := openAIChatToResponsesResponse(body, "m")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	items, _ := payload["output"].([]any)
	if len(items) != 1 {
		t.Fatalf("output has %d items, want only the function_call: %s", len(items), out)
	}
	if got := stringValue(items[0].(map[string]any)["type"]); got != "function_call" {
		t.Fatalf("item type = %q, want function_call", got)
	}
}

// The streamed path must emit the same shape: a message item carrying the
// preamble, closed before the function_call item opens.
func TestChatStreamToResponsesKeepsPreambleBeforeToolCall(t *testing.T) {
	input := strings.Join([]string{
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"Let me read "},"finish_reason":null}]}`,
		``,
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"the file."},"finish_reason":null}]}`,
		``,
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]},"finish_reason":null}]}`,
		``,
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	recorder := httptest.NewRecorder()
	if _, err := streamOpenAIChatToResponsesEvents(recorder, strings.NewReader(input), "m"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	response := lastResponsesCompletedResponse(t, recorder.Body.String())
	items, _ := response["output"].([]any)
	if len(items) != 2 {
		t.Fatalf("output has %d items, want message + function_call: %v", len(items), response["output"])
	}
	message, _ := items[0].(map[string]any)
	if got := stringValue(message["type"]); got != "message" {
		t.Fatalf("first item type = %q, want message", got)
	}
	content, _ := message["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("message content = %#v", message["content"])
	}
	if got := stringValue(content[0].(map[string]any)["text"]); got != "Let me read the file." {
		t.Fatalf("preamble text = %q", got)
	}
	tool, _ := items[1].(map[string]any)
	if got := stringValue(tool["type"]); got != "function_call" {
		t.Fatalf("second item type = %q, want function_call", got)
	}
	if got := stringValue(message["phase"]); got != "commentary" {
		t.Fatalf("preamble phase = %q, want commentary", got)
	}
	// 逐帧的 output_item.done 也必须带 phase：严格客户端只读这一帧。
	var donePhase string
	for _, block := range strings.Split(recorder.Body.String(), "\n\n") {
		if !strings.Contains(block, "response.output_item.done") {
			continue
		}
		var event map[string]any
		payload := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(block[strings.Index(block, "data:"):]), "data:"))
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		item, _ := event["item"].(map[string]any)
		if stringValue(item["type"]) != "message" {
			continue
		}
		donePhase = stringValue(item["phase"])
	}
	if donePhase != "commentary" {
		t.Fatalf("output_item.done phase = %q, want commentary", donePhase)
	}
	if got := stringValue(response["status"]); got != "completed" {
		t.Fatalf("status = %q, want completed", got)
	}
}
