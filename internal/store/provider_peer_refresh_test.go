package store

import (
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 网关互联的续签凭据必须跟 peer provider 一起跨重启保留，否则子网关只能
// 退回用户名密码重新注册。它走 oauth_refresh_token 列，不新增表结构。
func TestProviderPeerRefreshTokenRoundTrip(t *testing.T) {
	t.Parallel()
	loaded := saveCloseReopen(t, domain.GatewayState{
		Providers: []domain.Provider{{
			ID:               "parent",
			Name:             "parent",
			Protocol:         domain.ProtocolOpenAIChat,
			BaseURL:          "https://parent.example.com",
			AuthType:         domain.AuthTypePeer,
			APIKeySource:     "literal:gwpeer-traffic",
			PeerRefreshToken: "gwrefresh-renew",
		}},
	})
	if len(loaded.Providers) != 1 {
		t.Fatalf("providers = %+v", loaded.Providers)
	}
	provider := loaded.Providers[0]
	if provider.APIKeySource != "literal:gwpeer-traffic" {
		t.Fatalf("traffic token = %q", provider.APIKeySource)
	}
	if provider.PeerRefreshToken != "gwrefresh-renew" {
		t.Fatalf("refresh token = %q, want persisted value", provider.PeerRefreshToken)
	}
}
