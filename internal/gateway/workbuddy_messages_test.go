package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Only the four measured models move to the Messages route. A model outside that list
// keeps the chat path, which is where the reference measured it working — routing an
// untested model there is how a working request becomes a 404.
func TestWorkBuddyMessagesModelGate(t *testing.T) {
	for _, model := range []string{"claude-opus-5.5", "claude-sonnet-5.5", "claude-opus-5", "claude-opus-4.8-1m"} {
		if !workBuddyMessagesModel(model) {
			t.Fatalf("%s should take the Messages route", model)
		}
	}
	for _, model := range []string{"glm-5.3", "deepseek-v4.1-flash", "gpt-5.5", "kimi-k3-1", "claude-opus-6"} {
		if workBuddyMessagesModel(model) {
			t.Fatalf("%s must stay on chat", model)
		}
	}
}

// The route is China-only: WorkBuddy AI answers 404 for it on every model.
func TestWorkBuddyMessagesAppliesIsChinaOnlyAndStreamed(t *testing.T) {
	t.Setenv(workBuddyMessagesRouteEnv, "")
	cn := domain.Provider{ID: "wb-cn", AuthType: domain.AuthTypeWorkBuddy}
	intl := domain.Provider{ID: "wb-gl", AuthType: domain.AuthTypeWorkBuddyAI}

	if !workBuddyMessagesApplies(cn, "claude-opus-5.5", true) {
		t.Fatal("a CN Claude model did not take the route")
	}
	if workBuddyMessagesApplies(intl, "claude-opus-5.5", true) {
		t.Fatal("the intl build has no such route")
	}
	// The route is streamed-only, and the pass-through it feeds is a streaming one.
	if workBuddyMessagesApplies(cn, "claude-opus-5.5", false) {
		t.Fatal("a non-stream request took a streamed-only route")
	}
	if workBuddyMessagesApplies(cn, "glm-5.3", true) {
		t.Fatal("a chat-only model took the route")
	}

	// The switch turns it off without a rebuild.
	t.Setenv(workBuddyMessagesRouteEnv, "0")
	if workBuddyMessagesApplies(cn, "claude-opus-5.5", true) {
		t.Fatal("the switch did not turn the route off")
	}
}

// The URL is built from the site root, not the row's /v2 chat base.
func TestWorkBuddyMessagesURL(t *testing.T) {
	previous := workBuddyCNEndpoint
	workBuddyCNEndpoint = "https://www.workbuddy.cn"
	defer func() { workBuddyCNEndpoint = previous }()
	if got := workBuddyMessagesURL(workBuddyCNSite); got != "https://www.workbuddy.cn/v1/messages" {
		t.Fatalf("url = %q", got)
	}
}

