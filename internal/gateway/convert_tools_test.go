package gateway

import (
	"encoding/json"
	"testing"
)

func TestOpenAIChatToClaudeRequestSanitizesInvalidToolUseIDs(t *testing.T) {
	openAIReq := map[string]any{
		"model": "claude-sonnet-5",
		"messages": []any{
			map[string]any{"role": "user", "content": "read this"},
			map[string]any{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"id":   "functions.Read:1",
						"type": "function",
						"function": map[string]any{
							"name":      "Read",
							"arguments": `{"path":"a.go"}`,
						},
					},
				},
			},
			map[string]any{
				"role":         "tool",
				"tool_call_id": "functions.Read:1",
				"content":      "ok",
			},
		},
	}
	claudeReq, err := openAIChatToClaudeRequest(openAIReq, "claude-sonnet-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	messages := claudeReq["messages"].([]map[string]any)
	assistantBlocks := messages[1]["content"].([]any)
	toolUse := assistantBlocks[0].(map[string]any)
	if toolUse["id"] != "functions_Read_1" {
		t.Fatalf("tool_use.id = %q, want sanitized", toolUse["id"])
	}
	resultBlocks := messages[2]["content"].([]any)
	toolResult := resultBlocks[0].(map[string]any)
	if toolResult["tool_use_id"] != "functions_Read_1" {
		t.Fatalf("tool_use_id = %q, want matching sanitized id", toolResult["tool_use_id"])
	}
}

func TestOpenAIContentToClaudeBlocksConvertsImageURL(t *testing.T) {
	blocks := openAIContentToClaudeBlocks([]any{
		map[string]any{"type": "text", "text": "see image"},
		map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": "data:image/png;base64,abc123",
			},
		},
	})
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	image := blocks[1].(map[string]any)
	if image["type"] != "image" {
		t.Fatalf("expected image block, got %#v", image)
	}
	source := image["source"].(map[string]any)
	if source["type"] != "base64" || source["media_type"] != "image/png" || source["data"] != "abc123" {
		t.Fatalf("unexpected image source %#v", source)
	}
}

func TestSanitizeAnthropicToolUseID(t *testing.T) {
	cases := map[string]string{
		"call_1":           "call_1",
		"functions.Read:1": "functions_Read_1",
		"call|abc":         "call_abc",
		"  ":               "toolu_missing",
		"...":              "toolu_sanitized",
	}
	for in, want := range cases {
		if got := sanitizeAnthropicToolUseID(in); got != want {
			t.Fatalf("sanitizeAnthropicToolUseID(%q)=%q want %q", in, got, want)
		}
	}
}

func TestOpenAIChatToClaudeRequestMapsToolsAndMessages(t *testing.T) {
	openAIReq := map[string]any{
		"model": "claude-sonnet-5",
		"messages": []any{
			map[string]any{"role": "user", "content": "What is the weather in SF?"},
			map[string]any{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []any{
					map[string]any{
						"id":   "call_1",
						"type": "function",
						"function": map[string]any{
							"name":      "get_weather",
							"arguments": `{"location":"SF"}`,
						},
					},
				},
			},
			map[string]any{
				"role":         "tool",
				"tool_call_id": "call_1",
				"content":      "sunny",
			},
			map[string]any{"role": "user", "content": "Thanks"},
		},
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "get_weather",
					"description": "Get weather",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"location": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
		"tool_choice": "auto",
	}
	claudeReq, err := openAIChatToClaudeRequest(openAIReq, "claude-sonnet-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tools := claudeReq["tools"].([]any)
	tool := tools[0].(map[string]any)
	if tool["name"] != "get_weather" {
		t.Fatalf("expected claude tool name, got %#v", tool["name"])
	}
	if tool["input_schema"] == nil {
		t.Fatalf("expected input_schema on claude tool")
	}
	toolChoice := claudeReq["tool_choice"].(map[string]any)
	if toolChoice["type"] != "auto" {
		t.Fatalf("expected auto tool_choice, got %#v", toolChoice)
	}

	messages := claudeReq["messages"].([]map[string]any)
	if len(messages) != 4 {
		t.Fatalf("expected 4 claude messages, got %d", len(messages))
	}
	assistantBlocks := messages[1]["content"].([]any)
	if assistantBlocks[0].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("expected tool_use block in assistant message, got %#v", assistantBlocks[0])
	}
	resultBlocks := messages[2]["content"].([]any)
	if resultBlocks[0].(map[string]any)["type"] != "tool_result" {
		t.Fatalf("expected tool_result block, got %#v", resultBlocks[0])
	}
}

