//go:build darwin

package desktopupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateAppRejectsReleaseMetadataMismatch(t *testing.T) {
	app := filepath.Join(t.TempDir(), "LLM Protocol Gateway.app")
	contents := filepath.Join(app, "Contents")
	if err := os.MkdirAll(filepath.Join(contents, "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contents, "MacOS", "LLM Protocol Gateway"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	info := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>com.wails.llm-protocol-gateway</string>
<key>CFBundleShortVersionString</key><string>0.1.3</string>
<key>CFBundleExecutable</key><string>LLM Protocol Gateway</string>
</dict></plist>`
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(info), 0o644); err != nil {
		t.Fatal(err)
	}

	err := validateApp(app, "1.0.20")
	if err == nil {
		t.Fatal("version mismatch must reject the update")
	}
	msg := err.Error()
	if !strings.Contains(msg, "发布清单为 1.0.20") || !strings.Contains(msg, "Info.plist 为 0.1.3") || !strings.Contains(msg, "发布修复版") {
		t.Fatalf("error should tell the operator which metadata is broken: %q", msg)
	}
}
