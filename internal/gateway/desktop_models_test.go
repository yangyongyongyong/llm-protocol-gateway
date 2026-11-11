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

const claudeDesktopUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Claude/2.7032.0 Chrome/140.0.0.0 Electron/38.0.0 Safari/537.36"

// desktopCatalogServer 起一个只有 m1/m2 的网关：这正是 Claude Desktop 一条
// 模型都留不下的那种配置。
func desktopCatalogServer(t *testing.T) (*Server, string) {
	t.Helper()
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "p1", Name: "P1", Protocol: domain.ProtocolOpenAIChat,
			AuthType: domain.AuthTypeAPIKey, BaseURL: "https://example.invalid",
			Models: []domain.Model{
				{ID: "m1", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, InMenu: true},
				{ID: "m2", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, InMenu: true},
			},
		}},
		Routes: []domain.Route{{
			ID: "r1", Name: "R1", ProviderID: "p1",
			OutputProtocol: domain.ProtocolOpenAIChat, Mode: domain.RouteModeAuto, Enabled: true,
		}},
		APIKeys: []domain.APIKey{{ID: "k1", Name: "K1", Key: "sk-gw", RouteID: "r1", Enabled: true}},
	})
	return NewServer(router, monitor.NewStore()), "sk-gw"
}

func getModels(t *testing.T, server *Server, token, userAgent string) []map[string]any {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	if userAgent != "" {
		request.Header.Set("User-Agent", userAgent)
	}
	recorder := httptest.NewRecorder()
	server.handleOpenAIModels(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v (%s)", err, recorder.Body.String())
	}
	return payload.Data
}

// 只提供 m1/m2 的网关，普通 OpenAI 客户端看到的仍是真实 id——别名只是
// Desktop 那一侧的投影，不能污染别人的模型目录。
func TestOpenAIModelsKeepsRealIDsForOtherClients(t *testing.T) {
	server, token := desktopCatalogServer(t)
	data := getModels(t, server, token, "curl/8.7.1")
	var ids []string
	for _, entry := range data {
		ids = append(ids, entry["id"].(string))
	}
	if len(ids) != 2 || ids[0] != "m1" || ids[1] != "m2" {
		t.Fatalf("ids = %v, want the real m1/m2", ids)
	}
	for _, entry := range data {
		if _, ok := entry["display_name"]; ok {
			t.Fatalf("display_name is Claude Desktop's field, not OpenAI's: %v", entry)
		}
	}
}

// Claude Desktop 拿到的是它认得的 id：m1/m2 会被它整条丢掉（一条都不剩就报
// 「Your organization hasn't configured any models」），所以换成别名并保留
// 真名供菜单显示。
func TestOpenAIModelsGivesClaudeDesktopUsableIDs(t *testing.T) {
	server, token := desktopCatalogServer(t)
	data := getModels(t, server, token, claudeDesktopUA)
	if len(data) != 2 {
		t.Fatalf("got %d entries, want both models listed: %v", len(data), data)
	}
	wantNames := map[string]bool{"m1": false, "m2": false}
	for _, entry := range data {
		id, _ := entry["id"].(string)
		name, _ := entry["display_name"].(string)
		if !desktopAcceptsModelID(id) {
			t.Fatalf("Claude Desktop would drop this id: %q", id)
		}
		if _, ok := wantNames[name]; !ok {
			t.Fatalf("display_name = %q, want the real model name", name)
		}
		wantNames[name] = true
	}
	for name, seen := range wantNames {
		if !seen {
			t.Fatalf("%q never appeared in the Desktop catalog", name)
		}
	}
}

// 别名要稳定：Desktop 把用户选的 id 存进会话，id 一变旧会话就打不开。
func TestDesktopModelAliasIsStable(t *testing.T) {
	first := desktopModelAlias("m1")
	if first != desktopModelAlias("m1") {
		t.Fatal("the alias must not change between calls")
	}
	if first == desktopModelAlias("m2") {
		t.Fatal("two models must not share an alias")
	}
	if !strings.HasPrefix(first, desktopAliasPrefix) || len(strings.TrimPrefix(first, desktopAliasPrefix)) != 10 {
		t.Fatalf("alias shape = %q, want anthropic/gw- plus ten digits", first)
	}
}

// 请求带着别名回来时要换回真实模型：否则上游会收到一个它没有的 id。
func TestDesktopAliasResolvesToTheRealModel(t *testing.T) {
	candidates := []string{"m1", "m2"}
	alias := desktopModelAlias("m1")
	if got := desktopModelForAlias(alias, candidates); got != "m1" {
		t.Fatalf("alias resolved to %q, want m1", got)
	}
	// 真实 id 与非别名一律原样通过。
	if got := desktopModelForAlias("m2", candidates); got != "m2" {
		t.Fatalf("a real id must pass through, got %q", got)
	}
	if got := desktopModelForAlias("gpt-4", candidates); got != "gpt-4" {
		t.Fatalf("an unknown id must pass through, got %q", got)
	}
	// 模型已被删除/撤销授权：别名查不到，不能被当成后门。
	if got := desktopModelForAlias(desktopModelAlias("gone"), candidates); got == "gone" {
		t.Fatal("an alias for a model outside the catalog must not resolve")
	}
	// Claude Code 的 1M 后缀与别名可以同时出现。
	if got := desktopModelForAlias(alias+"[1m]", candidates); got != "m1" {
		t.Fatalf("alias with a context tag resolved to %q, want m1", got)
	}
}

