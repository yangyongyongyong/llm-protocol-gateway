package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func thinkingBindProvider() domain.Provider {
	return domain.Provider{
		ID:       "claude-sub",
		AuthType: domain.AuthTypeClaudeOAuth,
		ClaudeOAuth: &domain.ClaudeOAuthCredential{
			AccessToken:  "at",
			AccountLabel: "840140851gq@gmail.com",
		},
	}
}

// boundHistory is a conversation whose first assistant turn thought on another
// account: message 1 is the one a signature refusal names.
func boundHistory() []any {
	return []any{
		map[string]any{"role": "user", "content": "go"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "stale", "signature": "foreign-sig"},
			map[string]any{"type": "text", "text": "working on it"},
		}},
		map[string]any{"role": "user", "content": "continue"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "later", "signature": "later-sig"},
			map[string]any{"type": "text", "text": "done"},
		}},
	}
}

func messagesOf(t *testing.T, req map[string]any) []any {
	t.Helper()
	messages, ok := req["messages"].([]any)
	if !ok {
		t.Fatalf("messages missing: %#v", req)
	}
	return messages
}

func blockTypesOf(message any) []string {
	entry, _ := message.(map[string]any)
	var out []string
	for _, raw := range asMapSlice(entry["content"]) {
		block, _ := raw.(map[string]any)
		out = append(out, stringValue(block["type"]))
	}
	return out
}

// The block the refusal names and every thinking block after it go; the text
// around them stays. This is what the refusal itself asks for.
func TestStripBoundClaudeThinkingCutsFromTheNamedBlock(t *testing.T) {
	resetClaudeRefusedThinking()
	req := map[string]any{"messages": boundHistory()}
	errBody := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content.0: Invalid ` + "`signature`" + ` in ` + "`thinking`" + ` block. The block is bound to a different conversation."}}`)

	if !stripBoundClaudeThinking(req, errBody, "owner") {
		t.Fatal("expected the refusal to cut the history")
	}
	messages := messagesOf(t, req)
	if len(messages) != 4 {
		t.Fatalf("expected every message kept (only blocks go), got %d", len(messages))
	}
	if got := blockTypesOf(messages[1]); len(got) != 1 || got[0] != "text" {
		t.Fatalf("expected only the text left in message 1, got %#v", got)
	}
	if got := blockTypesOf(messages[3]); len(got) != 1 || got[0] != "text" {
		t.Fatalf("expected the later thinking cut too, got %#v", got)
	}
}

// Remembered signatures are cut on the way in, so the next turn does not pay the
// refusal again.
func TestStripRefusedClaudeThinkingAppliesOnTheNextTurn(t *testing.T) {
	resetClaudeRefusedThinking()
	provider := thinkingBindProvider()
	owner := claudeProviderThinkingOwner(provider)
	req := map[string]any{"messages": boundHistory()}
	errBody := []byte(`messages.1.content.0: Invalid signature in thinking block`)

	if !stripBoundClaudeThinking(req, errBody, owner) {
		t.Fatal("expected the first refusal to cut the history")
	}
	if !claudeThinkingRefused(owner, claudeThinkingKey(map[string]any{"type": "thinking", "signature": "foreign-sig"})) {
		t.Fatal("expected the refused signature remembered")
	}
	if claudeThinkingRefused(owner, claudeThinkingKey(map[string]any{"type": "thinking", "signature": "other-sig"})) {
		t.Fatal("an unrefused signature must not be remembered")
	}

	// A fresh conversation replaying the same signed blocks goes without them
	// from the first try, in one shot.
	body, err := json.Marshal(map[string]any{"messages": boundHistory()})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	stripped := stripRefusedClaudeThinkingBody(body, provider)
	if strings.Contains(string(stripped), "foreign-sig") && strings.Contains(string(stripped), "later-sig") {
		t.Fatalf("expected the refused signatures gone before sending, got %s", stripped)
	}
}

// An account is part of the refusal identity: a block refused by one account is
// not refused by another until that one says so.
func TestRefusedThinkingIsPerAccount(t *testing.T) {
	resetClaudeRefusedThinking()
	first := thinkingBindProvider()
	second := thinkingBindProvider()
	second.ClaudeOAuth = &domain.ClaudeOAuthCredential{AccessToken: "at2", AccountLabel: "other@example.com"}

	req := map[string]any{"messages": boundHistory()}
	if !stripBoundClaudeThinking(req, []byte(`messages.1.content.0: Invalid signature`), claudeProviderThinkingOwner(first)) {
		t.Fatal("expected the refusal to be recorded")
	}
	body, err := json.Marshal(map[string]any{"messages": boundHistory()})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := stripRefusedClaudeThinkingBody(body, second); string(got) != string(body) {
		t.Fatalf("another account must not inherit the refusal, got %s", got)
	}
}

// A refusal that names no block cannot be cut at a position, so — as magpie does
// ("from the block named, or from the start when it names none") — every thinking
// block goes. The turn still asks again instead of failing.
func TestStripBoundClaudeThinkingWithoutPositionCutsEveryThinking(t *testing.T) {
	resetClaudeRefusedThinking()
	req := map[string]any{"messages": boundHistory()}
	if !stripBoundClaudeThinking(req, []byte(`missing field signature`), "owner") {
		t.Fatal("expected a refusal without a position to cut every thinking block")
	}
	thinkingBefore, redactedBefore := countClaudeThinkingBlocks(req["messages"])
	if thinkingBefore != 0 || redactedBefore != 0 {
		t.Fatalf("expected no thinking left, got %d/%d", thinkingBefore, redactedBefore)
	}
}

// rectifyClaudeThinkingBoundBody keeps the whole message list when only blocks
// go, and reports what it removed.
func TestRectifyClaudeThinkingBoundBodyReportsRemovals(t *testing.T) {
	resetClaudeRefusedThinking()
	provider := thinkingBindProvider()
	body, err := json.Marshal(map[string]any{"messages": boundHistory()})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rectified, result, ok := rectifyClaudeThinkingBoundBody(body, []byte(`messages.1.content.0: Invalid signature in thinking block`), provider)
	if !ok {
		t.Fatal("expected the bound rectifier to handle the refusal")
	}
	if !result.applied {
		t.Fatal("expected the result to report it applied")
	}
	if result.removedThinkingBlocks != 2 {
		t.Fatalf("expected 2 thinking blocks cut, got %d", result.removedThinkingBlocks)
	}
	if strings.Contains(string(rectified), "foreign-sig") {
		t.Fatalf("expected the refused signature gone, got %s", rectified)
	}
}

// An assistant message that held nothing but thinking goes whole, and the two
// user turns it stood between are merged back (Anthropic refuses them apart).
func TestStripBoundClaudeThinkingMergesTheGap(t *testing.T) {
	resetClaudeRefusedThinking()
	req := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "first"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "only", "signature": "sig-only"},
		}},
		map[string]any{"role": "user", "content": "second"},
	}}
	if !stripBoundClaudeThinking(req, []byte(`messages.1.content.0: Invalid signature`), "owner") {
		t.Fatal("expected the thinking-only turn to be cut")
	}
	messages := messagesOf(t, req)
	if len(messages) != 1 {
		t.Fatalf("expected the empty turn dropped and the user turns merged, got %#v", messages)
	}
	if role := stringValue(messages[0].(map[string]any)["role"]); role != "user" {
		t.Fatalf("expected one user message left, got %q", role)
	}
}
