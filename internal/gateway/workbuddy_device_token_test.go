package gateway

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestResolveWorkBuddyDeviceTokenCredentialWins(t *testing.T) {
	t.Setenv("WORKBUDDY_DEVICE_TOKEN", "env-token")
	cred := &domain.WorkBuddyCredential{DeviceToken: "acct-token"}
	if got := resolveWorkBuddyDeviceToken(cred); got != "acct-token" {
		t.Fatalf("token = %q, want acct-token", got)
	}
}

func TestResolveWorkBuddyDeviceTokenEnvFallback(t *testing.T) {
	t.Setenv("WORKBUDDY_DEVICE_TOKEN", "env-only")
	if got := resolveWorkBuddyDeviceToken(nil); got != "env-only" {
		t.Fatalf("token = %q, want env-only", got)
	}
}

func TestResolveWorkBuddyDeviceTokenFileFallback(t *testing.T) {
	t.Setenv("WORKBUDDY_DEVICE_TOKEN", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "device_token")
	if err := os.WriteFile(path, []byte("  file-token  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKBUDDY_DEVICE_TOKEN_FILE", path)
	if got := resolveWorkBuddyDeviceToken(nil); got != "file-token" {
		t.Fatalf("token = %q, want file-token", got)
	}
}

func TestResolveWorkBuddyDeviceTokenOmitWhenMissing(t *testing.T) {
	t.Setenv("WORKBUDDY_DEVICE_TOKEN", "")
	t.Setenv("WORKBUDDY_DEVICE_TOKEN_FILE", "")
	if got := resolveWorkBuddyDeviceToken(&domain.WorkBuddyCredential{}); got != "" {
		t.Fatalf("token = %q, want empty", got)
	}
}
