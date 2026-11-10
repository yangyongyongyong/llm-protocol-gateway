package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/clidesktop"
	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// /__clients/setup 会写本机文件、落盘 API 密钥，所以除了管理员身份还必须
// 来自回环地址：反代后面的网关永远看到 127.0.0.1 的对端，只查身份等于让公网
// 域名上的管理员改掉本机配置。

func newSetupServer(t *testing.T) *Server {
	t.Helper()
	home := t.TempDir()
	oldHome, oldGOOS := clidesktop.Home, clidesktop.GOOS
	clidesktop.Home = func() (string, error) { return home, nil }
	clidesktop.GOOS = "darwin"
	t.Cleanup(func() { clidesktop.Home, clidesktop.GOOS = oldHome, oldGOOS })

	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "p1", Name: "P1", Protocol: domain.ProtocolClaude,
			AuthType: domain.AuthTypeAPIKey, BaseURL: "https://example.invalid",
			DefaultModel: "glm-5.3",
		}},
		Routes: []domain.Route{{
			ID: "r1", Name: "R1", ProviderID: "p1",
			OutputProtocol: domain.ProtocolClaude, Mode: domain.RouteModeAuto, Enabled: true,
		}},
		APIKeys: []domain.APIKey{{ID: "k1", Name: "K1", Key: "sk-gw", RouteID: "r1", Enabled: true}},
	})
	return NewServer(router, monitor.NewStore())
}

// asAdmin marks the request as an authenticated administrator. It goes through
// withPrincipal so the handler sees exactly what the real auth middleware
// attaches.
func asAdmin(server *Server, request *http.Request) *http.Request {
	identity := adminIdentity()
	return withPrincipal(request, identity, server.principalFor(identity))
}

func setupRequest(remoteAddr, host string) *http.Request {
	body := `{"baseUrl":"https://gw.example.com/anthropic","gatewayUrl":"https://gw.example.com","token":"sk-gw-test","model":"default"}`
	request := httptest.NewRequest(http.MethodPost, "/__clients/setup", strings.NewReader(body))
	request.RemoteAddr = remoteAddr
	request.Host = host
	return request
}

func TestSetupClientsRejectsRemoteCallers(t *testing.T) {
	server := newSetupServer(t)
	// 已认证的管理员，但来自另一台机器：必须拒绝。
	request := asAdmin(server, setupRequest("203.0.113.9:5555", "127.0.0.1:18093"))
	recorder := httptest.NewRecorder()
	server.handleSetupClients(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestSetupClientsRejectsProxyHostHeader(t *testing.T) {
	server := newSetupServer(t)
	// 反代场景：对端确实是回环，但 Host 是公网域名 —— 坐在公网那头的人
	// 不能因此被当成本机操作员。
	request := asAdmin(server, setupRequest("127.0.0.1:5555", "llm.example.com"))
	recorder := httptest.NewRecorder()
	server.handleSetupClients(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestSetupClientsRejectsAnonymous(t *testing.T) {
	server := newSetupServer(t)
	// 没有身份：本机也不写。写入要经过管理员这一步。
	request := setupRequest("127.0.0.1:5555", "127.0.0.1:18093")
	recorder := httptest.NewRecorder()
	server.handleSetupClients(recorder, request)

	if recorder.Code != http.StatusUnauthorized && recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 401/403", recorder.Code)
	}
}

func TestSetupClientsWritesBothClientsForLocalAdmin(t *testing.T) {
	server := newSetupServer(t)
	request := asAdmin(server, setupRequest("127.0.0.1:5555", "127.0.0.1:18093"))
	recorder := httptest.NewRecorder()
	server.handleSetupClients(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !payload.OK {
		t.Fatalf("ok = false: %s", payload.Error)
	}

	home, _ := clidesktop.Home()

	// CLI 侧
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("CLI settings not written: %v", err)
	}
	var cli map[string]any
	if err := json.Unmarshal(raw, &cli); err != nil {
		t.Fatal(err)
	}
	env, _ := cli["env"].(map[string]any)
	if env["ANTHROPIC_BASE_URL"] != "https://gw.example.com/anthropic" {
		t.Errorf("CLI base URL = %v", env["ANTHROPIC_BASE_URL"])
	}
	if env["ANTHROPIC_MODEL"] != "default" {
		t.Errorf("CLI model = %v", env["ANTHROPIC_MODEL"])
	}

	// Desktop 侧：profile 与 _meta.json
	library := filepath.Join(home, "Library", "Application Support", "Claude-3p", "configLibrary")
	entries, err := os.ReadDir(library)
	if err != nil {
		t.Fatalf("desktop profile dir not written: %v", err)
	}
	if len(entries) < 2 {
		t.Fatalf("want a profile and _meta.json, got %v", entries)
	}
}
