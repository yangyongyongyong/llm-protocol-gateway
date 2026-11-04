package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	xxHash64 "github.com/pierrec/xxHash/xxHash64"
)

const (
	claudeOAuthCCHSeed               = uint64(0x6E52736AC806831E)
	claudeOAuthBillingVersion        = "2.1.303"
	claudeOAuthAgentIdentifier       = "You are Claude Code, Anthropic's official CLI for Claude."
	claudeOAuthBillingCCHPlaceholder = "00000"
)

var claudeOAuthBillingCCHPattern = regexp.MustCompile(`\bcch=([0-9a-f]{5});`)

// oauthToolRenameMap maps common third-party tool names to Claude Code TitleCase names.
var oauthToolRenameMap = map[string]string{
	"bash":         "Bash",
	"read":         "Read",
	"write":        "Write",
	"edit":         "Edit",
	"glob":         "Glob",
	"grep":         "Grep",
	"task":         "Task",
	"webfetch":     "WebFetch",
	"todowrite":    "TodoWrite",
	"question":     "Question",
	"skill":        "Skill",
	"ls":           "LS",
	"todoread":     "TodoRead",
	"notebookedit": "NotebookEdit",
	"taskcreate":   "TaskCreate",
	"taskget":      "TaskGet",
	"taskupdate":   "TaskUpdate",
	"tasklist":     "TaskList",
}

// anthropicServerToolNames are Anthropic built-in / server-tool *canonical names*
// that must never be OAuth-TitleCased. Same failure class as mcp__ preservation
// (9dc8e1d): the generic snake_case→TitleCase path turns web_search→WebSearch
// (and web_fetch→WebFetch, code_execution→CodeExecution), then tool_choice /
// history no longer matches tools[] and Anthropic returns
// "Tool 'WebSearch' not found in provided tools".
//
// Only list names that are unambiguously Anthropic server tools — do NOT add
// names that Claude Code / Codex also use as client tools and intentionally
// cloak (e.g. bash→Bash, tool_search→ToolSearch). Those stay protected via the
// request-scoped serverToolNames set when tools[].type is a server discriminator.
var anthropicServerToolNames = map[string]struct{}{
	"web_search":     {},
	"web_fetch":      {},
	"code_execution": {},
}

var oauthToolRenameReverseMap = func() map[string]string {
	out := make(map[string]string, len(oauthToolRenameMap))
	for original, renamed := range oauthToolRenameMap {
		out[renamed] = original
	}
	return out
}()

