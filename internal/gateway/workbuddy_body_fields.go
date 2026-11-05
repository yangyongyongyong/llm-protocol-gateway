package gateway

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
)

// The per-round fields the desktop client writes into the chat body.
//
// Headers alone are not the whole story. The official client puts "which round
// is this" in **both** the head and the body (captures of CN 5.8.3 / intl 5.8.4,
// see misakano7545/workbuddy2api-panel internal/upstream/conv_fields.go):
//
//	{"role":"user", "content":[…], "agent":"cli",
//	 "conversationRequestId":"<this round's X-Conversation-Request-ID>",
//	 "startsNewUserRequest":true}
//
// Only writing it in the header — which this gateway did — leaves head and body
// disagreeing, and that inconsistency is itself a detectable shape.
//
// The boundary is honesty: only the **current** round's last user message gets
// an id. History messages have no round id the client ever told us about (the
// OpenAI protocol has no such field), so nothing is invented for them — a
// mismatched id is easier to correlate than a missing one.
//
// Two more body fields ride along in the same pass, because re-serialising the
// body once is cheaper and keeps the three consistent:
//
//   - prompt_cache_key — the upstream honours it, and leaving it out loses the
//     prefix cache entirely (measured on an 8k-token prefix: 0 cache-hit tokens
//     and ~0.34 credit without it, 7808 hit tokens and ~0.02 credit with it,
//     roughly 17x).
//   - stream_options.include_usage — the official CLI always sends it, and the
//     upstream reports usage in the final frame because of it.

// workBuddyBodyFieldOptions carries what the body fields need to know.
type workBuddyBodyFieldOptions struct {
	userID                string
	conversationID        string
	conversationRequestID string
}

// workBuddyAgentCLI is the agent segment every outbound message carries; both
// builds send "cli".
const workBuddyAgentCLI = "cli"

// applyWorkBuddyBodyFields writes the round fields, the cache key and the usage
// option into an already-shaped body, in one decode/encode pass. A body that is
// not a chat request, or that will not parse, comes back untouched — the same
// discipline shapeWorkBuddyChatBody follows: never turn a bad body into a second
// error.
func applyWorkBuddyBodyFields(body []byte, options workBuddyBodyFieldOptions) []byte {
	if len(body) == 0 {
		return body
	}
	var payload map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil || payload == nil {
		return body
	}
	messages, hasMessages := payload["messages"].([]any)
	if !hasMessages {
		return body
	}
	changed := false
	if injectWorkBuddyConversationFields(payload, messages, options.conversationRequestID) {
		changed = true
	}
	if injectWorkBuddyPromptCacheKey(payload, options.userID, options.conversationID) {
		changed = true
	}
	if injectWorkBuddyStreamOptions(payload) {
		changed = true
	}
	if !changed {
		return body
	}
	encoded := encodeJSONWithoutEscaping(payload)
	if encoded == nil {
		return body
	}
	return encoded
}

// injectWorkBuddyConversationFields tags the current round on the last user
// message and reports whether it changed anything.
//
// A client that already set conversationRequestId wins: it knows which round it
// means, and this gateway does not overwrite it.
func injectWorkBuddyConversationFields(payload map[string]any, messages []any, conversationRequestID string) bool {
	if strings.TrimSpace(conversationRequestID) == "" {
		return false
	}
	// The last user message is where the current round starts
	// (startsNewUserRequest semantics).
	index := -1
	for position := len(messages) - 1; position >= 0; position-- {
		message, isMessage := messages[position].(map[string]any)
		if !isMessage {
			continue
		}
		if role, _ := message["role"].(string); strings.EqualFold(role, "user") {
			index = position
			break
		}
	}
	if index < 0 {
		return false
	}
	message, isMessage := messages[index].(map[string]any)
	if !isMessage {
		return false
	}
	if existing, _ := message["conversationRequestId"].(string); strings.TrimSpace(existing) != "" {
		return false
	}
	message["conversationRequestId"] = strings.TrimSpace(conversationRequestID)
	message["startsNewUserRequest"] = true
	if agent, _ := message["agent"].(string); strings.TrimSpace(agent) == "" {
		message["agent"] = workBuddyAgentCLI
	}
	messages[index] = message
	payload["messages"] = messages
	return true
}

