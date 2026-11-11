package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 下游这一侧的控制台接口：注册到一个上游网关，然后本机就能像用普通
// provider 一样用它。这条测试把「注册 → 落 provider → 建路由 → 真发一次请求」
// 四步串起来，任何一环断了这里都会红。
func TestPeerUpstreamRegisterCreatesProvider(t *testing.T) {
	realUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"from parent"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	}))
	defer realUpstream.Close()

	parent := newPeerTestServer(t, domain.Provider{
		ID: "parent-model", Name: "parent-model", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeAPIKey, APIKeySource: "literal:sk-parent",
		BaseURL: realUpstream.URL,
		Models:  []domain.Model{{ID: "claude-sonnet-4-5", ProviderID: "parent-model", Protocol: domain.ProtocolOpenAIChat, InMenu: true}},
	})
	parent.SetGatewayID(otherID)
	parentHTTP := httptest.NewServer(parent.Handler())
	defer parentHTTP.Close()

	child := newPeerTestServer(t)
	child.SetGatewayID(gwID)

	// 走控制台接口注册，而不是直接调 peerClient：这正是界面会做的事。
	body, _ := json.Marshal(map[string]any{
		"parentUrl":            parentHTTP.URL,
		"username":             "sub-gw",
		"password":             "a-strong-password",
		"name":                 "vps1",
		"protocol":             "openai_chat",
		"requestedProviderIds": []string{"parent-model"},
	})
	request := httptest.NewRequest(http.MethodPost, "/__peer-upstreams", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	// 本机操作员：控制台的本机免登录靠源地址 + 回环 Host 两条一起判定。
	request.Host = "127.0.0.1"
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	child.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	// 落下来的 provider：authType=peer、凭据就是上游发的对端凭据、模型来自授权。
	var created domain.Provider
	for _, provider := range child.router.State().Providers {
		if provider.AuthType == domain.AuthTypePeer {
			created = provider
		}
	}
	if created.ID == "" {
		t.Fatal("注册之后本机应当多出一条 peer provider")
	}
	if !strings.HasPrefix(peerUpstreamToken(created), peerTokenPrefix) {
		t.Fatalf("apiKeySource 必须存的是对端凭据，实际 %q", created.APIKeySource)
	}
	if len(created.Models) != 1 || created.Models[0].ID != "claude-sonnet-4-5" {
		t.Fatalf("模型清单应当来自上游授权：%+v", created.Models)
	}

	// 注册顺手建好的那条路由必须真的能用。
	routes := child.router.State().Routes
	if len(routes) != 1 || routes[0].ProviderID != created.ID {
		t.Fatalf("应当自动建一条路由：%+v", routes)
	}
	key, err := child.router.AddAPIKey(domain.APIKey{Name: "local", RouteID: routes[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	chat := chatThroughChild(t, child, key.Key, `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)
	if chat.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", chat.Code, chat.Body.String())
	}
	if !strings.Contains(chat.Body.String(), "from parent") {
		t.Fatalf("父网关的回答必须回到客户端：%s", chat.Body.String())
	}
}

// 协议对不上的模型不能进模型清单：发过去必然被上游拒，留在列表里等于给用户
// 一个注定失败的选项。
func TestPeerModelsForProtocolDropsMismatchedProviders(t *testing.T) {
	granted := []peerProviderView{
		{ID: "a", Name: "a", Protocol: string(domain.ProtocolClaude), Models: []string{"claude-x"}},
		{ID: "b", Name: "b", Protocol: string(domain.ProtocolOpenAIChat), Models: []string{"gpt-x", "shared"}},
	}
	models := peerModelsForProtocol(domain.ProtocolOpenAIChat, granted)
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	if strings.Join(ids, ",") != "gpt-x,shared" {
		t.Fatalf("models = %v, 只应留下同协议的模型", ids)
	}
	// 协议为空是"老版本上游没说协议"的情形，此时不按协议过滤。
	if got := peerModelsForProtocol(domain.ProtocolClaude, []peerProviderView{{ID: "c", Models: []string{"m"}}}); len(got) != 1 {
		t.Fatalf("上游没声明协议时不该过滤：%+v", got)
	}
}

// 刷新（界面上的「测试连接」）必须更新模型清单，同时绝不能把已存的凭据
// 清空——Router.UpdateProvider 会拿 patch 里的空 apiKeySource 覆盖旧值。
func TestPeerUpstreamRefreshKeepsCredential(t *testing.T) {
	parent := newPeerTestServer(t, domain.Provider{
		ID: "parent-model", Name: "parent-model", Protocol: domain.ProtocolOpenAIChat,
		BaseURL: "https://parent.example.com",
		Models:  []domain.Model{{ID: "old-model", ProviderID: "parent-model", Protocol: domain.ProtocolOpenAIChat, InMenu: true}},
	})
	parent.SetGatewayID(otherID)
	parentHTTP := httptest.NewServer(parent.Handler())
	defer parentHTTP.Close()

	child := newPeerTestServer(t)
	child.SetGatewayID(gwID)
	client := newPeerClient()
	registration, err := client.register(t.Context(), parentHTTP.URL, gwID, "vps1", "sub-gw", "a-strong-password", []string{"parent-model"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	created, err := child.router.AddProvider(domain.Provider{
		Name: "vps1", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypePeer, APIKeySource: "literal:" + registration.Token,
		BaseURL: parentHTTP.URL,
		Models:  []domain.Model{{ID: "stale-model", ProviderID: "vps1", InMenu: true}},
	})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/__peer-upstreams/"+created.ID+"/refresh", nil)
	request.SetPathValue("id", created.ID)
	request.Host = "127.0.0.1"
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	child.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	refreshed, err := child.router.ProviderByID(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if peerUpstreamToken(refreshed) != registration.Token {
		t.Fatalf("刷新把凭据弄丢了：%q", refreshed.APIKeySource)
	}
	if len(refreshed.Models) != 1 || refreshed.Models[0].ID != "old-model" {
		t.Fatalf("模型清单应当按上游最新授权更新：%+v", refreshed.Models)
	}
	if refreshed.HealthStatus != "healthy" {
		t.Fatalf("能拉到清单就说明链路是通的，health = %q", refreshed.HealthStatus)
	}
}

// 授权快照要一路走到列表接口上：/__peer-upstreams 的 grantCount 曾经恒为 0，
// 因为注册时把上游告诉我们的 GrantedProviderIDs 整个丢掉了，列表接口压根没有
// 数据来源。这条测试盯住整条链——注册落 provider 时存下快照、读出来是几个、
// 界面拿到的 summary 也是几个。
func TestPeerUpstreamSummariesReportGrantCount(t *testing.T) {
	parent := newPeerTestServer(t,
		domain.Provider{
			ID: "ds-claude", Name: "ds-claude", Protocol: domain.ProtocolOpenAIChat,
			BaseURL: "https://parent.example.com",
			Models:  []domain.Model{{ID: "m1", ProviderID: "ds-claude", Protocol: domain.ProtocolOpenAIChat, InMenu: true}},
		},
		domain.Provider{
			ID: "only-claude", Name: "only-claude", Protocol: domain.ProtocolClaude,
			BaseURL: "https://parent.example.com",
			Models:  []domain.Model{{ID: "claude-x", ProviderID: "only-claude", Protocol: domain.ProtocolClaude, InMenu: true}},
		},
	)
	parent.SetGatewayID(otherID)
	parentHTTP := httptest.NewServer(parent.Handler())
	defer parentHTTP.Close()

	child := newPeerTestServer(t)
	child.SetGatewayID(gwID)
	registration, granted, err := child.registerWithParent(
		httptest.NewRequest(http.MethodGet, "/__probe", nil),
		parentHTTP.URL, "vps1", "sub-gw", "a-strong-password", []string{"ds-claude"},
	)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(granted) != 1 || granted[0].ID != "ds-claude" {
		t.Fatalf("上游应当只授权了 ds-claude：%+v", granted)
	}
	created, err := child.createPeerUpstreamProviders(
		httptest.NewRequest(http.MethodGet, "/__probe", nil), "vps1", domain.ProtocolOpenAIChat, registration, granted,
	)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("只授权了一个 provider，就该只落一行：%+v", created)
	}

	summary := child.peerUpstreamSummaryOf(created[0].ID)
	if summary.GrantCount != 1 {
		t.Fatalf("grantCount = %d，注册日志里 granted=1 就该是 1", summary.GrantCount)
	}
	if !summary.GrantsSynced {
		t.Fatal("注册时问到了上游，grantsSynced 必须是 true")
	}
	if summary.GrantsSyncedAt == "" {
		t.Fatal("同步过就必须记下同步时间")
	}
	if summary.PeerUsername != registration.Username {
		t.Fatalf("peerUsername = %q，应当是对上游给本机建的账号 %q", summary.PeerUsername, registration.Username)
	}
	if summary.ParentGatewayID != otherID {
		t.Fatalf("parentGatewayId = %q，应当是上游的身份 %q（注册响应里回显过，却从没被抄下来）",
			summary.ParentGatewayID, otherID)
	}
	// 一条链路只说一种协议：另一个被授权的 claude provider 不进模型清单，
	// 但它照样算"被授权了"，所以 grantCount 和 models 不相等是对的。
	if len(summary.Models) != 1 || summary.Models[0] != "m1" {
		t.Fatalf("models = %v", summary.Models)
	}
}

// 从没同步过的链路不能报"授权了 0 个"：那是一个会误导人的结论。列表中它是
// grantCount=0 + grantsSynced=false，界面据此说「还没同步」。
func TestPeerUpstreamSummaryDistinguishesNeverSynced(t *testing.T) {
	server := newPeerTestServer(t, domain.Provider{
		ID: "vps1", Name: "vps1", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypePeer, APIKeySource: "literal:" + peerTokenPrefix + "sometoken",
		BaseURL: "https://parent.example.com",
	})
	summary := server.peerUpstreamSummaryOf("vps1")
	if summary.GrantCount != 0 {
		t.Fatalf("grantCount = %d", summary.GrantCount)
	}
	if summary.GrantsSynced {
		t.Fatal("没有快照就是还没问过上游，grantsSynced 必须是 false")
	}
	if summary.GrantsSyncedAt != "" {
		t.Fatalf("没同步过不该有时间：%q", summary.GrantsSyncedAt)
	}
}

// granted 为 nil 和空切片是两件事：前者是"没问到上游"，后者是"问到了，答案
// 是零个"。合并成一个就再也分不出「上游没授权」和「网络挂了」。
func TestPeerGrantsFromRegistrationNilVersusEmpty(t *testing.T) {
	registration := peerRegistration{Username: "peer-gw-1", UserID: "u1", ParentGatewayID: "gw_parent"}

	if grants := peerGrantsFromRegistration(registration, nil); grants != nil {
		t.Fatalf("没问到上游时不该落快照：%+v", grants)
	}

	grants := peerGrantsFromRegistration(registration, []peerProviderView{})
	if grants == nil {
		t.Fatal("问到了、答案是零个，也必须落快照")
	}
	if len(grants.GrantedProviderIDs) != 0 {
		t.Fatalf("grantedProviderIds = %v", grants.GrantedProviderIDs)
	}
	if grants.SyncedAt == "" {
		t.Fatal("问到了就要记下同步时间")
	}
	if grants.Username != "peer-gw-1" || grants.UserID != "u1" || grants.ParentGatewayID != "gw_parent" {
		t.Fatalf("账号信息应当原样保留：%+v", grants)
	}

	// 授权清单排好序再存：上游响应顺序会变，快照不该跟着抖。
	ordered := peerGrantsFromRegistration(registration, []peerProviderView{{ID: "b"}, {ID: "a"}, {ID: " "}})
	if strings.Join(ordered.GrantedProviderIDs, ",") != "a,b" {
		t.Fatalf("grantedProviderIds = %v", ordered.GrantedProviderIDs)
	}
}

// Router 存快照时要拷一份：调用方之后改自己那片切片，不能绕过后台的锁改到
// 已经"存好"的状态。
func TestRouterSetPeerGrantsCopiesSlice(t *testing.T) {
	registry := NewRouter(domain.GatewayState{Providers: []domain.Provider{{
		ID: "vps1", Name: "vps1", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypePeer, BaseURL: "https://parent.example.com",
	}}})
	ids := []string{"ds-claude"}
	if _, err := registry.SetPeerGrants("vps1", domain.PeerUpstreamGrants{GrantedProviderIDs: ids}); err != nil {
		t.Fatal(err)
	}
	ids[0] = "tampered"
	provider, err := registry.ProviderByID("vps1")
	if err != nil {
		t.Fatal(err)
	}
	if provider.PeerGrants == nil || len(provider.PeerGrants.GrantedProviderIDs) != 1 ||
		provider.PeerGrants.GrantedProviderIDs[0] != "ds-claude" {
		t.Fatalf("存下来的快照被调用方改到了：%+v", provider.PeerGrants)
	}
	if _, err := registry.SetPeerGrants("nope", domain.PeerUpstreamGrants{}); err == nil {
		t.Fatal("不存在的 provider 必须报错")
	}
}

// 刷新只重新要授权清单，账号信息不会再来一遍：那份必须从旧快照沿用，
// 否则一次「测试连接」就会把上游账号名擦掉。
func TestPeerUpstreamRefreshKeepsAccountIdentity(t *testing.T) {
	parent := newPeerTestServer(t, domain.Provider{
		ID: "ds-claude", Name: "ds-claude", Protocol: domain.ProtocolOpenAIChat,
		BaseURL: "https://parent.example.com",
		Models:  []domain.Model{{ID: "m1", ProviderID: "ds-claude", Protocol: domain.ProtocolOpenAIChat, InMenu: true}},
	})
	parent.SetGatewayID(otherID)
	parentHTTP := httptest.NewServer(parent.Handler())
	defer parentHTTP.Close()

	child := newPeerTestServer(t)
	child.SetGatewayID(gwID)
	registration, err := newPeerClient().register(
		t.Context(), parentHTTP.URL, gwID, "vps1", "sub-gw", "a-strong-password", []string{"ds-claude"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	createdRows, err := child.createPeerUpstreamProviders(
		httptest.NewRequest(http.MethodGet, "/__probe", nil), "vps1", domain.ProtocolOpenAIChat, registration,
		[]peerProviderView{{ID: "ds-claude", Protocol: "openai_chat", Models: []string{"m1"}}},
	)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	created := createdRows[0]
	// 升级前的记录只有账号信息、没有分组键：刷新要能就地把它接住，
	// 而不是另起一行。
	if _, err := child.router.SetPeerGrants(created.ID, domain.PeerUpstreamGrants{
		Username: "peer-gw-sub", UserID: "u-sub", ParentGatewayID: otherID, GrantedProviderIDs: []string{"ds-claude"},
	}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/__peer-upstreams/"+created.ID+"/refresh", nil)
	request.SetPathValue("id", created.ID)
	request.Host = "127.0.0.1"
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	child.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	summary := child.peerUpstreamSummaryOf(created.ID)
	if summary.GrantCount != 1 || !summary.GrantsSynced {
		t.Fatalf("刷新之后授权数应当是 1：%+v", summary)
	}
	if summary.PeerUsername != "peer-gw-sub" || summary.ParentGatewayID != otherID {
		t.Fatalf("刷新不该擦掉账号信息：%+v", summary)
	}
}

// 对端 provider 的「获取模型」不能走 baseUrl + /models：父网关没有那个端点。
func TestPeerUpstreamFetchModelsUsesProvidersEndpoint(t *testing.T) {
	parent := newPeerTestServer(t, domain.Provider{
		ID: "parent-model", Name: "parent-model", Protocol: domain.ProtocolOpenAIChat,
		BaseURL: "https://parent.example.com",
		Models:  []domain.Model{{ID: "m1", ProviderID: "parent-model", Protocol: domain.ProtocolOpenAIChat, InMenu: true}},
	})
	parent.SetGatewayID(otherID)
	parentHTTP := httptest.NewServer(parent.Handler())
	defer parentHTTP.Close()

	child := newPeerTestServer(t)
	child.SetGatewayID(gwID)
	client := newPeerClient()
	registration, err := client.register(t.Context(), parentHTTP.URL, gwID, "vps1", "sub-gw", "a-strong-password", []string{"parent-model"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	provider := domain.Provider{
		ID: "vps1", Name: "vps1", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypePeer, APIKeySource: "literal:" + registration.Token,
		BaseURL: parentHTTP.URL,
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/__probe", nil)
	result := child.fetchProviderModels(request, provider, time.Now())
	_ = recorder
	if !result.Success {
		t.Fatalf("获取模型失败：%+v", result)
	}
	if !strings.HasSuffix(result.ModelsURL, peerEndpointPrefix+"providers") {
		t.Fatalf("modelsUrl = %q，应当走对端 provider 清单", result.ModelsURL)
	}
	if len(result.Models) != 1 || result.Models[0].ID != "m1" {
		t.Fatalf("models = %+v", result.Models)
	}
}

// 对端 provider 只能由「网关互联」的注册流程创建。手工 POST 一条 authType=peer
// 会得到一条既没有凭据、也没有门的记录：请求打出去必然 401，而看到的人只会
// 以为上游拒绝了自己。所以创建接口必须直接把它拒掉，而不是让它落库。
func TestPeerUpstreamRejectsHandcraftedPeerProvider(t *testing.T) {
	child := newPeerTestServer(t)
	child.SetGatewayID(gwID)

	body, _ := json.Marshal(map[string]any{
		"name":         "手工对端",
		"protocol":     "openai_chat",
		"baseUrl":      "https://vps1.example.com",
		"apiKeySource": "literal:whatever",
		"authType":     "peer",
	})
	request := httptest.NewRequest(http.MethodPost, "/__providers", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Host = "127.0.0.1"
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	child.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s，手工创建 peer provider 必须被拒", recorder.Code, recorder.Body.String())
	}
	for _, provider := range child.router.State().Providers {
		if provider.AuthType == domain.AuthTypePeer {
			t.Fatalf("被拒的请求不应落库，实际多出 %q", provider.ID)
		}
	}
}

// 反过来：编辑一张注册生成的记录时，控制台会把 authType=peer 原样回传。
// 那不是"手工创建"，必须放行——否则改个名字、调个默认模型都会 400。
func TestPeerUpstreamUpdateKeepsExistingPeerProvider(t *testing.T) {
	realUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer realUpstream.Close()

	parent := newPeerTestServer(t, domain.Provider{
		ID: "parent-model", Name: "parent-model", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeAPIKey, APIKeySource: "literal:sk-parent",
		BaseURL: realUpstream.URL,
		Models:  []domain.Model{{ID: "m1", ProviderID: "parent-model", Protocol: domain.ProtocolOpenAIChat, InMenu: true}},
	})
	parent.SetGatewayID(otherID)
	parentHTTP := httptest.NewServer(parent.Handler())
	defer parentHTTP.Close()

	child := newPeerTestServer(t)
	child.SetGatewayID(gwID)
	registration, err := newPeerClient().register(t.Context(), parentHTTP.URL, gwID, "vps1", "sub-gw", "a-strong-password", []string{"parent-model"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	createdRows, err := child.createPeerUpstreamProviders(
		httptest.NewRequest(http.MethodGet, "/__probe", nil),
		"vps1", domain.ProtocolOpenAIChat, registration,
		[]peerProviderView{{ID: "parent-model", Protocol: "openai_chat", Models: []string{"m1"}}},
	)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	created := createdRows[0]

	// 控制台编辑：只改名字，其余原样回传（含 authType=peer 与凭据）。
	body, _ := json.Marshal(map[string]any{
		"name":         "vps1-改名后",
		"protocol":     "openai_chat",
		"baseUrl":      created.BaseURL,
		"apiKeySource": created.APIKeySource,
		"authType":     "peer",
		"defaultModel": "m1",
	})
	request := httptest.NewRequest(http.MethodPatch, "/__providers/"+created.ID, strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Host = "127.0.0.1"
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	child.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s，编辑已有对端记录应当放行", recorder.Code, recorder.Body.String())
	}
	updated, err := child.router.ProviderByID(created.ID)
	if err != nil {
		t.Fatalf("provider 应当还在：%v", err)
	}
	if updated.Name != "vps1-改名后" || updated.AuthType != domain.AuthTypePeer {
		t.Fatalf("updated = %+v", updated)
	}
}

// 表单上的「测试连接」：只探连通性与注册来源限制，不建账号、不换凭据。
func TestPeerUpstreamProbeChecksGatewayWithoutRegistering(t *testing.T) {
	parent := newPeerTestServer(t)
	parent.SetGatewayID(otherID)
	parentHTTP := httptest.NewServer(parent.Handler())
	defer parentHTTP.Close()

	child := newPeerTestServer(t)
	child.SetGatewayID(gwID)
	body, _ := json.Marshal(map[string]any{"parentUrl": parentHTTP.URL + peerEndpointPrefix + "register"})
	request := httptest.NewRequest(http.MethodPost, "/__peer-upstreams/probe", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Host = "127.0.0.1"
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	child.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var probed struct {
		ParentURL           string `json:"parentUrl"`
		GatewayID           string `json:"gatewayId"`
		RegistrationAllowed bool   `json:"registrationAllowed"`
		Message             string `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &probed); err != nil {
		t.Fatal(err)
	}
	// 粘进来的 /__peer/register 路径要被去掉，回的是根地址。
	if probed.ParentURL != parentHTTP.URL {
		t.Fatalf("parentUrl = %q, want %q", probed.ParentURL, parentHTTP.URL)
	}
	if probed.GatewayID != otherID {
		t.Fatalf("gatewayId = %q, want the upstream identity %q", probed.GatewayID, otherID)
	}
	if !probed.RegistrationAllowed || probed.Message == "" {
		t.Fatalf("probe result = %+v", probed)
	}
	// 探测不能建账号：上游那侧一个 peer 用户都不该有。
	users, err := parent.userStore.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		if user.IsPeer() {
			t.Fatalf("探测不该在上游建出账号：%+v", user)
		}
	}
	// 本机也不该多出 provider。
	if len(child.router.State().Providers) != 0 {
		t.Fatalf("探测不该落 provider：%+v", child.router.State().Providers)
	}
}

// 上游的白名单挡住本机时，探测仍然回答 200，但明确说"不能从这里注册"：
// 运维要能区分「不是网关」和「对面不让你注册」。
func TestPeerUpstreamProbeReportsRefusedSource(t *testing.T) {
	parent := newPeerTestServer(t)
	parent.SetGatewayID(otherID)
	parent.router.SetPeerRegistrationAllowCIDRs("10.0.0.0/8")
	parentHTTP := httptest.NewServer(parent.Handler())
	defer parentHTTP.Close()

	child := newPeerTestServer(t)
	child.SetGatewayID(gwID)
	body, _ := json.Marshal(map[string]any{"parentUrl": parentHTTP.URL})
	request := httptest.NewRequest(http.MethodPost, "/__peer-upstreams/probe", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Host = "127.0.0.1"
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	child.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var probed struct {
		RegistrationAllowed    bool   `json:"registrationAllowed"`
		RegistrationAllowCIDRs string `json:"registrationAllowCidrs"`
		Message                string `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &probed); err != nil {
		t.Fatal(err)
	}
	if probed.RegistrationAllowed {
		t.Fatalf("registrationAllowed must be false: %+v", probed)
	}
	if !strings.Contains(probed.Message, "白名单") || probed.RegistrationAllowCIDRs != "10.0.0.0/8" {
		t.Fatalf("说明里要带上游实际的白名单：%+v", probed)
	}
}

// 地址背后不是网关时，错误要说人话，而不是把 HTML 解析错误抛给运维。
func TestPeerUpstreamProbeRejectsNonGatewayAddress(t *testing.T) {
	notAGateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html>welcome to nginx</html>")
	}))
	defer notAGateway.Close()

	child := newPeerTestServer(t)
	child.SetGatewayID(gwID)
	for _, parentURL := range []string{notAGateway.URL, "http://127.0.0.1:1"} {
		body, _ := json.Marshal(map[string]any{"parentUrl": parentURL})
		request := httptest.NewRequest(http.MethodPost, "/__peer-upstreams/probe", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Host = "127.0.0.1"
		request.RemoteAddr = "127.0.0.1:40000"
		recorder := httptest.NewRecorder()
		child.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadGateway {
			t.Fatalf("%s: status=%d body=%s", parentURL, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "连不上") {
			t.Fatalf("%s: 错误应当说清是连不上：%s", parentURL, recorder.Body.String())
		}
	}
}

// 一条上游授权了多个 provider 时，子网关要落成多条 provider 行：只有这样才能
// 像本地 provider 一样一条条显示，也才能被单独选进 API 密钥的备选链。它们共享
// 同一把凭据、同一个上游，各自记住"我对应上游的哪一个"。
func TestPeerUpstreamSplitsGrantedProvidersIntoRows(t *testing.T) {
	registration := peerRegistration{
		ParentURL: "https://parent.example.com", Token: peerTokenPrefix + "tok",
		Username: "peer-gw-sub", UserID: "u1", ParentGatewayID: otherID,
	}
	granted := []peerProviderView{
		{ID: "ds-chat", Name: "DeepSeek Chat", Protocol: "openai_chat", Models: []string{"ds-chat"}},
		{ID: "ds-claude", Name: "DeepSeek Claude", Protocol: "claude", Models: []string{"ds-claude-opus"}},
	}
	server := newPeerTestServer(t)
	server.SetGatewayID(gwID)
	rows, err := server.createPeerUpstreamProviders(
		httptest.NewRequest(http.MethodGet, "/__probe", nil), "vps1", domain.ProtocolOpenAIChat, registration, granted,
	)
	if err != nil {
		t.Fatalf("create providers: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("授权了两个 provider 就该落两行：%+v", rows)
	}
	seen := map[string]domain.Protocol{}
	for _, row := range rows {
		if row.AuthType != domain.AuthTypePeer {
			t.Fatalf("成员行必须是对端类型：%+v", row)
		}
		if peerUpstreamToken(row) != peerTokenPrefix+"tok" {
			t.Fatalf("每一条成员行都要带着同一把凭据：% +v", row.APIKeySource)
		}
		if row.PeerGrants == nil || row.PeerGrants.UpstreamKey == "" {
			t.Fatalf("成员行必须带上分组键：%+v", row.PeerGrants)
		}
		grantedID := peerGrantedProviderID(row)
		if grantedID == "" {
			t.Fatalf("成员行必须记住自己对应上游的哪个 provider：%+v", row.PeerGrants)
		}
		seen[grantedID] = row.Protocol
	}
	if seen["ds-chat"] != domain.ProtocolOpenAIChat || seen["ds-claude"] != domain.ProtocolClaude {
		t.Fatalf("每个成员行的协议应当取自上游：%+v", seen)
	}
	// 组里只要一条：界面上它就是一个上游。
	if summaries := server.peerUpstreamSummaries(); len(summaries) != 1 {
		t.Fatalf("同一上游的成员行要收成一组，实际 %d 组：%+v", len(summaries), summaries)
	} else if summaries[0].MemberCount != 2 || summaries[0].GrantCount != 2 {
		t.Fatalf("组信息不对：%+v", summaries[0])
	}
}

// 刷新时上游新授权一个、又收回一个：成员行要跟着增删。收回的那条被软删除，
// 于是绑过它的密钥引用会被一并清掉，而不是继续指向一个上游已经拒绝的 provider。
func TestPeerUpstreamRefreshAddsAndRevokesMembers(t *testing.T) {
	parent := newPeerTestServer(t,
		domain.Provider{
			ID: "a", Name: "a", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://parent.example.com",
			Models: []domain.Model{{ID: "m-a", ProviderID: "a", InMenu: true}},
		},
		domain.Provider{
			ID: "b", Name: "b", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://parent.example.com",
			Models: []domain.Model{{ID: "m-b", ProviderID: "b", InMenu: true}},
		},
		domain.Provider{
			ID: "c", Name: "c", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://parent.example.com",
			Models: []domain.Model{{ID: "m-c", ProviderID: "c", InMenu: true}},
		},
	)
	parent.SetGatewayID(otherID)
	parentHTTP := httptest.NewServer(parent.Handler())
	defer parentHTTP.Close()

	child := newPeerTestServer(t)
	child.SetGatewayID(gwID)
	registration, granted, err := child.registerWithParent(
		httptest.NewRequest(http.MethodGet, "/__probe", nil),
		parentHTTP.URL, "vps1", "sub-gw", "a-strong-password", []string{"a", "b"},
	)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	rows, err := child.createPeerUpstreamProviders(
		httptest.NewRequest(http.MethodGet, "/__probe", nil), "vps1", domain.ProtocolOpenAIChat, registration, granted,
	)
	if err != nil {
		t.Fatalf("create providers: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("应当落两行：%+v", rows)
	}
	var rowB domain.Provider
	for _, row := range rows {
		if peerGrantedProviderID(row) == "b" {
			rowB = row
		}
	}
	if rowB.ID == "" {
		t.Fatalf("没找到对应 b 的那一行：%+v", rows)
	}

	// 上游改授权：收回 b、加上 c。
	account, err := parent.userStore.UserByGatewayID(gwID)
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.setPeerGrants(account.ID, []string{"a", "c"}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/__peer-upstreams/"+rows[0].ID+"/refresh", nil)
	request.SetPathValue("id", rows[0].ID)
	request.Host = "127.0.0.1"
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	child.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	members := child.peerUpstreamMembers(peerGroupKeyForProvider(rows[0]))
	byGranted := map[string]bool{}
	for _, member := range members {
		byGranted[peerGrantedProviderID(member)] = true
	}
	if !byGranted["a"] || !byGranted["c"] {
		t.Fatalf("新授权的 c 要落成一行、a 要留着：%+v", byGranted)
	}
	if byGranted["b"] {
		t.Fatalf("上游收回的 b 必须从成员里消失：%+v", byGranted)
	}
	if _, err := child.router.ProviderByID(rowB.ID); err == nil {
		t.Fatalf("被收回的成员行应当被软删除：%s", rowB.ID)
	}
}

// 透传信封里的 providerId 取自这条 provider 对应上游的哪个 provider：有了它，
// 「在子网关里选中上游的某一个 provider」才会被上游精确路由到那一个。
func TestPeerForwardEnvelopeCarriesGrantedProviderID(t *testing.T) {
	provider := domain.Provider{
		ID: "vps1-ds-claude", Name: "vps1 · ds-claude", Protocol: domain.ProtocolOpenAIChat,
		AuthType:   domain.AuthTypePeer,
		PeerGrants: &domain.PeerUpstreamGrants{GrantedProviderID: "ds-claude"},
	}
	if got := peerGrantedProviderID(provider); got != "ds-claude" {
		t.Fatalf("peerGrantedProviderID = %q, want ds-claude", got)
	}
	// 升级前的老记录没有这个字段：留空，上游仍按授权集自己挑。
	if got := peerGrantedProviderID(domain.Provider{AuthType: domain.AuthTypePeer}); got != "" {
		t.Fatalf("老记录不该凭空多出一个 providerId：%q", got)
	}
	body, err := buildPeerForwardBody(peerGrantedProviderID(provider), provider.Protocol, []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"providerId":"ds-claude"`) {
		t.Fatalf("信封里必须带上被选中的上游 provider：%s", body)
	}
}

// 上游一次把授权全收回：整组成员收敛成一条「连上了但没授权」的占位行，而不是
// 全删光再从零建一条——绑过其中某条的密钥备选不该被顺手清掉，锚点也还是原来那条。
func TestPeerUpstreamRefreshKeepsAnchorWhenGrantsEmptied(t *testing.T) {
	parent := newPeerTestServer(t,
		domain.Provider{
			ID: "a", Name: "a", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://parent.example.com",
			Models: []domain.Model{{ID: "m-a", ProviderID: "a", InMenu: true}},
		},
		domain.Provider{
			ID: "b", Name: "b", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://parent.example.com",
			Models: []domain.Model{{ID: "m-b", ProviderID: "b", InMenu: true}},
		},
	)
	parent.SetGatewayID(otherID)
	parentHTTP := httptest.NewServer(parent.Handler())
	defer parentHTTP.Close()

	child := newPeerTestServer(t)
	child.SetGatewayID(gwID)
	registration, granted, err := child.registerWithParent(
		httptest.NewRequest(http.MethodGet, "/__probe", nil),
		parentHTTP.URL, "vps1", "sub-gw", "a-strong-password", []string{"a", "b"},
	)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	// 锚点就是本机自己落的第一行：它必须活过这一轮，且身份（id）不变。
	rows, err := child.createPeerUpstreamProviders(
		httptest.NewRequest(http.MethodGet, "/__probe", nil), "vps1", domain.ProtocolOpenAIChat, registration, granted,
	)
	if err != nil {
		t.Fatalf("create providers: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("应当落两行：%+v", rows)
	}
	anchor := rows[0]
	account, err := parent.userStore.UserByGatewayID(gwID)
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.setPeerGrants(account.ID, []string{}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/__peer-upstreams/"+anchor.ID+"/refresh", nil)
	request.SetPathValue("id", anchor.ID)
	request.Host = "127.0.0.1"
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	child.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	members := child.peerUpstreamMembers(peerGroupKeyForProvider(anchor))
	if len(members) != 1 {
		t.Fatalf("授权全收回后应当只剩一条占位行，实际 %d 条：%+v", len(members), members)
	}
	if members[0].ID != anchor.ID {
		t.Fatalf("留下的应当是原来的锚点 %s，实际 %s", anchor.ID, members[0].ID)
	}
	if got := peerGrantedProviderID(members[0]); got != "" {
		t.Fatalf("占位行不该声称自己对应上游的某个 provider：%q", got)
	}
	if grants := members[0].PeerGrants; grants == nil || grants.SyncedAt == "" {
		// 授权快照仍要刷新：界面上「上游授权 0 个」是个可信的结论，不是没同步。
		t.Fatalf("快照仍应更新：%+v", grants)
	}
}
