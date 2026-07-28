package gateway

import (
	"fmt"
	"strings"
	"testing"
)

func TestResponsesTextFormatToClaudeAndChat(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"answer": map[string]any{"type": "string"},
		},
		"required": []any{"answer"},
	}
	responsesReq := map[string]any{
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "hi"},
		},
		"text": map[string]any{
			"format": map[string]any{
				"type":   "json_schema",
				"name":   "result",
				"strict": true,
				"schema": schema,
			},
		},
		"parallel_tool_calls": false,
		"max_output_tokens":   float64(1024),
		"tools": []any{
			map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}},
		},
	}

	claudeReq, _, err := responsesToClaudeRequestDirect(responsesReq, "claude-sonnet-5", 0)
	if err != nil {
		t.Fatalf("claude convert: %v", err)
	}
	if got := int(int64FromAny(claudeReq["max_tokens"])); got != 1024 {
		t.Fatalf("max_tokens should respect client max_output_tokens=1024, got %d", got)
	}
	cfg, _ := claudeReq["output_config"].(map[string]any)
	format, _ := cfg["format"].(map[string]any)
	if stringValue(format["type"]) != "json_schema" {
		t.Fatalf("expected output_config.format json_schema, got %#v", claudeReq["output_config"])
	}
	if _, hasName := format["name"]; hasName {
		t.Fatalf("Claude output_config.format must not include name, got %#v", format)
	}
	choice, _ := claudeReq["tool_choice"].(map[string]any)
	if choice["disable_parallel_tool_use"] != true {
		t.Fatalf("expected disable_parallel_tool_use, got %#v", claudeReq["tool_choice"])
	}

	chatReq, _, err := responsesToOpenAIChatRequest(responsesReq, "gpt-5")
	if err != nil {
		t.Fatalf("chat convert: %v", err)
	}
	rf, _ := chatReq["response_format"].(map[string]any)
	if stringValue(rf["type"]) != "json_schema" {
		t.Fatalf("expected chat response_format json_schema, got %#v", chatReq["response_format"])
	}
	js, _ := rf["json_schema"].(map[string]any)
	if stringValue(js["name"]) != "result" {
		t.Fatalf("expected schema name result, got %#v", js)
	}
	if chatReq["parallel_tool_calls"] != false {
		t.Fatalf("expected parallel_tool_calls=false, got %#v", chatReq["parallel_tool_calls"])
	}
}

func TestResponsesInputFileToClaudeDocument(t *testing.T) {
	responsesReq := map[string]any{
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "summarize"},
					map[string]any{
						"type":      "input_file",
						"filename":  "notes.pdf",
						"file_data": "data:application/pdf;base64,AAA",
					},
				},
			},
		},
	}
	claudeReq, _, err := responsesToClaudeRequestDirect(responsesReq, "claude-sonnet-5", 0)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	msg := claudeReq["messages"].([]any)[0].(map[string]any)
	blocks := msg["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("want text+document, got %#v", blocks)
	}
	doc := blocks[1].(map[string]any)
	if doc["type"] != "document" {
		t.Fatalf("expected document block, got %#v", doc)
	}
	source := doc["source"].(map[string]any)
	if source["type"] != "base64" || source["media_type"] != "application/pdf" || source["data"] != "AAA" {
		t.Fatalf("unexpected document source: %#v", source)
	}
}

func TestResolveClaudeMaxTokensFromResponsesCapsClient(t *testing.T) {
	req := map[string]any{"max_output_tokens": float64(999999)}
	got := resolveClaudeMaxTokensFromResponses(req, "claude-opus-4-6", 0)
	capTokens := effectiveClaudeMaxTokens("claude-opus-4-6", 0)
	if got != capTokens {
		t.Fatalf("client over-budget should clamp to %d, got %d", capTokens, got)
	}
	got = resolveClaudeMaxTokensFromResponses(map[string]any{"max_output_tokens": float64(2048)}, "claude-opus-4-6", 4096)
	if got != 2048 {
		t.Fatalf("key cap 4096 + client 2048 => 2048, got %d", got)
	}
	got = resolveClaudeMaxTokensFromResponses(map[string]any{"max_output_tokens": float64(8000)}, "claude-opus-4-6", 4096)
	if got != 4096 {
		t.Fatalf("key override should hard-cap, got %d", got)
	}
}

