package gateway

import (
	"bytes"
	"context"
	"net/http"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 下游把请求送进父网关的那一扇门。
//
// 整条下游链路只留这一个出口，理由很实际：一次 provider 请求最终变成 []byte
// 的地方有四类（OpenAI chat 直通、经 OpenAI 的转换、Responses、Claude），而对端
// 要的信封、链路头、凭据三样东西一个都不能漏。分散到各自写一遍，漏掉的那一处
// 不会在本机报错，只会变成父网关回一句 400 "payload is required" 或 401，很难
// 追到是网关自己的 bug；集中在这里，四处各自只多一个早返回分支。
//
// 包体的协议不必单独传：对端 provider 的 Protocol 就是它那一侧的声明，而
// executeProtocolFlow 正是按它决定直通还是转换，所以走到这里时 body 一定是
// 这个协议的形状。把这个值随包体告诉父网关，父网关才能在授权集里挑出真正读得懂
// 它的 provider，而不是把一份 openai_chat 的请求喂给 Claude 端点。
//
// providerId 同理：上游授权了 N 个 provider 时，子网关这边就是 N 条 provider，
// 每一条对应上游的一个。用户把其中一条选进备选，意思就是"这条请求走上游的那一个
// provider"，所以这里要把它的 id 带上去；留空（升级前注册的老记录）时上游仍按
// 授权集自己挑，行为和以前完全一样。
func (s *Server) sendPeerDoor(ctx context.Context, r *http.Request, provider domain.Provider, model string, body []byte, accept string) (*http.Response, error) {
	wrapped, err := buildPeerForwardBody(peerGrantedProviderID(provider), provider.Protocol, body)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, resolveProviderChatURL(provider, model), bytes.NewReader(wrapped))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if accept != "" {
		request.Header.Set("Accept", accept)
	} else {
		request.Header.Set("Accept", "application/json")
	}
	// 凭据只用自己的那一把。applyProviderAuth 只认 provider 配置的 apiKeySource，
	// 传空 incomingAuth 等于把「客户端带来的 Authorization」彻底排除在这条路
	// 之外：客户端的密钥漏到父网关，和父网关的凭据漏回客户端，都是这条分支要
	// 防的事。
	applyProviderAuth(request, provider, "")
	// 链路头整段替换而不是追加：客户端自带的那份不可信（见 extendGatewayHop）。
	for key, value := range extendGatewayHop(parseGatewayHop(r), s.GatewayID()) {
		request.Header.Set(key, value)
	}
	response, err := doHTTPWithTiming(ctx, &http.Client{Timeout: 0}, request)
	if response == nil {
		return nil, err
	}
	// 响应头里的网关身份是本机唯一能可靠得知「门后面是谁」的时刻：注册响应回显
	// 的是本机自己的 ID，本地也不另存父网关身份。拿不到就不写，宁可日志少一列，
	// 也不要在链路栏里填一个猜出来的 ID。
	//
	// 读完就摘掉：这个头是给网关看的，不该沿着响应继续传给最终客户端——那等于
	// 把父网关的身份公开给每一个用这个下游的人。
	if gatewayID := strings.TrimSpace(response.Header.Get(peerIdentityHeader)); gatewayID != "" {
		notePeerEgress(ctx, gatewayID)
		response.Header.Del(peerIdentityHeader)
	}
	return response, err
}

// notePeerEgress 把本次请求转发到的父网关记到请求计时器上。
//
// 挂在 requestTiming 上而不是像 ingress 那样换一个 *http.Request：发送点在很深
// 的地方（每个协议各有一个 send 闭包），而写日志用的是最外层那个 r，换出去的
// 请求传不回来。requestTiming 在 handler 入口就挂好、写日志时再读一次，正是
// 这种「路上才知道、最后才用」的值该待的地方（schedule trace 同理）。
func notePeerEgress(ctx context.Context, gatewayID string) {
	if t := requestTimingFrom(ctx); t != nil {
		t.notePeerEgress(gatewayID)
	}
}
