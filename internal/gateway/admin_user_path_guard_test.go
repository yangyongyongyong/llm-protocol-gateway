package gateway

import (
	"net/http"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"golang.org/x/crypto/bcrypt"
)

// 普通用户自带 providers.create，但 export/import 不在 isUserAllowedPath 白名单；
// 中间件必须在 permission 之外再挡一层，否则能拖走全站 OAuth 备份。
func TestScopeOwnUserCannotExportOrImportProviders(t *testing.T) {
	server, settings := newAuthTestServer(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("adminpass123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	_ = settings.SetSetting(settingAdminPassword, string(hash))
	_ = settings.SetSetting(settingAdminSession, "test-secret")
	userHash, err := bcrypt.GenerateFromPassword([]byte("userpass123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	server.userStore = newMemoryUserStore(domain.User{
		ID: "u-normal", Username: "normal", Role: domain.UserRoleUser, Enabled: true, PasswordHash: string(userHash),
	})
	_, err = server.router.AddProvider(domain.Provider{
		ID: "admin-secret", Name: "admin-secret", Protocol: domain.ProtocolClaude, BaseURL: "https://claude.example",
		ClaudeOAuth: &domain.ClaudeOAuthCredential{AccessToken: "at-leak", RefreshToken: "rt-leak"},
	})
	if err != nil {
		t.Fatal(err)
	}

	login := publicRequest(t, server, http.MethodPost, "/__auth/login",
		`{"username":"normal","password":"userpass123"}`, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0].Value

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/__providers/export"},
		{http.MethodPost, "/__providers/export"},
		{http.MethodPost, "/__providers/import"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			rec := publicRequest(t, server, route.method, route.path, `{"providers":[]}`, cookie)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "at-leak") {
				t.Fatal("export leaked oauth material")
			}
		})
	}
}

// 白名单内的端点不应被误伤：普通用户仍能 POST 创建 Provider。
func TestScopeOwnUserCanStillCreateProvider(t *testing.T) {
	server, settings := newAuthTestServer(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("adminpass123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	_ = settings.SetSetting(settingAdminPassword, string(hash))
	_ = settings.SetSetting(settingAdminSession, "test-secret")
	userHash, err := bcrypt.GenerateFromPassword([]byte("userpass123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	server.userStore = newMemoryUserStore(domain.User{
		ID: "u-normal", Username: "normal", Role: domain.UserRoleUser, Enabled: true, PasswordHash: string(userHash),
	})

	login := publicRequest(t, server, http.MethodPost, "/__auth/login",
		`{"username":"normal","password":"userpass123"}`, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d", login.Code)
	}
	cookie := login.Result().Cookies()[0].Value

	body := `{"name":"mine","protocol":"claude","baseUrl":"https://mine.example","authType":"api_key","apiKeySource":"sk-test"}`
	rec := publicRequest(t, server, http.MethodPost, "/__providers", body, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
}
