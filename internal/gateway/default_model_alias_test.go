package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// The reserved "default" alias lets a Claude Code session pin
// ANTHROPIC_MODEL=default once and have the platform pick the model. These
// tests pin the contract:
//
//  1. "default" resolves to the provider's DefaultModel.
//  2. "default[1m]" (Claude Code's extended-context spelling) resolves the same.
//  3. A provider without a DefaultModel keeps the client's model — configuring a
//     default is optional, because naming the model in the client is supported.
//  4. An API key's own ModelOverride and modelAliases still win, so per-key
//     switching is unchanged.

func TestResolveReservedDefaultAlias(t *testing.T) {
	cases := []struct {
		name         string
		provider     domain.Provider
		requestModel string
		wantModel    string
		wantMapped   bool
	}{
		{
			name:         "bare default uses provider default",
			provider:     domain.Provider{ID: "p1", DefaultModel: "glm-5.3"},
			requestModel: "default",
			wantModel:    "glm-5.3",
			wantMapped:   true,
		},
		{
			name:         "case insensitive",
			provider:     domain.Provider{ID: "p1", DefaultModel: "glm-5.3"},
			requestModel: "DEFAULT",
			wantModel:    "glm-5.3",
			wantMapped:   true,
		},
		{
			name:         "1m spelling resolves to the same real model",
			provider:     domain.Provider{ID: "p1", DefaultModel: "glm-5.3"},
			requestModel: "default[1m]",
			wantModel:    "glm-5.3",
			wantMapped:   true,
		},
		{
			name:         "padded 1m spelling",
			provider:     domain.Provider{ID: "p1", DefaultModel: "glm-5.3"},
			requestModel: "  default[1m]  ",
			wantModel:    "glm-5.3",
			wantMapped:   true,
		},
		{
			// Optional configuration: no default model means no substitution.
			// The request keeps "default" and the provider's own error surfaces.
			name:         "no default model leaves the request alone",
			provider:     domain.Provider{ID: "p1"},
			requestModel: "default",
			wantModel:    "",
			wantMapped:   false,
		},
		{
			// A catalog without a default must NOT be mined for a substitute:
			// silently running the first listed model is a model the user never
			// chose, and it hides a misconfiguration behind a plausible answer.
			name:         "catalog without default is not a substitute",
			provider:     domain.Provider{ID: "p1", Models: []domain.Model{{ID: "glm-5.3"}, {ID: "glm-5.2"}}},
			requestModel: "default",
			wantModel:    "",
			wantMapped:   false,
		},
		{
			name:         "a real model is never touched",
			provider:     domain.Provider{ID: "p1", DefaultModel: "glm-5.3"},
			requestModel: "claude-sonnet-5",
			wantModel:    "",
			wantMapped:   false,
		},
		{
			// "default[1m]" is only the reserved alias with a context tag; a
			// bracketed model id that is not "default" must not be truncated.
			name:         "unrelated bracketed model is not stripped",
			provider:     domain.Provider{ID: "p1", DefaultModel: "glm-5.3"},
			requestModel: "some-model[1m]",
			wantModel:    "",
			wantMapped:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model, mapped := resolveReservedModelAlias(tc.provider, tc.requestModel)
			if model != tc.wantModel || mapped != tc.wantMapped {
				t.Fatalf("model=%q mapped=%v, want %q/%v", model, mapped, tc.wantModel, tc.wantMapped)
			}
		})
	}
}

