package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// CloudBase 的 Base URL 必须从环境域名和 /v1/ai/<provider> 路径共同识别；
// 普通 tcloudbasegateway 域名下的其它路径不能误走厂商专用模型目录。
func TestTencentCloudBaseProviderGroup(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		base   string
		group  string
		accept bool
	}{
		{"cloudbase", "https://dev-d5gze50axaadb179e.api.tcloudbasegateway.com/v1/ai/cloudbase", "cloudbase", true},
		{"responses endpoint", "https://env.api.tcloudbasegateway.com/v1/ai/cloudbase/responses", "cloudbase", true},
		{"other path", "https://env.api.tcloudbasegateway.com/v1/functions", "", false},
		{"plain relay", "https://relay.example.com/v1/ai/cloudbase", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			group, ok := tencentCloudBaseProviderGroup(domain.Provider{BaseURL: tc.base})
			if ok != tc.accept || group != tc.group {
				t.Fatalf("got group=%q ok=%v, want group=%q ok=%v", group, ok, tc.group, tc.accept)
			}
		})
	}
}

func TestTencentCloudBaseProxyFunctionURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		base   string
		want   string
		accept bool
	}{
		{"aiProxy", "https://dev.api.tcloudbasegateway.com/v1/functions/aiProxy", "https://dev.api.tcloudbasegateway.com/v1/functions/aiProxy", true},
		{"trailing slash and query", "https://dev.api.tcloudbasegateway.com/v1/functions/aiProxy/?x=1", "https://dev.api.tcloudbasegateway.com/v1/functions/aiProxy", true},
		{"case insensitive", "https://dev.api.tcloudbasegateway.com/v1/functions/aiproxy", "https://dev.api.tcloudbasegateway.com/v1/functions/aiproxy", true},
		{"other function", "https://dev.api.tcloudbasegateway.com/v1/functions/other", "", false},
		{"extra path", "https://dev.api.tcloudbasegateway.com/v1/functions/aiProxy/chat", "", false},
		{"other host", "https://relay.example.com/v1/functions/aiProxy", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tencentCloudBaseProxyFunctionURL(domain.Provider{BaseURL: tc.base})
			if ok != tc.accept || got != tc.want {
				t.Fatalf("got url=%q ok=%v, want url=%q ok=%v", got, ok, tc.want, tc.accept)
			}
		})
	}
}

func TestFetchTencentCloudBaseProxyModels(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer jwt-test" {
			t.Fatalf("Authorization = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]string
		if err := json.Unmarshal(body, &payload); err != nil || payload["action"] != "ping" {
			t.Fatalf("body = %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"envId":"dev","models":["hy4-flash","hy3","hy4-flash"],"protocols":["chat","responses","claude"]}`)
	}))
	defer upstream.Close()

	provider := domain.Provider{
		ID:           "cloudbase",
		Protocol:     domain.ProtocolOpenAIChat,
		AuthType:     domain.AuthTypeAPIKey,
		AuthHeader:   "Authorization",
		APIKeySource: "literal:jwt-test",
	}
	models, protocols, err := fetchTencentCloudBaseProxyModels(context.Background(), provider, upstream.URL)
	if err != nil {
		t.Fatalf("fetch models: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v, want 2 unique ids", models)
	}
	if models[0].ID != "hy4-flash" || models[0].ContextLength != 1_000_000 || models[0].MaxOutputTokens != 64_000 || models[0].Protocol != domain.ProtocolOpenAIChat {
		t.Fatalf("unexpected hy4-flash model: %+v", models[0])
	}
	if models[1].ID != "hy3" || models[1].ContextLength != 256_000 || models[1].MaxOutputTokens != 16_384 {
		t.Fatalf("unexpected hy3 model: %+v", models[1])
	}
	if got := strings.Join(protocols, ","); got != "chat,responses,claude" {
		t.Fatalf("protocols = %q", got)
	}
}