func isAnthropicServerToolName(name string) bool {
	_, ok := anthropicServerToolNames[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// applyClaudeOAuthCloaking rewrites OAuth upstream bodies so Anthropic attributes
// traffic to the Claude Code plan bucket instead of extra usage.
//
// The client's own system prompt is kept. This path used to replace the whole
// system with a canned Claude Code imitation and forward a three-line summary of
// the real one into the first user message, so a real client's system — its
// environment, its CLAUDE.md, the agent loop's "keep going until the task is
// done", its tool-use rules — never reached the model. A turn that has none of
// that answers a line, or only thinks and says nothing at all, which is exactly
// how a thinking-only empty turn is produced. Magpie's rule for the same
// upstream is that everything but the billing line goes as the client sent it,
// byte for byte (billing_line.go, withoutBillingLine); this follows it: the
// billing line is lifted out of the client's system and re-issued as the first
// block, the agent identifier goes before the client's own blocks so the OAuth
// upstream still recognises the caller, and every other block is kept verbatim.
func applyClaudeOAuthCloaking(payload map[string]any) {
	if payload == nil {
		return
	}
	if _, ok := payload["messages"]; !ok {
		return
	}
	if !claudeOAuthSystemAlreadyCloaked(payload) {
		payload["system"] = buildClaudeOAuthSystem(payload["system"])
	}
	remapClaudeOAuthToolNames(payload)
	ensureClaudeOAuthCacheControl(payload)
}

// claudeOAuthBillingLine matches a system block that is Claude Code's billing
// line and nothing else. Mirrors magpie's billing_line.go billingLine: the
// header names only key=value pairs, each ended by a semicolon. A block that
// merely mentions the header — a client's own rules may quote it — is not a
// billing line and is kept as the client wrote it.
var claudeOAuthBillingLine = regexp.MustCompile(`^\s*x-anthropic-billing-header:(\s*[A-Za-z_]+=[^;\s]*;)*\s*$`)

// claudeOAuthSystemAlreadyCloaked reports a system this function already built:
// the billing block first, the agent identifier second. Anything else is
// rebuilt, so a client whose own system happens to start with a mixed billing
// block never loses the rest of it.
func claudeOAuthSystemAlreadyCloaked(payload map[string]any) bool {
	blocks, ok := payload["system"].([]any)
	if !ok || len(blocks) < 2 {
		return false
	}
	first, ok := blocks[0].(map[string]any)
	if !ok || !claudeOAuthBillingLine.MatchString(stringValue(first["text"])) {
		return false
	}
	second, ok := blocks[1].(map[string]any)
	if !ok {
		return false
	}
	return stringValue(second["text"]) == claudeOAuthAgentIdentifier
}

// buildClaudeOAuthSystem is the system sent upstream: the billing block, the
// agent identifier, then the client's own blocks as it wrote them.
func buildClaudeOAuthSystem(system any) []any {
	blocks := make([]any, 0, 8)
	blocks = append(blocks, claudeOAuthBillingBlock())
	blocks = append(blocks, map[string]any{"type": "text", "text": claudeOAuthAgentIdentifier})
	for _, text := range claudeOAuthClientSystemTexts(system) {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	return blocks
}

// claudeOAuthClientSystemTexts is the client's own system text, in order, with
// Claude Code's billing line left out (it is re-issued as its own block above, so
// keeping it would send it twice) and the agent identifier left out when the
// client already wrote exactly that.
func claudeOAuthClientSystemTexts(system any) []string {
	switch typed := system.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" || claudeOAuthBillingLine.MatchString(trimmed) {
			return nil
		}
		if trimmed == claudeOAuthAgentIdentifier {
			return nil
		}
		return []string{trimmed}
	case []any:
		texts := make([]string, 0, len(typed))
		for _, item := range typed {
			block, ok := item.(map[string]any)
			if !ok || stringValue(block["type"]) != "text" {
				continue
			}
			text := stringValue(block["text"])
			if strings.TrimSpace(text) == "" || claudeOAuthBillingLine.MatchString(text) {
				continue
			}
			if strings.TrimSpace(text) == claudeOAuthAgentIdentifier {
				continue
			}
			texts = append(texts, text)
		}
		return texts
	default:
		return nil
	}
}

func isClaudeOAuthBillingBlock(block map[string]any) bool {
	return claudeOAuthBillingLine.MatchString(stringValue(block["text"]))
}

// claudeOAuthBillingBlock is the billing header Claude Code writes first in its
// system. signClaudeOAuthCCH replaces the cch placeholder with the real value.
func claudeOAuthBillingBlock() map[string]any {
	return map[string]any{
		"type": "text",
		"text": fmt.Sprintf(
			"x-anthropic-billing-header: cc_version=%s; cc_entrypoint=cli; cch=%s;",
			claudeOAuthBillingVersion,
			claudeOAuthBillingCCHPlaceholder,
		),
	}
}

func remapClaudeOAuthToolName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	// MCP namespaced tools ("mcp__<server>__<tool>") are a protocol convention,
	// not a "third-party lowercase/snake_case" tool name — PascalCasing them
	// (mcp__test__ping_server → McpTestPingServer) breaks every MCP server
	// behind this OAuth route, since the client only recognizes the original
	// mcp__ name. Leave these untouched.
	if strings.HasPrefix(name, "mcp__") {
		return name, false
	}
	// Anthropic server tools keep snake_case canonical names (web_search, …).
	if isAnthropicServerToolName(name) {
		return name, false
	}
	if mapped, ok := oauthToolRenameMap[strings.ToLower(name)]; ok {
		return mapped, mapped != name
	}
	if isLikelyThirdPartyToolName(name) {
		return snakeOrLowerToTitleCase(name), true
	}
	return name, false
}

func isLikelyThirdPartyToolName(name string) bool {
	if name == strings.ToLower(name) {
		return true
	}
	for _, r := range name {
		if r == '_' {
			return true
		}
	}
	return false
}

