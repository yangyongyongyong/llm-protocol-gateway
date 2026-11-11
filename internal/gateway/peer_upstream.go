package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 下游（本机）这一侧的控制台接口：把「注册到一个上游网关」做成界面上的一次操作。
//
// 注册本身在 peer_client.go，透传在 peer_egress.go，这里只负责把它们接到控制台：
// 校验输入、落一个 authType=peer 的 provider、把上游授权的模型清单抄进它名下。
//
// 为什么是一条 provider 而不是一张新表：那把对端凭据就是它的 apiKeySource，而
// 「一个上游 + 一种协议」正好对应「一条 provider + 一条路由」——沿用现成的模型
// 清单、路由、密钥绑定、日志归属，界面上一行都不用为它新写。

// peerUpstreamRegisterRequest 是注册到一个上游网关的请求体。
type peerUpstreamRegisterRequest struct {
	// ParentURL 是上游网关地址，允许只填主机名（见 normalizePeerParentURL）。
	ParentURL string `json:"parentUrl"`
	// Username / Password 是本机在上游的账号——由本机运维自己定，第一次注册
	// 时上游原样建成账号，之后重新注册必须填同一个。密码不落本机：它只在注册
	// 这一刻用来换凭据，换回来的 token 才是长期保存的东西。
	Username string `json:"username"`
	Password string `json:"password"`
	// Name 是本机给这个上游起的名字，也是新建 provider 的名字。
	Name string `json:"name,omitempty"`
	// Protocol 是这条链路要说的协议。一条 provider 只能说一种，所以想同时用
	// 上游的 OpenAI Chat 和 Claude 上游，就按两个协议各注册一次。
	Protocol string `json:"protocol,omitempty"`
	// RequestedProviderIDs 是希望上游授权的 provider，仅首次注册时作为初始
	// 授权清单——最终审批权永远在上游管理员手里。
	RequestedProviderIDs []string `json:"requestedProviderIds,omitempty"`
}

// peerUpstreamSummary 是界面上一条「已连接的上游」。
//
// 刻意不回传 provider 本体：它的 apiKeySource 就是本机在上游的那把凭据，
// 这个页面没有任何理由把它发到浏览器里。
type peerUpstreamSummary struct {
	ProviderID   string `json:"providerId"`
	Name         string `json:"name"`
	ParentURL    string `json:"parentUrl"`
	Protocol     string `json:"protocol"`
	Enabled      bool   `json:"enabled"`
	HealthStatus string `json:"healthStatus,omitempty"`
	// Models 是上游授权、且与本条协议对得上的模型。
	Models []string `json:"models"`
	// GrantCount 是上游授权给本机的 provider 个数（不限协议）。
	//
	// 它和 Models 是两个口径：一条链路只说一种协议，所以协议对不上的授权
	// provider 不会出现在 Models 里，但它们确实被授权了。界面要能告诉运维
	// 「上游授权了 3 个，其中 1 个是本协议的」，靠的就是这两个数的差。
	GrantCount int `json:"grantCount"`
	// GrantsSynced 表示 GrantCount 是不是真的问到过上游。false 时它是零值，
	// 界面要显示「还没同步」而不是「授权了 0 个」——后者是一个会误导人的
	// 结论。老数据（升级前注册的链路）就是 false，点一次「测试连接」即可。
	GrantsSynced bool `json:"grantsSynced"`
	// GrantsSyncedAt 是这份授权快照的取得时间（RFC3339）；从未同步过时为空。
	GrantsSyncedAt string `json:"grantsSyncedAt,omitempty"`
	// PeerUsername / ParentGatewayID 是上游那一侧的身份：排查时能直接对上
	// 上游用户管理页里的那一行。
	PeerUsername    string `json:"peerUsername,omitempty"`
	ParentGatewayID string `json:"parentGatewayId,omitempty"`
}

// peerProtocolChoices 是这条链路能说的协议，和网关对外的三种端点一致。
func peerProtocolChoices() []domain.Protocol {
	return []domain.Protocol{domain.ProtocolOpenAIChat, domain.ProtocolOpenAIResponses, domain.ProtocolClaude}
}

