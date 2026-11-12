package gateway

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/packaged"
)

// uiNavPaths 是控制台里能被直接打开（刷新、粘贴链接、书签）的页面路径。
//
// 它必须和 web/src/lib.tsx 的 navItems 一一对应：页面本身是单页应用，这些
// 路径在服务器上并不存在文件，全靠这里回退到 index.html。漏掉一条的症状很
// 具体——侧边栏点进去正常（那是前端路由，不发请求），但在那一页按 F5 或者
// 把链接发给别人就变成 404。曾经就是这样漏掉了 /peers 和 /roles。
//
// 新增页面时两边一起加；TestUINavPathsCoverFrontendNav 会对比这份清单和前端
// 的 navItems，漏了哪一条它直接报出来。
var uiNavPaths = map[string]struct{}{
	"/":                 {},
	"/login":            {},
	"/overview":         {},
	"/input-providers":  {},
	"/models-menu":      {},
	"/api-keys":         {},
	"/output-providers": {},
	"/usage-stats":      {},
	"/public-access":    {},
	"/traffic-tokens":   {},
	"/monitoring":       {},
	"/quota-keeper":     {},
	"/peers":            {},
	"/users":            {},
	"/roles":            {},
	"/machine":          {},
	"/settings":         {},
}

func findWebDistDir() string {
	if dist := packaged.WebDistDir(); dist != "" {
		return dist
	}
	// Fallback for older layouts / tests.
	for _, candidate := range webDistCandidates() {
		if dist := webDistIfPresent(candidate); dist != "" {
			return dist
		}
	}
	return ""
}

func webDistIfPresent(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	if info, err := os.Stat(filepath.Join(dir, "index.html")); err == nil && !info.IsDir() {
		return dir
	}
	return ""
}

