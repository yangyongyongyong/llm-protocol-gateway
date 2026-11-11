package gateway

import (
	"context"
	"net/http"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// 父子网关透传在请求日志里的落点。
//
// 这三个值描述的是"这次请求在链路里的位置"：谁送进来的、本机又转给了谁。
// 纯本地请求三者都为空，日志和引入父子功能之前一字不差。
//
// 它们随请求走 context 而不是穿进 recordRequestLogEx 的参数列表：那条签名
// 已经有十几个参数，而且只在透传这一条路上才会有值——每加一个可选参数都
// 要改十几处调用点，还容易把顺序搞错。
type peerTraceKey struct{}

// peerTrace 是一次请求的链路标记。
type peerTrace struct {
	// Ingress 是把请求送进来的对端网关 ID。
	Ingress string
	// Egress 是本机把请求转发出去的对端网关 ID。
	Egress string
	// Origin 是发起方网关 ID（X-Gateway-Origin 的值）。
	Origin string
	// Path 是这次请求已经过的网关 ID 链（X-Gateway-Path，从发起方开始，不含
	// 本机）。Origin 只指向起点，Path 能回答"中间到底绕了几道"——三级及
	// 以上链路里它才是排障时真正要看的。
	Path []string
}

// peerGatewayID 返回这次请求要对账的那个对端网关。
//
// 有 Ingress 时优先用它：对上游（父）来说，关心的是"哪个子网关送来的流量"，
// 即使这次请求最终又被本机转发给了另一个对端，那条链的起点仍然是 Ingress。
// 两者都没有就是纯本地请求，返回空串。
func (t peerTrace) peerGatewayID() string {
	if id := strings.TrimSpace(t.Ingress); id != "" {
		return id
	}
	return strings.TrimSpace(t.Egress)
}

// peerDirection 说明 PeerGatewayID 在这条请求里扮演什么角色。
//
// 上游看到的是 ingress（子网关把流量送进来），下游看到的是 egress（本机把
// 流量送出去）。两级都各自记一条，"这个子网关用了多少"在上游算，而"我借道
// 上游用了多少"在下游算。
func (t peerTrace) peerDirection() string {
	if strings.TrimSpace(t.Ingress) != "" {
		return "ingress"
	}
	if strings.TrimSpace(t.Egress) != "" {
		return "egress"
	}
	return ""
}

func (t peerTrace) isEmpty() bool {
	return strings.TrimSpace(t.Ingress) == "" && strings.TrimSpace(t.Egress) == "" &&
		strings.TrimSpace(t.Origin) == "" && len(t.Path) == 0
}

func withPeerTrace(ctx context.Context, trace peerTrace) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, peerTraceKey{}, trace)
}

// peerTraceFrom 取回链路标记；没有就返回零值。
func peerTraceFrom(ctx context.Context) peerTrace {
	if ctx == nil {
		return peerTrace{}
	}
	if trace, ok := ctx.Value(peerTraceKey{}).(peerTrace); ok {
		return trace
	}
	return peerTrace{}
}

// withIngressPeer 把"这次请求来自哪个对端网关"记到请求上。
//
// 只在 /__peer/* 的对端鉴权通过之后调用：链路头是外部可写的，不能拿它当
// 身份——那会让任何人都能把自己标成某个子网关，从而在上游的用量里冒充别人。
// Ingress 必须来自校验过的 peer 凭据。
func withIngressPeer(r *http.Request, peerGatewayID string) *http.Request {
	if r == nil {
		return r
	}
	trace := peerTraceFrom(r.Context())
	trace.Ingress = strings.TrimSpace(peerGatewayID)
	// Origin 已经由 extendGatewayHop 写在头里了，这里补的是"本机看到的值"。
	if trace.Origin == "" {
		trace.Origin = strings.TrimSpace(r.Header.Get(headerGatewayOrigin))
	}
	return r.WithContext(withPeerTrace(r.Context(), trace))
}

// peerTraceFor 汇总一条请求的全部链路标记。
//
// Ingress 来自请求本身（校验过的对端凭据，见 withIngressPeer）；Egress 来自
// requestTiming，因为它是「路上才知道」的：父网关的身份要到它的响应头回来时
// 才拿得到（sendPeerDoor），而那时已经在每个协议各自的 send 闭包深处，换出去
// 的 *http.Request 传不回最外层的写日志处。requestTiming 在 handler 入口挂好、
// 写日志时再读，正是这种值该待的地方。两个来源都空就是纯本地请求。
//
// Path 直接从请求头解析而不是走 context：链路头随请求到达，写日志时 r 还在
// 手边，没必要再多一层传递。但它只在确认是透传流量（有校验过的 ingress 或
// egress）时才记——链路头是外部可写的，本地请求随手带一个同名头（浏览器插件
// 就干得出来）不能凭空造出一条链路日志。两级链路里 Ingress/Origin 已经足够，
// Path 是给三级及以上的排障看的。
func peerTraceFor(r *http.Request, timing *requestTiming) peerTrace {
	var trace peerTrace
	if r != nil {
		trace = peerTraceFrom(r.Context())
	}
	if id := timing.peerEgressFrom(); id != "" {
		trace.Egress = id
		if trace.Origin == "" && r != nil {
			trace.Origin = strings.TrimSpace(r.Header.Get(headerGatewayOrigin))
		}
	}
	if r != nil && (trace.Ingress != "" || trace.Egress != "") && len(trace.Path) == 0 {
		trace.Path = parseGatewayHop(r).Path
	}
	return trace
}

// applyPeerTrace 把链路标记填进日志条目。空 trace 是零操作，所以调用点不必
// 先判断——纯本地请求的路径和以前完全一致。
func applyPeerTrace(entry *monitor.RequestLog, trace peerTrace) {
	if entry == nil || trace.isEmpty() {
		return
	}
	entry.Ingress = strings.TrimSpace(trace.Ingress)
	entry.Egress = strings.TrimSpace(trace.Egress)
	entry.PeerGatewayID = trace.peerGatewayID()
	entry.PeerDirection = trace.peerDirection()
	entry.PeerOrigin = strings.TrimSpace(trace.Origin)
	if len(trace.Path) > 0 {
		entry.PeerPath = strings.Join(trace.Path, ",")
	}
}
