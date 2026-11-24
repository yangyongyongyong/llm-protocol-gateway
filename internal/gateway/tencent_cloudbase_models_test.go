package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
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
