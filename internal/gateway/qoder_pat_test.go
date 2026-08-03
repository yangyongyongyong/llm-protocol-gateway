package gateway

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestQoderTokenExpiryPrefersExplicitLifetime(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	got := qoderTokenExpiry("opaque-token", 86082, 0, now)
	want := now.Add(86082 * time.Second).Add(-qoderTokenExpirySkew)
	if !got.Equal(want) {
		t.Fatalf("expiry = %v, want %v", got, want)
	}
}

func TestQoderTokenExpiryAcceptsEpochSecondsAndMillis(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	absolute := now.Add(2 * time.Hour)
	want := absolute.Add(-qoderTokenExpirySkew)

	if got := qoderTokenExpiry("opaque", 0, absolute.Unix(), now); !got.Equal(want) {
		t.Fatalf("seconds: expiry = %v, want %v", got, want)
	}
	if got := qoderTokenExpiry("opaque", 0, absolute.UnixMilli(), now); !got.Equal(want) {
		t.Fatalf("millis: expiry = %v, want %v", got, want)
	}
}

func TestQoderTokenExpiryFallsBackToJWTExp(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	exp := now.Add(3 * time.Hour).Unix()
	payload, err := json.Marshal(map[string]any{"exp": exp})
	if err != nil {
		t.Fatal(err)
	}
	token := "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"

	got := qoderTokenExpiry(token, 0, 0, now)
	want := time.Unix(exp, 0).Add(-qoderTokenExpirySkew)
	if !got.Equal(want) {
		t.Fatalf("expiry = %v, want %v", got, want)
	}
}

func TestQoderTokenExpiryFallsBackToFixedTTL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	got := qoderTokenExpiry("opaque-not-a-jwt", 0, 0, now)
	if want := now.Add(qoderTokenFallbackTTL); !got.Equal(want) {
		t.Fatalf("expiry = %v, want %v", got, want)
	}
}

func TestQoderTokenNeedsRefresh(t *testing.T) {
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)

	cases := []struct {
		name       string
		credential *domain.QoderPATCredential
		want       bool
	}{
		{"nil credential", nil, true},
		{"missing job token", &domain.QoderPATCredential{RefreshToken: "pt-x"}, true},
		// Fail-open: an absent or unparseable expiry must not trigger an exchange
		// on every single request.
		{"empty expiry", &domain.QoderPATCredential{AccessToken: "jt", ExpiresAt: ""}, false},
		{"unparseable expiry", &domain.QoderPATCredential{AccessToken: "jt", ExpiresAt: "not-a-time"}, false},
		{"expired", &domain.QoderPATCredential{AccessToken: "jt", ExpiresAt: past}, true},
		{"fresh", &domain.QoderPATCredential{AccessToken: "jt", ExpiresAt: future}, false},
	}
	for _, testCase := range cases {
		if got := qoderTokenNeedsRefresh(testCase.credential); got != testCase.want {
			t.Errorf("%s: needsRefresh = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

func TestExchangeQoderJobTokenParsesResponseShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"flat jobToken", `{"jobToken":"jt-flat","expiresIn":86082}`},
		{"flat token", `{"token":"jt-flat","ttl":86082}`},
		{"enveloped", `{"success":true,"data":{"jobToken":"jt-flat","expiresIn":86082}}`},
		{"enveloped accessToken", `{"data":{"accessToken":"jt-flat"}}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var gotAuth, gotBody string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				raw, _ := io.ReadAll(r.Body)
				gotBody = string(raw)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(testCase.body))
			}))
			defer server.Close()

			credential, err := exchangeQoderJobTokenAt(server.URL, "pt-secret")
			if err != nil {
				t.Fatalf("exchange failed: %v", err)
			}
			if gotAuth != "Bearer pt-secret" {
				t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer pt-secret")
			}
			// The upstream requires the PAT in the body; an empty body is rejected.
			if !strings.Contains(gotBody, `"personal_token":"pt-secret"`) {
				t.Errorf("request body = %q, want it to carry personal_token", gotBody)
			}
			if credential.AccessToken != "jt-flat" {
				t.Errorf("job token = %q, want %q", credential.AccessToken, "jt-flat")
			}
			if credential.RefreshToken != "pt-secret" {
				t.Errorf("PAT should be carried forward, got %q", credential.RefreshToken)
			}
			if credential.ExpiresAt == "" {
				t.Error("expected a resolved expiry")
			}
			if !credential.Connected {
				t.Error("expected Connected = true")
			}
		})
	}
}

func TestExchangeQoderJobTokenErrorsOmitTheToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad credentials"}`))
	}))
	defer server.Close()

	_, err := exchangeQoderJobTokenAt(server.URL, "pt-super-secret")
	if err == nil {
		t.Fatal("expected an error for a non-2xx response")
	}
	// Exchange errors flow into request logs, so the PAT must never appear.
	if strings.Contains(err.Error(), "pt-super-secret") {
		t.Fatalf("error message leaked the PAT: %v", err)
	}
}

