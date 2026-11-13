package gateway

import (
	"context"
	"encoding/json"
	"errors"
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
	ProviderID string `json:"providerId"`
	Name       string `json:"name"`
	ParentURL  string `json:"parentUrl"`
	// Protocol / Models 是这一组里所有 provider 行的并集；一条上游授权了多种
	// 协议时这里会有多个值，界面按行显示，不再假设"一条链路一种协议"。
	Protocol string `json:"protocol"`
	// Protocols 是这一组里各成员行的协议（去重、有序），界面按它渲染协议徽标。
	Protocols    []string `json:"protocols"`
	Enabled      bool     `json:"enabled"`
	HealthStatus string   `json:"healthStatus,omitempty"`
	// Models 是上游授权给本机、且已经落成本机 provider 行的模型（并集）。
	Models []string `json:"models"`
	// MemberCount 是这一组里有几条本机 provider 行。
	//
	// 一个上游授权了 N 个 provider，本机就落 N 行：它们和本地 provider 长得
	// 一模一样，也才能被单独选进 API 密钥的备选链。界面上"一行上游"其实是
	// 这一组，删掉某一行不该让整组消失。
	MemberCount int `json:"memberCount"`
	// GrantCount 是上游授权给本机的 provider 个数（不限协议）。
	//
	// 它和 Models 是两个口径：协议对不上的授权 provider 不会出现在 Models 里，
	// 但它们确实被授权了。界面要能告诉运维「上游授权了 3 个，其中 1 个是本协议
	// 的」，靠的就是这两个数的差。
	GrantCount int `json:"grantCount"`
	// AutoRenew 表示本机已保存续签凭据，父网关重启或流量凭据轮换后可以
	// 静默恢复，不需要用户再次填写用户名密码。
	AutoRenew bool `json:"autoRenew,omitempty"`
	// GrantsSynced 表示 GrantCount 是不是真的问到过上游。false 时它是零值，
	// 界面要显示「还没同步」而不是「授权了 0 个」——后者是一个会误导人的
	// 结论。老数据（升级前注册的链路）就是 false，点一次「测试连接」即可。
	GrantsSynced bool `json:"grantsSynced"`
	// GrantsSyncedAt 是这份授权快照的取得时间（RFC3339）；从未同步过时为空。
	GrantsSyncedAt string `json:"grantsSyncedAt,omitempty"`
	// GrantedProviders 是上游授权的 provider 清单（含名字、协议、模型）。
	// 界面用它把上游那一侧的样子原样列出来，不再只报一个数字。
	GrantedProviders []domain.PeerGrantedProvider `json:"grantedProviders,omitempty"`
	// PeerUsername / ParentGatewayID 是上游那一侧的身份：排查时能直接对上
	// 上游用户管理页里的那一行。
	PeerUsername    string `json:"peerUsername,omitempty"`
	ParentGatewayID string `json:"parentGatewayId,omitempty"`
	// UpstreamKey / UpstreamName 是这一组的分组标识与显示名：本机落下来的每个
	// 成员行都带着同一份，界面据此把"属于某个上游"说清楚。
	UpstreamKey  string `json:"upstreamKey,omitempty"`
	UpstreamName string `json:"upstreamName,omitempty"`
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
//
// 一个上游授权了 N 个 provider，本机就有 N 条成员行；它们共享同一个 UpstreamKey，
// 在这里合成一行「已连接的上游」——界面上「一条上游」指的就是这一组，而不是
// 某一条成员行。升级前注册的老记录没有 UpstreamKey，各自成组（一行一条链路），
// 显示和以前完全一样。
func (s *Server) peerUpstreamSummaries() []peerUpstreamSummary {
	state := s.router.State()
	modelsByProvider := map[string][]string{}
	for _, model := range state.Models {
		if id := strings.TrimSpace(model.ID); id != "" {
			modelsByProvider[model.ProviderID] = append(modelsByProvider[model.ProviderID], id)
		}
	}
	order := make([]string, 0)
	groups := map[string]*peerUpstreamSummary{}
	for _, provider := range state.Providers {
		if provider.AuthType != domain.AuthTypePeer || provider.Deleted {
			continue
		}
		key := peerGroupKeyForProvider(provider)
		summary, ok := groups[key]
		if !ok {
			summary = &peerUpstreamSummary{
				ProviderID:  provider.ID,
				Name:        provider.Name,
				ParentURL:   resolvePeerParentURL(provider),
				Protocol:    string(provider.Protocol),
				Protocols:   []string{},
				Enabled:     true,
				Models:      []string{},
				UpstreamKey: key,
			}
			groups[key] = summary
			order = append(order, key)
		}
		summary.MemberCount++
		if provider.Disabled {
			summary.Enabled = false
		}
		// 组内的健康状态取"最差"的：有一条成员行异常，整个上游就该显示异常，
		// 否则运维看到绿色却有一部分请求在失败。
		if summary.HealthStatus == "" || peerHealthRank(provider.HealthStatus) > peerHealthRank(summary.HealthStatus) {
			summary.HealthStatus = provider.HealthStatus
		}
		summary.Protocols = appendUnique(summary.Protocols, string(provider.Protocol))
		summary.Models = append(summary.Models, modelsByProvider[provider.ID]...)
		if strings.TrimSpace(provider.PeerRefreshToken) != "" {
			summary.AutoRenew = true
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
			if grants.UpstreamName != "" {
				summary.UpstreamName = grants.UpstreamName
			}
			if grants.GrantedProviders != nil {
				summary.GrantedProviders = grants.GrantedProviders
			}
		}
	}
	out := make([]peerUpstreamSummary, 0, len(order))
	for _, key := range order {
		summary := groups[key]
		sort.Strings(summary.Models)
		summary.Models = dedupeStrings(summary.Models)
		sort.Strings(summary.Protocols)
		// 一条成员行的组沿用它的协议；多条时取第一个（协议徽标另有 Protocols）。
		if len(summary.Protocols) > 0 {
			summary.Protocol = summary.Protocols[0]
		}
		if summary.UpstreamName == "" {
			summary.UpstreamName = summary.Name
		}
		out = append(out, *summary)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpstreamName != out[j].UpstreamName {
			return out[i].UpstreamName < out[j].UpstreamName
		}
		return out[i].ProviderID < out[j].ProviderID
	})
	return out
}