// injectWorkBuddyPromptCacheKey sets prompt_cache_key, reporting whether it did.
//
// A key the client already carries is never overwritten: that client knows which
// prefix slot it wants. Otherwise the key is derived from the account and the
// conversation, which is what makes consecutive turns of one conversation hit
// the same upstream prefix cache while keeping different accounts apart.
func injectWorkBuddyPromptCacheKey(payload map[string]any, userID, conversationID string) bool {
	if existing, _ := payload["prompt_cache_key"].(string); strings.TrimSpace(existing) != "" {
		return false
	}
	conversation := strings.TrimSpace(conversationID)
	if fromBody := workBuddyStringField(payload, "conversation_id"); fromBody != "" {
		conversation = fromBody
	} else if fromBody := workBuddyStringField(payload, "conversationId"); fromBody != "" {
		conversation = fromBody
	}
	payload["prompt_cache_key"] = workBuddyCacheKey(userID, conversation)
	return true
}

// injectWorkBuddyStreamOptions adds include_usage when the client set no
// stream_options of its own, so the final frame carries usage.
func injectWorkBuddyStreamOptions(payload map[string]any) bool {
	if _, has := payload["stream_options"]; has {
		return false
	}
	payload["stream_options"] = map[string]any{"include_usage": true}
	return true
}

// workBuddyStringField reads a trimmed string field, or "".
func workBuddyStringField(payload map[string]any, key string) string {
	value, ok := payload[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

// workBuddyCacheKey derives the prefix-cache slot for an account and
// conversation, in the shape the client's own identifiers take: a 36-character
// hyphenated UUID, version 4.
//
// Account isolation is a hash input, never a literal segment. A key shared
// between accounts would make one account's request hit another's cached prefix
// — the cache is keyed upstream and the hit leaks the other conversation. The
// earlier shape of this idea (`wb2a-<uid8>-<hash>`, 48 characters, project
// abbreviation, the account id in clear text) was itself a fingerprint; that is
// why the value is derived and shaped like the client's rather than readable.
func workBuddyCacheKey(userID, conversationID string) string {
	sum := sha256.Sum256([]byte("workbuddy-cache\x00" + userID + "|" + conversationID))
	var uuid [16]byte
	copy(uuid[:], sum[:16])
	uuid[6] = (uuid[6] & 0x0f) | 0x40 // version 4
	uuid[8] = (uuid[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}

// workBuddyTimeOrderedUUID derives a hyphenated UUIDv7-shaped id whose embedded
// time is plausible.
//
// Why the time bits have to be faked rather than random: the client's
// conversation ids decode to the moment the conversation started. Taking the 48
// time bits straight from a hash yields dates scattered across centuries —
// 1974, 5000 — which is a shape no real capture shows and is easy to filter for.
// The bits are therefore anchored at 2026-01-01 and spread over a 24-month
// window, so a value decodes to a believable past instant while the same seed
// always yields the same id (a conversation's id must not change per turn, or
// its prefix cache slot is destroyed every request).
func workBuddyTimeOrderedUUID(seed string) string {
	sum := sha256.Sum256([]byte("workbuddy:" + seed))
	const (
		anchorMS = int64(1767225600000)                 // 2026-01-01T00:00:00Z
		windowMS = int64(24 * 30 * 24 * 60 * 60 * 1000) // ≈24 months
	)
	ms := uint64(anchorMS + int64(binary.BigEndian.Uint64(sum[8:16])%uint64(windowMS)))
	var uuid [16]byte
	uuid[0] = byte(ms >> 40)
	uuid[1] = byte(ms >> 32)
	uuid[2] = byte(ms >> 24)
	uuid[3] = byte(ms >> 16)
	uuid[4] = byte(ms >> 8)
	uuid[5] = byte(ms)
	copy(uuid[6:], sum[:10])
	uuid[6] = (uuid[6] & 0x0f) | 0x70 // version 7
	uuid[8] = (uuid[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}
