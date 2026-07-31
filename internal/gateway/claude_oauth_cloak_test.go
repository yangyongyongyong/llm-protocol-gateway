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

func TestRemapClaudeOAuthToolNamesPreservesServerWebSearchToolChoice(t *testing.T) {
	// Regression: 2026-07-31 16:30 — OAuth cloaking TitleCased tool_choice
	// web_search → WebSearch while leaving the server tool name as web_search,
	// causing Anthropic 400: Tool 'WebSearch' not found in provided tools.
	payload := map[string]any{
		"tools": []any{
			map[string]any{
				"type":     "web_search_20250305",
				"name":     "web_search",
				"max_uses": 8,
			},
			map[string]any{"name": "bash", "description": "run shell"},
		},
		"tool_choice": map[string]any{"type": "tool", "name": "web_search"},
	}
	remapClaudeOAuthToolNames(payload)

	tools := payload["tools"].([]any)
	web := tools[0].(map[string]any)
	if web["name"] != "web_search" {
		t.Fatalf("server tool name changed: %#v", web)
	}
	if tools[1].(map[string]any)["name"] != "Bash" {
		t.Fatalf("client tool should still cloak to Bash, got %#v", tools[1])
	}
	choice := payload["tool_choice"].(map[string]any)
	if choice["name"] != "web_search" {
		t.Fatalf("server tool_choice must stay web_search, got %#v", choice)
	}
}

func TestRemapClaudeOAuthToolNamePreservesAnthropicServerToolNames(t *testing.T) {
	// Root-level hard denylist: even without a tools[] context, server tool
	// names must not TitleCase (same class as mcp__ preservation).
	for _, name := range []string{"web_search", "web_fetch", "code_execution"} {
		got, changed := remapClaudeOAuthToolName(name)
		if changed || got != name {
			t.Fatalf("remapClaudeOAuthToolName(%q) = (%q, %v); want unchanged", name, got, changed)
		}
	}
	// Claude Code client webfetch (no underscore) still cloaks to WebFetch.
	if got, changed := remapClaudeOAuthToolName("webfetch"); !changed || got != "WebFetch" {
		t.Fatalf("client webfetch should cloak to WebFetch, got (%q, %v)", got, changed)
	}
}

func TestRemapClaudeOAuthToolNamesPreservesAllServerToolSurfaces(t *testing.T) {
	payload := map[string]any{
		"tools": []any{
			map[string]any{"type": "web_search_20250305", "name": "web_search", "max_uses": 8},
			map[string]any{"type": "web_fetch_20250910", "name": "web_fetch", "max_uses": 5},
			map[string]any{"type": "code_execution_20250522", "name": "code_execution"},
			// Server bash_* must NOT become Bash even though client bash does.
			map[string]any{"type": "bash_20250124", "name": "bash"},
			map[string]any{"name": "read", "description": "read file"},
		},
		"tool_choice": map[string]any{"type": "tool", "name": "web_fetch"},
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "server_tool_use", "id": "srv_1", "name": "web_search", "input": map[string]any{"query": "q"}},
					map[string]any{"type": "tool_use", "id": "toolu_1", "name": "web_fetch", "input": map[string]any{"url": "https://example.com"}},
					map[string]any{"type": "tool_use", "id": "toolu_2", "name": "bash", "input": map[string]any{"command": "ls"}},
					map[string]any{"type": "tool_use", "id": "toolu_3", "name": "write", "input": map[string]any{}},
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "toolu_search",
						"content": []any{
							map[string]any{"type": "tool_reference", "tool_name": "code_execution"},
							map[string]any{"type": "tool_reference", "tool_name": "read"},
						},
					},
				},
			},
		},
	}
	remapClaudeOAuthToolNames(payload)

	tools := payload["tools"].([]any)
	wantToolNames := []string{"web_search", "web_fetch", "code_execution", "bash", "Read"}
	for i, want := range wantToolNames {
		got := tools[i].(map[string]any)["name"]
		if got != want {
			t.Fatalf("tools[%d].name=%#v want %q", i, got, want)
		}
	}
	if payload["tool_choice"].(map[string]any)["name"] != "web_fetch" {
		t.Fatalf("tool_choice web_fetch remapped: %#v", payload["tool_choice"])
	}
	blocks := payload["messages"].([]any)[0].(map[string]any)["content"].([]any)
	assertName := func(i int, field, want string) {
		t.Helper()
		block := blocks[i].(map[string]any)
		if got := stringValue(block[field]); got != want {
			t.Fatalf("blocks[%d].%s=%q want %q (block=%#v)", i, field, got, want, block)
		}
	}
	assertName(0, "name", "web_search")      // server_tool_use
	assertName(1, "name", "web_fetch")       // hard denylist
	assertName(2, "name", "bash")            // request-scoped server tool
	assertName(3, "name", "Write")           // client tool still cloaked
	refs := blocks[4].(map[string]any)["content"].([]any)
	if stringValue(refs[0].(map[string]any)["tool_name"]) != "code_execution" {
		t.Fatalf("nested code_execution remapped: %#v", refs[0])
	}
	if stringValue(refs[1].(map[string]any)["tool_name"]) != "Read" {
		t.Fatalf("nested client read should cloak to Read, got %#v", refs[1])
	}
}

