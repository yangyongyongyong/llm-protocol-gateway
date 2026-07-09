package gateway

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestBuildCursorLoginURL(t *testing.T) {
	loginURL := buildCursorLoginURL("challenge-value", "uuid-value")
	parsed, err := url.Parse(loginURL)
	if err != nil {
		t.Fatalf("parse login url: %v", err)
	}
	if parsed.Scheme+"://"+parsed.Host+parsed.Path != cursorOAuthLoginURL {
		t.Fatalf("login url = %q, want base %q", parsed.Scheme+"://"+parsed.Host+parsed.Path, cursorOAuthLoginURL)
	}
	query := parsed.Query()
	if query.Get("challenge") != "challenge-value" {
		t.Fatalf("missing challenge")
	}
	if query.Get("uuid") != "uuid-value" {
		t.Fatalf("missing uuid")
	}
	if query.Get("mode") != "login" {
		t.Fatalf("mode = %q, want login", query.Get("mode"))
	}
	if query.Get("redirectTarget") != "cli" {
		t.Fatalf("redirectTarget = %q, want cli", query.Get("redirectTarget"))
	}
}

func TestGenerateCursorPKCE(t *testing.T) {
	verifier, challenge, err := generateCursorPKCE()
	if err != nil {
		t.Fatalf("generate pkce: %v", err)
	}
	if verifier == "" || challenge == "" {
		t.Fatalf("empty pkce values")
	}
	if strings.Contains(verifier, "+") || strings.Contains(verifier, "/") || strings.Contains(verifier, "=") {
		t.Fatalf("verifier should be url-safe base64 without padding: %q", verifier)
	}
}

func TestCursorTokenExpiryFromJWT(t *testing.T) {
	exp := time.Now().Add(2 * time.Hour).Unix()
	payload, _ := json.Marshal(map[string]any{"exp": exp})
	token := "header." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
	got := cursorTokenExpiry(token)
	want := time.Unix(exp, 0).Add(-cursorTokenExpirySkew)
	if got.Sub(want).Abs() > time.Second {
		t.Fatalf("expiry = %v, want %v", got, want)
	}
}

func TestCursorTokenNeedsRefresh(t *testing.T) {
	if !cursorTokenNeedsRefresh(nil) {
		t.Fatalf("nil credential should need refresh")
	}
	if cursorTokenNeedsRefresh(&domain.CursorOAuthCredential{AccessToken: "token", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}) {
		t.Fatalf("fresh token should not need refresh")
	}
	if !cursorTokenNeedsRefresh(&domain.CursorOAuthCredential{AccessToken: "token", ExpiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)}) {
		t.Fatalf("expired token should need refresh")
	}
}
