package gateway

import (
	"reflect"
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

func TestWorkBuddyRoundTripKeepsMaskedPhone(t *testing.T) {
	stored := domain.WorkBuddyCredential{
		AccessToken: "at", RefreshToken: "rt", AccountLabel: "herui",
		Phone: "18012343664", UserID: "uid-wb", Connected: true,
	}
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{{
		ID: "wb", Name: "WorkBuddy 订阅-herui", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeWorkBuddy, WorkBuddy: &stored,
	}}})
	bundle, errs := router.ExportProviders([]string{"wb"})
	if len(errs) != 0 || len(bundle.Providers) != 1 || bundle.Providers[0].WorkBuddy == nil {
		t.Fatalf("export = %+v errs=%v", bundle, errs)
	}
	if bundle.Providers[0].WorkBuddy.Phone != "180****3664" {
		t.Fatalf("exported phone = %q", bundle.Providers[0].WorkBuddy.Phone)
	}
	fresh := NewRouter(domain.GatewayState{})
	result := fresh.ImportProviders(bundle)
	if len(result.Errors) != 0 || len(result.Created) != 1 {
		t.Fatalf("import result = %+v", result)
	}
	imported, err := fresh.ProviderByID("wb")
	if err != nil || imported.WorkBuddy == nil || imported.WorkBuddy.Phone != "180****3664" {
		t.Fatalf("imported = %+v err=%v", imported.WorkBuddy, err)
	}
}

func TestExportProvidersIncludesCustomConnectors(t *testing.T) {
	router := NewRouter(domain.GatewayState{CustomProviders: []domain.CustomProvider{{
		ID: "ocean", Name: "Ocean", Protocol: domain.ProtocolOpenAIResponses, AuthHeader: "api-key",
		Script:    domain.ProviderScript{Language: "python", Entrypoint: "adapter.py", Source: "print('ocean')", UpstreamBaseURL: "https://secret.example"},
		ScriptDir: "/tmp/should-not-export",
	}}, Providers: []domain.Provider{{
		ID: "p-ocean", Name: "Ocean dev", Protocol: domain.ProtocolOpenAIResponses, AuthType: domain.AuthTypeAPIKey,
		BaseURL: "http://127.0.0.1:12345/v1", APIKeySource: "literal:key", CustomProviderID: "ocean",
		ProviderScript: &domain.ProviderScript{Language: "python", Entrypoint: "adapter.py", Source: "print('instance')", UpstreamBaseURL: "https://vendor.example"},
	}}})
	bundle, errs := router.ExportProviders([]string{"p-ocean"})
	if len(errs) != 0 {
		t.Fatalf("export errors = %v", errs)
	}
	if len(bundle.CustomProviders) != 1 || bundle.CustomProviders[0].ID != "ocean" {
		t.Fatalf("custom connectors not exported: %+v", bundle.CustomProviders)
	}
	custom := bundle.CustomProviders[0]
	if custom.Script.Source != "print('ocean')" || custom.Script.UpstreamBaseURL != "" || custom.ScriptDir != "" {
		t.Fatalf("custom connector leaked runtime fields: %+v", custom)
	}
}

