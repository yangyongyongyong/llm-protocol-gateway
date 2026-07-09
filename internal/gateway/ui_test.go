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
