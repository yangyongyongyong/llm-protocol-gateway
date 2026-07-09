package gateway

import (
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestUpdatePublicAccessClearsCustomDomainFields(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		PublicAccess: domain.PublicAccessSettings{
			Enabled:          true,
			Provider:         "cloudflare",
			Mode:             domain.PublicAccessModeCustomDomain,
			ExposeAPI:        true,
			ExposeUI:         true,
			CustomDomain:     "gateway.lucadesign.uk",
			UIDomain:         "gateway-ui.lucadesign.uk",
			RuntimeURL:       "https://old-runtime.trycloudflare.com",
			TunnelName:       "llm-protocol-gateway",
			TunnelToken:      "secret-token",
			CredentialsFile:  "/tmp/creds.json",
			TunnelConfigFile: "/tmp/tunnel-config.yml",
			PublicBaseURL:    "https://gateway.lucadesign.uk",
			UIPublicBaseURL:  "https://gateway-ui.lucadesign.uk",
			Status:           "runtime_url_recorded",
		},
	})

	updated := router.UpdatePublicAccess(domain.PublicAccessSettings{
		Enabled:   false,
		Provider:  "cloudflare",
		Mode:      domain.PublicAccessModeRandomTunnel,
		ExposeAPI: true,
		ExposeUI:  true,
		Expose:    "all",
	})

	if updated.CustomDomain != "" {
		t.Fatalf("CustomDomain = %q, want empty", updated.CustomDomain)
	}
	if updated.UIDomain != "" {
		t.Fatalf("UIDomain = %q, want empty", updated.UIDomain)
	}
	if updated.RuntimeURL != "" {
		t.Fatalf("RuntimeURL = %q, want empty", updated.RuntimeURL)
	}
	if updated.TunnelName != "" {
		t.Fatalf("TunnelName = %q, want empty", updated.TunnelName)
	}
	if updated.TunnelToken != "" {
		t.Fatalf("TunnelToken = %q, want empty", updated.TunnelToken)
	}
	if updated.CredentialsFile != "" {
		t.Fatalf("CredentialsFile = %q, want empty", updated.CredentialsFile)
	}
	if updated.TunnelConfigFile != "" {
		t.Fatalf("TunnelConfigFile = %q, want empty", updated.TunnelConfigFile)
	}
	if updated.PublicBaseURL != "" {
		t.Fatalf("PublicBaseURL = %q, want empty", updated.PublicBaseURL)
	}
	if updated.UIPublicBaseURL != "" {
		t.Fatalf("UIPublicBaseURL = %q, want empty", updated.UIPublicBaseURL)
	}
	if updated.Mode != domain.PublicAccessModeRandomTunnel {
		t.Fatalf("Mode = %q", updated.Mode)
	}
	if updated.Enabled {
		t.Fatal("Enabled = true, want false")
	}
}

func TestUpdatePublicAccessPreservesFieldsWhenPartialUpdateKeepsValues(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		PublicAccess: domain.PublicAccessSettings{
			Enabled:          true,
			Provider:         "cloudflare",
			Mode:             domain.PublicAccessModeCustomDomain,
			ExposeAPI:        true,
			ExposeUI:         true,
			CustomDomain:     "gateway.lucadesign.uk",
			UIDomain:         "gateway-ui.lucadesign.uk",
			TunnelName:       "llm-protocol-gateway",
			CredentialsFile:  "/tmp/creds.json",
			TunnelConfigFile: "/tmp/tunnel-config.yml",
		},
	})

	updated := router.UpdatePublicAccess(domain.PublicAccessSettings{
		Enabled:          true,
		Provider:         "cloudflare",
		Mode:             domain.PublicAccessModeCustomDomain,
		ExposeAPI:        true,
		ExposeUI:         true,
		Expose:           "all",
		CustomDomain:     "gateway.lucadesign.uk",
		UIDomain:         "gateway-ui.lucadesign.uk",
		TunnelName:       "llm-protocol-gateway",
		CredentialsFile:  "/tmp/creds.json",
		TunnelConfigFile: "/tmp/tunnel-config.yml",
	})

	if updated.CustomDomain != "gateway.lucadesign.uk" {
		t.Fatalf("CustomDomain = %q", updated.CustomDomain)
	}
	if updated.UIDomain != "gateway-ui.lucadesign.uk" {
		t.Fatalf("UIDomain = %q", updated.UIDomain)
	}
	if updated.PublicBaseURL != "https://gateway.lucadesign.uk" {
		t.Fatalf("PublicBaseURL = %q", updated.PublicBaseURL)
	}
	if updated.UIPublicBaseURL != "https://gateway-ui.lucadesign.uk" {
		t.Fatalf("UIPublicBaseURL = %q", updated.UIPublicBaseURL)
	}
}