func TestImportProvidersRestoresCustomConnectors(t *testing.T) {
	fresh := NewRouter(domain.GatewayState{})
	result := fresh.ImportProviders(ProvidersExportBundle{Version: providersExportVersion,
		CustomProviders: []domain.CustomProvider{{
			ID: "ocean", Name: "Ocean", Protocol: domain.ProtocolOpenAIResponses, AuthHeader: "api-key",
			Script: domain.ProviderScript{Language: "python", Entrypoint: "adapter.py", Source: "print('ocean')"},
		}},
		Providers: []domain.Provider{{
			ID: "p-ocean", Name: "Ocean dev", Protocol: domain.ProtocolOpenAIResponses, AuthType: domain.AuthTypeAPIKey,
			BaseURL: "http://127.0.0.1:12345/v1", APIKeySource: "literal:key", CustomProviderID: "ocean",
			ProviderScript: &domain.ProviderScript{Language: "python", Entrypoint: "adapter.py", Source: "print('ocean')", UpstreamBaseURL: "https://vendor.example"},
		}},
	})
	if len(result.Errors) != 0 || len(result.Created) != 1 {
		t.Fatalf("import result = %+v", result)
	}
	custom, err := fresh.CustomProviderByID("ocean")
	if err != nil || custom.Script.Source != "print('ocean')" {
		t.Fatalf("custom connector not restored: %+v err=%v", custom, err)
	}
	provider, err := fresh.ProviderByID("p-ocean")
	if err != nil || provider.CustomProviderID != "ocean" || provider.ProviderScript == nil {
		t.Fatalf("provider import lost connector: %+v err=%v", provider, err)
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
				BaseURL:     "https://api.anthropic.com",
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

func TestImportProvidersMergesChatGPTOAuth(t *testing.T) {
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{
		{
			ID: "cg", Name: "cccc", Protocol: domain.ProtocolOpenAIResponses, AuthType: domain.AuthTypeChatGPTOAuth,
			BaseURL: "https://chatgpt.com/backend-api/codex/responses",
			ChatGPTOAuth: &domain.ChatGPTOAuthCredential{
				AccessToken: "expired", RefreshToken: "old-refresh", ExpiresAt: "2026-10-01T00:00:00+08:00",
			},
		},
	},
	})
	result := router.ImportProviders(ProvidersExportBundle{
		Version: providersExportVersion,
		Providers: []domain.Provider{{
			ID: "cccc", Name: "cccc", Protocol: domain.ProtocolOpenAIResponses, AuthType: domain.AuthTypeChatGPTOAuth,
			BaseURL: "https://chatgpt.com/backend-api/codex/responses",
			ChatGPTOAuth: &domain.ChatGPTOAuthCredential{
				AccessToken: "fresh", RefreshToken: "fresh-refresh", ExpiresAt: "2026-10-13T20:40:02+08:00",
				ChatGPTAccountID: "acct-1", AccountLabel: "me@example.com",
			},
		}},
	})
	if len(result.Updated) != 1 || len(result.Created) != 0 || len(result.Errors) != 0 {
		t.Fatalf("result=%+v", result)
	}
	provider, err := router.ProviderByID("cg")
	if err != nil {
		t.Fatal(err)
	}
	cred := provider.ChatGPTOAuth
	if cred == nil || cred.AccessToken != "fresh" || cred.RefreshToken != "fresh-refresh" || cred.ChatGPTAccountID != "acct-1" {
		t.Fatalf("bundle tokens not merged: %+v", cred)
	}

	// A bundle without tokens (redacted copy) must not wipe the live pair.
	result = router.ImportProviders(ProvidersExportBundle{Providers: []domain.Provider{{
		ID: "cg", Name: "cccc", AuthType: domain.AuthTypeChatGPTOAuth,
		ChatGPTOAuth: &domain.ChatGPTOAuthCredential{Connected: true, AccountLabel: "renamed@example.com"},
	}}})
	if len(result.Updated) != 1 {
		t.Fatalf("result=%+v", result)
	}
	provider, _ = router.ProviderByID("cg")
	if provider.ChatGPTOAuth.AccessToken != "fresh" || provider.ChatGPTOAuth.AccountLabel != "renamed@example.com" || provider.ChatGPTOAuth.Connected {
		t.Fatalf("metadata-only import disturbed the pair: %+v", provider.ChatGPTOAuth)
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

// A Qoder browser sign-in is more than its tokens: the uid and machine id sign
// every request and name the account a daily check-in belongs to. Export is a
// full backup, so a restore has to come back able to forward.
func TestQoderCredentialSurvivesAnExportRoundTrip(t *testing.T) {
	stored := domain.QoderCredential{
		SignInMode: "device", UID: "uid-7", Email: "me@example.com", Name: "Me",
		AccessToken: "job", RefreshToken: "jobr", ExpiresAt: "2026-11-01T00:00:00Z",
		DeviceToken: "dev", DeviceRefresh: "devr", MachineID: "mach", AccountLabel: "me@example.com",
	}
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{
		{ID: "q", Name: "Qoder", AuthType: domain.AuthTypeQoderCN, QoderPAT: &stored},
	}})
	bundle, errs := router.ExportProviders(nil)
	if len(errs) != 0 {
		t.Fatalf("export errors: %v", errs)
	}
	fresh := NewRouter(domain.GatewayState{})
	if result := fresh.ImportProviders(bundle); len(result.Created) != 1 {
		t.Fatalf("import result = %+v", result)
	}
	imported, err := fresh.ProviderByID("q")
	if err != nil || imported.QoderPAT == nil {
		t.Fatalf("imported provider = %+v (%v)", imported, err)
	}
	if !reflect.DeepEqual(*imported.QoderPAT, stored) {
		t.Fatalf("round trip lost fields:\n got %+v\nwant %+v", *imported.QoderPAT, stored)
	}
}

