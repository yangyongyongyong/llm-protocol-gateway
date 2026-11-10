package clidesktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这组测试锁的是「写进去的东西能被客户端读到」这件事本身，格式对齐 magpie
// 与 CC Switch 已经验证过的写法：
//
//  1. CLI：settings.json 的 env 里钉死四个档位与子 agent 模型；
//  2. Desktop：Claude-3p 下 deploymentMode=3p、gateway profile、appliedId；
//  3. 两端都只碰自己负责的键，其余原样保留，且写前有备份。

// desktop3pDir 用被测函数自己算出来的 macOS 3p 目录：这条测试固定 GOOS=darwin，
// 期望值也必须走同一套拼接，才能在 Windows 的 CI 上跑出 / 而不是 \。
func desktop3pDir(home string) string {
	_, threeP := desktopDirs("darwin", home)
	return threeP
}

func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	oldHome, oldGOOS, oldStamp := Home, GOOS, nowStamp
	Home = func() (string, error) { return home, nil }
	GOOS = "darwin"
	nowStamp = func() string { return "20260101-000000" }
	t.Cleanup(func() { Home, GOOS, nowStamp = oldHome, oldGOOS, oldStamp })
	return home
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return out
}

func endpoint() Endpoint {
	return Endpoint{
		BaseURL:    "https://gw.example.com/anthropic",
		GatewayURL: "https://gw.example.com",
		Token:      "sk-gw-test",
		Model:      "default[1m]",
	}
}

func TestApplyCLIPinsEveryTierAndSubagentModel(t *testing.T) {
	home := withHome(t)
	res, err := Apply(endpoint())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.CLIChanged {
		t.Fatal("first run must report a change")
	}
	env, _ := readJSON(t, filepath.Join(home, ".claude", "settings.json"))["env"].(map[string]any)
	want := map[string]string{
		"ANTHROPIC_BASE_URL":             "https://gw.example.com/anthropic",
		"ANTHROPIC_AUTH_TOKEN":           "sk-gw-test",
		"ANTHROPIC_MODEL":                "default[1m]",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "default[1m]",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "default[1m]",
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  "default[1m]",
		// 短任务档位与子 agent 刻意不带 [1m]
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": "default",
		"ANTHROPIC_SMALL_FAST_MODEL":    "default",
		"CLAUDE_CODE_SUBAGENT_MODEL":    "default",
	}
	for key, wantValue := range want {
		if got, _ := env[key].(string); got != wantValue {
			t.Errorf("env[%s] = %q, want %q", key, got, wantValue)
		}
	}
}