func TestClaudeRequestToOpenAIChatMapsToolsAndMessages(t *testing.T) {
	claudeReq := map[string]any{
		"model": "deepseek-chat",
		"messages": []any{
			map[string]any{"role": "user", "content": "Weather?"},
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{
						"type":  "tool_use",
						"id":    "toolu_1",
						"name":  "get_weather",
						"input": map[string]any{"location": "SF"},
					},
				},
			},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "toolu_1",
						"content":     "sunny",
					},
				},
			},
		},
		"tools": []any{
			map[string]any{
				"name":        "get_weather",
				"description": "Get weather",
				"input_schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"location": map[string]any{"type": "string"},
					},
				},
			},
		},
		"tool_choice": map[string]any{"type": "any"},
	}
	openAIReq, err := claudeRequestToOpenAIChat(claudeReq, "deepseek-chat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tools := openAIReq["tools"].([]any)
	tool := tools[0].(map[string]any)
	functionValue := tool["function"].(map[string]any)
	if functionValue["name"] != "get_weather" {
		t.Fatalf("expected openai tool name, got %#v", functionValue["name"])
	}
	if openAIReq["tool_choice"] != "required" {
		t.Fatalf("expected required tool_choice, got %#v", openAIReq["tool_choice"])
	}

	messages := openAIReq["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("expected 3 openai messages, got %d", len(messages))
	}
	assistant := messages[1].(map[string]any)
	if assistant["tool_calls"] == nil {
		t.Fatalf("expected assistant tool_calls")
	}
	toolMessage := messages[2].(map[string]any)
	if toolMessage["role"] != "tool" || toolMessage["tool_call_id"] != "toolu_1" {
		t.Fatalf("expected tool message, got %#v", toolMessage)
	}
}

func TestClaudeResponseToOpenAIChatMapsToolUse(t *testing.T) {
	claudeResp := []byte(`{
		"id":"msg_test",
		"type":"message",
		"role":"assistant",
		"model":"claude-sonnet-5",
		"content":[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"location":"SF"}}],
		"stop_reason":"tool_use",
		"usage":{"input_tokens":10,"output_tokens":2}
	}`)
	body, _, err := claudeResponseToOpenAIChat(claudeResp, "claude-sonnet-5", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	choices := payload["choices"].([]any)
	choice := choices[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("expected finish_reason tool_calls, got %#v", choice["finish_reason"])
	}
	message := choice["message"].(map[string]any)
	toolCalls := message["tool_calls"].([]any)
	toolCall := toolCalls[0].(map[string]any)
	if toolCall["id"] != "toolu_1" {
		t.Fatalf("expected tool call id, got %#v", toolCall["id"])
	}
}

func TestResolveOpenAIToolNameFromClaudePreservesCursorTitleCase(t *testing.T) {
	clientTools := extractOpenAIToolNames(map[string]any{
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": "Read",
				},
			},
		},
	})
	if got := resolveOpenAIToolNameFromClaude("Read", clientTools); got != "Read" {
		t.Fatalf("expected Read, got %q", got)
	}
}

func TestResolveOpenAIToolNameFromClaudeRestoresLowercaseClientNames(t *testing.T) {
	clientTools := extractOpenAIToolNames(map[string]any{
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": "bash",
				},
			},
		},
	})
	if got := resolveOpenAIToolNameFromClaude("Bash", clientTools); got != "bash" {
		t.Fatalf("expected bash, got %q", got)
	}
}

func TestOpenAIToolToClaudeHandlesResponsesFlatAndCustomTools(t *testing.T) {
	flat := openAIToolToClaude(map[string]any{
		"type":        "function",
		"name":        "shell",
		"description": "Run a shell command",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
			},
		},
	})
	if flat["name"] != "shell" {
		t.Fatalf("flat function name = %#v", flat["name"])
	}
	schema, _ := flat["input_schema"].(map[string]any)
	if schema["type"] != "object" {
		t.Fatalf("flat function schema = %#v", flat["input_schema"])
	}

	custom := openAIToolToClaude(map[string]any{
		"type":        "custom",
		"name":        "computer_use",
		"description": "Control the computer",
	})
	if custom["name"] != "computer_use" {
		t.Fatalf("custom name = %#v", custom["name"])
	}
	if custom["input_schema"] == nil {
		t.Fatal("expected default input_schema for custom tool")
	}

	nested := openAIToolToClaude(map[string]any{
		"type": "custom",
		"custom": map[string]any{
			"name":        "nested_tool",
			"description": "nested",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	})
	if nested["name"] != "nested_tool" {
		t.Fatalf("nested custom name = %#v", nested["name"])
	}
	if nested["input_schema"] == nil {
		t.Fatal("expected nested custom input_schema")
	}
}

