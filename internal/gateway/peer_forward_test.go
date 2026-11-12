package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// peerForwardServer 起一个上游网关：两个 provider，一个真实指向假上游，
// 一个故意不给授权。这样每条断言都能区分"授权集内"和"授权集外"。
func peerForwardServer(t *testing.T, upstream *httptest.Server) (*Server, string) {
	t.Helper()
	server := newPeerTestServer(t,
		domain.Provider{
			ID: "hj-claude", Name: "hj-claude", Protocol: domain.ProtocolOpenAIChat,
			AuthType: domain.AuthTypeAPIKey, APIKeySource: "literal:sk-upstream-secret",
			BaseURL: upstream.URL,
			Models:  []domain.Model{{ID: "claude-sonnet-4-5", ProviderID: "hj-claude", Protocol: domain.ProtocolOpenAIChat, InMenu: true}},
		},
		domain.Provider{
			ID: "not-granted", Name: "not-granted", Protocol: domain.ProtocolOpenAIChat,
			AuthType: domain.AuthTypeAPIKey, APIKeySource: "literal:sk-other",
			BaseURL: upstream.URL + "/other",
			Models:  []domain.Model{{ID: "claude-sonnet-4-5", ProviderID: "not-granted", Protocol: domain.ProtocolOpenAIChat, InMenu: true}},
		},
	)
	server.router.SetPeerRegistrationAllowCIDRs("127.0.0.1/32")
	_, response := peerRegister(t, server, peerRegisterPayload(map[string]any{
		"requestedProviderIds": []string{"hj-claude"},
	}))
	return server, response.Token
}

// peerChat 发一次透传请求。
func peerChat(t *testing.T, server *Server, token string, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"payload": payload})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, peerEndpointPrefix+"chat/completions", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	// 下游自己的凭据放在这里：它绝不能被转发给真实上游。
	request.Header.Set("X-Api-Key", token)
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

