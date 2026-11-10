package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// handlePeerMe 让下游确认自己的凭据还有效、拿到了哪些 provider。
//
// 它是下游的"探活"接口：注册成功后定期调一次，就能在上游把它们断开时
// 立刻发现，而不是等到用户报错。
func (s *Server) handlePeerMe(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requirePeer(w, r)
	if !ok {
		return
	}
	s.touchPeerSeen(account)
	granted := s.resourceGrants()[account.User.ID]
	if granted == nil {
		granted = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"gatewayId":          account.gatewayIDValue(),
		"userId":             account.User.ID,
		"username":           account.User.Username,
		"grantedProviderIds": granted,
		"maxHops":            maxGatewayHops,
	})
}

// peerProviderView 是下游看到的 provider 清单。
//
// 只给下游决策需要的东西：id、协议、有哪些可用模型。上游的 base_url 和凭据
// 一概不下发——下游只需要知道"用哪个模型能被接受"，不需要知道上游怎么连。
type peerProviderView struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Protocol string   `json:"protocol"`
	Models   []string `json:"models"`
}

// handlePeerProviders 列出这个对端当前能用的 provider。
//
// 走的是和其它所有授权判定同一个函数（allowedProviderIDsForPrincipal），所以
// 管理员在用户管理页里改授权、或者禁用这个账号，下游下一次调用就能看到，
// 不需要重启也不会有第二套权限判断。
func (s *Server) handlePeerProviders(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requirePeer(w, r)
	if !ok {
		return
	}
	s.touchPeerSeen(account)
	p := s.principalForUserID(account.User.ID)
	allowed := s.allowedProviderIDsForPrincipal(p)
	state := s.router.State()
	out := make([]peerProviderView, 0, len(allowed))
	for _, provider := range state.Providers {
		if provider.Disabled || provider.Deleted || !allowed[provider.ID] {
			continue
		}
		view := peerProviderView{ID: provider.ID, Name: provider.Name, Protocol: string(provider.Protocol), Models: []string{}}
		for _, model := range state.Models {
			if model.ProviderID == provider.ID && strings.TrimSpace(model.ID) != "" {
				view.Models = append(view.Models, model.ID)
			}
		}
		sort.Strings(view.Models)
		out = append(out, view)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

// peerChatRequest 是下游发来的透传请求。
//
// 请求体基本就是一次普通的模型请求，外加它希望使用的 provider 和这份请求的
// 协议。provider 只是"建议"：上游永远在授权集内选择，指定了集外的 provider
// 会被忽略而不是报错（下游可能记着一份过期的清单）。
type peerChatRequest struct {
	// ProviderID 是下游建议的 provider。空 = 由上游按授权集和模型自己挑。
	ProviderID string `json:"providerId,omitempty"`
	// Protocol 是 payload 的协议（openai_chat / openai_responses / claude）。
	// 上游据此决定是把 payload 原样交给 provider，还是先做一次协议转换——
	// 下游说 openai_chat 而上游只有 Claude provider 可用时转换必须发生，而
	// 转换的前提正是知道源协议。空 = 只接受同协议的 provider。
	Protocol string `json:"protocol,omitempty"`
	// Payload 是原样转发给 provider 的请求体。
	Payload json.RawMessage `json:"payload"`
}

// handlePeerChatCompletions 是父子透传的上游入口。
//
// 数据流：下游把用户的请求原样送上来 → 上游在自己的授权集里选 provider →
// 用上游自己的凭据打真实上游 → 把响应原样送回去。下游从不接触上游的凭据，
// 这既是"不复制 provider"的技术形态，也是用量统计能成立的前提。
func (s *Server) handlePeerChatCompletions(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requirePeer(w, r)
	if !ok {
		return
	}
	s.touchPeerSeen(account)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, peerRequestBodyLimit))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "failed to read request body: "+err.Error())
		return
	}
	var payload peerChatRequest
	if err := json.Unmarshal(body, &payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if len(payload.Payload) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "payload is required")
		return
	}
	var model string
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(payload.Payload, &envelope); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "payload must be a JSON object")
		return
	}
	if raw, ok := envelope["model"]; ok {
		_ = json.Unmarshal(raw, &model)
		model = strings.TrimSpace(model)
	}
	// 不强制要求模型：有人的客户端就是不带 model，把判断留给 provider 的
	// default model。这一条和网关其它入口的口径保持一致。
	s.forwardPeerRequest(w, r, account, strings.TrimSpace(payload.ProviderID), domain.Protocol(strings.TrimSpace(payload.Protocol)), model, payload.Payload)
}

