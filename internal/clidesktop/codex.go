package clidesktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Codex 那一侧的一键写入。
//
// Codex 读的是 ~/.codex/config.toml（TOML），和 Claude 的 JSON 不是一回事，
// 所以单独一个文件。做法与 magpie / CC Switch 对齐：只替换本工具管理的那一段，
// 文件里其余内容（[features]、[memories]、用户自己的 MCP 配置……）一个字都不动。
//
// 段用注释行当分界线：TOML 没有「命名块」这种东西，注解是唯一能标记"这一段是
// 别人写的"的语法，也正好让人打开文件就看出哪些不是自己的。

// codexBlockBegin / codexBlockEnd 与前端 lib.tsx 里的
// CODEX_PROVIDER_BLOCK_BEGIN / END 必须完全一致：脚本和本机写入改的是同一段，
// 两边写法不同就会在文件里留下两份互相冲突的 provider 表。
const (
	codexBlockBegin = "################ LPG-CODEX-PROVIDER-BEGIN ################"
	codexBlockEnd   = "################ LPG-CODEX-PROVIDER-END ################"
)

// CodexEndpoint 是 Codex 侧要写入的连接信息。
type CodexEndpoint struct {
	// ProviderID 是 [model_providers.<id>] 里的 id，也写进 model_provider。
	ProviderID string
	// ProviderName 是这一段 provider 的显示名。
	ProviderName string
	// BaseURL 是网关的 /openai/v1 根地址（Codex 自己拼 /responses）。
	BaseURL string
	// Token 是 API 密钥。
	Token string
	// Model 是写进 model 的模型名。
	Model string
	// ReasoningEffort 是 model_reasoning_effort，空则不写这一行。
	ReasoningEffort string
	// ModelCatalogPath 是 model_catalog_json 的路径：可以是绝对路径，也可以
	// 相对于主目录（前端只传相对路径，只有这里知道 Home() 在哪）。
	ModelCatalogPath string
	// ModelCatalog 是要写到 ModelCatalogPath 的内容，空则不写这个文件。
	ModelCatalog string
	// KeepOfficialLogin 决定要不要动 auth.json。
	//
	// Codex 判定"已登录"看的是 auth.json 里的 auth_mode，不是 config.toml 的
	// provider 段——配置写得再对，没有 auth.json 它照样一进来就弹登录页。所以
	// 想让 CLI 直接可用就必须写 auth.json（auth_mode=apikey）。
	//
	// 但那是本机唯一的一份凭据文件：CLI 和 Codex Desktop 共用 ~/.codex，用户如果
	// 本来用 ChatGPT 账号登录（auth_mode=chatgpt），覆盖掉等于把他踢下线。所以这
	// 个开关默认关，只有用户明确同意时才写。
	KeepOfficialLogin bool
}

// CodexResult 汇总一次 Codex 写入做了什么。
type CodexResult struct {
	ConfigPath  string   `json:"configPath"`
	Changed     bool     `json:"changed"`
	Unchanged   bool     `json:"unchanged"`
	AuthPath    string   `json:"authPath,omitempty"`
	AuthChanged bool     `json:"authChanged,omitempty"`
	CatalogPath string   `json:"catalogPath,omitempty"`
	Backups     []string `json:"backups"`
	Notes       []string `json:"notes"`
}

// codexConfigPath 是 Codex 的配置文件位置。
//
// CLI 与 Codex Desktop 读的是同一份 ~/.codex/config.toml（Desktop 只是多认几个
// 自己的 [desktop] 段），所以写这一处两端都生效，不需要分别写。
func codexConfigPath(home string) string {
	return filepath.Join(home, ".codex", "config.toml")
}

// codexAuthPath 是 Codex 的凭据文件。
func codexAuthPath(home string) string {
	return filepath.Join(home, ".codex", "auth.json")
}

