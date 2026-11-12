package gateway

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// peerSummary 是管理页里一个下游网关的样子。
//
// 它刻意不返回凭据的任何形态（连摘要都不给）：管理员需要的是"这个下游还在
// 不在、能用什么、最后什么时候来的"，而不是一把能直接拿去用的密钥。要换发
// 凭据就让下游重新注册——那样上游始终只留一把有效凭据。
type peerSummary struct {
	UserID    string `json:"userId"`
	Username  string `json:"username"`
	GatewayID string `json:"gatewayId"`
	Name      string `json:"name,omitempty"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"createdAt,omitempty"`
	// LastSeenAt 来自它那把对端密钥的最后使用时间。
	LastSeenAt string `json:"lastSeenAt,omitempty"`
	// KeyID 是凭据行的 ID，方便管理员在上游的密钥页里对照；不含任何密钥内容。
	KeyID string `json:"keyId,omitempty"`
	// KeyEnabled 反映凭据本身是否可用：吊销后用户行还在（保留历史授权），
	// 但凭据已删，所以这两者要分开显示。
	KeyEnabled         bool     `json:"keyEnabled"`
	GrantedProviderIDs []string `json:"grantedProviderIds"`
	// GrantMode 是 "selected" 或 "all"。all 表示动态可见当前及未来新增的
	// Provider；GrantedProviderIDs 这时是当前展开结果，仅用于计数与预览。
	GrantMode string `json:"grantMode"`
	// IngressRequests 是最近这段时间它送进来的请求数，用来一眼看出"活跃/挂空"。
	IngressRequests int64 `json:"ingressRequests"`
}

const (
	peerGrantModeSelected = "selected"
	peerGrantModeAll      = "all"
)

// normalizePeerGrantMode 归一化授权模式；旧版控制台不带字段时按指定列表处理。
func normalizePeerGrantMode(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", peerGrantModeSelected:
		return peerGrantModeSelected, nil
	case peerGrantModeAll:
		return peerGrantModeAll, nil
	default:
		return "", fmt.Errorf("grantMode 必须是 selected 或 all")
	}
}

// handleListPeers 列出所有已注册的下游网关。
func (s *Server) handleListPeers(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, domain.PermPeersView) {
		return
	}
	if s.userStore == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "user store is not configured")
		return
	}
	users, err := s.userStore.ListUsers()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	keys := map[string]domain.APIKey{}
	for _, key := range s.router.State().APIKeys {
		if strings.HasPrefix(key.Key, peerTokenPrefix) {
			keys[strings.TrimSpace(key.OwnerUserID)] = key
		}
	}
	out := make([]peerSummary, 0)
	for _, user := range users {
		if !user.IsPeer() {
			continue
		}
		allProviders, grantedProviderIDs := s.peerGrantState(user.ID)
		grantMode := peerGrantModeSelected
		if allProviders {
			grantMode = peerGrantModeAll
		}
		item := peerSummary{
			UserID:             user.ID,
			Username:           user.Username,
			GatewayID:          strings.TrimSpace(user.GatewayID),
			Name:               strings.TrimSpace(user.DisplayName),
			Enabled:            user.Enabled,
			CreatedAt:          user.CreatedAt,
			GrantedProviderIDs: grantedProviderIDs,
			GrantMode:          grantMode,
		}
		if item.GrantedProviderIDs == nil {
			item.GrantedProviderIDs = []string{}
		}
		if key, ok := keys[user.ID]; ok {
			item.KeyID = key.ID
			item.KeyEnabled = key.Enabled
			item.LastSeenAt = key.LastUsedAt
		}
		item.IngressRequests = s.peerIngressCount(user.GatewayID)
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].GatewayID < out[j].GatewayID
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"peers": out,
		// 本机身份：下游把它记在「我注册到了谁」旁边，两边对得上才说明
		// 这条链路注册的是这台机器。
		"gatewayId": s.GatewayID(),
		// 让界面能把限额直接写在提示里，而不是写死一个数字。
		"maxHops":                s.maxGatewayHopsConfigured(),
		"registrationAllowCidrs": s.router.PeerRegistrationAllowCIDRs(),
		// registrationOpen 表示当前没有来源白名单：任何来源都能发起注册，
		// 界面的提示语据此区分「默认全放行」和「只放行这些网段」。
		"registrationOpen": strings.TrimSpace(s.router.PeerRegistrationAllowCIDRs()) == "",
	})
}

