package gateway

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Claude Desktop 指向第三方网关时，只保留「读起来像 Anthropic 的」模型 id：
// id 里得出现 claude / sonnet / opus / haiku / fable / mythos / anthropic，
// 且不含别家厂商名（gpt、glm、qwen、deepseek…）。它的判断在 app.asar 里，
// 对 /v1/models 返回的每一行都跑一遍，一条都不留就报
// 「Your organization hasn't configured any models」。
//
// 于是只提供 m1 / m2 的网关在 Desktop 里看到的是空清单。magpie 的解法是给
// 这类模型换一个它认得的 id 再列出来（anthropic/magpie-<10 位数字>），请求
// 回来时反查回真实模型。这里照做，别名换成 anthropic/gw-<10 位数字>。
//
// 别名必须稳定：Desktop 会把用户选的模型 id 写进会话，id 一变旧会话就打不开。
// 所以数字是真实模型 id 的哈希，不是序号——增删别的模型不会让已有的别名挪位。
const desktopAliasPrefix = "anthropic/gw-"

// desktopDeniedWords 是 Desktop 一律拒收的厂商词。命中即拒，即使同时含
// claude（"anthropic/deepseek-claude" 这类中转命名也照样被挡）。
var desktopDeniedWords = []string{
	"ark-code", "astron", "command-r", "deepseek", "doubao", "gemini", "gemma",
	"glm", "gpt", "grok", "hermes", "hy3", "kimi", "lfm", "llama", "longcat",
	"mimo", "minimax", "mistral", "mixtral", "moonshot", "nemotron", "openai",
	"phi-", "qianfan", "qwen", "tc-code", "unic", "yi-", "stepfun", "step-3",
	"seed-", "bytedance", "hunyuan", "granite", "amazon.nova", "nova-",
	"devstral", "ministral", "ernie", "codex", "arcee", "trinity", "abab",
	"jamba", "arctic", "solar", "mercury", "zamba", "kat-coder", "ds-", "dpsk",
}

// desktopAcceptedWords 是 Desktop 认作 Anthropic 模型的词。
var desktopAcceptedWords = []string{"claude", "sonnet", "opus", "haiku", "fable", "mythos", "anthropic"}