// ApplyCodex 把 Codex（CLI 与 Desktop 共用同一份配置）指向网关。
//
// 和 Claude 的写入一样只碰我们负责的那一段：先摘掉上一次写进去的 BEGIN/END
// 区块，再把新的一段放到文件最前面。放最前面而不是追加到末尾，是因为 TOML 的
// 顶层键必须在任何 [table] 之前——追加到末尾会让 model_provider 落进上一个表里。
func ApplyCodex(e CodexEndpoint) (CodexResult, error) {
	res := CodexResult{}
	if strings.TrimSpace(e.BaseURL) == "" || strings.TrimSpace(e.Token) == "" {
		return res, errors.New("Codex 的网关地址与密钥都不能为空")
	}
	providerID := strings.TrimSpace(e.ProviderID)
	if providerID == "" {
		providerID = "lpg"
	}
	home, err := Home()
	if err != nil {
		return res, fmt.Errorf("定位用户主目录失败: %w", err)
	}
	path := codexConfigPath(home)
	res.ConfigPath = path

	// model_catalog_json 必须是绝对路径（Codex 不展开 ~），所以相对路径在这里
	// 换成基于主目录的绝对路径。
	if catalog := strings.TrimSpace(e.ModelCatalogPath); catalog != "" && !filepath.IsAbs(catalog) {
		e.ModelCatalogPath = filepath.Join(home, catalog)
	}
	// 模型目录必须是"至少有一个模型"的合法 JSON：Codex 读到空清单会直接拒绝
	// 启动（model_catalog_json path ... must contain at least one model），
	// 那是比缺元数据严重得多的失败。内容不合格就整段不写、配置里也不再引用它。
	//
	// 这一步必须排在拼配置之前：e.ModelCatalogPath 会被下面当作"要不要写这一行"
	// 的依据，改晚了就会写出一条指向不存在文件的 model_catalog_json。
	if strings.TrimSpace(e.ModelCatalog) != "" && strings.TrimSpace(e.ModelCatalogPath) != "" {
		if refusal := codexCatalogRefusal(e.ModelCatalog); refusal != "" {
			res.Notes = append(res.Notes, "模型目录未写入："+refusal)
			e.ModelCatalogPath = ""
			e.ModelCatalog = ""
		}
	}

	existing, err := readTextFile(path)
	if err != nil {
		return res, err
	}
	block := codexProviderBlock(e, providerID)
	merged := block + "\n" + stripCodexBlock(existing)
	if existing != "" && merged == existing {
		res.Unchanged = true
		return res, nil
	}
	backup, err := writeTextFile(path, merged)
	if err != nil {
		return res, err
	}
	res.Changed = true
	res.Backups = append(res.Backups, backup)

	if strings.TrimSpace(e.ModelCatalog) != "" && strings.TrimSpace(e.ModelCatalogPath) != "" {
		catalogBackup, catalogErr := writeTextFile(e.ModelCatalogPath, e.ModelCatalog)
		if catalogErr != nil {
			// 模型目录只是元数据，写不进去不影响转发：记一条说明，不算整体失败。
			res.Notes = append(res.Notes, fmt.Sprintf("模型目录没写进去（%v），Codex 仍可用默认元数据", catalogErr))
		} else {
			res.CatalogPath = e.ModelCatalogPath
			res.Backups = append(res.Backups, catalogBackup)
		}
	}
	// 第三方 provider 下长会话自动压缩常报 compaction 相关错误，这是 Codex
	// 自己的已知缺陷；官方给的规避办法就是关掉它。这里只写配置、不执行命令：
	// 网关进程可能根本没有 codex 这个二进制。

	if e.KeepOfficialLogin {
		res.Notes = append(res.Notes, "已保留原有登录凭据（auth.json 未改动）；配置本身已指向网关。")
	} else {
		// 覆盖之前先看一眼原来那份凭据能不能登进去：能的话一定要说出来，
		// 那是用户在别处（Codex Desktop 里）真金白银登的账号，被顶掉会有感觉。
		existingAuth, _ := readJSONObject(codexAuthPath(home))
		hadLogin := codexAuthHasLoginMaterial(existingAuth)

		authResult, authErr := applyCodexAuth(home, e.Token)
		res.AuthPath = authResult.path
		res.AuthChanged = authResult.changed
		res.Backups = append(res.Backups, authResult.backups...)
		if authErr != nil {
			return res, authErr
		}
		if authResult.changed {
			res.Notes = append(res.Notes, "已把网关密钥写入 auth.json（auth_mode=apikey），Codex 启动时不会再要求登录。")
			if hadLogin {
				res.Notes = append(res.Notes, "注意：这里原本有一份可用的官方登录凭据，已被网关密钥取代（原文件已备份）。想保住它请勾选「保留官方登录」。")
			}
		} else {
			res.Notes = append(res.Notes, "auth.json 里已经是这把网关密钥，未做改动。")
		}
	}
	res.Notes = append(res.Notes, "Codex 只在启动时读配置，重开一次终端里的 codex 会话即可生效；Codex Desktop 需要完全退出后重开。")
	return res, nil
}

