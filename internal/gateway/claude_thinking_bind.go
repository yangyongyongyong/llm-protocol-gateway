package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Claude's thinking block is signed for one conversation and one account, so a
// block signed elsewhere is refused with the whole request (magpie
// thinking_bound.go, checked against api.anthropic.com):
//
//	400 messages.3.content.20: Invalid signature in thinking block.
//	    The block is bound to a different conversation.
//
// That is not a corner case on a gateway with a fallback: the same conversation
// moving account to account (a provider cooling down, a subscription switching
// to its backup) replays a history whose thinking blocks the next account never
// signed. This gateway answered it by stripping every thinking block in the
// request, which repairs the turn at the price of the whole conversation's
// reasoning, and then paid the same 400 again on the next turn, and on every
// turn after it, because nothing recorded what had already been refused.
//
// The two pieces magpie uses are added here:
//
//   - refusal location: the block the 400 names, and every thinking block after
//     it, is what goes (their signatures cover the turns before them, the
//     refused block among them).
//   - refusal memory: those signatures are remembered for the upstream, so the
//     conversation's later turns go without them on the first try.

// claudeBoundThinkingAt is where the refusal says the block is:
// messages.<m>.content.<b>.
var claudeBoundThinkingAt = regexp.MustCompile(`messages\.(\d+)\.content\.(\d+)`)

// claudeThinkingRefusedMost bounds how many signatures are remembered; past it
// the memory starts again, and a conversation still going is mended by one more
// refusal.
const claudeThinkingRefusedMost = 1 << 14

// claudeRefusedThinking is the signatures an upstream refused, per provider and
// account. Package-level rather than on Server so that the converter paths,
// which do not always hold a *Server, share the same memory.
var claudeRefusedThinking = struct {
	mu sync.Mutex
	m  map[string]struct{}
}{m: map[string]struct{}{}}

// claudeThinkingOwner is who a refusal is remembered for: the provider, and the
// account signed in to it when the credential names one.
func claudeThinkingOwner(providerID string, accountLabel string) string {
	if accountLabel = strings.TrimSpace(accountLabel); accountLabel != "" {
		return providerID + "\x00" + accountLabel
	}
	return providerID
}

// claudeThinkingKey names a thinking or redacted_thinking block by its signature
// (a redacted block's data), or its text where it has neither.
func claudeThinkingKey(block map[string]any) string {
	key := stringValue(block["signature"])
	if key == "" {
		key = stringValue(block["data"])
	}
	if key == "" {
		key = stringValue(block["thinking"])
	}
	sum := sha256.Sum256([]byte(stringValue(block["type"]) + "\x00" + key))
	return hex.EncodeToString(sum[:16])
}

func isClaudeThinkingBlock(block map[string]any) bool {
	switch stringValue(block["type"]) {
	case "thinking", "redacted_thinking":
		return true
	}
	return false
}

func claudeThinkingRefusedKey(owner, key string) string { return owner + "\x00" + key }

// claudeThinkingRefused reports whether owner refused the block with key.
func claudeThinkingRefused(owner, key string) bool {
	claudeRefusedThinking.mu.Lock()
	defer claudeRefusedThinking.mu.Unlock()
	_, ok := claudeRefusedThinking.m[claudeThinkingRefusedKey(owner, key)]
	return ok
}

// refuseClaudeThinking remembers that owner refused the blocks with keys.
func refuseClaudeThinking(owner string, keys []string) {
	if len(keys) == 0 {
		return
	}
	claudeRefusedThinking.mu.Lock()
	defer claudeRefusedThinking.mu.Unlock()
	if len(claudeRefusedThinking.m)+len(keys) > claudeThinkingRefusedMost {
		claudeRefusedThinking.m = map[string]struct{}{}
	}
	for _, key := range keys {
		claudeRefusedThinking.m[claudeThinkingRefusedKey(owner, key)] = struct{}{}
	}
}

// resetClaudeRefusedThinking forgets every refusal. Tests that share the process
// memory call it between cases.
func resetClaudeRefusedThinking() {
	claudeRefusedThinking.mu.Lock()
	defer claudeRefusedThinking.mu.Unlock()
	claudeRefusedThinking.m = map[string]struct{}{}
}

