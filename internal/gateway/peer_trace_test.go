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
		entry.PeerDirection != "" || entry.PeerOrigin != "" {
		t.Fatalf("a local request must stay clean, got %+v", entry)
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
