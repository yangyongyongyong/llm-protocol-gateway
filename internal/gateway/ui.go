package gateway

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/packaged"
)

var uiNavPaths = map[string]struct{}{
	"/":                  {},
	"/login":             {},
	"/input-providers":   {},
	"/models-menu":       {},
	"/api-keys":          {},
	"/output-providers":  {},
	"/usage-stats":       {},
	"/public-access":     {},
	"/traffic-tokens":    {},
	"/self-check":        {},
	"/settings":          {},
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
	case "/index.html", "/favicon.ico", "/favicon.svg", "/apple-touch-icon.png":
		return true
	default:
		return false
	}
}

func newUIHandler(distDir string) http.Handler {
	fileServer := http.FileServer(http.Dir(distDir))
	indexPath := filepath.Join(distDir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		path := r.URL.Path
		if path == "/" {
			http.ServeFile(w, r, indexPath)
			return
		}
		if strings.HasPrefix(path, "/assets/") {
			fileServer.ServeHTTP(w, r)
			return
		}
		if candidate := filepath.Join(distDir, strings.TrimPrefix(path, "/")); fileExists(candidate) {
			http.ServeFile(w, r, candidate)
			return
		}
		if shouldServeUIFallback(path) {
			http.ServeFile(w, r, indexPath)
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
			// Model-API hostname: only protocol routes (+ health). No UI / admin APIs.
			if r.URL.Path == "/__health" || isModelProtocolPath(r.URL.Path) {
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
	case path == "/v1/models", strings.HasPrefix(path, "/v1/chat/completions"), strings.HasPrefix(path, "/v1/images/"):
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
