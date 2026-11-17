package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppVersionStripsReleasePrefix(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })
	version = "v1.2.3"
	if got := appVersion(); got != "1.2.3" {
		t.Fatalf("appVersion() = %q, want 1.2.3", got)
	}
}

func TestMacAppBundleFindsEnclosingApp(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "LLM Protocol Gateway.app")
	bin := filepath.Join(app, "Contents", "MacOS", "binary")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if got := macAppBundle(bin); got != app {
		t.Fatalf("macAppBundle(%q) = %q, want %q", bin, got, app)
	}
}

// Windows 上 filepath.Dir(`C:\`) 仍是 `C:\`：这个用例防的正是当年把进程
// 钉在 100% CPU 直到用户强杀的那种写法。
func TestMacAppBundleTerminatesAtFilesystemRoot(t *testing.T) {
	for _, exe := range []string{"tool", "/usr/local/bin/tool", "/", `C:\`, `C:\Users\yy\tool.exe`} {
		exe := exe
		done := make(chan string, 1)
		go func() { done <- macAppBundle(exe) }()
		select {
		case got := <-done:
			if got != "" {
				t.Fatalf("macAppBundle(%q) = %q, want empty", exe, got)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("macAppBundle(%q) 没有在 5s 内终止", exe)
		}
	}
}
