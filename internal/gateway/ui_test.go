package gateway

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindWebDistDirFromEnv(t *testing.T) {
	tmp := t.TempDir()
	dist := filepath.Join(tmp, "web-dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_WEB_DIST", dist)
	got := findWebDistDir()
	if got != dist {
		t.Fatalf("findWebDistDir()=%q want %q", got, dist)
	}
}

func TestWebDistIfPresent(t *testing.T) {
	tmp := t.TempDir()
	if webDistIfPresent(tmp) != "" {
		t.Fatal("expected empty without index.html")
	}
	if err := os.WriteFile(filepath.Join(tmp, "index.html"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := webDistIfPresent(tmp); got != tmp {
		t.Fatalf("got %q want %q", got, tmp)
	}
}

func TestPwaInstallPathsStayFetchable(t *testing.T) {
	// manifest / service worker 被 SPA 兜底成 index.html 时浏览器判定不可安装；
	// 被登录拦截时未登录页也装不上，两白名单必须同时放行。
	for _, path := range []string{"/manifest.webmanifest", "/sw.js", "/registerSW.js"} {
		if !shouldServeUIFallback(path) {
			t.Errorf("shouldServeUIFallback(%q) = false, want true", path)
		}
		if !isAdminAuthPublicPath(path) {
			t.Errorf("isAdminAuthPublicPath(%q) = false, want true", path)
		}
	}
}
