package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 下游注册到上游。
//
// 用户提的做法在这里落地：下游用「网关 ID + 用户名 + 密码」注册，上游把它
// 建成一行 users(kind=peer) 加一把 api_keys，于是它立刻出现在用户管理页里，
// 和其它账号并排显示、并排授权、并排禁用。
//
// 密码只在注册时用一次，换回的是一把长期的对端凭据。之后所有透传请求都用
// 那把凭据，不再发密码——密码是可被浏览器/代理缓存和回放的东西，凭据则能
// 单独吊销、单独看最后一次使用时间。

// peerRegisterRequest 是 POST /__peer/register 的请求体。
type peerRegisterRequest struct {
	// GatewayID 是下游自己的稳定身份（gatewayid 派生，重装不变）。注册按它
	// 做幂等：同一个网关重复注册不会建出第二个账号。
	GatewayID string `json:"gatewayId"`
	// Name 是下游的显示名，缺省用 GatewayID。
	Name     string `json:"name,omitempty"`
	Username string `json:"username"`
	Password string `json:"password"`
	// RequestedProviderIDs 是下游希望使用的 provider。上游不会照单全收：
	// 权限始终由上游管理员决定，这里只是给一个初始授权清单，方便上游在
	// 「用户管理」里对着它勾。
	RequestedProviderIDs []string `json:"requestedProviderIds,omitempty"`
}

// peerRegisterResponse 是注册成功后返回的内容。
type peerRegisterResponse struct {
	// Token 是上游给这把对端凭据的明文，只在此刻返回一次。
	Token string `json:"token"`
	// GatewayID 回显上游记录的网关身份，方便下游核对注册到了谁身上。
	GatewayID string `json:"gatewayId"`
	// UserID 是上游为这个对端建的账号 ID。下游记下来，排查时能直接对上
	// 上游用户管理页里的那一行。
	UserID string `json:"userId"`
	// Username 是上游账号名（带 peer- 前缀，与网关身份派生）。
	Username string `json:"username"`
	// GrantedProviderIDs 是上游当前实际授权给它的 provider 列表。可能比
	// RequestedProviderIDs 少——被拒的请求要能看见，不然下游会一直重试一个
	// 永远不会成功的东西。
	GrantedProviderIDs []string `json:"grantedProviderIds"`
}

// peerAccountName 由网关身份派生账号名：同一个下游无论重装多少次、改多少次
// 显示名，落到的都是同一个用户名。
func peerAccountName(gatewayID string) string {
	id := strings.TrimSpace(strings.ToLower(gatewayID))
	if id == "" {
		return peerUsernamePrefix + "unknown"
	}
	return peerUsernamePrefix + id
}

// handlePeerRegister 处理下游的注册/重新注册。
//
// 幂等：同一个 GatewayID 再次注册时复用原账号，只换一把新凭据（旧的立刻失效
// ——下游重装或怀疑凭据泄漏时会走这条路，留着旧的等于留了口子）。
func (s *Server) handlePeerRegister(w http.ResponseWriter, r *http.Request) {
	if s.userStore == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "user store is not configured")
		return
	}
	if !s.peerRegistrationAllowed(r) {
		s.logs.AddApp("warn", "peer registration refused", "source address not allowed: "+requestClientIP(r))
		writeOpenAIError(w, http.StatusForbidden, "peer registration is not allowed from this address")
		return
	}
	var payload peerRegisterRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	gatewayID, err := validatePeerGatewayID(payload.GatewayID)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	username := strings.TrimSpace(payload.Username)
	password := strings.TrimSpace(payload.Password)
	if username == "" || len(password) < adminPasswordMinLen {
		writeOpenAIError(w, http.StatusBadRequest,
			fmt.Sprintf("username is required and password must be at least %d characters", adminPasswordMinLen))
		return
	}
	if strings.EqualFold(username, "admin") {
		writeOpenAIError(w, http.StatusBadRequest, "username \"admin\" is reserved")
		return
	}

	existingUser, err := s.userStore.UserByGatewayID(gatewayID)
	account := peerAccount{User: existingUser}
	created := false
	switch {
	case err != nil:
		// 没有这个网关的账号：确认用户名没被占用之后建一个。
		if taken, lookupErr := s.userStore.UserByUsername(username); lookupErr == nil && !taken.IsPeer() {
			writeOpenAIError(w, http.StatusConflict, "username already exists")
			return
		}
		account, err = s.createPeerAccount(gatewayID, username, password, payload.Name)
		if err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		created = true
	case account.User.Username != username || !peerPasswordMatches(account.User, password):
		// 账号已经存在：用户名或密码对不上就不许改。否则任何人只要拿到一个
		// 网关 ID（它是公开信息，会出现在界面上）就能冒名顶替、把别人的下游
		// 顶掉并换走凭据。
		s.logs.AddApp("warn", "peer re-registration refused", fmt.Sprintf("%s: credential mismatch", account.User.Username))
		writeOpenAIError(w, http.StatusUnauthorized, "peer account already exists and the credentials do not match")
		return
	}

	// 换发凭据：旧的立刻失效。同一次调用里完成，避免中途失败留下两把有效凭据。
	token, err := generatePeerToken()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to generate peer token: "+err.Error())
		return
	}
	key, err := s.rotatePeerToken(account, token)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	account.Key = key

	// 首次注册按请求里的清单预置授权；已有账号不动它的授权——重新注册不能
	// 变成一条悄悄提权的路径。
	if created {
		if err := s.applyPeerRequestedGrants(account.User.ID, payload.RequestedProviderIDs); err != nil {
			// 授权失败不该让已经建好的账号和凭据作废：上游管理员本来就要在
			// 用户管理页里再确认一遍，这里记日志即可。
			s.logs.AddApp("warn", "peer requested grants not applied", err.Error())
		}
	}

	granted := s.resourceGrants()[account.User.ID]
	if granted == nil {
		granted = []string{}
	}
	verb := "re-registered"
	if created {
		verb = "registered"
	}
	s.logs.AddApp("info", "peer gateway "+verb, fmt.Sprintf("%s (%s) from %s", account.User.Username, gatewayID, requestClientIP(r)))
	writeJSON(w, http.StatusOK, peerRegisterResponse{
		Token:              token,
		GatewayID:          gatewayID,
		UserID:             account.User.ID,
		Username:           account.User.Username,
		GrantedProviderIDs: granted,
	})
}

