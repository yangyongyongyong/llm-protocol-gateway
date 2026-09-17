package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// OpenAI prompt caching on the codex backend routes cache shards by session
// signals (session_id header / prompt_cache_key). Without them, consecutive
// turns of one conversation land on different shards and only a short,
// content-independent prefix (~15.6k tokens in the field) ever hits.
//
// sub2api / cc-switch both derive a stable per-conversation key. We follow the
// same precedence:
//  1. X-Claude-Code-Session-Id request header
//  2. metadata.user_id "...session_<id>" suffix (Claude Code)
//  3. cache_control anchor hashes (system + first anchored user block)
//
// Never inject a random value: an unstable key is worse than none.

const chatgptPromptCacheKeyHeader = "X-Claude-Code-Session-Id"

// deriveChatGPTPromptCacheKey returns "" when no stable session signal exists.
func deriveChatGPTPromptCacheKey(r *http.Request, claudeReq map[string]any) string {
	if r != nil {
		if sid := strings.TrimSpace(r.Header.Get(chatgptPromptCacheKeyHeader)); sid != "" {
			return "session-" + sid
		}
	}
	if metadata, ok := claudeReq["metadata"].(map[string]any); ok {
		if userID := strings.TrimSpace(stringValue(metadata["user_id"])); userID != "" {
			if idx := strings.LastIndex(userID, "_session_"); idx >= 0 && idx+len("_session_") < len(userID) {
				return "session-" + userID[idx+len("_session_"):]
			}
		}
	}
	if key := deriveCacheControlAnchorKey(claudeReq); key != "" {
		return key
	}
	return ""
}

// deriveCacheControlAnchorKey hashes the text of cache_control-bearing blocks:
// same conversation keeps the same anchors across turns, so the key is stable
// while still distinguishing different conversations.
func deriveCacheControlAnchorKey(claudeReq map[string]any) string {
	parts := make([]string, 0, 4)
	if system, ok := claudeReq["system"].([]any); ok {
		for _, raw := range system {
			block, ok := raw.(map[string]any)
			if !ok || block["cache_control"] == nil {
				continue
			}
			if text := strings.TrimSpace(stringValue(block["text"])); text != "" {
				parts = append(parts, "system:"+text)
			}
		}
	}
	if messages, ok := claudeReq["messages"].([]any); ok {
		for _, raw := range messages {
			msg, ok := raw.(map[string]any)
			if !ok || stringValue(msg["role"]) != "user" {
				continue
			}
			blocks, ok := msg["content"].([]any)
			if !ok {
				continue
			}
			for _, rawBlock := range blocks {
				block, ok := rawBlock.(map[string]any)
				if !ok || block["cache_control"] == nil {
					continue
				}
				if text := strings.TrimSpace(stringValue(block["text"])); text != "" {
					parts = append(parts, "user_anchor:"+text)
				}
			}
			if len(parts) > 0 && strings.HasPrefix(parts[len(parts)-1], "user_anchor:") {
				break // only the first anchored user message matters
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte("anchor:" + strings.Join(parts, "\n")))
	return "anthropic-cache-" + hex.EncodeToString(sum[:16])
}

// chatgptSessionIDForCache isolates the derived key per ChatGPT account so a
// failover to another provider/account cannot collide two upstream sessions
// (sub2api's isolateOpenAIUpstreamSessionID semantics).
func chatgptSessionIDForCache(apiKeyID, chatgptAccountID, promptCacheKey string) string {
	seed := strings.Join([]string{"k:" + apiKeyID, "a:" + chatgptAccountID, "c:" + promptCacheKey}, "|")
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:16])
}

// --- digest-chain fallback ---------------------------------------------------
//
// When the client sends neither a session header nor cache_control anchors
// (the field case behind the 15616 stuck-cache report), bind the request's
// message digest chain to a generated key: the next turn's history is a
// prefix-extension of this chain, so the binding reuses the same key and the
// upstream cache keeps growing turn over turn.

type chatgptDigestSession struct {
	key       string
	expiresAt time.Time
}

type chatgptDigestSessionStore struct {
	mu       sync.Mutex
	sessions map[string]chatgptDigestSession
	ttl      time.Duration
}

func newChatGPTDigestSessionStore() *chatgptDigestSessionStore {
	return &chatgptDigestSessionStore{
		sessions: map[string]chatgptDigestSession{},
		ttl:      10 * time.Minute,
	}
}

// bindOrReuse returns the key bound to a chain that the current chain extends
// (prefix match), or binds a fresh key. chain is an ordered list of per-message
// digests, oldest first.
func (s *chatgptDigestSessionStore) bindOrReuse(chain []string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	// Reap expired entries opportunistically.
	for id, session := range s.sessions {
		if now.After(session.expiresAt) {
			delete(s.sessions, id)
		}
	}
	// Longest-prefix match: compare from the full chain down to a minimum
	// length so the newest turn's additions don't break continuity.
	for take := len(chain); take >= 2; take-- {
		id := strings.Join(chain[:take], "|")
		if session, ok := s.sessions[id]; ok && now.Before(session.expiresAt) {
			session.expiresAt = now.Add(s.ttl)
			s.sessions[id] = session
			return session.key
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(chain, "|")))
	key := "anthropic-digest-" + hex.EncodeToString(sum[:16])
	s.sessions[strings.Join(chain, "|")] = chatgptDigestSession{key: key, expiresAt: now.Add(s.ttl)}
	return key
}

// buildClaudeMessageDigestChain digests every message (role + content text) so
// equal history prefix maps to equal chain prefix.
func buildClaudeMessageDigestChain(claudeReq map[string]any) []string {
	messages, ok := claudeReq["messages"].([]any)
	if !ok || len(messages) < 2 {
		return nil
	}
	chain := make([]string, 0, len(messages))
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := strings.TrimSpace(stringValue(msg["role"]))
		var text strings.Builder
		switch typed := msg["content"].(type) {
		case string:
			text.WriteString(typed)
		case []any:
			for _, rawBlock := range typed {
				block, ok := rawBlock.(map[string]any)
				if !ok {
					continue
				}
				switch stringValue(block["type"]) {
				case "text", "tool_result":
					text.WriteString(stringValue(block["text"]))
				case "tool_use":
					text.WriteString(stringValue(block["name"]))
				}
			}
		}
		sum := sha256.Sum256([]byte(role + ":" + text.String()))
		chain = append(chain, hex.EncodeToString(sum[:8]))
	}
	return chain
}

// extractPromptCacheKeyFromBody reads prompt_cache_key from a marshalled
// Responses request body without mutating it ("" when absent/unparsable).
func extractPromptCacheKeyFromBody(body []byte) string {
	if len(body) == 0 || !strings.Contains(string(body), "prompt_cache_key") {
		return ""
	}
	var payload struct {
		PromptCacheKey string `json:"prompt_cache_key"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.PromptCacheKey)
}

// promptCacheKeyForClaudeRequest is the single entry point: explicit signals
// first, digest-chain continuity as the fallback.
func (s *Server) promptCacheKeyForClaudeRequest(r *http.Request, claudeReq map[string]any) string {
	if key := deriveChatGPTPromptCacheKey(r, claudeReq); key != "" {
		return key
	}
	if s.chatgptDigestSessions == nil {
		s.chatgptDigestSessions = newChatGPTDigestSessionStore()
	}
	if chain := buildClaudeMessageDigestChain(claudeReq); len(chain) >= 2 {
		return s.chatgptDigestSessions.bindOrReuse(chain)
	}
	return ""
}
