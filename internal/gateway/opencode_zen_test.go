package gateway

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestOpenCodeZenFreeModel(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"big-pickle", true},              // free, and its id does not say so
		{"grok-code", true},               //
		{"kimi-k2.5-free", true},          // named by its suffix
		{"opencode/kimi-k2.5-free", true}, // asked through a route alias
		{"GLM-5.2", false},                //
		{"opencode-go/glm-5.3", false},    //
		{"", false},                       //
		{"  kimi-k2.6-free  ", true},      //
		{"minimax-m2", false},             //
	}
	for _, item := range cases {
		if got := openCodeZenFreeModel(item.model); got != item.want {
			t.Errorf("openCodeZenFreeModel(%q) = %v, want %v", item.model, got, item.want)
		}
	}
}

func TestOpenCodeZenInferenceURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"chat", "https://opencode.ai/zen/v1/chat/completions", "https://opencode.ai/inference/v1/chat/completions"},
		{"messages", "https://opencode.ai/zen/v1/messages", "https://opencode.ai/inference/v1/messages"},
		{"bare zen root", "https://opencode.ai/zen", "https://opencode.ai/inference"},
		// Go's paid deployment is a different product and is never moved.
		{"go", "https://opencode.ai/zen/go/v1/chat/completions", "https://opencode.ai/zen/go/v1/chat/completions"},
		{"another host", "https://example.com/zen/v1/chat/completions", "https://example.com/zen/v1/chat/completions"},
		{"not a url", "zen/v1", "zen/v1"},
	}
	for _, item := range cases {
		if got := openCodeZenInferenceURL(item.in); got != item.want {
			t.Errorf("%s: openCodeZenInferenceURL(%q) = %q, want %q", item.name, item.in, got, item.want)
		}
	}
}

// Zen's free tier reads the session id's shape and refuses one that OpenCode
// could not have made — which a hash cut to look like an id is. So a minted id
// has to pass the same check the upstream applies.
func TestOpenCodeZenSessionIDShape(t *testing.T) {
	id := openCodeZenNewID("ses", true)
	if !openCodeZenIDRe.MatchString(id) {
		t.Fatalf("minted id %q is not of OpenCode's shape", id)
	}
	if !openCodeZenRealSession(id) {
		t.Fatalf("minted id %q would be refused as not one OpenCode made", id)
	}
	for _, fake := range []string{"", "ses_", "ses_deadbeefcafebabe000000000000", "sess_0123456789abcdef0123456789ab", "msg_0123456789abcdef0123456789ab"} {
		if openCodeZenRealSession(fake) {
			t.Errorf("openCodeZenRealSession(%q) accepted a made-up id", fake)
		}
	}
}

// One conversation keeps one session id, because Zen routes and caches by it.
func TestOpenCodeZenSessionIsPerConversation(t *testing.T) {
	body := []byte(`{"model":"big-pickle","messages":[{"role":"system","content":"be terse"},{"role":"user","content":"first turn"}]}`)
	provider := domain.Provider{ID: "zen", AuthType: domain.AuthTypeOpenCodeZenFree}
	first := openCodeZenSessionID(provider.ID + "|" + openCodeZenFirstUserText(body))
	// A later turn repeats the first user message, which is what identifies it.
	later := []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"first turn"},{"role":"assistant","content":"a"},{"role":"user","content":"and then"}]}`)
	if second := openCodeZenSessionID(provider.ID + "|" + openCodeZenFirstUserText(later)); second != first {
		t.Fatalf("a later turn of one conversation got %q, want the same %q", second, first)
	}
	other := []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"a different conversation"}]}`)
	if third := openCodeZenSessionID(provider.ID + "|" + openCodeZenFirstUserText(other)); third == first {
		t.Fatalf("two conversations shared the session id %q", third)
	}
}