// normalizePeerProtocol 校验并归一化协议。
//
// 空值取 OpenAI Chat：它是绝大多数上游和客户端说的那种，作为默认不会让人
// 一上手就撞到一个不匹配的协议。
func normalizePeerProtocol(raw string) (domain.Protocol, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return domain.ProtocolOpenAIChat, nil
	}
	for _, protocol := range peerProtocolChoices() {
		if string(protocol) == value {
			return protocol, nil
		}
	}
	return "", fmt.Errorf("不支持的协议 %q，可选 openai_chat / openai_responses / claude", raw)
}

// handleListPeerUpstreams 列出本机注册到的所有上游网关。
func (s *Server) handleListPeerUpstreams(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, domain.PermPeersView) {
		return
	}
	upstreams := s.peerUpstreamSummaries()
	writeJSON(w, http.StatusOK, map[string]any{
		"upstreams": upstreams,
		// 本机身份：注册时用的就是它，界面上把它显示出来，运维才能核对
		// 「上游用户管理页里那一行是不是我」。
		"gatewayId": s.GatewayID(),
		"protocols": peerProtocolChoices(),
	})
}

// peerUpstreamSummaries 把当前所有 peer provider 折成界面要的形状。
func (s *Server) peerUpstreamSummaries() []peerUpstreamSummary {
	state := s.router.State()
	out := make([]peerUpstreamSummary, 0)
	for _, provider := range state.Providers {
		if provider.AuthType != domain.AuthTypePeer || provider.Deleted {
			continue
		}
		summary := peerUpstreamSummary{
			ProviderID:   provider.ID,
			Name:         provider.Name,
			ParentURL:    resolvePeerParentURL(provider),
			Protocol:     string(provider.Protocol),
			Enabled:      !provider.Disabled,
			HealthStatus: provider.HealthStatus,
			Models:       []string{},
		}
		// 授权快照是注册/刷新时存下来的（见 domain.PeerUpstreamGrants）。
		// 没有快照 = 还没问过上游，GrantCount 保持 0 但 GrantsSynced 是 false，
		// 界面据此说「还没同步」而不是「授权了 0 个」。
		if grants := provider.PeerGrants; grants != nil {
			summary.GrantCount = len(grants.GrantedProviderIDs)
			summary.GrantsSynced = true
			summary.GrantsSyncedAt = grants.SyncedAt
			summary.PeerUsername = grants.Username
			summary.ParentGatewayID = grants.ParentGatewayID
		}
		for _, model := range state.Models {
			if model.ProviderID == provider.ID && strings.TrimSpace(model.ID) != "" {
				summary.Models = append(summary.Models, model.ID)
			}
		}
		sort.Strings(summary.Models)
		out = append(out, summary)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// peerUpstreamSummaryOf 是单条的版本，用于注册/刷新之后直接回给界面。
func (s *Server) peerUpstreamSummaryOf(providerID string) peerUpstreamSummary {
	for _, summary := range s.peerUpstreamSummaries() {
		if summary.ProviderID == providerID {
			return summary
		}
	}
	return peerUpstreamSummary{ProviderID: providerID}
}

// handleRegisterPeerUpstream 在本机注册到一个上游网关。
//
// 注册成功后立即落一条 provider：凭据（gwpeer-…）是它的 apiKeySource，上游
// 授权的模型抄进它的模型清单，再顺手建一条同协议的路由——否则刚注册完的用户
// 还得自己去路由页接线，而这条链路只有一种接法。
func (s *Server) handleRegisterPeerUpstream(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, domain.PermPeersManage) {
		return
	}
	selfID := s.GatewayID()
	if strings.TrimSpace(selfID) == "" {
		writeOpenAIError(w, http.StatusServiceUnavailable, "网关身份还没就绪，请重启网关后重试")
		return
	}
	var payload peerUpstreamRegisterRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	protocol, err := normalizePeerProtocol(payload.Protocol)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	username := strings.TrimSpace(payload.Username)
	password := strings.TrimSpace(payload.Password)
	if username == "" || password == "" {
		writeOpenAIError(w, http.StatusBadRequest, "用户名和密码都必须填写（第一次注册时由你自己设定，之后每次填同一个）")
		return
	}
	name := strings.TrimSpace(payload.Name)
	registration, granted, err := s.registerWithParent(r, payload.ParentURL, name, username, password, payload.RequestedProviderIDs)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	if name == "" {
		name = peerUpstreamDefaultName(registration.ParentURL)
	}

	created, err := s.createPeerUpstreamProvider(r, name, protocol, registration, granted)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.ensurePeerUpstreamRoute(created); err != nil {
		// 路由没建起来不算注册失败：provider 已经在，用户能在路由页手动接。
		s.logs.AddApp("warn", "peer upstream route not created", fmt.Sprintf("provider=%s: %v", created.ID, err))
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "peer upstream registered",
		fmt.Sprintf("provider=%s parent=%s user=%s granted=%d", created.ID, registration.ParentURL, registration.Username, len(granted)))
	writeJSON(w, http.StatusCreated, map[string]any{
		"upstream":  s.peerUpstreamSummaryOf(created.ID),
		"username":  registration.Username,
		"userId":    registration.UserID,
		"gatewayId": selfID,
	})
}

// registerWithParent 是注册的公共部分：调上游、拿凭据、再取一次授权清单。
func (s *Server) registerWithParent(r *http.Request, parentURL, name, username, password string, requested []string) (peerRegistration, []peerProviderView, error) {
	root, err := normalizePeerParentURL(parentURL)
	if err != nil {
		return peerRegistration{}, nil, err
	}
	client := newPeerClient()
	registration, err := client.register(r.Context(), root, s.GatewayID(), name, username, password, requested)
	if err != nil {
		return peerRegistration{}, nil, err
	}
	// 再取一次授权清单：注册响应里那份是"此刻的"，但刚注册完马上取一次能让
	// 「授权为空」这种情况在界面上当场可见，而不是等到第一次请求失败。
	granted, err := client.providers(r.Context(), root, registration.Token)
	if err != nil {
		// 清单取不到不该让注册失败：凭据已经到手，模型列表可以稍后刷新。
		s.logs.AddApp("warn", "peer upstream providers unavailable", fmt.Sprintf("parent=%s: %v", root, err))
		granted = nil
	}
	return registration, granted, nil
}

// createPeerUpstreamProvider 把一次成功的注册落成一条 provider。
func (s *Server) createPeerUpstreamProvider(r *http.Request, name string, protocol domain.Protocol, registration peerRegistration, granted []peerProviderView) (domain.Provider, error) {
	models := peerModelsForProtocol(protocol, granted)
	provider := domain.Provider{
		Name:         name,
		Protocol:     protocol,
		BaseURL:      registration.ParentURL,
		APIKeySource: "literal:" + registration.Token,
		AuthType:     domain.AuthTypePeer,
		Models:       models,
		HealthStatus: "healthy",
		// 注册这一刻拿到的授权状态得存下来：列表接口要显示「上游授权了几个
		// provider」，而它是个纯读接口，不能每渲染一次就去打一遍上游。
		PeerGrants: peerGrantsFromRegistration(registration, granted),
	}
	// 默认模型只是给"客户端不带 model"的情形兜一个底，不强制：注册时上游
	// 授权了哪些模型就挑第一个，一个都没有就留空。
	if len(models) > 0 {
		provider.DefaultModel = models[0].ID
	}
	if p := s.requestPrincipal(r); p.UserID != "" {
		provider.OwnerUserID = p.UserID
	}
	return s.router.AddProvider(provider)
}

// peerRegistrationFromProvider 从已存的 provider 还原注册记录里那些不随
// 刷新变化的字段（账号名 / 账号 ID / 上游身份）。刷新只重新要授权清单，
// 不会再拿到一遍账号信息，所以沿用注册时存下的那份。
func peerRegistrationFromProvider(provider domain.Provider) peerRegistration {
	registration := peerRegistration{ParentURL: resolvePeerParentURL(provider), Token: peerUpstreamToken(provider)}
	if grants := provider.PeerGrants; grants != nil {
		registration.Username = grants.Username
		registration.UserID = grants.UserID
		registration.ParentGatewayID = grants.ParentGatewayID
	}
	return registration
}

// peerGrantsFromRegistration 把注册/刷新拿到的东西折成要落库的授权快照。
//
// granted 为 nil 表示"没问到上游"（注册时取清单失败，见 registerWithParent），
// 这时 SyncedAt 留空，界面才能把「上游确实一个都没授权」和「还没问过」分开。
// 空切片则是真实答案：问到了，答案是零个。
func peerGrantsFromRegistration(registration peerRegistration, granted []peerProviderView) *domain.PeerUpstreamGrants {
	if granted == nil {
		return nil
	}
	ids := make([]string, 0, len(granted))
	for _, view := range granted {
		if id := strings.TrimSpace(view.ID); id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return &domain.PeerUpstreamGrants{
		Username:           registration.Username,
		UserID:             registration.UserID,
		ParentGatewayID:    registration.ParentGatewayID,
		GrantedProviderIDs: ids,
		SyncedAt:           time.Now().UTC().Format(time.RFC3339),
	}
}

// peerModelsForProtocol 从上游的授权清单里挑出与本条协议对得上的模型。
//
// 协议对不上的一律不进模型清单：它们发过去也只会被上游拒绝（上游那边的选路
// 也按协议过滤），留在清单里等于给用户一个必然失败的选项。界面上另有提示，
// 告诉运维"想用它们就按那个协议再连一次"。
func peerModelsForProtocol(protocol domain.Protocol, granted []peerProviderView) []domain.Model {
	seen := map[string]bool{}
	out := make([]domain.Model, 0)
	for _, view := range granted {
		if view.Protocol != "" && domain.Protocol(view.Protocol) != protocol {
			continue
		}
		for _, modelID := range view.Models {
			id := strings.TrimSpace(modelID)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			// ProviderID / Protocol / 上下文长度由 router.normalizeProvider 补全，
			// 这里只声明"上游有这个名字的模型"。
			out = append(out, domain.Model{ID: id, InMenu: true})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ensurePeerUpstreamRoute 确保这条 provider 有一条可用的同协议路由。
func (s *Server) ensurePeerUpstreamRoute(provider domain.Provider) error {
	for _, route := range s.router.State().Routes {
		if route.ProviderID == provider.ID && route.OutputProtocol == provider.Protocol {
			return nil
		}
	}
	_, err := s.router.AddRoute(domain.Route{
		Name:           provider.Name + " · " + string(provider.Protocol),
		ProviderID:     provider.ID,
		OutputProtocol: provider.Protocol,
		Mode:           domain.RouteModePassThrough,
	})
	return err
}

// peerUpstreamDefaultName 在用户没起名时从地址里取一个：vps1.example.com。
func peerUpstreamDefaultName(parentURL string) string {
	root := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(parentURL), "https://"), "http://")
	if index := strings.IndexAny(root, "/?#"); index >= 0 {
		root = root[:index]
	}
	if root == "" {
		return "上游网关"
	}
	return root
}

// handleRefreshPeerUpstream 用已存的凭据重新拉一次上游的授权模型清单。
//
// 它是界面上的「测试连接」：能拉到就说明凭据还有效、上游还在，顺带把模型
// 清单更新到最新——上游管理员新授权了一个 provider，这里点一下就能用上，
// 不用重新注册（重新注册会换掉凭据）。
func (s *Server) handleRefreshPeerUpstream(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, domain.PermPeersManage) {
		return
	}
	provider, ok := s.peerUpstreamByID(w, r)
	if !ok {
		return
	}
	token := peerUpstreamToken(provider)
	if token == "" {
		writeOpenAIError(w, http.StatusConflict, "这条上游没有存下凭据，请用用户名密码重新注册一次")
		return
	}
	granted, err := newPeerClient().providers(r.Context(), provider.BaseURL, token)
	if err != nil {
		// 拉不到就把它标成异常：界面上的「测试连接」失败必须留下痕迹，
		// 否则运维只会看到一句转瞬即逝的报错。
		_, _ = s.router.UpdateProviderModels(provider.ID, provider.Models, "failed")
		_ = s.saveState()
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	// 只更模型与健康状态，绝不走 UpdateProvider：那条路径会用 patch 里的
	// 空 apiKeySource 覆盖掉已存的凭据（见 Router.UpdateProvider）。
	if _, err := s.router.UpdateProviderModels(provider.ID, peerModelsForProtocol(provider.Protocol, granted), "healthy"); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 授权快照一起更新：这次确实问到了上游，所以「授权了 0 个」从此是个
	// 可信的结论，而不是还没问过。注册时记下的账号信息沿用旧值，本次响应
	// 里没有它们。
	//
	// granted 仍可能是 nil（上游回了 "providers": null），那说明这次其实也
	// 没问到，快照保持原样——不能拿它当"授权了 0 个"。
	if grants := peerGrantsFromRegistration(peerRegistrationFromProvider(provider), granted); grants != nil {
		if _, err := s.router.SetPeerGrants(provider.ID, *grants); err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "peer upstream refreshed", fmt.Sprintf("provider=%s granted=%d", provider.ID, len(granted)))
	writeJSON(w, http.StatusOK, map[string]any{"upstream": s.peerUpstreamSummaryOf(provider.ID)})
}

// handleReconnectPeerUpstream 用用户名密码把一条已有的上游链路重新注册一次。
//
// 它换发一把新凭据，上游那边旧的立刻失效——这正是「怀疑凭据泄漏」或「上游
// 吊销后要恢复」该做的事，所以它是显式操作，不在刷新路径上顺手做。
func (s *Server) handleReconnectPeerUpstream(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, domain.PermPeersManage) {
		return
	}
	provider, ok := s.peerUpstreamByID(w, r)
	if !ok {
		return
	}
	var payload peerUpstreamRegisterRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	username := strings.TrimSpace(payload.Username)
	password := strings.TrimSpace(payload.Password)
	if username == "" || password == "" {
		writeOpenAIError(w, http.StatusBadRequest, "用户名和密码都必须填写，且与第一次注册时填的完全一样")
		return
	}
	// 协议与名字都沿用这条 provider 现有的：换协议等于换一条链路，该新建一条。
	registration, granted, err := s.registerWithParent(r, provider.BaseURL, provider.Name, username, password, payload.RequestedProviderIDs)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	models := peerModelsForProtocol(provider.Protocol, granted)
	defaultModel := strings.TrimSpace(provider.DefaultModel)
	if defaultModel == "" && len(models) > 0 {
		defaultModel = models[0].ID
	}
	if _, err := s.router.UpdateProvider(provider.ID, domain.Provider{
		APIKeySource: "literal:" + registration.Token,
		BaseURL:      registration.ParentURL,
		DefaultModel: defaultModel,
		Models:       models,
	}); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := s.router.UpdateProviderModels(provider.ID, models, "healthy"); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 重新注册换了一把凭据，也重新问到了授权清单和账号信息：整份快照刷新。
	if grants := peerGrantsFromRegistration(registration, granted); grants != nil {
		if _, err := s.router.SetPeerGrants(provider.ID, *grants); err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "peer upstream reconnected", fmt.Sprintf("provider=%s parent=%s", provider.ID, registration.ParentURL))
	writeJSON(w, http.StatusOK, map[string]any{
		"upstream": s.peerUpstreamSummaryOf(provider.ID),
		"username": registration.Username,
	})
}

// handleProbePeerUpstream 是「连接到上游网关」表单上的「测试连接」。
//
// 它只回答一个问题：这个地址现在能不能注册。因此它不创建账号、不换发凭据、
// 不落任何状态——表单还没提交，用户名密码可能都还没填。注册失败的原因有很多
// 种（账号名被占用、密码长度不够、上游没授权的 provider……），这些都不在这里
// 拦，留给真正的注册去说。
func (s *Server) handleProbePeerUpstream(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, domain.PermPeersManage) {
		return
	}
	var payload struct {
		ParentURL string `json:"parentUrl"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	parentURL := strings.TrimSpace(payload.ParentURL)
	if parentURL == "" {
		writeOpenAIError(w, http.StatusBadRequest, "请填写上游网关地址")
		return
	}
	root, ok := trimPeerParentURL(parentURL)
	if !ok {
		writeOpenAIError(w, http.StatusBadRequest, "上游网关地址不能为空")
		return
	}
	result, err := newPeerClient().probe(r.Context(), root)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, fmt.Sprintf("连不上 %s：%v", root, err))
		return
	}
	message := strings.TrimSpace(result.Message)
	if message == "" {
		message = "上游网关可以访问，注册入口正常"
	}
	s.logs.AddApp("info", "peer upstream probe", fmt.Sprintf("parent=%s allowed=%t", root, result.RegistrationAllowed))
	writeJSON(w, http.StatusOK, map[string]any{
		"parentUrl": root,
		// GatewayID 是上游自己的身份：连对机器比"能连上"更重要，尤其是同一个
		// 域名后面换过机器的时候。
		"gatewayId":              result.GatewayID,
		"registrationAllowed":    result.RegistrationAllowed,
		"registrationAllowCidrs": result.RegistrationAllowCIDRs,
		"message":                message,
	})
}

// peerUpstreamToken 取出存下的对端凭据明文。
func peerUpstreamToken(provider domain.Provider) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(provider.APIKeySource), "literal:"))
}

// fetchPeerUpstreamModels 是「获取模型」按钮在对端 provider 上的形态。
//
// 它问的不是 baseUrl + /models（父网关没有这个端点），而是 /__peer/providers：
// 那正是父网关告诉下游"你被授权了哪些模型"的地方。顺便探活——能拿到清单就
// 说明凭据还有效，所以成功时把健康状态记成 healthy。
func (s *Server) fetchPeerUpstreamModels(r *http.Request, provider domain.Provider, started time.Time) providerTestResult {
	failed := func(message string) providerTestResult {
		return providerTestResult{
			Success: false, Provider: provider.ID, LatencyMs: time.Since(started).Milliseconds(),
			Error: message, Models: []domain.Model{},
		}
	}
	token := peerUpstreamToken(provider)
	if token == "" {
		return failed("这条 provider 没有存下对端凭据，请用用户名密码重新注册一次")
	}
	root := resolvePeerParentURL(provider)
	if root == "" {
		return failed("这条 provider 没有上游网关地址")
	}
	granted, err := newPeerClient().providers(r.Context(), root, token)
	if err != nil {
		return failed(err.Error())
	}
	return providerTestResult{
		Success:   true,
		Provider:  provider.ID,
		ModelsURL: root + peerEndpointPrefix + "providers",
		Status:    http.StatusOK,
		LatencyMs: time.Since(started).Milliseconds(),
		Models:    peerModelsForProtocol(provider.Protocol, granted),
		Preview:   fmt.Sprintf("上游网关授权了 %d 个 provider", len(granted)),
	}
}

// peerUpstreamByID 取出路径上那条 peer provider，失败时已经写好响应。
func (s *Server) peerUpstreamByID(w http.ResponseWriter, r *http.Request) (domain.Provider, bool) {
	provider, err := s.router.ProviderByID(r.PathValue("id"))
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, "上游网关不存在")
		return domain.Provider{}, false
	}
	if provider.AuthType != domain.AuthTypePeer {
		writeOpenAIError(w, http.StatusBadRequest, "这条 provider 不是通过网关互联注册的")
		return domain.Provider{}, false
	}
	return provider, true
}