// forwardPeerRequest 在授权集内挑一个 provider 并把请求打过去。
func (s *Server) forwardPeerRequest(w http.ResponseWriter, r *http.Request, account peerAccount, requestedProviderID string, protocol domain.Protocol, model string, payload []byte) {
	p := s.principalForUserID(account.User.ID)
	allowed := s.allowedProviderIDsForPrincipal(p)
	if len(allowed) == 0 {
		// 授权集为空是最常见的配置态（刚注册、管理员还没勾 provider），
		// 要和"凭据无效"区分开，否则下游会去重注册而不是找管理员要授权。
		writeOpenAIError(w, http.StatusForbidden, "no providers are granted to this peer gateway")
		return
	}
	decision, ok := s.selectPeerProvider(allowed, requestedProviderID, protocol, model)
	if !ok {
		// 把三个条件都写进错误里：下游据此能分清"模型不认""协议不匹配"和
		// "授权集里没有这类 provider"，而不用去猜父网关为什么不给用。
		writeOpenAIError(w, http.StatusNotFound, fmt.Sprintf(
			"no granted provider can serve this request (model=%q protocol=%q)", model, string(protocol)))
		return
	}
	// 这一段把下游请求接到和本地请求完全相同的执行路径上：
	// servePeerProtocol 负责凭据、协议头、响应中继和用量统计，于是上游的
	// 日志/用量里天然就有这条记录，不需要另写一套统计。
	//
	// 只标 Ingress，不标 Egress：父网关把请求发给了自己名下的真实 provider，
	// 不是发给另一个对端网关。链路上"再往下一跳"是 P2 的下游侧才有的形态。
	r = withIngressPeer(r, account.gatewayIDValue())
	s.servePeerProtocol(w, r, account, decision, model, payload)
}

// peerRequestBodyLimit 是透传请求体的上限。比普通模型请求宽松一档：透传的是
// 一整轮对话，可能带着很长的上下文。
const peerRequestBodyLimit = 32 << 20 // 32 MiB

// selectPeerProvider 在授权集内选择一个能服务这份请求的 provider。
//
// 优先用下游建议的那个（下游可能知道该走哪条线路），但它必须在授权集内、
// 能服务该模型、且读得懂这份请求；否则按顺序在集内挑，都不行就返回 false
// 让调用方报错——绝不越过授权集去满足一个请求。
//
// protocol 是下游送来的 payload 的协议，用来排除协议对不上的 provider：父网关
// 这里做的是原样透传（见 servePeerProtocol），把一份 openai_chat 的报文交给
// Claude provider，只会换来上游一个 400。所以宁可明确地回一句"授权集里没有
// 这类 provider"，也不要发一次注定失败的请求。protocol 为空（老版本下游不带
// 这个字段）时不按协议过滤，按 payload 原样转发的旧行为不变。
func (s *Server) selectPeerProvider(allowed map[string]bool, requestedProviderID string, protocol domain.Protocol, model string) (domain.RouteDecision, bool) {
	state := s.router.State()
	servable := func(provider domain.Provider) bool {
		if !allowed[provider.ID] || provider.Disabled || provider.Deleted {
			return false
		}
		if protocol != "" && provider.Protocol != protocol {
			return false
		}
		return s.providerServesModel(provider, model)
	}
	if requestedProviderID != "" {
		for _, provider := range state.Providers {
			if provider.ID == requestedProviderID && servable(provider) {
				return peerDecisionFor(provider), true
			}
		}
	}
	// 按授权集里 provider 的原始顺序挑第一个能用的：顺序由管理员在授权时
	// 决定，比随机挑更可预测。
	for _, provider := range state.Providers {
		if servable(provider) {
			return peerDecisionFor(provider), true
		}
	}
	return domain.RouteDecision{}, false
}

// peerDecisionFor 把 provider 包成一个 RouteDecision，复用既有的执行入口。
//
// 对端流量在父网关这一侧永远是原样透传：payload 已经是按下游的协议写好的，
// servePeerProtocol 直接交给 provider 的端点，不做协议转换（转换需要
// 目的协议的转换器与回包整形，而"父网关看到的就是子网关看到的那份报文"正是
// 这条链路最容易解释的性质）。
func peerDecisionFor(provider domain.Provider) domain.RouteDecision {
	return domain.RouteDecision{
		RouteID:        "",
		ProviderID:     provider.ID,
		OutputProtocol: provider.Protocol,
		InputProtocol:  provider.Protocol,
		Mode:           domain.RouteModePassThrough,
		Action:         "peer",
	}
}

// providerServesModel 判断 provider 能不能服务这个模型。
//
// 空模型一律放行：有人的客户端不带 model，靠 provider 的 default model 兜底
// （和网关其它入口同一个口径，见 CLAUDE.md 里那条"不要强制要求配置 default
// model"的约束）。非空时看模型清单与该 provider 的默认模型。
func (s *Server) providerServesModel(provider domain.Provider, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(provider.DefaultModel), model) {
		return true
	}
	for _, candidate := range s.router.State().Models {
		if candidate.ProviderID == provider.ID && strings.EqualFold(candidate.ID, model) {
			return true
		}
	}
	// 模型清单可能还没拉取（新加的 provider），此时不该把请求挡死：
	// 让 provider 自己去判定模型是否存在，它比网关的缓存更权威。
	return !s.providerHasKnownModels(provider.ID)
}

// providerHasKnownModels 报告网关是否已经知道这个 provider 有哪些模型。
//
// 拉取过模型清单的 provider 才能在网关侧判断"这个模型它有没有"；没拉过的
// 时候网关的判断力是零，此时拦下来只会误伤。
func (s *Server) providerHasKnownModels(providerID string) bool {
	for _, candidate := range s.router.State().Models {
		if candidate.ProviderID == providerID {
			return true
		}
	}
	return false
}
