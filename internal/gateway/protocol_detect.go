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
		add(domain.ProtocolClaude, base, "")
		return roots
	}
	// 普通 OpenAI 风格的根：兄弟是它自己加上 /anthropic。
	add(domain.ProtocolOpenAIChat, base, "")
	add(domain.ProtocolOpenAIResponses, base, "")
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
	// 0 表示连不上：不能据此断言不存在，宁可报"未知"由调用方当作不支持。
	if status == 0 {
		return false
	}
	return true
}

// detectProviderProtocols 探一遍这条上游支持的协议。
func (s *Server) detectProviderProtocols(ctx context.Context, provider domain.Provider) []protocolDetection {
	candidates := protocolProbeCandidates(provider.BaseURL, provider.Protocol)
	out := make([]protocolDetection, 0, len(candidates))
	for _, candidate := range candidates {
		result := protocolDetection{
			Protocol: candidate.Protocol,
			URL:      candidate.URL,
			Current:  candidate.Protocol == provider.Protocol,
		}
		status, detail := s.probeOneProtocol(ctx, provider, candidate)
		result.Status = status
		result.Detail = detail
		result.Supported = classifyProtocolProbe(status)
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

	timeoutCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
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

	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
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
		"providerId":    provider.ID,
		"baseUrl":       provider.BaseURL,
		"protocols":     results,
		"protocolSupport": provider.ProtocolSupport,
	})
}

// supportedProtocols 把探测结果压成协议名清单（只留可用的）。
func supportedProtocols(results []protocolDetection) []string {
	out := make([]string, 0, len(results))
	for _, item := range results {
		if item.Supported {
			out = append(out, string(item.Protocol))
		}
	}
	return out
}

// persistProtocolSupport 把探测结论写回 provider，并落盘。
//
// 失败只记日志：探测是个附加信息，不该因为它写不进去就让建 provider 这件事失败。
func (s *Server) persistProtocolSupport(providerID string, results []protocolDetection) {
	protocols := supportedProtocols(results)
	if _, err := s.router.SetProviderProtocolSupport(providerID, protocols); err != nil {
		s.logs.AddApp("warn", "protocol support not saved", fmt.Sprintf("provider=%s: %v", providerID, err))
		return
	}
	if err := s.saveState(); err != nil {
		s.logs.AddApp("warn", "protocol support not persisted", fmt.Sprintf("provider=%s: %v", providerID, err))
	}
}

// autoDetectProviderProtocols 在 provider 建好或 baseUrl 变更之后，后台探一遍
// 它支持哪些协议。
//
// 为什么是后台而不是同步：探测要真的往上游发请求，慢起来能到十几秒（每条候选
// 一个 15s 超时）。让「保存 provider」等它，等于给一个本来瞬时的操作绑上网络
// 延迟。所以这里起一个 goroutine，结论写完才出现在控制台上。
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
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		results := s.detectProviderProtocols(ctx, provider)
		protocols := supportedProtocols(results)
		if len(protocols) == 0 {
			// 一条都没探到，多半是这台机器此刻连不上上游（或被 Cloudflare 挡）。
			// 不要把空结论写进去——那会把"没探成"记成"什么都不支持"。
			s.logs.AddApp("warn", "protocol detection found nothing", fmt.Sprintf("provider=%s baseUrl=%s", provider.ID, provider.BaseURL))
			return
		}
		s.persistProtocolSupport(provider.ID, results)
		s.logs.AddApp("info", "protocol support detected", fmt.Sprintf("provider=%s protocols=%s", provider.ID, strings.Join(protocols, ",")))
	}()
}