func TestExchangeQoderJobTokenRejectsEmptyToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()

	if _, err := exchangeQoderJobTokenAt(server.URL, "pt-x"); err == nil {
		t.Fatal("expected an error when the response carries no token")
	}
}

// The catalog comes from docs.qoder.com/cli/model. The endpoint rejects the
// docs' title-cased display names, so entries must be lowercase to match the
// CLI's own `--model efficient` form.
func TestDefaultQoderModelsUseDocumentedLowercaseNames(t *testing.T) {
	models := defaultQoderModels("qoder")
	if len(models) == 0 {
		t.Fatal("catalog must not be empty: an empty catalog makes the gateway pass unknown model names through")
	}
	seen := map[string]bool{}
	for _, model := range models {
		if model.ID != strings.ToLower(model.ID) {
			t.Errorf("model %q must be lowercase; the endpoint rejects display-cased names", model.ID)
		}
		if seen[model.ID] {
			t.Errorf("duplicate model %q", model.ID)
		}
		seen[model.ID] = true
		if model.ProviderID != "qoder" {
			t.Errorf("model %q has providerID %q", model.ID, model.ProviderID)
		}
		if model.Protocol != domain.ProtocolOpenAIChat {
			t.Errorf("model %q has protocol %q", model.ID, model.Protocol)
		}
	}
	// The documented tier aliases are available on every plan.
	for _, required := range []string{"auto", "ultimate", "performance", "efficient", "lite"} {
		if !seen[required] {
			t.Errorf("documented tier %q missing from the catalog", required)
		}
	}
}

// Guard against the redaction trap: without a Qoder branch in
// redactProviderForClient, both tokens serialize to the browser via /__state.
func TestRedactProviderForClientHidesQoderTokens(t *testing.T) {
	provider := domain.Provider{
		ID:       "qoder",
		Name:     "Qoder",
		AuthType: domain.AuthTypeQoderPAT,
		Protocol: domain.ProtocolOpenAIChat,
		BaseURL:  qoderDirectBaseURL,
		QoderPAT: &domain.QoderPATCredential{
			AccessToken:  "jt-should-never-be-sent",
			RefreshToken: "pt-should-never-be-sent",
			ExpiresAt:    "2026-01-01T00:00:00Z",
			AccountLabel: qoderAccountLabel,
		},
	}
	encoded, err := json.Marshal(redactProviderForClient(provider))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"jt-should-never-be-sent", "pt-should-never-be-sent"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("redacted provider leaked %q: %s", secret, encoded)
		}
	}
	redacted := redactProviderForClient(provider)
	// Connected is derived from the PAT, not the job token, so an expired job
	// token still reads as connected.
	if !redacted.QoderPAT.Connected {
		t.Error("expected Connected = true when a PAT is present")
	}
	if redacted.QoderPAT.ExpiresAt != "2026-01-01T00:00:00Z" {
		t.Errorf("expiry should survive redaction, got %q", redacted.QoderPAT.ExpiresAt)
	}
}

