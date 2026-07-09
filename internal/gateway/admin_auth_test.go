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
	token, err := mintAdminSessionToken("secret", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if verifyAdminSessionToken(token, "secret") {
		t.Fatal("expired token should be invalid")
	}
	token, err = mintAdminSessionToken("secret", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !verifyAdminSessionToken(token, "secret") {
		t.Fatal("fresh token should be valid")
	}
	if verifyAdminSessionToken(token, "other") {
		t.Fatal("wrong secret should fail")
	}
	if verifyAdminSessionToken(strings.Replace(token, ".", "x", 1), "secret") {
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
