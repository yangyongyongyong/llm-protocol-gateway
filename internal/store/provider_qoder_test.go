package store

import (
	"path/filepath"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Guards the paired Save/load switches on provider.AuthType: omitting either
// case loses the credential silently, and because the load switch has a
// default: arm that maps stray tokens onto ClaudeOAuth, a missing load case
// mis-types the credential rather than dropping it.
func TestProviderQoderPATRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	state := domain.GatewayState{
		Providers: []domain.Provider{{
			ID:       "qoder",
			Name:     "Qoder",
			Protocol: domain.ProtocolOpenAIChat,
			BaseURL:  "https://api2-v2.qoder.sh/model/v1",
			AuthType: domain.AuthTypeQoderPAT,
			QoderPAT: &domain.QoderPATCredential{
				AccessToken:  "jt-job-token",
				RefreshToken: "pt-personal-access-token",
				ExpiresAt:    "2026-01-01T00:00:00Z",
				AccountLabel: "Qoder",
			},
		}},
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load(domain.GatewayState{})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(loaded.Providers))
	}
	provider := loaded.Providers[0]
	if provider.AuthType != domain.AuthTypeQoderPAT {
		t.Fatalf("authType = %q, want %q", provider.AuthType, domain.AuthTypeQoderPAT)
	}
	if provider.ClaudeOAuth != nil {
		t.Fatal("credential was mis-typed as ClaudeOAuth by the load default: arm")
	}
	if provider.QoderPAT == nil {
		t.Fatal("QoderPAT credential did not survive the round trip")
	}
	if provider.QoderPAT.RefreshToken != "pt-personal-access-token" {
		t.Errorf("PAT = %q, want %q", provider.QoderPAT.RefreshToken, "pt-personal-access-token")
	}
	if provider.QoderPAT.AccessToken != "jt-job-token" {
		t.Errorf("job token = %q, want %q", provider.QoderPAT.AccessToken, "jt-job-token")
	}
	if provider.QoderPAT.ExpiresAt != "2026-01-01T00:00:00Z" {
		t.Errorf("expiry = %q", provider.QoderPAT.ExpiresAt)
	}
	if provider.QoderPAT.AccountLabel != "Qoder" {
		t.Errorf("accountLabel = %q", provider.QoderPAT.AccountLabel)
	}
}