type codexAuthResult struct {
	path    string
	changed bool
	backups []string
}

// codexCatalogRefusal 报告模型目录内容为什么不能用，"" 表示可以用。
//
// Codex 对 model_catalog_json 的要求比"是个 JSON"严得多：解析失败、或者 models
// 为空，都会让它在启动阶段直接失败退出——不是降级、是连会话都进不去。所以这里
// 先按它的规矩验一遍，宁可退回去用内置元数据。
func codexCatalogRefusal(content string) string {
	var parsed struct {
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return fmt.Sprintf("不是合法 JSON（%v）", err)
	}
	if len(parsed.Models) == 0 {
		return "models 为空，Codex 会拒绝启动"
	}
	return ""
}

// applyCodexAuth 写 auth.json，让 Codex 认为"已登录"。
//
// 这不是可选项：Codex 的交互入口在 auth.json 缺失时一律先弹登录页，哪怕
// config.toml 里的 provider 指向的是一个自带凭据的第三方网关。经 `codex login
// --with-api-key` 写入后，文件形状就是下面这两个键，Codex 自己也是这么写的。
//
// 只覆盖 auth_mode / OPENAI_API_KEY 两个键，其余（chatgpt 的 tokens 等）留着：
// 万一用户切回官方登录，那边的东西还在。
//
// 但"留着"有个前提——残留的东西得能真的登进去。`auth_mode` 是显式声明，
// Codex 见到它就不再往下看别的凭据（规则见 cc-switch 的
// codex_auth_resolved_mode，它复刻了 Codex 的 AuthDotJson::resolved_mode）。
// 也就是说一旦写成 apikey，后续就算把 auth_mode 删掉，也不该指望旁边那份
// 半残的 tokens 还能顶上。
func applyCodexAuth(home, token string) (codexAuthResult, error) {
	path := codexAuthPath(home)
	res := codexAuthResult{path: path}
	root, err := readJSONObject(path)
	if err != nil {
		return res, err
	}
	current, _ := root["auth_mode"].(string)
	currentKey, _ := root["OPENAI_API_KEY"].(string)
	if strings.TrimSpace(current) == "apikey" && currentKey == strings.TrimSpace(token) {
		return res, nil
	}
	root["auth_mode"] = "apikey"
	root["OPENAI_API_KEY"] = strings.TrimSpace(token)
	backup, err := writeJSONObject(path, root)
	if err != nil {
		return res, err
	}
	res.changed = true
	res.backups = append(res.backups, backup)
	return res, nil
}

// codexAuthHasLoginMaterial 报告这份 auth.json 里有没有"真的能登进去"的凭据。
//
// 与 cc-switch 的 codex_auth_has_credential_login_material 同一口径：只要存在
// 非空的 personal_access_token / agent_identity / bedrock_api_key 之一，或者
// tokens 里三个令牌有一个非空，就算有。空串、空对象、null 都不算。
//
// 我们只在提示语里用它：本机已经有一份能用的官方登录时，别让用户以为关掉
// "保留官方登录"不会有代价。
func codexAuthHasLoginMaterial(root map[string]any) bool {
	present := func(value any) bool {
		switch typed := value.(type) {
		case nil:
			return false
		case string:
			return strings.TrimSpace(typed) != ""
		case map[string]any:
			return len(typed) > 0
		case []any:
			return len(typed) > 0
		default:
			return true
		}
	}
	for _, key := range []string{"personal_access_token", "agent_identity", "bedrock_api_key"} {
		if present(root[key]) {
			return true
		}
	}
	tokens, ok := root["tokens"].(map[string]any)
	if !ok {
		return false
	}
	for _, key := range []string{"id_token", "access_token", "refresh_token"} {
		if present(tokens[key]) {
			return true
		}
	}
	return false
}