func snakeOrLowerToTitleCase(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool {
		return r == '_' || r == '-' || r == ' '
	})
	if len(parts) == 0 {
		return name
	}
	var b strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		runes := []rune(strings.ToLower(part))
		runes[0] = unicode.ToUpper(runes[0])
		b.WriteString(string(runes))
	}
	if out := b.String(); out != "" {
		return out
	}
	return name
}

func remapClaudeOAuthToolNames(payload map[string]any) {
	walkClaudeOAuthToolNames(payload, remapClaudeOAuthToolName)
}

// claudeOAuthToolNameAliases is the names a request's tools wear after cloaking,
// each mapped to the name the client declared. Cloaking renames third-party
// lowercase/snake_case tools into Claude Code's TitleCase, and a client only
// dispatches the names it declared, so the answer's tool_use names must be
// named back with this map. It performs the same walk as the rename without
// changing anything, so the two can never drift apart.
func claudeOAuthToolNameAliases(claudeReq map[string]any) map[string]string {
	aliases := map[string]string{}
	walkClaudeOAuthToolNames(claudeReq, func(name string) (string, bool) {
		mapped, changed := remapClaudeOAuthToolName(name)
		if !changed {
			return name, false
		}
		if _, taken := aliases[mapped]; !taken {
			aliases[mapped] = name
		}
		return name, false
	})
	if len(aliases) == 0 {
		return nil
	}
	return aliases
}

// walkClaudeOAuthToolNames applies rewrite to every tool name the cloak owns —
// tools[].name, tool_choice.name and the names in message history — and reports
// whether any name changed. rewrite returns the name to use and whether it
// differs from the one given; a walker that only observes passes the name back
// unchanged.
func walkClaudeOAuthToolNames(payload map[string]any, rewrite func(name string) (string, bool)) bool {
	if payload == nil || rewrite == nil {
		return false
	}
	// Collect Anthropic server-tool names from tools[] (type != "" && != custom).
	// These must stay snake_case in tool_choice and message history even when
	// the name also appears in oauthToolRenameMap (e.g. server bash_* vs client bash).
	serverToolNames := map[string]struct{}{}
	changed := false
	if rawTools, ok := payload["tools"].([]any); ok && len(rawTools) > 0 {
		updated := make([]any, 0, len(rawTools))
		for _, item := range rawTools {
			tool, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if toolType := stringValue(tool["type"]); toolType != "" && toolType != "custom" {
				if name := stringValue(tool["name"]); name != "" {
					serverToolNames[name] = struct{}{}
				}
				updated = append(updated, tool)
				continue
			}
			name := stringValue(tool["name"])
			if mapped, differs := rewrite(name); differs {
				tool["name"] = mapped
				changed = true
			}
			updated = append(updated, tool)
		}
		payload["tools"] = updated
	}

	if choice, ok := payload["tool_choice"].(map[string]any); ok && stringValue(choice["type"]) == "tool" {
		name := stringValue(choice["name"])
		if !shouldPreserveOAuthToolName(name, serverToolNames) {
			if mapped, differs := rewrite(name); differs {
				choice["name"] = mapped
				changed = true
			}
		}
	}

	rawMessages, ok := payload["messages"].([]any)
	if !ok {
		return changed
	}
	for _, item := range rawMessages {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if walkClaudeOAuthToolNamesInContent(entry["content"], serverToolNames, rewrite) {
			changed = true
		}
	}
	return changed
}

// shouldPreserveOAuthToolName reports names that must stay unchanged under
// OAuth cloaking: Anthropic server tools (hard denylist + request-scoped).
func shouldPreserveOAuthToolName(name string, serverToolNames map[string]struct{}) bool {
	if name == "" {
		return false
	}
	if isAnthropicServerToolName(name) {
		return true
	}
	if _, ok := serverToolNames[name]; ok {
		return true
	}
	return false
}

func remapClaudeOAuthToolNamesInContent(content any, serverToolNames map[string]struct{}) {
	walkClaudeOAuthToolNamesInContent(content, serverToolNames, remapClaudeOAuthToolName)
}

