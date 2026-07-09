package gateway

import (
	"path/filepath"

	"github.com/luca/llm-protocol-gateway/internal/config"
	"github.com/luca/llm-protocol-gateway/internal/cursor"
	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/store"
	"github.com/luca/llm-protocol-gateway/internal/tunnel"
)

// ResolveDataPaths returns absolute paths for user-level gateway data.
// These live outside the macOS .app bundle so updates/reinstalls keep state.
func ResolveDataPaths() domain.DataPaths {
	paths := domain.DataPaths{
		Note: "更新或重装 App 不会删除此目录中的数据；数据独立于 .app 包。",
	}

	if configPath, err := config.DefaultConfigPath(); err == nil {
		paths.ConfigFile = absOrSelf(configPath)
		paths.DataDir = absOrSelf(filepath.Dir(configPath))
	}
	if dbPath, err := store.DefaultDBPath(); err == nil {
		paths.SQLiteDB = absOrSelf(dbPath)
		if paths.DataDir == "" {
			paths.DataDir = absOrSelf(filepath.Dir(dbPath))
		}
	}
	if dir, err := tunnel.CloudflareAppDirPath(); err == nil {
		paths.CloudflareConfigDir = absOrSelf(dir)
	}
	if home, err := tunnel.CloudflaredHomeDir(); err == nil {
		paths.CloudflaredHome = absOrSelf(home)
	}
	tokenFile := cursor.DefaultTokenFilePath()
	if tokenFile != "" {
		paths.CursorTokenFile = absOrSelf(tokenFile)
		paths.CursorTokenDir = absOrSelf(filepath.Dir(tokenFile))
	}
	return paths
}

func absOrSelf(path string) string {
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