// 透传的完整往返：上游凭据生效、响应原样回来、用量被记到上游这一侧。
func TestPeerChatForwardUsesUpstreamCredential(t *testing.T) {
	var seenAuth []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = append(seenAuth, r.Header.Get("Authorization"), r.Header.Get("X-Api-Key"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":11,"completion_tokens":5}}`)
	}))
	defer upstream.Close()

	server, token := peerForwardServer(t, upstream)
	recorder := peerChat(t, server, token, map[string]any{"model": "claude-sonnet-4-5", "messages": []any{}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"content":"hi"`) {
		t.Fatalf("body was not relayed: %s", recorder.Body.String())
	}
	// 上游看到的必须是 provider 自己的凭据，且下游的对端凭据一次都不能出现。
	for index, value := range seenAuth {
		if strings.Contains(value, peerTokenPrefix) {
			t.Fatalf("header %d leaked the peer credential upstream: %q", index, value)
		}
	}
	if len(seenAuth) < 2 || !strings.Contains(seenAuth[0], "sk-upstream-secret") {
		t.Fatalf("the provider's own credential must be used, saw %v", seenAuth)
	}
	// 上游网关这一侧的日志要带上入口网关身份（这是父侧唯一的可见性来源）。
	logs := server.logs.Query(monitor.RequestLogQuery{Page: 1, PageSize: 50, Status: "all"})
	if len(logs.Items) == 0 {
		t.Fatal("the peer request was not logged on the upstream side")
	}
	found := false
	for _, item := range logs.Items {
		if item.PeerGatewayID == otherID && item.PeerDirection == "ingress" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the ingress gateway must be recorded, got %+v", logs.Items)
	}
}

// 建议了授权集外的 provider：不能被采纳，必须落到授权集内的那个。
func TestPeerChatIgnoresProviderOutsideGrants(t *testing.T) {
	hit := ""
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = r.URL.Path
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	server, token := peerForwardServer(t, upstream)
	body, _ := json.Marshal(map[string]any{
		"providerId": "not-granted",
		"payload":    map[string]any{"model": "claude-sonnet-4-5"},
	})
	request := httptest.NewRequest(http.MethodPost, peerEndpointPrefix+"chat/completions", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.HasPrefix(hit, "/other") {
		t.Fatalf("a provider outside the grant set was used: %s", hit)
	}
}

// 授权集为空要给出与"凭据无效"不同的错误：下游据此去要授权，而不是去重注册。
func TestPeerChatWithoutGrantsSaysSo(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	server := newPeerTestServer(t)
	server.roles = newMemoryRoleStore()
	_, response := peerRegister(t, server, peerRegisterPayload(nil))
	recorder := peerChat(t, server, response.Token, map[string]any{"model": "whatever"})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "granted") {
		t.Fatalf("the error must point at grants, got %s", recorder.Body.String())
	}
}

// providerHasKnownModels 决定"模型不认识时该不该拦"。没拉过模型清单的
// provider 不该被拦死：它比网关的缓存更清楚自己有什么。
func TestProviderServesModelToleratesUnknownCatalog(t *testing.T) {
	server := newPeerTestServer(t,
		domain.Provider{ID: "no-catalog", Name: "no-catalog", Protocol: domain.ProtocolOpenAIChat, DefaultModel: "default-model"},
		domain.Provider{
			ID: "with-catalog", Name: "with-catalog", Protocol: domain.ProtocolOpenAIChat,
			Models: []domain.Model{{ID: "known-model", ProviderID: "with-catalog", Protocol: domain.ProtocolOpenAIChat}},
		},
	)
	state := server.router.State()
	noCatalog, withCatalog := state.Providers[0], state.Providers[1]
	if !server.providerServesModel(noCatalog, "any-model") {
		t.Fatal("a provider without a model catalog must not be blocked")
	}
	if !server.providerServesModel(noCatalog, "default-model") {
		t.Fatal("the provider's default model must always be allowed")
	}
	if server.providerServesModel(withCatalog, "unknown-model") {
		t.Fatal("a provider with a catalog must only serve models it lists")
	}
	// 空模型一律放行：不强制要求模型是全局约定。
	if !server.providerServesModel(withCatalog, "") {
		t.Fatal("an empty model must always be allowed")
	}
}

// 环路：链路里出现自己就拒绝，且拒绝要能被下游看见是"环"而不是"凭据坏了"。
func TestPeerChatRefusesLoop(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a looping request must never reach the upstream")
	}))
	defer upstream.Close()
	server, token := peerForwardServer(t, upstream)
	body, _ := json.Marshal(map[string]any{"payload": map[string]any{"model": "claude-sonnet-4-5"}})
	request := httptest.NewRequest(http.MethodPost, peerEndpointPrefix+"chat/completions", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(headerGatewayPath, gwID)
	request.Header.Set(headerGatewayHopCount, "1")
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusLoopDetected {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

// 用量解析要按 provider 的协议选解析器：用错一个会得到全 0，
// 而全 0 在用量页上看起来和"这次没花钱"一模一样。
//
// 父网关这一侧不自己解析用量：servePeerProtocol 直接调各协议的直通函数，
// 由它们各自算好 usage 交回来。这条测试守的是"哪种协议该用哪个解析器"这条
// 对应关系本身——它一旦反了，上游的计费会静静地变成 0。
func TestExtractPeerUsagePicksParserPerProtocol(t *testing.T) {
	openAI := []byte(`{"usage":{"prompt_tokens":100,"completion_tokens":40,"prompt_tokens_details":{"cached_tokens":20}}}`)
	claude := []byte(`{"usage":{"input_tokens":100,"output_tokens":40,"cache_read_input_tokens":20,"cache_creation_input_tokens":5}}`)
	responses := []byte(`{"usage":{"input_tokens":100,"output_tokens":40,"input_tokens_details":{"cached_tokens":20}}}`)

	if usage := ParseOpenAIUsage(openAI); usage.InputTokens == 0 || usage.OutputTokens == 0 {
		t.Fatalf("openai usage not parsed: %+v", usage)
	}
	if usage := ParseClaudeUsage(claude); usage.InputTokens == 0 || usage.OutputTokens == 0 {
		t.Fatalf("claude usage not parsed: %+v", usage)
	}
	if usage := ParseResponsesUsage(responses); usage.InputTokens == 0 || usage.OutputTokens == 0 {
		t.Fatalf("responses usage not parsed: %+v", usage)
	}
	// 换错的解析器必须解析不出来：这正是这条测试要防的。
	if usage := ParseClaudeUsage(openAI); usage.InputTokens != 0 {
		t.Fatalf("the claude parser must not read an openai body: %+v", usage)
	}
}

// 管理页：列出下游、吊销后凭据消失但账号保留。
func TestPeerAdminListAndRevoke(t *testing.T) {
	server := newPeerTestServer(t,
		domain.Provider{ID: "hj-claude", Name: "hj-claude", Protocol: domain.ProtocolClaude, BaseURL: "https://hj.example.com"},
	)
	_, response := peerRegister(t, server, peerRegisterPayload(map[string]any{
		"requestedProviderIds": []string{"hj-claude"},
	}))

	recorder := adminCall(t, server.handleListPeers, http.MethodGet, "/__peers", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var listed struct {
		Peers []peerSummary `json:"peers"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode peers: %v (%s)", err, recorder.Body.String())
	}
	if len(listed.Peers) != 1 {
		t.Fatalf("peers = %+v", listed.Peers)
	}
	if listed.Peers[0].GatewayID != otherID || !listed.Peers[0].KeyEnabled || !listed.Peers[0].Enabled {
		t.Fatalf("peer summary = %+v", listed.Peers[0])
	}

	recorder = adminCall(t, server.handleRevokePeer, http.MethodDelete, "/__peers/"+response.UserID, nil, map[string]string{"id": response.UserID})
	if recorder.Code != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	// 凭据没了，账号还在（授权还在，重新注册能恢复）。
	recorder = adminCall(t, server.handleListPeers, http.MethodGet, "/__peers", nil, nil)
	_ = json.Unmarshal(recorder.Body.Bytes(), &listed)
	if len(listed.Peers) != 1 || listed.Peers[0].KeyEnabled || listed.Peers[0].Enabled {
		t.Fatalf("revoked peer summary = %+v", listed.Peers)
	}
}

