package gateway

import (
	"encoding/json"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestNormalizeClaudePassThroughPayloadStripsVolatileFields(t *testing.T) {
	payload := map[string]any{
		"model": "claude-sonnet-5",
		"system": []any{
			map[string]any{
				"type":          "text",
				"text":          "You are OpenCode",
				"cache_control": map[string]any{"type": "ephemeral"},
				"citations":     []any{map[string]any{"id": "1"}},
			},
		},
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":          "text",
						"text":          "hello",
						"cache_control": map[string]any{"type": "ephemeral"},
					},
				},
			},
		},
		"tools": []any{
			map[string]any{
				"name":        "bash",
				"description": "run shell",
				"input_schema": map[string]any{
					"type": "object",
				},
				"cache_control": map[string]any{"type": "ephemeral"},
			},
		},
		"max_tokens":   1024,
		"stream":       true,
		"metadata":     map[string]any{"user_id": "opencode"},
		"service_tier": "standard",
		"context_management": map[string]any{
			"edits": []any{},
		},
	}
	normalizeClaudePassThroughPayload(payload)

	if _, exists := payload["metadata"]; exists {
		t.Fatalf("expected metadata stripped, got %#v", payload["metadata"])
	}
	if _, exists := payload["service_tier"]; exists {
		t.Fatalf("expected service_tier stripped")
	}
	if _, exists := payload["context_management"]; exists {
		t.Fatalf("expected context_management stripped")
	}

	system := payload["system"].(string)
	if system != "You are OpenCode" {
		t.Fatalf("expected collapsed system string, got %#v", payload["system"])
	}

	messages := payload["messages"].([]any)
	user := messages[0].(map[string]any)
	if user["content"] != "hello" {
		t.Fatalf("expected collapsed user content, got %#v", user["content"])
	}

	tools := payload["tools"].([]any)
	tool := tools[0].(map[string]any)
	if tool["name"] != "bash" {
		t.Fatalf("expected tool name preserved, got %#v", tool)
	}
	if _, exists := tool["cache_control"]; exists {
		t.Fatalf("expected tool cache_control stripped before cloaking")
	}
	// normalize 本身保留客户端 max_tokens；真正按上游模型覆盖由
	// rewriteClaudeUpstreamMaxTokens 在发送前完成。
	if payload["max_tokens"] != 1024 {
		t.Fatalf("max_tokens=%#v want client value 1024 preserved by normalize", payload["max_tokens"])
	}
}

func TestNormalizeClaudePassThroughPayloadPreservesToolBlocks(t *testing.T) {
	payload := map[string]any{
		"model": "claude-sonnet-5",
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{
						"type":  "tool_use",
						"id":    "toolu_1",
						"name":  "bash",
						"input": map[string]any{"command": "ls"},
					},
				},
			},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "toolu_1",
						"content":     "ok",
					},
				},
			},
		},
		"max_tokens": 32,
	}
	normalizeClaudePassThroughPayload(payload)

	messages := payload["messages"].([]any)
	assistantBlocks := messages[0].(map[string]any)["content"].([]any)
	toolUse := assistantBlocks[0].(map[string]any)
	if toolUse["name"] != "bash" || toolUse["id"] != "toolu_1" {
		t.Fatalf("expected tool_use preserved, got %#v", toolUse)
	}
	if payload["max_tokens"] != 32 {
		t.Fatalf("max_tokens=%#v want client 32 preserved by normalize", payload["max_tokens"])
	}
}

func TestRewriteClaudeUpstreamMaxTokensIgnoresClientBudget(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5","max_tokens":4096,"messages":[{"role":"user","content":"hi"}]}`)
	out := rewriteClaudeUpstreamMaxTokens(body, domain.Provider{}, 0)
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatal(err)
	}
	want := defaultClaudeMaxTokens("claude-fable-5")
	got, ok := payload["max_tokens"].(float64) // json numbers
	if !ok {
		if n, ok2 := payload["max_tokens"].(int); ok2 {
			got = float64(n)
			ok = true
		}
	}
	if !ok || int(got) != want {
		t.Fatalf("max_tokens=%#v want %d", payload["max_tokens"], want)
	}
}

func TestNormalizeClaudePassThroughToolsKeepsWebSearchWithoutInputSchema(t *testing.T) {
	payload := map[string]any{
		"model": "claude-sonnet-5",
		"messages": []any{
			map[string]any{"role": "user", "content": "search something"},
		},
		"tools": []any{
			map[string]any{
				"type":          "web_search_20250305",
				"name":          "web_search",
				"max_uses":      8,
				"cache_control": map[string]any{"type": "ephemeral"},
				// Client/gateway must not invent this; Anthropic rejects it on server tools.
				"input_schema": map[string]any{"type": "object", "properties": map[string]any{}},
			},
			map[string]any{
				"name":        "bash",
				"description": "run shell",
				"input_schema": map[string]any{
					"type": "object",
				},
			},
		},
		"max_tokens": 1024,
	}
	normalizeClaudePassThroughPayload(payload)

	tools := payload["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools len=%d want 2: %#v", len(tools), tools)
	}
	webSearch := tools[0].(map[string]any)
	if webSearch["type"] != "web_search_20250305" {
		t.Fatalf("web_search type=%#v", webSearch["type"])
	}
	if webSearch["name"] != "web_search" {
		t.Fatalf("web_search name=%#v", webSearch["name"])
	}
	if webSearch["max_uses"] != 8 {
		t.Fatalf("web_search max_uses=%#v want 8", webSearch["max_uses"])
	}
	if _, exists := webSearch["input_schema"]; exists {
		t.Fatalf("server tool must not keep/inject input_schema, got %#v", webSearch)
	}
	if _, exists := webSearch["cache_control"]; exists {
		t.Fatalf("server tool cache_control should be stripped, got %#v", webSearch)
	}

	custom := tools[1].(map[string]any)
	if custom["name"] != "bash" {
		t.Fatalf("custom tool name=%#v", custom["name"])
	}
	if custom["input_schema"] == nil {
		t.Fatalf("custom tool must keep input_schema")
	}
}

func TestNormalizeClaudePassThroughToolsInjectsSchemaOnlyForCustomTools(t *testing.T) {
	tools := normalizeClaudePassThroughTools([]any{
		map[string]any{
			"type":     "web_search_20250305",
			"name":     "web_search",
			"max_uses": float64(3),
		},
		map[string]any{
			"name": "no_schema_custom",
		},
		map[string]any{
			"type": "custom",
			"name": "typed_custom",
		},
	})
	if len(tools) != 3 {
		t.Fatalf("tools len=%d want 3", len(tools))
	}
	web := tools[0].(map[string]any)
	if _, ok := web["input_schema"]; ok {
		t.Fatalf("web_search got input_schema: %#v", web)
	}
	if web["max_uses"] != float64(3) {
		t.Fatalf("max_uses=%#v", web["max_uses"])
	}
	for i, label := range []string{"no_schema_custom", "typed_custom"} {
		custom := tools[i+1].(map[string]any)
		if custom["name"] != label {
			t.Fatalf("tool[%d] name=%#v want %s", i+1, custom["name"], label)
		}
		if custom["input_schema"] == nil {
			t.Fatalf("tool[%d] missing default input_schema: %#v", i+1, custom)
		}
	}
}
