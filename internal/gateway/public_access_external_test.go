package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestExternalDNSPublishesWithoutATunnel(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		Endpoints: []domain.OutputEndpoint{{
			ID: "openai", Name: "OpenAI", Protocol: domain.ProtocolOpenAIChat,
			BasePath: "/v1", ListenHost: "127.0.0.1", ListenPort: 18093, StreamEnabled: true,
		}},
		PublicAccess: domain.PublicAccessSettings{
			Enabled:      true,
			Provider:     "external",
			Mode:         domain.PublicAccessModeExternalDNS,
			ExposeAPI:    true,
			ExposeUI:     true,
			CustomDomain: "api.example.com",
			UIDomain:     "console.example.com",
			Expose:       "all",
		},
	})

	got := router.State().PublicAccess
	if got.Status != "external_dns_active" {
		t.Fatalf("status = %q, want external_dns_active: %+v", got.Status, got)
	}
	if got.PublicBaseURL != "https://api.example.com" || got.UIPublicBaseURL != "https://console.example.com" {
		t.Fatalf("public URLs = %q / %q", got.PublicBaseURL, got.UIPublicBaseURL)
	}
	if got.Tunnel != nil {
		t.Fatalf("external DNS must not acquire a tunnel: %+v", got.Tunnel)
	}
	endpoint := router.State().Endpoints[0]
	if !endpoint.PublicAccessEnabled || endpoint.PublicURL != "https://api.example.com/v1" {
		t.Fatalf("endpoint public surface = %+v", endpoint)
	}
}

func TestExternalDNSIncompleteWhenEnabledSurfaceHasNoHostname(t *testing.T) {
	router := NewRouter(domain.GatewayState{PublicAccess: domain.PublicAccessSettings{
		Enabled:   true,
		Provider:  "external",
		Mode:      domain.PublicAccessModeExternalDNS,
		ExposeAPI: true,
		ExposeUI:  false,
		Expose:    "all",
	}})
	got := router.State().PublicAccess
	if got.Status != "external_dns_incomplete" {
		t.Fatalf("status = %q, want external_dns_incomplete", got.Status)
	}
	if got.PublicBaseURL != "" {
		t.Fatalf("PublicBaseURL = %q, want empty", got.PublicBaseURL)
	}
}

func TestExternalDNSUpdateValidatesDomains(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	body := `{"enabled":true,"provider":"external","mode":"external_dns","exposeApi":true,"exposeUi":false,"expose":"all"}`
	req := httptest.NewRequest(http.MethodPatch, "/__public-access", strings.NewReader(body))
	req.Host = "127.0.0.1:18093"
	req.RemoteAddr = "127.0.0.1:4321"
	rec := httptest.NewRecorder()
	server.handleUpdatePublicAccess(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
}

func TestExternalDNSStartWorksWithoutTunnelManager(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	body := `{"enabled":true,"provider":"external","mode":"external_dns","exposeApi":true,"exposeUi":true,"customDomain":"api.example.com","uiDomain":"console.example.com","expose":"all"}`
	req := httptest.NewRequest(http.MethodPost, "/__public/start", strings.NewReader(body))
	req.Host = "127.0.0.1:18093"
	req.RemoteAddr = "127.0.0.1:4321"
	rec := httptest.NewRecorder()
	server.handlePublicStart(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := server.router.State().PublicAccess
	if !got.Enabled || got.Mode != domain.PublicAccessModeExternalDNS {
		t.Fatalf("public access = %+v", got)
	}
	if got.PublicBaseURL != "https://api.example.com" || got.UIPublicBaseURL != "https://console.example.com" {
		t.Fatalf("public URLs = %q / %q", got.PublicBaseURL, got.UIPublicBaseURL)
	}
}

func TestExternalDNSConfigIsGlobalForUserConsoles(t *testing.T) {
	got := publicAccessForUser(domain.PublicAccessSettings{
		Enabled:         true,
		Provider:        "external",
		Mode:            domain.PublicAccessModeExternalDNS,
		ExposeAPI:       true,
		ExposeUI:        true,
		CustomDomain:    "api.example.com",
		UIDomain:        "console.example.com",
		PublicBaseURL:   "https://api.example.com",
		UIPublicBaseURL: "https://console.example.com",
		Status:          "external_dns_active",
	})
	if got.CustomDomain != "api.example.com" || got.UIDomain != "console.example.com" {
		t.Fatalf("global domains = %q / %q", got.CustomDomain, got.UIDomain)
	}
	if got.PublicBaseURL != "https://api.example.com" || got.UIPublicBaseURL != "https://console.example.com" {
		t.Fatalf("global URLs = %q / %q", got.PublicBaseURL, got.UIPublicBaseURL)
	}
}

func TestExternalDNSControlTokenWorksWithoutTunnelManager(t *testing.T) {
	server, store, handler := newPublicAccessControlTestServer(t, domain.GatewayState{
		PublicAccess: domain.PublicAccessSettings{
			Enabled:      false,
			Provider:     "external",
			Mode:         domain.PublicAccessModeExternalDNS,
			ExposeAPI:    true,
			ExposeUI:     true,
			CustomDomain: "api.example.com",
			UIDomain:     "console.example.com",
			Expose:       "all",
		},
	})
	token := storeControlToken(t, store)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, controlRequest(t, token, string(domain.PublicAccessModeExternalDNS), true))
	if rec.Code != http.StatusOK {
		t.Fatalf("enable status=%d body=%s", rec.Code, rec.Body.String())
	}
	after := server.router.State().PublicAccess
	if !after.Enabled || after.Status != "external_dns_active" || after.PublicBaseURL != "https://api.example.com" {
		t.Fatalf("after enable = %+v", after)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, controlRequest(t, token, string(domain.PublicAccessModeExternalDNS), false))
	if rec.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := server.router.State().PublicAccess; got.Enabled {
		t.Fatalf("external DNS stayed enabled: %+v", got)
	}
}
