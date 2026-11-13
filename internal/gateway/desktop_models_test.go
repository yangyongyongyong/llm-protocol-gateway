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
			OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true,
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
	if len(data) != 4 {
		t.Fatalf("got %d entries, want two defaults and both models: %v", len(data), data)
	}
	wantNames := map[string]bool{"default": false, "default[1m]": false, "m1": false, "m2": false}
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

// desktop1MServer 起一个「窗口各不相同」的网关：厂商已知的 1M 模型、上游自己
// 报了 1M 的模型、正好 1M 的模型、差一点的和默认的。
func desktop1MServer(t *testing.T) (*Server, string) {
	t.Helper()
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "p1", Name: "P1", Protocol: domain.ProtocolOpenAIChat,
			AuthType: domain.AuthTypeAPIKey, BaseURL: "https://example.invalid",
			Models: []domain.Model{
				// 厂商已知窗口：就算上游没说也是 1M。
				{ID: "claude-opus-5", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, InMenu: true},
				// 上游自己报的窗口。
				{ID: "m-long", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, ContextLength: 1_048_576, InMenu: true},
				// 正好 1M：Desktop 的门槛是 >=，包含它。
				{ID: "m-edge", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, ContextLength: 1_000_000, InMenu: true},
				// 872K 和没报的：都不是 1M，不许被标成 1M。
				{ID: "m-short", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, ContextLength: 872_000, InMenu: true},
				{ID: "m-plain", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, InMenu: true},
				// 上游自己就把 [1m] 写进 id 的：不要再叠一层。
				{ID: "some-model[1m]", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, InMenu: true},
			},
		}},
		Routes: []domain.Route{{
			ID: "r1", Name: "R1", ProviderID: "p1",
			OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true,
		}},
		APIKeys: []domain.APIKey{{ID: "k1", Name: "K1", Key: "sk-gw", RouteID: "r1", Enabled: true}},
	})
	return NewServer(router, monitor.NewStore()), "sk-gw"
}

// 1M 窗口必须真的告诉 Desktop：它判「这是不是 1M 模型」看的是
// max_input_tokens（app.asar 2.7032），而我们过去只发 context_length，于是
// 每个模型在它眼里都是短窗口，Code 页永远跑 200K。
//
// 同时：1M 的模型按 "[1m]" 的 id 列出去。不带这个标记的话，Desktop 会在菜单里
// 给同一个模型再补一条「1M context window」，一个模型变成两条（magpie #1272：
// 21 个模型 34 条）。
func TestOpenAIModelsGivesClaudeDesktopA1MWindow(t *testing.T) {
	server, token := desktop1MServer(t)
	data := getModels(t, server, token, claudeDesktopUA)
	if len(data) != 8 {
		t.Fatalf("got %d entries, want two defaults and one per model: %v", len(data), data)
	}
	// 真实模型 id -> 它在 Desktop 清单里的那一行。菜单显示的是 display_name，
	// 我们给 id 加的 "[1m]" 不进名字，所以它正好就是真实 id。
	byName := map[string]map[string]any{}
	for _, entry := range data {
		name, _ := entry["display_name"].(string)
		if _, dup := byName[name]; dup {
			t.Fatalf("one model listed twice to Desktop: %q in %v", name, data)
		}
		byName[name] = entry
	}

	long := []string{"claude-opus-5", "m-long", "m-edge", "some-model[1m]"}
	for _, name := range long {
		entry := byName[name]
		if entry == nil {
			t.Fatalf("%q missing from the Desktop list", name)
		}
		id, _ := entry["id"].(string)
		if !strings.HasSuffix(id, claudeContextTag1M) {
			t.Fatalf("%q listed as %q, want a [1m] id so Desktop's Code tab runs the 1M window", name, id)
		}
		if strings.HasSuffix(id, claudeContextTag1M+claudeContextTag1M) {
			t.Fatalf("%q listed as %q: the tag must not be doubled", name, id)
		}
		if !desktopAcceptsModelID(stripClaudeContextTag(id)) {
			t.Fatalf("Desktop would drop this id: %q", id)
		}
		if said, _ := entry["description"].(string); said != desktop1MSaid {
			t.Fatalf("%q description = %q, want %q (Desktop tells its own 1M entry that way)", name, said, desktop1MSaid)
		}
	}
	// 三个名字都要给：Desktop 看 max_input_tokens，别的客户端看 context_length，
	// 而 magpie 的 modelObject 三个都发，我们一起对齐。
	for _, name := range long {
		for _, field := range []string{"max_input_tokens", "context_length", "context_window"} {
			got, _ := byName[name][field].(float64)
			if got < desktop1M {
				t.Fatalf("%s.%s = %v, want >= %d", name, field, got, desktop1M)
			}
		}
	}

	for _, name := range []string{"m-short", "m-plain"} {
		entry := byName[name]
		id, _ := entry["id"].(string)
		if strings.HasSuffix(id, claudeContextTag1M) {
			t.Fatalf("%q listed as %q, but it has no 1M window", name, id)
		}
		if said, _ := entry["description"].(string); said != "" {
			t.Fatalf("%q described as %q, want no 1M claim", name, said)
		}
	}
	// 872K 是它自己的数，不能被抹平成默认值。
	if got, _ := byName["m-short"]["max_input_tokens"].(float64); got != 872_000 {
		t.Fatalf("m-short max_input_tokens = %v, want its own 872000", got)
	}
}

