package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// 这条测试把父子对接的两端真的接起来：本机（子）注册到一个上游（父），
// 父把请求转给它自己的真实上游。任何一侧的协议改错了这里都会红。
func TestPeerChainEndToEnd(t *testing.T) {
	var upstreamSaw string
	realUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamSaw = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"from parent"}}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`)
	}))
	defer realUpstream.Close()

	// 父：一个 provider 指向真实上游，授权给它注册上来的下游。
	parent := newPeerTestServer(t, domain.Provider{
		ID: "parent-model", Name: "parent-model", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeAPIKey, APIKeySource: "literal:sk-parent-upstream",
		BaseURL: realUpstream.URL,
		Models:  []domain.Model{{ID: "claude-sonnet-4-5", ProviderID: "parent-model", Protocol: domain.ProtocolOpenAIChat, InMenu: true}},
	})
	parent.SetGatewayID(otherID)
	parentHTTP := httptest.NewServer(parent.Handler())
	defer parentHTTP.Close()

	// 子：自己的身份 + 一个 peer provider 指向上游。
	child := newPeerTestServer(t)
	child.SetGatewayID(gwID)

	client := newPeerClient()
	registration, err := client.register(t.Context(), parentHTTP.URL, gwID, "国内网关", "sub-gw", "a-strong-password", []string{"parent-model"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if registration.Token == "" {
		t.Fatal("no token issued")
	}
	// 父那边现在应该有这个下游的账号。
	peerUser, err := parent.userStore.UserByGatewayID(gwID)
	if err != nil || !peerUser.IsPeer() {
		t.Fatalf("parent did not create the peer account: %v %+v", err, peerUser)
	}
	// 下游能读到自己的授权清单。
	providers, err := client.providers(t.Context(), parentHTTP.URL, registration.Token)
	if err != nil {
		t.Fatalf("providers: %v", err)
	}
	if len(providers) != 1 || providers[0].ID != "parent-model" {
		t.Fatalf("providers = %+v", providers)
	}

	// 子把它记成一个 peer provider，然后走本机正常的转发路径发一次请求。
	if _, err := child.router.AddProvider(domain.Provider{
		ID: "via-parent", Name: "via-parent", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypePeer, APIKeySource: "literal:" + registration.Token,
		BaseURL: parentHTTP.URL,
		Models:  []domain.Model{{ID: "claude-sonnet-4-5", ProviderID: "via-parent", Protocol: domain.ProtocolOpenAIChat, InMenu: true}},
	}); err != nil {
		t.Fatal(err)
	}
	route, err := child.router.AddRoute(domain.Route{
		Name: "via-parent", ProviderID: "via-parent", OutputProtocol: domain.ProtocolOpenAIChat, Mode: domain.RouteModePassThrough,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := child.router.AddAPIKey(domain.APIKey{Name: "local-client", RouteID: route.ID})
	if err != nil {
		t.Fatal(err)
	}

	recorder := chatThroughChild(t, child, key.Key, `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "from parent") {
		t.Fatalf("the parent's answer must reach the client: %s", recorder.Body.String())
	}
	// 真实上游看到的是父的凭据；对端凭据一次都不该出现。
	if !strings.Contains(upstreamSaw, "sk-parent-upstream") {
		t.Fatalf("parent did not use its own credential, saw %q", upstreamSaw)
	}
	if strings.Contains(upstreamSaw, peerTokenPrefix) {
		t.Fatalf("the peer token leaked to the real upstream: %q", upstreamSaw)
	}

	// 两边都各自记了一条：父记 ingress，子记 egress。
	if !hasPeerLog(parent, gwID, "ingress") {
		t.Fatalf("parent has no ingress record for this peer: %+v", parent.logs.Query(monitor.RequestLogQuery{Page: 1, PageSize: 50, Status: "all"}).Items)
	}
	if !hasPeerLog(child, otherID, "egress") {
		t.Fatalf("child has no egress record: %+v", child.logs.Query(monitor.RequestLogQuery{Page: 1, PageSize: 50, Status: "all"}).Items)
	}
}

// 链路上出现自己时，父必须拒绝：A→B→A 的每一跳都在等下一跳。
func TestPeerChainRefusesLoopAcrossTwoGateways(t *testing.T) {
	parent := newPeerTestServer(t)
	parent.SetGatewayID(otherID)
	parentHTTP := httptest.NewServer(parent.Handler())
	defer parentHTTP.Close()

	// 直接把父自己的 ID 放进链路里：这正是 A→B→A 的第二跳会带的东西。
	client := newPeerClient()
	registration, err := client.register(t.Context(), parentHTTP.URL, gwID, "子", "sub-gw", "a-strong-password", nil)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	body := `{"payload":{"model":"m"}}`
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		parentHTTP.URL+peerEndpointPrefix+"chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+registration.Token)
	request.Header.Set(headerGatewayPath, otherID)
	request.Header.Set(headerGatewayHopCount, "1")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusLoopDetected {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("status=%d body=%s", response.StatusCode, payload)
	}
}