// codexProviderBlock 拼出本工具管理的那一整段 TOML。
func codexProviderBlock(e CodexEndpoint, providerID string) string {
	var b strings.Builder
	b.WriteString(codexBlockBegin + "\n")
	b.WriteString("# 以下段落由 LLM Protocol Gateway 写入；重新写入只会替换这一段，其余配置不动。\n")
	b.WriteString(fmt.Sprintf("model_provider = %s\n", tomlString(providerID)))
	if model := strings.TrimSpace(e.Model); model != "" {
		b.WriteString(fmt.Sprintf("model = %s\n", tomlString(model)))
	}
	if effort := strings.TrimSpace(e.ReasoningEffort); effort != "" {
		b.WriteString(fmt.Sprintf("model_reasoning_effort = %s\n", tomlString(effort)))
	}
	if catalog := strings.TrimSpace(e.ModelCatalogPath); catalog != "" && strings.TrimSpace(e.ModelCatalog) != "" {
		b.WriteString(fmt.Sprintf("model_catalog_json = %s\n", tomlString(catalog)))
	}
	b.WriteString("approval_policy = \"never\"\n")
	b.WriteString("sandbox_mode = \"danger-full-access\"\n\n")
	b.WriteString(fmt.Sprintf("[model_providers.%s]\n", providerID))
	name := strings.TrimSpace(e.ProviderName)
	if name == "" {
		name = providerID
	}
	b.WriteString(fmt.Sprintf("name = %s\n", tomlString(name)))
	b.WriteString(fmt.Sprintf("base_url = %s\n", tomlString(strings.TrimRight(strings.TrimSpace(e.BaseURL), "/"))))
	b.WriteString("wire_api = \"responses\"\n")
	// requires_openai_auth 决定的是「登录界面」，不是「请求带什么凭据」。
	//
	// Codex 0.149+ 的行为（cc-switch 在 live/project/codex.rs 里注释得很清楚）：
	// 这个值为 true 而 auth.json 里没有能用的登录时，Codex 卡在登录页不放；
	// 为 false 时它直接用表里的 experimental_bearer_token 发请求。我们的凭据
	// 本来就走 bearer，所以必须写 false——写 true 就会出现「配置明明对，一进
	// TUI 还是要你选登录方式」。
	//
	// 保持官方登录（KeepOfficialLogin）时才写 true：那时 auth.json 里有一份真
	// 登录，Codex 认它，账号信息和 token 刷新都照常。
	b.WriteString(fmt.Sprintf("requires_openai_auth = %t\n", e.KeepOfficialLogin))
	// Codex 0.118+ 会先试 WebSocket 连 /v1/responses（OpenAI 内部协议，第三方
	// 网关接不住，只会 405 再退回 HTTPS）。显式关掉，省掉那几轮重试。
	b.WriteString("supports_websockets = false\n")
	b.WriteString(fmt.Sprintf("experimental_bearer_token = %s\n", tomlString(strings.TrimSpace(e.Token))))
	b.WriteString(codexBlockEnd + "\n")
	return b.String()
}

// stripCodexBlock 摘掉本工具此前写入的整段（含分界线本身），其余内容原样返回。
//
// 分界线之间是"我们写的"，分界线之外是"用户的"——只按这两行精确匹配，不去
// 猜哪些键可能也是我们的，猜错一次就会删掉用户自己的配置。
func stripCodexBlock(text string) string {
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	inside := false
	for _, line := range lines {
		raw := strings.TrimRight(line, "\r")
		switch {
		case raw == codexBlockBegin:
			inside = true
			continue
		case raw == codexBlockEnd:
			inside = false
			continue
		case inside:
			continue
		}
		out = append(out, line)
	}
	// 摘掉区块后开头会留下一堆空行，收一下；正文内容本身一个都不动。
	result := strings.Join(out, "\n")
	result = strings.TrimLeft(result, "\n")
	return result
}

// tomlString 把任意字符串写成一个 TOML basic string。
//
// 不用手写转义：密钥里出现引号或反斜杠并不罕见，json.Marshal 产生的是同一种
// 双引号字符串语法（TOML basic string 与 JSON string 规则一致），直接借用。
func tomlString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "\"\""
	}
	return string(encoded)
}

// readTextFile 读一个文本文件；不存在当成空串（新建）。
func readTextFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(raw), nil
}

// writeTextFile 备份后整份写入，返回备份路径（没有旧文件时为空）。
func writeTextFile(path, text string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	backup, err := backupFile(path)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	// 0o600：这个文件里有网关密钥。
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", err
	}
	return backup, nil
}
