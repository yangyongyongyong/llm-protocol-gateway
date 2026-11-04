package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

const deepSeekUUID = "3f2a1c44-8b9e-4d17-9a06-1c2d3e4f5a6b"

// A thinking block another vendor signed is refused whole by Anthropic, so it is
// dropped on the way in. Claude's own base64 signature is kept: it is what the
// next turn replays.
func TestStripForeignClaudeThinkingDropsUUIDSignatures(t *testing.T) {
	resetClaudeRefusedThinking()
	req := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "go"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "deepseek thought", "signature": deepSeekUUID},
			map[string]any{"type": "text", "text": "answered"},
		}},
		map[string]any{"role": "user", "content": "continue"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "claude thought", "signature": strings.Repeat("Ab3=", 200)},
			map[string]any{"type": "text", "text": "answered again"},
		}},
	}}
	if !stripForeignClaudeThinking(req) {
		t.Fatal("expected the foreign signature to be dropped")
	}
	messages := messagesOf(t, req)
	if got := blockTypesOf(messages[1]); len(got) != 1 || got[0] != "text" {
		t.Fatalf("expected the foreign thinking dropped, got %#v", got)
	}
	if got := blockTypesOf(messages[3]); len(got) != 2 {
		t.Fatalf("expected Claude's own thinking kept, got %#v", got)
	}
}

// Only Anthropic's endpoint refuses a signature it did not write: another
// vendor's block is its own to read back, so the body goes as the client wrote
// it.
func TestStripRefusedClaudeThinkingBodyLeavesOtherUpstreamsAlone(t *testing.T) {
	resetClaudeRefusedThinking()
	body, err := json.Marshal(map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "go"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "x", "signature": deepSeekUUID},
		}},
	}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	relay := domain.Provider{ID: "relay", AuthType: "claude", BaseURL: "https://api.deepseek.com/anthropic"}
	if got := stripRefusedClaudeThinkingBody(body, relay); string(got) != string(body) {
		t.Fatalf("a DeepSeek endpoint must keep its own blocks, got %s", got)
	}
	anthropic := domain.Provider{ID: "sub", AuthType: domain.AuthTypeClaudeOAuth}
	if got := stripRefusedClaudeThinkingBody(body, anthropic); strings.Contains(string(got), deepSeekUUID) {
		t.Fatalf("an Anthropic endpoint must not receive the foreign block, got %s", got)
	}
}

// anthropicThinkingUpstream is what decides it.
func TestAnthropicThinkingUpstream(t *testing.T) {
	cases := []struct {
		provider domain.Provider
		want     bool
	}{
		{domain.Provider{ID: "a", AuthType: domain.AuthTypeClaudeOAuth}, true},
		{domain.Provider{ID: "b", AuthType: "claude", BaseURL: "https://api.anthropic.com/v1"}, true},
		{domain.Provider{ID: "c", AuthType: "claude", BaseURL: ""}, true},
		{domain.Provider{ID: "d", AuthType: "claude", BaseURL: "https://relay.example.com/anthropic"}, false},
		{domain.Provider{ID: "e", AuthType: "claude", BaseURL: "https://api.deepseek.com"}, false},
	}
	for _, tc := range cases {
		if got := anthropicThinkingUpstream(tc.provider); got != tc.want {
			t.Fatalf("%s: anthropicThinkingUpstream = %t, want %t", tc.provider.ID, got, tc.want)
		}
	}
}
