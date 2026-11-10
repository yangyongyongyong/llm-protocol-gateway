package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// memoryUserStore is a minimal in-memory UserStore for permission tests.
type memoryUserStore struct {
	users  map[string]domain.User
	grants map[string][]string
}

func newMemoryUserStore(users ...domain.User) *memoryUserStore {
	store := &memoryUserStore{users: map[string]domain.User{}, grants: map[string][]string{}}
	for _, user := range users {
		store.users[user.ID] = user
	}
	return store
}

// newGrantedUserStore is newMemoryUserStore for the common "role=user plus these
// providers were granted" fixture.
func newGrantedUserStore(user domain.User, granted []string) *memoryUserStore {
	store := newMemoryUserStore(user)
	store.grants[user.ID] = granted
	return store
}

func (m *memoryUserStore) ListResourceGrants() (map[string][]string, error) {
	out := make(map[string][]string, len(m.grants))
	for userID, ids := range m.grants {
		out[userID] = append([]string{}, ids...)
	}
	return out, nil
}

func (m *memoryUserStore) SetResourceGrants(userID string, resourceIDs []string) error {
	m.grants[userID] = append([]string{}, resourceIDs...)
	return nil
}

func (m *memoryUserStore) ListUsers() ([]domain.User, error) {
	out := make([]domain.User, 0, len(m.users))
	for _, user := range m.users {
		out = append(out, user)
	}
	return out, nil
}

func (m *memoryUserStore) UserByID(id string) (domain.User, error) {
	if user, ok := m.users[id]; ok {
		return user, nil
	}
	return domain.User{}, http.ErrNoLocation
}

func (m *memoryUserStore) UserByUsername(username string) (domain.User, error) {
	for _, user := range m.users {
		if user.Username == username {
			return user, nil
		}
	}
	return domain.User{}, http.ErrNoLocation
}

// UserByGatewayID 只认 kind=peer 的行，与真实存储的行为一致：本地账号没有
// 网关身份，拿一个网关 ID 去查不该查到本地用户。
func (m *memoryUserStore) UserByGatewayID(gatewayID string) (domain.User, error) {
	for _, user := range m.users {
		if user.IsPeer() && user.GatewayID == gatewayID {
			return user, nil
		}
	}
	return domain.User{}, http.ErrNoLocation
}

func (m *memoryUserStore) CreateUser(user domain.User) error {
	m.users[user.ID] = user
	return nil
}

func (m *memoryUserStore) UpdateUser(user domain.User) error {
	m.users[user.ID] = user
	return nil
}

func (m *memoryUserStore) DeleteUser(id string) error {
	delete(m.users, id)
	return nil
}

func (m *memoryUserStore) TouchUserLogin(string, string) error  { return nil }
func (m *memoryUserStore) TouchUserActive(string, string) error { return nil }

// newRevokeTestServer reproduces the real-world shape of the bug: user "shouze"
// is granted hj-claude but NOT tuya-claude-pro, while still owning two keys that
// were bound to tuya-claude-pro back when the grant existed — one directly via
// its route, one via its fallback chain.
func newRevokeTestServer(t *testing.T) *Server {
	t.Helper()
	state := domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "hj-claude", Name: "hj-claude", Protocol: domain.ProtocolClaude, BaseURL: "https://hj.example.com"},
			{ID: "tuya-claude-pro", Name: "tuya-claude-pro", Protocol: domain.ProtocolClaude, BaseURL: "https://tuya.example.com"},
			// Self-created provider: ownership implies access even though it is
			// absent from AllowedProviderIDs (mirrors shouze's 第二个qoder).
			{ID: "第二个qoder", Name: "第二个qoder", Protocol: domain.ProtocolClaude, BaseURL: "https://qoder.example.com", OwnerUserID: "shouze"},
		},
		Routes: []domain.Route{
			{ID: "hj-claude-claude", ProviderID: "hj-claude", OutputProtocol: domain.ProtocolClaude, Enabled: true},
			{ID: "tuya-claude-pro-claude", ProviderID: "tuya-claude-pro", OutputProtocol: domain.ProtocolClaude, Enabled: true},
			{ID: "第二个qoder-claude", ProviderID: "第二个qoder", OutputProtocol: domain.ProtocolClaude, Enabled: true},
		},
	}
	server := NewServer(NewRouter(state), monitor.NewStore())
	server.userStore = newMemoryUserStore(domain.User{
		ID:                 "shouze",
		Username:           "shouze",
		Role:               domain.UserRoleUser,
		RoleID:             domain.RoleIDGrantee,
		AllowedResourceIDs: []string{"hj-claude"},
		Enabled:            true,
	})
	return server
}