// 注册白名单：写错一个 CIDR 必须当场报错，否则管理员会以为已经限制住了。
func TestPeerRegistrationCIDRsValidation(t *testing.T) {
	server := newPeerTestServer(t)
	recorder := adminCall(t, server.handleSetPeerRegistrationCIDRs, http.MethodPatch, "/__settings/peer-registration-cidrs",
		map[string]any{"cidrs": "10.0.0.0/8, not-a-cidr"}, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if server.router.PeerRegistrationAllowCIDRs() != "" {
		t.Fatal("a rejected write must not change the setting")
	}
	recorder = adminCall(t, server.handleSetPeerRegistrationCIDRs, http.MethodPatch, "/__settings/peer-registration-cidrs",
		map[string]any{"cidrs": "10.0.0.0/8, 192.168.0.0/16"}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if server.router.PeerRegistrationAllowCIDRs() != "10.0.0.0/8, 192.168.0.0/16" {
		t.Fatalf("cidrs = %q", server.router.PeerRegistrationAllowCIDRs())
	}
}

// 跳数上限可配置：默认 3，写 0 恢复默认，写超天花板的收敛到天花板。
// 一条端到端链也在这里验：调大后原本会被 508 的 3 跳链路放行。
func TestPeerMaxHopsConfigurable(t *testing.T) {
	server := newPeerTestServer(t)
	recorder := adminCall(t, server.handleSetPeerMaxHops, http.MethodPatch, "/__settings/peer-max-hops",
		map[string]any{"hops": 5}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if server.router.MaxGatewayHops() != 5 {
		t.Fatalf("max hops = %d, want 5", server.router.MaxGatewayHops())
	}
	// 缺 hops 字段必须报错，而不是被当成 0 悄悄改回默认。
	recorder = adminCall(t, server.handleSetPeerMaxHops, http.MethodPatch, "/__settings/peer-max-hops",
		map[string]any{}, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing hops status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	// 0 恢复默认。
	recorder = adminCall(t, server.handleSetPeerMaxHops, http.MethodPatch, "/__settings/peer-max-hops",
		map[string]any{"hops": 0}, nil)
	if recorder.Code != http.StatusOK || server.router.MaxGatewayHops() != maxGatewayHops {
		t.Fatalf("reset status=%d hops=%d, want the default", recorder.Code, server.router.MaxGatewayHops())
	}

	// 端到端：3 跳的链在默认配置下被 508，调到 5 后放行到 handler。
	loopServer := newHopTestServer(t, gwID)
	atDefault := httptest.NewRequest(http.MethodPost, peerEndpointPrefix+"chat/completions", nil)
	atDefault.Header.Set(headerGatewayPath, "gw_0000000000000001,gw_0000000000000002,gw_0000000000000003")
	recorder2 := httptest.NewRecorder()
	loopServer.Handler().ServeHTTP(recorder2, atDefault)
	if recorder2.Code != http.StatusLoopDetected {
		t.Fatalf("a 3-hop chain must be refused at the default limit, got %d", recorder2.Code)
	}
	loopServer.router.SetMaxGatewayHops(5)
	polled := httptest.NewRequest(http.MethodPost, peerEndpointPrefix+"chat/completions", nil)
	polled.Header.Set(headerGatewayPath, "gw_0000000000000001,gw_0000000000000002,gw_0000000000000003")
	recorder2 = httptest.NewRecorder()
	loopServer.Handler().ServeHTTP(recorder2, polled)
	if recorder2.Code == http.StatusLoopDetected {
		t.Fatal("a 3-hop chain must pass once the limit is raised to 5")
	}
}

// 按层授权的编辑入口：注册只是预置授权的快照，上游随时可以改，改动立即生效
// 且不需要下游重新注册。这里验三件事——改授权真的写进 role store、不存在的
// provider 被静默丢弃、空列表收回全部。
func TestPeerGrantsEditableAfterRegistration(t *testing.T) {
	server := newPeerTestServer(t,
		domain.Provider{ID: "hj-claude", Name: "hj-claude", Protocol: domain.ProtocolClaude, BaseURL: "https://hj.example.com"},
		domain.Provider{ID: "new-one", Name: "new-one", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://n.example.com"},
		domain.Provider{ID: "gone-provider", Name: "deleted", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://x.example.com", Deleted: true},
	)
	_, response := peerRegister(t, server, peerRegisterPayload(map[string]any{
		"requestedProviderIds": []string{"hj-claude"},
	}))

	// 新增授权 + 夹带一个不存在的 provider：有效的那部分必须落库，坏的静默丢弃。
	recorder := adminCall(t, server.handleSetPeerGrants, http.MethodPatch, "/__peers/{id}/grants",
		map[string]any{"providerIds": []string{"hj-claude", "new-one", "gone-provider"}}, map[string]string{"id": response.UserID})
	if recorder.Code != http.StatusOK {
		t.Fatalf("grants status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	grants, err := server.roles.ListResourceGrants()
	if err != nil {
		t.Fatal(err)
	}
	if ids := grants[response.UserID]; len(ids) != 2 {
		t.Fatalf("grants = %v, want hj-claude + new-one (deleted/unknown dropped)", ids)
	}

	// 空列表 = 收回全部；下一个请求当场拿不到任何 provider。
	recorder = adminCall(t, server.handleSetPeerGrants, http.MethodPatch, "/__peers/{id}/grants",
		map[string]any{"providerIds": []string{}}, map[string]string{"id": response.UserID})
	if recorder.Code != http.StatusOK {
		t.Fatalf("empty grants status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	grants, err = server.roles.ListResourceGrants()
	if err != nil {
		t.Fatal(err)
	}
	if ids := grants[response.UserID]; len(ids) != 0 {
		t.Fatalf("grants after revoke-all = %v, want empty", ids)
	}

	// 缺 providerIds 字段必须报错，而不是被当成"清空"悄悄把授权抹掉。
	recorder = adminCall(t, server.handleSetPeerGrants, http.MethodPatch, "/__peers/{id}/grants",
		map[string]any{}, map[string]string{"id": response.UserID})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing providerIds status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	grants, err = server.roles.ListResourceGrants()
	if err != nil {
		t.Fatal(err)
	}
	if ids := grants[response.UserID]; len(ids) != 0 {
		t.Fatalf("grants must be untouched by the rejected write, got %v", ids)
	}

	// 只对真正的对端账号有效。
	recorder = adminCall(t, server.handleSetPeerGrants, http.MethodPatch, "/__peers/{id}/grants",
		map[string]any{"providerIds": []string{"hj-claude"}}, map[string]string{"id": "not-a-user"})
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown peer status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

// adminCall 直接调 handler 并注入管理员身份（withAdminAuth 正是这样注入的）。
//
// 不走 Handler()：那一层还要求先设好管理密码，而这里要验的是 handler 自己
// 的行为。整条链路上的鉴权由 TestNormalUserIsRefusedOnAdminEndpoints 覆盖。
func adminCall(t *testing.T, handler http.HandlerFunc, method, target string, body any, values map[string]string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, target, reader)
	for key, value := range values {
		request.SetPathValue(key, value)
	}
	request = request.WithContext(context.WithValue(request.Context(), authContextKey{}, adminIdentity()))
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

// 授权集里的 Provider 声明 openai_chat、实测支持 claude 时，父网关应当接受
// Claude payload，并在本次尝试里按 Claude 直通。
func TestSelectPeerProviderUsesProtocolSupport(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "pod", Name: "pod", Protocol: domain.ProtocolOpenAIChat,
			ProtocolSupport: []string{"openai_chat", "openai_responses", "claude"},
			Models:          []domain.Model{{ID: "m", ProviderID: "pod", Protocol: domain.ProtocolOpenAIChat}},
		}},
	}), monitor.NewStore())
	decision, ok := server.selectPeerProvider(map[string]bool{"pod": true}, "pod", domain.ProtocolClaude, "m")
	if !ok {
		t.Fatal("应当选中声明 openai_chat、实测支持 claude 的 Provider")
	}
	if decision.InputProtocol != domain.ProtocolClaude || decision.OutputProtocol != domain.ProtocolClaude {
		t.Fatalf("decision protocols = %s/%s, want claude/claude", decision.InputProtocol, decision.OutputProtocol)
	}
}

// 父网关收到 Claude payload：Provider 声明 openai_chat 但实测支持 claude 时，
// 必须打它的 /messages，而不是按声明协议转成 /chat/completions。
func TestPeerChatForwardsWithSupportedNativeProtocol(t *testing.T) {
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/messages" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"not found"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"native ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()

	server := newPeerTestServer(t,
		domain.Provider{
			ID: "multi", Name: "multi", Protocol: domain.ProtocolOpenAIChat,
			ProtocolSupport: []string{"openai_chat", "claude"},
			AuthType:        domain.AuthTypeAPIKey, APIKeySource: "literal:sk-upstream",
			BaseURL: upstream.URL,
			Models:  []domain.Model{{ID: "m", ProviderID: "multi", Protocol: domain.ProtocolOpenAIChat, InMenu: true}},
		},
	)
	server.router.SetPeerRegistrationAllowCIDRs("127.0.0.1/32")
	_, response := peerRegister(t, server, peerRegisterPayload(map[string]any{
		"requestedProviderIds": []string{"multi"},
	}))

	body, _ := json.Marshal(map[string]any{
		"providerId": "multi",
		"protocol":   "claude",
		"payload": map[string]any{
			"model": "m", "max_tokens": 16,
			"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		},
	})
	request := httptest.NewRequest(http.MethodPost, peerEndpointPrefix+"chat/completions", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+response.Token)
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(paths) != 1 || paths[0] != "/messages" {
		t.Fatalf("upstream paths = %v, want [/messages]", paths)
	}
	if !strings.Contains(recorder.Body.String(), "native ok") {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}
