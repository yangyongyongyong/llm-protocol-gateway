package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
	"testing"
)

// 宿主分离的约定：UI 域名只放控制台，模型协议一律 404；API 域名只放模型协议
// 与 /__health，控制台接口一律 404。M9 审查曾报「UI 域名能访问 /openai/v1/models」，
// 复核不成立（该前缀已被 isModelProtocolPath 覆盖），这里把约定钉住。
func TestHostSeparationKeepsModelRoutesOffTheUIDomain(t *testing.T) {
	hit := false
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	})
	handler := withHostSeparatedServing(api, "", func(host string) string {
		switch host {
		case "ui.example.com":
			return "ui"
		case "api.example.com":
			return "api"
		}
		return ""
	})

	modelPaths := []string{
		"/openai/v1/models",
		"/openai/v1/responses",
		"/v1/models",
		"/v1/chat/completions",
		"/v1/embeddings",
		"/v1/audio/transcriptions",
		"/v1beta/models/gemini:generateContent",
		"/anthropic/v1/messages",
		"/chat/completions",
	}
	for _, path := range modelPaths {
		hit = false
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "http://ui.example.com"+path, nil))
		if hit {
			t.Fatalf("UI host reached the API handler for %s", path)
		}
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("UI host %s = %d, want 404", path, recorder.Code)
		}
	}

	// API 域名上控制台接口同样不可达。
	for _, path := range []string{"/__state", "/__host-metrics", "/__public/control", "/callback", "/"} {
		hit = false
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://api.example.com"+path, nil))
		if hit {
			t.Fatalf("API host reached the console handler for %s", path)
		}
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("API host %s = %d, want 404", path, recorder.Code)
		}
	}
}

// 网关互联入口属于 API 那一侧：对外暴露独立 API 域名时，别的网关只会拿到那个
// 域名，/__peer/* 必须在那上面可达，否则注册与透传全是 404。
func TestAPIHostServesPeerGatewayEndpoints(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	server.SetGatewayID(gwID)
	server.userStore = newMemoryUserStore()
	server.roles = newMemoryRoleStore()
	settings := server.router.State().PublicAccess
	settings.CustomDomain = "api.test.local"
	settings.UIDomain = "ui.test.local"
	settings.ExposeAPI = true
	settings.ExposeUI = true
	server.router.UpdatePublicAccess(settings)
	handler := server.Handler()

	for _, path := range []string{peerEndpointPrefix + "probe", peerEndpointPrefix + "me", peerEndpointPrefix + "providers"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://api.test.local"+path, nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code == http.StatusNotFound && strings.Contains(recorder.Body.String(), "management UI is not available") {
			t.Fatalf("API host %s was refused as a console path: %s", path, recorder.Body.String())
		}
	}

	// 注册入口同样要在 API 域名上可达：公网下游只有这一个地址。
	body, err := json.Marshal(peerRegisterPayload(nil))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://api.test.local"+peerEndpointPrefix+"register", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "203.0.113.9:4444"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("API host register = %d: %s", recorder.Code, recorder.Body.String())
	}

	// 控制台接口在 API 域名上仍然不可达，这条分离不能被上面的放行带开。
	console := httptest.NewRecorder()
	handler.ServeHTTP(console, httptest.NewRequest(http.MethodGet, "http://api.test.local/__peers", nil))
	if console.Code != http.StatusNotFound {
		t.Fatalf("API host /__peers = %d, want 404", console.Code)
	}
}

// 组合顺序：宿主分离必须在鉴权外层。反过来的话，API 域名上的 /__state 会先被
// 鉴权拦成 401——等于对外确认"这里有个管理接口，只是你没登录"，而不是 404。
func TestAPIHostAnswers404NotUnauthorizedForConsolePaths(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	// 配好双域名与暴露开关，让 publicHostRole 能认出 api 角色。
	settings := server.router.State().PublicAccess
	settings.CustomDomain = "api.test.local"
	settings.UIDomain = "ui.test.local"
	settings.ExposeAPI = true
	settings.ExposeUI = true
	server.router.UpdatePublicAccess(settings)

	handler := server.Handler()
	for _, path := range []string{"/__state", "/__logs", "/__host-metrics", "/__public/control"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://api.test.local"+path, nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("API host %s = %d, want 404 (a 401 confirms the endpoint exists)", path, recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), "management UI is not available") {
			t.Fatalf("API host %s body = %q, want the host-separation message", path, recorder.Body.String())
		}
	}
}

// 单域名部署不能按宿主分离处理。
//
// 只登记 API 域名、没配 UI 域名时，分离逻辑会把该域名判成 "api"，控制台请求
// 全部 404——可错误信息让人"改用 UI 域名"，而那个域名根本不存在。外部反代 /
// 外部 DNS 的单域名安装都是这种配置，升级到带分离的版本后整站直接打不开。
// 只有两个域名都配好（真正的双域名部署）才做分离。
func TestPublicHostRoleKeepsSingleDomainCombined(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	settings := server.router.State().PublicAccess
	settings.Enabled = true
	settings.Mode = domain.PublicAccessModeExternalDNS
	settings.Provider = "external"
	settings.CustomDomain = "gateway.example.com"
	settings.ExposeAPI = true
	settings.ExposeUI = false
	server.router.UpdatePublicAccess(settings)

	if role := server.publicHostRole("gateway.example.com"); role != "" {
		t.Fatalf("只配 API 域名时 host role = %q，want 组合模式（空）", role)
	}

	// 配上 UI 域名后才分离。
	settings.UIDomain = "ui.example.com"
	settings.ExposeUI = true
	server.router.UpdatePublicAccess(settings)
	if role := server.publicHostRole("gateway.example.com"); role != "api" {
		t.Fatalf("双域名下 API host role = %q，want api", role)
	}
	if role := server.publicHostRole("ui.example.com"); role != "ui" {
		t.Fatalf("双域名下 UI host role = %q，want ui", role)
	}

	// 只配 UI 域名同理：没有 API 域名可去，保持组合。
	settings.ExposeAPI = false
	settings.CustomDomain = ""
	settings.ExposeUI = true
	settings.UIDomain = "ui.example.com"
	server.router.UpdatePublicAccess(settings)
	if role := server.publicHostRole("ui.example.com"); role != "" {
		t.Fatalf("只配 UI 域名时 host role = %q，want 组合模式（空）", role)
	}
}
