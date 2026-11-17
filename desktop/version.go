package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// version is stamped by the release build with -X main.version=<tag>.
//
// 注意：desktop 是 package main，linker 只认 main.version —— 写模块路径
// （github.com/luca/llm-protocol-gateway/desktop.version）会被静默忽略，
// 变量保持空串。空串曾经把 Windows 上的进程钉死在 .app 搜索里，见
// macAppBundle 的注释。
var version = ""

var plistShortVersion = regexp.MustCompile(`(?s)<key>CFBundleShortVersionString</key>\s*<string>(.*?)</string>`)

func appVersion() string {
	if v := strings.TrimSpace(strings.TrimPrefix(version, "v")); v != "" {
		return v
	}
	// A locally launched Wails build may not have ldflags. Read the bundle's
	// version so the console and updater still agree on what is running.
	exe, err := os.Executable()
	if err != nil {
		return "dev"
	}
	bundle := macAppBundle(exe)
	if bundle == "" {
		return "dev"
	}
	raw, readErr := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist"))
	if readErr != nil {
		return "dev"
	}
	if m := plistShortVersion.FindSubmatch(raw); len(m) == 2 {
		if v := strings.TrimSpace(string(m[1])); v != "" {
			return strings.TrimPrefix(v, "v")
		}
	}
	return "dev"
}

// macAppBundle 返回 exe 所在的 .app 目录，没有就返回空串。
//
// 这里曾经写成 `for dir := filepath.Dir(exe); dir != "/" && dir != "."; dir =
// filepath.Dir(dir)`：macOS/Linux 上遇 "/" 或 "." 会停，但 Windows 上
// filepath.Dir(`C:\`) 仍然是 `C:\`，条件永远不成立，启动即 100% CPU
// 死循环、永远走不到打开数据库那一步。改成比较父目录是否等于自己，
// 任何平台的最终根（`/`、`C:\`、UNC 共享根）都会终止。
func macAppBundle(exe string) string {
	dir := filepath.Dir(exe)
	for {
		if strings.HasSuffix(dir, ".app") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