// /v1/models 会连 Key 的固定模型与映射别名一起列出来，它们同样被换成别名；
// 反查必须认得出它们，否则用户选中的「映射别名」会被当成上游模型名发出去。
func TestDesktopAliasResolvesKeyLevelModels(t *testing.T) {
	provider := domain.Provider{ID: "p1", Models: []domain.Model{{ID: "m1"}}}
	key := domain.APIKey{ModelOverride: "m2", ModelAliases: map[string]string{"fast": "m1"}}
	candidates := desktopAliasCandidates(provider, key, true)

	for _, want := range []string{"m1", "m2", "fast"} {
		if got := desktopModelForAlias(desktopModelAlias(want), candidates); got != want {
			t.Fatalf("alias for %q resolved to %q", want, got)
		}
	}
	// 不是网关匹配到的 Key 时，Key 上的配置一律不认。
	anonymous := desktopAliasCandidates(provider, key, false)
	if got := desktopModelForAlias(desktopModelAlias("m2"), anonymous); got == "m2" {
		t.Fatal("an unmatched key's override must not be reachable through an alias")
	}
}

// 走完整的模型解析：别名要能命中真实模型并出现在日志里的是有效模型。
func TestDesktopAliasFeedsModelResolution(t *testing.T) {
	provider := domain.Provider{
		ID: "p1", Protocol: domain.ProtocolOpenAIChat,
		Models: []domain.Model{{ID: "m1", ProviderID: "p1"}, {ID: "m2", ProviderID: "p1"}},
	}
	model, logModel := resolveConsumerModelOnProvider(provider, domain.APIKey{}, false, desktopModelAlias("m2"))
	if model != "m2" {
		t.Fatalf("model = %q, want the real m2", model)
	}
	// 日志左侧仍是客户端发来的那个 id（它确实是请求体里的值），右侧是真实模型。
	if !strings.Contains(logModel, "m2") {
		t.Fatalf("logModel = %q, want it to name the real model", logModel)
	}
}

// 官方 Claude 模型的 id 本来就被 Desktop 接受，不该被改成别名——那样它在
// Desktop 里就失去了自己的名字和图标。
func TestDesktopKeepsNativeClaudeIDs(t *testing.T) {
	for _, id := range []string{"claude-sonnet-5", "claude-opus-5", "sonnet", "opus-5.5", "claude-haiku-4-5-20251001"} {
		if !desktopAcceptsModelID(id) {
			t.Fatalf("%q should be accepted as-is", id)
		}
	}
	for _, id := range []string{"m1", "m2", "gpt-5.5", "glm-5.3", "deepseek-v3", "qwen-max"} {
		if desktopAcceptsModelID(id) {
			t.Fatalf("%q must not be listed to Desktop unchanged", id)
		}
	}
	// 同时含 claude 与别家厂商名：仍然要换别名。
	if desktopAcceptsModelID("anthropic/deepseek-claude") {
		t.Fatal("a denied vendor word must win over an accepted one")
	}
}

// Claude Desktop 把 /v1/messages 与 /v1/models 直接拼在它在配置里写的
// inferenceGatewayBaseUrl 后面（那个地址不带 /anthropic，magpie 也是在根路径
// 上提供这两个端点）。这两条路由是桌面端真正发消息的入口：少了它们，模型
// 菜单能列出来，一发消息却是 404。
func TestGatewayServesDesktopRootModelPaths(t *testing.T) {
	server, token := desktopCatalogServer(t)
	handler := server.Handler()
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens", "/v1/models"} {
		method := http.MethodPost
		var body io.Reader = strings.NewReader(`{"model":"m1","max_tokens":1,"messages":[]}`)
		if path == "/v1/models" {
			method, body = http.MethodGet, nil
		}
		request := httptest.NewRequest(method, path, body)
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		// 这里只认「路由存在」：上游是 example.invalid，打不通是正常的；
		// 关键是它没有落到 404 的兜底上。
		if recorder.Code == http.StatusNotFound {
			t.Fatalf("%s is not routed: %d %s", path, recorder.Code, recorder.Body.String())
		}
	}
	if !isModelProtocolPath("/v1/messages") || !isModelProtocolPath("/v1/messages/count_tokens") {
		t.Fatal("the root messages paths must count as model paths, or a local host never forwards them to the mux")
	}
}
