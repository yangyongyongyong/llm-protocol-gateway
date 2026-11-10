package gateway

import (
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
	created, err := child.createPeerUpstreamProvider(
		httptest.NewRequest(http.MethodGet, "/__probe", nil),
		"vps1", domain.ProtocolOpenAIChat, registration,
		[]peerProviderView{{Protocol: "openai_chat", Models: []string{"m1"}}},
	)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}

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
