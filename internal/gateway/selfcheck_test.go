package gateway

import (
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestContentLooksOK(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		text   string
		prompt string
		want   bool
	}{
		{name: "empty", text: "", want: false},
		{name: "digit two", text: "答案是 2", prompt: selfcheckDefaultPrompt, want: true},
		{name: "chinese er", text: "等于二", prompt: selfcheckDefaultPrompt, want: true},
		{name: "chinese liang", text: "一共两块", prompt: selfcheckDefaultPrompt, want: true},
		{name: "connect error", text: "Connect error while calling model", want: false},
		{name: "bracket error", text: "[Error: boom] 2", want: false},
		{name: "not found", text: "model not_found", want: false},
		{name: "http 4xx", text: "HTTP 401 unauthorized", want: false},
		{name: "no answer marker", text: "我不知道", prompt: selfcheckDefaultPrompt, want: false},
		{name: "whitespace only", text: "   \n\t  ", want: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := contentLooksOK(tc.text, tc.prompt)
			if got != tc.want {
				t.Fatalf("contentLooksOK(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestSelfcheckLANRoot(t *testing.T) {
	t.Parallel()
	state := domain.GatewayState{
		Endpoints: []domain.OutputEndpoint{
			{ListenHost: "192.168.199.124", ListenPort: 18093, Protocol: domain.ProtocolOpenAIChat},
		},
	}
	root := selfcheckLANRoot(state)
	if root != "http://192.168.199.124:18093" {
		t.Fatalf("unexpected lan root: %s", root)
	}
}

func TestSelfcheckKeyName(t *testing.T) {
	t.Parallel()
	got := selfcheckKeyName("cursor-pro", domain.ProtocolOpenAIChat)
	want := "selfcheck-cursor-pro-openai_chat"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestNormalizeSelfcheckModels(t *testing.T) {
	t.Parallel()
	got := normalizeSelfcheckModels(map[string]string{
		"chatgpt账号": " gpt-5.6-terra ",
		"other":      "x",
		"":           "y",
		"empty":      "  ",
	}, []string{"chatgpt账号", "empty"})
	if len(got) != 1 || got["chatgpt账号"] != "gpt-5.6-terra" {
		t.Fatalf("got %#v", got)
	}
}

func TestResolveSelfcheckModelPrefersRequest(t *testing.T) {
	t.Parallel()
	got := resolveSelfcheckModel(
		domain.APIKey{ModelOverride: "from-key"},
		domain.Provider{DefaultModel: "from-provider", Models: []domain.Model{{ID: "listed"}}},
		"from-request",
	)
	if got != "from-request" {
		t.Fatalf("got %q", got)
	}
}

