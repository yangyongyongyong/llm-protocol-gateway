package gateway

import (
	"sync"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Two goroutines hitting ensureFreshChatGPTToken with an already-fresh token
// must both take the fast path; the per-provider mutex plus the post-lock
// re-read only matters on the slow path, but the fast path itself must never
// deadlock under contention.
func TestEnsureFreshChatGPTTokenConcurrentFastPath(t *testing.T) {
	server := &Server{}
	provider := domain.Provider{
		ID:       "p1",
		AuthType: domain.AuthTypeChatGPTOAuth,
		ChatGPTOAuth: &domain.ChatGPTOAuthCredential{
			AccessToken:  "live-token",
			RefreshToken: "rt",
			// Far future: no refresh attempt, both callers pass through.
			ExpiresAt: "2099-01-01T00:00:00Z",
		},
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			refreshed, err := server.ensureFreshChatGPTToken(provider)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if refreshed.ChatGPTOAuth.AccessToken != "live-token" {
				t.Errorf("token mutated: %q", refreshed.ChatGPTOAuth.AccessToken)
			}
		}()
	}
	wg.Wait()
}

// The proactive sweep helper must skip providers whose token is still far
// from expiry (no refresh) and providers without expiry data.
func TestRefreshAllChatGPTTokensSkipsFreshTokens(t *testing.T) {
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{
		{ID: "fresh", AuthType: domain.AuthTypeChatGPTOAuth, ChatGPTOAuth: &domain.ChatGPTOAuthCredential{
			RefreshToken: "rt", ExpiresAt: "2099-01-01T00:00:00Z",
		}},
		{ID: "no-expiry", AuthType: domain.AuthTypeChatGPTOAuth, ChatGPTOAuth: &domain.ChatGPTOAuthCredential{
			RefreshToken: "rt",
		}},
		{ID: "api", AuthType: domain.AuthTypeAPIKey},
	}})
	server := &Server{router: router}
	// A due-for-refresh provider would call ensureFreshChatGPTToken → refresh
	// against auth.openai.com; none of the above qualify, so this must be a
	// silent no-op.
	server.refreshAllChatGPTTokens()
}
