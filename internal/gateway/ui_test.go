package gateway

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// 控制台的每个页面都要能在服务器上刷新出来。
//
// 前端是单页应用：侧边栏切换只改 history，不发请求，所以后端白名单漏一条
// 页面照样能点进去；但在那一页按 F5（或把链接发给别人）就是一个裸 404。
// 这条测试直接读 web/src/lib.tsx 的 navItems，把它和 uiNavPaths 对齐，
// 以后新增页面忘了同步后端就会红。
func TestUINavPathsCoverFrontendNav(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "lib.tsx"))
	if err != nil {
		t.Fatalf("read frontend nav definitions: %v", err)
	}
	start := strings.Index(string(source), "export const navItems")
	if start < 0 {
		t.Fatal("web/src/lib.tsx 里找不到 navItems")
	}
	// navItems 是一个以 "] as const;" 结尾的数组字面量。
	block := string(source)[start:]
	if end := strings.Index(block, "] as const;"); end >= 0 {
		block = block[:end]
	}
	ids := regexp.MustCompile(`\{ id: '([a-z0-9-]+)', label:`).FindAllStringSubmatch(block, -1)
	if len(ids) == 0 {
		t.Fatal("没能从 navItems 里解析出任何页面 id")
	}
	for _, match := range ids {
		// input-providers 就是首屏 "/"，navPathForID 也这么映射。
		path := "/" + match[1]
		if match[1] == "input-providers" {
			path = "/"
		}
		if !shouldServeUIFallback(path) {
			t.Errorf("前端有页面 %s，后端 uiNavPaths 里没有它：侧边栏点得进去，刷新就 404", path)
		}
	}
}
