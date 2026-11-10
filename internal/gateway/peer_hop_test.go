package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// gwID / otherID 都是 IsWellFormed 认得的形状，方便对着真实头值调试。
const (
	gwID    = "gw_0123456789abcdef"
	otherID = "gw_fedcba9876543210"
)

func newHopTestServer(t *testing.T, selfID string) *Server {
	t.Helper()
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	server.SetGatewayID(selfID)
	return server
}

// 链路里出现自己就是环：这正是 A→B→A 的形态，也包含 A→A 的自指。
func TestGatewayLoopDetectionCatchesSelfInPath(t *testing.T) {
	cases := []struct {
		name string
		hop  gatewayHop
	}{
		{"只有自己（自指）", gatewayHop{Path: []string{gwID}, Count: 1}},
		{"自己是最后一跳", gatewayHop{Path: []string{otherID, gwID}, Count: 2}},
		{"自己是第一跳", gatewayHop{Path: []string{gwID, otherID}, Count: 2}},
		{"多级链里的自己", gatewayHop{Path: []string{otherID, otherID, gwID}, Count: 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, looped := checkGatewayHop(tc.hop, gwID); !looped {
				t.Fatalf("hop %+v must be refused as a loop", tc.hop)
			}
		})
	}
}

// 大小写不同也要认出来：ID 在 URL/头里转过一道手就可能被改写大小写，
// 漏判等于放行一个环。
func TestGatewayLoopDetectionIgnoresCase(t *testing.T) {
	hop := gatewayHop{Path: []string{strings.ToUpper(gwID)}, Count: 1}
	if _, looped := checkGatewayHop(hop, gwID); !looped {
		t.Fatal("a case-changed id is still the same gateway")
	}
}

// 别人的链路要放行：这是正常的多级转发。
func TestGatewayLoopDetectionAllowsOtherGateways(t *testing.T) {
	hop := gatewayHop{Path: []string{otherID}, Count: 1}
	if refusal, looped := checkGatewayHop(hop, gwID); looped {
		t.Fatalf("a foreign path must pass, got %+v", refusal)
	}
}

// 跳数上限：用不同 ID 无限对转时靠它兜底，也是被伪造的链路头的最后一道闸。
func TestGatewayLoopDetectionCapsHopCount(t *testing.T) {
	atLimit := gatewayHop{Path: []string{"gw_0000000000000001", "gw_0000000000000002", "gw_0000000000000003"}, Count: maxGatewayHops}
	refusal, looped := checkGatewayHop(atLimit, gwID)
	if !looped {
		t.Fatalf("a chain at %d hops must be refused", maxGatewayHops)
	}
	if !strings.Contains(refusal.Reason, "跳") {
		t.Fatalf("refusal must name the hop limit, got %q", refusal.Reason)
	}
	below := gatewayHop{Path: []string{"gw_0000000000000001"}, Count: maxGatewayHops - 1}
	if _, looped := checkGatewayHop(below, gwID); looped {
		t.Fatal("a chain below the limit must pass")
	}
}

// 身份未知时一律放行：初始化失败不该让所有透传流量都挂掉。
func TestGatewayLoopDetectionPassesWhenIdentityUnknown(t *testing.T) {
	hop := gatewayHop{Path: []string{gwID}, Count: 1}
	if _, looped := checkGatewayHop(hop, ""); looped {
		t.Fatal("without a self id there is nothing to compare against")
	}
}

// 头解析：逗号分隔、允许空白、空段忽略；计数缺失时按路径长度推断。
func TestParseGatewayHopReadsHeaders(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Header.Set(headerGatewayPath, " gw_aaaaaaaaaaaaaaaa , ,gw_bbbbbbbbbbbbbbbb ")
	hop := parseGatewayHop(request)
	if len(hop.Path) != 2 || hop.Path[0] != "gw_aaaaaaaaaaaaaaaa" || hop.Path[1] != "gw_bbbbbbbbbbbbbbbb" {
		t.Fatalf("Path = %v", hop.Path)
	}
	if hop.Count != 2 {
		t.Fatalf("Count = %d, want 2 inferred from the path", hop.Count)
	}

	// 显式计数比路径长时以计数为准：伪造的短路径不能绕过跳数上限。
	explicit := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	explicit.Header.Set(headerGatewayPath, "gw_aaaaaaaaaaaaaaaa")
	explicit.Header.Set(headerGatewayHopCount, "7")
	if got := parseGatewayHop(explicit).Count; got != 7 {
		t.Fatalf("Count = %d, want the explicit 7", got)
	}

	// 计数比路径短时取路径长度：少报不能变相放宽限制。
	short := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	short.Header.Set(headerGatewayPath, "gw_aaaaaaaaaaaaaaaa,gw_bbbbbbbbbbbbbbbb")
	short.Header.Set(headerGatewayHopCount, "1")
	if got := parseGatewayHop(short).Count; got != 2 {
		t.Fatalf("Count = %d, want the path length 2", got)
	}
}