// dropClaudeThinkingBlocks removes the thinking blocks drop names, and any
// assistant message left with nothing, then merges the same-role messages that
// removal left adjacent (Anthropic refuses two of them sent apart). It reports
// whether anything changed.
func dropClaudeThinkingBlocks(req map[string]any, drop func(block map[string]any) bool) bool {
	messages, ok := req["messages"].([]any)
	if !ok {
		return false
	}
	changed := false
	droppedAny := false
	out := make([]any, 0, len(messages))
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			out = append(out, raw)
			continue
		}
		blocks, ok := message["content"].([]any)
		if !ok {
			out = append(out, raw)
			continue
		}
		kept := make([]any, 0, len(blocks))
		gone := false
		for _, blockItem := range blocks {
			block, ok := blockItem.(map[string]any)
			if ok && isClaudeThinkingBlock(block) && drop(block) {
				gone = true
				continue
			}
			kept = append(kept, blockItem)
		}
		if !gone {
			out = append(out, raw)
			continue
		}
		changed = true
		if len(kept) == 0 {
			droppedAny = true
			continue
		}
		trimmed := make(map[string]any, len(message))
		for key, value := range message {
			trimmed[key] = value
		}
		trimmed["content"] = kept
		out = append(out, trimmed)
	}
	if !changed {
		return false
	}
	if droppedAny {
		out = mergeAdjacentClaudePassThroughMessages(out)
	}
	req["messages"] = out
	return true
}

// stripRefusedClaudeThinking drops the thinking blocks this upstream refused
// before, so the conversation's later turns do not pay the refusal again.
func stripRefusedClaudeThinking(req map[string]any, owner string) bool {
	claudeRefusedThinking.mu.Lock()
	none := len(claudeRefusedThinking.m) == 0
	claudeRefusedThinking.mu.Unlock()
	if none {
		return false
	}
	return dropClaudeThinkingBlocks(req, func(block map[string]any) bool {
		return claudeThinkingRefused(owner, claudeThinkingKey(block))
	})
}

// claudeProviderThinkingOwner is the refusal identity of provider: its ID, plus
// the account signed in to it when the credential names one. A block signed on
// one account is exactly what the next account refuses, so the account is part
// of the key.
func claudeProviderThinkingOwner(provider domain.Provider) string {
	label := ""
	if provider.ClaudeOAuth != nil {
		label = provider.ClaudeOAuth.AccountLabel
	}
	return claudeThinkingOwner(provider.ID, label)
}

// claudeForeignThinkingSignature matches a thinking signature another vendor's
// Messages endpoint wrote: DeepSeek's is its reply id, a UUID. Claude's own are
// base64 and long — 800 characters and more for Claude Code's — never a UUID, so
// a UUID signature is proof the block was signed elsewhere, and Anthropic refuses
// each one with the whole request.
//
// A conversation that ran on a DeepSeek-compatible endpoint first (a routing
// group, a fallback that has since moved back) carries such blocks in every later
// turn, and each turn pays a 400 to learn what the signature already says.
var claudeForeignThinkingSignature = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// anthropicThinkingUpstream reports a provider whose Messages endpoint is
// Anthropic's own, or a relay of it: the one that refuses a signature it did not
// write. Every other upstream gets the body as the client wrote it, since a block
// that vendor signed is its own to read back.
func anthropicThinkingUpstream(provider domain.Provider) bool {
	if provider.AuthType == domain.AuthTypeClaudeOAuth {
		return true
	}
	base := strings.ToLower(strings.TrimSpace(provider.BaseURL))
	return base == "" || strings.Contains(base, "anthropic.com")
}

// stripForeignClaudeThinking drops every thinking block another vendor signed, and
// the assistant messages that had only those.
func stripForeignClaudeThinking(req map[string]any) bool {
	return dropClaudeThinkingBlocks(req, func(block map[string]any) bool {
		if stringValue(block["type"]) != "thinking" {
			// A redacted_thinking's data is opaque ciphertext: another vendor's
			// is not identifiable as such, and dropping a real one would lose
			// what a later turn replays.
			return false
		}
		return claudeForeignThinkingSignature.MatchString(strings.TrimSpace(stringValue(block["signature"])))
	})
}

// stripRefusedClaudeThinkingBody is a Messages body prepared for provider: the
// thinking blocks it refused before are gone, and so are the blocks another vendor
// signed. A conversation that moved accounts or providers pays that once, on the
// way in, instead of once per turn on the way back.
func stripRefusedClaudeThinkingBody(body []byte, provider domain.Provider) []byte {
	if len(body) == 0 || !strings.Contains(string(body), `"thinking"`) {
		return body
	}
	var req map[string]any
	if json.Unmarshal(body, &req) != nil {
		return body
	}
	changed := false
	if anthropicThinkingUpstream(provider) && stripForeignClaudeThinking(req) {
		changed = true
	}
	if stripRefusedClaudeThinking(req, claudeProviderThinkingOwner(provider)) {
		changed = true
	}
	if !changed {
		return body
	}
	encoded, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return encoded
}

// countClaudeThinkingBlocks counts the thinking and redacted_thinking blocks in a
// message list, so a cut can report what it removed.
func countClaudeThinkingBlocks(messages any) (thinking, redacted int) {
	for _, raw := range asMapSlice(messages) {
		message, _ := raw.(map[string]any)
		for _, blockRaw := range asMapSlice(message["content"]) {
			block, ok := blockRaw.(map[string]any)
			if !ok || !isClaudeThinkingBlock(block) {
				continue
			}
			if stringValue(block["type"]) == "redacted_thinking" {
				redacted++
			} else {
				thinking++
			}
		}
	}
	return thinking, redacted
}