func webDistCandidates() []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		if _, ok := seen[dir]; ok {
			return
		}
		seen[dir] = struct{}{}
		out = append(out, dir)
	}

	if exe, err := os.Executable(); err == nil {
		exe, _ = filepath.EvalSymlinks(exe)
		macOSDir := filepath.Dir(exe)
		contentsDir := filepath.Dir(macOSDir)
		add(filepath.Join(contentsDir, "Resources", "web-dist"))
		dir := macOSDir
		for i := 0; i < 10; i++ {
			add(filepath.Join(dir, "web", "dist"))
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if root := strings.TrimSpace(os.Getenv("GATEWAY_REPO_ROOT")); root != "" {
		add(filepath.Join(root, "web", "dist"))
	}
	if cwd, err := os.Getwd(); err == nil {
		dir := cwd
		for i := 0; i < 10; i++ {
			add(filepath.Join(dir, "web", "dist"))
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return out
}

func isGatewayAPIPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	if strings.HasPrefix(path, "/__") {
		return true
	}
	// Claude Code OAuth client allowlists http://localhost:<port>/callback.
	if path == "/callback" {
		return true
	}
	return isModelProtocolPath(path)
}

func shouldServeUIFallback(path string) bool {
	if isGatewayAPIPath(path) {
		return false
	}
	if strings.HasPrefix(path, "/assets/") {
		return false
	}
	if _, ok := uiNavPaths[path]; ok {
		return true
	}
	switch path {
	case "/index.html", "/favicon.ico", "/favicon.svg", "/apple-touch-icon.png",
		// PWA 安装（「安装到桌面」）必需的三个入口：manifest 与 service worker
		// 被当成 SPA 路由兜底成 index.html 的话，浏览器会判定应用不可安装。
		"/manifest.webmanifest", "/sw.js", "/registerSW.js":
		return true
	default:
		return false
	}
}

func newUIHandler(distDir string) http.Handler {
	fileServer := http.FileServer(http.Dir(distDir))
	indexPath := filepath.Join(distDir, "index.html")
	// serveIndex sends the SPA entry page with revalidation forced.
	//
	// index.html references content-hashed bundles (index-<hash>.js). Without an
	// explicit Cache-Control the browser applies heuristic caching to the entry
	// page and keeps pointing at an old hash long after a rebuild — the console
	// then runs stale JS while the server already serves the new bundle. Assets
	// themselves stay immutable-cacheable because their name changes on rebuild.
	serveIndex := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		http.ServeFile(w, r, indexPath)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		path := r.URL.Path
		if path == "/" {
			serveIndex(w, r)
			return
		}
		if strings.HasPrefix(path, "/assets/") {
			// Vite embeds a content hash in every asset filename, so a changed
			// file always has a new URL; caching them hard is safe.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			fileServer.ServeHTTP(w, r)
			return
		}
		if candidate := filepath.Join(distDir, strings.TrimPrefix(path, "/")); fileExists(candidate) {
			http.ServeFile(w, r, candidate)
			return
		}
		if shouldServeUIFallback(path) {
			serveIndex(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func withUIServing(api http.Handler, distDir string) http.Handler {
	return withHostSeparatedServing(api, distDir, nil)
}

// publicHostRoleFunc reports whether the request Host is the dedicated API or UI
// public hostname. Returning "" means "no host split" (local / LAN / quick tunnel).
type publicHostRoleFunc func(host string) string

func withHostSeparatedServing(api http.Handler, distDir string, roleForHost publicHostRoleFunc) http.Handler {
	ui := http.Handler(http.NotFoundHandler())
	if strings.TrimSpace(distDir) != "" {
		ui = newUIHandler(distDir)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := ""
		if roleForHost != nil {
			role = roleForHost(requestHostname(r.Host))
		}
		switch role {
		case "api":
			// Model-API hostname: protocol routes (+ health) and the peer gateway
			// endpoints. /__peer/* is where other gateways register and push their
			// traffic, so it belongs to the API surface: on a split deployment the
			// API domain is the only public hostname a peer should ever be given.
			// Everything else under /__ is the console and stays 404 here.
			if r.URL.Path == "/__health" || strings.HasPrefix(r.URL.Path, peerEndpointPrefix) || isModelProtocolPath(r.URL.Path) {
				api.ServeHTTP(w, r)
				return
			}
			writeOpenAIError(w, http.StatusNotFound, "management UI is not available on the API hostname; use the UI domain instead")
			return
		case "ui":
			// Management-UI hostname: block model protocol routes.
			if isModelProtocolPath(r.URL.Path) {
				writeOpenAIError(w, http.StatusNotFound, "model API is not available on the UI hostname; use the API domain instead")
				return
			}
			if isGatewayAPIPath(r.URL.Path) {
				api.ServeHTTP(w, r)
				return
			}
			ui.ServeHTTP(w, r)
			return
		default:
			// Local / LAN / quick tunnel: keep previous combined behavior.
			if isGatewayAPIPath(r.URL.Path) {
				api.ServeHTTP(w, r)
				return
			}
			ui.ServeHTTP(w, r)
		}
	})
}

func requestHostname(hostport string) string {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(hostport); err == nil && host != "" {
		return strings.ToLower(host)
	}
	return strings.ToLower(strings.TrimSuffix(hostport, "."))
}

func isModelProtocolPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	switch {
	case path == "/v1/models",
		strings.HasPrefix(path, "/v1/chat/completions"),
		// Claude Desktop 的 3p 模式把 /v1/messages 拼在网关根地址后面（见
		// server.go 的路由注册）。它不是控制台路径：漏了这一条，请求在本地 /
		// 局域网主机上不会进 mux，桌面端拿到的是 SPA 的 404。
		path == "/v1/messages",
		path == "/v1/messages/count_tokens",
		strings.HasPrefix(path, "/v1/images/"),
		// Video submit and poll are model calls too: without this prefix the local/LAN
		// host path never forwards them to the mux, and they answer a plain 404
		// ("page not found") while the route itself is registered.
		strings.HasPrefix(path, "/v1/videos/"),
		// The agent tools are model calls too (they are admitted and billed like one),
		// and a path the allowlist does not know never reaches the mux on a local host.
		path == "/v1/search",
		path == "/v1/webfetch",
		strings.HasPrefix(path, "/v1/embeddings"),
		// Speech-to-text: the body is a multipart upload, still a model call
		// (it is admitted, quota-checked and billed like one).
		strings.HasPrefix(path, "/v1/audio/"),
		// Gemini's model endpoint puts the model in the path.
		strings.HasPrefix(path, "/v1beta/"),
		strings.HasPrefix(path, "/v1/models/"):
		return true
	case strings.HasPrefix(path, "/openai/v1"):
		return true
	case strings.HasPrefix(path, "/anthropic/v1"):
		return true
	case path == "/chat/completions":
		return true
	}
	return false
}
