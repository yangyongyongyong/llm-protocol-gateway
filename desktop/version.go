package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// version is stamped by the release build with
// -X github.com/luca/llm-protocol-gateway/desktop.version=<tag>.
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
	for dir := filepath.Dir(exe); dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		if !strings.HasSuffix(dir, ".app") {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(dir, "Contents", "Info.plist"))
		if readErr != nil {
			break
		}
		if m := plistShortVersion.FindSubmatch(raw); len(m) == 2 {
			if v := strings.TrimSpace(string(m[1])); v != "" {
				return strings.TrimPrefix(v, "v")
			}
		}
		break
	}
	return "dev"
}
