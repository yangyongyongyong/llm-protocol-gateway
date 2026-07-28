package gateway

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponsesToOpenAIChatCodexNamespaceFlatten(t *testing.T) {
	req := map[string]any{
		"model": "gpt-5",
		"tools": []any{
			map[string]any{
				"type": "namespace",
				"name": "mcp__files",
				"tools": []any{
					map[string]any{"type": "function", "name": "read", "parameters": map[string]any{"type": "object"}},
				},
			},
			map[string]any{"type": "tool_search"},
			map[string]any{"type": "custom", "name": "apply_patch"},
		},
		"tool_choice": map[string]any{"type": "namespace", "name": "mcp__files"},
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "hi"},
			map[string]any{
				"type":      "function_call",
				"call_id":   "call_ns_1",
				"name":      "read",
				"namespace": "mcp__files",
				"arguments": `{"path":"/a"}`,
			},
			map[string]any{"type": "function_call_output", "call_id": "call_ns_1", "output": "ok"},
			map[string]any{
				"type":      "tool_search_call",
				"call_id":   "call_ts_1",
				"arguments": map[string]any{"query": "spawn"},
			},
			map[string]any{"type": "tool_search_output", "call_id": "call_ts_1", "output": "found"},
			map[string]any{
				"type":    "custom_tool_call",
				"call_id": "call_cu_1",
				"name":    "apply_patch",
				"input":   "patch-body",
			},
			map[string]any{"type": "custom_tool_call_output", "call_id": "call_cu_1", "output": "applied"},
		},
	}

	chatReq, toolCtx, err := responsesToOpenAIChatRequest(req, "gpt-5")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if toolCtx == nil {
		t.Fatal("expected tool context")
	}
	if chatReq["tool_choice"] != "auto" {
		t.Fatalf("namespace tool_choice should degrade to auto, got %#v", chatReq["tool_choice"])
	}

	names := map[string]bool{}
	for _, item := range chatReq["tools"].([]any) {
		tool := item.(map[string]any)
		fn := tool["function"].(map[string]any)
		names[stringValue(fn["name"])] = true
	}
	for _, want := range []string{"mcp__files__read", "tool_search", "apply_patch"} {
		if !names[want] {
			t.Fatalf("missing flattened tool %q in %#v", want, names)
		}
	}

	found := map[string]bool{}
	for _, m := range chatReq["messages"].([]any) {
		msg := m.(map[string]any)
		for _, tc := range asMapSlice(msg["tool_calls"]) {
			call := tc.(map[string]any)
			fn := call["function"].(map[string]any)
			found[stringValue(fn["name"])] = true
			switch stringValue(call["id"]) {
			case "call_cu_1":
				if !strings.Contains(stringValue(fn["arguments"]), "patch-body") {
					t.Fatalf("custom args not wrapped: %#v", fn["arguments"])
				}
			}
		}
	}
	for _, want := range []string{"mcp__files__read", "tool_search", "apply_patch"} {
		if !found[want] {
			t.Fatalf("history missing flat tool_call %q in %#v", want, found)
		}
	}
}

func TestOpenAIChatToResponsesCodexRestore(t *testing.T) {
	toolCtx := buildCodexToolContextFromRequest(map[string]any{
		"tools": []any{
			map[string]any{
				"type": "namespace",
				"name": "mcp__files",
				"tools": []any{
					map[string]any{"type": "function", "name": "read", "parameters": map[string]any{"type": "object"}},
				},
			},
			map[string]any{"type": "tool_search"},
			map[string]any{"type": "custom", "name": "apply_patch"},
		},
	})

	chatBody := []byte(`{
		"id":"chatcmpl_1",
		"choices":[{
			"message":{
				"role":"assistant",
				"tool_calls":[
					{"id":"call_ns","type":"function","function":{"name":"mcp__files__read","arguments":"{\"path\":\"/a\"}"}},
					{"id":"call_ts","type":"function","function":{"name":"tool_search","arguments":"{\"query\":\"spawn\"}"}},
					{"id":"call_cu","type":"function","function":{"name":"apply_patch","arguments":"{\"input\":\"patch-body\"}"}}
				]
			},
			"finish_reason":"tool_calls"
		}],
		"usage":{"prompt_tokens":10,"completion_tokens":5}
	}`)

	out, _, err := openAIChatToResponsesResponseWithTools(chatBody, "gpt-5", toolCtx)
	if err != nil {
		t.Fatalf("response convert: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	output := payload["output"].([]any)
	if len(output) != 3 {
		t.Fatalf("want 3 output items, got %s", string(out))
	}
	ns := output[0].(map[string]any)
	if ns["type"] != "function_call" || ns["name"] != "read" || ns["namespace"] != "mcp__files" {
		t.Fatalf("namespace restore failed: %#v", ns)
	}
	if output[1].(map[string]any)["type"] != "tool_search_call" {
		t.Fatalf("tool_search restore failed: %#v", output[1])
	}
	cu := output[2].(map[string]any)
	if cu["type"] != "custom_tool_call" || cu["name"] != "apply_patch" || cu["input"] != "patch-body" {
		t.Fatalf("custom restore failed: %#v", cu)
	}
}

func TestStreamOpenAIChatToResponsesCodexCustomAndToolSearch(t *testing.T) {
	opts := StandardChatToResponsesStreamOptions()
	opts.ToolContext = buildCodexToolContextFromRequest(map[string]any{
		"tools": []any{
			map[string]any{"type": "tool_search"},
			map[string]any{"type": "custom", "name": "apply_patch"},
		},
	})

	upstream := strings.Join([]string{
		`data: {"id":"chatcmpl_s","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_cu","type":"function","function":{"name":"apply_patch","arguments":""}}]}}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"input\":\"hi\"}"}}]}}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_ts","type":"function","function":{"name":"tool_search","arguments":""}}]}}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"query\":\"spawn\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`data: {"usage":{"prompt_tokens":1,"completion_tokens":2}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	rec := httptest.NewRecorder()
	if _, err := streamOpenAIChatToResponsesEventsWithOptions(rec, bytes.NewReader([]byte(upstream)), "gpt-5", opts); err != nil {
		t.Fatalf("stream: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "custom_tool_call") {
		t.Fatalf("stream missing custom_tool_call: %s", body)
	}
	if !strings.Contains(body, "response.custom_tool_call_input.done") {
		t.Fatalf("stream missing custom_tool_call_input.done: %s", body)
	}
	if !strings.Contains(body, "tool_search_call") {
		t.Fatalf("stream missing tool_search_call: %s", body)
	}
}