// peerPasswordMatches 比对已存下的密码摘要。
//
// peer 账号的 password_hash 为空时这里恒返回 false：那意味着它是以别的途径
// 建出来的（或者是升级前的残留），没有可校验的密码，就不该接受重新注册。
// bcrypt 对空 hash 会直接报错，正好是想要的语义。
func peerPasswordMatches(account domain.User, password string) bool {
	return bcryptPasswordMatches(account.PasswordHash, password)
}

// createPeerAccount 建一行 kind=peer 账号。
//
// 密码存 bcrypt 摘要，但**永远不能用来登录**：对端账号在控制台登录路径上被
// 显式拒绝（见 handleAdminLogin / sessionFromRequest），所以这份摘要只服务于
// 「重新注册时校验同一个下游」这一个用途。保留它而不是丢弃，是因为没有它
// 任何人都能凭一个公开的网关 ID 顶替别人的注册。
func (s *Server) createPeerAccount(gatewayID, username, password, displayName string) (peerAccount, error) {
	hash, err := bcryptHash(password)
	if err != nil {
		return peerAccount{}, err
	}
	existing, _ := s.userStore.ListUsers()
	user := domain.User{
		ID: uniqueID(slug(username), func(id string) bool {
			if id == legacyAdminUserID {
				return true
			}
			for _, item := range existing {
				if item.ID == id {
					return true
				}
			}
			return false
		}),
		Username:     username,
		PasswordHash: hash,
		// 对端账号用最低权限角色：它只能按被授予的 provider 走，不能看控制台。
		Role:        domain.UserRoleUser,
		RoleID:      domain.RoleIDUser,
		DisplayName: strings.TrimSpace(displayName),
		Enabled:     true,
		CreatedAt:   nowRFC3339(),
		Kind:        domain.UserKindPeer,
		GatewayID:   gatewayID,
	}
	if user.DisplayName == "" {
		user.DisplayName = gatewayID
	}
	if err := s.userStore.CreateUser(user); err != nil {
		return peerAccount{}, err
	}
	return peerAccount{User: user}, nil
}

// rotatePeerToken 为对端换发凭据：删掉它名下已有的对端密钥，再插一把新的。
//
// 先删后插而不是就地改写，是因为「吊销后重建」和「换发」在语义上是同一件
// 事，共用一条已验证的路径比维护两条更安全。整个过程在路由状态锁内完成，
// 请求线程看到的要么是旧凭据、要么是新凭据，不会看到空档。
//
// token 以明文写进 api_keys.key——和普通密钥同一列、同一套读写口径（鉴权时
// 现算摘要比较）。控制台的密钥页本来就靠这一列展示密钥，对端凭据能在这里看到
// 反而有用：管理员可以对着它确认「这个下游用的还是不是那一把」。
func (s *Server) rotatePeerToken(account peerAccount, token string) (domain.APIKey, error) {
	name := peerKeyName(account.User)
	key := domain.APIKey{
		ID:          peerKeyID(account.User.ID),
		Name:        name,
		Key:         token,
		Enabled:     true,
		CreatedAt:   nowRFC3339(),
		OwnerUserID: account.User.ID,
	}
	if err := s.router.ReplacePeerKey(key); err != nil {
		return domain.APIKey{}, err
	}
	if err := s.saveState(); err != nil {
		return domain.APIKey{}, fmt.Errorf("failed to save configuration: %w", err)
	}
	return key, nil
}

// peerKeyID 由账号 ID 派生，保证一个对端只有一把凭据。
func peerKeyID(userID string) string { return "peerkey-" + strings.TrimSpace(userID) }