func TestStripClaudeContextTag(t *testing.T) {
	cases := map[string]string{
		"default":       "default",
		"default[1m]":   "default",
		"default[2m]":   "default",
		"DEFAULT[1M]":   "DEFAULT",
		" default[1m] ": "default",
		"claude-opus-5": "claude-opus-5",
		"gpt-4[1m]":     "gpt-4",
		"model[beta]":   "model[beta]",
		"":              "",
	}
	for input, want := range cases {
		if got := stripClaudeContextTag(input); got != want {
			t.Fatalf("stripClaudeContextTag(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIsReservedDefaultModel(t *testing.T) {
	for _, input := range []string{"default", "Default", "default[1m]", "default[2m]", " default[1m] "} {
		if !isReservedDefaultModel(input) {
			t.Fatalf("isReservedDefaultModel(%q) = false, want true", input)
		}
	}
	for _, input := range []string{"", "sonnet", "my-default", "default-model", "defaults"} {
		if isReservedDefaultModel(input) {
			t.Fatalf("isReservedDefaultModel(%q) = true, want false", input)
		}
	}
}

// A key must keep working exactly as before: its own override and aliases are
// more specific than the reserved alias.
func TestReservedDefaultAliasYieldsToKeyConfiguration(t *testing.T) {
	provider := domain.Provider{ID: "p1", DefaultModel: "provider-default"}
	key := domain.APIKey{
		ID:            "k1",
		ModelOverride: "key-override",
		ModelAliases:  map[string]string{"default": "alias-target"},
	}

	model, _ := resolveConsumerModelOnProvider(provider, key, true, "default")
	if model != "key-override" {
		t.Fatalf("ModelOverride must win, got %q", model)
	}

	key.ModelOverride = ""
	model, logModel := resolveConsumerModelOnProvider(provider, key, true, "default")
	if model != "alias-target" {
		t.Fatalf("ModelAliases must win over the reserved alias, got %q", model)
	}
	if logModel != "default -> alias-target" {
		t.Fatalf("logModel=%q", logModel)
	}
}

// With nothing configured, "default" is logged as "default -> <resolved>" so the
// API log shows both what the client asked for and what actually ran.
func TestReservedDefaultAliasIsLoggedAsMapping(t *testing.T) {
	provider := domain.Provider{ID: "p1", DefaultModel: "glm-5.3"}
	model, logModel := resolveConsumerModelOnProvider(provider, domain.APIKey{}, false, "default[1m]")
	if model != "glm-5.3" {
		t.Fatalf("model=%q", model)
	}
	if logModel != "default[1m] -> glm-5.3" {
		t.Fatalf("logModel=%q", logModel)
	}
}

// End to end over the real Claude pass-through handler: a client that sends
// model "default[1m]" must reach the upstream with the provider's default model
// and keep the 1M-context beta the client asked for.
func TestClaudePassThroughResolvesReservedDefaultModel(t *testing.T) {
	var gotModel string
	var gotBeta string
	var gotRawBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBeta = r.Header.Get("anthropic-beta")
		body, _ := io.ReadAll(r.Body)
		gotRawBody = string(body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		gotModel, _ = parsed["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"glm-5.3","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()

	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "p1", Name: "P1", Protocol: domain.ProtocolClaude,
			AuthType: domain.AuthTypeSelfRegister, BaseURL: upstream.URL,
			APIKeySource: "literal:sk-test", AuthHeader: "x-api-key",
			DefaultModel: "glm-5.3",
		}},
		Routes: []domain.Route{{
			ID: "r1", Name: "R1", ProviderID: "p1",
			OutputProtocol: domain.ProtocolClaude, Mode: domain.RouteModeAuto, Enabled: true,
		}},
		APIKeys: []domain.APIKey{{ID: "k1", Name: "K1", Key: "sk-gw", RouteID: "r1", Enabled: true}},
	})
	server := NewServer(router, monitor.NewStore())

	// Claude Code strips the "[1m]" tag before sending, so the body carries the
	// bare name; the 1M intent arrives as the beta header.
	body := `{"model":"default","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`
	request := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(body))
	request.Header.Set("x-api-key", "sk-gw")
	request.Header.Set("anthropic-version", "2023-06-01")
	request.Header.Set("anthropic-beta", claudeContext1MBeta)
	recorder := httptest.NewRecorder()
	server.handleClaudeMessages(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if gotModel != "glm-5.3" {
		t.Fatalf("upstream model = %q, want the provider default glm-5.3 (body: %s)", gotModel, gotRawBody)
	}
	if !strings.Contains(gotBeta, claudeContext1MBeta) {
		t.Fatalf("client's 1M-context beta must survive resolution, got %q", gotBeta)
	}
}

// The same handler with no default model configured must forward "default"
// untouched rather than invent one: the upstream error is the honest outcome.
func TestClaudePassThroughKeepsDefaultWithoutProviderDefault(t *testing.T) {
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		gotModel, _ = parsed["model"].(string)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"not_found_error","message":"model: default"}}`)
	}))
	defer upstream.Close()

	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "p1", Name: "P1", Protocol: domain.ProtocolClaude,
			AuthType: domain.AuthTypeSelfRegister, BaseURL: upstream.URL,
			APIKeySource: "literal:sk-test", AuthHeader: "x-api-key",
		}},
		Routes: []domain.Route{{
			ID: "r1", Name: "R1", ProviderID: "p1",
			OutputProtocol: domain.ProtocolClaude, Mode: domain.RouteModeAuto, Enabled: true,
		}},
		APIKeys: []domain.APIKey{{ID: "k1", Name: "K1", Key: "sk-gw", RouteID: "r1", Enabled: true}},
	})
	server := NewServer(router, monitor.NewStore())

	body := `{"model":"default","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`
	request := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(body))
	request.Header.Set("x-api-key", "sk-gw")
	request.Header.Set("anthropic-version", "2023-06-01")
	recorder := httptest.NewRecorder()
	server.handleClaudeMessages(recorder, request)

	if gotModel != "default" {
		t.Fatalf("upstream model = %q, want the client's untouched \"default\"", gotModel)
	}
}

