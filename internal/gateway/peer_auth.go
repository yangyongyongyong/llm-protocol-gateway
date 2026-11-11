package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/gatewayid"
)

// 父子网关的对端凭据。
//
// 一个对端 = users 表里一行 kind=peer 的账号 + api_keys 表里一行属于它的
// 密钥。这样它天然能出现在用户管理页、能被授权具体 provider、能被禁用（禁用
// 即停流量，见 principalForUserID），而不需要为「网关」单独造一套权限模型。
//
// 密钥前缀刻意与普通密钥不同：泄漏时一眼能看出这是网关凭据而不是模型密钥，
// 也方便日志和告警按前缀归类。
const peerTokenPrefix = "gwpeer-"

// peerUsernamePrefix 让对端账号在用户管理页里一眼可辨，即使界面还没渲染
// kind 列。用户名唯一由网关身份派生，因此同一个下游重复注册是幂等的。
const peerUsernamePrefix = "peer-"

var (
	// errPeerTokenInvalid 是"这把凭据不是有效对端"的唯一对外说法。
	// 具体是没注册、被吊销、还是账号被禁用，只在服务端日志里区分——对端
	// 拿不到理由就无从试探哪个环节出了问题。
	errPeerTokenInvalid = errors.New("peer credential is not valid")
	// errPeerLoopDetected 表示链路里已经出现过本网关（真正传给 handler 的那个）。
	errPeerLoopDetected = errors.New("gateway loop detected")
)

// generatePeerToken 生成一把新的对端凭据。
//
// 用前缀而不是纯随机串，是为了让运维在日志/配置里一眼认出这是网关凭据。
//
// 明文写进 api_keys.key，和普通密钥（sk-gw-…）完全一致：那张表存的就是可用
// 凭据本身，控制台的密钥页也靠这一列把密钥显示给它的所有者。摘要只在比对时
// 现算（见 peerTokenHash），因此表里既有 shape 一致、也仍然只有一份真值。
func generatePeerToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return peerTokenPrefix + hex.EncodeToString(buf), nil
}

// peerTokenHash 与 APIKeyByToken 的比对口径一致：都是对明文取一次 sha256，
// 再恒定时间比较。两条鉴权路径共用同一套口径，就不会有人在新路径上写出一个
// 更弱的比较。
func peerTokenHash(raw string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(raw)))
	return hex.EncodeToString(sum[:])
}

// peerAccount 是一个校验通过的对端。
type peerAccount struct {
	// User 是对端在 users 表里那行（kind=peer）。
	User domain.User
	// Key 是它对本机的凭据行，OwnerUserID 指向 User。
	Key domain.APIKey
}

// gatewayIDValue 返回对端的网关身份。以 users.gateway_id 为准，缺失时退化
// 成密钥所有者 ID——后者也可用，只是可读性差一些。
func (a peerAccount) gatewayIDValue() string {
	if id := strings.TrimSpace(a.User.GatewayID); id != "" {
		return id
	}
	return strings.TrimSpace(a.User.ID)
}

// authenticatePeer 校验对端凭据并返回它对应的账号。
//
// 三道都是必须的，缺一条就会出现"吊销了还能用"或"禁用了还能用"：
//   - 凭据本身存在且启用（api_keys 行）；
//   - 它属于一个 kind=peer 的账号（本地用户的密钥不能从对端入口进来）；
//   - 那个账号本身是启用的（管理员在用户管理页禁用即停）。
//
// 凭据比对用 sha256 + constant-time：和 APIKeyByToken 同一套口径，避免在
// 这条路径上引入另一种（可能更弱的）比较方式。
func (s *Server) authenticatePeer(r *http.Request) (peerAccount, error) {
	raw := bearerToken(r)
	if raw == "" || !strings.HasPrefix(raw, peerTokenPrefix) {
		return peerAccount{}, errPeerTokenInvalid
	}
	if s.userStore == nil {
		return peerAccount{}, errPeerTokenInvalid
	}
	wanted := peerTokenHash(raw)
	// 先按摘要找出密钥行。这里不提前返回，逐个比对全部候选，理由同
	// APIKeyByToken：命中的是哪一把不该从耗时上看出来。摘要是定长十六进制，
	// 直接比字符串就够，不必再解回字节。
	var candidates []domain.APIKey
	for _, key := range s.router.State().APIKeys {
		if key.Key == "" || !strings.HasPrefix(key.Key, peerTokenPrefix) {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(peerTokenHash(key.Key)), []byte(wanted)) == 1 {
			candidates = append(candidates, key)
		}
	}
	for _, key := range candidates {
		if !key.Enabled {
			continue
		}
		user, err := s.userStore.UserByID(strings.TrimSpace(key.OwnerUserID))
		if err != nil || !user.IsPeer() || !user.Enabled {
			continue
		}
		return peerAccount{User: user, Key: key}, nil
	}
	return peerAccount{}, errPeerTokenInvalid
}

// peerIdentityHeader 是下游转发时报告自己身份的响应头。它只是提示：
// 真正的身份来自凭据，头可以被伪造，所以任何决策都不能只依赖它。
const peerIdentityHeader = "X-Gateway-Id"

// writePeerAuthError 把失败统一成一种对外表述，内部原因写日志。
func (s *Server) writePeerAuthError(w http.ResponseWriter, r *http.Request, reason string, err error) {
	if s != nil && s.logs != nil {
		s.logs.AddApp("warn", "peer request refused", fmt.Sprintf("%s %s: %s (%v)", r.Method, requestPathOf(r), reason, err))
	}
	writeOpenAIError(w, http.StatusUnauthorized, errPeerTokenInvalid.Error())
}

// validatePeerGatewayID 校验下游自报的身份格式。
//
// 注册时下游必须给出一个形状正确的网关 ID：形状明显不对说明它根本没实现
// 这一侧（或者配置错了），早拒绝比事后再排查便宜。但格式对不等于可信——
// 真正的防伪靠的是凭据，ID 只用于展示与环路检测。
func validatePeerGatewayID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" {
		return "", fmt.Errorf("gatewayId is required")
	}
	if !gatewayid.IsWellFormed(id) {
		return "", fmt.Errorf("gatewayId must look like %q followed by 16 hex characters", "gw_")
	}
	return strings.ToLower(id), nil
}
