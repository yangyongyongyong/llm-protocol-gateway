package gateway

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 父子网关透传的链路标记。
//
// 父子关系只表达 provider 的继承：A 可以把它的 provider 授权给 B，B 也可以
// 把它自己的授权给 A。所以"层级"是假的，唯一必须防的是环——A 的请求绕到 B
// 又绕回 A，每一跳都在等下一跳，直到耗尽所有连接。这里是那个闸门。
const (
	// headerGatewayPath 是已经过的网关 ID，逗号分隔，从发起方开始。
	// 用 ID 而不是主机名：主机名会变（换域名、走隧道、内网外网两套地址），
	// 而网关 ID 是稳定身份，正是判断"这是不是我自己"需要的那个值。
	headerGatewayPath = "X-Gateway-Path"
	// headerGatewayHopCount 是已经过的跳数。有它才能挡住"用不同 ID 但确实
	// 无限绕"的情形（例如一群人互相转发），以及被篡改/伪造过的链路头。
	headerGatewayHopCount = "X-Gateway-Hops"
	// headerGatewayOrigin 是发起这次透传的网关 ID，用于在两级日志里对上同一次
	// 请求——子那边记的是它收到的请求，父这边记的是同一次请求的另一半。
	headerGatewayOrigin = "X-Gateway-Origin"
)

// maxGatewayHops 是一条链路允许经过的网关数量上限。
//
// 3 是刻意的：真实场景是"内网网关 → 一个公网网关"，再往上就是少见的多级
// 中转了。给到 8 会让一个配置错误在超时前跑很久，而且每跳都占着两边的连接。
// 这个值也写进界面提示，运维配错时能直接看到限制在哪。
const maxGatewayHops = 3

// peerEndpointPrefix 是对端网关专用的接口前缀。
//
// 单独开一段路径而不是复用控制台接口，是因为鉴权模型不同：这里只认对端凭据
// （P1 的 /__peer/* 注册与透传），控制台的会话 cookie 在这里一概无效。混在
// 一起的话，任何一个控制台接口的鉴权疏漏都会变成对端入口的疏漏。
const peerEndpointPrefix = "/__peer/"

// gatewayHop 是一次请求经过的链路状态。
type gatewayHop struct {
	// Path 是已经过的网关 ID（不含自己）。
	Path []string
	// Count 是已经过的跳数，与 len(Path) 不一定相等（外部可能只带计数）。
	Count int
	// Origin 是发起方 ID，可能为空（老版本的下游）。
	Origin string
}

// parseGatewayHop 从请求头读出链路状态。头是客户端可写的，所以这里只做
// 解析，校验交给 checkGatewayHop。
func parseGatewayHop(r *http.Request) gatewayHop {
	hop := gatewayHop{}
	if r == nil {
		return hop
	}
	for _, part := range strings.Split(r.Header.Get(headerGatewayPath), ",") {
		if id := strings.TrimSpace(part); id != "" {
			hop.Path = append(hop.Path, id)
		}
	}
	if raw := strings.TrimSpace(r.Header.Get(headerGatewayHopCount)); raw != "" {
		if count, err := strconv.Atoi(raw); err == nil && count > 0 {
			hop.Count = count
		}
	}
	// 计数缺失时按路径长度推断：老版本下游可能只带了路径。
	if hop.Count < len(hop.Path) {
		hop.Count = len(hop.Path)
	}
	hop.Origin = strings.TrimSpace(r.Header.Get(headerGatewayOrigin))
	return hop
}

// loopRefusal 描述一次被拒绝的环路请求。它是要给运维看的，所以带上完整链路。
type loopRefusal struct {
	// Reason 是给界面的短句。
	Reason string
	// Chain 是涉及到的网关顺序，用于日志。
	Chain string
}

// checkGatewayHop 判断这次请求是否构成环或超出跳数上限。
//
// selfID 是本网关身份；为空时（身份还没初始化）放行——宁可漏掉一次环路
// 检测，也不要在身份未知时拒绝所有透传流量。
func checkGatewayHop(hop gatewayHop, selfID string) (loopRefusal, bool) {
	selfID = strings.TrimSpace(selfID)
	if selfID != "" {
		for _, id := range hop.Path {
			if strings.EqualFold(id, selfID) {
				return loopRefusal{
					Reason: "检测到网关环路：这次请求已经过本网关",
					Chain:  strings.Join(append(append([]string{}, hop.Path...), selfID), " → "),
				}, true
			}
		}
	}
	if hop.Count >= maxGatewayHops {
		chain := strings.Join(hop.Path, " → ")
		if chain == "" {
			chain = fmt.Sprintf("%d 跳", hop.Count)
		}
		return loopRefusal{
			Reason: fmt.Sprintf("网关链路超过 %d 跳上限", maxGatewayHops),
			Chain:  chain,
		}, true
	}
	return loopRefusal{}, false
}

