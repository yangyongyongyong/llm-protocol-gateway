// Package clidesktop 把本机上的 Claude Code（CLI）与 Claude Desktop（app）
// 指向本网关，做法与 magpie / CC Switch 一致：
//
//   - CLI 只认 ~/.claude/settings.json 的 env，改几个键即可；
//   - Desktop 的第三方网关模式（3p）是本地文件，不是下发：
//     Claude-3p/claude_desktop_config.json 写 deploymentMode=3p，
//     Claude-3p/configLibrary/<id>.json 写 gateway 连接配置，
//     configLibrary/_meta.json 把这份 profile 设为 appliedId。
//     Desktop 只在启动时读，所以写完要重启 app。
//
// 两个客户端都只写「我们负责的键」，其余原样保留；改之前先备份。
package clidesktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// Endpoint 是本次要写入的连接信息。
type Endpoint struct {
	// BaseURL 是 Claude Code CLI 用的地址（含 /anthropic 前缀）。
	BaseURL string
	// GatewayURL 是 Desktop 的 3p 模式用的地址（不带 /anthropic：
	// Desktop 自己拼 /v1/messages）。
	GatewayURL string
	// Token 是 API 密钥。
	Token string
	// Model 是固定的模型名，CLI 侧用 default 或 default[1m]。
	Model string
}

// Result 汇总一次写入做了什么，供界面展示。
type Result struct {
	// CLI 相关
	CLIPath       string `json:"cliPath"`
	CLIChanged    bool   `json:"cliChanged"`
	CLIUnchanged  bool   `json:"cliUnchanged"`
	DesktopPath   string `json:"desktopPath"`
	DesktopMade   bool   `json:"desktopMade"`
	DesktopChange bool   `json:"desktopChange"`
	// Backups 是本次改动前留下的备份文件，按写入顺序。
	Backups []string `json:"backups"`
	// Notes 是给人看的补充说明（需要重启、app 未安装等）。
	Notes []string `json:"notes"`
}

// Home 返回当前用户主目录，测试可替换。
var Home = func() (string, error) { return os.UserHomeDir() }

// GOOS 是当前系统，测试可替换。
var GOOS = runtime.GOOS

// claudeSettingsPath 是 CLI 的配置文件。
func claudeSettingsPath(home string) string {
	return filepath.Join(home, ".claude", "settings.json")
}

// desktopDirs 返回 Desktop 的模式目录与 3p 目录。
//
// 与 magpie 的 desktopdir.Sets 对齐：macOS 是
// ~/Library/Application Support/Claude 与 Claude-3p；Linux 是
// $XDG_CONFIG_HOME/Claude 与 Claude-3p；Windows 是
// %APPDATA%\Claude 与 %LOCALAPPDATA%\Claude-3p。
//
// 拼接按 goos 走而不是按宿主走：CI 在 Windows 上跑着 darwin 的用例，
// 用 filepath.Join 会把 macOS 的路径拼成 \Users\luca\Library\...。
func desktopDirs(goos, home string) (mode, threeP string) {
	switch goos {
	case "darwin":
		base := joinFor(goos, home, "Library", "Application Support")
		return joinFor(goos, base, "Claude"), joinFor(goos, base, "Claude-3p")
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = joinFor(goos, home, "AppData", "Roaming")
		}
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			local = joinFor(goos, home, "AppData", "Local")
		}
		return joinFor(goos, appData, "Claude"), joinFor(goos, local, "Claude-3p")
	default:
		cfg := os.Getenv("XDG_CONFIG_HOME")
		if cfg == "" {
			cfg = joinFor(goos, home, ".config")
		}
		return joinFor(goos, cfg, "Claude"), joinFor(goos, cfg, "Claude-3p")
	}
}