// Qoder's /effort ladder tops out at "max" (docs.qoder.com/cli/model), and the
// direct endpoint accepts it, so the pass-through path must not downgrade it.
// The max→high downgrade only applies to Claude models that lack the tier.
func TestNormalizeReasoningEffortKeepsMax(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		if got := normalizeReasoningEffort(effort); got != effort {
			t.Errorf("normalizeReasoningEffort(%q) = %q, want %q", effort, got, effort)
		}
	}
	for _, off := range []string{"", "none", "off", "disabled"} {
		if got := normalizeReasoningEffort(off); got != "" {
			t.Errorf("normalizeReasoningEffort(%q) = %q, want empty", off, got)
		}
	}
}

// Qoder reports failures as an `event: error` frame whose data payload is a
// bare error object with no "error" wrapper. Without event-name tracking the
// frame is skipped as an ordinary chunk and the real message ("Unsupported
// model …") is replaced by the useless "stream ended without any chunks".
func TestChatToResponsesStreamSurfacesBareErrorEvent(t *testing.T) {
	sse := "event: error\n" +
		`data: {"code":"invalid_model_error","message":"Unsupported model \"qwen3.8-max-preview\"","type":"invalid_model_error"}` + "\n\n"

	recorder := httptest.NewRecorder()
	_, err := streamOpenAIChatToResponsesEvents(recorder, strings.NewReader(sse), "qwen3.8-max-preview")
	if err == nil {
		t.Fatal("expected an error for an error-event stream")
	}
	if !strings.Contains(err.Error(), "Unsupported model") {
		t.Fatalf("error should carry the upstream message, got: %v", err)
	}
	if strings.Contains(err.Error(), "without any chunks") {
		t.Fatalf("upstream message was masked by the empty-stream error: %v", err)
	}
}

func TestChatToClaudeStreamSurfacesBareErrorEvent(t *testing.T) {
	sse := "event: error\n" +
		`data: {"code":"invalid_model_error","message":"Unsupported model \"nope\"","type":"invalid_model_error"}` + "\n\n"

	recorder := httptest.NewRecorder()
	_, err := streamOpenAIChatToClaudeEvents(recorder, strings.NewReader(sse), "nope")
	if err == nil {
		t.Fatal("expected an error for an error-event stream")
	}
	if !strings.Contains(err.Error(), "Unsupported model") {
		t.Fatalf("error should carry the upstream message, got: %v", err)
	}
}

// The console has no real Qoder username to show (no account endpoint, opaque
// job token), so the label carries the PAT's last 4 chars to tell two connected
// accounts apart. It must never expose more of the credential than that.
func TestQoderAccountLabelShowsOnlyLastFour(t *testing.T) {
	const pat = "pt-abcdefghijklmnop9xyz"
	label := qoderAccountLabelFor(pat)
	if !strings.HasSuffix(label, "****9xyz") {
		t.Fatalf("expected the last 4 chars, got %q", label)
	}
	if strings.Contains(label, pat) {
		t.Fatalf("label leaked the full PAT: %q", label)
	}
	// Nothing beyond the tail may appear: strip the tail and the rest of the
	// PAT must not be present anywhere in the label.
	if strings.Contains(label, pat[:len(pat)-4]) {
		t.Fatalf("label leaked the PAT prefix: %q", label)
	}
}

func TestQoderAccountLabelFallsBackWhenTooShort(t *testing.T) {
	for _, short := range []string{"", "  ", "abc"} {
		if got := qoderAccountLabelFor(short); got != qoderAccountLabel {
			t.Fatalf("qoderAccountLabelFor(%q) = %q, want plain %q", short, got, qoderAccountLabel)
		}
	}
}