func TestAdditionalToolsAndAgentMessageReachClaude(t *testing.T) {
	responsesReq := map[string]any{
		"input": []any{
			map[string]any{
				"type": "additional_tools",
				"role": "developer",
				"tools": []any{
					map[string]any{
						"type":        "custom",
						"name":        "exec",
						"description": "run js",
					},
					map[string]any{
						"type": "namespace",
						"name": "collaboration",
						"tools": []any{
							map[string]any{
								"type":       "function",
								"name":       "followup_task",
								"parameters": map[string]any{"type": "object", "properties": map[string]any{}},
							},
						},
					},
				},
			},
			map[string]any{
				"type":      "agent_message",
				"id":        "amsg_1",
				"author":    "/root",
				"recipient": "/root/pod_a",
				"content": []any{
					map[string]any{
						"type": "input_text",
						"text": "Message Type: NEW_TASK\nPayload:\nrun hostname on pod_a",
					},
				},
			},
		},
	}

	claudeReq, toolCtx, err := responsesToClaudeRequestDirect(responsesReq, "claude-sonnet-5", 0)
	if err != nil {
		t.Fatalf("claude convert: %v", err)
	}
	tools := toolCtx.ClaudeTools()
	if len(tools) < 2 {
		t.Fatalf("expected additional_tools promoted into Claude tools, got %#v", tools)
	}
	names := map[string]bool{}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		names[stringValue(tool["name"])] = true
	}
	if !names["exec"] {
		t.Fatalf("expected custom exec tool, names=%v", names)
	}
	if !names["collaboration__followup_task"] && !names["followup_task"] {
		// flatten uses namespace__name
		found := false
		for n := range names {
			if strings.Contains(n, "followup_task") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected collaboration followup_task flattened, names=%v", names)
		}
	}

	messages, _ := claudeReq["messages"].([]any)
	joined := ""
	for _, raw := range messages {
		msg, _ := raw.(map[string]any)
		blocks, _ := msg["content"].([]any)
		for _, b := range blocks {
			block, _ := b.(map[string]any)
			joined += stringValue(block["text"])
		}
	}
	if !strings.Contains(joined, "run hostname on pod_a") {
		t.Fatalf("agent_message payload missing from Claude messages: %q", joined)
	}

	chatReq, _, err := responsesToOpenAIChatRequest(responsesReq, "gpt-5")
	if err != nil {
		t.Fatalf("chat convert: %v", err)
	}
	chatTools, _ := chatReq["tools"].([]any)
	if len(chatTools) < 2 {
		t.Fatalf("expected additional_tools in chat tools, got %#v", chatTools)
	}
	chatMessages, _ := chatReq["messages"].([]any)
	foundTask := false
	for _, raw := range chatMessages {
		msg, _ := raw.(map[string]any)
		if strings.Contains(fmt.Sprint(msg["content"]), "run hostname on pod_a") {
			foundTask = true
			break
		}
	}
	if !foundTask {
		t.Fatalf("agent_message missing from chat messages: %#v", chatMessages)
	}
}

func TestAgentMessageEncryptedContentPassthrough(t *testing.T) {
	// Codex multi-agent v2 envelope: plaintext routing header + "encrypted" body
	// that is still plaintext when the parent turn did not go through OpenAI
	// Responses encryption.
	item := map[string]any{
		"type":      "agent_message",
		"author":    "/root",
		"recipient": "/root/host_pod1",
		"content": []any{
			map[string]any{
				"type": "input_text",
				"text": "Message Type: NEW_TASK\nTask name: /root/host_pod1\nSender: /root\nPayload:\n",
			},
			map[string]any{
				"type":              "encrypted_content",
				"encrypted_content": "在容器内执行 hostname，page-url=https://example/pod1",
			},
		},
	}
	got := responsesAgentMessagePlainText(item)
	if !strings.Contains(got, "Message Type: NEW_TASK") {
		t.Fatalf("missing envelope header: %q", got)
	}
	if !strings.Contains(got, "在容器内执行 hostname") {
		t.Fatalf("encrypted_content body must pass through as plaintext: %q", got)
	}
	if strings.Contains(got, "omitted by gateway") {
		t.Fatalf("must not emit omission marker: %q", got)
	}

	responsesReq := map[string]any{
		"input": []any{item},
	}
	claudeReq, _, err := responsesToClaudeRequestDirect(responsesReq, "claude-sonnet-5", 0)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	joined := ""
	for _, raw := range claudeReq["messages"].([]any) {
		msg := raw.(map[string]any)
		for _, b := range msg["content"].([]any) {
			joined += stringValue(b.(map[string]any)["text"])
		}
	}
	if !strings.Contains(joined, "page-url=https://example/pod1") {
		t.Fatalf("Claude messages missing task body: %q", joined)
	}
}

func TestSanitizeClaudeStructuredOutputSchemaDropsUnsupported(t *testing.T) {
	schema := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type":    "object",
		"properties": map[string]any{
			"suggestions": map[string]any{
				"type":     "array",
				"maxItems": 3,
				"items":    map[string]any{"type": "string", "minLength": 1},
			},
		},
	}
	cleaned, _ := sanitizeClaudeStructuredOutputSchema(schema).(map[string]any)
	if _, has := cleaned["$schema"]; has {
		t.Fatalf("$schema should be stripped: %#v", cleaned)
	}
	props := cleaned["properties"].(map[string]any)
	suggestions := props["suggestions"].(map[string]any)
	if _, has := suggestions["maxItems"]; has {
		t.Fatalf("maxItems should be stripped: %#v", suggestions)
	}
	items := suggestions["items"].(map[string]any)
	if _, has := items["minLength"]; has {
		t.Fatalf("minLength should be stripped: %#v", items)
	}
}
