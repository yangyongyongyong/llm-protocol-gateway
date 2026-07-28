package gateway

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFlattenNamespaceToolName(t *testing.T) {
	got := flattenNamespaceToolName("mcp__files", "read")
	if got != "mcp__files__read" {
		t.Fatalf("flatten short name: got %q", got)
	}
	longNS := strings.Repeat("n", 40)
	longName := strings.Repeat("t", 40)
	got = flattenNamespaceToolName(longNS, longName)
	if len(got) != codexChatToolNameMaxLen {
		t.Fatalf("flatten long name length=%d want %d (%q)", len(got), codexChatToolNameMaxLen, got)
	}
	if !strings.Contains(got, "__") {
		t.Fatalf("flatten long name missing hash separator: %q", got)
	}
}

func TestCodexToolContextNamespaceFlatten(t *testing.T) {
	req := map[string]any{
		"model": "claude-sonnet-5",
		"tools": []any{
			map[string]any{
				"type": "namespace",
				"name": "mcp__files",
				"tools": []any{
					map[string]any{
						"type":        "function",
						"name":        "read",
						"description": "Read a file",
						"parameters": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"path": map[string]any{"type": "string"},
							},
						},
					},
				},
			},
			map[string]any{"type": "tool_search"},
			map[string]any{
				"type": "custom",
				"name": "apply_patch",
				"description": "Apply a patch",
			},
			map[string]any{
				"type": "function",
				"name": "exec_command",
				"parameters": map[string]any{"type": "object", "properties": map[string]any{}},
			},
		},
		"tool_choice": map[string]any{"type": "namespace", "namespace": "mcp__files"},
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "hi"},
		},
	}

	claudeReq, toolCtx, err := responsesToClaudeRequestDirect(req, "claude-sonnet-5", 0)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if toolCtx == nil {
		t.Fatal("expected tool context")
	}

	tools, ok := claudeReq["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("claude tools missing: %s", mustJSON(claudeReq))
	}
	names := map[string]bool{}
	for _, item := range tools {
		tool := item.(map[string]any)
		names[stringValue(tool["name"])] = true
	}
	wantFlat := "mcp__files__read"
	if !names[wantFlat] {
		t.Fatalf("missing flattened namespace tool %q in %#v", wantFlat, names)
	}
	if !names["tool_search"] {
		t.Fatalf("tool_search proxy dropped: %#v", names)
	}
	if !names["apply_patch"] {
		t.Fatalf("custom tool dropped: %#v", names)
	}
	if !names["exec_command"] {
		t.Fatalf("plain function dropped: %#v", names)
	}
	choice, ok := claudeReq["tool_choice"].(map[string]any)
	if !ok || choice["type"] != "auto" {
		t.Fatalf("namespace tool_choice should degrade to auto, got %#v", claudeReq["tool_choice"])
	}

	// Custom tools must expose a single string input field.
	for _, item := range tools {
		tool := item.(map[string]any)
		if stringValue(tool["name"]) != "apply_patch" {
			continue
		}
		schema := tool["input_schema"].(map[string]any)
		props := schema["properties"].(map[string]any)
		if _, ok := props["input"]; !ok {
			t.Fatalf("custom tool schema missing input: %s", mustJSON(tool))
		}
	}
}

func TestCodexToolContextInputHistoryFlatten(t *testing.T) {
	req := map[string]any{
		"model": "claude-sonnet-5",
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
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "load tools"},
			map[string]any{
				"type":      "function_call",
				"call_id":   "call_ns_1",
				"name":      "read",
				"namespace": "mcp__files",
				"arguments": `{"path":"/tmp/a"}`,
			},
			map[string]any{
				"type":    "function_call_output",
				"call_id": "call_ns_1",
				"output":  "ok",
			},
			map[string]any{
				"type":      "tool_search_call",
				"call_id":   "call_ts_1",
				"arguments": map[string]any{"query": "spawn", "limit": 5},
			},
			map[string]any{
				"type":    "tool_search_output",
				"call_id": "call_ts_1",
				"output":  "found",
			},
			map[string]any{
				"type":    "custom_tool_call",
				"call_id": "call_cu_1",
				"name":    "apply_patch",
				"input":   "*** Begin Patch\n*** End Patch",
			},
			map[string]any{
				"type":    "custom_tool_call_output",
				"call_id": "call_cu_1",
				"output":  "applied",
			},
		},
	}

	claudeReq, _, err := responsesToClaudeRequestDirect(req, "claude-sonnet-5", 0)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	msgs := claudeReq["messages"].([]any)
	var flatUses []string
	for _, m := range msgs {
		msg := m.(map[string]any)
		blocks, _ := msg["content"].([]any)
		for _, b := range blocks {
			block, ok := b.(map[string]any)
			if !ok || stringValue(block["type"]) != "tool_use" {
				continue
			}
			flatUses = append(flatUses, stringValue(block["name"]))
			switch stringValue(block["id"]) {
			case "call_ns_1":
				if stringValue(block["name"]) != "mcp__files__read" {
					t.Fatalf("namespaced call not flattened: %#v", block)
				}
			case "call_ts_1":
				if stringValue(block["name"]) != "tool_search" {
					t.Fatalf("tool_search_call name: %#v", block)
				}
			case "call_cu_1":
				if stringValue(block["name"]) != "apply_patch" {
					t.Fatalf("custom_tool_call name: %#v", block)
				}
				input, _ := block["input"].(map[string]any)
				if stringValue(input["input"]) != "*** Begin Patch\n*** End Patch" {
					t.Fatalf("custom input wrap: %#v", block["input"])
				}
			}
		}
	}
	if len(flatUses) < 3 {
		t.Fatalf("expected >=3 tool_use blocks, got %v from %s", flatUses, mustJSON(claudeReq))
	}
}

