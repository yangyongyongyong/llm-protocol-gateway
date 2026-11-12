package gateway

import (
	"fmt"
	"net/http"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 对端透传的执行段。
//
// 这里刻意不重写"怎么连上游"：凭据、协议头、适配器、各种 OAuth 刷新、流式
// 转发、用量解析都在各协议既有的直通函数里，重写一遍等于把它们全部重新实现
// 一次，而且必然会漏掉某个 provider 的特殊处理（Claude 的 /messages 路径就是
// 一个：通用的 OpenAI 式拼接会把它打到 /chat/completions 上）。下游的价值恰恰
// 是"上游已经连好的那些"，所以这里只做三件事：选定 provider、把它交给那个
// 协议自己的直通函数、记一条日志。

// servePeerProtocol 在选定的 provider 上执行一次透传请求。
func (s *Server) servePeerProtocol(w http.ResponseWriter, r *http.Request, account peerAccount, decision domain.RouteDecision, model string, payload []byte) {
	provider, err := s.router.ProviderByID(decision.ProviderID)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "granted provider is no longer available: "+err.Error())
		return
	}
	// 下游送来的 payload 协议可能不是这台 Provider 声明的那一种：上游实测
	// 支持它（selectPeerProvider 已确认）时，本次就按 payload 的协议直通。
	// 只改这一次尝试的副本，不落库、不影响其它请求。
	if decision.InputProtocol != "" {
		provider.Protocol = decision.InputProtocol
	}
	started := time.Now()
	// 下游的凭据不能以任何形式继续往上游走。Claude 直通在没有配置自己的 key
	// 时会回退到客户端的 Authorization，而这里那个头正是对端凭据——先摘掉，
	// 让"provider 自己的凭据"成为唯一可能的来源。
	r = r.Clone(r.Context())
	r.Header.Del("Authorization")
	r.Header.Del("X-Api-Key")
	// 告下游"门后面是谁"：下游靠这个头在它自己的日志里记下 egress 对端。
	// 必须是本网关身份，而不是任何外部输入。
	w.Header().Set(peerIdentityHeader, s.GatewayID())

	var (
		status   int
		usage    TokenUsage
		recorded []byte
		callErr  error
	)
	// 按 provider 的协议选直通函数。上游 payload 的协议在选 provider 时已经
	// 对齐过（见 selectPeerProvider），所以这里不存在"协议对不上"的分支。
	switch provider.Protocol {
	case domain.ProtocolClaude:
		status, usage, recorded, callErr = s.proxyClaudeMessages(w, r, provider, payload)
	case domain.ProtocolOpenAIResponses:
		status, usage, recorded, callErr = s.proxyOpenAIResponses(w, r, provider, model, payload, true)
	default:
		status, usage, recorded, callErr = s.proxyOpenAIChat(w, r, provider, model, payload, true)
	}
	if callErr != nil {
		// 直通函数自己已经写了响应体时不要重复写；它只在连不上上游时才返回
		// 一个尚未动过响应的错误。
		s.recordPeerRequestLog(r, started, account, provider, model, upstreamErrorStatus(callErr), usage, payload, []byte(callErr.Error()))
		if status == 0 {
			writeOpenAIError(w, upstreamErrorStatus(callErr), "upstream request failed: "+callErr.Error())
		}
		return
	}
	s.recordPeerRequestLog(r, started, account, provider, model, status, usage, payload, recorded)
	s.touchPeerSeen(account)
}

// recordPeerRequestLog 记一条对端请求。
//
// 父网关这一侧只留用量，不留正文：那一次对话是子网关的用户跟模型说的，
// 父网关既不是它的服务对象，也没有留存它的理由——"只统计透传过来的用量"
// 正是当初定下的边界。所以请求正文一栏只写 provider 与模型（足够对上账），
// 响应正文留空；链路上的 ingress 标记照常写，用量与状态照常写。
func (s *Server) recordPeerRequestLog(r *http.Request, started time.Time, account peerAccount, provider domain.Provider, model string, status int, usage TokenUsage, requestBody, responseBody []byte) {
	summary := fmt.Sprintf(`{"peerGatewayId":%q,"providerId":%q,"model":%q}`, account.gatewayIDValue(), provider.ID, model)
	var errorBody []byte
	if status < 200 || status >= 300 {
		// 失败原因要留：它是父网关自己排障时唯一能看到的线索，而且错误正文
		// 比对话正文小几个数量级，脱敏后留存不成问题。
		errorBody = responseBody
	}
	s.recordRequestLogFromRequest(r, started, account.Key, true, "", provider.ID, model, "peer",
		string(provider.Protocol), requestPathOf(r), status, usage, []byte(summary), errorBody)
}
