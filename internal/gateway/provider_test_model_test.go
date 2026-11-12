package gateway

import (
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 「测试用哪个模型」的顺序：指定的 → 默认模型 → 目录第一个 → 占位符。
//
// 第三档是关键：默认模型为空是很常见的状态（刚建好、还没手动选），而目录就是
// 「这台上游能服务什么」的权威答案。跳过它直接发 "request-model-not-set"，换来
// 的是上游一句莫名其妙的 400/404 —— 看起来像 provider 坏了，其实是网关自己编了
// 一个不存在的模型名。
func TestProviderTestModelFallsBackToCatalog(t *testing.T) {
	t.Parallel()
	provider := domain.Provider{
		ID:     "p1",
		Models: []domain.Model{{ID: "beam-free"}, {ID: "deepseek-v4-pro"}},
	}

	if got := providerTestModel(provider, "explicit"); got != "explicit" {
		t.Fatalf("指定了就用指定的，got %q", got)
	}

	provider.DefaultModel = "chosen-default"
	if got := providerTestModel(provider, ""); got != "chosen-default" {
		t.Fatalf("没有指定时先用默认模型，got %q", got)
	}

	// 默认模型为空 → 目录第一个，而不是占位符。
	provider.DefaultModel = ""
	if got := providerTestModel(provider, ""); got != "beam-free" {
		t.Fatalf("默认模型为空时应当用目录第一个，got %q", got)
	}

	// 目录也空 → 只能退到占位符。
	if got := providerTestModel(domain.Provider{}, ""); got != "request-model-not-set" {
		t.Fatalf("全都为空时才用占位符，got %q", got)
	}
}

// 目录刷新后要顺手补上默认模型；已经有值的不许覆盖。
func TestUpdateProviderModelsFillsDefaultModel(t *testing.T) {
	t.Parallel()
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "p-empty", Name: "empty", Protocol: domain.ProtocolClaude,
		}, {
			ID: "p-has", Name: "has", Protocol: domain.ProtocolClaude, DefaultModel: "mine",
		}},
	})

	saved, err := router.UpdateProviderModels("p-empty",
		[]domain.Model{{ID: "beam-free"}, {ID: "second"}}, "healthy")
	if err != nil {
		t.Fatalf("UpdateProviderModels: %v", err)
	}
	if saved.DefaultModel != "beam-free" {
		t.Fatalf("默认模型应当补成目录第一个，got %q", saved.DefaultModel)
	}

	saved, err = router.UpdateProviderModels("p-has",
		[]domain.Model{{ID: "beam-free"}}, "healthy")
	if err != nil {
		t.Fatalf("UpdateProviderModels: %v", err)
	}
	if saved.DefaultModel != "mine" {
		t.Fatalf("运维自己选的默认模型不能被覆盖，got %q", saved.DefaultModel)
	}
}
