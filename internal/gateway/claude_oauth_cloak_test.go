package gateway

import (
	"strings"
	"testing"
)

func TestRemapClaudeOAuthToolNames(t *testing.T) {
	payload := map[string]any{
		"tools": []any{
			map[string]any{"name": "bash", "description": "run shell"},
			map[string]any{"name": "codegraph_search", "description": "search"},
		},
		"tool_choice": map[string]any{"type": "tool", "name": "read"},
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "tool_use", "id": "toolu_1", "name": "write"},
				},
			},
		},
	}
	remapClaudeOAuthToolNames(payload)

	tools := payload["tools"].([]any)
	if tools[0].(map[string]any)["name"] != "Bash" {
		t.Fatalf("expected Bash, got %#v", tools[0])
	}
	if tools[1].(map[string]any)["name"] != "CodegraphSearch" {
		t.Fatalf("expected CodegraphSearch, got %#v", tools[1])
	}
	if payload["tool_choice"].(map[string]any)["name"] != "Read" {
		t.Fatalf("expected Read tool_choice, got %#v", payload["tool_choice"])
	}
	block := payload["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if block["name"] != "Write" {
		t.Fatalf("expected Write tool_use, got %#v", block["name"])
	}
}

func TestApplyClaudeOAuthCloakingReplacesOpenCodeSystem(t *testing.T) {
	payload := map[string]any{
		"model": "claude-sonnet-5",
		"system": "You are OpenCode, the best coding agent on the planet.",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
		"tools": []any{
			map[string]any{"name": "bash"},
		},
	}
	applyClaudeOAuthCloaking(payload)

	system := payload["system"].([]any)
	if !strings.HasPrefix(stringValue(system[0].(map[string]any)["text"]), "x-anthropic-billing-header:") {
		t.Fatalf("expected billing header in system[0], got %#v", system[0])
	}
	if stringValue(system[1].(map[string]any)["text"]) != claudeOAuthAgentIdentifier {
		t.Fatalf("expected agent identifier in system[1], got %#v", system[1])
	}
	user := payload["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(user, "<system-reminder>") {
		t.Fatalf("expected forwarded system context in first user message, got %q", user)
	}
	if payload["tools"].([]any)[0].(map[string]any)["name"] != "Bash" {
		t.Fatalf("expected remapped tool name Bash, got %#v", payload["tools"])
	}
}

func TestClaudeOAuthBillingHTTPHeaderValue(t *testing.T) {
	payload := map[string]any{
		"model":  "claude-sonnet-5",
		"system": buildClaudeOAuthCloakedSystem(),
		"messages": []any{
			map[string]any{"role": "user", "content": "hi"},
		},
	}
	unsigned, err := marshalClaudeOAuthBody(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	signed := signClaudeOAuthCCH(unsigned)
	value := claudeOAuthBillingHTTPHeaderValue(signed)
	if !strings.Contains(value, "cc_version="+claudeOAuthBillingVersion) {
		t.Fatalf("expected billing header value, got %q", value)
	}
	if strings.Contains(value, "cch=00000;") {
		t.Fatalf("expected signed cch in billing header value, got %q", value)
	}
}

func TestSignClaudeOAuthCCH(t *testing.T) {
	payload := map[string]any{
		"model": "claude-sonnet-5",
		"system": buildClaudeOAuthCloakedSystem(),
		"messages": []any{
			map[string]any{"role": "user", "content": "hi"},
		},
		"max_tokens": 32,
	}
	unsigned, err := marshalClaudeOAuthBody(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	signed := signClaudeOAuthCCH(unsigned)
	if strings.Contains(string(signed), "cch=00000;") {
		t.Fatalf("expected signed cch, got %s", signed)
	}
	if string(signed) == string(unsigned) {
		t.Fatalf("expected body to change after signing")
	}
}

func TestReverseRemapClaudeOAuthToolName(t *testing.T) {
	if got := reverseRemapClaudeOAuthToolName("Bash"); got != "bash" {
		t.Fatalf("expected bash, got %q", got)
	}
	if got := reverseRemapClaudeOAuthToolName("CustomTool"); got != "CustomTool" {
		t.Fatalf("expected unchanged name, got %q", got)
	}
}

func TestEnsureClaudeOAuthCacheControl(t *testing.T) {
	payload := map[string]any{
		"system": buildClaudeOAuthCloakedSystem(),
		"tools": []any{
			map[string]any{"name": "Bash"},
			map[string]any{"name": "Read"},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": "first"},
			map[string]any{"role": "assistant", "content": "ok"},
			map[string]any{"role": "user", "content": "second"},
		},
	}
	ensureClaudeOAuthCacheControl(payload)

	tools := payload["tools"].([]any)
	if tools[1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("expected cache_control on last tool")
	}
	system := payload["system"].([]any)
	if system[len(system)-1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("expected cache_control on last system block")
	}
	messages := payload["messages"].([]any)
	firstUser := messages[0].(map[string]any)["content"].([]any)
	if firstUser[len(firstUser)-1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("expected cache_control on second-to-last user turn")
	}
}

func TestEnsureClaudeOAuthCacheControlForcesCanonicalBreakpoints(t *testing.T) {
	payload := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "block-1", "cache_control": map[string]any{"type": "ephemeral"}},
			map[string]any{"type": "text", "text": "block-2"},
		},
		"tools": []any{
			map[string]any{"name": "Bash", "cache_control": map[string]any{"type": "ephemeral"}},
			map[string]any{"name": "Read"},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "first", "cache_control": map[string]any{"type": "ephemeral"}},
			}},
			map[string]any{"role": "assistant", "content": "ok"},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "second", "cache_control": map[string]any{"type": "ephemeral"}},
			}},
		},
	}
	ensureClaudeOAuthCacheControl(payload)

	system := payload["system"].([]any)
	if system[0].(map[string]any)["cache_control"] != nil {
		t.Fatalf("expected first system block cache_control cleared")
	}
	if system[1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("expected cache_control forced onto last system block")
	}
	tools := payload["tools"].([]any)
	if tools[0].(map[string]any)["cache_control"] != nil {
		t.Fatalf("expected first tool cache_control cleared")
	}
	if tools[1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("expected cache_control forced onto last tool")
	}
	firstUser := payload["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if firstUser["cache_control"] == nil {
		t.Fatalf("expected cache_control on second-to-last user turn")
	}
	lastUser := payload["messages"].([]any)[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if lastUser["cache_control"] != nil {
		t.Fatalf("expected last user turn cache_control cleared")
	}
}