// 子 agent 必须跟着走：不给 CLAUDE_CODE_SUBAGENT_MODEL，内置的
// claude-code-guide（haiku）和 statusline-setup（sonnet）会按真实模型名发请求，
// 绕开平台的模型切换。
func TestApplyCLISubagentFollowsPlatform(t *testing.T) {
	home := withHome(t)
	if _, err := Apply(endpoint()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	env, _ := readJSON(t, filepath.Join(home, ".claude", "settings.json"))["env"].(map[string]any)
	sub, _ := env["CLAUDE_CODE_SUBAGENT_MODEL"].(string)
	if sub == "" {
		t.Fatal("CLAUDE_CODE_SUBAGENT_MODEL must be set or subagents bypass the platform")
	}
	if sub != "default" {
		t.Fatalf("subagent model = %q, want the platform-resolved %q", sub, "default")
	}
}

// 只碰 env 里我们负责的键：用户自己的 model 选择、权限、hooks 都要留着。
func TestApplyCLILeavesTheRestAlone(t *testing.T) {
	home := withHome(t)
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	before := `{
  "model": "claude-opus-5",
  "permissions": {"allow": ["Bash(ls:*)"]},
  "env": {"ANTHROPIC_BASE_URL": "https://old.example.com", "MY_VAR": "keep-me"}
}`
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Apply(endpoint()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	root := readJSON(t, path)
	if root["model"] != "claude-opus-5" {
		t.Errorf("user's model choice must survive, got %v", root["model"])
	}
	if _, ok := root["permissions"]; !ok {
		t.Error("permissions must survive")
	}
	env, _ := root["env"].(map[string]any)
	if env["MY_VAR"] != "keep-me" {
		t.Errorf("unrelated env must survive, got %v", env["MY_VAR"])
	}
	if env["ANTHROPIC_BASE_URL"] != "https://gw.example.com/anthropic" {
		t.Errorf("base URL must be updated, got %v", env["ANTHROPIC_BASE_URL"])
	}
}

func TestApplyCLIIsIdempotentAndBacksUp(t *testing.T) {
	home := withHome(t)
	// 先放一份旧配置：首次写入才有东西可备份（全新安装不必凭空造一个备份）。
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"env":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(endpoint()); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	res, err := Apply(endpoint())
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if res.CLIChanged {
		t.Error("second run must report no change")
	}
	if !res.CLIUnchanged {
		t.Error("second run must set CLIUnchanged")
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".claude", "settings.json.bak-*"))
	if len(matches) != 1 {
		t.Fatalf("want exactly one backup from the first run, got %v", matches)
	}
}

func TestApplyDesktopWritesThirdPartyMode(t *testing.T) {
	home := withHome(t)
	res, err := Apply(endpoint())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	base, threeP := desktopDirs("darwin", home)

	// 两个 config 文件都要有 deploymentMode=3p：Desktop 各家版本读的位置不同。
	for _, dir := range []string{base, threeP} {
		path := filepath.Join(dir, "claude_desktop_config.json")
		root := readJSON(t, path)
		if root["deploymentMode"] != "3p" {
			t.Errorf("%s deploymentMode = %v, want 3p", dir, root["deploymentMode"])
		}
	}

	profile := readJSON(t, filepath.Join(threeP, "configLibrary", profileID+".json"))
	want := map[string]any{
		"inferenceProvider":            "gateway",
		"inferenceGatewayBaseUrl":      "https://gw.example.com",
		"inferenceGatewayApiKey":       "sk-gw-test",
		"inferenceGatewayAuthScheme":   "bearer",
		"disableDeploymentModeChooser": true,
	}
	for key, wantValue := range want {
		if profile[key] != wantValue {
			t.Errorf("profile[%s] = %v, want %v", key, profile[key], wantValue)
		}
	}

	meta := readJSON(t, filepath.Join(threeP, "configLibrary", "_meta.json"))
	if meta["appliedId"] != profileID {
		t.Errorf("appliedId = %v, want %s", meta["appliedId"], profileID)
	}
	entries, _ := meta["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("want one entry, got %v", entries)
	}
	if res.DesktopPath == "" || !res.DesktopChange {
		t.Errorf("result must describe the desktop write: %+v", res)
	}
}

// Desktop 目录里已有的 profile 与条目不能丢：用户可能从别的机器导入过配置。
func TestApplyDesktopKeepsExistingProfiles(t *testing.T) {
	home := withHome(t)
	library := filepath.Join(desktop3pDir(home), "configLibrary")
	if err := os.MkdirAll(library, 0o755); err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(library, "_meta.json")
	before := `{"appliedId":"user-profile","entries":[{"id":"user-profile","name":"公司网关"}]}`
	if err := os.WriteFile(metaPath, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Apply(endpoint()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	meta := readJSON(t, metaPath)
	entries, _ := meta["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("the user's profile must be kept beside ours, got %v", entries)
	}
	if meta["appliedId"] != profileID {
		t.Errorf("our profile must be applied, got %v", meta["appliedId"])
	}
	found := false
	for _, item := range entries {
		if entry, ok := item.(map[string]any); ok && entry["id"] == "user-profile" {
			found = true
		}
	}
	if !found {
		t.Error("user-profile entry disappeared")
	}
}

// 坏掉的 JSON 必须拒绝写入：把它当成空对象覆盖，等于把用户的配置删了。
func TestApplyRefusesToOverwriteBrokenJSON(t *testing.T) {
	home := withHome(t)
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Apply(endpoint()); err == nil {
		t.Fatal("Apply must fail on broken JSON")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "{not json" {
		t.Fatalf("broken file must be left untouched, got %q", raw)
	}
}

// endpoint.env 存在但不是对象时同样拒绝，不覆盖。
func TestApplyRefusesNonObjectEnv(t *testing.T) {
	home := withHome(t)
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"env":"oops"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(endpoint()); err == nil {
		t.Fatal("Apply must fail when env is not an object")
	}
}

func TestDesktopDirsPerOS(t *testing.T) {
	// 期望值按各自系统的分隔符写死：这条测试的意义正是「拼出来的路要走目标
	// 系统的分隔符」，不能在 Windows 上跑 darwin 用例时拼出反斜杠。
	cases := []struct {
		goos  string
		home  string
		env   map[string]string
		mode  string
		three string
	}{
		{
			"darwin", "/Users/luca", nil,
			"/Users/luca/Library/Application Support/Claude",
			"/Users/luca/Library/Application Support/Claude-3p",
		},
		{
			"linux", "/home/luca", map[string]string{"XDG_CONFIG_HOME": "/home/luca/.config"},
			"/home/luca/.config/Claude",
			"/home/luca/.config/Claude-3p",
		},
		{
			"windows", `C:\Users\luca`, map[string]string{
				"APPDATA":      `C:\Users\luca\AppData\Roaming`,
				"LOCALAPPDATA": `C:\Users\luca\AppData\Local`,
			},
			`C:\Users\luca\AppData\Roaming\Claude`,
			`C:\Users\luca\AppData\Local\Claude-3p`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			mode, three := desktopDirs(tc.goos, tc.home)
			if mode != tc.mode || three != tc.three {
				t.Errorf("%s: got (%q, %q), want (%q, %q)", tc.goos, mode, three, tc.mode, tc.three)
			}
		})
	}
}

func TestStripContextTag(t *testing.T) {
	cases := map[string]string{
		"default[1m]": "default",
		"default[2m]": "default",
		"default":     "default",
		"glm-5.3":     "glm-5.3",
	}
	for input, want := range cases {
		if got := stripContextTag(input); got != want {
			t.Errorf("stripContextTag(%q) = %q, want %q", input, got, want)
		}
	}
}

// 备份文件必须留得住：写入前的那份原文件要能找回。
func TestBackupKeepsOriginal(t *testing.T) {
	home := withHome(t)
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	original := `{"env":{"ANTHROPIC_BASE_URL":"https://old.example.com"}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(endpoint())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var cliBackup string
	for _, b := range res.Backups {
		if strings.HasSuffix(b, "settings.json.bak-20260101-000000") {
			cliBackup = b
		}
	}
	if cliBackup == "" {
		t.Fatalf("no CLI backup recorded: %v", res.Backups)
	}
	raw, err := os.ReadFile(cliBackup)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != original {
		t.Fatalf("backup content = %q, want the original", raw)
	}
}
