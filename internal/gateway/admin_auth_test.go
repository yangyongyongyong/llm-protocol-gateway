package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
	"golang.org/x/crypto/bcrypt"
)

type memoryAdminAuthStore struct {
	mu   sync.Mutex
	data map[string]string
}

func newMemoryAdminAuthStore() *memoryAdminAuthStore {
	return &memoryAdminAuthStore{data: map[string]string{}}
}

func (m *memoryAdminAuthStore) Setting(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data[key]
}

func (m *memoryAdminAuthStore) SetSetting(key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return nil
}

func newAuthTestServer(t *testing.T) (*Server, *memoryAdminAuthStore) {
	t.Helper()
	router := NewRouter(domain.GatewayState{})
	logs := monitor.NewStore()
	server := NewServer(router, logs)
	store := newMemoryAdminAuthStore()
	server.SetAdminAuthStore(store)
	return server, store
}

func TestLocalBypassAllowsState(t *testing.T) {
	server, _ := newAuthTestServer(t)
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodGet, "/__state", nil)
	req.Host = "127.0.0.1:18093"
	req.RemoteAddr = "127.0.0.1:4321"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPublicBlocksStateWithoutSession(t *testing.T) {
	server, store := newAuthTestServer(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.SetSetting(settingAdminPassword, string(hash))
	_ = store.SetSetting(settingAdminSession, "test-secret")
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodGet, "/__state", nil)
	req.Host = "console.lucadesign.uk"
	req.RemoteAddr = "203.0.113.10:443"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSetupOnceAndLogin(t *testing.T) {
	server, _ := newAuthTestServer(t)
	handler := server.Handler()

	setupBody, _ := json.Marshal(map[string]string{"password": "password123"})
	req := httptest.NewRequest(http.MethodPost, "/__auth/setup", bytes.NewReader(setupBody))
	req.Host = "console.lucadesign.uk"
	req.RemoteAddr = "203.0.113.10:443"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup status=%d body=%s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected session cookie after setup")
	}

	req = httptest.NewRequest(http.MethodPost, "/__auth/setup", bytes.NewReader(setupBody))
	req.Host = "console.lucadesign.uk"
	req.RemoteAddr = "203.0.113.10:443"
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second setup status=%d", rec.Code)
	}

	loginBody, _ := json.Marshal(map[string]string{"password": "wrong-password"})
	req = httptest.NewRequest(http.MethodPost, "/__auth/login", bytes.NewReader(loginBody))
	req.Host = "console.lucadesign.uk"
	req.RemoteAddr = "203.0.113.10:443"
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad login status=%d", rec.Code)
	}

	loginBody, _ = json.Marshal(map[string]string{"password": "password123"})
	req = httptest.NewRequest(http.MethodPost, "/__auth/login", bytes.NewReader(loginBody))
	req.Host = "console.lucadesign.uk"
	req.RemoteAddr = "203.0.113.10:443"
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", rec.Code, rec.Body.String())
	}
	cookie := rec.Result().Cookies()[0]

	req = httptest.NewRequest(http.MethodGet, "/__state", nil)
	req.Host = "console.lucadesign.uk"
	req.RemoteAddr = "203.0.113.10:443"
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authed state status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSessionTokenExpiry(t *testing.T) {
	token, err := mintSessionToken("secret", legacyAdminUserID, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := verifySessionToken(token, "secret"); ok {
		t.Fatal("expired token should be invalid")
	}
	token, err = mintSessionToken("secret", legacyAdminUserID, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	userID, ok := verifySessionToken(token, "secret")
	if !ok {
		t.Fatal("fresh token should be valid")
	}
	if userID != legacyAdminUserID {
		t.Fatalf("token should carry userID, got %q", userID)
	}
	if _, ok := verifySessionToken(token, "other"); ok {
		t.Fatal("wrong secret should fail")
	}
	if _, ok := verifySessionToken(strings.Replace(token, ".", "x", 1), "secret"); ok {
		t.Fatal("tampered token should fail")
	}
}

func TestAuthStatusPublic(t *testing.T) {
	server, _ := newAuthTestServer(t)
	handler := server.Handler()
	req := httptest.NewRequest(http.MethodGet, "/__auth/status", nil)
	req.Host = "console.lucadesign.uk"
	req.RemoteAddr = "203.0.113.10:443"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var body adminAuthStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Configured || body.Authenticated || !body.RequireAuth {
		t.Fatalf("body=%+v", body)
	}
}

func TestIsUserAllowedPathProviderUsage(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodGet, "/__providers/p1/claude-oauth/usage", true},
		{http.MethodGet, "/__providers/p1/cursor-oauth/usage", true},
		{http.MethodGet, "/__providers/p1/claude-oauth/usage?refresh=1", true}, // path only; query stripped by caller
		{http.MethodPost, "/__providers/p1/claude-oauth/usage", false},
		{http.MethodGet, "/__providers/p1/test", false},
		{http.MethodPost, "/__providers/p1/test", false},
		{http.MethodGet, "/__providers", false},
		{http.MethodPost, "/__providers", false},
		{http.MethodPatch, "/__providers/p1", false},
		{http.MethodGet, "/__state", true},
		{http.MethodGet, "/__apikeys/abc/profiles", true},
	}
	for _, tc := range cases {
		path := tc.path
		if i := strings.IndexByte(path, '?'); i >= 0 {
			path = path[:i]
		}
		got := isUserAllowedPath(tc.method, path)
		if got != tc.want {
			t.Fatalf("%s %s: got %v want %v", tc.method, tc.path, got, tc.want)
		}
	}
}