// desktopAcceptsModelID 复刻 Desktop 的过滤，决定一个 id 能不能直接列给它。
func desktopAcceptsModelID(id string) bool {
	lower := strings.ToLower(strings.TrimSpace(id))
	if lower == "" {
		return false
	}
	if isDesktopTierID(lower) {
		return true
	}
	for _, word := range desktopDeniedWords {
		if strings.Contains(lower, word) {
			return false
		}
	}
	for _, word := range desktopAcceptedWords {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

// isDesktopTierID 匹配裸档位名（sonnet / opus-5.5 这种），Desktop 直接放行。
func isDesktopTierID(id string) bool {
	base := id
	if index := strings.IndexByte(base, '-'); index >= 0 {
		base = base[:index]
	}
	switch base {
	case "sonnet", "opus", "haiku", "fable", "mythos":
	default:
		return false
	}
	for _, r := range id[len(base):] {
		if r != '-' && r != '.' && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// desktopModelAlias 是一个真实模型 id 对应的 Desktop 别名。哈希保证稳定。
func desktopModelAlias(modelID string) string {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(modelID))
	return fmt.Sprintf("%s%010d", desktopAliasPrefix, hash.Sum64()%1e10)
}

// desktopAliasCandidates 是 Desktop 在这个 Key 下能看到的全部真实 id。除了
// provider 自己的模型，还有 Key 的固定模型与映射别名——modelsForRequest 会把
// 它们一起列进 /v1/models，所以它们同样会被换成别名，反查也就必须认得它们，
// 否则用户选中「m2」这种映射别名后，上游收到的是那串 anthropic/gw- 哈希。
//
// 反查只认这份集合：已删除或撤销授权的模型不会因为别名被别人猜中就重新可达。
func desktopAliasCandidates(provider domain.Provider, key domain.APIKey, gatewayKeyMatched bool) []string {
	ids := make([]string, 0, len(provider.Models)+1+len(key.ModelAliases))
	for _, model := range provider.Models {
		if id := strings.TrimSpace(model.ID); id != "" {
			ids = append(ids, id)
		}
	}
	if gatewayKeyMatched {
		if override := strings.TrimSpace(key.ModelOverride); override != "" {
			ids = append(ids, override)
		}
		for alias := range key.ModelAliases {
			if trimmed := strings.TrimSpace(alias); trimmed != "" {
				ids = append(ids, trimmed)
			}
		}
	}
	return ids
}

// desktopModelForAlias 把别名反查回真实模型 id，不是别名则原样返回。
//
// 查不到就原样返回，让上游去报「模型不存在」——比在这里编一个更诚实。
func desktopModelForAlias(requestModel string, candidates []string) string {
	trimmed := strings.TrimSpace(requestModel)
	if !strings.HasPrefix(strings.ToLower(stripClaudeContextTag(trimmed)), desktopAliasPrefix) {
		return trimmed
	}
	asked := stripClaudeContextTag(trimmed)
	for _, id := range candidates {
		if strings.EqualFold(desktopModelAlias(id), asked) {
			return id
		}
	}
	return trimmed
}

// desktop1M 是 Desktop 认作「长窗口」的上下文门槛。它的 app.asar（2.7032）在
// 读 /v1/models 时按这个数判：id 带 "[1m]" 的行它不再补一条，其余行
// max_input_tokens >= 1e6 时它会在旁边自己加一条「1M context window」。
const desktop1M = 1_000_000

// desktop1MSaid 是 Desktop 给它自己补的那条 1M 菜单项写的说明。
//
// 我们只列一条（id 直接带 "[1m]"），所以「这条是 1M 的」得自己说出口：不写
// 这句话，Desktop 就按普通模型显示它，用户看不出区别——magpie 的
// DesktopLongest 就是这么被反馈「开关像是反的」的。
const desktop1MSaid = "1M context window"

// desktopModelEntry 是 /v1/models 里给 Desktop 的一行：模型本体，加上它是不是
// 按 1M 的 id 列出去的（那一行要多一句说明）。
type desktopModelEntry struct {
	Model domain.Model
	OneM  bool
}

// desktopModelView 把一份真实模型清单转成 Desktop 能认的样子：本来就被它
// 接受的 id 原样保留（官方 Claude 模型在 Desktop 里有名字和图标），其余换成
// 别名，真实名字放进 Name 供菜单显示。
//
// 窗口够 1M 的模型按 "[1m]" 的 id 列出去：Desktop 的 Code 页把选中的 id 交给
// Claude Code，带 "[1m]" 的那个才会跑 1M 窗口。反过来说，不带 "[1m]" 的 1M
// 模型会被 Desktop 在菜单里额外补一条「1M context window」，于是同一个模型
// 出现两次（magpie #1272：21 个模型变成 34 条）。带 "[1m]" 的 id 它不再补，
// 一个模型就只有一条。
//
// 旧会话里存的是不带 "[1m]" 的那条 id，照样能用：反查只按真实模型算别名，
// 与列出去的形态无关（desktopModelForAlias），而 "[1m]" 在模型解析时会被去掉
// （见 resolveConsumerModelOnProvider）。
func desktopModelView(catalog []domain.Model) []desktopModelEntry {
	out := make([]desktopModelEntry, 0, len(catalog))
	for _, model := range catalog {
		realID := strings.TrimSpace(model.ID)
		if realID == "" {
			continue
		}
		view := model
		// 窗口先按真实 id 定下来，再换 id：换成 anthropic/gw-<哈希> 之后就再也
		// 查不到厂商了，那时候再 resolve 只会得到默认的 128K。运维给这个模型
		// 设过窗口时按他设的算（见 modelWindow）。
		view.ContextLength = modelWindow(view)
		if strings.TrimSpace(view.Name) == "" {
			// 菜单显示的是这个名字。我们后来才给 id 加 "[1m]"，所以它天然不带
			// 标记；上游自己把标记写进 id 的（有的中转站这么卖），那标记就是
			// 名字的一部分，照抄。
			view.Name = realID
		}
		if !desktopAcceptsModelID(realID) {
			view.ID = desktopModelAlias(realID)
		}
		entry := desktopModelEntry{Model: view}
		if view.ContextLength >= desktop1M {
			entry.OneM = true
			// 上游自己就把 id 写成 "[1m]" 的不要再叠一层。
			if !strings.HasSuffix(strings.ToLower(view.ID), claudeContextTag1M) {
				entry.Model.ID += claudeContextTag1M
			}
		}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Model.ID < out[j].Model.ID })
	return out
}

// desktopModelList 是 /v1/models 给 Claude Desktop 的那一份。
//
// 上下文窗口按客户端认的三个名字一起给：Desktop 判 1M 看的是
// max_input_tokens（magpie 的 modelObject 也是三个名字一起发），而我们以前只发
// context_length，于是它把每个模型都当成短窗口，Code 页永远跑 200K。
func desktopModelList(catalog []domain.Model) []map[string]any {
	entries := desktopModelView(catalog)
	data := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		contextLen := entry.Model.ContextLength
		item := map[string]any{
			"id":               entry.Model.ID,
			"object":           "model",
			"created":          0,
			"owned_by":         entry.Model.ProviderID,
			"context_length":   contextLen,
			"context_window":   contextLen,
			"max_input_tokens": contextLen,
			"display_name":     entry.Model.Name,
		}
		if entry.OneM {
			item["description"] = desktop1MSaid
		}
		data = append(data, item)
	}
	return data
}

// isClaudeDesktopRequest 认出 Claude Desktop 自己的请求：Electron 会话的
// User-Agent 形如 "Mozilla/5.0 … Claude/2.7032.0 Chrome/…"。只看这两个特征，
// 不解析版本号。
func isClaudeDesktopRequest(userAgent string) bool {
	return strings.HasPrefix(userAgent, "Mozilla/") && strings.Contains(userAgent, " Claude/")
}
