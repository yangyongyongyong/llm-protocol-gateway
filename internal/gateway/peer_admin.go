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
	// IngressRequests 是最近这段时间它送进来的请求数，用来一眼看出"活跃/挂空"。
	IngressRequests int64 `json:"ingressRequests"`
}

// handleListPeers 列出所有已注册的下游网关。
func (s *Server) handleListPeers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
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
	grants := s.resourceGrants()
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
		item := peerSummary{
			UserID:             user.ID,
			Username:           user.Username,
			GatewayID:          strings.TrimSpace(user.GatewayID),
			Name:               strings.TrimSpace(user.DisplayName),
			Enabled:            user.Enabled,
			CreatedAt:          user.CreatedAt,
			GrantedProviderIDs: grants[user.ID],
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
		// 让界面能把限额直接写在提示里，而不是写死一个数字。
		"maxHops":                   maxGatewayHops,
		"registrationAllowCidrs":    s.router.PeerRegistrationAllowCIDRs(),
		"registrationAllowsPrivate": strings.TrimSpace(s.router.PeerRegistrationAllowCIDRs()) == "",
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
	if _, ok := s.requireAdmin(w, r); !ok {
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
// 注册是唯一不需要已有凭据的入口，所以这条设置值得单独暴露出来：默认只允许
// 本机与内网，公网部署时管理员必须显式写出允许的来源段。
func (s *Server) handleSetPeerRegistrationCIDRs(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
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
		writeOpenAIError(w, http.StatusBadRequest, "cidrs is required (empty string restores the private-only default)")
		return
	}
	raw := strings.TrimSpace(*payload.CIDRs)
	// 先校验再保存：一个写错的 CIDR 如果只是被忽略，管理员会以为已经限制住了
	// 来源，实际却仍在按默认规则放行内网。
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
