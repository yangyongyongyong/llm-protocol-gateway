package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// 纯本地请求：三个字段都必须留空。这不只是"没数据"，而是界面判断要不要画
// 链路的依据——一旦本地请求被写上任何链路标记，日志里就会出现一条假的
// "来自某网关"的记录，运维会去查一个不存在的对端。
func TestPeerTraceLeavesLocalRequestsClean(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	entry := monitor.RequestLog{}
	applyPeerTrace(&entry, peerTraceFor(request, newRequestTiming(time.Now())))
	if entry.Ingress != "" || entry.Egress != "" || entry.PeerGatewayID != "" ||
		entry.PeerDirection != "" || entry.PeerOrigin != "" || entry.PeerPath != "" {
		t.Fatalf("a local request must stay clean, got %+v", entry)
	}
}

// 链路头是外部可写的（浏览器插件塞同名头是真实发生过的事），本地请求带着它
// 也不能凭空造出一条链路日志——PeerPath 必须和其它链路字段一样，
// 只在确认过对端身份的流量上才有值。
func TestPeerTraceIgnoresSpoofedPathOnLocalTraffic(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	request.Header.Set(headerGatewayPath, otherID+",gw_9999999999999999")
	entry := monitor.RequestLog{}
	applyPeerTrace(&entry, peerTraceFor(request, newRequestTiming(time.Now())))
	if entry.PeerPath != "" {
		t.Fatalf("a spoofed path on local traffic must not be logged, got %q", entry.PeerPath)
	}
}

// 上游方向：子网关把请求送进来，PeerGatewayID 是那个子网关，方向是 ingress。
func TestPeerTraceMarksIngress(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	request.Header.Set(headerGatewayOrigin, otherID)
	request = withIngressPeer(request, gwID)

	entry := monitor.RequestLog{}
	applyPeerTrace(&entry, peerTraceFrom(request.Context()))
	if entry.Ingress != gwID {
		t.Fatalf("Ingress = %q, want %q", entry.Ingress, gwID)
	}
	if entry.Egress != "" {
		t.Fatalf("Egress must stay empty on the ingress side, got %q", entry.Egress)
	}
	if entry.PeerGatewayID != gwID || entry.PeerDirection != "ingress" {
		t.Fatalf("peer columns = %q/%q", entry.PeerGatewayID, entry.PeerDirection)
	}
	if entry.PeerOrigin != otherID {
		t.Fatalf("PeerOrigin = %q, want the origin from the header", entry.PeerOrigin)
	}
}

// 下游方向：本机把请求转给父网关，PeerGatewayID 是那个父网关，方向是 egress。
//
// Egress 只在父网关回包头时才拿得到（见 sendPeerDoor），所以它挂在
// requestTiming 上而不是请求的 context 上。
func TestPeerTraceMarksEgress(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	timing := newRequestTiming(time.Now())
	timing.notePeerEgress(otherID)

	entry := monitor.RequestLog{}
	applyPeerTrace(&entry, peerTraceFor(request, timing))
	if entry.Egress != otherID || entry.Ingress != "" {
		t.Fatalf("Ingress/Egress = %q/%q", entry.Ingress, entry.Egress)
	}
	if entry.PeerGatewayID != otherID || entry.PeerDirection != "egress" {
		t.Fatalf("peer columns = %q/%q", entry.PeerGatewayID, entry.PeerDirection)
	}
	// 没有 X-Gateway-Origin 时，发起方就是本机自己——由 extendGatewayHop 的
	// 语义决定，这里不重复推断，只保证不会把对端当成发起方。
	if entry.PeerOrigin == otherID {
		t.Fatal("the egress peer must not be recorded as the origin")
	}
}

// 中间跳：既有 ingress 又有 egress 时，对账对象取 ingress（上游更关心哪个子
// 网关送来的），但同时保留 egress，界面才能画出完整的一段。
func TestPeerTracePrefersIngressForAccounting(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	request = withIngressPeer(request, "gw_1111111111111111")
	timing := newRequestTiming(time.Now())
	timing.notePeerEgress("gw_2222222222222222")

	entry := monitor.RequestLog{}
	applyPeerTrace(&entry, peerTraceFor(request, timing))
	if entry.Ingress != "gw_1111111111111111" || entry.Egress != "gw_2222222222222222" {
		t.Fatalf("both sides must be kept, got %+v", entry)
	}
	if entry.PeerGatewayID != "gw_1111111111111111" || entry.PeerDirection != "ingress" {
		t.Fatalf("accounting must point at the ingress side, got %q/%q", entry.PeerGatewayID, entry.PeerDirection)
	}
}

// 三级链路的中间跳要留下完整路径：只有 Ingress/Origin 时排障的人只知道两端，
// 不知道中间还绕了几道。Path 来自 X-Gateway-Path（发起方开始、不含本机），
// ingress 与 egress 方向都要记。
func TestPeerTraceRecordsFullPath(t *testing.T) {
	// ingress 方向：请求已经过 origin → middle，中间跳在本机看到这条头。
	in := httptest.NewRequest(http.MethodPost, "/__peer/chat/completions", nil)
	in.Header.Set(headerGatewayPath, otherID+",gw_mmmmmmmmmmmmmmmm")
	in.Header.Set(headerGatewayOrigin, otherID)
	in = withIngressPeer(in, gwID)
	entry := monitor.RequestLog{}
	applyPeerTrace(&entry, peerTraceFor(in, newRequestTiming(time.Now())))
	if entry.PeerPath != otherID+",gw_mmmmmmmmmmmmmmmm" {
		t.Fatalf("ingress PeerPath = %q, want the full chain", entry.PeerPath)
	}

	// egress 方向：本机是发起方，头里只有自己；转给父网关后路径同样要留。
	out := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	out.Header.Set(headerGatewayPath, gwID)
	timing := newRequestTiming(time.Now())
	timing.notePeerEgress(otherID)
	entry = monitor.RequestLog{}
	applyPeerTrace(&entry, peerTraceFor(out, timing))
	if entry.PeerPath != gwID {
		t.Fatalf("egress PeerPath = %q, want the self path", entry.PeerPath)
	}
}

// nil 请求不能让整条日志链路崩：拒绝路径上可能还没有构造出请求。
func TestPeerTraceHelpersTolerateNil(t *testing.T) {
	if withIngressPeer(nil, gwID) != nil {
		t.Fatal("withIngressPeer(nil) must return nil")
	}
	applyPeerTrace(nil, peerTrace{Ingress: gwID}) // 不 panic 即可
	if got := peerTraceFrom(nil); !got.isEmpty() {
		t.Fatalf("peerTraceFrom(nil) = %+v", got)
	}
	if got := peerTraceFor(nil, nil); !got.isEmpty() {
		t.Fatalf("peerTraceFor(nil, nil) = %+v", got)
	}
	// timing 为 nil 时不能 panic：有些合成日志路径根本没有计时器。
	var noTiming *requestTiming
	noTiming.notePeerEgress(gwID)
	if noTiming.peerEgressFrom() != "" {
		t.Fatal("a nil timing must report no egress peer")
	}
}
