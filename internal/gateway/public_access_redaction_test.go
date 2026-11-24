package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestTunnelRuntimeResponseNeverContainsTunnelToken(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), nil)
	settings := server.withTunnelRuntime(domain.PublicAccessSettings{
		Enabled:     true,
		Provider:    "cloudflare",
		Mode:        domain.PublicAccessModeCustomDomain,
		TunnelToken: "eyJhIjoi-never-return-this",
	})
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "never-return-this") || strings.Contains(string(raw), "tunnelToken") {
		t.Fatalf("public status response leaked TunnelToken: %s", raw)
	}
}