func TestCodexToolContextResponseRestore(t *testing.T) {
	req := map[string]any{
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
	}
	toolCtx := buildCodexToolContextFromRequest(req)

	claudeBody := []byte(`{
		"id":"msg_1",
		"type":"message",
		"role":"assistant",
		"model":"claude-sonnet-5",
		"content":[
			{"type":"tool_use","id":"call_ns","name":"mcp__files__read","input":{"path":"/a"}},
			{"type":"tool_use","id":"call_ts","name":"tool_search","input":{"query":"spawn","limit":3}},
			{"type":"tool_use","id":"call_cu","name":"apply_patch","input":{"input":"patch-body"}}
		],
		"stop_reason":"tool_use",
		"usage":{"input_tokens":10,"output_tokens":5}
	}`)

	out, _, err := claudeToResponsesResponseDirect(claudeBody, "claude-sonnet-5", toolCtx)
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
	ts := output[1].(map[string]any)
	if ts["type"] != "tool_search_call" {
		t.Fatalf("tool_search restore failed: %#v", ts)
	}
	cu := output[2].(map[string]any)
	if cu["type"] != "custom_tool_call" || cu["name"] != "apply_patch" || cu["input"] != "patch-body" {
		t.Fatalf("custom restore failed: %#v", cu)
	}
}

func TestCodexToolContextStreamCustomAndToolSearch(t *testing.T) {
	req := map[string]any{
		"tools": []any{
			map[string]any{"type": "tool_search"},
			map[string]any{"type": "custom", "name": "apply_patch"},
		},
	}
	toolCtx := buildCodexToolContextFromRequest(req)

	sse := "" +
		"event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_s\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-5\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_cu\",\"name\":\"apply_patch\",\"input\":{}}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"input\\\":\\\"hi\\\"}\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_ts\",\"name\":\"tool_search\",\"input\":{}}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"query\\\":\\\"spawn\\\"}\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":2}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	rec := httptest.NewRecorder()
	if _, err := streamClaudeToResponsesEventsDirect(rec, bytes.NewReader([]byte(sse)), "claude-sonnet-5", toolCtx); err != nil {
		t.Fatalf("stream: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"custom_tool_call"`) && !strings.Contains(body, `"type": "custom_tool_call"`) {
		// SSE JSON is compact without spaces after colon typically.
		if !strings.Contains(body, "custom_tool_call") {
			t.Fatalf("stream missing custom_tool_call: %s", body)
		}
	}
	if !strings.Contains(body, "response.custom_tool_call_input.done") {
		t.Fatalf("stream missing custom_tool_call_input.done: %s", body)
	}
	if !strings.Contains(body, "tool_search_call") {
		t.Fatalf("stream missing tool_search_call: %s", body)
	}
}

func TestResponsesToolsToOpenAIChatKeepsNamespace(t *testing.T) {
	tools := []any{
		map[string]any{
			"type": "namespace",
			"name": "mcp__files",
			"tools": []any{
				map[string]any{"type": "function", "name": "read", "parameters": map[string]any{"type": "object"}},
			},
		},
		map[string]any{"type": "tool_search"},
	}
	out := responsesToolsToOpenAIChat(tools)
	names := map[string]bool{}
	for _, item := range out {
		tool := item.(map[string]any)
		fn, _ := tool["function"].(map[string]any)
		names[stringValue(fn["name"])] = true
	}
	if !names["mcp__files__read"] {
		t.Fatalf("chat hop dropped namespace flatten: %#v", names)
	}
	if !names["tool_search"] {
		t.Fatalf("chat hop dropped tool_search: %#v", names)
	}
}