// walkClaudeOAuthToolNamesInContent is the history half of
// walkClaudeOAuthToolNames: it names a message's tool_use / tool_reference
// blocks through rewrite and reports whether any changed.
func walkClaudeOAuthToolNamesInContent(content any, serverToolNames map[string]struct{}, rewrite func(name string) (string, bool)) bool {
	blocks, ok := content.([]any)
	if !ok {
		return false
	}
	changed := false
	for _, blockItem := range blocks {
		block, ok := blockItem.(map[string]any)
		if !ok {
			continue
		}
		switch stringValue(block["type"]) {
		case "server_tool_use":
			// Anthropic server-tool calls keep their canonical name; never cloak.
		case "tool_use":
			name := stringValue(block["name"])
			if shouldPreserveOAuthToolName(name, serverToolNames) {
				continue
			}
			if mapped, differs := rewrite(name); differs {
				block["name"] = mapped
				changed = true
			}
		case "tool_reference":
			name := stringValue(block["tool_name"])
			if shouldPreserveOAuthToolName(name, serverToolNames) {
				continue
			}
			if mapped, differs := rewrite(name); differs {
				block["tool_name"] = mapped
				changed = true
			}
		case "tool_result":
			// tool_search results nest tool_reference inside tool_result.content.
			if walkClaudeOAuthToolNamesInContent(block["content"], serverToolNames, rewrite) {
				changed = true
			}
		}
	}
	return changed
}

func reverseRemapClaudeOAuthToolName(name string) string {
	if original, ok := oauthToolRenameReverseMap[name]; ok {
		return original
	}
	return name
}

