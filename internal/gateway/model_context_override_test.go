package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// contextOverrideServer 起一个「上游什么都不说」的网关：m1 没有任何线索，
// 厂商表里也查不到，于是它默认就是 128K——正是中转模型要手工声明窗口的场景。
func contextOverrideServer(t *testing.T, models ...domain.Model) *Server {
	t.Helper()
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "p1", Name: "P1", Protocol: domain.ProtocolOpenAIChat,
			AuthType: domain.AuthTypeAPIKey, BaseURL: "https://example.invalid",
			Models: models,
		}},
		Routes: []domain.Route{{
			ID: "r1", Name: "R1", ProviderID: "p1",
			OutputProtocol: domain.ProtocolOpenAIChat, Mode: domain.RouteModeAuto, Enabled: true,
		}},
		APIKeys: []domain.APIKey{{ID: "k1", Name: "K1", Key: "sk-gw", RouteID: "r1", Enabled: true}},
	})
	return NewServer(router, monitor.NewStore())
}

func setModelContext(t *testing.T, server *Server, providerID, model string, n int) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"model": model, "contextLength": n})
	request := httptest.NewRequest(http.MethodPost, "/__providers/"+providerID+"/model-context", strings.NewReader(string(body)))
	request.SetPathValue("id", providerID)
	recorder := httptest.NewRecorder()
	server.handleSetProviderModelContext(recorder, request)
	return recorder
}

func modelByID(t *testing.T, state domain.GatewayState, id string) domain.Model {
	t.Helper()
	for _, model := range state.Models {
		if strings.EqualFold(model.ID, id) {
			return model
		}
	}
	t.Fatalf("%q missing from the catalog", id)
	return domain.Model{}
}

