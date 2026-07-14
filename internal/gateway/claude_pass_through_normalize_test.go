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
		"max_tokens":     1024,
		"stream":         true,
		"metadata":       map[string]any{"user_id": "opencode"},
		"service_tier":   "standard",
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
	wantBudget := defaultClaudeMaxTokens("claude-sonnet-5")
	if payload["max_tokens"] != wantBudget {
		t.Fatalf("max_tokens=%#v want upstream budget %d (ignore client 1024)", payload["max_tokens"], wantBudget)
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
	wantBudget := defaultClaudeMaxTokens("claude-sonnet-5")
	if payload["max_tokens"] != wantBudget {
		t.Fatalf("max_tokens=%#v want %d", payload["max_tokens"], wantBudget)
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