// 1M 标记是窗口标记，不是模型名：请求带着它回来（Desktop 的 Code 页会把列出去
// 的 id 原样交给 Claude Code，会话里存下来的也是它）时必须还原成真实模型。
// 目录里真有这个带标记的 id 时保留原样——那是一个真实的模型名。
func TestDesktop1MIDResolvesToTheModel(t *testing.T) {
	provider := domain.Provider{
		ID: "p1", Protocol: domain.ProtocolOpenAIChat,
		Models: []domain.Model{
			{ID: "claude-opus-5", ProviderID: "p1"},
			{ID: "m1", ProviderID: "p1"},
		},
	}
	for _, tc := range []struct{ request, want, wantLog string }{
		// 只是多了个窗口标记：同一个模型，日志里不该多出一次「映射」。
		{"claude-opus-5[1m]", "claude-opus-5", "claude-opus-5"},
		// 别名是真的换了个 id：日志两侧都要留。
		{desktopModelAlias("m1") + claudeContextTag1M, "m1", desktopModelAlias("m1") + claudeContextTag1M + " -> m1"},
	} {
		model, logModel := resolveConsumerModelOnProvider(provider, domain.APIKey{}, false, tc.request)
		if model != tc.want {
			t.Fatalf("%q resolved to %q, want %q", tc.request, model, tc.want)
		}
		if logModel != tc.wantLog {
			t.Fatalf("%q logged as %q, want %q", tc.request, logModel, tc.wantLog)
		}
	}

	// 上游真的有一个叫 "vendor-model[1m]" 的模型时，它自己就是那个模型名。
	tagged := domain.Provider{ID: "p2", Models: []domain.Model{{ID: "vendor-model[1m]", ProviderID: "p2"}}}
	if model, _ := resolveConsumerModelOnProvider(tagged, domain.APIKey{}, false, "vendor-model[1m]"); model != "vendor-model[1m]" {
		t.Fatalf("a real [1m] model id must not be stripped: %q", model)
	}
}

// 走完整的 Claude 透传：Desktop 的 Code 页会拿 /v1/models 里那个 "[1m]" 的 id
// 直接发 /v1/messages，上游必须收到真实模型名，并带上 1M 上下文那个 beta
// （new-api 风格的中转不带它会 400）。
func TestClaudeMessagesAcceptsDesktop1MID(t *testing.T) {
	var gotModel string
	var gotBeta string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBeta = r.Header.Get("anthropic-beta")
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		gotModel, _ = parsed["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude-opus-5","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()

	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "p1", Name: "P1", Protocol: domain.ProtocolClaude,
			AuthType: domain.AuthTypeSelfRegister, BaseURL: upstream.URL,
			APIKeySource: "literal:sk-test", AuthHeader: "x-api-key",
			Models: []domain.Model{
				{ID: "claude-opus-5", ProviderID: "p1", Protocol: domain.ProtocolClaude, InMenu: true},
				{ID: "m1", ProviderID: "p1", Protocol: domain.ProtocolClaude, InMenu: true},
			},
			// 有默认模型才能证明请求没被悄悄换成它。
			DefaultModel: "m1",
		}},
		Routes: []domain.Route{{
			ID: "r1", Name: "R1", ProviderID: "p1",
			OutputProtocol: domain.ProtocolClaude, Enabled: true,
		}},
		APIKeys: []domain.APIKey{{ID: "k1", Name: "K1", Key: "sk-gw", RouteID: "r1", Enabled: true}},
	})
	server := NewServer(router, monitor.NewStore())

	// Desktop 把列出去的 id 原样交给 Claude Code：claude-opus-5 本来就被接受，
	// m1 拿到的是别名；两者发回来都带 [1m]。
	for _, sent := range []struct{ request, want string }{
		{"claude-opus-5" + claudeContextTag1M, "claude-opus-5"},
		{desktopModelAlias("m1") + claudeContextTag1M, "m1"},
	} {
		gotModel = ""
		gotBeta = ""
		body := `{"model":"` + sent.request + `","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
		request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		request.Header.Set("x-api-key", "sk-gw")
		request.Header.Set("anthropic-version", "2023-06-01")
		recorder := httptest.NewRecorder()
		server.handleClaudeMessages(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body = %s", sent.request, recorder.Code, recorder.Body.String())
		}
		if gotModel != sent.want {
			t.Fatalf("%s reached the upstream as %q, want %q", sent.request, gotModel, sent.want)
		}
		// 1M 的 id 到了上游是真实模型名，beta 就得按真实模型判：Opus 5 是 1M 家族。
		if !isClaude1MContextModel(sent.want) {
			continue
		}
		if !strings.Contains(gotBeta, claudeContext1MBeta) {
			t.Fatalf("%s: anthropic-beta %q must carry %q", sent.request, gotBeta, claudeContext1MBeta)
		}
	}
}
