package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 腾讯云 CloudBase 的 AI 网关只提供对话端点，不提供标准 GET /models。
// 控制台自己展示的模型目录来自这个公开 JSON；它同时带上下文和最大输出，
// 比让用户手抄模型 ID 更可靠。目录是厂商级公开数据，不包含环境 Key。
const tencentCloudBaseModelCatalogURL = "https://tcb.cloud.tencent.com/ai/ai-model/production/ap-shanghai/config.json"

// tencentCloudBaseCatalogModel 只声明模型发现需要的字段。modelSpec 里的窗口
// 字段在 CloudBase 目录中可能是 "1M" / "960k" 文本，也可能是数字，因此先用
// RawMessage 接住，交给 parseTencentCloudBaseTokenLimit 统一解释。
type tencentCloudBaseCatalogModel struct {
	ModelName  string `json:"modelName"`
	GroupName  string `json:"groupName"`
	ModelType  string `json:"modelType"`
	Status     string `json:"status"`
	ModelBrand struct {
		CN string `json:"cn"`
		EN string `json:"en"`
	} `json:"modelBrand"`
	ModelSpec struct {
		ContextLength  json.RawMessage `json:"contextLength"`
		MaxInputToken  json.RawMessage `json:"maxInputToken"`
		MaxOutputToken json.RawMessage `json:"maxOutputToken"`
	} `json:"modelSpec"`
}

// tencentCloudBaseProviderGroup 从 CloudBase 的 Base URL 中取出 provider 组：
// https://<env>.api.tcloudbasegateway.com/v1/ai/cloudbase -> cloudbase。
// 只认这个域名和 /v1/ai/ 前缀，避免把普通 OpenAI 兼容中转也误判成 CloudBase。
func tencentCloudBaseProviderGroup(provider domain.Provider) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(provider.BaseURL))
	if err != nil {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "api.tcloudbasegateway.com" && !strings.HasSuffix(host, ".api.tcloudbasegateway.com") {
		return "", false
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for index := 0; index+2 < len(segments); index++ {
		if strings.EqualFold(segments[index], "v1") && strings.EqualFold(segments[index+1], "ai") {
			group := strings.TrimSpace(segments[index+2])
			if group != "" {
				return group, true
			}
		}
	}
	return "", false
}

// fetchTencentCloudBaseModels 读取 CloudBase 官方目录并按 Base URL 的 provider
// 组过滤。catalogURL 单独传入是为了让测试可以用本地 httptest 校验解析流程。
func fetchTencentCloudBaseModels(ctx context.Context, provider domain.Provider, catalogURL string) ([]domain.Model, error) {
	group, ok := tencentCloudBaseProviderGroup(provider)
	if !ok {
		return nil, fmt.Errorf("provider baseUrl is not a Tencent CloudBase AI endpoint")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, catalogURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("CloudBase model catalog returned HTTP %d", response.StatusCode)
	}
	return parseTencentCloudBaseModelCatalog(body, provider, group)
}

// parseTencentCloudBaseModelCatalog 把公开目录转成网关模型表。只收当前可用的
// 文本模型；图片、离线、待上线以及其它 provider 组的模型不会混进来。
func parseTencentCloudBaseModelCatalog(body []byte, provider domain.Provider, group string) ([]domain.Model, error) {
	var catalog []tencentCloudBaseCatalogModel
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil, fmt.Errorf("decode CloudBase model catalog: %w", err)
	}
	seen := make(map[string]bool)
	models := make([]domain.Model, 0)
	for _, item := range catalog {
		if !strings.EqualFold(strings.TrimSpace(item.GroupName), strings.TrimSpace(group)) {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(item.ModelType), "text") {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(item.Status))
		if status != "online" && status != "deprecating" {
			continue
		}
		id := strings.TrimSpace(item.ModelName)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		reportedContext := parseTencentCloudBaseTokenLimit(item.ModelSpec.ContextLength)
		if reportedContext == 0 {
			reportedContext = parseTencentCloudBaseTokenLimit(item.ModelSpec.MaxInputToken)
		}
		models = append(models, domain.Model{
			ID:              id,
			ProviderID:      provider.ID,
			Name:            strings.TrimSpace(item.ModelBrand.CN),
			Protocol:        provider.Protocol,
			ContextLength:   resolveModelContextLength(id, reportedContext),
			MaxOutputTokens: parseTencentCloudBaseTokenLimit(item.ModelSpec.MaxOutputToken),
			InMenu:          true,
		})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("CloudBase model catalog has no available text model in group %q", group)
	}
	return models, nil
}

// parseTencentCloudBaseTokenLimit 解析 CloudBase 的窗口写法，例如 960k、1M、
// 1000k 或纯数字。无法识别的值返回 0，由调用方决定是否回退或保持未知。
func parseTencentCloudBaseTokenLimit(raw json.RawMessage) int {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return 0
	}
	var stringValue string
	if err := json.Unmarshal(raw, &stringValue); err == nil {
		normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(stringValue), " ", ""))
		if normalized == "" {
			return 0
		}
		multiplier := float64(1)
		switch normalized[len(normalized)-1] {
		case 'k':
			multiplier = 1_000
			normalized = normalized[:len(normalized)-1]
		case 'm':
			multiplier = 1_000_000
			normalized = normalized[:len(normalized)-1]
		}
		value, err := strconv.ParseFloat(normalized, 64)
		if err != nil || value <= 0 {
			return 0
		}
		return int(value * multiplier)
	}
	var numberValue float64
	if err := json.Unmarshal(raw, &numberValue); err == nil && numberValue > 0 {
		return int(numberValue)
	}
	return 0
}
