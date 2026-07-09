// Package packaged locates binaries and assets bundled inside a macOS .app
// (Contents/Resources/...) so the gateway can run without a source checkout.
package packaged

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ResourcesDir returns Contents/Resources when running from a macOS .app, else "".
func ResourcesDir() string {
	if v := strings.TrimSpace(os.Getenv("GATEWAY_APP_RESOURCES")); v != "" {
		if abs, err := filepath.Abs(v); err == nil {
			return abs
		}
		return v
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	// .../App.app/Contents/MacOS/<binary>
	macOSDir := filepath.Dir(exe)
	contentsDir := filepath.Dir(macOSDir)
	resources := filepath.Join(contentsDir, "Resources")
	if info, err := os.Stat(resources); err == nil && info.IsDir() {
		return resources
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// Cloudflared returns the preferred cloudflared binary path.
// Order: GATEWAY_CLOUDFLARED → App Resources/bin/cloudflared → PATH.
func Cloudflared() (string, error) {
	if v := strings.TrimSpace(os.Getenv("GATEWAY_CLOUDFLARED")); v != "" && fileExists(v) {
		return v, nil
	}
	if res := ResourcesDir(); res != "" {
		candidate := filepath.Join(res, "bin", "cloudflared")
		if fileExists(candidate) {
			return candidate, nil
		}
	}
	return exec.LookPath("cloudflared")
}

// Bun returns the preferred bun binary path.
// Order: GATEWAY_BUN → App Resources/bin/bun → PATH.
func Bun() (string, error) {
	if v := strings.TrimSpace(os.Getenv("GATEWAY_BUN")); v != "" && fileExists(v) {
		return v, nil
	}
	if res := ResourcesDir(); res != "" {
		for _, name := range []string{"bun", "bun.exe"} {
			candidate := filepath.Join(res, "bin", name)
			if fileExists(candidate) {
				return candidate, nil
			}
		}
	}
	return exec.LookPath("bun")
}

// CursorBridgeDir returns the directory containing standalone.ts.
// Order: GATEWAY_CURSOR_BRIDGE_DIR → App Resources/cursor-bridge → repo scripts/cursor-bridge.
func CursorBridgeDir() string {
	if v := strings.TrimSpace(os.Getenv("GATEWAY_CURSOR_BRIDGE_DIR")); v != "" {
		if fileExists(filepath.Join(v, "standalone.ts")) {
			return v
		}
	}
	if res := ResourcesDir(); res != "" {
		candidate := filepath.Join(res, "cursor-bridge")
		if fileExists(filepath.Join(candidate, "standalone.ts")) {
			return candidate
		}
	}
	if root := RepoRoot(); root != "" {
		candidate := filepath.Join(root, "scripts", "cursor-bridge")
		if fileExists(filepath.Join(candidate, "standalone.ts")) {
			return candidate
		}
	}
	return ""
}

// WebDistDir returns the packaged or checkout web UI dist directory.
func WebDistDir() string {
	if v := strings.TrimSpace(os.Getenv("GATEWAY_WEB_DIST")); v != "" && fileExists(filepath.Join(v, "index.html")) {
		return v
	}
	if res := ResourcesDir(); res != "" {
		candidate := filepath.Join(res, "web-dist")
		if fileExists(filepath.Join(candidate, "index.html")) {
			return candidate
		}
	}
	if root := RepoRoot(); root != "" {
		candidate := filepath.Join(root, "web", "dist")
		if fileExists(filepath.Join(candidate, "index.html")) {
			return candidate
		}
	}
	return ""
}

// RepoRoot locates a source checkout (dev) or returns "".
func RepoRoot() string {
	if v := strings.TrimSpace(os.Getenv("GATEWAY_REPO_ROOT")); v != "" {
		return v
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		dir := filepath.Dir(exe)
		for i := 0; i < 12; i++ {
			if fileExists(filepath.Join(dir, "scripts", "cursor-bridge", "standalone.ts")) ||
				fileExists(filepath.Join(dir, "cmd", "gateway", "main.go")) {
				return dir
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	dir := cwd
	for i := 0; i < 10; i++ {
		if fileExists(filepath.Join(dir, "scripts", "cursor-bridge", "standalone.ts")) ||
			fileExists(filepath.Join(dir, "cmd", "gateway", "main.go")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}
