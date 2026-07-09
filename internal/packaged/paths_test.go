package packaged

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCloudflaredPrefersEnv(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "cloudflared")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_CLOUDFLARED", bin)
	got, err := Cloudflared()
	if err != nil {
		t.Fatal(err)
	}
	if got != bin {
		t.Fatalf("got %q want %q", got, bin)
	}
}

func TestBunPrefersResources(t *testing.T) {
	tmp := t.TempDir()
	res := filepath.Join(tmp, "Resources")
	binDir := filepath.Join(res, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "bun")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_APP_RESOURCES", res)
	t.Setenv("GATEWAY_BUN", "")
	got, err := Bun()
	if err != nil {
		t.Fatal(err)
	}
	if got != bin {
		t.Fatalf("got %q want %q", got, bin)
	}
}

func TestCursorBridgeDirFromResources(t *testing.T) {
	tmp := t.TempDir()
	res := filepath.Join(tmp, "Resources")
	bridge := filepath.Join(res, "cursor-bridge")
	if err := os.MkdirAll(bridge, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bridge, "standalone.ts"), []byte("//"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_APP_RESOURCES", res)
	t.Setenv("GATEWAY_CURSOR_BRIDGE_DIR", "")
	got := CursorBridgeDir()
	if got != bridge {
		t.Fatalf("got %q want %q", got, bridge)
	}
}

func TestWebDistDirFromResources(t *testing.T) {
	tmp := t.TempDir()
	res := filepath.Join(tmp, "Resources")
	dist := filepath.Join(res, "web-dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_APP_RESOURCES", res)
	t.Setenv("GATEWAY_WEB_DIST", "")
	got := WebDistDir()
	if got != dist {
		t.Fatalf("got %q want %q", got, dist)
	}
}