func TestProviderAllowedForKeyOwnerAfterRevoke(t *testing.T) {
	server := newRevokeTestServer(t)

	userKey := domain.APIKey{ID: "hj-test", OwnerUserID: "shouze"}
	if !server.providerAllowedForKeyOwner(userKey, "hj-claude") {
		t.Fatal("granted provider must stay usable")
	}
	if server.providerAllowedForKeyOwner(userKey, "tuya-claude-pro") {
		t.Fatal("revoked provider must not be usable by the key owner")
	}

	// Admin-owned (legacy, empty owner) keys are unrestricted.
	adminKey := domain.APIKey{ID: "admin-key"}
	if !server.providerAllowedForKeyOwner(adminKey, "tuya-claude-pro") {
		t.Fatal("admin-owned key must not be restricted")
	}

	// Ownership implies access: a provider the user created themselves stays
	// usable even though it is not in AllowedProviderIDs. Revoking an unrelated
	// provider must not collaterally break these keys.
	if !server.providerAllowedForKeyOwner(userKey, "第二个qoder") {
		t.Fatal("self-created provider must stay usable by its owner")
	}
}

func TestAllowedProviderChainForKeyDropsRevokedFallback(t *testing.T) {
	server := newRevokeTestServer(t)

	// shouze's real "hj-test" key: preferred hj-claude, fallback tuya-claude-pro.
	key := domain.APIKey{
		ID:                  "hj-test",
		OwnerUserID:         "shouze",
		RouteID:             "hj-claude-claude",
		FallbackProviderIDs: []string{"tuya-claude-pro"},
	}
	chain := apiKeyProviderChain("hj-claude", key.FallbackProviderIDs)
	got := server.allowedProviderChainForKey(key, chain)
	if len(got) != 1 || got[0] != "hj-claude" {
		t.Fatalf("revoked fallback must be dropped, got %v", got)
	}

	// Admin keys keep the full chain.
	adminChain := server.allowedProviderChainForKey(domain.APIKey{ID: "admin"}, chain)
	if len(adminChain) != 2 {
		t.Fatalf("admin chain must be untouched, got %v", adminChain)
	}
}

// TestExecuteFailoverRejectsRevokedProvider covers shouze's "新 API 密钥 2":
// bound directly to a route whose provider was revoked. The request must be
// refused before any upstream call.
func TestExecuteFailoverRejectsRevokedProvider(t *testing.T) {
	server := newRevokeTestServer(t)
	route, err := server.router.RouteByID("tuya-claude-pro-claude")
	if err != nil {
		t.Fatalf("route lookup: %v", err)
	}
	key := domain.APIKey{
		ID:          "新-api-密钥-2",
		OwnerUserID: "shouze",
		RouteID:     "tuya-claude-pro-claude",
	}

	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	rec := httptest.NewRecorder()
	status, _, _, _, _, execErr := server.executeProtocolFlowWithFailover(
		rec, req, route, domain.RouteDecision{RouteID: route.ID, ProviderID: route.ProviderID},
		"claude-opus-5", map[string]any{"model": "claude-opus-5"},
		domain.ProtocolClaude, true, key, true,
	)
	if execErr == nil {
		t.Fatal("expected a permission error for a revoked provider")
	}
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; err=%v", status, execErr)
	}
	// The handlers surface this to the client and the request log; it must read
	// as a permission denial, not a 502 upstream failure.
	if got := upstreamErrorStatus(execErr); got != http.StatusForbidden {
		t.Fatalf("client-facing status = %d, want 403", got)
	}
	if upstreamErrorStatus(http.ErrHandlerTimeout) != http.StatusBadGateway {
		t.Fatal("ordinary upstream errors must stay 502")
	}
}

// TestExecuteFailoverKeepsGrantedProviderWhenActiveRevoked guards the ordering
// trap: a stale ActiveProviderID pointing at the revoked provider must not skip
// past the still-granted preferred provider.
func TestExecuteFailoverKeepsGrantedProviderWhenActiveRevoked(t *testing.T) {
	server := newRevokeTestServer(t)
	key := domain.APIKey{
		ID:                  "hj-test",
		OwnerUserID:         "shouze",
		RouteID:             "hj-claude-claude",
		FallbackProviderIDs: []string{"tuya-claude-pro"},
		ActiveProviderID:    "tuya-claude-pro",
	}
	chain := server.allowedProviderChainForKey(key, apiKeyProviderChain("hj-claude", key.FallbackProviderIDs))
	if idx := apiKeyEffectiveProviderIndex(key.ActiveProviderID, chain); idx > 0 {
		t.Fatalf("stale active provider must not index past the granted chain, got %d", idx)
	}
	if len(chain) != 1 || chain[0] != "hj-claude" {
		t.Fatalf("chain = %v, want [hj-claude]", chain)
	}
}

// grantedUser builds a role=user fixture; the allowed ids become the user's
// resource grants, which the built-in "user" role's "granted" semantics read.
func grantedUser(user domain.User, allowedProviderIDs []string) domain.User {
	user.AllowedResourceIDs = append([]string{}, allowedProviderIDs...)
	user.RoleID = domain.RoleIDGrantee
	return user
}
