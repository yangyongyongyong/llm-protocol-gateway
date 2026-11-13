package clidesktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codexPath 返回测试用的 config.toml 路径，并确保目录存在。
func codexPath(t *testing.T, home string) string {
	t.Helper()
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// 一键写入的语义是「这台机器的 Codex 从此只认这个网关」，所以历史遗留的
// [model_providers.*] 要一并清掉。这段测试锁的是边界：只删 provider 表，
// Codex 自己的配置一个字符都不许动。
func TestApplyCodexRemovesForeignProviderTables(t *testing.T) {
	home := withHome(t)
	path := codexPath(t, home)
	// 模拟真实文件：一堆指向已停用地址的历史 provider + 大量无关配置。
	original := `approval_policy = "never"
sandbox_mode = "danger-full-access"

[desktop]
theme = "dark"

[model_providers.magpie]
name = "magpie"
base_url = "http://127.0.0.1:3425/v1"

[model_providers.custom]
name = "custom"
base_url = "http://127.0.0.1:3425/v1"

[projects."/Users/someone/repo"]
trust_level = "trusted"

[model_providers.kimi]
name = "kimi"
base_url = "http://127.0.0.1:3425/v1"

[plugins."chrome@openai-bundled"]
enabled = true
`
	writeFile(t, path, original)

	res, err := ApplyCodex(codexEndpoint())
	if err != nil {
		t.Fatalf("ApplyCodex: %v", err)
	}
	if !res.Changed {
		t.Fatal("应当报告改动")
	}
	text := readFile(t, path)

	// 历史 provider 全部消失。
	for _, gone := range []string{"model_providers.magpie", "model_providers.custom", "model_providers.kimi", "base_url = \"http://127.0.0.1:3425/v1\""} {
		if strings.Contains(text, gone) {
			t.Fatalf("历史 provider 没被清掉 %q：\n%s", gone, text)
		}
	}
	// 只剩我们这一个。
	if strings.Count(text, "model_providers.") != 1 {
		t.Fatalf("应当只剩一个 provider 定义：\n%s", text)
	}
	// 无关配置原样保留。
	for _, keep := range []string{
		`[desktop]`, `theme = "dark"`,
		`[projects."/Users/someone/repo"]`, `trust_level = "trusted"`,
		`[plugins."chrome@openai-bundled"]`, `enabled = true`,
		`approval_policy = "never"`, `sandbox_mode = "danger-full-access"`,
	} {
		if !strings.Contains(text, keep) {
			t.Fatalf("无关配置被误删 %q：\n%s", keep, text)
		}
	}
}

// provider 表里的子表（.env 之类）要跟着父表一起删，不能留下孤儿。
func TestStripForeignProviderTablesRemovesSubtables(t *testing.T) {
	input := `[model_providers.old]
name = "old"

[model_providers.old.env]
API_KEY = "x"

[features]
memories = true
`
	got := stripForeignProviderTables(input)
	if strings.Contains(got, "model_providers.old") {
		t.Fatalf("父表没删掉：\n%s", got)
	}
	if strings.Contains(got, "API_KEY") {
		t.Fatalf("子表没删掉：\n%s", got)
	}
	if !strings.Contains(got, "[features]") || !strings.Contains(got, "memories = true") {
		t.Fatalf("无关表被误删：\n%s", got)
	}
}

// 表外的顶层键不属于 provider，绝不能顺手删掉——那可能是用户自己的选择。
func TestStripForeignProviderTablesKeepsTopLevelKeys(t *testing.T) {
	input := `model_verbosity = "high"
notify = ["/bin/x"]

[model_providers.old]
name = "old"
`
	got := stripForeignProviderTables(input)
	if !strings.Contains(got, `model_verbosity = "high"`) {
		t.Fatalf("顶层键被误删：\n%s", got)
	}
	if !strings.Contains(got, `notify = ["/bin/x"]`) {
		t.Fatalf("顶层键被误删：\n%s", got)
	}
	if strings.Contains(got, "model_providers.old") {
		t.Fatalf("provider 表没删掉：\n%s", got)
	}
}

// 带引号的 provider id（含点号）也要认得出来。
func TestStripForeignProviderTablesHandlesQuotedIDs(t *testing.T) {
	input := `model_providers."my.dotted" = { name = "x" }

[model_providers."other.dotted"]
name = "y"

[features]
memories = true
`
	got := stripForeignProviderTables(input)
	if strings.Contains(got, `model_providers."other.dotted"`) {
		t.Fatalf("带点的 provider 表没删掉：\n%s", got)
	}
	if !strings.Contains(got, "[features]") {
		t.Fatalf("无关表被误删：\n%s", got)
	}
}

// 清理后不能把文件变成空的：无关内容还在，只是 provider 段没了。
func TestStripForeignProviderTablesDoesNotNukeFile(t *testing.T) {
	input := `[model_providers.only]
name = "only"

[desktop]
theme = "dark"
`
	got := stripForeignProviderTables(input)
	if strings.TrimSpace(got) == "" {
		t.Fatal("文件被清空了")
	}
	if !strings.Contains(got, "[desktop]") {
		t.Fatalf("desktop 段丢了：\n%s", got)
	}
}