func TestResponsesToClaudeRequestPreservesCodexStyleTools(t *testing.T) {
	responsesReq := map[string]any{
		"model": "claude-sonnet-5",
		"input": "hi",
		"tools": []any{
			map[string]any{
				"type":        "custom",
				"name":        "update_plan",
				"description": "Update the plan",
			},
			map[string]any{
				"type":        "function",
				"name":        "shell",
				"description": "Shell",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
	claudeReq, err := responsesToClaudeRequest(responsesReq, "claude-sonnet-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tools, ok := claudeReq["tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("expected 2 claude tools, got %#v", claudeReq["tools"])
	}
	for i, item := range tools {
		tool := item.(map[string]any)
		if stringValue(tool["name"]) == "" {
			t.Fatalf("tool[%d] missing name: %#v", i, tool)
		}
		if tool["input_schema"] == nil {
			t.Fatalf("tool[%d] missing input_schema: %#v", i, tool)
		}
	}
}

func TestResponsesToolsToOpenAIChatNormalizesCustomAndDropsBuiltins(t *testing.T) {
	responsesReq := map[string]any{
		"model": "glm-5.1",
		"input": "hi",
		"tools": []any{
			map[string]any{
				"type":        "function",
				"name":        "shell",
				"description": "Shell",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{"type": "string"},
					},
				},
			},
			map[string]any{
				"type":        "custom",
				"name":        "update_plan",
				"description": "Update plan",
			},
			map[string]any{"type": "web_search"},
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "already_nested",
					"description": "ok",
					"parameters":   map[string]any{"type": "object", "properties": map[string]any{}},
				},
			},
		},
		"tool_choice": map[string]any{"type": "function", "name": "shell"},
	}
	chatReq, err := responsesToOpenAIChatRequest(responsesReq, "glm-5.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tools, ok := chatReq["tools"].([]any)
	if !ok || len(tools) != 3 {
		t.Fatalf("expected 3 chat tools (dropped web_search), got %#v", chatReq["tools"])
	}
	for i, item := range tools {
		tool := item.(map[string]any)
		if tool["type"] != "function" {
			t.Fatalf("tool[%d].type = %#v, want function", i, tool["type"])
		}
		fn, ok := tool["function"].(map[string]any)
		if !ok || stringValue(fn["name"]) == "" {
			t.Fatalf("tool[%d] missing nested function: %#v", i, tool)
		}
		if fn["parameters"] == nil {
			t.Fatalf("tool[%d] missing parameters: %#v", i, tool)
		}
	}
	choice, ok := chatReq["tool_choice"].(map[string]any)
	if !ok || choice["type"] != "function" {
		t.Fatalf("unexpected tool_choice %#v", chatReq["tool_choice"])
	}
	if fn, _ := choice["function"].(map[string]any); stringValue(fn["name"]) != "shell" {
		t.Fatalf("expected tool_choice function shell, got %#v", choice)
	}
}

func TestOpenAIChatResponseToClaudeMapsToolCalls(t *testing.T) {
	openAIResp := []byte(`{
		"id":"chatcmpl-test",
		"object":"chat.completion",
		"model":"deepseek-chat",
		"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"location\":\"SF\"}"}}]},"finish_reason":"tool_calls"}],
		"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}
	}`)
	body, _, err := openAIChatResponseToClaude(openAIResp, "deepseek-chat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if payload["stop_reason"] != "tool_use" {
		t.Fatalf("expected stop_reason tool_use, got %#v", payload["stop_reason"])
	}
	content := payload["content"].([]any)
	block := content[0].(map[string]any)
	if block["type"] != "tool_use" || block["name"] != "get_weather" {
		t.Fatalf("expected tool_use block, got %#v", block)
	}
}