// peerGroupKeyForProvider 是这条成员行所属上游的分组标识。
//
// 新注册的记录都带着 UpstreamKey；升级前的老记录没有，就用它自己的 provider id
// 当 key——每条老记录各自成组，正是它原来的样子。
func peerGroupKeyForProvider(provider domain.Provider) string {
	if grants := provider.PeerGrants; grants != nil && strings.TrimSpace(grants.UpstreamKey) != "" {
		return strings.TrimSpace(grants.UpstreamKey)
	}
	return "provider:" + provider.ID
}

// peerHealthRank 让"最差者胜"的合并能比较健康状态：数值越大越该被看见。
func peerHealthRank(status string) int {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "unavailable":
		return 3
	case "failed":
		return 2
	case "healthy":
		return 1
	default:
		return 0
	}
}

func appendUnique(list []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return list
	}
	for _, item := range list {
		if item == value {
			return list
		}
	}
	return append(list, value)
}

func dedupeStrings(list []string) []string {
	if len(list) < 2 {
		return list
	}
	out := list[:1]
	for _, item := range list[1:] {
		if out[len(out)-1] != item {
			out = append(out, item)
		}
	}
	return out
}

// peerUpstreamSummaryOf 是单条的版本，用于注册/刷新之后直接回给界面。
// 传任意一条成员行的 id 即可，返回它所属的那一组。
func (s *Server) peerUpstreamSummaryOf(providerID string) peerUpstreamSummary {
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		return peerUpstreamSummary{ProviderID: providerID}
	}
	key := peerGroupKeyForProvider(provider)
	for _, summary := range s.peerUpstreamSummaries() {
		if summary.UpstreamKey == key {
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

	created, err := s.createPeerUpstreamProviders(r, name, protocol, registration, granted)
	if err != nil {
		// 已经有成员行落下来了：注册本身成功，凭据可用，不该整批回滚。
		// 只有一行都没落成时才算注册失败。
		if len(created) == 0 {
			writeOpenAIError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.logs.AddApp("warn", "peer upstream member incomplete", fmt.Sprintf("parent=%s: %v", registration.ParentURL, err))
	}
	if created == nil {
		created = []domain.Provider{}
	}
	for _, provider := range created {
		if routeErr := s.ensurePeerUpstreamRoute(provider); routeErr != nil {
			// 路由没建起来不算注册失败：provider 已经在，用户能在路由页手动接。
			s.logs.AddApp("warn", "peer upstream route not created", fmt.Sprintf("provider=%s: %v", provider.ID, routeErr))
		}
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "peer upstream registered",
		fmt.Sprintf("parent=%s user=%s providers=%d granted=%d", registration.ParentURL, registration.Username, len(created), len(granted)))
	response := map[string]any{
		"username":  registration.Username,
		"userId":    registration.UserID,
		"gatewayId": selfID,
		"providers": created,
	}
	if len(created) > 0 {
		response["upstream"] = s.peerUpstreamSummaryOf(created[0].ID)
	}
	writeJSON(w, http.StatusCreated, response)
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

// createPeerUpstreamProvider 把一次成功的注册落成 provider 行。
//
// 一条上游可能授权了 N 个 provider。这里为其中每一个都落一条本机 provider：
// 名字取上游的名字、协议取上游的协议、模型取上游的模型，于是它们在「输入 Provider」
// 里和本地 provider 长得一模一样，也才能被单独选进 API 密钥的备选链（备选是
// provider id 的列表，上游那一侧靠信封里的 providerId 精确路由）。
//
// 一个都没授权时退化成一条占位行：链路本身仍然存在（运维要能看到"连上了但没
// 授权"），只是没有模型可发。
func (s *Server) createPeerUpstreamProviders(r *http.Request, name string, protocol domain.Protocol, registration peerRegistration, granted []peerProviderView) ([]domain.Provider, error) {
	ownerID := ""
	if p := s.requestPrincipal(r); p.UserID != "" {
		ownerID = p.UserID
	}
	grants := peerGrantsFromRegistration(registration, granted)
	upstreamKey := peerUpstreamKey(grants, registration.ParentURL)
	plans := peerProviderPlans(name, protocol, granted)
	created := make([]domain.Provider, 0, len(plans))
	for _, plan := range plans {
		provider := domain.Provider{
			Name:             plan.Name,
			Protocol:         plan.Protocol,
			ProtocolSupport:  plan.ProtocolSupport,
			BaseURL:          registration.ParentURL,
			APIKeySource:     "literal:" + registration.Token,
			PeerRefreshToken: registration.RefreshToken,
			AuthType:         domain.AuthTypePeer,
			Models:           plan.Models,
			HealthStatus:     "healthy",
			OwnerUserID:      ownerID,
		}
		// 每一条都带上同一份授权快照（同一把凭据、同一个上游），只改"我对应
		// 上游的哪一个"。列表接口靠这份快照显示「上游授权了几个」，透传靠
		// GrantedProviderID 精确路由。
		if grants != nil {
			snapshot := grants.Clone()
			snapshot.UpstreamKey = upstreamKey
			snapshot.UpstreamName = name
			snapshot.GrantedProviderID = plan.GrantedProviderID
			if plan.Card != nil {
				card := plan.Card.Clone()
				snapshot.Card = &card
			}
			provider.PeerGrants = &snapshot
		}
		// 默认模型只是给"客户端不带 model"的情形兜一个底，不强制：这条 provider
		// 有哪些模型就挑第一个，一个都没有就留空。
		if len(plan.Models) > 0 {
			provider.DefaultModel = plan.Models[0].ID
		}
		saved, err := s.router.AddProvider(provider)
		if err != nil {
			// 已经落的行留着：注册本身成功了，凭据可用，多出来的行删掉反而
			// 让运维难以理解发生了什么。错误往上带，由调用方决定怎么报。
			return created, err
		}
		created = append(created, saved)
	}
	return created, nil
}

// peerProviderPlan 是"上游的一个授权 provider"在本机要落成的 provider 行的样子。
type peerProviderPlan struct {
	Name     string
	Protocol domain.Protocol
	// ProtocolSupport 是这条上游 Provider 实测支持的全部协议，注册/刷新时
	// 原样抄到下游的 peer provider 上，供调度判断能否同协议直通。
	ProtocolSupport   []string
	Models            []domain.Model
	GrantedProviderID string
	Card              *domain.PeerProviderCard
}

// peerProviderPlans 把上游的授权清单摊成要落的 provider 行。
//
// 上游没告诉我们协议时（老版本上游）只能按注册时选的协议落一行——这正是
// 升级前的行为，保持不变：没有协议就没法判断该建几条、各自说哪种协议。
//
// 一个 provider 都没授权时仍然落一行占位：界面要能显示"这条上游连上了、
// 但目前没有任何授权"，否则整个上游会从列表里消失，看起来像注册失败。
func peerProviderPlans(name string, protocol domain.Protocol, granted []peerProviderView) []peerProviderPlan {
	plans := make([]peerProviderPlan, 0, len(granted))
	seen := map[string]bool{}
	for _, view := range granted {
		id := strings.TrimSpace(view.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		memberProtocol := protocol
		if declared := strings.TrimSpace(view.Protocol); declared != "" {
			memberProtocol = domain.Protocol(declared)
		}
		models := peerModelsForProtocol(memberProtocol, []peerProviderView{view})
		label := strings.TrimSpace(view.Name)
		if label == "" {
			label = id
		}
		plans = append(plans, peerProviderPlan{
			// 名字带上上游的名字：子网关里可能连着好几个上游，都叫 "gpt-4o" 的
			// 两行看起来像重复；「vps1 · 名字」一眼就知道它属于哪条链路。
			Name:              peerMemberProviderName(name, label),
			Protocol:          memberProtocol,
			ProtocolSupport:   peerProtocolSupport(view.ProtocolSupport, memberProtocol),
			Models:            models,
			GrantedProviderID: id,
			Card:              view.Card,
		})
	}
	if len(plans) > 0 {
		return plans
	}
	// 没授权（或上游没给清单）：沿用注册时选的协议落一条占位行。
	return []peerProviderPlan{{
		Name:            name,
		Protocol:        protocol,
		ProtocolSupport: peerProtocolSupport(nil, protocol),
		Models:          peerModelsForProtocol(protocol, granted),
	}}
}

// peerProtocolSupport 归一化上游的多协议清单：去空、去重，并保证声明协议
// 一定在清单里（老版本上游不带这个字段时，至少还能按声明协议直通）。
func peerProtocolSupport(raw []string, declared domain.Protocol) []string {
	seen := make(map[string]bool, len(raw)+1)
	out := make([]string, 0, len(raw)+1)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		out = append(out, value)
	}
	add(string(declared))
	for _, value := range raw {
		add(value)
	}
	return out
}

// peerMemberProviderName 是一条成员 provider 在子网关里的名字。
//
// 上游名字已经带着上游名时（运维给上游的 provider 起名 "vps1-gpt"）不再重复
// 前缀，否则会出现「vps1 · vps1-gpt」这种自己叠自己的名字。
func peerMemberProviderName(upstreamName, providerName string) string {
	upstreamName = strings.TrimSpace(upstreamName)
	providerName = strings.TrimSpace(providerName)
	if upstreamName == "" {
		return providerName
	}
	if providerName == "" || strings.EqualFold(upstreamName, providerName) {
		return upstreamName
	}
	if strings.HasPrefix(strings.ToLower(providerName), strings.ToLower(upstreamName)) {
		return providerName
	}
	return upstreamName + " · " + providerName
}

// peerUpstreamKey 是同一批成员行共享的分组标识。
//
// 注册时生成一次就固定下来（落进每条成员行的快照里），之后改名、刷新、重连都
// 沿用旧值——分组跟着改名裂开会让界面上的"一个上游"变成两个。
func peerUpstreamKey(grants *domain.PeerUpstreamGrants, parentURL string) string {
	base := strings.TrimSpace(parentURL)
	if grants != nil && strings.TrimSpace(grants.ParentGatewayID) != "" {
		base = strings.TrimSpace(grants.ParentGatewayID) + "|" + base
	}
	if base == "" {
		base = "peer-upstream"
	}
	return base
}

// peerRegistrationFromProvider 从已存的 provider 还原注册记录里那些不随
// 刷新变化的字段（账号名 / 账号 ID / 上游身份）。刷新只重新要授权清单，
// 不会再拿到一遍账号信息，所以沿用注册时存下的那份。
func peerRegistrationFromProvider(provider domain.Provider) peerRegistration {
	registration := peerRegistration{
		ParentURL:    resolvePeerParentURL(provider),
		Token:        peerUpstreamToken(provider),
		RefreshToken: provider.PeerRefreshToken,
	}
	if grants := provider.PeerGrants; grants != nil {
		registration.Username = grants.Username
		registration.UserID = grants.UserID
		registration.ParentGatewayID = grants.ParentGatewayID
	}
	return registration
}

// peerGrantedProviderID 是这条 provider 对应上游的哪个 provider（空 = 整条链路）。
//
// 透传时把它放进信封，上游才会把请求路由到这一个 provider，而不是在授权集里
// 自己挑一个——「备选里单独选了上游的某一个 provider」要成立就靠它。
func peerGrantedProviderID(provider domain.Provider) string {
	if grants := provider.PeerGrants; grants != nil {
		return strings.TrimSpace(grants.GrantedProviderID)
	}
	return ""
}

// peerUpstreamMembers 返回同一条上游落下来的所有成员行（不含已删除的）。
func (s *Server) peerUpstreamMembers(upstreamKey string) []domain.Provider {
	if strings.TrimSpace(upstreamKey) == "" {
		return nil
	}
	members := make([]domain.Provider, 0)
	for _, provider := range s.router.State().Providers {
		if provider.AuthType != domain.AuthTypePeer || provider.Deleted {
			continue
		}
		if peerGroupKeyForProvider(provider) == upstreamKey {
			members = append(members, provider)
		}
	}
	return members
}

// reconcilePeerUpstreamMembers 把一组成员行同步到上游最新的授权清单。
//
// 三件事：授权里新出现的 provider 落成新行；已有的按最新名字/模型就地更新；
// 上游已经收回的软删除（绑过它的密钥引用会被 Router.DeleteProvider 一并清掉，
// 这正是"这个 provider 不存在了"该有的效果）。
//
// 升级前注册的老记录没有"对应上游哪个 provider"这个身份（GrantedProviderID 为空），
// 它自己一行就是整条链路。这里让它就地变成第一个成员行——保住它的 id，也就保住
// 了已经绑在它上面的路由和密钥。优先挑与它当前协议一致的那个，否则换协议会让
// 既有路由的协议流对不上。
//
// 凭据写在每一条成员行上，所以更新时只碰地址、名字、模型和快照，绝不把
// APIKeySource 置空——UpdateProvider 会用 patch 里的空值覆盖旧值。
func (s *Server) reconcilePeerUpstreamMembers(anchor domain.Provider, registration peerRegistration, granted []peerProviderView, upstreamName string) ([]domain.Provider, error) {
	upstreamKey := peerGroupKeyForProvider(anchor)
	grants := peerGrantsFromRegistration(registration, granted)
	if grants == nil {
		return nil, nil
	}
	grants.UpstreamKey = upstreamKey
	grants.UpstreamName = upstreamName
	plans := peerProviderPlans(upstreamName, anchor.Protocol, granted)

	members := s.peerUpstreamMembers(upstreamKey)
	byGrantedID := make(map[string]domain.Provider, len(members))
	var legacy *domain.Provider
	for index := range members {
		if id := peerGrantedProviderID(members[index]); id != "" {
			byGrantedID[id] = members[index]
		} else if legacy == nil {
			legacy = &members[index]
		}
	}
	consumeLegacy := func(protocol domain.Protocol) (domain.Provider, bool) {
		if legacy == nil {
			return domain.Provider{}, false
		}
		if protocol != "" && legacy.Protocol != protocol {
			return domain.Provider{}, false
		}
		member := *legacy
		legacy = nil
		return member, true
	}

	updated := make([]domain.Provider, 0, len(plans))
	// kept 记"这一轮保住了哪些成员行"，两把键：本机 provider id，和它对应的上游
	// provider id。上游 id 为空（升级前的老记录、占位行）时只记本机 id——把空串
	// 当键会让所有没有上游身份的成员行一起被当成"已保住"，收回的授权就删不掉。
	kept := make(map[string]bool, len(plans)*2)
	markKept := func(providerID, grantedID string) {
		kept[providerID] = true
		if grantedID != "" {
			kept[grantedID] = true
		}
	}
	isKept := func(member domain.Provider) bool {
		if kept[member.ID] {
			return true
		}
		id := peerGrantedProviderID(member)
		return id != "" && kept[id]
	}
	// 上游一个都没授权时 plans 只有一条占位行。这时优先让锚点自己就地退化成
	// 「连上了但没授权」，而不是把整组成员删掉再新建一行：删掉会连带清空已经
	// 绑在它们上面的路由与密钥备选，而上游重新授权时也拿不回原来的 id。
	placeholderSurvivor := domain.Provider{}
	if len(plans) == 1 && plans[0].GrantedProviderID == "" {
		for _, member := range members {
			if member.ID == anchor.ID {
				placeholderSurvivor = member
				break
			}
		}
	}
	for _, plan := range plans {
		member, ok := byGrantedID[plan.GrantedProviderID]
		if !ok {
			// 老记录优先接住与自己同协议的那个 plan：换协议会让既有路由错位。
			member, ok = consumeLegacy(plan.Protocol)
		}
		if !ok && placeholderSurvivor.ID != "" {
			member, ok = placeholderSurvivor, true
			placeholderSurvivor = domain.Provider{}
		}
		if ok && !isKept(member) {
			markKept(member.ID, peerGrantedProviderID(member))
			token := peerUpstreamToken(member)
			if token == "" {
				token = registration.Token
			}
			defaultModel := strings.TrimSpace(member.DefaultModel)
			if defaultModel == "" && len(plan.Models) > 0 {
				defaultModel = plan.Models[0].ID
			}
			if _, err := s.router.UpdateProvider(member.ID, domain.Provider{
				Name:             plan.Name,
				Protocol:         plan.Protocol,
				BaseURL:          registration.ParentURL,
				APIKeySource:     "literal:" + token,
				PeerRefreshToken: registration.RefreshToken,
				DefaultModel:     defaultModel,
			}); err != nil {
				return updated, err
			}
			saved, err := s.router.UpdateProviderModels(member.ID, plan.Models, "healthy")
			if err != nil {
				return updated, err
			}
			saved, err = s.router.SetProviderProtocolSupport(member.ID, plan.ProtocolSupport)
			if err != nil {
				return updated, err
			}
			snapshot := grants.Clone()
			snapshot.GrantedProviderID = plan.GrantedProviderID
			if plan.Card != nil {
				card := plan.Card.Clone()
				snapshot.Card = &card
			}
			if _, err := s.router.SetPeerGrants(member.ID, snapshot); err != nil {
				return updated, err
			}
			updated = append(updated, saved)
			continue
		}
		// 新授权：落一条新的成员行。
		provider := domain.Provider{
			Name:             plan.Name,
			Protocol:         plan.Protocol,
			ProtocolSupport:  plan.ProtocolSupport,
			BaseURL:          registration.ParentURL,
			APIKeySource:     "literal:" + registration.Token,
			PeerRefreshToken: registration.RefreshToken,
			AuthType:         domain.AuthTypePeer,
			Models:           plan.Models,
			HealthStatus:     "healthy",
			OwnerUserID:      anchor.OwnerUserID,
		}
		if len(plan.Models) > 0 {
			provider.DefaultModel = plan.Models[0].ID
		}
		snapshot := grants.Clone()
		snapshot.GrantedProviderID = plan.GrantedProviderID
		if plan.Card != nil {
			card := plan.Card.Clone()
			snapshot.Card = &card
		}
		provider.PeerGrants = &snapshot
		saved, err := s.router.AddProvider(provider)
		if err != nil {
			return updated, err
		}
		markKept(saved.ID, plan.GrantedProviderID)
		updated = append(updated, saved)
	}

	// 上游已经收回的：软删除。留着不删会让用户在备选里选中一个上游明确
	// 拒绝了的 provider，而报错会指向"上游拒绝"，很难追到是授权没了。
	for _, member := range members {
		if isKept(member) {
			continue
		}
		if len(updated) == 0 && member.ID == anchor.ID {
			// 一条都没谈成时不要把锚点自己删掉：链路还在，只是暂时没授权。
			continue
		}
		if err := s.router.DeleteProvider(member.ID); err != nil {
			s.logs.AddApp("warn", "peer upstream member not revoked", fmt.Sprintf("provider=%s: %v", member.ID, err))
		}
	}
	return updated, nil
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
	details := make([]domain.PeerGrantedProvider, 0, len(granted))
	for _, view := range granted {
		id := strings.TrimSpace(view.ID)
		if id == "" {
			continue
		}
		ids = append(ids, id)
		models := make([]string, 0, len(view.Models))
		for _, modelID := range view.Models {
			if trimmed := strings.TrimSpace(modelID); trimmed != "" {
				models = append(models, trimmed)
			}
		}
		sort.Strings(models)
		details = append(details, domain.PeerGrantedProvider{
			ID:       id,
			Name:     strings.TrimSpace(view.Name),
			Protocol: strings.TrimSpace(view.Protocol),
			Models:   models,
			Card:     view.Card,
		})
	}
	// 两份清单按 id 排序：上游响应顺序会变，快照不该跟着抖。
	sort.Strings(ids)
	sort.Slice(details, func(i, j int) bool { return details[i].ID < details[j].ID })
	return &domain.PeerUpstreamGrants{
		Username:           registration.Username,
		UserID:             registration.UserID,
		ParentGatewayID:    registration.ParentGatewayID,
		GrantedProviderIDs: ids,
		GrantedProviders:   details,
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

// ensurePeerUpstreamRoute 确保这条 provider 有一条自动创建的路由。
//
// 路由只保存 provider 绑定，不保存“直通/转换”模式。每次请求由实际客户端
// 协议和 provider 声明协议决定：相同就直通，不同就转换；如果实测
// ProtocolSupport 说明当前 provider 也支持客户端协议，则先在当前 provider
// 内尝试同协议直通，失败后再走转换。Provider 的先后顺序完全由 Key 的调度
// 规则决定，这里不参与协议偏好排序。
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
	summary, status, err := s.refreshPeerUpstream(r.Context(), provider)
	if err != nil {
		writeOpenAIError(w, status, err.Error())
		return
	}
	s.logs.AddApp("info", "peer upstream refreshed", fmt.Sprintf("provider=%s granted=%d", provider.ID, summary.GrantCount))
	writeJSON(w, http.StatusOK, map[string]any{"upstream": summary})
}

// refreshPeerUpstream 是手动刷新与后台同步共用的一条路径：向上游重新取
// 授权 Provider 清单，更新模型、成员行、路由与健康状态。返回的 status 只
// 给控制台接口用，后台轮询忽略它。
func (s *Server) refreshPeerUpstream(ctx context.Context, provider domain.Provider) (peerUpstreamSummary, int, error) {
	s.peerUpstreamSyncMu.Lock()
	defer s.peerUpstreamSyncMu.Unlock()

	token := peerUpstreamToken(provider)
	if token == "" {
		return peerUpstreamSummary{}, http.StatusConflict, fmt.Errorf("这条上游没有存下凭据，请用用户名密码重新注册一次")
	}
	root := resolvePeerParentURL(provider)
	if root == "" {
		return peerUpstreamSummary{}, http.StatusBadRequest, fmt.Errorf("这条上游没有网关地址")
	}
	// 老记录只有流量 bearer。父端新版支持时，先用当前 bearer 一次性换出
	// 续签凭据；之后重启、轮换都走自动恢复，不再需要用户重新注册。
	if strings.TrimSpace(provider.PeerRefreshToken) == "" {
		if upgraded, upgradeErr := s.bootstrapPeerUpstreamRefresh(ctx, provider); upgradeErr == nil {
			provider = upgraded
			token = peerUpstreamToken(provider)
		} else {
			var httpErr *peerHTTPError
			if !errors.As(upgradeErr, &httpErr) || httpErr.Status != http.StatusNotFound {
				s.logs.AddApp("debug", "peer upstream refresh credential not issued", fmt.Sprintf("provider=%s: %v", provider.ID, upgradeErr))
			}
		}
	}
	granted, err := newPeerClient().providers(ctx, root, token)
	if err != nil {
		var httpErr *peerHTTPError
		if errors.As(err, &httpErr) && httpErr.Status == http.StatusUnauthorized && strings.TrimSpace(provider.PeerRefreshToken) != "" {
			renewed, renewErr := s.renewPeerUpstreamCredential(ctx, provider)
			if renewErr == nil {
				provider = renewed
				granted, err = newPeerClient().providers(ctx, root, peerUpstreamToken(provider))
			} else {
				s.logs.AddApp("warn", "peer upstream auto renew failed", fmt.Sprintf("provider=%s: %v", provider.ID, renewErr))
			}
		}
	}
	if err != nil {
		// 拉不到就把它标成异常：界面上的「测试连接」失败必须留下痕迹，
		// 否则运维只会看到一句转瞬即逝的报错。
		_, _ = s.router.UpdateProviderModels(provider.ID, provider.Models, "failed")
		_ = s.saveState()
		return peerUpstreamSummary{}, http.StatusBadGateway, err
	}
	// 只更模型与健康状态，绝不走 UpdateProvider：那条路径会用 patch 里的
	// 空 apiKeySource 覆盖掉已存的凭据（见 Router.UpdateProvider）。
	if _, err := s.router.UpdateProviderModels(provider.ID, peerModelsForProtocol(provider.Protocol, granted), "healthy"); err != nil {
		return peerUpstreamSummary{}, http.StatusInternalServerError, err
	}
	// 授权快照与成员行一起同步：这次确实问到了上游，所以「授权了 0 个」从此
	// 是个可信的结论，而不是还没问过。注册时记下的账号信息沿用旧值，本次响应
	// 里没有它们。
	//
	// granted 仍可能是 nil（上游回了 "providers": null），那说明这次其实也
	// 没问到，快照保持原样——不能拿它当"授权了 0 个"，成员行也不动。
	if granted != nil {
		upstreamName := provider.Name
		if provider.PeerGrants != nil && strings.TrimSpace(provider.PeerGrants.UpstreamName) != "" {
			upstreamName = provider.PeerGrants.UpstreamName
		}
		members, memberErr := s.reconcilePeerUpstreamMembers(provider, peerRegistrationFromProvider(provider), granted, upstreamName)
		if memberErr != nil {
			return peerUpstreamSummary{}, http.StatusInternalServerError, memberErr
		}
		for _, member := range members {
			if routeErr := s.ensurePeerUpstreamRoute(member); routeErr != nil {
				s.logs.AddApp("warn", "peer upstream route not created", fmt.Sprintf("provider=%s: %v", member.ID, routeErr))
			}
		}
	}
	if err := s.saveState(); err != nil {
		return peerUpstreamSummary{}, http.StatusInternalServerError, fmt.Errorf("failed to save configuration: %w", err)
	}
	return s.peerUpstreamSummaryOf(provider.ID), http.StatusOK, nil
}

// renewPeerUpstreamCredential 用隐藏的续签凭据换一组新凭据，并更新同一上游
// 下的全部成员行。并发 401 只会触发一次真实轮换：后到的调用读取到 token 已变，
// 直接复用新值。
func (s *Server) renewPeerUpstreamCredential(ctx context.Context, provider domain.Provider) (domain.Provider, error) {
	s.peerCredentialRenewMu.Lock()
	defer s.peerCredentialRenewMu.Unlock()

	latest, err := s.router.ProviderByID(provider.ID)
	if err != nil {
		return domain.Provider{}, err
	}
	if oldToken, currentToken := peerUpstreamToken(provider), peerUpstreamToken(latest); currentToken != "" && currentToken != oldToken {
		return latest, nil
	}
	refreshToken := strings.TrimSpace(latest.PeerRefreshToken)
	if refreshToken == "" {
		return domain.Provider{}, fmt.Errorf("这条上游没有续签凭据，请重新注册一次以启用自动续签")
	}
	root := resolvePeerParentURL(latest)
	if root == "" {
		return domain.Provider{}, fmt.Errorf("这条上游没有网关地址")
	}
	renewed, err := newPeerClient().renew(ctx, root, s.GatewayID(), refreshToken)
	if err != nil {
		return domain.Provider{}, err
	}
	if strings.TrimSpace(renewed.Token) == "" || strings.TrimSpace(renewed.RefreshToken) == "" {
		return domain.Provider{}, fmt.Errorf("父网关续签响应缺少凭据")
	}
	anchor, err := s.updatePeerUpstreamCredentialGroup(latest, renewed)
	if err != nil {
		return domain.Provider{}, err
	}
	s.logs.AddApp("info", "peer upstream credentials renewed", fmt.Sprintf("provider=%s", latest.ID))
	return anchor, nil
}

// bootstrapPeerUpstreamRefresh 为旧版单凭据记录补发续签凭据。它只在当前
// bearer 仍然有效时成功；已经具备续签凭据的记录直接复用，不发网络请求。
func (s *Server) bootstrapPeerUpstreamRefresh(ctx context.Context, provider domain.Provider) (domain.Provider, error) {
	s.peerCredentialRenewMu.Lock()
	defer s.peerCredentialRenewMu.Unlock()

	latest, err := s.router.ProviderByID(provider.ID)
	if err != nil {
		return domain.Provider{}, err
	}
	if strings.TrimSpace(latest.PeerRefreshToken) != "" {
		return latest, nil
	}
	token := peerUpstreamToken(latest)
	if token == "" {
		return domain.Provider{}, fmt.Errorf("这条上游没有流量凭据")
	}
	root := resolvePeerParentURL(latest)
	if root == "" {
		return domain.Provider{}, fmt.Errorf("这条上游没有网关地址")
	}
	upgraded, err := newPeerClient().issueRefreshToken(ctx, root, s.GatewayID(), token)
	if err != nil {
		return domain.Provider{}, err
	}
	if strings.TrimSpace(upgraded.Token) == "" || strings.TrimSpace(upgraded.RefreshToken) == "" {
		return domain.Provider{}, fmt.Errorf("父网关升级响应缺少凭据")
	}
	anchor, err := s.updatePeerUpstreamCredentialGroup(latest, upgraded)
	if err != nil {
		return domain.Provider{}, err
	}
	s.logs.AddApp("info", "peer upstream refresh credential enabled", fmt.Sprintf("provider=%s", latest.ID))
	return anchor, nil
}

// updatePeerUpstreamCredentialGroup 把父网关返回的新 bearer/refresh 同时写进
// 同一 upstreamKey 下的全部成员行，并持久化。调用方负责持有续签锁。
func (s *Server) updatePeerUpstreamCredentialGroup(latest domain.Provider, renewed peerRegistration) (domain.Provider, error) {

	members := s.peerUpstreamMembers(peerGroupKeyForProvider(latest))
	if len(members) == 0 {
		members = []domain.Provider{latest}
	}
	var anchor domain.Provider
	for _, member := range members {
		updated, updateErr := s.router.SetPeerUpstreamCredentials(member.ID, renewed.Token, renewed.RefreshToken)
		if updateErr != nil {
			return domain.Provider{}, updateErr
		}
		if member.ID == latest.ID {
			anchor = updated
		}
	}
	if anchor.ID == "" {
		restored, err := s.router.ProviderByID(latest.ID)
		if err != nil {
			return domain.Provider{}, err
		}
		anchor = restored
	}
	if err := s.saveState(); err != nil {
		return domain.Provider{}, err
	}
	return anchor, nil
}

const peerUpstreamRefreshInterval = 5 * time.Minute

// StartPeerUpstreamBackgroundRefresh 周期性同步所有上游网关的 Provider 清单。
// 父网关开启「全部 Provider · 动态」后，下游不需要用户手动点刷新，也能在
// 下一轮同步中看到新增的 Provider；普通中英文模型只会多一次轻量清单查询。
func (s *Server) StartPeerUpstreamBackgroundRefresh(ctx context.Context) {
	go func() {
		timer := time.NewTimer(45 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				s.refreshAllPeerUpstreams()
				timer.Reset(peerUpstreamRefreshInterval)
			}
		}
	}()
}

// refreshAllPeerUpstreams 按 upstreamKey 每组只刷一次，避免同一上游的多个
// 成员 Provider 触发 N 次相同请求。
func (s *Server) refreshAllPeerUpstreams() {
	seen := map[string]bool{}
	for _, provider := range s.router.State().Providers {
		if provider.AuthType != domain.AuthTypePeer || provider.Deleted || provider.Disabled {
			continue
		}
		key := peerGroupKeyForProvider(provider)
		if seen[key] {
			continue
		}
		seen[key] = true
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, _, err := s.refreshPeerUpstream(ctx, provider)
		cancel()
		if err != nil {
			s.logs.AddApp("warn", "peer upstream background refresh failed", fmt.Sprintf("provider=%s: %v", provider.ID, err))
			continue
		}
		s.logs.AddApp("debug", "peer upstream background refreshed", provider.ID)
	}
}

// handleDisconnectPeerUpstream 断开一条上游：把它落在本机的整组成员行一起删掉。
//
// 一条上游授权了 N 个 provider，本机就有 N 条成员行，它们共享同一个 UpstreamKey。
// 按「一行删一条」处理会留下半条链路：界面还显示连着，请求却只剩某个协议的成员，
// 而且成员行之间的凭据必须是同一把，删一半等于留下一组自相矛盾的记录。所以这个
// 接口以「上游」为单位工作，删的是组。
//
// 删除沿用 provider 的软删除（可恢复），与本机其它 provider 一致；上游那边的注册
// 记录不会因此消失——要让上游也忘掉这台下游，得去上游的「网关互联」页吊销。
func (s *Server) handleDisconnectPeerUpstream(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, domain.PermPeersManage) {
		return
	}
	provider, ok := s.peerUpstreamByID(w, r)
	if !ok {
		return
	}
	upstreamKey := peerGroupKeyForProvider(provider)
	memberIDs := make([]string, 0, 4)
	for _, candidate := range s.router.State().Providers {
		if candidate.AuthType != domain.AuthTypePeer || candidate.Deleted {
			continue
		}
		if peerGroupKeyForProvider(candidate) != upstreamKey {
			continue
		}
		memberIDs = append(memberIDs, candidate.ID)
	}
	if len(memberIDs) == 0 {
		writeOpenAIError(w, http.StatusNotFound, "这条上游已经没有成员行了")
		return
	}
	removed := make([]string, 0, len(memberIDs))
	for _, id := range memberIDs {
		if err := s.router.DeleteProvider(id); err != nil {
			// 已经删掉的成员不该让整组失败：把剩下的继续删完，最后如实报告删了哪些。
			s.logs.AddApp("warn", "peer upstream member delete failed", fmt.Sprintf("provider=%s: %v", id, err))
			continue
		}
		removed = append(removed, id)
	}
	if len(removed) == 0 {
		writeOpenAIError(w, http.StatusConflict, "这一组成员行都没能删除，请检查是否有 API 密钥绑定")
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "peer upstream disconnected", fmt.Sprintf("upstream=%s removed=%d/%d", upstreamKey, len(removed), len(memberIDs)))
	writeJSON(w, http.StatusOK, map[string]any{
		"disconnected": true,
		"removed":      len(removed),
		"total":        len(memberIDs),
	})
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
	// 新凭据要落到这一组的每一条成员行上：它们各存一份 apiKeySource。
	upstreamKey := peerGroupKeyForProvider(provider)
	upstreamName := provider.Name
	if provider.PeerGrants != nil && strings.TrimSpace(provider.PeerGrants.UpstreamName) != "" {
		upstreamName = provider.PeerGrants.UpstreamName
	}
	members := s.peerUpstreamMembers(upstreamKey)
	if len(members) == 0 {
		members = []domain.Provider{provider}
	}
	for _, member := range members {
		models := peerModelsForProtocol(member.Protocol, granted)
		defaultModel := strings.TrimSpace(member.DefaultModel)
		if defaultModel == "" && len(models) > 0 {
			defaultModel = models[0].ID
		}
		if _, err := s.router.UpdateProvider(member.ID, domain.Provider{
			APIKeySource:     "literal:" + registration.Token,
			PeerRefreshToken: registration.RefreshToken,
			BaseURL:          registration.ParentURL,
			DefaultModel:     defaultModel,
			Models:           models,
		}); err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if granted != nil {
		// 重新注册换了一把凭据，也重新问到了授权清单和账号信息：成员行与整份
		// 快照一起刷新（授权可能在这期间变了，增删也在这里统一处理）。
		if _, err := s.reconcilePeerUpstreamMembers(provider, registration, granted, upstreamName); err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, member := range s.peerUpstreamMembers(upstreamKey) {
			if routeErr := s.ensurePeerUpstreamRoute(member); routeErr != nil {
				s.logs.AddApp("warn", "peer upstream route not created", fmt.Sprintf("provider=%s: %v", member.ID, routeErr))
			}
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
