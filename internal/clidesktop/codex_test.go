package clidesktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Codex 的一键写入：配置是 TOML，没有"命名块"，所以用一对注释行圈住本工具管理
// 的那一段。这组测试锁三件事——写进去的形状能读、重复写不会堆出第二份、用户
// 自己的配置一个字符都不动。

func codexEndpoint() CodexEndpoint {
	return CodexEndpoint{
		ProviderID:      "lpg",
		ProviderName:    "LLM Gateway",
		BaseURL:         "https://gw.example.com/openai/v1",
		Token:           "sk-gw-test",
		Model:           "gpt-5.6-terra",
		ReasoningEffort: "high",
	}
}

func TestApplyCodexWritesProviderBlock(t *testing.T) {
	home := withHome(t)
	res, err := ApplyCodex(codexEndpoint())
	if err != nil {
		t.Fatalf("ApplyCodex: %v", err)
	}
	if !res.Changed {
		t.Fatal("first run must report a change")
	}
	raw, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		codexBlockBegin,
		codexBlockEnd,
		`model_provider = "lpg"`,
		`model = "gpt-5.6-terra"`,
		`model_reasoning_effort = "high"`,
		`[model_providers.lpg]`,
		`base_url = "https://gw.example.com/openai/v1"`,
		`wire_api = "responses"`,
		`experimental_bearer_token = "sk-gw-test"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("config.toml 缺少 %q:\n%s", want, text)
		}
	}
}

// 顶层键必须排在所有 [table] 之前，否则 TOML 会把 model_provider 读成上一个
// 表的字段。用户文件里本来就有 [features] 这类表时这一点才会暴露。
func TestApplyCodexPutsTopLevelKeysBeforeUserTables(t *testing.T) {
	home := withHome(t)
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	userConfig := "[features]\nremote_compaction_v2 = false\n\n[memories]\nenabled = true\n"
	if err := os.WriteFile(path, []byte(userConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ApplyCodex(codexEndpoint()); err != nil {
		t.Fatalf("ApplyCodex: %v", err)
	}
	raw, _ := os.ReadFile(path)
	text := string(raw)
	topLevel := strings.Index(text, `model_provider = "lpg"`)
	firstTable := strings.Index(text, "[features]")
	if topLevel < 0 || firstTable < 0 || topLevel > firstTable {
		t.Fatalf("顶层键必须写在用户表之前：\n%s", text)
	}
	// 用户自己的配置原样保留。
	if !strings.Contains(text, "remote_compaction_v2 = false") || !strings.Contains(text, "[memories]") {
		t.Fatalf("用户配置被改动了：\n%s", text)
	}
}

// 重复点「一键写入本机」不能在文件里堆出第二份 provider 表（TOML 会报
// duplicate key，整个配置文件直接读不出来）。
func TestApplyCodexIsIdempotent(t *testing.T) {
	home := withHome(t)
	if _, err := ApplyCodex(codexEndpoint()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".codex", "config.toml")
	first, _ := os.ReadFile(path)

	res, err := ApplyCodex(codexEndpoint())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Unchanged {
		t.Fatal("同样的输入再写一次应当报告 unchanged")
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatalf("重复写入改变了文件：\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if strings.Count(string(second), codexBlockBegin) != 1 {
		t.Fatalf("文件里出现了多份区块：\n%s", second)
	}
}

// 换密钥 / 换模型再写一次：旧的整段要被替换掉，而不是叠加。
func TestApplyCodexReplacesPreviousBlock(t *testing.T) {
	home := withHome(t)
	if _, err := ApplyCodex(codexEndpoint()); err != nil {
		t.Fatal(err)
	}
	next := codexEndpoint()
	next.Token = "sk-gw-rotated"
	next.Model = "gpt-6"
	if _, err := ApplyCodex(next); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	text := string(raw)
	if strings.Contains(text, "sk-gw-test") {
		t.Fatalf("旧密钥还在文件里：\n%s", text)
	}
	if !strings.Contains(text, `experimental_bearer_token = "sk-gw-rotated"`) || !strings.Contains(text, `model = "gpt-6"`) {
		t.Fatalf("新值没写进去：\n%s", text)
	}
	if strings.Count(text, "[model_providers.lpg]") != 1 {
		t.Fatalf("provider 表出现了多份：\n%s", text)
	}
}

func TestApplyCodexWritesModelCatalog(t *testing.T) {
	home := withHome(t)
	e := codexEndpoint()
	e.ModelCatalogPath = filepath.Join(home, ".codex", "lpg-model-catalog.json")
	e.ModelCatalog = `{"models":[{"slug":"gpt-5.6-terra"}]}`
	res, err := ApplyCodex(e)
	if err != nil {
		t.Fatalf("ApplyCodex: %v", err)
	}
	if res.CatalogPath != e.ModelCatalogPath {
		t.Fatalf("catalogPath = %q", res.CatalogPath)
	}
	raw, err := os.ReadFile(e.ModelCatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "gpt-5.6-terra") {
		t.Fatalf("模型目录内容不对：%s", raw)
	}
	config, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if !strings.Contains(string(config), `model_catalog_json = "`+e.ModelCatalogPath+`"`) {
		t.Fatalf("config.toml 里没引用模型目录：\n%s", config)
	}
}

// 密钥里带引号和反斜杠时不能把 TOML 写坏。
func TestApplyCodexEscapesToken(t *testing.T) {
	home := withHome(t)
	e := codexEndpoint()
	e.Token = `sk-"quoted"\path`
	if _, err := ApplyCodex(e); err != nil {
		t.Fatalf("ApplyCodex: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if !strings.Contains(string(raw), `experimental_bearer_token = "sk-\"quoted\"\\path"`) {
		t.Fatalf("密钥没有正确转义：\n%s", raw)
	}
}

// auth.json 是「已登录」的唯一依据：只写 provider 段、不写它，Codex 交互入口
// 照样一进来就弹登录页。这条测试盯住那个默认行为。
func TestApplyCodexWritesAuthFile(t *testing.T) {
	home := withHome(t)
	res, err := ApplyCodex(codexEndpoint())
	if err != nil {
		t.Fatalf("ApplyCodex: %v", err)
	}
	if !res.AuthChanged {
		t.Fatal("first run must write auth.json")
	}
	auth := readJSON(t, filepath.Join(home, ".codex", "auth.json"))
	if auth["auth_mode"] != "apikey" {
		t.Fatalf("auth_mode = %v, want apikey", auth["auth_mode"])
	}
	if auth["OPENAI_API_KEY"] != "sk-gw-test" {
		t.Fatalf("OPENAI_API_KEY = %v", auth["OPENAI_API_KEY"])
	}
}

// 保留官方登录：用户本机已有 chatgpt 凭据时，勾了这个就不许碰 auth.json。
func TestApplyCodexKeepsOfficialLogin(t *testing.T) {
	home := withHome(t)
	authPath := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o755); err != nil {
		t.Fatal(err)
	}
	official := `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"secret"}}`
	if err := os.WriteFile(authPath, []byte(official), 0o600); err != nil {
		t.Fatal(err)
	}

	e := codexEndpoint()
	e.KeepOfficialLogin = true
	res, err := ApplyCodex(e)
	if err != nil {
		t.Fatalf("ApplyCodex: %v", err)
	}
	if res.AuthChanged {
		t.Fatal("KeepOfficialLogin must leave auth.json alone")
	}
	raw, _ := os.ReadFile(authPath)
	if string(raw) != official {
		t.Fatalf("auth.json 被改动了：%s", raw)
	}
}

// 重复写入不该反复改写 auth.json，也不该把 chatgpt 那段 token 抹掉。
func TestApplyCodexPreservesChatGPTTokensWhenSwitchingToAPIKey(t *testing.T) {
	home := withHome(t)
	authPath := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o755); err != nil {
		t.Fatal(err)
	}
	official := `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"keep-me"}}`
	if err := os.WriteFile(authPath, []byte(official), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ApplyCodex(codexEndpoint()); err != nil {
		t.Fatalf("ApplyCodex: %v", err)
	}
	auth := readJSON(t, authPath)
	if auth["auth_mode"] != "apikey" || auth["OPENAI_API_KEY"] != "sk-gw-test" {
		t.Fatalf("应当切成 apikey：%v", auth)
	}
	tokens, ok := auth["tokens"].(map[string]any)
	if !ok || tokens["id_token"] != "keep-me" {
		t.Fatalf("原有的 chatgpt tokens 不该被删：%v", auth["tokens"])
	}

	// 再写一次：不该报告改动。
	res, err := ApplyCodex(codexEndpoint())
	if err != nil {
		t.Fatal(err)
	}
	if res.AuthChanged {
		t.Fatal("same token again must not rewrite auth.json")
	}
}

// 空的模型目录会让 Codex 直接拒绝启动（不是降级），所以宁可退回去用内置元数据：
// 既不能写空清单，也不能在 config.toml 里留下一条指向它的 model_catalog_json。
func TestApplyCodexRejectsEmptyModelCatalog(t *testing.T) {
	home := withHome(t)
	e := codexEndpoint()
	e.ModelCatalogPath = ".codex/catalog.json"
	e.ModelCatalog = `{"models":[]}`
	res, err := ApplyCodex(e)
	if err != nil {
		t.Fatalf("ApplyCodex: %v", err)
	}
	if res.CatalogPath != "" {
		t.Fatalf("空清单不该被写入：%q", res.CatalogPath)
	}
	config, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if strings.Contains(string(config), "model_catalog_json") {
		t.Fatalf("配置里不该指向一个没写的目录：\n%s", config)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "catalog.json")); !os.IsNotExist(err) {
		t.Fatal("空清单文件不该落盘")
	}
}

// 合法的目录照写不误。
func TestApplyCodexAcceptsValidCatalog(t *testing.T) {
	home := withHome(t)
	e := codexEndpoint()
	e.ModelCatalogPath = ".codex/catalog.json"
	e.ModelCatalog = `{"models":[{"slug":"m1"}]}`
	if _, err := ApplyCodex(e); err != nil {
		t.Fatalf("ApplyCodex: %v", err)
	}
	config, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if !strings.Contains(string(config), "model_catalog_json") {
		t.Fatalf("合法目录应当被引用：\n%s", config)
	}
}

// 覆盖掉一份能登进去的官方登录时，必须在提示里说清楚——那是用户在别处真登录
// 过的账号，静默顶掉会让人莫名其妙被登出。
func TestApplyCodexWarnsWhenOverwritingRealLogin(t *testing.T) {
	home := withHome(t)
	authPath := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o755); err != nil {
		t.Fatal(err)
	}
	official := `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"real","refresh_token":"real"}}`
	if err := os.WriteFile(authPath, []byte(official), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := ApplyCodex(codexEndpoint())
	if err != nil {
		t.Fatalf("ApplyCodex: %v", err)
	}
	found := false
	for _, note := range res.Notes {
		if strings.Contains(note, "官方登录凭据") {
			found = true
		}
	}
	if !found {
		t.Fatalf("应当提醒覆盖了真实登录：%v", res.Notes)
	}
}

// 只有半残的残留（tokens 是空对象）时不该吓唬人。
func TestApplyCodexNoWarningForEmptyTokenResidue(t *testing.T) {
	home := withHome(t)
	authPath := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o755); err != nil {
		t.Fatal(err)
	}
	residue := `{"tokens":{},"last_refresh":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(authPath, []byte(residue), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := ApplyCodex(codexEndpoint())
	if err != nil {
		t.Fatalf("ApplyCodex: %v", err)
	}
	for _, note := range res.Notes {
		if strings.Contains(note, "官方登录凭据") {
			t.Fatalf("空 tokens 不该被当成真实登录：%v", res.Notes)
		}
	}
}

func TestCodexAuthHasLoginMaterial(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"chatgpt tokens", `{"tokens":{"id_token":"x"}}`, true},
		{"empty tokens", `{"tokens":{}}`, false},
		{"blank token", `{"tokens":{"id_token":"   "}}`, false},
		{"apikey only", `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-x"}`, false},
		{"access token only", `{"tokens":{"access_token":"x"}}`, true},
	}
	for _, tc := range cases {
		var root map[string]any
		if err := json.Unmarshal([]byte(tc.body), &root); err != nil {
			t.Fatal(err)
		}
		if got := codexAuthHasLoginMaterial(root); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

// requires_openai_auth 决定的是「登录界面」：为 true 而盘上没有能用的登录时，
// Codex 会卡在登录选择页（这是配置全对却仍然弹登录的真实原因）。我们的凭据走
// experimental_bearer_token，所以默认必须写 false。
func TestApplyCodexWritesRequiresOpenAIAuthFalseByDefault(t *testing.T) {
	home := withHome(t)
	if _, err := ApplyCodex(codexEndpoint()); err != nil {
		t.Fatalf("ApplyCodex: %v", err)
	}
	config, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if !strings.Contains(string(config), "requires_openai_auth = false") {
		t.Fatalf("默认必须写 false，否则 Codex 会卡在登录页：\n%s", config)
	}
	if strings.Contains(string(config), "requires_openai_auth = true") {
		t.Fatalf("不该出现 true：\n%s", config)
	}
}

// 保留官方登录时才写 true：那时 auth.json 里有一份真登录，Codex 靠它认账号。
func TestApplyCodexWritesRequiresOpenAIAuthTrueWhenKeepingOfficialLogin(t *testing.T) {
	home := withHome(t)
	e := codexEndpoint()
	e.KeepOfficialLogin = true
	if _, err := ApplyCodex(e); err != nil {
		t.Fatalf("ApplyCodex: %v", err)
	}
	config, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if !strings.Contains(string(config), "requires_openai_auth = true") {
		t.Fatalf("保留官方登录时应当写 true：\n%s", config)
	}
}