// joinFor 按目标系统的分隔符拼路径。生产里 goos 就是宿主，等价于
// filepath.Join；差别只在测试会在非目标系统上模拟另一套路径。
func joinFor(goos string, parts ...string) string {
	joined := path.Join(parts...)
	if goos == "windows" {
		return strings.ReplaceAll(joined, "/", `\`)
	}
	return joined
}

// profileID 是我们在 Desktop configLibrary 里的固定 profile id。固定值意味着
// 重复执行只会更新同一份 profile，不会攒出一堆孤儿条目。
//
// 末段 "6c6c6d" 是 "llm" 的十六进制，与 magpie 的 "6d6167706965"（magpie）
// 同一个套路：UUID 形状合法，又能一眼看出是谁写的。
const profileID = "00000000-0000-4000-8000-6c6c6d000001"

// Apply 把 CLI 与 Desktop 都指向 e。两者互相独立：一边失败不影响另一边，
// 错误用 errors.Join 汇总返回，调用方仍可从 Result 看出哪些成功。
func Apply(e Endpoint) (Result, error) {
	home, err := Home()
	if err != nil {
		return Result{}, fmt.Errorf("定位用户主目录失败: %w", err)
	}
	var result Result
	var errs []error

	cliRes, cliErr := applyCLI(home, e)
	result.CLIPath = cliRes.path
	result.CLIChanged = cliRes.changed
	result.CLIUnchanged = cliRes.unchanged
	result.Backups = append(result.Backups, cliRes.backup)
	if cliErr != nil {
		errs = append(errs, fmt.Errorf("写入 Claude Code CLI 配置失败: %w", cliErr))
	}

	deskRes, deskErr := applyDesktop(GOOS, home, e)
	result.DesktopPath = deskRes.path
	result.DesktopMade = deskRes.made
	result.DesktopChange = deskRes.changed
	result.Backups = append(result.Backups, deskRes.backups...)
	if deskErr != nil {
		errs = append(errs, fmt.Errorf("写入 Claude Desktop 配置失败: %w", deskErr))
	} else {
		if !deskRes.appPresent {
			result.Notes = append(result.Notes, "没有找到 Claude Desktop 的配置目录，已创建；装好 Desktop 后重启它即可生效。")
		}
		result.Notes = append(result.Notes, "Claude Desktop 只在启动时读配置，写完后需要完全退出再打开一次。")
	}
	if len(errs) > 0 {
		return result, errors.Join(errs...)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// CLI
// ---------------------------------------------------------------------------

type cliResult struct {
	path      string
	changed   bool
	unchanged bool
	backup    string
}

// applyCLI 写 ~/.claude/settings.json 的 env。只碰我们这几个键，其余配置
// （权限、hooks、用户自己选的 model）原样保留。
func applyCLI(home string, e Endpoint) (cliResult, error) {
	path := claudeSettingsPath(home)
	res := cliResult{path: path}

	wallet := envFor(e)
	root, err := readJSONObject(path)
	if err != nil {
		return res, err
	}
	env, err := childObject(root, "env")
	if err != nil {
		return res, err
	}
	changed := false
	for k, v := range wallet {
		if cur, ok := env[k].(string); !ok || cur != v {
			env[k] = v
			changed = true
		}
	}
	if !changed {
		res.unchanged = true
		return res, nil
	}
	root["env"] = env
	backup, err := writeJSONObject(path, root)
	if err != nil {
		return res, err
	}
	res.changed = true
	res.backup = backup
	return res, nil
}

// envFor 是 CLI 侧要钉死的环境变量。
//
// 四个档位加 CLAUDE_CODE_SUBAGENT_MODEL 全钉到同一个名字，是为了让内置
// 子 agent 也走网关：Explore/Plan 用 inherit，而 claude-code-guide 走
// haiku、statusline-setup 走 sonnet——不钉死的话它们会按 Anthropic 的
// 真实模型名发请求，绕开平台的模型切换。这也是 magpie 的做法。
//
// haiku 与小模型档位刻意不带 [1m]：它们跑的是短任务，按 1M 上下文计费
// 没有意义，反而可能让上游按大窗口处理。
func envFor(e Endpoint) map[string]string {
	model := strings.TrimSpace(e.Model)
	if model == "" {
		model = "default"
	}
	big := model
	small := stripContextTag(model)
	return map[string]string{
		"ANTHROPIC_BASE_URL":             e.BaseURL,
		"ANTHROPIC_AUTH_TOKEN":           e.Token,
		"ANTHROPIC_MODEL":                big,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   big,
		"ANTHROPIC_DEFAULT_SONNET_MODEL": big,
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  big,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  small,
		"ANTHROPIC_SMALL_FAST_MODEL":     small,
		"CLAUDE_CODE_SUBAGENT_MODEL":     small,
	}
}

// stripContextTag 去掉 "[1m]" / "[2m]" 后缀。
func stripContextTag(model string) string {
	lower := strings.ToLower(model)
	for _, tag := range []string{"[1m]", "[2m]"} {
		if strings.HasSuffix(lower, tag) {
			return strings.TrimSpace(model[:len(model)-len(tag)])
		}
	}
	return model
}

// ---------------------------------------------------------------------------
// Desktop（3p 模式）
// ---------------------------------------------------------------------------

type deskResult struct {
	path       string
	made       bool
	changed    bool
	appPresent bool
	backups    []string
}

// applyDesktop 写 3p 模式需要的四个文件。
func applyDesktop(goos, home string, e Endpoint) (deskResult, error) {
	modeDir, threeP := desktopDirs(goos, home)
	res := deskResult{path: threeP}
	// 目录已经在了，就说明这台机器装过 Desktop；不在则写出来的配置要等它
	// 装好并重启才生效，Result.Notes 里会提示。
	res.appPresent = dirExists(modeDir)

	modeConfig := joinFor(goos, modeDir, "claude_desktop_config.json")
	threePConfig := joinFor(goos, threeP, "claude_desktop_config.json")
	library := joinFor(goos, threeP, "configLibrary")
	meta := joinFor(goos, library, "_meta.json")
	profile := joinFor(goos, library, profileID+".json")

	// 先确认每个文件都能解析，再动手写：半路失败会留下一个连 Desktop
	// 自己都打不开的配置。
	for _, f := range []string{modeConfig, threePConfig, profile, meta} {
		if _, err := readJSONObject(f); err != nil {
			return res, fmt.Errorf("%s 无法解析，已放弃写入以免破坏原配置: %w", f, err)
		}
	}

	// 1. 两个 claude_desktop_config.json 写 deploymentMode=3p
	for _, f := range []string{modeConfig, threePConfig} {
		root, _ := readJSONObject(f)
		if mode, _ := root["deploymentMode"].(string); mode == "3p" && fileExists(f) {
			continue
		}
		// 先记下「原来没有」，再写：写完再问就一定存在了。
		existed := fileExists(f)
		root["deploymentMode"] = "3p"
		backup, err := writeJSONObject(f, root)
		if err != nil {
			return res, err
		}
		res.made = res.made || !existed
		res.changed = true
		res.backups = append(res.backups, backup)
	}

	// 2. profile：网关连接信息
	prof, err := readJSONObject(profile)
	if err != nil {
		return res, err
	}
	prof["inferenceProvider"] = "gateway"
	prof["inferenceGatewayBaseUrl"] = e.GatewayURL
	prof["inferenceGatewayApiKey"] = e.Token
	prof["inferenceGatewayAuthScheme"] = "bearer"
	// 只在缺失时补，不覆盖用户已经在 Desktop 里调过的值。第一项藏掉部署模式
	// 选择器，第二项让 Cowork 的沙箱能出网；两者都由 Desktop 自己的界面管理，
	// 重复执行时保留用户的选择。
	if _, ok := prof["disableDeploymentModeChooser"]; !ok {
		prof["disableDeploymentModeChooser"] = true
	}
	if _, ok := prof["coworkEgressAllowedHosts"]; !ok {
		prof["coworkEgressAllowedHosts"] = []string{"*"}
	}
	backup, err := writeJSONObject(profile, prof)
	if err != nil {
		return res, err
	}
	res.changed = true
	res.backups = append(res.backups, backup)

	// 3. _meta.json：把这份 profile 设为 appliedId，并保留其余条目
	metaRoot, err := readJSONObject(meta)
	if err != nil {
		return res, err
	}
	entries := metaEntries(metaRoot["entries"])
	if !hasEntry(entries, profileID) {
		entries = append(entries, map[string]any{"id": profileID, "name": "LLM Gateway"})
	}
	metaRoot["entries"] = entries
	metaRoot["appliedId"] = profileID
	backup, err = writeJSONObject(meta, metaRoot)
	if err != nil {
		return res, err
	}
	res.backups = append(res.backups, backup)
	return res, nil
}

// metaEntries 取出现有的 entries，保留原顺序与未知字段。
func metaEntries(raw any) []map[string]any {
	list, _ := raw.([]any)
	out := make([]map[string]any, 0, len(list)+1)
	for _, item := range list {
		if entry, ok := item.(map[string]any); ok {
			out = append(out, entry)
		}
	}
	return out
}

func hasEntry(entries []map[string]any, id string) bool {
	for _, entry := range entries {
		if v, _ := entry["id"].(string); v == id {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// JSON 读写：保留未知字段，写前备份
// ---------------------------------------------------------------------------

// readJSONObject 读一个 JSON 对象；文件不存在返回空对象，文件存在但坏了
// 返回错误（绝不把它当成空对象覆盖掉）。
func readJSONObject(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]any{}, nil
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	if root == nil {
		return map[string]any{}, nil
	}
	return root, nil
}

// childObject 取一个子对象；存在但不是对象时报错，而不是覆盖掉它。
func childObject(root map[string]any, key string) (map[string]any, error) {
	v, ok := root[key]
	if !ok || v == nil {
		return map[string]any{}, nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%q 不是对象，已放弃写入", key)
	}
	return obj, nil
}

// writeJSONObject 备份后写回，返回备份路径（没有旧文件时为空）。
func writeJSONObject(path string, root map[string]any) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	backup, err := backupFile(path)
	if err != nil {
		return "", err
	}
	body, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", err
	}
	body = append(body, '\n')
	// 0o600：这些文件里有网关密钥。
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", err
	}
	return backup, nil
}

// backupFile 把现有文件复制一份带时间戳的副本，返回副本路径。
func backupFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	backup := fmt.Sprintf("%s.bak-%s", path, nowStamp())
	if err := os.WriteFile(backup, raw, 0o600); err != nil {
		return "", err
	}
	return backup, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