// peerIngressCount 统计某个下游最近送进来的请求数。
//
// 从内存里的请求日志取而不是查库：管理页打开时要立刻有数，而 SQLite 的读
// 也未必包含刚落地的那一批。真要看长期趋势去用控制台的日志页。
func (s *Server) peerIngressCount(gatewayID string) int64 {
	gatewayID = strings.TrimSpace(gatewayID)
	if gatewayID == "" || s.logs == nil {
		return 0
	}
	page := s.logs.Query(peerIngressQuery())
	var count int64
	for _, item := range page.Items {
		if item.PeerDirection == "ingress" && item.PeerGatewayID == gatewayID {
			count++
		}
	}
	return count
}

// peerIngressQuery 是上面那次统计取的窗口。
func peerIngressQuery() monitor.RequestLogQuery {
	return monitor.RequestLogQuery{Page: 1, PageSize: 500, Status: "all"}
}

// handleRevokePeer 吊销一个下游网关。
//
// 做两件事，缺一不可：
//   - 删掉它的对端凭据（下一次请求立刻被拒）；
//   - 保留用户行但记成禁用状态，于是它在用户管理页里仍然可见、授权也还在，
//     重新注册时能恢复，而不是"删了就永远不知道自己授权过什么"。
//
// 想彻底清掉它就用 DELETE /__users/{id}——那是用户管理页已有的删除，语义明确。
func (s *Server) handleRevokePeer(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, domain.PermPeersManage) {
		return
	}
	if s.userStore == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "user store is not configured")
		return
	}
	user, err := s.userStore.UserByID(r.PathValue("id"))
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, "peer not found")
		return
	}
	if !user.IsPeer() {
		writeOpenAIError(w, http.StatusBadRequest, "this account is not a peer gateway")
		return
	}
	if err := s.router.RemovePeerKeys(user.ID); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 库里的那一行也要删：对端凭据是逐条 CRUD 落的，不走 saveState。只清内存
	// 的话进程一重启那把已经吊销的凭据又会回来，等于吊销没发生过。
	if s.apiKeyStore != nil {
		for _, key := range s.apiKeyStoreKeys() {
			if strings.TrimSpace(key.OwnerUserID) != user.ID {
				continue
			}
			if err := s.apiKeyStore.DeleteAPIKey(key.ID); err != nil {
				writeOpenAIError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}
	user.Enabled = false
	if err := s.userStore.UpdateUser(user); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "peer gateway revoked", fmt.Sprintf("%s (%s)", user.Username, user.GatewayID))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "userId": user.ID})
}