func TestPrepareOpenCodeZenChatBody(t *testing.T) {
	zen := domain.Provider{ID: "zen", AuthType: domain.AuthTypeOpenCodeZenFree}
	t.Run("free model gets streamed and offered tools", func(t *testing.T) {
		body := []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}]}`)
		shaped, free := prepareOpenCodeZenChatBody(zen, body)
		if !free {
			t.Fatal("a free model was not recognized as one")
		}
		var payload map[string]any
		if err := json.Unmarshal(shaped, &payload); err != nil {
			t.Fatalf("shaped body is not JSON: %v", err)
		}
		if stream, _ := payload["stream"].(bool); !stream {
			t.Error("the free tier is served to a streaming request only, but stream was not set")
		}
		tools, _ := payload["tools"].([]any)
		if len(tools) != 2 {
			t.Fatalf("got %d tools, want the two stubs added", len(tools))
		}
		for _, want := range []string{"bash", "read"} {
			tool := tools[0]
			if name := toolName(tool); name != want && name == "" {
				continue
			}
			found := false
			for _, item := range tools {
				if toolName(item) == want {
					found = true
					// Zen refuses either offered with no parameters.
					if len(propertiesOf(item)) == 0 {
						t.Errorf("%s was offered with no parameters", want)
					}
				}
			}
			if !found {
				t.Errorf("%s was not offered", want)
			}
		}
	})

	t.Run("a client's own Bash and Read count as offered", func(t *testing.T) {
		body := []byte(`{"model":"kimi-k2.5-free","stream":true,"tools":[{"type":"function","function":{"name":"Bash","parameters":{"type":"object"}}},{"type":"function","function":{"name":"Read","parameters":{"type":"object"}}}]}`)
		shaped, _ := prepareOpenCodeZenChatBody(zen, body)
		var payload struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(shaped, &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Tools) != 2 {
			t.Fatalf("got %d tools, want the client's two kept and no stubs added", len(payload.Tools))
		}
	})

	t.Run("paid models are left as they came", func(t *testing.T) {
		body := []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)
		shaped, free := prepareOpenCodeZenChatBody(zen, body)
		if free {
			t.Error("a paid model was dressed for the free tier")
		}
		if string(shaped) != string(body) {
			t.Errorf("body changed: %s", shaped)
		}
	})

	t.Run("another provider is untouched", func(t *testing.T) {
		body := []byte(`{"model":"big-pickle","messages":[]}`)
		shaped, free := prepareOpenCodeZenChatBody(domain.Provider{AuthType: domain.AuthTypeAPIKey}, body)
		if free || string(shaped) != string(body) {
			t.Errorf("a non-Zen provider was changed (%q, free=%v)", shaped, free)
		}
	})

	t.Run("shaping twice changes nothing more", func(t *testing.T) {
		body := []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}]}`)
		once, _ := prepareOpenCodeZenChatBody(zen, body)
		twice, _ := prepareOpenCodeZenChatBody(zen, once)
		if string(once) != string(twice) {
			t.Errorf("shaping is not idempotent:\n%s\n%s", once, twice)
		}
	})
}

func toolName(item any) string {
	tool, _ := item.(map[string]any)
	fn, _ := tool["function"].(map[string]any)
	name, _ := fn["name"].(string)
	return name
}

func propertiesOf(item any) map[string]any {
	tool, _ := item.(map[string]any)
	fn, _ := tool["function"].(map[string]any)
	params, _ := fn["parameters"].(map[string]any)
	properties, _ := params["properties"].(map[string]any)
	return properties
}