// rectifyClaudeThinkingBoundBody answers a signature refusal the way it asks: it
// leaves out the block it names and every thinking block after it, remembers
// those signatures for provider, and returns the body to send once more. It
// reports false when the refusal named nothing this can cut, so the caller can
// fall back to the whole-request rectifier.
func rectifyClaudeThinkingBoundBody(body, errBody []byte, provider domain.Provider) ([]byte, thinkingRectifyResult, bool) {
	var req map[string]any
	if json.Unmarshal(body, &req) != nil {
		return body, thinkingRectifyResult{}, false
	}
	thinkingBefore, redactedBefore := countClaudeThinkingBlocks(req["messages"])
	if !stripBoundClaudeThinking(req, errBody, claudeProviderThinkingOwner(provider)) {
		return body, thinkingRectifyResult{}, false
	}
	encoded, err := json.Marshal(req)
	if err != nil {
		return body, thinkingRectifyResult{}, false
	}
	thinkingAfter, redactedAfter := countClaudeThinkingBlocks(req["messages"])
	return encoded, thinkingRectifyResult{
		applied:                       true,
		removedThinkingBlocks:         thinkingBefore - thinkingAfter,
		removedRedactedThinkingBlocks: redactedBefore - redactedAfter,
	}, true
}

// stripBoundClaudeThinking does what a signature refusal asks: it leaves out the
// block the refusal names and every thinking block after it, drops the assistant
// messages that had only those, and remembers those signatures for owner so the
// next turn goes without them first time. It reports whether the request changed,
// i.e. whether asking again can go otherwise.
func stripBoundClaudeThinking(req map[string]any, errBody []byte, owner string) bool {
	if req == nil || len(errBody) == 0 {
		return false
	}
	messages, ok := req["messages"].([]any)
	if !ok || len(messages) == 0 {
		return false
	}
	// Where the refusal points. It names no position for a whole-request
	// complaint, and then every thinking block is suspect.
	fromMessage, fromBlock := 0, 0
	if at := claudeBoundThinkingAt.FindSubmatch(errBody); at != nil {
		m, err := strconv.Atoi(string(at[1]))
		if err != nil {
			m = 0
		}
		b, err := strconv.Atoi(string(at[2]))
		if err != nil {
			b = 0
		}
		if m < len(messages) {
			if blocks, ok := messages[m].(map[string]any)["content"].([]any); ok && b < len(blocks) {
				if block, ok := blocks[b].(map[string]any); ok && isClaudeThinkingBlock(block) {
					fromMessage, fromBlock = m, b
				}
			}
		}
	}

	// Everything from there on is refused, and so is anything already known to
	// be: the signatures of later blocks cover the refused one.
	var keys []string
	for mi := fromMessage; mi < len(messages); mi++ {
		message, ok := messages[mi].(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := message["content"].([]any)
		if !ok {
			continue
		}
		for bi, blockItem := range blocks {
			block, ok := blockItem.(map[string]any)
			if !ok || !isClaudeThinkingBlock(block) {
				continue
			}
			if mi == fromMessage && bi < fromBlock {
				continue
			}
			if key := claudeThinkingKey(block); !claudeThinkingRefused(owner, key) {
				keys = append(keys, key)
			}
		}
	}
	if len(keys) == 0 {
		return false
	}
	refuseClaudeThinking(owner, keys)

	// Cut at the refusal point: every thinking block from there to the end goes.
	changed := false
	droppedAny := false
	out := make([]any, 0, len(messages))
	for mi, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok || mi < fromMessage {
			out = append(out, raw)
			continue
		}
		blocks, ok := message["content"].([]any)
		if !ok {
			out = append(out, raw)
			continue
		}
		kept := make([]any, 0, len(blocks))
		gone := false
		for bi, blockItem := range blocks {
			block, ok := blockItem.(map[string]any)
			if !ok {
				kept = append(kept, blockItem)
				continue
			}
			refusedHere := mi > fromMessage || bi >= fromBlock
			if refusedHere && isClaudeThinkingBlock(block) {
				gone = true
				continue
			}
			kept = append(kept, blockItem)
		}
		if !gone {
			out = append(out, raw)
			continue
		}
		changed = true
		if len(kept) == 0 {
			droppedAny = true
			continue
		}
		trimmed := make(map[string]any, len(message))
		for key, value := range message {
			trimmed[key] = value
		}
		trimmed["content"] = kept
		out = append(out, trimmed)
	}
	if !changed {
		return false
	}
	if droppedAny {
		out = mergeAdjacentClaudePassThroughMessages(out)
	}
	req["messages"] = out
	return true
}