// The label is only written during a token exchange, and exchanges happen
// lazily on the forward path — so an idle connected provider would keep the old
// bare "Qoder" label forever. Startup backfill closes that gap.
func TestBackfillQoderAccountLabels(t *testing.T) {
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{
		{
			ID: "stale", AuthType: domain.AuthTypeQoderPAT, Protocol: domain.ProtocolOpenAIChat,
			BaseURL:  qoderDirectBaseURL,
			QoderPAT: &domain.QoderPATCredential{RefreshToken: "pt-abcdefghijkl7890", AccountLabel: "Qoder", Connected: true},
		},
		{
			ID: "no-pat", AuthType: domain.AuthTypeQoderPAT, Protocol: domain.ProtocolOpenAIChat,
			BaseURL:  qoderDirectBaseURL,
			QoderPAT: &domain.QoderPATCredential{AccountLabel: "Qoder"},
		},
		{
			ID: "other", AuthType: domain.AuthTypeAPIKey, Protocol: domain.ProtocolOpenAIChat,
			BaseURL: "https://api.example.com", APIKeySource: "sk-x",
		},
	}})
	server := NewServer(router, monitor.NewStore())

	server.BackfillQoderAccountLabels()

	stale, err := router.ProviderByID("stale")
	if err != nil {
		t.Fatal(err)
	}
	if stale.QoderPAT.AccountLabel != "Qoder · ****7890" {
		t.Fatalf("stale label not backfilled, got %q", stale.QoderPAT.AccountLabel)
	}
	// The PAT itself must never end up in the label.
	if strings.Contains(stale.QoderPAT.AccountLabel, "pt-abcdefghijkl") {
		t.Fatalf("label leaked the PAT: %q", stale.QoderPAT.AccountLabel)
	}

	// A connection without a stored PAT has nothing to derive from: leave it be.
	noPAT, err := router.ProviderByID("no-pat")
	if err != nil {
		t.Fatal(err)
	}
	if noPAT.QoderPAT.AccountLabel != "Qoder" {
		t.Fatalf("provider without a PAT should keep its label, got %q", noPAT.QoderPAT.AccountLabel)
	}

	// Non-Qoder providers must not be touched.
	other, err := router.ProviderByID("other")
	if err != nil {
		t.Fatal(err)
	}
	if other.AuthType != domain.AuthTypeAPIKey || other.QoderPAT != nil {
		t.Fatalf("unrelated provider was modified: %+v", other)
	}
}

// Disconnect must not wipe the personal access token — only pause forwarding.
// Losing the PAT forces a trip back to qoder.com/account/integrations just to
// reconnect, which is the exact regression this guards.
func TestDisconnectProviderQoderPATKeepsToken(t *testing.T) {
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{{
		ID: "p1", AuthType: domain.AuthTypeQoderPAT, Protocol: domain.ProtocolOpenAIChat,
		BaseURL: qoderDirectBaseURL,
		QoderPAT: &domain.QoderPATCredential{
			AccessToken: "jt-live", RefreshToken: "pt-keepme1234", ExpiresAt: "2026-01-01T00:00:00Z",
			AccountLabel: "Qoder · ****1234",
		},
	}}})

	updated, err := router.DisconnectProviderQoderPAT("p1")
	if err != nil {
		t.Fatal(err)
	}
	if updated.QoderPAT == nil {
		t.Fatal("QoderPAT must not be nil after disconnect")
	}
	if updated.QoderPAT.RefreshToken != "pt-keepme1234" {
		t.Fatalf("PAT must survive disconnect, got %q", updated.QoderPAT.RefreshToken)
	}
	if !updated.QoderPAT.Disconnected {
		t.Fatal("expected Disconnected=true")
	}
	if updated.QoderPAT.AccessToken != "" {
		t.Fatalf("job token should be cleared, got %q", updated.QoderPAT.AccessToken)
	}
	if updated.QoderPAT.AccountLabel != "Qoder · ****1234" {
		t.Fatalf("account label must survive disconnect, got %q", updated.QoderPAT.AccountLabel)
	}

	// Redaction must reflect the paused state: not connected, but the console
	// can still tell there is a saved token to reconnect with.
	redacted := redactProviderForClient(updated)
	if redacted.QoderPAT.Connected {
		t.Error("disconnected provider must not read as Connected")
	}
	if !redacted.QoderPAT.HasStoredToken {
		t.Error("expected HasStoredToken=true so the console can offer one-click reconnect")
	}
	if !redacted.QoderPAT.Disconnected {
		t.Error("expected Disconnected=true in the redacted view")
	}
}

// A provider that was never connected must not claim HasStoredToken, so the
// console shows the paste box rather than a "reconnect" button with nothing
// to reconnect.
func TestRedactProviderQoderPATNeverConnected(t *testing.T) {
	redacted := redactProviderForClient(domain.Provider{
		ID: "p1", AuthType: domain.AuthTypeQoderPAT, Protocol: domain.ProtocolOpenAIChat,
		QoderPAT: &domain.QoderPATCredential{},
	})
	if redacted.QoderPAT.Connected || redacted.QoderPAT.HasStoredToken {
		t.Fatalf("expected no connection state without a PAT, got %+v", redacted.QoderPAT)
	}
}

