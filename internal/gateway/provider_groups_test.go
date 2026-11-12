package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// Provider 分组只做管理员归类：创建、改名、重排、批量移动和删除都不得改变
// Provider 本身，更不得影响路由或用户授权。
func TestProviderGroupsLifecycle(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "p1", Name: "p1", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://p1.example/v1"},
			{ID: "p2", Name: "p2", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://p2.example/v1"},
		},
	})
	openai, err := router.AddProviderGroup("OpenAI")
	if err != nil {
		t.Fatal(err)
	}
	claude, err := router.AddProviderGroup("Claude")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.AddProviderGroup("openai"); err == nil {
		t.Fatal("大小写不同的同名分组必须被拒绝")
	}
	if updated, err := router.UpdateProviderGroup(openai.ID, "OpenAI 账号"); err != nil || updated.Name != "OpenAI 账号" {
		t.Fatalf("rename: %+v %v", updated, err)
	}
	if updated, err := router.AssignProvidersToGroup([]string{"p1", "p2"}, openai.ID); err != nil || updated != 2 {
		t.Fatalf("assign: updated=%d err=%v", updated, err)
	}
	p1, _ := router.ProviderByID("p1")
	if p1.GroupID != openai.ID {
		t.Fatalf("p1 group=%q, want %q", p1.GroupID, openai.ID)
	}
	router.ReorderProviderGroups([]string{claude.ID, openai.ID})
	groups := router.ProviderGroups()
	if len(groups) != 2 || groups[0].ID != claude.ID || groups[1].ID != openai.ID {
		t.Fatalf("groups order=%+v", groups)
	}
	moved, err := router.DeleteProviderGroup(openai.ID)
	if err != nil || moved != 2 {
		t.Fatalf("delete: moved=%d err=%v", moved, err)
	}
	p1, _ = router.ProviderByID("p1")
	if p1.GroupID != "" {
		t.Fatalf("p1 group after delete=%q, want empty", p1.GroupID)
	}
}

// Provider 分组接口只对管理员开放；普通用户即使知道路径也必须被 403 拦住。
func TestProviderGroupHandlersRequireAdmin(t *testing.T) {
	server := &Server{router: NewRouter(domain.GatewayState{}), logs: monitor.NewStore()}
	request := httptest.NewRequest(http.MethodPost, "/__provider-groups", strings.NewReader(`{"name":"private"}`))
	request = request.WithContext(context.WithValue(request.Context(), authContextKey{}, sessionIdentity{UserID: "u1", Username: "u1", Role: domain.UserRoleUser}))
	recorder := httptest.NewRecorder()
	server.handleCreateProviderGroup(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s, want 403", recorder.Code, recorder.Body.String())
	}
	if groups := server.router.ProviderGroups(); len(groups) != 0 {
		t.Fatalf("普通用户创建出了分组: %+v", groups)
	}
}

// 普通用户状态必须完全去掉管理员分组和 groupId，避免在控制台或 API 泄漏内部归类。
func TestStateForUserStripsProviderGroups(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		ProviderGroups: []domain.ProviderGroup{{ID: "private-tag", Name: "内部", Position: 0}},
		Providers: []domain.Provider{{
			ID: "p1", Name: "p1", Protocol: domain.ProtocolOpenAIChat,
			BaseURL: "https://p1.example/v1", OwnerUserID: "u1", GroupID: "private-tag",
		}},
	})
	server := &Server{router: router}
	out := server.stateForUser(sessionIdentity{UserID: "u1", Username: "u1", Role: domain.UserRoleUser}, router.State())
	if len(out.ProviderGroups) != 0 || len(out.Providers) != 1 || out.Providers[0].GroupID != "" {
		t.Fatalf("普通用户状态泄漏了分组: %+v %+v", out.ProviderGroups, out.Providers)
	}
}