// handleSetPeerRegistrationCIDRs 设置注册来源白名单。
//
// 默认没有白名单：任何来源都能发起注册，下游从公网直接连上来是正常用法。
// 这条设置是用来收紧的——写进白名单之后，只有列出的网段还能注册。
func (s *Server) handleSetPeerRegistrationCIDRs(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, domain.PermPeersManage) {
		return
	}
	var payload struct {
		CIDRs *string `json:"cidrs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if payload.CIDRs == nil {
		writeOpenAIError(w, http.StatusBadRequest, "cidrs is required (an empty string removes the whitelist)")
		return
	}
	raw := strings.TrimSpace(*payload.CIDRs)
	// 先校验再保存：一个写错的 CIDR 如果只是被忽略，管理员会以为已经限制住了
	// 来源，实际却仍在放行任何来源。
	for _, cidr := range splitCIDRList(raw) {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, fmt.Sprintf("invalid CIDR %q: %v", cidr, err))
			return
		}
	}
	s.router.SetPeerRegistrationAllowCIDRs(raw)
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "peer registration cidrs updated", raw)
	writeJSON(w, http.StatusOK, map[string]any{"cidrs": s.router.PeerRegistrationAllowCIDRs()})
}

// handleSetPeerMaxHops 设置链路跳数上限（1..8，0 恢复默认 3）。
//
// 环检测每台网关都按自己的配置做：把上限调大是让"内网 → 公网 → 公网"这种
// 真实的多级链路能走通，调小则是在自己这一段更早拦住被伪造/误配的长链。
// 现有会话不受影响——判定发生在每个新请求上。
func (s *Server) handleSetPeerMaxHops(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, domain.PermPeersManage) {
		return
	}
	var payload struct {
		Hops *int `json:"hops"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if payload.Hops == nil {
		writeOpenAIError(w, http.StatusBadRequest, "hops is required (0 restores the default of 3)")
		return
	}
	s.router.SetMaxGatewayHops(*payload.Hops)
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	effective := s.router.MaxGatewayHops()
	s.logs.AddApp("info", "peer max hops updated", fmt.Sprintf("%d（生效上限 %d）", *payload.Hops, effective))
	writeJSON(w, http.StatusOK, map[string]any{"hops": effective})
}

// handleSetPeerGrants 修改一个已注册下游网关的授权。
//
// 注册时的预置授权（applyPeerRequestedGrants）只是一次快照；上游的授权应当
// 随时可以调整——新增、收回都立即生效，不需要下游重新注册。校验规则与注册
// 时一致：只接受真实存在、未删除的 provider；传空列表等于收回全部授权，
// 下游下一次请求就会拿不到任何 provider。
//
// 权限走 /__peers/ 前缀已有的 PermPeersManage（rbac routePermission）。
func (s *Server) handleSetPeerGrants(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, domain.PermPeersManage) {
		return
	}
	if s.userStore == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "user store is not configured")
		return
	}
	user, err := s.userStore.UserByID(r.PathValue("id"))
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, "peer not found")
		return
	}
	if !user.IsPeer() {
		writeOpenAIError(w, http.StatusBadRequest, "this account is not a peer gateway")
		return
	}
	var payload struct {
		ProviderIDs *[]string `json:"providerIds"`
		GrantMode   string    `json:"grantMode,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	mode, err := normalizePeerGrantMode(payload.GrantMode)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if mode == peerGrantModeAll {
		if err := s.setPeerAllProviders(user.ID); err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		if payload.ProviderIDs == nil {
			writeOpenAIError(w, http.StatusBadRequest, "providerIds is required when grantMode=selected (an empty list revokes all grants)")
			return
		}
		// 与注册预置同一套校验：不存在的 ID 静默丢弃而不是整单拒绝——授权列表
		// 本来就不可信（可能来自旧控制台页面缓存），删掉的 provider 不该让管理员
		// 连合法的部分都存不进去。空列表在这里是"收回全部"，与注册的"不动"区分。
		if err := s.setPeerGrants(user.ID, *payload.ProviderIDs); err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	allProviders, granted := s.peerGrantState(user.ID)
	if granted == nil {
		granted = []string{}
	}
	if allProviders {
		s.logs.AddApp("info", "peer grants updated", fmt.Sprintf("%s (%s) → 全部 Provider（动态，当前 %d 个）", user.Username, user.GatewayID, len(granted)))
	} else {
		s.logs.AddApp("info", "peer grants updated", fmt.Sprintf("%s (%s) → %d 个 provider", user.Username, user.GatewayID, len(granted)))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"userId":             user.ID,
		"grantMode":          mode,
		"grantedProviderIds": granted,
	})
}
