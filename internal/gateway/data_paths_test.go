package gateway

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveDataPathsUsesConfigOverride(t *testing.T) {
	tmp := t.TempDir()
	configFile := filepath.Join(tmp, "llm-protocol-gateway", "config.json")
	t.Setenv("GATEWAY_CONFIG", configFile)
	t.Setenv("GATEWAY_DB", "")

	paths := ResolveDataPaths()
	if !strings.HasSuffix(paths.DataDir, "llm-protocol-gateway") {
		t.Fatalf("dataDir=%q want suffix llm-protocol-gateway", paths.DataDir)
	}
	if !strings.HasSuffix(paths.ConfigFile, "config.json") {
		t.Fatalf("configFile=%q", paths.ConfigFile)
	}
	if !strings.HasSuffix(paths.SQLiteDB, "gateway.db") {
		t.Fatalf("sqliteDb=%q", paths.SQLiteDB)
	}
	if paths.Note == "" {
		t.Fatal("expected note about lossless updates")
	}
	if paths.CursorTokenFile == "" || !strings.Contains(paths.CursorTokenFile, "cursor") {
		t.Fatalf("cursorTokenFile=%q", paths.CursorTokenFile)
	}
	for _, p := range []string{paths.DataDir, paths.ConfigFile, paths.SQLiteDB, paths.CursorTokenFile} {
		if strings.Contains(p, ".app/Contents") {
			t.Fatalf("path must not live inside .app bundle: %s", p)
		}
	}
}
