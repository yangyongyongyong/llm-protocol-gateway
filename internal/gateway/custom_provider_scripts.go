package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// customProviderScriptRoot 是用户自定义 Provider 类型的脚本目录。默认放在
// App 的用户数据目录（macOS 为 ~/Library/Application Support/llm-protocol-gateway），
// 不写进签名 .app 包，避免升级替换 App 时丢脚本或破坏代码签名。
func customProviderScriptRoot() string {
	if root := strings.TrimSpace(os.Getenv("GATEWAY_CUSTOM_PROVIDER_DIR")); root != "" {
		return root
	}
	if configDir, err := os.UserConfigDir(); err == nil && strings.TrimSpace(configDir) != "" {
		return filepath.Join(configDir, "llm-protocol-gateway", "custom-providers")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".llm-protocol-gateway", "custom-providers")
	}
	return filepath.Join(os.TempDir(), "llm-protocol-gateway", "custom-providers")
}

func customProviderScriptDir(id string) string {
	return filepath.Join(customProviderScriptRoot(), safeScriptDirName(id))
}

func customProviderScriptFilePath(provider domain.CustomProvider) string {
	entry := strings.TrimSpace(provider.Script.Entrypoint)
	if entry == "" {
		entry = "adapter.py"
	}
	return filepath.Join(customProviderScriptDir(provider.ID), entry)
}

// writeCustomProviderScriptFiles 把用户提供的脚本文本写入类型专属目录。脚本
// 原文会同时保留在配置中用于备份/导出，但运行时以磁盘文件为准，用户也能在
// App 数据目录里直接找到并审计。
func writeCustomProviderScriptFiles(provider domain.CustomProvider) (string, error) {
	if strings.TrimSpace(provider.Script.Source) == "" {
		return "", fmt.Errorf("custom provider script source is required")
	}
	dir := customProviderScriptDir(provider.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	entry := strings.TrimSpace(provider.Script.Entrypoint)
	if entry == "" {
		entry = "adapter.py"
	}
	if filepath.Base(entry) != entry || strings.Contains(entry, "..") {
		return "", fmt.Errorf("custom provider entrypoint must be a plain filename")
	}
	if err := atomicWriteScriptFile(filepath.Join(dir, entry), []byte(provider.Script.Source)); err != nil {
		return "", err
	}
	requirementsPath := filepath.Join(dir, "requirements.txt")
	if requirements := strings.TrimSpace(provider.Script.Requirements); requirements != "" {
		if err := atomicWriteScriptFile(requirementsPath, []byte(requirements)); err != nil {
			return "", err
		}
	} else {
		_ = os.Remove(requirementsPath)
	}
	return dir, nil
}

func atomicWriteScriptFile(path string, content []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func removeCustomProviderScriptFiles(id string) error {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	return os.RemoveAll(customProviderScriptDir(id))
}

// decorateCustomProvidersForClient 只补充运行期展示路径，不修改模板定义。
func decorateCustomProvidersForClient(providers []domain.CustomProvider) []domain.CustomProvider {
	out := make([]domain.CustomProvider, len(providers))
	copy(out, providers)
	for index := range out {
		out[index].ScriptDir = customProviderScriptDir(out[index].ID)
	}
	return out
}
