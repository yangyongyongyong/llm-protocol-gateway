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

func desktopEntryByName(t *testing.T, data []map[string]any, name string) map[string]any {
	t.Helper()
	for _, entry := range data {
		if entry["display_name"] == name {
			return entry
		}
	}
	t.Fatalf("%q missing from Desktop catalog: %v", name, data)
	return nil
}

func TestDesktopReservedDefaultsHaveStableWindows(t *testing.T) {
	for _, tc := range []struct {
		name      string
		catalog   []domain.Model
		wantCount int
	}{
		{"empty", nil, 2},
		{"short", []domain.Model{{ID: "deepseek-flash", ContextLength: contextLengthDefault}}, 3},
		{"all long", []domain.Model{{ID: "long", ContextLength: contextLength1M}}, 3},
		{"reserved collisions", []domain.Model{
			{ID: "default", ContextLength: contextLength1M},
			{ID: " DEFAULT[1M] ", ContextLength: contextLengthDefault},
		}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := desktopModelList(tc.catalog)
			if len(data) != tc.wantCount {
				t.Fatalf("got %d entries, want %d: %v", len(data), tc.wantCount, data)
			}
			for _, name := range []string{"default", "default[1m]"} {
				entry := desktopEntryByName(t, data, name)
				wantWindow := contextLengthDefault
				wantID := desktopModelAlias(name)
				wantDescription := ""
				if name == "default[1m]" {
					wantWindow = contextLength1M
					wantID += claudeContextTag1M
					wantDescription = desktop1MSaid
				}
				if entry["id"] != wantID || !desktopAcceptsModelID(wantID) {
					t.Fatalf("%s wire ID = %v, want accepted %q", name, entry["id"], wantID)
				}
				for _, field := range []string{"context_length", "context_window", "max_input_tokens"} {
					if entry[field] != wantWindow {
						t.Fatalf("%s.%s = %v, want %d", name, field, entry[field], wantWindow)
					}
				}
				description, _ := entry["description"].(string)
				if description != wantDescription {
					t.Fatalf("%s description = %q, want %q", name, description, wantDescription)
				}
			}
		})
	}
}

func TestDesktopReservedDefaultsResolvePlatformSelection(t *testing.T) {
	provider := domain.Provider{
		ID: "p1", Protocol: domain.ProtocolClaude, DefaultModel: "deepseek-flash",
		Models: []domain.Model{{ID: "deepseek-flash"}, {ID: "other"}},
	}
	requests := []string{
		desktopModelAlias("default"),
		desktopModelAlias("default[1m]") + claudeContextTag1M,
		// Claude Code strips the context suffix before HTTP, but the stable hash remains.
		desktopModelAlias("default[1m]"),
	}
	for _, request := range requests {
		for _, target := range []string{"deepseek-flash", "other"} {
			provider.DefaultModel = target
			model, logModel := resolveConsumerModelOnProvider(provider, domain.APIKey{}, false, request)
			if model != target || logModel != request+" -> "+target {
				t.Fatalf("%q default %q: model=%q log=%q", request, target, model, logModel)
			}
		}
		key := domain.APIKey{ModelOverride: "key-model"}
		if model, _ := resolveConsumerModelOnProvider(provider, key, true, request); model != "key-model" {
			t.Fatalf("key override lost for %q: %q", request, model)
		}
		provider.DefaultModel = ""
		if model, _ := resolveConsumerModelOnProvider(provider, domain.APIKey{}, false, request); model != "default" {
			t.Fatalf("unconfigured default invented a target for %q: %q", request, model)
		}
	}
	if model, _ := resolveConsumerModelOnProvider(provider, domain.APIKey{}, false, desktopModelAlias("deepseek-flash")); model != "deepseek-flash" {
		t.Fatalf("explicit selection requires a default: %q", model)
	}
}

func TestClaudeMessagesAcceptsDesktopReservedDefaults(t *testing.T) {
	var gotModel, gotBeta string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBeta = r.Header.Get("anthropic-beta")
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		gotModel = body.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"deepseek-flash","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "p1", Name: "P1", Protocol: domain.ProtocolClaude,
			AuthType: domain.AuthTypeSelfRegister, BaseURL: upstream.URL,
			APIKeySource: "literal:sk-test", AuthHeader: "x-api-key",
			DefaultModel: "deepseek-flash",
			Models:       []domain.Model{{ID: "deepseek-flash", ProviderID: "p1", Protocol: domain.ProtocolClaude, InMenu: true}},
		}},
		Routes:  []domain.Route{{ID: "r1", ProviderID: "p1", OutputProtocol: domain.ProtocolClaude, Mode: domain.RouteModeAuto, Enabled: true}},
		APIKeys: []domain.APIKey{{ID: "k1", Key: "sk-gw", RouteID: "r1", Enabled: true}},
	})
	server := NewServer(router, monitor.NewStore())
	data := getModels(t, server, "sk-gw", claudeDesktopUA)
	for _, name := range []string{"default", "default[1m]"} {
		id := desktopEntryByName(t, data, name)["id"].(string)
		for _, sent := range []string{id, stripClaudeContextTag(id)} {
			gotModel, gotBeta = "", ""
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"`+sent+`","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
			request.Header.Set("x-api-key", "sk-gw")
			request.Header.Set("anthropic-version", "2023-06-01")
			if name == "default[1m]" {
				request.Header.Set("anthropic-beta", claudeContext1MBeta)
			}
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK || gotModel != "deepseek-flash" {
				t.Fatalf("%q status=%d model=%q body=%s", sent, recorder.Code, gotModel, recorder.Body.String())
			}
			if name == "default[1m]" && !strings.Contains(gotBeta, claudeContext1MBeta) {
				t.Fatalf("1M intent lost: beta=%q", gotBeta)
			}
		}
	}
}