// 运维说这个模型有 1M，它就真的是 1M——而且对外（目录、/v1/models、Claude
// Desktop 那一份）都按这个数说。模型自己存的 ContextLength 仍是「自动值」：
// 清空之后要能原样退回去，所以覆盖值不能盖在上面。
func TestModelContextOverrideReachesEveryCatalog(t *testing.T) {
	server := contextOverrideServer(t,
		domain.Model{ID: "m1", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, InMenu: true},
	)
	before := modelByID(t, server.router.State(), "m1")
	if before.ContextLength != contextLengthDefault {
		t.Fatalf("before the override m1 = %d, want the generic default %d", before.ContextLength, contextLengthDefault)
	}

	if recorder := setModelContext(t, server, "p1", "m1", contextLength1M); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	model := modelByID(t, server.router.State(), "m1")
	if model.ContextOverride != contextLength1M {
		t.Fatalf("override = %d, want %d", model.ContextOverride, contextLength1M)
	}
	// ContextLength 留着自动值：清空时靠它退回去（见下一个用例）。
	if model.ContextLength != before.ContextLength {
		t.Fatalf("automatic window = %d, want it untouched at %d", model.ContextLength, before.ContextLength)
	}
	// 对外宣称的是覆盖值，输出上限也跟着它走：以前按 128K 算成 64K。
	if got := modelWindow(model); got != contextLength1M {
		t.Fatalf("effective window = %d, want %d", got, contextLength1M)
	}
	if got := model.MaxOutputTokens; got != resolveModelMaxOutputTokens("m1", contextLength1M) {
		t.Fatalf("max output = %d, want the 1M budget", got)
	}

	// OpenAI 形态：普通客户端看 context_length。
	data := getModels(t, server, "sk-gw", "curl/8.7.1")
	if len(data) != 1 {
		t.Fatalf("catalog = %v, want one entry", data)
	}
	if got, _ := data[0]["context_length"].(float64); got != contextLength1M {
		t.Fatalf("context_length = %v, want %d", got, contextLength1M)
	}

	// Claude Desktop 那一份：1M 是它决定「要不要给 Code 页 1M 窗口」的依据。
	desktopData := getModels(t, server, "sk-gw", claudeDesktopUA)
	if len(desktopData) != 1 {
		t.Fatalf("Desktop list = %v, want one entry", desktopData)
	}
	if said, _ := desktopData[0]["max_input_tokens"].(float64); said < desktop1M {
		t.Fatalf("Desktop max_input_tokens = %v, want >= %d", said, desktop1M)
	}
	if id, _ := desktopData[0]["id"].(string); !strings.HasSuffix(id, claudeContextTag1M) {
		t.Fatalf("listed as %q, want a [1m] id", id)
	}

	// Claude Code 那一份也要一致，否则同一个模型在两端说两个窗口。
	request := httptest.NewRequest(http.MethodGet, "/anthropic/v1/models", nil)
	request.Header.Set("x-api-key", "sk-gw")
	recorder := httptest.NewRecorder()
	server.handleClaudeModels(recorder, request)
	var payload struct {
		Data []struct {
			ID             string `json:"id"`
			MaxInputTokens int    `json:"max_input_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v (%s)", err, recorder.Body.String())
	}
	for _, entry := range payload.Data {
		if entry.ID != "m1" {
			continue
		}
		if entry.MaxInputTokens != contextLength1M {
			t.Fatalf("Claude discovery says m1 is %d, want %d", entry.MaxInputTokens, contextLength1M)
		}
		return
	}
	t.Fatalf("m1 missing from Claude discovery: %s", recorder.Body.String())
}

// 设成 0 就是「退回自动」，不是「窗口是 0」：清空之后必须回到厂商/上游那个数。
func TestModelContextOverrideClearsBackToTheVendor(t *testing.T) {
	server := contextOverrideServer(t,
		domain.Model{ID: "claude-opus-5", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, InMenu: true},
	)
	// 厂商已知 1M 的模型同样可以被按到 200K：套餐/中转真的截断时这是唯一说得
	// 出口的地方，而单靠上下文长度是表达不了「比厂商说的更小」的。
	if recorder := setModelContext(t, server, "p1", "claude-opus-5", contextLength200K); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := modelWindow(modelByID(t, server.router.State(), "claude-opus-5")); got != contextLength200K {
		t.Fatalf("window = %d, want the operator's %d", got, contextLength200K)
	}
	// 设了 200K 就不再是 1M，Desktop 那一侧也不该再拿 1M 的 id 去骗它。
	data := getModels(t, server, "sk-gw", claudeDesktopUA)
	if id, _ := data[0]["id"].(string); strings.HasSuffix(id, claudeContextTag1M) {
		t.Fatalf("listed as %q, but the operator cut it to 200K", id)
	}

	if recorder := setModelContext(t, server, "p1", "claude-opus-5", 0); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	model := modelByID(t, server.router.State(), "claude-opus-5")
	if model.ContextOverride != 0 {
		t.Fatalf("override = %d, want it cleared", model.ContextOverride)
	}
	if got := modelWindow(model); got != contextLength1M {
		t.Fatalf("window = %d, want the vendor's %d back", got, contextLength1M)
	}
}

// 目录同步整份换掉模型清单，但运维说过的话不能被冲掉——冲掉之后那个模型会从
// 1M 悄悄退回默认值，界面上看不出发生过什么。
func TestModelContextOverrideSurvivesCatalogSync(t *testing.T) {
	server := contextOverrideServer(t,
		domain.Model{ID: "m1", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, InMenu: true},
	)
	if recorder := setModelContext(t, server, "p1", "m1", contextLength1M); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	// 上游这次报了另一个窗口，还新上了一个模型：同步用的是全新的一份清单。
	if _, err := server.router.UpdateProviderModels("p1", []domain.Model{
		{ID: "m1", ContextLength: 32_000},
		{ID: "m2"},
	}, "healthy"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	model := modelByID(t, server.router.State(), "m1")
	if model.ContextOverride != contextLength1M {
		t.Fatalf("after sync override = %d, want the operator's 1M kept", model.ContextOverride)
	}
	if got := modelWindow(model); got != contextLength1M {
		t.Fatalf("after sync the window is %d, want 1M", got)
	}
	// 同步把自动值换成了上游这次报的数：清空覆盖值之后落到那个数上，不是原来的默认值。
	if model.ContextLength != 32_000 {
		t.Fatalf("automatic window = %d, want the freshly synced 32000", model.ContextLength)
	}
	// 新模型没有设置，按它自己的数走。
	if got := modelByID(t, server.router.State(), "m2"); got.ContextOverride != 0 || got.ContextLength != contextLengthDefault {
		t.Fatalf("m2 override=%d window=%d, want no override and the default", got.ContextOverride, got.ContextLength)
	}
	// 从清单里消失的模型，设置也不再留着。
	if _, err := server.router.UpdateProviderModels("p1", []domain.Model{{ID: "m2"}}, "healthy"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	for _, item := range server.router.State().Models {
		if strings.EqualFold(item.ID, "m1") {
			t.Fatal("m1 came back after the upstream dropped it")
		}
	}
}

// 控制台保存 Provider 时会把整份模型清单一起 PATCH 回来（弹窗里改个名字也算），
// 那次 PATCH 不能顺手把窗口设置删掉。
func TestProviderPatchKeepsModelContextOverride(t *testing.T) {
	server := contextOverrideServer(t,
		domain.Model{ID: "m1", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, InMenu: true},
	)
	if recorder := setModelContext(t, server, "p1", "m1", contextLength1M); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, err := server.router.UpdateProvider("p1", domain.Provider{
		Name:   "renamed",
		Models: []domain.Model{{ID: "m1", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, InMenu: true}},
	}); err != nil {
		t.Fatalf("patch: %v", err)
	}
	model := modelByID(t, server.router.State(), "m1")
	if model.ContextOverride != contextLength1M {
		t.Fatalf("override = %d after a rename, want %d", model.ContextOverride, contextLength1M)
	}
}

// 同步路径拿到的常常就是 state 里那一片模型（刷新授权会把 provider.Models
// 原样传回去），就地改写会绕开调用方对那份数据的预期。所以只有在真要写的时候
// 才复制一份，而且复制之后入参一个字节都不能变。
func TestCarryModelContextOverridesDoesNotMutateInput(t *testing.T) {
	incoming := []domain.Model{{ID: "m1"}, {ID: "m2"}}
	existing := []domain.Model{{ID: "m1", ContextOverride: contextLength1M}}
	out := carryModelContextOverrides(incoming, existing)

	if out[0].ContextOverride != contextLength1M {
		t.Fatalf("override did not carry: %+v", out)
	}
	for _, model := range incoming {
		if model.ContextOverride != 0 {
			t.Fatalf("the caller's slice was written through: %+v", incoming)
		}
	}
	// 没有要带的东西时原样返回，不做无谓的复制。
	same := []domain.Model{{ID: "m3"}}
	if got := carryModelContextOverrides(same, existing); &got[0] != &same[0] {
		t.Fatal("nothing to carry should not copy the slice")
	}
}

// 手滑写错一个 id、或写一个荒唐的数，都必须当场拒绝：窗口是客户端用来决定
// 什么时候压缩上下文的，静默接受一个不生效的值比报错糟得多。
func TestModelContextOverrideRejectsBadInput(t *testing.T) {
	server := contextOverrideServer(t,
		domain.Model{ID: "m1", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, InMenu: true},
	)
	for _, tc := range []struct {
		name     string
		model    string
		n        int
		wantCode int
	}{
		{"unknown model", "nope", contextLength1M, http.StatusNotFound},
		{"negative window", "m1", -1, http.StatusBadRequest},
		{"absurd window", "m1", maxModelContextOverride + 1, http.StatusBadRequest},
	} {
		if recorder := setModelContext(t, server, "p1", tc.model, tc.n); recorder.Code != tc.wantCode {
			t.Fatalf("%s: status = %d, want %d (%s)", tc.name, recorder.Code, tc.wantCode, recorder.Body.String())
		}
	}
	if got := modelByID(t, server.router.State(), "m1").ContextOverride; got != 0 {
		t.Fatalf("a rejected request still wrote %d", got)
	}
}