// Claude 协议的下游链路：下游这边走的是 /v1/messages，父网关那一侧必须把
// 报文送到 provider 的 /messages 上，而不是通用的 /chat/completions。
//
// 这条路径上父网关不能自己做 Claude 整流（stop_sequences 剪裁、thinking 写法），
// 因为"门后面的真实上游"是谁要等父网关选定 provider 才知道。
func TestPeerChainClaudePassthrough(t *testing.T) {
	var upstreamPath string
	realUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"type":"message","role":"assistant","content":[{"type":"text","text":"from claude parent"}],"usage":{"input_tokens":9,"output_tokens":4}}`)
	}))
	defer realUpstream.Close()

	parent := newPeerTestServer(t, domain.Provider{
		ID: "parent-claude", Name: "parent-claude", Protocol: domain.ProtocolClaude,
		AuthType: domain.AuthTypeAPIKey, APIKeySource: "literal:sk-parent-claude",
		BaseURL: realUpstream.URL,
		Models:  []domain.Model{{ID: "claude-sonnet-4-5", ProviderID: "parent-claude", Protocol: domain.ProtocolClaude, InMenu: true}},
	})
	parent.SetGatewayID(otherID)
	parentHTTP := httptest.NewServer(parent.Handler())
	defer parentHTTP.Close()

	child := newPeerTestServer(t)
	child.SetGatewayID(gwID)
	client := newPeerClient()
	registration, err := client.register(t.Context(), parentHTTP.URL, gwID, "国内网关", "sub-gw", "a-strong-password", []string{"parent-claude"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := child.router.AddProvider(domain.Provider{
		ID: "via-parent", Name: "via-parent", Protocol: domain.ProtocolClaude,
		AuthType: domain.AuthTypePeer, APIKeySource: "literal:" + registration.Token,
		BaseURL: parentHTTP.URL,
		Models:  []domain.Model{{ID: "claude-sonnet-4-5", ProviderID: "via-parent", Protocol: domain.ProtocolClaude, InMenu: true}},
	}); err != nil {
		t.Fatal(err)
	}
	route, err := child.router.AddRoute(domain.Route{
		Name: "via-parent", ProviderID: "via-parent", OutputProtocol: domain.ProtocolClaude, Mode: domain.RouteModePassThrough,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := child.router.AddAPIKey(domain.APIKey{Name: "local-claude-client", RouteID: route.ID})
	if err != nil {
		t.Fatal(err)
	}

	// Claude 原生入口是 /anthropic/v1/messages（server.go 的 mux 注册），
	// 不是裸的 /v1/messages：后者在本网关里根本没有这条路由。
	request := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+key.Key)
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	child.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "from claude parent") {
		t.Fatalf("the parent's answer must reach the client: %s", recorder.Body.String())
	}
	if !strings.HasSuffix(upstreamPath, "/messages") {
		t.Fatalf("the parent sent the Claude request to %q, want an /messages path", upstreamPath)
	}
	// 两边的链路标记都要有：父记 ingress，子记 egress。
	if !hasPeerLog(parent, gwID, "ingress") {
		t.Fatalf("parent has no ingress record: %+v", parent.logs.Query(monitor.RequestLogQuery{Page: 1, PageSize: 50, Status: "all"}).Items)
	}
	if !hasPeerLog(child, otherID, "egress") {
		t.Fatalf("child has no egress record: %+v", child.logs.Query(monitor.RequestLogQuery{Page: 1, PageSize: 50, Status: "all"}).Items)
	}
}
func chatThroughChild(t *testing.T, server *Server, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+key)
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func hasPeerLog(server *Server, gatewayID, direction string) bool {
	for _, item := range server.logs.Query(monitor.RequestLogQuery{Page: 1, PageSize: 50, Status: "all"}).Items {
		if item.PeerGatewayID == gatewayID && item.PeerDirection == direction {
			return true
		}
	}
	return false
}

// normalizePeerParentURL 是下游唯一的输入整理点，错一次就会拼出打不通的地址。
func TestNormalizePeerParentURL(t *testing.T) {
	cases := map[string]string{
		"https://vps1.example.com":                          "https://vps1.example.com",
		"vps1.example.com":                                  "https://vps1.example.com",
		"http://10.0.0.5:8080/":                             "http://10.0.0.5:8080",
		"https://vps1.example.com/__peer/register":          "https://vps1.example.com",
		"https://vps1.example.com/__peer/probe":             "https://vps1.example.com",
		"https://vps1.example.com/__peer/chat/completions/": "https://vps1.example.com",
	}
	for input, want := range cases {
		if got, _ := normalizePeerParentURL(input); got != want {
			t.Fatalf("normalizePeerParentURL(%q) = %q, want %q", input, got, want)
		}
	}
	if _, err := normalizePeerParentURL("   "); err == nil {
		t.Fatal("an empty parent URL must be refused")
	}
}

// 信封是上游唯一认的形状；payload 本身必须原样进去，一个字段都不能动。
func TestBuildPeerForwardBodyKeepsPayloadVerbatim(t *testing.T) {
	payload := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	body, err := buildPeerForwardBody("p1", domain.ProtocolOpenAIChat, payload)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		ProviderID string          `json:"providerId"`
		Protocol   string          `json:"protocol"`
		Payload    json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.ProviderID != "p1" {
		t.Fatalf("providerId = %q", envelope.ProviderID)
	}
	if envelope.Protocol != string(domain.ProtocolOpenAIChat) {
		t.Fatalf("protocol = %q", envelope.Protocol)
	}
	if string(envelope.Payload) != string(payload) {
		t.Fatalf("payload was rewritten: %s", envelope.Payload)
	}
	// 没有建议 provider 时不能出现空字段，否则会覆盖上游"由我按授权集挑"的判断。
	body, _ = buildPeerForwardBody("", "", payload)
	if strings.Contains(string(body), "providerId") {
		t.Fatalf("an empty suggestion must be omitted: %s", body)
	}
	if strings.Contains(string(body), `"protocol"`) {
		t.Fatalf("an empty protocol must be omitted: %s", body)
	}
	if _, err := buildPeerForwardBody("p1", domain.ProtocolOpenAIChat, []byte("not json")); err == nil {
		t.Fatal("an invalid payload must be refused locally")
	}
}