// A disconnected provider must not silently keep forwarding via the lazy
// token-refresh path — otherwise "断开连接" would do nothing observable.
func TestEnsureFreshQoderTokenRefusesWhenDisconnected(t *testing.T) {
	provider := domain.Provider{
		ID: "p1", AuthType: domain.AuthTypeQoderPAT, Protocol: domain.ProtocolOpenAIChat,
		QoderPAT: &domain.QoderPATCredential{RefreshToken: "pt-x", Disconnected: true},
	}
	server := &Server{}
	if _, err := server.ensureFreshQoderToken(provider); err == nil {
		t.Fatal("expected an error for a disconnected provider")
	} else if !strings.Contains(err.Error(), "disconnected") {
		t.Fatalf("expected a disconnected-specific error, got %v", err)
	}
}

// The catalog order should read priciest-first (docs.qoder.com's credit
// multiplier, descending) so anything that iterates provider.Models without
// its own sort — the model list page, dropdowns — shows it that way too.
func TestDefaultQoderModelsOrderedByMultiplierDescending(t *testing.T) {
	models := defaultQoderModels("qoder")
	got := make([]string, len(models))
	for i, m := range models {
		got[i] = m.ID
	}
	want := []string{"ultimate", "performance", "auto", "efficient", "lite"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order mismatch at %d: got %v, want %v", i, got, want)
		}
	}
}

// Reproduces a production incident (2026-08-03): Claude Code's built-in
// web_search_20250305 tool carries no description at all, and Qoder's
// "ultimate" tier rejected the resulting request with a Pydantic-style
// "tools.0.custom.description: Input should be a valid string" — the field
// must be present, even as "", not omitted.
func TestQoderBackfillToolDescriptionsFillsMissingField(t *testing.T) {
	claudeReq := map[string]any{
		"model": "ultimate",
		"messages": []any{
			map[string]any{"role": "user", "content": "search"},
		},
		"tools": []any{
			map[string]any{"type": "web_search_20250305", "name": "web_search", "max_uses": float64(8)},
		},
	}
	openAIReq, err := claudeRequestToOpenAIChat(claudeReq, "ultimate")
	if err != nil {
		t.Fatal(err)
	}
	// Before the backfill: description is absent, which is exactly what
	// tripped Qoder's stricter-than-spec validation.
	tools := openAIReq["tools"].([]any)
	function := tools[0].(map[string]any)["function"].(map[string]any)
	if _, exists := function["description"]; exists {
		t.Fatalf("test setup assumption broken: description already present: %+v", function)
	}

	qoderBackfillToolDescriptions(openAIReq)

	description, ok := function["description"]
	if !ok {
		t.Fatal("expected description key to be backfilled")
	}
	if _, isString := description.(string); !isString {
		t.Fatalf("description must be a string, got %T: %v", description, description)
	}
}

// The workaround must only touch Qoder-bound requests: every other
// openai_chat provider's conversion tolerates an absent description just
// fine (it is optional per OpenAI's own Chat Completions spec), so baking
// this into the shared conversion path would be the wrong scope.
func TestQoderBackfillToolDescriptionsNotAppliedToOtherProviders(t *testing.T) {
	claudeReq := map[string]any{
		"model":    "glm-5.2",
		"messages": []any{map[string]any{"role": "user", "content": "search"}},
		"tools": []any{
			map[string]any{"type": "web_search_20250305", "name": "web_search", "max_uses": float64(8)},
		},
	}
	openAIReq, err := claudeRequestToOpenAIChat(claudeReq, "glm-5.2")
	if err != nil {
		t.Fatal(err)
	}
	tools := openAIReq["tools"].([]any)
	function := tools[0].(map[string]any)["function"].(map[string]any)
	if _, exists := function["description"]; exists {
		t.Fatalf("non-Qoder conversion should leave description untouched/absent: %+v", function)
	}
}
