package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestRedactProviderHidesAPIKeySourceButReportsConfigured(t *testing.T) {
	redacted := redactProviderForClient(domain.Provider{
		ID: "p1", Name: "p1", Protocol: domain.ProtocolOpenAIChat,
		APIKeySource: "literal:sk-never-return-this",
	})
	if redacted.APIKeySource != "" {
		t.Fatalf("apiKeySource leaked: %q", redacted.APIKeySource)
	}
	if !redacted.APIKeyConfigured {
		t.Fatal("console needs to know that a key is configured")
	}
	raw, err := json.Marshal(redacted)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-never-return-this") {
		t.Fatalf("redacted JSON leaked the key: %s", raw)
	}
	if !strings.Contains(string(raw), `"apiKeyConfigured":true`) {
		t.Fatalf("redacted JSON lost configured flag: %s", raw)
	}
}

func TestUpdateProviderEmptyAPIKeySourceKeepsCredential(t *testing.T) {
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{{
		ID: "p1", Name: "old", Protocol: domain.ProtocolOpenAIChat,
		BaseURL: "https://example.com/v1", APIKeySource: "literal:keep-me",
	}}})
	updated, err := router.UpdateProvider("p1", domain.Provider{Name: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.APIKeySource != "literal:keep-me" {
		t.Fatalf("empty APIKeySource patch erased the credential: %q", updated.APIKeySource)
	}
	updated, err = router.UpdateProvider("p1", domain.Provider{APIKeySource: "literal:new-key"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.APIKeySource != "literal:new-key" {
		t.Fatalf("non-empty APIKeySource patch did not replace the credential: %q", updated.APIKeySource)
	}
}
