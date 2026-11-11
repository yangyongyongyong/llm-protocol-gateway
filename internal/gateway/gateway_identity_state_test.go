package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 身份必须出现在 /__state 里：界面靠它显示"本网关 ID"让人复制到对端去配置。
func TestStateExposesGatewayIdentity(t *testing.T) {
	server, _ := newAuthTestServer(t)
	server.SetGatewayIdentity(domain.GatewayIdentity{
		ID:                gwID,
		Source:            "machine",
		MachineIDReadable: true,
	})

	request := httptest.NewRequest(http.MethodGet, "/__state", nil)
	request.Host = "127.0.0.1:18093"
	request.RemoteAddr = "127.0.0.1:4321"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	var state struct {
		GatewayIdentity *domain.GatewayIdentity `json:"gatewayIdentity"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if state.GatewayIdentity == nil {
		t.Fatal("/__state must carry the gateway identity")
	}
	if state.GatewayIdentity.ID != gwID {
		t.Fatalf("id = %q, want %q", state.GatewayIdentity.ID, gwID)
	}
	if state.GatewayIdentity.Source != "machine" || !state.GatewayIdentity.MachineIDReadable {
		t.Fatalf("identity metadata lost: %+v", state.GatewayIdentity)
	}
}

// 身份没初始化时整段落省，而不是给一个空 ID：界面看到空 ID 会画出一个
// 抄不出东西的复制按钮，比不显示更糟。
func TestStateOmitsGatewayIdentityWhenUnknown(t *testing.T) {
	server, _ := newAuthTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "/__state", nil)
	request.Host = "127.0.0.1:18093"
	request.RemoteAddr = "127.0.0.1:4321"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if body := recorder.Body.String(); strings.Contains(body, `"gatewayIdentity"`) {
		t.Fatalf("an uninitialised identity must be omitted, got %s", body)
	}
}

// 普通用户也要看得到：他们配置"父网关 ID"时同样需要复制自己这侧的 ID。
func TestGatewayIdentityIsVisibleToNonAdmin(t *testing.T) {
	server, _ := newAuthTestServer(t)
	server.SetGatewayIdentity(domain.GatewayIdentity{ID: gwID, Source: "machine"})
	server.userStore = newMemoryUserStore(domain.User{
		ID:       "u1",
		Username: "u1",
		Role:     domain.UserRoleUser,
		RoleID:   domain.RoleIDUser,
		Enabled:  true,
	})

	request := httptest.NewRequest(http.MethodGet, "/__state", nil)
	request.Host = "127.0.0.1:18093"
	request.RemoteAddr = "127.0.0.1:4321"
	request = withPrincipal(request, sessionIdentity{UserID: "u1", Username: "u1", Role: domain.UserRoleUser}, principal{
		UserID: "u1", Username: "u1", Role: domain.UserRoleUser, RoleID: domain.RoleIDUser,
		scope: domain.ScopeOwn,
	})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), gwID) {
		t.Fatalf("a regular user must see the gateway id, got %s", recorder.Body.String())
	}
}