func marshalClaudeOAuthBody(payload map[string]any) ([]byte, error) {
	orderedKeys := []string{
		"model", "system", "messages", "tools", "tool_choice",
		"max_tokens", "thinking", "output_config", "stream", "temperature", "top_p",
	}
	seen := map[string]bool{}
	buf := bytes.NewBuffer(make([]byte, 0, 4096))
	buf.WriteByte('{')
	first := true
	writeField := func(key string, value any) error {
		if value == nil {
			return nil
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if string(encoded) == "null" {
			return nil
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		buf.WriteByte('"')
		buf.WriteString(key)
		buf.WriteString(`":`)
		buf.Write(encoded)
		seen[key] = true
		return nil
	}
	for _, key := range orderedKeys {
		if value, ok := payload[key]; ok {
			if err := writeField(key, value); err != nil {
				return nil, err
			}
		}
	}
	rest := make([]string, 0, len(payload))
	for key := range payload {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	for _, key := range rest {
		if err := writeField(key, payload[key]); err != nil {
			return nil, err
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func signClaudeOAuthCCH(body []byte) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	system, ok := payload["system"].([]any)
	if !ok || len(system) == 0 {
		return body
	}
	first, ok := system[0].(map[string]any)
	if !ok {
		return body
	}
	billingHeader := stringValue(first["text"])
	if !strings.HasPrefix(billingHeader, "x-anthropic-billing-header:") {
		return body
	}
	if !claudeOAuthBillingCCHPattern.MatchString(billingHeader) {
		return body
	}

	unsignedHeader := claudeOAuthBillingCCHPattern.ReplaceAllString(billingHeader, "cch="+claudeOAuthBillingCCHPlaceholder+";")
	first["text"] = unsignedHeader
	system[0] = first
	payload["system"] = system

	unsignedBody, err := marshalClaudeOAuthBody(payload)
	if err != nil {
		return body
	}
	cch := fmt.Sprintf("%05x", xxHash64.Checksum(unsignedBody, claudeOAuthCCHSeed)&0xFFFFF)
	signedHeader := claudeOAuthBillingCCHPattern.ReplaceAllString(unsignedHeader, "cch="+cch+";")
	first["text"] = signedHeader
	system[0] = first
	payload["system"] = system

	signedBody, err := marshalClaudeOAuthBody(payload)
	if err != nil {
		return unsignedBody
	}
	return signedBody
}

func claudeOAuthBillingHTTPHeaderValue(body []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	system, ok := payload["system"].([]any)
	if !ok || len(system) == 0 {
		return ""
	}
	first, ok := system[0].(map[string]any)
	if !ok {
		return ""
	}
	text := strings.TrimSpace(stringValue(first["text"]))
	if !strings.HasPrefix(text, "x-anthropic-billing-header:") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(text, "x-anthropic-billing-header:"))
}

var claudeEphemeralCacheControl = map[string]any{"type": "ephemeral"}

func ensureClaudeOAuthCacheControl(payload map[string]any) {
	injectClaudeOAuthToolsCacheControl(payload)
	injectClaudeOAuthSystemCacheControl(payload)
	injectClaudeOAuthMessagesCacheControl(payload)
}

func setClaudeCacheControl(block map[string]any) {
	if block == nil {
		return
	}
	block["cache_control"] = cloneMap(claudeEphemeralCacheControl)
}

func clearClaudeCacheControl(block map[string]any) {
	if block == nil {
		return
	}
	delete(block, "cache_control")
}

func injectClaudeOAuthToolsCacheControl(payload map[string]any) {
	rawTools, ok := payload["tools"].([]any)
	if !ok || len(rawTools) == 0 {
		return
	}
	lastIndex := -1
	for index, item := range rawTools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if toolType := stringValue(tool["type"]); toolType != "" && toolType != "custom" {
			clearClaudeCacheControl(tool)
			continue
		}
		lastIndex = index
		clearClaudeCacheControl(tool)
	}
	if lastIndex < 0 {
		return
	}
	if last, ok := rawTools[lastIndex].(map[string]any); ok {
		setClaudeCacheControl(last)
	}
}

func injectClaudeOAuthSystemCacheControl(payload map[string]any) {
	switch system := payload["system"].(type) {
	case string:
		if strings.TrimSpace(system) == "" {
			return
		}
		payload["system"] = []any{
			map[string]any{
				"type":          "text",
				"text":          system,
				"cache_control": cloneMap(claudeEphemeralCacheControl),
			},
		}
	case []any:
		if len(system) == 0 {
			return
		}
		lastIndex := -1
		for index, item := range system {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if isClaudeOAuthBillingBlock(block) {
				clearClaudeCacheControl(block)
				continue
			}
			lastIndex = index
			clearClaudeCacheControl(block)
		}
		if lastIndex < 0 {
			return
		}
		if last, ok := system[lastIndex].(map[string]any); ok {
			setClaudeCacheControl(last)
			payload["system"] = system
		}
	}
}

func injectClaudeOAuthMessagesCacheControl(payload map[string]any) {
	rawMessages, ok := payload["messages"].([]any)
	if !ok || len(rawMessages) == 0 {
		return
	}
	for _, item := range rawMessages {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		clearMessageCacheControl(entry["content"])
	}

	userIndexes := make([]int, 0, 4)
	for index, item := range rawMessages {
		entry, ok := item.(map[string]any)
		if ok && stringValue(entry["role"]) == "user" {
			userIndexes = append(userIndexes, index)
		}
	}
	if len(userIndexes) < 2 {
		return
	}
	targetIndex := userIndexes[len(userIndexes)-2]
	entry, ok := rawMessages[targetIndex].(map[string]any)
	if !ok {
		return
	}
	switch content := entry["content"].(type) {
	case string:
		entry["content"] = []any{
			map[string]any{
				"type":          "text",
				"text":          content,
				"cache_control": cloneMap(claudeEphemeralCacheControl),
			},
		}
	case []any:
		if len(content) == 0 {
			return
		}
		last, ok := content[len(content)-1].(map[string]any)
		if ok {
			setClaudeCacheControl(last)
			entry["content"] = content
		}
	}
	rawMessages[targetIndex] = entry
	payload["messages"] = rawMessages
}

func clearMessageCacheControl(content any) {
	switch typed := content.(type) {
	case []any:
		for _, item := range typed {
			block, ok := item.(map[string]any)
			if ok {
				clearClaudeCacheControl(block)
			}
		}
	}
}

func messageHasCacheControl(content any) bool {
	switch typed := content.(type) {
	case []any:
		for _, item := range typed {
			block, ok := item.(map[string]any)
			if ok && block["cache_control"] != nil {
				return true
			}
		}
	}
	return false
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}
