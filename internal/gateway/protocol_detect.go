package gateway

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 上游协议能力探测。
//
// 有些上游（OpenCode Go 是最典型的一个）在同一个域名下同时提供多种线协议：
//
//	https://opencode.ai/inference/go/anthropic/v1/messages   ← Claude Messages
//	https://opencode.ai/inference/go/v1/chat/completions    ← OpenAI Chat
//	https://opencode.ai/inference/go/v1/responses           ← OpenAI Responses
//
// 对这类上游，「让下游协议决定走哪一条」是有实际收益的：直通只是把字节搬过去，
// 转换则要重写报文、重打包流，每一处都可能吃掉一次本该成功的请求。
//
// 这个探测只回答"这条路径存在吗"，不判断"能不能跑通"——后者要花真额度、要等
// 上游真的生成内容，不该由一次控制台点击来做。判断方法是发一个必然失败的请求，
// 看它落在哪一类错误上：
//
//	404 / 405            → 这条路径没有实现（路由不存在）
//	其他任意 4xx（含 401/403/400）→ 路由存在，只是这次请求不合格
//	2xx                  → 当然存在
//
// 也就是说，**404 是唯一的否定证据**。这也正是 cc-switch 那套路径分流的前提：
// 每条协议一条路径，路径存在与否是静态事实，跟凭据无关。

// protocolProbeTimeout 是单条候选协议的探测上限。
//
// 40s 而不是 15s：探测发出的请求必然会被上游拒掉，但「拒掉」之前它可能要先过一层
// 边缘鉴权、再做路由判定，跨境线路上十几秒被卡断很常见。15s 时 OpenCode Go 的两条
// OpenAI 路径被探成「连接失败」，而同样的地址用 curl 单独打是通的——把「慢」误判成
// 「不存在」，比不探更糟。
const protocolProbeTimeout = 40 * time.Second

// protocolProbe 是一次探测的目标：协议 + 该协议的根地址 + 用来探的路径。
type protocolProbe struct {
	Protocol domain.Protocol
	Root     string
	// URL 是探测真正要打的完整地址（根 + 该协议的典型端点）。
	URL string
}

// protocolProbeCandidates 从一条 base URL 推出同一上游可能存在的其他协议根。
//
// 规则很直接：Claude 的根是「…/anthropic/v1」，同一层级上的兄弟就是「…/v1」；
// 反过来也一样。只做这一层替换，不猜别的命名——猜错只是多探一次，不会改坏配置。
func protocolProbeCandidates(baseURL string, current domain.Protocol) []protocolProbe {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil
	}
	lower := strings.ToLower(base)

	var roots []protocolProbe
	add := func(protocol domain.Protocol, root, endpoint string) {
		root = strings.TrimRight(root, "/")
		if root == "" {
			return
		}
		for _, existing := range roots {
			if existing.Protocol == protocol && existing.Root == root {
				return
			}
		}
		path := "/chat/completions"
		if protocol == domain.ProtocolClaude {
			path = "/messages"
		} else if protocol == domain.ProtocolOpenAIResponses {
			path = "/responses"
		}
		url := root + path
		if strings.Contains(lower, "/"+strings.TrimPrefix(path, "/")) {
			// base 已经把端点写在里面了，直接用它。
			url = base
			root = base
		}
		roots = append(roots, protocolProbe{Protocol: protocol, Root: root, URL: url})
		_ = endpoint
	}

	// 已经带 /anthropic 的基地址：它的兄弟是砍掉 /anthropic 的那一层。
	if index := strings.Index(lower, "/anthropic/"); index >= 0 {
		openaiRoot := base[:index] + base[index+len("/anthropic"):]
		add(domain.ProtocolClaude, base, "")
		add(domain.ProtocolOpenAIChat, openaiRoot, "")
		add(domain.ProtocolOpenAIResponses, openaiRoot, "")
		return roots
	}
	if strings.HasSuffix(lower, "/anthropic") {
		sibling := strings.TrimSuffix(base, "/anthropic")
		add(domain.ProtocolOpenAIChat, sibling, "")
		add(domain.ProtocolOpenAIResponses, sibling, "")
		// Anthropic 风格的根多数是 …/anthropic/v1（DeepSeek 等），端点还要再带
		// 一层 /v1；同根 /messages 作为兜底保留。
		add(domain.ProtocolClaude, base+"/v1", "")
		add(domain.ProtocolClaude, base, "")
		return roots
	}
	// 普通 OpenAI 风格的根：有的上游把 Claude 放在 /anthropic 兄弟路径下，也有
	// 的（OpenCode Go）就在同一个根上开 /messages。两种都探：同根优先，404 时
	// 再退到 /anthropic 兄弟路径。少探这一条会让 OpenCode Go 的 Claude 能力永远
	// 显示不出来。
	add(domain.ProtocolOpenAIChat, base, "")
	add(domain.ProtocolOpenAIResponses, base, "")
	add(domain.ProtocolClaude, base, "")
	add(domain.ProtocolClaude, base+"/anthropic", "")
	return roots
}