// Model discovery publishes both spellings so a client pinning "default" finds
// it in the catalog instead of relying on an unrecognized-model warning.
func TestClaudeModelsListsReservedDefaultAlias(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "p1", Name: "P1", Protocol: domain.ProtocolClaude,
			AuthType: domain.AuthTypeAPIKey, BaseURL: "https://example.invalid",
			DefaultModel: "glm-5.3",
		}},
		Routes: []domain.Route{{
			ID: "r1", Name: "R1", ProviderID: "p1",
			OutputProtocol: domain.ProtocolClaude, Mode: domain.RouteModeAuto, Enabled: true,
		}},
		APIKeys: []domain.APIKey{{ID: "k1", Name: "K1", Key: "sk-gw", RouteID: "r1", Enabled: true}},
	})
	server := NewServer(router, monitor.NewStore())

	request := httptest.NewRequest(http.MethodGet, "/anthropic/v1/models", nil)
	request.Header.Set("x-api-key", "sk-gw")
	recorder := httptest.NewRecorder()
	server.handleClaudeModels(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var payload struct {
		Data []struct {
			ID             string `json:"id"`
			MaxInputTokens int    `json:"max_input_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]int{}
	for _, entry := range payload.Data {
		byID[entry.ID] = entry.MaxInputTokens
	}
	if _, ok := byID["default"]; !ok {
		t.Fatalf("model list must include the reserved alias, got %v", byID)
	}
	if got := byID["default[1m]"]; got != contextLength1M {
		t.Fatalf("default[1m] max_input_tokens = %d, want %d", got, contextLength1M)
	}
}

// Model discovery must not advertise a tier the provider does not serve. A
// provider offering only m1/m2 used to list sonnet/opus/haiku with full-size
// context windows, which is a capacity claim the client acts on.
func TestClaudeModelsOmitsUnservedTierAliases(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "p1", Name: "P1", Protocol: domain.ProtocolClaude,
			AuthType: domain.AuthTypeAPIKey, BaseURL: "https://example.invalid",
			Models: []domain.Model{
				{ID: "m1", ProviderID: "p1", Protocol: domain.ProtocolClaude, InMenu: true},
				{ID: "m2", ProviderID: "p1", Protocol: domain.ProtocolClaude, InMenu: true},
			},
		}},
		Routes: []domain.Route{{
			ID: "r1", Name: "R1", ProviderID: "p1",
			OutputProtocol: domain.ProtocolClaude, Mode: domain.RouteModeAuto, Enabled: true,
		}},
		APIKeys: []domain.APIKey{{ID: "k1", Name: "K1", Key: "sk-gw", RouteID: "r1", Enabled: true}},
	})
	server := NewServer(router, monitor.NewStore())

	request := httptest.NewRequest(http.MethodGet, "/anthropic/v1/models", nil)
	request.Header.Set("x-api-key", "sk-gw")
	recorder := httptest.NewRecorder()
	server.handleClaudeModels(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]bool{}
	for _, entry := range payload.Data {
		byID[entry.ID] = true
	}
	for _, alias := range []string{"sonnet", "opus", "haiku"} {
		if byID[alias] {
			t.Fatalf("model list advertised %q, which no provider serves: %v", alias, byID)
		}
	}
	for _, real := range []string{"m1", "m2"} {
		if !byID[real] {
			t.Fatalf("the provider's own model %q must be listed, got %v", real, byID)
		}
	}
	// The reserved alias is about "let the gateway choose", not about a tier
	// existing, so it survives regardless of the catalog.
	if !byID["default"] {
		t.Fatalf("the reserved alias must stay listed, got %v", byID)
	}
}

// The counterpart: a provider that does serve a tier keeps advertising it, so
// official Claude providers see no change.
func TestClaudeModelsKeepsServedTierAliases(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "p1", Name: "P1", Protocol: domain.ProtocolClaude,
			AuthType: domain.AuthTypeAPIKey, BaseURL: "https://example.invalid",
			Models: []domain.Model{
				{ID: "claude-sonnet-5", ProviderID: "p1", Protocol: domain.ProtocolClaude, InMenu: true},
				// A relay namespaces its ids; that is still the same model.
				{ID: "anthropic/claude-haiku-4-5-20251001", ProviderID: "p1", Protocol: domain.ProtocolClaude, InMenu: true},
			},
		}},
		Routes: []domain.Route{{
			ID: "r1", Name: "R1", ProviderID: "p1",
			OutputProtocol: domain.ProtocolClaude, Mode: domain.RouteModeAuto, Enabled: true,
		}},
		APIKeys: []domain.APIKey{{ID: "k1", Name: "K1", Key: "sk-gw", RouteID: "r1", Enabled: true}},
	})
	server := NewServer(router, monitor.NewStore())

	request := httptest.NewRequest(http.MethodGet, "/anthropic/v1/models", nil)
	request.Header.Set("x-api-key", "sk-gw")
	recorder := httptest.NewRecorder()
	server.handleClaudeModels(recorder, request)
	var payload struct {
		Data []struct {
			ID             string `json:"id"`
			MaxInputTokens int    `json:"max_input_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]int{}
	for _, entry := range payload.Data {
		byID[entry.ID] = entry.MaxInputTokens
	}
	if got := byID["sonnet"]; got != contextLength1M {
		t.Fatalf("sonnet max_input_tokens = %d, want %d", got, contextLength1M)
	}
	if got := byID["haiku"]; got != contextLength200K {
		t.Fatalf("haiku max_input_tokens = %d, want %d", got, contextLength200K)
	}
	if _, ok := byID["opus"]; ok {
		t.Fatalf("opus is not served and must not be listed, got %v", byID)
	}
}

// Discovery is not the rewriting layer: even when the alias is not listed, a
// client that sends the tier name still resolves to the model it names.
func TestClaudeTierAliasResolvesWithoutBeingListed(t *testing.T) {
	provider := domain.Provider{
		ID: "p1", Protocol: domain.ProtocolClaude,
		Models: []domain.Model{{ID: "m1", ProviderID: "p1", Protocol: domain.ProtocolClaude}},
	}
	model, _ := resolveConsumerModelOnProvider(provider, domain.APIKey{}, false, "sonnet")
	if model != "claude-sonnet-5" {
		t.Fatalf("model = %q, want the alias to keep rewriting to claude-sonnet-5", model)
	}
}