func TestApplyOpenCodeZenHeaders(t *testing.T) {
	provider := domain.Provider{ID: "zen", AuthType: domain.AuthTypeOpenCodeZenFree}
	request, _ := http.NewRequest(http.MethodPost, "https://opencode.ai/inference/v1/chat/completions", nil)
	request.Header.Set("x-session-affinity", "stale")
	request.Header.Set("X-Session-Id", "stale")
	body := []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}]}`)
	applyOpenCodeZenHeaders(request, provider, body)
	for _, key := range []string{"User-Agent", "x-opencode-session", "x-opencode-session-id", "x-opencode-request", "x-opencode-client", "x-opencode-project"} {
		if request.Header.Get(key) == "" {
			t.Errorf("%s was not sent; the free tier refuses a request that does not look like OpenCode's", key)
		}
	}
	if !strings.HasPrefix(request.Header.Get("User-Agent"), "opencode/1.") {
		t.Errorf("User-Agent = %q, want an OpenCode release the free tier accepts", request.Header.Get("User-Agent"))
	}
	if request.Header.Get("x-session-affinity") != "" || request.Header.Get("X-Session-Id") != "" {
		t.Error("a session-affinity header survived; the free tier answers 403 to one")
	}
	if id := request.Header.Get("x-opencode-session"); !openCodeZenRealSession(id) {
		t.Errorf("session %q is not one OpenCode could have made", id)
	}

	// A paid model is not dressed.
	paid, _ := http.NewRequest(http.MethodPost, "https://opencode.ai/zen/v1/chat/completions", nil)
	applyOpenCodeZenHeaders(paid, provider, []byte(`{"model":"glm-5.2","messages":[]}`))
	if paid.Header.Get("x-opencode-client") != "" {
		t.Error("a paid model was sent OpenCode's client headers")
	}
}

func TestResolveOpenCodeZenURL(t *testing.T) {
	provider := normalizeProviderForTest(domain.Provider{Name: "zen", AuthType: domain.AuthTypeOpenCodeZenFree, BaseURL: "https://example.com"})
	free := resolveOpenCodeZenURL(provider, "big-pickle", "https://opencode.ai/zen/v1/chat/completions")
	if !strings.Contains(free, "/inference/v1/") {
		t.Errorf("a free model asked %q, want the inference API", free)
	}
	paid := resolveOpenCodeZenURL(provider, "glm-5.2", "https://opencode.ai/zen/v1/chat/completions")
	if paid != "https://opencode.ai/zen/v1/chat/completions" {
		t.Errorf("a paid model was moved to %q", paid)
	}
	// The chat URL resolver is where every path goes through, so check the hook
	// is wired there rather than only in the helper.
	if got := resolveProviderChatURLWithAdapter(provider, "big-pickle"); got != "https://opencode.ai/inference/v1/chat/completions" {
		t.Errorf("resolveProviderChatURLWithAdapter returned %q", got)
	}
	if got := resolveProviderChatURLWithAdapter(provider, "glm-5.2"); got != "https://opencode.ai/zen/v1/chat/completions" {
		t.Errorf("a paid model was sent to %q", got)
	}
}

func TestOpenCodeZenAnonymousKeyAndNormalize(t *testing.T) {
	provider := domain.Provider{Name: "OpenCode Zen Free", AuthType: domain.AuthTypeOpenCodeZenFree, BaseURL: "https://example.com"}
	normalized := normalizeProviderForTest(provider)
	if got := resolveProviderAuth(normalized); got != openCodeZenAnonymousKey {
		t.Errorf("auth = %q, want the anonymous key Zen's free tier answers", got)
	}
	if normalized.Protocol != domain.ProtocolOpenAIChat {
		t.Errorf("protocol = %s, want OpenAI Chat", normalized.Protocol)
	}
	// An operator's own key is what reaches the paid models.
	withKey := normalizeProviderForTest(domain.Provider{Name: "zen", AuthType: domain.AuthTypeOpenCodeZenFree, APIKeySource: "sk-opencode"})
	if got := resolveProviderAuth(withKey); got != "sk-opencode" {
		t.Errorf("auth = %q, want the configured key", got)
	}
	// A placeholder left by the create form is replaced, not sent.
	blank := normalizeProviderForTest(domain.Provider{Name: "zen", AuthType: domain.AuthTypeOpenCodeZenFree, BaseURL: "https://example.com"})
	parsed, err := url.Parse(blank.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "opencode.ai" {
		t.Errorf("base url = %q, want Zen's own host", blank.BaseURL)
	}
}

// normalizeProviderForTest runs the router's normalization without a Router,
// the way the AddProvider path does.
func normalizeProviderForTest(provider domain.Provider) domain.Provider {
	normalizeProvider(&provider)
	return provider
}

// OpenCode Go 的 Claude 端点不认 Authorization: Bearer，只认 x-api-key，而且
// 必须带 x-opencode-session；少任何一个都会 401 / MissingSessionID。
func TestApplyOpenCodeGoClaudeHeaders(t *testing.T) {
	provider := domain.Provider{
		ID:           "opencode-go",
		AuthType:     domain.AuthTypeAPIKey,
		BaseURL:      "https://opencode.ai/zen/go/v1",
		APIKeySource: "literal:oc_sk_test",
	}
	request, err := http.NewRequest(http.MethodPost, "https://opencode.ai/zen/go/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	applyOpenCodeGoClaudeHeaders(request, provider, []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	if got := request.Header.Get("x-api-key"); got != "oc_sk_test" {
		t.Fatalf("x-api-key = %q", got)
	}
	if got := request.Header.Get("x-opencode-session"); got == "" {
		t.Fatal("x-opencode-session 不能为空")
	}
	// 同一段对话每次都得到同一个 session，否则上游会把每一轮当冷启动。
	again, _ := http.NewRequest(http.MethodPost, "https://opencode.ai/zen/go/v1/messages", nil)
	applyOpenCodeGoClaudeHeaders(again, provider, []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	if got, want := again.Header.Get("x-opencode-session"), request.Header.Get("x-opencode-session"); got != want {
		t.Fatalf("session 不稳定：%q != %q", got, want)
	}
}