// peerKeyName 是密钥在控制台里的显示名。
func peerKeyName(user domain.User) string {
	label := strings.TrimSpace(user.DisplayName)
	if label == "" {
		label = strings.TrimSpace(user.GatewayID)
	}
	if label == "" {
		label = user.Username
	}
	return "网关 " + label
}

// validatedPeerGrants 把请求的 provider 列表过滤成真实存在、未删除的那部分。
// 列表本身不可信（可能来自旧控制台页面缓存），所以不存在的 ID 静默丢弃，
// 而不是让整单失败。
func (s *Server) validatedPeerGrants(requested []string) []string {
	valid := map[string]bool{}
	for _, provider := range s.router.State().Providers {
		if provider.Deleted {
			continue
		}
		valid[provider.ID] = true
	}
	grants := make([]string, 0, len(requested))
	for _, id := range requested {
		id = strings.TrimSpace(id)
		if id != "" && valid[id] {
			grants = append(grants, id)
		}
	}
	return grants
}

// applyPeerRequestedGrants 首次注册时按请求预置授权，只接受真实存在的、
// 未删除的 provider，其余静默丢弃（列表本身不可信，也不该让注册失败）。
// 空列表意味着"没请求"，保持现状不动——这是注册语义；授权编辑的"清空"
// 语义在 setPeerGrants。
func (s *Server) applyPeerRequestedGrants(userID string, requested []string) error {
	if len(requested) == 0 {
		return nil
	}
	roleStore, ok := s.roleStoreRef()
	if !ok {
		return fmt.Errorf("role store is not configured")
	}
	return roleStore.SetResourceGrants(userID, s.validatedPeerGrants(requested))
}

// setPeerGrants 无条件写一个账号的授权：空列表就是收回全部。
// 授权编辑入口（handleSetPeerGrants）用它，与注册预置的"不动"语义区分开。
func (s *Server) setPeerGrants(userID string, requested []string) error {
	roleStore, ok := s.roleStoreRef()
	if !ok {
		return fmt.Errorf("role store is not configured")
	}
	return roleStore.SetResourceGrants(userID, s.validatedPeerGrants(requested))
}

// peerRegistrationAllowed 判断这个来源是否允许注册。
//
// 注册是唯一不需要已有凭据的入口，所以它是整条链路上最值得收紧的地方：
//   - 默认只允许本机/内网来源（下游通常在内网，或者经隧道从反代进来）；
//   - 显式配置了 CIDR 白名单时，只认白名单。
//
// 用 requestClientIP 而不是直接读 RemoteAddr：它已经处理了「公网对端伪造
// X-Forwarded-For」的情况，公网直连时会忽略所有转发头。
func (s *Server) peerRegistrationAllowed(r *http.Request) bool {
	raw := strings.TrimSpace(s.router.PeerRegistrationAllowCIDRs())
	if raw == "" {
		return isPrivateOrLoopbackAddress(requestClientIP(r))
	}
	ip := net.ParseIP(strings.TrimSpace(requestClientIP(r)))
	if ip == nil {
		return false
	}
	for _, cidr := range splitCIDRList(raw) {
		if _, network, err := net.ParseCIDR(cidr); err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

// isPrivateOrLoopbackAddress 判断来源是否属于本机/内网/链路本地。
func isPrivateOrLoopbackAddress(raw string) bool {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// splitCIDRList 把逗号/空白分隔的 CIDR 串拆开。
func splitCIDRList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// peerContextKey 把校验通过的对端挂到请求上，供透传 handler 使用。
type peerContextKey struct{}

func withPeerAccount(ctx context.Context, account peerAccount) context.Context {
	return context.WithValue(ctx, peerContextKey{}, account)
}

// peerAccountFrom 取回校验通过的对端；没有则返回 false。
func peerAccountFrom(ctx context.Context) (peerAccount, bool) {
	if ctx == nil {
		return peerAccount{}, false
	}
	account, ok := ctx.Value(peerContextKey{}).(peerAccount)
	return account, ok
}

// requirePeer 是 /__peer/* 的统一闸门。
//
// 控制台的会话 cookie 在这里一概无效：对端入口只认对端凭据。两条鉴权路径
// 混用的话，任何一个控制台接口的疏漏都会变成对端入口的疏漏。
func (s *Server) requirePeer(w http.ResponseWriter, r *http.Request) (peerAccount, bool) {
	account, err := s.authenticatePeer(r)
	if err != nil {
		s.writePeerAuthError(w, r, "invalid peer credential", err)
		return peerAccount{}, false
	}
	return account, true
}

// touchPeerSeen 更新时间戳，供控制台显示"这个下游还有没有在动"。
//
// 只写内存：对端每发一个请求都落一次库会把单条 SQLite 写连接打满，而
// LastUsedAt 本来就是展示用的，重启后重新累计可以接受（API 密钥也是这个口径）。
func (s *Server) touchPeerSeen(account peerAccount) {
	if s.router == nil || strings.TrimSpace(account.Key.ID) == "" {
		return
	}
	s.router.TouchAPIKeyLastUsed(account.Key.ID, nowRFC3339())
}