// 转发时把"加上自己"的链路写出去，并**替换**掉客户端自带的那份：
// 留着它等于让外部能给上游投毒。
func TestExtendGatewayHopAppendsSelfAndReplacesClientHeaders(t *testing.T) {
	incoming := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	incoming.Header.Set(headerGatewayPath, "gw_aaaaaaaaaaaaaaaa")
	incoming.Header.Set(headerGatewayHopCount, "5") // 客户端瞎报的
	hop := parseGatewayHop(incoming)

	headers := extendGatewayHop(hop, gwID)
	if got, want := headers[headerGatewayPath], "gw_aaaaaaaaaaaaaaaa,"+gwID; got != want {
		t.Fatalf("%s = %q, want %q", headerGatewayPath, got, want)
	}
	if got := headers[headerGatewayHopCount]; got != "2" {
		t.Fatalf("%s = %q, want the real 2", headerGatewayHopCount, got)
	}
	if got := headers[headerGatewayOrigin]; got != "gw_aaaaaaaaaaaaaaaa" {
		t.Fatalf("%s = %q, want the original origin", headerGatewayOrigin, got)
	}

	// 发起方自己没有 origin 时，把自己写成 origin。
	fresh := extendGatewayHop(gatewayHop{}, gwID)
	if fresh[headerGatewayOrigin] != gwID {
		t.Fatalf("origin = %q, want self", fresh[headerGatewayOrigin])
	}
	if fresh[headerGatewayPath] != gwID {
		t.Fatalf("path = %q, want just self", fresh[headerGatewayPath])
	}
}

// 端到端：环路请求被中间件拦下，回 508，并且没有落到 handler。
func TestLoopGuardMiddlewareRefusesAndDoesNotReachTheHandler(t *testing.T) {
	server := newHopTestServer(t, gwID)
	reached := false
	guarded := server.withGatewayLoopGuard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Header.Set(headerGatewayPath, otherID+","+gwID)
	recorder := httptest.NewRecorder()
	guarded.ServeHTTP(recorder, request)

	if reached {
		t.Fatal("a loop must not reach the handler")
	}
	if recorder.Code != http.StatusLoopDetected {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusLoopDetected)
	}
	// 报文要说清是哪条链，否则运维只能看到一句 508 去猜。
	if body := recorder.Body.String(); !strings.Contains(body, otherID) || !strings.Contains(body, gwID) {
		t.Fatalf("refusal must name the chain, got %s", body)
	}
}

// 环路请求必须留一条可查的日志：只回 508 不留痕，运维会去查上游和网络。
func TestLoopGuardRecordsARequestLog(t *testing.T) {
	server := newHopTestServer(t, gwID)
	guarded := server.withGatewayLoopGuard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Header.Set(headerGatewayPath, gwID)
	guarded.ServeHTTP(httptest.NewRecorder(), request)

	logs := server.logs.Query(monitor.RequestLogQuery{Page: 1, PageSize: 50, Status: "all"})
	if len(logs.Items) == 0 {
		t.Fatal("a refused loop must leave a request log")
	}
	found := false
	for _, item := range logs.Items {
		if item.Status == http.StatusLoopDetected && strings.Contains(item.ErrorDescription, "环路") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no 508 log with a loop description, got %+v", logs.Items)
	}
}

// 普通请求（没有任何链路头）必须原样放行：这个中间件不能给正常流量加行为。
func TestLoopGuardLeavesPlainTrafficAlone(t *testing.T) {
	server := newHopTestServer(t, gwID)
	reached := false
	guarded := server.withGatewayLoopGuard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))
	guarded.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if !reached {
		t.Fatal("plain traffic must reach the handler")
	}
}

// /__peer/* 是对端网关送流量的入口，必须同样受环路闸门约束。
//
// 这里专门盯一个曾经写错的地方：初版的判据是
// `!strings.HasPrefix(path, "/__") && !isModelAPIPath(path)`，Go 的 && 优先级
// 让 /__ 前缀的控制台路径反而进了检测分支，而真正该拦的 /__peer/* 因为
// 不以 /v1/ 之类开头被漏在了外面——最该有闸门的那条路恰好没有。
func TestLoopGuardCoversPeerEndpoint(t *testing.T) {
	server := newHopTestServer(t, gwID)
	guarded := server.withGatewayLoopGuard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a loop arriving through /__peer/ must not reach the handler")
	}))
	request := httptest.NewRequest(http.MethodPost, peerEndpointPrefix+"chat/completions", nil)
	request.Header.Set(headerGatewayPath, otherID+","+gwID)
	recorder := httptest.NewRecorder()
	guarded.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusLoopDetected {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusLoopDetected)
	}
}

// 控制台路径即使带着链路头也不检测：浏览器插件塞进来的同名头不该变成 508。
func TestLoopGuardSkipsConsolePaths(t *testing.T) {
	server := newHopTestServer(t, gwID)
	for _, path := range []string{"/__state", "/__logs", "/", "/assets/app.js"} {
		reached := false
		guarded := server.withGatewayLoopGuard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			reached = true
		}))
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set(headerGatewayPath, gwID)
		guarded.ServeHTTP(httptest.NewRecorder(), request)
		if !reached {
			t.Fatalf("%s must not be treated as passthrough traffic", path)
		}
	}
}
