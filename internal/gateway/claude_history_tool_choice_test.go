package gateway

import (
	"testing"
)

// historyWithUndeclaredTool is a conversation that called a tool the request no
// longer declares — what an on-demand tool set (tool_search) or a dropped MCP
// server leaves behind.
func historyWithUndeclaredTool() []any {
	return []any{
		map[string]any{"role": "user", "content": "go"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "toolu_gone", "name": "LookupThing", "input": map[string]any{}},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "toolu_gone", "content": "found"},
		}},
	}
}

// The request still declares tools, so nothing may forbid calling them: Claude
// Code sends no tool_choice, and a forced "none" would have reached the upstream
// and stopped the agent loop for the rest of the conversation — the history's
// undeclared tool_use never goes away, so every later turn was poisoned too.
func TestNormalizeKeepsToolsUsableWhenHistoryNamesAnUndeclaredOne(t *testing.T) {
	payload := map[string]any{
		"model":    "claude-opus-5-5",
		"messages": historyWithUndeclaredTool(),
		"tools": []any{
			map[string]any{"name": "Bash", "input_schema": map[string]any{"type": "object"}},
		},
	}
	normalizeClaudePassThroughPayload(payload)

	tools := payload["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("expected the stub added beside the declared tool, got %#v", tools)
	}
	names := map[string]bool{}
	for _, raw := range tools {
		names[stringValue(raw.(map[string]any)["name"])] = true
	}
	if !names["Bash"] || !names["LookupThing"] {
		t.Fatalf("expected both the declared and the stubbed tool, got %#v", names)
	}
	if choice, ok := payload["tool_choice"]; ok {
		t.Fatalf("a request that declares tools must not have its tool_choice forced, got %#v", choice)
	}
}

// A request that declares no tools at all is a turn the client meant to run
// without them: "none" is then the truth, and it also keeps the model off the
// stub. The stub itself is still needed, or upstream refuses the history.
func TestNormalizeSetsNoToolChoiceOnlyWithoutDeclaredTools(t *testing.T) {
	payload := map[string]any{
		"model":    "claude-opus-5-5",
		"messages": historyWithUndeclaredTool(),
	}
	normalizeClaudePassThroughPayload(payload)

	tools, ok := payload["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected a stub declaration for the undeclared history tool, got %#v", payload["tools"])
	}
	if got := stringValue(tools[0].(map[string]any)["name"]); got != "LookupThing" {
		t.Fatalf("expected the stub named LookupThing, got %q", got)
	}
	choice, ok := payload["tool_choice"].(map[string]any)
	if !ok || stringValue(choice["type"]) != "none" {
		t.Fatalf("expected tool_choice none for a turn with no tools, got %#v", payload["tool_choice"])
	}
}

// A tool_choice the client really sent still wins over anything the normalize
// step would decide.
func TestNormalizeKeepsTheClientToolChoice(t *testing.T) {
	payload := map[string]any{
		"model":       "claude-opus-5-5",
		"messages":    historyWithUndeclaredTool(),
		"tool_choice": map[string]any{"type": "any"},
		"tools": []any{
			map[string]any{"name": "Bash", "input_schema": map[string]any{"type": "object"}},
		},
	}
	normalizeClaudePassThroughPayload(payload)

	choice, ok := payload["tool_choice"].(map[string]any)
	if !ok || stringValue(choice["type"]) != "any" {
		t.Fatalf("expected the client's own tool_choice kept, got %#v", payload["tool_choice"])
	}
}

// The history itself is left intact: the tool_use the model made and the result
// the client returned still pair up, so the turn stays continuable.
func TestNormalizeKeepsToolPairingWithAStub(t *testing.T) {
	payload := map[string]any{
		"model":    "claude-opus-5-5",
		"messages": historyWithUndeclaredTool(),
		"tools": []any{
			map[string]any{"name": "Bash", "input_schema": map[string]any{"type": "object"}},
		},
	}
	normalizeClaudePassThroughPayload(payload)

	messages := payload["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("expected the conversation kept whole, got %d messages", len(messages))
	}
	use := messages[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	result := messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if stringValue(use["id"]) != stringValue(result["tool_use_id"]) {
		t.Fatalf("tool_use id %q and tool_result id %q no longer pair",
			stringValue(use["id"]), stringValue(result["tool_use_id"]))
	}
}
