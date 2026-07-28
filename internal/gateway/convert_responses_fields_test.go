package gateway

import (
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