// protocolDetection 是一条候选协议探测完之后给控制台的结论。
type protocolDetection struct {
	Protocol domain.Protocol `json:"protocol"`
	URL      string          `json:"url"`
	// Supported 只表示"这条路径存在"，不代表有额度或模型能用。
	Supported bool   `json:"supported"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	// Unknown 表示这次没结论（连不上、超时）。它既不是"支持"也不是"不支持"，
	// 写回结论时要原样保留上一次的判断，而不是把这条抹掉。
	Unknown bool `json:"unknown,omitempty"`
	// Current 标出上游配置里选中的那一条，界面上好对照。
	Current bool `json:"current,omitempty"`
}

// classifyProtocolProbe 把一次探测的状态码归成"这条路径存不存在"。
//
// 404/405 是唯一的否定证据：一个实现了该协议的上游，对不合格的请求会回 401/403/
// 400，绝不会回"路由不存在"。反过来，404 也可能来自一层不认识这个路径的反代——
// 那同样说明"从网关这边走这条路是白费"，结论一致。
func classifyProtocolProbe(status int) bool {
	switch status {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return false
	}
	// 0 表示这次没连上（超时、TLS 断开、域名解析失败）。这不能当成「不存在」：
	// 真正不存在的路径会老老实实回 404，而"连不上"什么也没证明。调用方据此把这条
	// 记成"未知"，绝不写进「不支持」——一次网络抖动不该让一台上游的某个协议从
	// 卡片上消失，更不该让它从此不再参与同协议直通的挑选。
	if status == 0 {
		return false
	}
	return true
}

// detectProviderProtocols 探一遍这条上游支持的协议。
func (s *Server) detectProviderProtocols(ctx context.Context, provider domain.Provider) []protocolDetection {
	candidates := protocolProbeCandidates(provider.BaseURL, provider.Protocol)
	// 同一个协议可能有多条候选路径（例如 Claude 同根 /messages 与 /anthropic
	// 兄弟路径）。按协议分组后只要任意一条成立就算支持，避免后一条 404 把前一条
	// 已经探到的结论覆盖掉。
	order := make([]domain.Protocol, 0, len(candidates))
	grouped := make(map[domain.Protocol][]protocolProbe, len(candidates))
	for _, candidate := range candidates {
		if _, ok := grouped[candidate.Protocol]; !ok {
			order = append(order, candidate.Protocol)
		}
		grouped[candidate.Protocol] = append(grouped[candidate.Protocol], candidate)
	}
	out := make([]protocolDetection, 0, len(order))
	for _, protocol := range order {
		result := protocolDetection{Protocol: protocol, Current: protocol == provider.Protocol}
		unknownDetail := ""
		for _, candidate := range grouped[protocol] {
			status, detail := s.probeOneProtocol(ctx, provider, candidate)
			if result.URL == "" {
				result.URL = candidate.URL
			}
			if classifyProtocolProbe(status) {
				result.URL = candidate.URL
				result.Status = status
				result.Detail = detail
				result.Supported = true
				result.Unknown = false
				break
			}
			if status == 0 {
				if unknownDetail == "" {
					unknownDetail = detail
				}
				continue
			}
			// 记下第一条明确的否定证据，供全部候选都失败时展示。
			if result.Status == 0 && unknownDetail == "" {
				result.Status = status
				result.Detail = detail
			}
		}
		// 有条候选没探出结论（超时/连不上）时，这条协议整体记「未知」：不能拿
		// 另一条候选的 404 去否定它，否则一次网络抖动就会把已支持的协议删掉。
		if !result.Supported && unknownDetail != "" {
			result.Unknown = true
			result.Status = 0
			result.Detail = unknownDetail
		}
		out = append(out, result)
	}
	return out
}

// probeOneProtocol 发一个"注定不合格"的最小请求，只为拿状态码。
//
// 用最小请求体是刻意的：上游要么在鉴权/参数校验那一层就拒掉（这正是我们要的
// 信号），要么真的去生成——那时它会回 400 之类，同样算"存在"。只有路径不存在
// 才会是 404。
func (s *Server) probeOneProtocol(ctx context.Context, provider domain.Provider, candidate protocolProbe) (int, string) {
	body := []byte(`{"model":"` + firstNonEmpty(provider.DefaultModel, "probe") + `"}`)
	switch candidate.Protocol {
	case domain.ProtocolClaude:
		body = []byte(`{"model":"` + firstNonEmpty(provider.DefaultModel, "probe") + `","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)
	case domain.ProtocolOpenAIResponses:
		body = []byte(`{"model":"` + firstNonEmpty(provider.DefaultModel, "probe") + `","input":"hi","max_output_tokens":1}`)
	default:
		body = []byte(`{"model":"` + firstNonEmpty(provider.DefaultModel, "probe") + `","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":1}`)
	}

	// 探测超时给足：这条请求会真的把一个「注定失败」的请求送到上游，而不少上游
	// 要先做一层鉴权/路由再决定拒掉的理由，跨境访问时十几秒很容易被卡掉。
	// 15s 那条曾经把 OpenCode Go 的两条 OpenAI 路径探成「连接失败」——同样的地址
	// 用 curl 单独打是通的，只是慢。
	timeoutCtx, cancel := context.WithTimeout(ctx, protocolProbeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(timeoutCtx, http.MethodPost, candidate.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err.Error()
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if candidate.Protocol == domain.ProtocolClaude {
		request.Header.Set("anthropic-version", "2023-06-01")
	}
	applyRequestAdapterHeaders(request, provider, provider.DefaultModel)
	applyProviderAuth(request, provider, "")
	if candidate.Protocol == domain.ProtocolClaude {
		applyOpenCodeGoClaudeHeaders(request, provider, body)
	}

	response, err := (&http.Client{Timeout: protocolProbeTimeout}).Do(request)
	if err != nil {
		return 0, err.Error()
	}
	defer func() { _ = response.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	return response.StatusCode, strings.TrimSpace(string(raw))
}

// handleDetectProviderProtocols 是控制台上的「重新探测」。
//
// 正常的探测不需要人点：provider 建好、或者 baseUrl 换了之后，后台会自己跑一遍
// （见 autoDetectProviderProtocols）。这个接口留给"我想现在就重探一次"。
func (s *Server) handleDetectProviderProtocols(w http.ResponseWriter, r *http.Request) {
	if !s.requireProviderOwnerForUser(w, r, r.PathValue("id")) {
		return
	}
	var provider domain.Provider
	var err error
	if provider, err = s.router.ProviderByID(r.PathValue("id")); err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	results := s.detectProviderProtocols(r.Context(), provider)
	s.persistProtocolSupport(provider.ID, results)
	provider, _ = s.router.ProviderByID(r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{
		"providerId":      provider.ID,
		"baseUrl":         provider.BaseURL,
		"protocols":       results,
		"protocolSupport": provider.ProtocolSupport,
	})
}

// supportedProtocols 把探测结果压成协议名清单（只留可用的）。
//
// 这次没探出结论的（Unknown：超时、连不上）不算"不支持"：它们既不该被加进来，
// 也不该让上一轮已经探到的结论消失。合并由 mergeProtocolSupport 负责。
func supportedProtocols(results []protocolDetection) []string {
	out := make([]string, 0, len(results))
	for _, item := range results {
		if item.Supported {
			out = append(out, string(item.Protocol))
		}
	}
	return out
}

// mergeProtocolSupport 把这一轮探测结果并进已有结论。
//
// 规则只有一条：**已知的覆盖，未知的保留**。超时是网络现象，不是上游说"我没有这个
// 协议"；一次跨境抖动不该让一张卡片上的协议徽标无声消失，更不该让它从此不再参与
// 同协议直通的挑选。真正的"不支持"（404）才会把它移除。
func mergeProtocolSupport(previous []string, results []protocolDetection) []string {
	keep := make(map[string]bool, len(previous))
	for _, item := range previous {
		keep[item] = true
	}
	for _, item := range results {
		if item.Unknown {
			continue
		}
		name := string(item.Protocol)
		if item.Supported {
			keep[name] = true
		} else {
			delete(keep, name)
		}
	}
	// 按候选顺序输出，保证同样的输入得到同样的顺序（界面和日志都好对照）。
	ordered := make([]string, 0, len(keep))
	for _, item := range results {
		name := string(item.Protocol)
		if keep[name] {
			ordered = append(ordered, name)
			delete(keep, name)
		}
	}
	for _, name := range previous {
		if keep[name] {
			ordered = append(ordered, name)
			delete(keep, name)
		}
	}
	return ordered
}

// persistProtocolSupport 把探测结论写回 provider，并落盘。
//
// 写回的是「上一轮 + 这一轮」的合并结果，不是这一轮单独的结果：这一轮超时的那条
// 会保留上次的判断（见 mergeProtocolSupport）。
//
// 失败只记日志：探测是个附加信息，不该因为它写不进去就让建 provider 这件事失败。
func (s *Server) persistProtocolSupport(providerID string, results []protocolDetection) {
	var previous []string
	if existing, err := s.router.ProviderByID(providerID); err == nil {
		previous = existing.ProtocolSupport
	}
	protocols := mergeProtocolSupport(previous, results)
	if _, err := s.router.SetProviderProtocolSupport(providerID, protocols); err != nil {
		s.logs.AddApp("warn", "protocol support not saved", fmt.Sprintf("provider=%s: %v", providerID, err))
		return
	}
	if err := s.saveState(); err != nil {
		s.logs.AddApp("warn", "protocol support not persisted", fmt.Sprintf("provider=%s: %v", providerID, err))
	}
}

// autoDetectProviderProtocols 在 provider 建好或 baseUrl 变更之后，后台做两件事：
// 探一遍它支持哪些协议，再抓一次模型清单。
//
// 为什么是后台而不是同步：两件事都要真的往上游发请求，慢起来能到几十秒（协议
// 探测每条候选一个 15s 超时）。让「保存 provider」等它们，等于给一个本来瞬时的
// 操作绑上网络延迟。所以这里起一个 goroutine，结论写完才出现在控制台上。
//
// 为什么连模型一起抓：不抓的话新 provider 的目录是空的，而对话测试、监控探测
// 这些"不指定模型"的路径全都得靠目录给出一个真实模型名。留着空目录直接测试，
// 发出去的是占位符 request-model-not-set，换回上游一句莫名其妙的 400 —— 用户
// 明明刚填对了 key，看起来却像没填对。
//
// 只对 api_key 型上游做：OAuth 订阅、对端网关、各种专有客户端（Qoder/Cursor/
// 自注册）的地址不是"一条 baseUrl 派生多条协议路径"的形态，探也是白探。
func (s *Server) autoDetectProviderProtocols(provider domain.Provider) {
	if provider.AuthType != domain.AuthTypeAPIKey {
		return
	}
	if strings.TrimSpace(provider.BaseURL) == "" {
		return
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				s.logs.AddApp("error", "protocol detection panicked", fmt.Sprintf("provider=%s: %v", provider.ID, rec))
			}
		}()
		// 总预算要容得下「抓模型 + 三条候选各一次探测」：单条探测上限是
		// protocolProbeTimeout，三条依次跑就是它的三倍，再给抓模型留一点。
		// 以前写死 60s，放宽探测超时之后会把第三条乃至第二条直接掐掉。
		ctx, cancel := context.WithTimeout(context.Background(), 3*protocolProbeTimeout+30*time.Second)
		defer cancel()

		s.autoFetchProviderModels(ctx, provider)

		// 抓模型可能已经改了内存里的 provider（默认模型被回填），重新取一份再探。
		current := provider
		if refreshed, err := s.router.ProviderByID(provider.ID); err == nil {
			current = refreshed
		}
		results := s.detectProviderProtocols(ctx, current)
		// 合并写回：这一轮超时的协议保留上次结论，不会因为一次网络抖动就从卡片上
		// 消失（见 mergeProtocolSupport）。
		s.persistProtocolSupport(provider.ID, results)
		if saved, err := s.router.ProviderByID(provider.ID); err == nil {
			s.logs.AddApp("info", "protocol support detected", fmt.Sprintf(
				"provider=%s protocols=%s", provider.ID, strings.Join(saved.ProtocolSupport, ",")))
		}
	}()
}

// autoFetchProviderModels 在后台抓一次模型清单，成功就落到 provider 上。
//
// 与「获取模型」按钮共用同一条实现（fetchProviderModels），所以凭据、自定义头、
// 各订阅类型的特殊取法全都一致；差别只是它可以自己发起。
func (s *Server) autoFetchProviderModels(ctx context.Context, provider domain.Provider) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "/__providers/"+provider.ID+"/test", nil)
	if err != nil {
		return
	}
	result := s.fetchProviderModels(request, provider, time.Now())
	if !result.Success {
		s.logs.AddApp("info", "provider models not fetched automatically", fmt.Sprintf(
			"provider=%s: %s", provider.ID, result.Error))
		return
	}
	if len(result.Models) == 0 {
		return
	}
	if _, err := s.router.UpdateProviderModels(provider.ID, result.Models, "healthy"); err != nil {
		s.logs.AddApp("warn", "provider models not saved", fmt.Sprintf("provider=%s: %v", provider.ID, err))
		return
	}
	if err := s.saveState(); err != nil {
		s.logs.AddApp("warn", "provider models not persisted", fmt.Sprintf("provider=%s: %v", provider.ID, err))
		return
	}
	s.logs.AddApp("info", "provider models fetched automatically", fmt.Sprintf(
		"provider=%s models=%d", provider.ID, len(result.Models)))
}