func TestNewRouterNormalizesTencentCloudBaseProxyProtocol(t *testing.T) {
	t.Parallel()
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{
		{
			ID:       "cloudbase-proxy",
			Name:     "CloudBase proxy",
			Protocol: domain.ProtocolOpenAIResponses,
			BaseURL:  "https://dev.api.tcloudbasegateway.com/v1/functions/aiProxy",
			Models: []domain.Model{{
				ID: "hy3", ProviderID: "cloudbase-proxy", Protocol: domain.ProtocolOpenAIResponses,
			}},
		},
		{
			ID:       "responses-relay",
			Name:     "Responses relay",
			Protocol: domain.ProtocolOpenAIResponses,
			BaseURL:  "https://relay.example.com/v1",
		},
	}})
	provider, err := router.ProviderByID("cloudbase-proxy")
	if err != nil {
		t.Fatal(err)
	}
	if provider.Protocol != domain.ProtocolOpenAIChat {
		t.Fatalf("aiProxy protocol = %q, want openai_chat", provider.Protocol)
	}
	if len(provider.Models) != 1 || provider.Models[0].Protocol != domain.ProtocolOpenAIChat {
		t.Fatalf("aiProxy models were not normalized: %+v", provider.Models)
	}
	relay, err := router.ProviderByID("responses-relay")
	if err != nil {
		t.Fatal(err)
	}
	if relay.Protocol != domain.ProtocolOpenAIResponses {
		t.Fatalf("unrelated relay protocol = %q, want openai_responses", relay.Protocol)
	}
}

// 官方目录里文本/图片、在线/下线、不同 provider 组混在一起；解析时必须只
// 留下目标组的在线文本模型，并把 1M、960k 这种窗口写法换算成整数。
func TestParseTencentCloudBaseModelCatalog(t *testing.T) {
	t.Parallel()
	body := []byte(`[
	  {"modelName":"hy4","groupName":"cloudbase","modelType":"text","status":"online","modelBrand":{"cn":"混元"},"modelSpec":{"contextLength":"1M","maxInputToken":"960k","maxOutputToken":"30k"}},
	  {"modelName":"hy3","groupName":"cloudbase","modelType":"text","status":"deprecating","modelSpec":{"contextLength":"256k","maxInputToken":"192k","maxOutputToken":"128k"}},
	  {"modelName":"offline-model","groupName":"cloudbase","modelType":"text","status":"offline","modelSpec":{"contextLength":"1M"}},
	  {"modelName":"image-model","groupName":"cloudbase","modelType":"image","status":"online","modelSpec":{"contextLength":"1M"}},
	  {"modelName":"other-group","groupName":"hunyuan-v3","modelType":"text","status":"online","modelSpec":{"contextLength":"1M"}}
	]`)
	provider := domain.Provider{
		ID:       "tencent-cloud",
		Protocol: domain.ProtocolOpenAIResponses,
		BaseURL:  "https://env.api.tcloudbasegateway.com/v1/ai/cloudbase",
	}
	models, err := parseTencentCloudBaseModelCatalog(body, provider, "cloudbase")
	if err != nil {
		t.Fatalf("parse catalog: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models=%d, want 2: %+v", len(models), models)
	}
	if models[0].ID != "hy4" || models[0].ContextLength != 1_000_000 || models[0].MaxOutputTokens != 30_000 || models[0].ProviderID != provider.ID || models[0].Protocol != provider.Protocol {
		t.Fatalf("unexpected first model: %+v", models[0])
	}
	if models[1].ID != "hy3" || models[1].ContextLength != 256_000 || models[1].MaxOutputTokens != 128_000 {
		t.Fatalf("unexpected second model: %+v", models[1])
	}
}

// 模型发现本身必须能通过可替换的 catalog URL 跑通，避免以后又退回通用的
// <baseUrl>/models 并重新得到 AI_MODEL_NOT_SUPPORTED。
func TestFetchTencentCloudBaseModels(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" {
			t.Fatalf("Accept=%q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
		  {"modelName":"deepseek-v4.1-flash","groupName":"cloudbase","modelType":"text","status":"online","modelSpec":{"contextLength":"1M","maxOutputToken":"384k"}}
		]`))
	}))
	defer upstream.Close()
	provider := domain.Provider{
		ID:       "cloudbase",
		Protocol: domain.ProtocolOpenAIChat,
		BaseURL:  "https://env.api.tcloudbasegateway.com/v1/ai/cloudbase",
	}
	models, err := fetchTencentCloudBaseModels(context.Background(), provider, upstream.URL)
	if err != nil {
		t.Fatalf("fetch models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "deepseek-v4.1-flash" || models[0].ContextLength != 1_000_000 || models[0].MaxOutputTokens != 384_000 {
		t.Fatalf("unexpected models: %+v", models)
	}
}
