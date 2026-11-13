package gateway

import (
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 旧的 CLI 自检被打断后，selfcheck-* 密钥会留在库里；启动时的清扫必须只删它们，
// 用户自己的密钥一个都不能动。这是 selfcheck.go 收缩成清理逻辑后唯一的行为。
func TestSweepSelfcheckLeftoversRemovesOnlySelfcheckKeys(t *testing.T) {
	db := newProberTestStore(t)
	server, _ := newQuotaTestServer(t, "http://127.0.0.1:1")
	server.apiKeyStore = db
	server.router = NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "p1", Name: "Claude", Protocol: domain.ProtocolClaude, AuthType: domain.AuthTypeAPIKey,
			BaseURL: "http://127.0.0.1:1/v1", APIKeySource: "literal:sk", DefaultModel: "claude-sonnet-4-5",
		}},
	})
	route, err := server.router.AddRoute(domain.Route{
		Name: "r", ProviderID: "p1", OutputProtocol: domain.ProtocolClaude, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	userKey, err := server.router.AddAPIKey(domain.APIKey{Name: "我的密钥", RouteID: route.ID, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAPIKey(userKey); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"selfcheck-p1-claude", "selfcheck-sc-cli-1-p1-openai_chat"} {
		key, err := server.router.AddAPIKey(domain.APIKey{Name: name, RouteID: route.ID, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.CreateAPIKey(key); err != nil {
			t.Fatal(err)
		}
	}

	server.SweepSelfcheckLeftovers()

	names := map[string]bool{}
	for _, key := range server.router.State().APIKeys {
		names[key.Name] = true
	}
	if !names["我的密钥"] || len(names) != 1 {
		t.Fatalf("only the user's own key may survive, got %v", names)
	}
	persisted, err := db.APIKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 1 || persisted[0].Name != "我的密钥" {
		t.Fatalf("the sweep must persist the deletion, got %+v", persisted)
	}
}

// 清扫在空库/无 apiKeyStore 的服务器上也不能 panic（桌面端与测试都这样构造）。
func TestSweepSelfcheckLeftoversIsQuietOnEmptyState(t *testing.T) {
	server, _ := newQuotaTestServer(t, "http://127.0.0.1:1")
	server.SweepSelfcheckLeftovers()
}
