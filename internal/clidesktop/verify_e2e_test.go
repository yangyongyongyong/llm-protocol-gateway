package clidesktop

import (
	"os"
	"path/filepath"
	"testing"
)

// 端到端：写进真实形状的文件，再用解析器读回来确认层级没串。
func TestE2ERealisticShape(t *testing.T) {
	home := withHome(t)
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	user := "approvals_reviewer = \"user\"\nnotify = [\"/x\"]\n\n[features]\nremote_compaction_v2 = false\n\n[model_providers.magpie]\nname = \"magpie\"\n"
	if err := os.WriteFile(path, []byte(user), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyCodex(CodexEndpoint{
		ProviderID: "luca", ProviderName: "luca",
		BaseURL: "https://api.lucadesign.uk/openai/v1", Token: "sk-gw-test",
		Model: "default[1m]", ReasoningEffort: "xhigh",
	}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	root, err := parseTOML(t, raw)
	if err != nil {
		t.Fatalf("非法 TOML: %v\n%s", err, raw)
	}
	t.Logf("生成的配置:\n%s", raw)
	if root["approvals_reviewer"] != "user" {
		t.Fatalf("approvals_reviewer 不在顶层: %v", root)
	}
	mp := root["model_providers"].(map[string]any)
	luca, ok := mp["luca"].(map[string]any)
	if !ok {
		t.Fatalf("luca 表缺失: %v", mp)
	}
	for _, bad := range []string{"approvals_reviewer", "notify"} {
		if _, leaked := luca[bad]; leaked {
			t.Fatalf("%s 泄漏进 luca: %v", bad, luca)
		}
	}
	// 与 provider 无关的配置必须原样保留。
	if root["features"] == nil {
		t.Fatalf("用户原有配置丢失: %v", root)
	}
	if root["approvals_reviewer"] != "user" || root["notify"] == nil {
		t.Fatalf("用户顶层键丢失: %v", root)
	}
	// 历史遗留的 provider 表应被新语义清掉：一键写入后只认本网关。
	if mp["magpie"] != nil {
		t.Fatalf("历史 provider 表没被清掉: %v", mp)
	}
	if len(mp) != 1 {
		t.Fatalf("应当只剩一个 provider 定义: %v", mp)
	}
}