func TestApplyClaudeOAuthCloakingPreservesWebSearchToolChoice(t *testing.T) {
	// Full cloaking path matching OpenCode / Claude Code web-search subagent.
	payload := map[string]any{
		"model": "claude-sonnet-5",
		"system": []any{
			map[string]any{"type": "text", "text": "You are an assistant for performing a web search tool use"},
		},
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "text", "text": "Perform a web search for the query: test"}},
			},
		},
		"tools": []any{
			map[string]any{"type": "web_search_20250305", "name": "web_search", "max_uses": 8},
		},
		"tool_choice": map[string]any{"type": "tool", "name": "web_search"},
	}
	applyClaudeOAuthCloaking(payload)
	if payload["tool_choice"].(map[string]any)["name"] != "web_search" {
		t.Fatalf("full cloaking remapped web_search tool_choice: %#v", payload["tool_choice"])
	}
	tools := payload["tools"].([]any)
	if tools[0].(map[string]any)["name"] != "web_search" {
		t.Fatalf("full cloaking remapped web_search tool: %#v", tools[0])
	}
}

func TestRemapClaudeOAuthToolNamePreservesMCPNamespacedNames(t *testing.T) {
	// mcp__<server>__<tool> is a protocol convention (namespacing), not a
	// "third-party lowercase/snake_case" client tool name; PascalCasing it
	// (mcp__test__ping_server → McpTestPingServer) breaks every MCP server
	// behind Claude OAuth, since the client only recognizes the mcp__ form.
	cases := []string{
		"mcp__test__ping_server",
		"mcp__files__read",
		"mcp__playwright__browser_click",
	}
	for _, name := range cases {
		got, changed := remapClaudeOAuthToolName(name)
		if changed || got != name {
			t.Fatalf("remapClaudeOAuthToolName(%q) = (%q, %v); want unchanged", name, got, changed)
		}
	}

	payload := map[string]any{
		"tools": []any{
			map[string]any{"name": "mcp__test__ping_server", "description": "ping"},
			map[string]any{"name": "bash"},
		},
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "tool_use", "id": "toolu_1", "name": "mcp__test__ping_server"},
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "toolu_search",
						"content": []any{
							map[string]any{"type": "tool_reference", "tool_name": "mcp__test__ping_server"},
						},
					},
				},
			},
		},
	}
	remapClaudeOAuthToolNames(payload)

	tools := payload["tools"].([]any)
	if tools[0].(map[string]any)["name"] != "mcp__test__ping_server" {
		t.Fatalf("mcp tool name changed: %#v", tools[0])
	}
	if tools[1].(map[string]any)["name"] != "Bash" {
		t.Fatalf("expected non-mcp tool still cloaked to Bash, got %#v", tools[1])
	}
	blocks := payload["messages"].([]any)[0].(map[string]any)["content"].([]any)
	toolUse := blocks[0].(map[string]any)
	if toolUse["name"] != "mcp__test__ping_server" {
		t.Fatalf("tool_use mcp name changed: %#v", toolUse["name"])
	}
	toolResult := blocks[1].(map[string]any)
	ref := toolResult["content"].([]any)[0].(map[string]any)
	if ref["tool_name"] != "mcp__test__ping_server" {
		t.Fatalf("nested tool_reference mcp name changed: %#v", ref["tool_name"])
	}
}

func TestApplyClaudeOAuthCloakingReplacesOpenCodeSystem(t *testing.T) {
	payload := map[string]any{
		"model":  "claude-sonnet-5",
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
		"model":  "claude-sonnet-5",
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
