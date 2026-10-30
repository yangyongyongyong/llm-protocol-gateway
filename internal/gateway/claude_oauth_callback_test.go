package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// Claude's callback goes to the gateway's own port on localhost, which a browser
// on another machine can never reach. The pasted address is the supported way
// out, so it must be accepted in the shape the address bar actually offers.
func TestParseClaudeOAuthInputTakesTheCallbackURL(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantCode  string
		wantState string
	}{
		{"whole callback URL", "http://localhost:18093/callback?code=abc%2F123&state=st-9", "abc/123", "st-9"},
		{"URL without a state", "http://localhost:18093/callback?code=abc", "abc", ""},
		{"relative callback path", "/callback?code=abc%2F123&state=st-9", "abc/123", "st-9"},
		{"code#state form", "abc#st-9", "abc", "st-9"},
		{"bare code", "abc", "abc", ""},
		{"empty", "   ", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, state := parseClaudeOAuthInput(tc.raw)
			if code != tc.wantCode || state != tc.wantState {
				t.Fatalf("parseClaudeOAuthInput(%q) = (%q, %q), want (%q, %q)", tc.raw, code, state, tc.wantCode, tc.wantState)
			}
		})
	}
}

// The code is one-time: whichever door arrives first spends it, and the other
// must be told so rather than sending a second exchange that Anthropic rejects
// (which reads to the user as a failed login).
func TestClaimClaudeCallbackIsSingleUse(t *testing.T) {
	pending := claudeOAuthPending{claim: &atomic.Bool{}}
	if !claimClaudeCallback(pending) {
		t.Fatal("the first arrival must be allowed to exchange the code")
	}
	if claimClaudeCallback(pending) {
		t.Fatal("the second arrival must not spend the same code again")
	}
	if !claimClaudeCallback(claudeOAuthPending{}) {
		t.Fatal("a flow without a claim must not be locked out")
	}
}

// Only a console opened on the gateway's own loopback can follow Anthropic's
// redirect back; everything else has to finish by pasting the code.
func TestRequestHostIsLoopback(t *testing.T) {
	cases := map[string]bool{
		"localhost:18093":     true,
		"127.0.0.1:18093":     true,
		"[::1]:18093":         true,
		"198.51.100.10:18093": false,
		"gateway.example.com": false,
		"192.168.1.5:18093":   false,
	}
	for host, want := range cases {
		if got := requestHostIsLoopback(host); got != want {
			t.Fatalf("requestHostIsLoopback(%q) = %v, want %v", host, got, want)
		}
	}
}

// A callback this gateway never issued usually means the browser followed the
// loopback redirect to a different gateway on the user's machine. The page has
// to say so and hand over the address the right console can finish with.
func TestClaudeOAuthCallbackUnknownStateOffersTheAddress(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	req := httptest.NewRequest(http.MethodGet, "/callback?code=abc%2F123&state=lost", nil)
	rec := httptest.NewRecorder()
	server.handleClaudeOAuthCallback(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"没有被当前网关认领", "http://example.com/callback?code=abc%2F123&amp;state=lost", "粘贴"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the page should contain %q", want)
		}
	}
	if strings.Contains(body, "<script>alert") {
		t.Fatal("the callback must not be reflected unescaped")
	}
}

// A remote console's browser can never reach the gateway's loopback, so only a
// loopback-issued flow may rely on the automatic callback.
func TestClaudeOAuthDefaultModeFollowsTheRequestHost(t *testing.T) {
	cases := map[string]string{
		"localhost:18093":     "localhost",
		"127.0.0.1:18093":     "localhost",
		"[::1]:18093":         "localhost",
		"198.51.100.10:18093": "manual",
		"gateway.example.com": "manual",
		"192.168.1.20:18093":  "manual",
	}
	for host, want := range cases {
		if got := claudeOAuthDefaultMode(host); got != want {
			t.Fatalf("claudeOAuthDefaultMode(%q) = %q, want %q", host, got, want)
		}
	}
}

// The callback hint belongs to the flow, not to one status transition: the
// console reads it while polling, after the flow has already moved on.
func TestClaudeOAuthStatusKeepsCallbackHint(t *testing.T) {
	store := newClaudeOAuthPendingStore()
	store.setStatus("f1", "pending", "waiting for browser callback")
	store.setCallbackHint("f1", claudeOAuthFlowStatus{
		PasteCallback: true, CallbackURL: "http://localhost:18093/callback", Instructions: "把地址粘回来",
	})
	store.setStatus("f1", "pending", "still waiting")
	status, ok := store.status("f1")
	if !ok {
		t.Fatal("status missing")
	}
	if !status.PasteCallback || status.CallbackURL == "" || status.Instructions == "" {
		t.Fatalf("callback hint lost: %+v", status)
	}
	if status.Status != "pending" || status.Message != "still waiting" {
		t.Fatalf("status transition lost: %+v", status)
	}
}
