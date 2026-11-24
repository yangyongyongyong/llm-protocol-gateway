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

// tencentCloudBaseProxyPath 是 CloudBase 云函数 HTTP 入口。成长计划连接器使用
// aiProxy 云函数转发 SDK 调用；它没有标准 GET /models，但 action=ping 会返回
// 当前允许的模型和协议。识别到这条入口时不能把请求格式猜成 Responses。
const tencentCloudBaseProxyFunction = "aiProxy"

type tencentCloudBaseModelBudget struct {
	contextLength   int
	maxOutputTokens int
}

// 与 connectors/tencent-cloudbase-growth/adapter.py 的 MODELS 元数据保持一致。
// ping 只返回模型 ID，这里补上控制台窗口和输出上限，避免所有模型都落成默认 128K。
var tencentCloudBaseGrowthModelBudgets = map[string]tencentCloudBaseModelBudget{
	"hy3":                      {contextLength: 256_000, maxOutputTokens: 16_384},
	"hy3-preview":              {contextLength: 256_000, maxOutputTokens: 16_384},
	"hy4-flash":                {contextLength: 1_000_000, maxOutputTokens: 64_000},
	"hy4":                      {contextLength: 1_000_000, maxOutputTokens: 16_384},
	"hy4-preview":              {contextLength: 1_000_000, maxOutputTokens: 16_384},
	"deepseek-v4.1-flash":      {contextLength: 1_000_000, maxOutputTokens: 16_384},
	"deepseek-v4.1-flash-1017": {contextLength: 1_000_000, maxOutputTokens: 16_384},
}

type tencentCloudBaseProxyPing struct {
	OK        bool     `json:"ok"`
	EnvID     string   `json:"envId"`
	Models    []string `json:"models"`
	Protocols []string `json:"protocols"`
	Error     *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

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

// tencentCloudBaseProxyFunctionURL 识别 CloudBase HTTP 云函数入口：
// https://<env>.api.tcloudbasegateway.com/v1/functions/aiProxy。
// 只认 aiProxy 这个自有函数契约，不去猜其它 CloudBase 函数的模型接口。
func tencentCloudBaseProxyFunctionURL(provider domain.Provider) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(provider.BaseURL))
	if err != nil || parsed.Host == "" {
		return "", false
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "api.tcloudbasegateway.com" && !strings.HasSuffix(host, ".api.tcloudbasegateway.com") {
		return "", false
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) < 3 ||
		!strings.EqualFold(segments[len(segments)-3], "v1") ||
		!strings.EqualFold(segments[len(segments)-2], "functions") ||
		!strings.EqualFold(segments[len(segments)-1], tencentCloudBaseProxyFunction) {
		return "", false
	}
	parsed.Path = "/" + strings.Join(segments, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), true
}

// normalizeTencentCloudBaseProxyProvider 修正直连 aiProxy 的协议声明。云函数
// 的 event 契约是 Chat（model + messages），不是 Responses；声明错会导致请求
// 带着 input 进入函数后得到 "messages is required"。Gateway 负责把 Responses /
// Claude 请求转换成这条 Chat 上游可接收的形状。
func normalizeTencentCloudBaseProxyProvider(provider *domain.Provider) {
	if provider == nil || provider.ProviderScript != nil || provider.CustomProviderID != "" {
		return
	}
	if provider.AuthType != "" && provider.AuthType != domain.AuthTypeAPIKey {
		return
	}
	if _, ok := tencentCloudBaseProxyFunctionURL(*provider); !ok {
		return
	}
	provider.Protocol = domain.ProtocolOpenAIChat
	for index := range provider.Models {
		provider.Models[index].ProviderID = provider.ID
		provider.Models[index].Protocol = provider.Protocol
	}
}

// fetchTencentCloudBaseProxyModels 用云函数自带的无额度 ping 动作读取模型目录。
// functionURL 独立传入，方便测试用 httptest 校验请求方法、鉴权和返回解析。
func fetchTencentCloudBaseProxyModels(ctx context.Context, provider domain.Provider, functionURL string) ([]domain.Model, []string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, functionURL, strings.NewReader(`{"action":"ping"}`))
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	applyRequestAdapterHeaders(request, provider, "")
	applyProviderAuth(request, provider, "")

	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
	if err != nil {
		return nil, nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("aiProxy ping returned HTTP %d: %s", response.StatusCode, compactResponsePreview(body))
	}
	var ping tencentCloudBaseProxyPing
	if err := json.Unmarshal(body, &ping); err != nil {
		return nil, nil, fmt.Errorf("decode aiProxy ping: %w", err)
	}
	if ping.Error != nil {
		code := strings.TrimSpace(ping.Error.Code)
		message := strings.TrimSpace(ping.Error.Message)
		if code == "" {
			code = "UPSTREAM_ERROR"
		}
		if message == "" {
			message = "aiProxy ping failed"
		}
		return nil, ping.Protocols, fmt.Errorf("%s: %s", code, message)
	}
	if !ping.OK {
		return nil, ping.Protocols, fmt.Errorf("aiProxy ping did not report ok=true")
	}

	seen := make(map[string]bool)
	models := make([]domain.Model, 0, len(ping.Models))
	for _, rawID := range ping.Models {
		id := strings.TrimSpace(rawID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		budget := tencentCloudBaseGrowthModelBudgets[strings.ToLower(id)]
		models = append(models, domain.Model{
			ID:              id,
			ProviderID:      provider.ID,
			Protocol:        provider.Protocol,
			ContextLength:   resolveModelContextLength(id, budget.contextLength),
			MaxOutputTokens: budget.maxOutputTokens,
			InMenu:          true,
		})
	}
	if len(models) == 0 {
		return nil, ping.Protocols, fmt.Errorf("aiProxy ping returned no model ids")
	}
	return models, ping.Protocols, nil
}

func compactResponsePreview(body []byte) string {
	preview := strings.TrimSpace(strings.ReplaceAll(string(body), "\n", " "))
	preview = strings.ReplaceAll(preview, "\r", " ")
	if len(preview) > 300 {
		return preview[:300]
	}
	return preview
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
