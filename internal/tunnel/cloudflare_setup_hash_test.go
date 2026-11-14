package tunnel

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNamedTunnelNameIsStableAndCollisionResistant(t *testing.T) {
	a := NamedTunnelName("gw_aaaaaaaaaaaaaaaa")
	b := NamedTunnelName("gw_bbbbbbbbbbbbbbbb")
	if a == b {
		t.Fatalf("two gateway identities collided: %q", a)
	}
	if again := NamedTunnelName("gw_aaaaaaaaaaaaaaaa"); again != a {
		t.Fatalf("name is not stable: %q vs %q", again, a)
	}
	if !strings.HasPrefix(a, "llm-protocol-gateway-") || len(strings.TrimPrefix(a, "llm-protocol-gateway-")) < 16 {
		t.Fatalf("expected a long stable suffix, got %q", a)
	}
}

func TestNamedTunnelConfigFileIsPerGateway(t *testing.T) {
	dir := t.TempDir()
	a := namedTunnelConfigFile(dir, "gw_aaaaaaaaaaaaaaaa")
	b := namedTunnelConfigFile(dir, "gw_bbbbbbbbbbbbbbbb")
	if a == b || filepath.Dir(a) != dir || filepath.Dir(b) != dir {
		t.Fatalf("config paths must differ within the app dir: %q %q", a, b)
	}
}

func TestEffectiveTunnelNameMigratesLegacySharedName(t *testing.T) {
	want := NamedTunnelName("gw_aaaaaaaaaaaaaaaa")
	if got := effectiveTunnelName("", "gw_aaaaaaaaaaaaaaaa"); got != want {
		t.Fatalf("empty name = %q, want %q", got, want)
	}
	if got := effectiveTunnelName("llm-protocol-gateway", "gw_aaaaaaaaaaaaaaaa"); got != want {
		t.Fatalf("legacy shared name must migrate to %q, got %q", want, got)
	}
	if got := effectiveTunnelName("my-custom-tunnel", "gw_aaaaaaaaaaaaaaaa"); got != "my-custom-tunnel" {
		t.Fatalf("custom name must stay explicit, got %q", got)
	}
}