// An unsigned thinking block is dropped; a signed one and everything else stays.
//
// This is the 11159 guard: a conversation that began on chat carries unsigned blocks,
// and once a signed one from this route sits beside them the upstream refuses the whole
// request.
func TestSanitizeWorkBuddyMessagesBodyDropsUnsignedThinking(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-5.5",
		"system":"You are Claude Code, Anthropic's official CLI for Claude.",
		"messages":[
			{"role":"user","content":[{"type":"text","text":"hi"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"unsigned trail"},
				{"type":"thinking","thinking":"signed trail","signature":"sig-1"},
				{"type":"text","text":"answer"}
			]},
			{"role":"user","content":"again"}
		]}`)
	got := sanitizeWorkBuddyMessagesBody(body)
	var payload map[string]any
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	messages, _ := payload["messages"].([]any)
	assistant, _ := messages[1].(map[string]any)
	parts, _ := assistant["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want the signed thinking and the text kept", len(parts))
	}
	first, _ := parts[0].(map[string]any)
	if first["type"] != "thinking" || first["signature"] != "sig-1" {
		t.Fatalf("the signed block was not kept first: %+v", first)
	}
	// The client's prompt is not rewritten on this route: it does not run the chat
	// channel check, so the billing line and identity sentence stay as sent.
	if payload["system"] != "You are Claude Code, Anthropic's official CLI for Claude." {
		t.Fatalf("the system prompt was rewritten: %v", payload["system"])
	}
	if payload["model"] != "claude-opus-5.5" {
		t.Fatalf("model = %v", payload["model"])
	}
}

// A body with nothing to drop comes back byte for byte, and a bad body is not made
// worse.
func TestSanitizeWorkBuddyMessagesBodyLeavesCleanBodiesAlone(t *testing.T) {
	clean := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if got := sanitizeWorkBuddyMessagesBody(clean); string(got) != string(clean) {
		t.Fatalf("clean body rewritten: %s", string(got))
	}
	for _, body := range []string{"", "not json", `{"model":"m"}`} {
		if got := sanitizeWorkBuddyMessagesBody([]byte(body)); string(got) != body {
			t.Fatalf("%q rewritten: %s", body, string(got))
		}
	}
}

// The request carries the desktop head plus Anthropic's version header, so the upstream
// sees the same client it sees on a chat.
func TestNewWorkBuddyMessagesRequestHead(t *testing.T) {
	provider := domain.Provider{ID: "wb", AuthType: domain.AuthTypeWorkBuddy,
		WorkBuddy: &domain.WorkBuddyCredential{AccessToken: "tok", UserID: "uid-m", Domain: "www.workbuddy.cn"}}
	request, err := newWorkBuddyMessagesRequest(context.Background(), provider,
		"https://www.workbuddy.cn/v1/messages", []byte(`{"model":"claude-opus-5.5"}`))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer tok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := request.Header.Get("X-User-Id"); got != "uid-m" {
		t.Fatalf("X-User-Id = %q", got)
	}
	if got := request.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Fatalf("anthropic-version = %q", got)
	}
	if got := request.Header.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("Accept = %q, the route is streamed-only", got)
	}
	if ua := request.Header.Get("User-Agent"); !strings.HasPrefix(ua, "WorkBuddy/") {
		t.Fatalf("User-Agent = %q", ua)
	}
}

// The body's model is what the gate reads.
func TestWorkBuddyMessagesBodyModel(t *testing.T) {
	if got := workBuddyMessagesBodyModel([]byte(`{"model":"claude-opus-5.5"}`)); got != "claude-opus-5.5" {
		t.Fatalf("model = %q", got)
	}
	if got := workBuddyMessagesBodyModel([]byte(`not json`)); got != "" {
		t.Fatalf("garbage gave %q", got)
	}
	if got := workBuddyMessagesBodyModel(nil); got != "" {
		t.Fatalf("nil gave %q", got)
	}
}

// A provider that is not a WorkBuddy account cannot build this request.
func TestNewWorkBuddyMessagesRequestRefusesOtherProviders(t *testing.T) {
	provider := domain.Provider{ID: "x", AuthType: domain.AuthTypeQoderPAT}
	if _, err := newWorkBuddyMessagesRequest(context.Background(), provider, "https://example.com", nil); err == nil {
		t.Fatal("a non-WorkBuddy provider was accepted")
	}
}

// The route is on unless an operator turns it off.
func TestWorkBuddyMessagesEnabledDefault(t *testing.T) {
	t.Setenv(workBuddyMessagesRouteEnv, "")
	if !workBuddyMessagesEnabled() {
		t.Fatal("the route is off by default")
	}
	for _, raw := range []string{"0", "false", "no", "off"} {
		t.Setenv(workBuddyMessagesRouteEnv, raw)
		if workBuddyMessagesEnabled() {
			t.Fatalf("%q did not turn the route off", raw)
		}
	}
	t.Setenv(workBuddyMessagesRouteEnv, "1")
	if !workBuddyMessagesEnabled() {
		t.Fatal("1 did not turn the route on")
	}
	_ = http.StatusOK
}
