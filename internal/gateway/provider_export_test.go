package gateway

import (
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestExportProvidersAllAndByIDs(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "p1", Name: "One", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://a.example/v1", APIKeySource: "env:A"},
			{ID: "p2", Name: "Two", Protocol: domain.ProtocolClaude, BaseURL: "https://b.example", AuthType: domain.AuthTypeClaudeOAuth, ClaudeOAuth: &domain.ClaudeOAuthCredential{
				AccessToken: "access", RefreshToken: "refresh", AccountLabel: "me@example.com",
			}},
		},
	})

	all, errs := router.ExportProviders(nil)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if all.Version != providersExportVersion || len(all.Providers) != 2 {
		t.Fatalf("all export: version=%d len=%d", all.Version, len(all.Providers))
	}
	if all.Providers[1].ClaudeOAuth == nil || all.Providers[1].ClaudeOAuth.AccessToken != "access" {
		t.Fatalf("export should include stored oauth secrets")
	}

	partial, errs := router.ExportProviders([]string{"p2", "missing", "p2"})
	if len(partial.Providers) != 1 || partial.Providers[0].ID != "p2" {
		t.Fatalf("partial export providers=%v", partial.Providers)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "missing") {
		t.Fatalf("expected missing id error, got %v", errs)
	}
}

func TestImportProvidersCreateUpdateByIDAndName(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "keep", Name: "Keep Me", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://keep.example/v1"},
			{ID: "old-id", Name: "By ID", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://old.example/v1", APIKeySource: "old-key"},
			{ID: "name-id", Name: "By Name", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://name.example/v1"},
		},
	})

	result := router.ImportProviders(ProvidersExportBundle{
		Version: providersExportVersion,
		Providers: []domain.Provider{
			{ID: "old-id", Name: "Updated By ID", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://new.example/v1", APIKeySource: "new-key", DefaultModel: "gpt-4o", Models: []domain.Model{{ID: "gpt-4o"}}},
			{Name: "By Name", Protocol: domain.ProtocolClaude, BaseURL: "https://claude.example", AuthType: domain.AuthTypeAPIKey},
			{ID: "brand-new", Name: "Brand New", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://brand.example/v1"},
			{Name: "", BaseURL: "https://bad.example"},
		},
	})

	if len(result.Updated) != 2 {
		t.Fatalf("updated=%v", result.Updated)
	}
	if len(result.Created) != 1 || result.Created[0] != "brand-new" {
		t.Fatalf("created=%v", result.Created)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("errors=%v", result.Errors)
	}

	state := router.State()
	if len(state.Providers) != 4 {
		t.Fatalf("provider count=%d", len(state.Providers))
	}
	keep, err := router.ProviderByID("keep")
	if err != nil || keep.Name != "Keep Me" {
		t.Fatalf("unrelated provider should remain untouched: %+v err=%v", keep, err)
	}
	updated, err := router.ProviderByID("old-id")
	if err != nil || updated.Name != "Updated By ID" || updated.APIKeySource != "new-key" || updated.DefaultModel != "gpt-4o" {
		t.Fatalf("id upsert failed: %+v err=%v", updated, err)
	}
	byName, err := router.ProviderByID("name-id")
	if err != nil || byName.Protocol != domain.ProtocolClaude || byName.BaseURL != "https://claude.example" {
		t.Fatalf("name upsert failed: %+v err=%v", byName, err)
	}
	brand, err := router.ProviderByID("brand-new")
	if err != nil || brand.Name != "Brand New" {
		t.Fatalf("create with id failed: %+v err=%v", brand, err)
	}
}

func TestImportProvidersNameMatchAndOAuthSecretMerge(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{
			{
				ID: "oauth-1", Name: "Claude Sub", Protocol: domain.ProtocolClaude, AuthType: domain.AuthTypeClaudeOAuth,
				BaseURL: "https://api.anthropic.com",
				ClaudeOAuth: &domain.ClaudeOAuthCredential{AccessToken: "secret-access", RefreshToken: "secret-refresh", AccountLabel: "old"},
			},
		},
	})

	// Metadata-only import (as if from redacted UI state) must not wipe secrets.
	result := router.ImportProviders(ProvidersExportBundle{
		Version: providersExportVersion,
		Providers: []domain.Provider{
			{
				Name: "Claude Sub", Protocol: domain.ProtocolClaude, AuthType: domain.AuthTypeClaudeOAuth,
				ClaudeOAuth: &domain.ClaudeOAuthCredential{Connected: true, AccountLabel: "new-label", ExpiresAt: "2099-01-01T00:00:00Z"},
			},
		},
	})
	if len(result.Updated) != 1 || len(result.Errors) != 0 {
		t.Fatalf("result=%+v", result)
	}
	provider, err := router.ProviderByID("oauth-1")
	if err != nil {
		t.Fatal(err)
	}
	if provider.ClaudeOAuth == nil || provider.ClaudeOAuth.AccessToken != "secret-access" || provider.ClaudeOAuth.RefreshToken != "secret-refresh" {
		t.Fatalf("secrets wiped: %+v", provider.ClaudeOAuth)
	}
	if provider.ClaudeOAuth.AccountLabel != "new-label" {
		t.Fatalf("metadata not merged: %+v", provider.ClaudeOAuth)
	}

	// Full secret import replaces tokens.
	result = router.ImportProviders(ProvidersExportBundle{
		Providers: []domain.Provider{
			{
				ID: "oauth-1", Name: "Claude Sub", Protocol: domain.ProtocolClaude, AuthType: domain.AuthTypeClaudeOAuth,
				ClaudeOAuth: &domain.ClaudeOAuthCredential{AccessToken: "new-access", RefreshToken: "new-refresh"},
			},
		},
	})
	if len(result.Updated) != 1 {
		t.Fatalf("result=%+v", result)
	}
	provider, _ = router.ProviderByID("oauth-1")
	if provider.ClaudeOAuth.AccessToken != "new-access" || provider.ClaudeOAuth.RefreshToken != "new-refresh" {
		t.Fatalf("secret replace failed: %+v", provider.ClaudeOAuth)
	}
}

func TestImportProvidersRejectsBadVersionAndProtocol(t *testing.T) {
	router := NewRouter(domain.GatewayState{})
	result := router.ImportProviders(ProvidersExportBundle{Version: 99, Providers: []domain.Provider{}})
	if len(result.Errors) != 1 {
		t.Fatalf("want version error, got %+v", result)
	}
	result = router.ImportProviders(ProvidersExportBundle{
		Providers: []domain.Provider{{Name: "x", Protocol: "nope", BaseURL: "https://x"}},
	})
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "protocol") {
		t.Fatalf("want protocol error, got %+v", result)
	}
}

func TestParseProviderExportIDs(t *testing.T) {
	ids := ParseProviderExportIDs(" a, b ,,c ")
	if len(ids) != 3 || ids[0] != "a" || ids[2] != "c" {
		t.Fatalf("ids=%v", ids)
	}
	if ParseProviderExportIDs("  ") != nil {
		t.Fatalf("empty should be nil")
	}
}