// A bundle taken without credentials says nothing about a live sign-in: it must
// not claim one, and it must not overwrite the account the card already is.
func TestQoderMetadataOnlyImportKeepsTheLiveAccount(t *testing.T) {
	existing := domain.QoderCredential{SignInMode: "device", UID: "uid-7", AccessToken: "job", DeviceToken: "dev"}
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{
		{ID: "q", Name: "Qoder", AuthType: domain.AuthTypeQoderCN, QoderPAT: &existing},
	}})
	result := router.ImportProviders(ProvidersExportBundle{
		Version: providersExportVersion,
		Providers: []domain.Provider{{ID: "q", Name: "Qoder", AuthType: domain.AuthTypeQoderCN,
			QoderPAT: &domain.QoderCredential{AccountLabel: "old@example.com", Connected: true}}},
	})
	if len(result.Updated) != 1 {
		t.Fatalf("import result = %+v", result)
	}
	provider, _ := router.ProviderByID("q")
	if provider.QoderPAT.DeviceToken != "dev" || provider.QoderPAT.AccessToken != "job" || provider.QoderPAT.UID != "uid-7" {
		t.Fatalf("a metadata-only import must not disturb the live credential: %+v", provider.QoderPAT)
	}
	if provider.QoderPAT.Connected {
		t.Fatal("a bundle without tokens must not assert a live sign-in")
	}
}

// Every connection kind the console can create has to come back through import:
// a bundle that answers "unsupported authType" for the subscriptions added after
// this list was written is a backup that cannot be restored.
func TestImportAcceptsEveryConsoleAuthType(t *testing.T) {
	authTypes := []string{
		"", domain.AuthTypeAPIKey, domain.AuthTypeClaudeOAuth, domain.AuthTypeCursorOAuth,
		domain.AuthTypeChatGPTOAuth, domain.AuthTypeQoderPAT, domain.AuthTypeQoderCN,
		domain.AuthTypeWorkBuddy, domain.AuthTypeWorkBuddyAI, domain.AuthTypeZCode,
		domain.AuthTypeZCodeBigModel, domain.AuthTypeTraeCN, domain.AuthTypeTraeGlobal,
		domain.AuthTypeOpenCodeZenFree,
	}
	router := NewRouter(domain.GatewayState{})
	bundle := ProvidersExportBundle{Version: providersExportVersion}
	for index, authType := range authTypes {
		name := authType
		if name == "" {
			name = "default"
		}
		bundle.Providers = append(bundle.Providers, domain.Provider{
			ID: "p" + string(rune('a'+index)), Name: name, Protocol: domain.ProtocolOpenAIChat,
			BaseURL: "https://example.invalid/v1", AuthType: authType, APIKeySource: "env:X",
		})
	}
	result := router.ImportProviders(bundle)
	if len(result.Errors) != 0 {
		t.Fatalf("import rejected auth types: %v", result.Errors)
	}
	if len(result.Created) != len(authTypes) {
		t.Fatalf("created %d of %d", len(result.Created), len(authTypes))
	}
}