// extendGatewayHop 返回转发到上游时要带的链路头。
//
// 写的是"加上自己"之后的链路，所以上游看到自己的直接下游在最后一位。
// 注意它把原始请求头里的值整个替换掉，而不是追加：客户端自带的那份不可信
// （见 parseGatewayHop 的说明），留着它只会让上游的环检测被投毒。
func extendGatewayHop(hop gatewayHop, selfID string) map[string]string {
	selfID = strings.TrimSpace(selfID)
	path := make([]string, 0, len(hop.Path)+1)
	path = append(path, hop.Path...)
	if selfID != "" {
		path = append(path, selfID)
	}
	// origin 缺失时不能填自己：自己不是发起方，填上去会让上游把链路中段当成
	// 起点，两级日志就对不上了。链路的第一位才是真正的发起方；连路径都没有
	// （例如上游透传下来时没带 origin 的老版本），才退回到自己。
	origin := hop.Origin
	if origin == "" && len(hop.Path) > 0 {
		origin = hop.Path[0]
	}
	if origin == "" {
		origin = selfID
	}
	headers := map[string]string{
		headerGatewayPath:     strings.Join(path, ","),
		headerGatewayHopCount: strconv.Itoa(len(path)),
	}
	if origin != "" {
		headers[headerGatewayOrigin] = origin
	}
	return headers
}

// gatewayIdentityFromR 返回本网关身份。空串表示启动时还没注入身份，此时
// 环路检测一律放行——宁可漏掉一次，也不要因为一个没初始化的字段拒掉所有
// 透传流量。
func (s *Server) gatewayIdentityFromR(r *http.Request) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s.gatewayID)
}

// withGatewayLoopGuard 拦截构成环路或超过跳数上限的透传请求。
//
// 挂在 mux 外层而不是每个 handler 里：模型端点和 /__peer/* 都必须经过，
// 漏掉一个就留了一条能把自己绕死的路径。没有链路头的普通请求只是一次
// 两次字符串比较，成本可以忽略。
func (s *Server) withGatewayLoopGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r == nil || !carriesModelTraffic(requestPathOf(r)) {
			next.ServeHTTP(w, r)
			return
		}
		hop := parseGatewayHop(r)
		if len(hop.Path) == 0 && hop.Count == 0 {
			next.ServeHTTP(w, r)
			return
		}
		if refusal, looped := checkGatewayHop(hop, s.gatewayIdentityFromR(r)); looped {
			s.rejectGatewayLoop(w, r, refusal)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// carriesModelTraffic 判断这条路径是否承载模型请求——只有这些路径需要做环
// 路检测。
//
// 两类：直接打到本机的模型协议入口，以及 /__peer/* 这个给对端网关用的入口。
// 后者是关键：子网关送来的请求正是从这里进来，如果按"控制台路径一律跳过"
// 处理，最该拦的那条路反而没有闸门。
//
// 控制台的页面、静态资源和 /__state 之类的接口一律跳过：它们不参与透传，
// 而用户浏览器里的插件可能随手塞一个同名头，那不该变成 508。
func carriesModelTraffic(path string) bool {
	if strings.HasPrefix(path, peerEndpointPrefix) {
		return true
	}
	return isModelAPIPath(path)
}

// isModelAPIPath 判断这条路径是不是模型协议入口（而不是控制台接口）。
// 透传只会发生在这些路径上，控制台路径即使带了链路头也不该被当成环路。
func isModelAPIPath(path string) bool {
	for _, prefix := range []string{
		"/v1/", "/v1beta/", "/openai/", "/anthropic/", "/chat/completions", "/models",
	} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// rejectGatewayLoop 写 508 并落一条可查的日志。
//
// 状态码用 508 Loop Detected：它是为这件事存在的（WebDAV 引入，语义就是
// "服务器在处理请求时检测到无限循环"），比 400/502 都准确——运维在日志里
// 看到 508 就知道该去看网关互联页面，而不是去查上游。
func (s *Server) rejectGatewayLoop(w http.ResponseWriter, r *http.Request, refusal loopRefusal) {
	message := fmt.Sprintf("%s（链路：%s）", refusal.Reason, refusal.Chain)
	s.logs.AddApp("warn", "gateway loop refused", message)
	s.recordRequestLogFromRequest(r, time.Now(), domain.APIKey{}, false, "", "", "",
		"rejected", "gateway loop", requestPathOf(r), http.StatusLoopDetected, TokenUsage{}, nil, []byte(message))
	writeOpenAIError(w, http.StatusLoopDetected, message)
}

// requestPathOf 是给小工具函数用的空安全取值。
func requestPathOf(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	return r.URL.Path
}
