package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 下游注册时会选一个自己能记住的密码，上游把它存成 bcrypt 摘要——这份摘要
// 只服务于「重新注册时证明自己是同一个下游」。如果它同时能换到一个控制台
// 会话，下游就变成了一个能看日志、能改自己密钥的用户，而这从来不是父子
// 网关想要的关系。
func TestPeerAccountCannotLogIntoConsole(t *testing.T) {
	server := newPeerTestServer(t)
	server.adminAuth = newConsoleAuthStore(t)
	_, response := peerRegister(t, server, peerRegisterPayload(nil))

	// 用注册时那个密码去登控制台：必须失败。用户名两种写法都试，防止
	// 有人以为去掉前缀就能绕过。
	for _, username := range []string{"sub-gw", response.Username} {
		recorder := loginAttempt(t, server, username, "a-strong-password")
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("username %q: status=%d body=%s", username, recorder.Code, recorder.Body.String())
		}
	}
	// 管理员口令照旧可用：这条闸门只针对对端账号。
	if recorder := loginAttempt(t, server, "admin", consoleTestPassword); recorder.Code != http.StatusOK {
		t.Fatalf("admin login broke: %d %s", recorder.Code, recorder.Body.String())
	}
}

// 会话恢复也走同一条判定：一条在升级前误发出去的会话，不能因为它存在于
// cookie 里就继续有效。
func TestPeerSessionIsNotRestored(t *testing.T) {
	server := newPeerTestServer(t)
	server.adminAuth = newConsoleAuthStore(t)
	server.userStore = newMemoryUserStore(domain.User{
		ID: "peer-u1", Username: "peer-gw", Role: domain.UserRoleAdmin, RoleID: domain.RoleIDAdmin,
		Enabled: true, Kind: domain.UserKindPeer, GatewayID: otherID,
	})
	token, err := mintSessionToken(consoleTestSecret, "peer-u1", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("mint session: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/__state", nil)
	request.Host = "console.example.test"
	request.AddCookie(&http.Cookie{Name: adminSessionCookieName, Value: token})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized && recorder.Code != http.StatusForbidden {
		t.Fatalf("a peer session must not be honoured, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

const (
	consoleTestSecret   = "peer-console-test-secret"
	consoleTestPassword = "admin-password-123"
)

// newConsoleAuthStore 造一个「管理口令已设置」的配置库，好让登录流程能走到
// users 表那一侧。
func newConsoleAuthStore(t *testing.T) *memorySettingStore {
	t.Helper()
	store := newMemorySettingStore(nil)
	if err := store.SetSetting(settingAdminPassword, mustHash(t, consoleTestPassword)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(settingAdminSession, consoleTestSecret); err != nil {
		t.Fatal(err)
	}
	return store
}

func loginAttempt(t *testing.T, server *Server, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"username":` + quoteJSON(username) + `,"password":` + quoteJSON(password) + `}`
	request := httptest.NewRequest(http.MethodPost, "/__auth/login", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func quoteJSON(value string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`) + `"`
}

// mustHash 在测试里造一个可用的 bcrypt 摘要。
func mustHash(t *testing.T, password string) string {
	t.Helper()
	hash, err := bcryptHash(password)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
