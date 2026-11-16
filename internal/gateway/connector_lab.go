package gateway

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Connector Lab 是给外部协议转换器（Ocean Adapter 这类）做全自动验收的
// 编排层。底层能力其实已经存在：POST /__providers、POST /__routes、
// POST /__apikeys、POST /__providers/{id}/test、POST /__routes/{id}/test。
// 缺的是把这几步放进一个带隔离命名、失败清理和完整报告的单一入口，否则
// 自动化脚本必须自己处理顺序、残留资源和“最后哪一步失败”。
//
// 本接口只编排现有模型，不引入第四种 Protocol：
//   Provider.Protocol 仍然是 openai_chat / openai_responses / claude 之一；
//   自定义 Connector 自己负责把私有协议转成其中至少一种。
//
// 安全边界：Lab 只创建临时 Provider/Route/API Key，不执行用户脚本，不读取
// Provider 的明文 Key。测试请求走网关自己的真实三协议入口，因此覆盖的是
// 用户实际会走的链路，而不是另写一套 mock。

const connectorLabIDPrefix = "connector-lab-"

type connectorLabRequest struct {
	Name string `json:"name,omitempty"`
	// Provider 是待验证的 Connector 实例。BaseURL/APIKeySource 由调用方填入，
	// Protocol 必须是三大协议之一。
	Provider domain.Provider `json:"provider"`
	// Protocols 是要跑的标准协议入口，默认按 Provider.Protocol 跑一条。
	// 可选 openai_chat / openai_responses / claude。
	Protocols []domain.Protocol `json:"protocols,omitempty"`
	// Model 不填时用 Provider.DefaultModel / 第一个模型 / request-model-not-set。
	Model string `json:"model,omitempty"`
	// Message 是每条协议实际发给模型的提示词。
	Message string `json:"message,omitempty"`
	// KeepOnFailure 为 true 时保留临时 Provider/Route/Key，返回 IDs 供排查；
	// 默认无论成功失败都清理，避免自动化把测试资源留在生产库里。
	KeepOnFailure bool `json:"keepOnFailure,omitempty"`
	// RunProviderConformance 额外跑 Provider 层的模型/非流式/流式/usage 检查。
	RunProviderConformance bool `json:"runProviderConformance,omitempty"`
}

type connectorLabStep struct {
	ID         string         `json:"id"`
	Title      string         `json:"title"`
	Passed     bool           `json:"passed"`
	Skipped    bool           `json:"skipped,omitempty"`
	Detail     string         `json:"detail,omitempty"`
	Hint       string         `json:"hint,omitempty"`
	LatencyMs  int64          `json:"latencyMs"`
	HTTPStatus int            `json:"httpStatus,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
}

type connectorLabReport struct {
	Success       bool               `json:"success"`
	RunID         string             `json:"runId"`
	ProviderID    string             `json:"providerId,omitempty"`
	RouteID       string             `json:"routeId,omitempty"`
	APIKeyID      string             `json:"apiKeyId,omitempty"`
	Protocols     []domain.Protocol  `json:"protocols,omitempty"`
	Steps         []connectorLabStep `json:"steps"`
	Cleaned       bool               `json:"cleaned"`
	KeptOnFailure bool               `json:"keptOnFailure,omitempty"`
	LatencyMs     int64              `json:"latencyMs"`
	Summary       string             `json:"summary"`
}

func (s *Server) handleConnectorLab(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if !s.requirePermission(w, r, domain.PermProvidersTest) {
		return
	}
	var payload connectorLabRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid connector lab json: "+err.Error())
		return
	}
	report := s.runConnectorLab(r, payload, started)
	status := http.StatusOK
	if !report.Success && len(report.Steps) > 0 && report.Steps[0].ID == "validate" {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, report)
}

func (s *Server) runConnectorLab(r *http.Request, payload connectorLabRequest, started time.Time) (report connectorLabReport) {
	runID := connectorLabRunID()
	report = connectorLabReport{
		RunID:     runID,
		Protocols: normalizeConnectorLabProtocols(payload.Protocols, payload.Provider.Protocol),
		Cleaned:   false,
	}
	var providerID, routeID, keyID string
	var providerCreated, routeCreated, keyCreated bool

	cleanup := func() {
		if payload.KeepOnFailure && !report.Success {
			report.KeptOnFailure = true
			return
		}
		var failures []string
		if keyCreated && keyID != "" {
			if err := s.router.DeleteAPIKey(keyID); err != nil && !strings.Contains(err.Error(), "not found") {
				failures = append(failures, "api key: "+err.Error())
			}
			keyCreated = false
		}
		if routeCreated && routeID != "" {
			if err := s.router.DeleteRoute(routeID); err != nil && !strings.Contains(err.Error(), "not found") {
				failures = append(failures, "route: "+err.Error())
			}
			routeCreated = false
		}
		if providerCreated && providerID != "" {
			if err := s.router.DeleteProvider(providerID); err != nil && !strings.Contains(err.Error(), "not found") {
				failures = append(failures, "delete provider: "+err.Error())
			}
			if err := s.router.PurgeProvider(providerID); err != nil && !strings.Contains(err.Error(), "not found") {
				failures = append(failures, "purge provider: "+err.Error())
			}
			providerCreated = false
		}
		if err := s.saveState(); err != nil {
			failures = append(failures, "save: "+err.Error())
		}
		report.Cleaned = len(failures) == 0
		if len(failures) > 0 {
			report.Steps = append(report.Steps, connectorLabStep{
				ID: "cleanup", Title: "清理临时资源", Passed: false,
				Detail: strings.Join(failures, "; "),
			})
		}
	}
	defer func() {
		report.Success = connectorLabAllRequiredPassed(report.Steps)
		cleanup()
		report.LatencyMs = time.Since(started).Milliseconds()
		if report.Success {
			report.Summary = "Connector Lab 全部通过"
		} else {
			report.Summary = "Connector Lab 有必过项失败"
		}
	}()

	provider := payload.Provider
	if strings.TrimSpace(provider.Name) == "" {
		provider.Name = "Connector Lab " + runID
	}
	if strings.TrimSpace(payload.Name) != "" {
		provider.Name = strings.TrimSpace(payload.Name)
	}
	if provider.ID == "" {
		provider.ID = connectorLabIDPrefix + runID
	}
	if !isConnectorLabProtocol(provider.Protocol) {
		report.Steps = append(report.Steps, connectorLabStep{
			ID: "validate", Title: "校验标准协议", Passed: false,
			Detail: fmt.Sprintf("protocol=%q 不是三大标准协议", provider.Protocol),
			Hint:   "Provider.Protocol 只能填 openai_chat / openai_responses / claude；私有协议请由 Connector 转成其中之一",
		})
		return report
	}
	if len(report.Protocols) == 0 {
		report.Steps = append(report.Steps, connectorLabStep{
			ID: "validate", Title: "校验测试协议", Passed: false,
			Detail: "protocols 为空",
			Hint:   "至少指定一种标准协议",
		})
		return report
	}
	report.Protocols = normalizeConnectorLabProtocols(report.Protocols, provider.Protocol)

	// Step 1: 创建临时 Provider。
	createStarted := time.Now()
	created, err := s.router.AddProvider(provider)
	if err != nil {
		report.Steps = append(report.Steps, connectorLabStep{
			ID: "create_provider", Title: "创建临时 Provider", Passed: false,
			Detail: err.Error(), Hint: "检查 Name/BaseURL/APIKeySource/Protocol",
			LatencyMs: time.Since(createStarted).Milliseconds(),
		})
		return report
	}
	providerID = created.ID
	providerCreated = true
	report.ProviderID = providerID
	report.Steps = append(report.Steps, connectorLabStep{
		ID: "create_provider", Title: "创建临时 Provider", Passed: true,
		LatencyMs: time.Since(createStarted).Milliseconds(),
		Data:      map[string]any{"providerId": created.ID, "protocol": created.Protocol},
	})

	// Step 2: 模型发现。模型目录是建议项，不阻塞三协议测试。
	testStarted := time.Now()
	testResult := s.fetchProviderModels(r, created, testStarted)
	modelOK := testResult.Success && len(testResult.Models) > 0
	modelStep := connectorLabStep{
		ID: "provider_models", Title: "Provider 模型发现", Passed: modelOK,
		LatencyMs: time.Since(testStarted).Milliseconds(), HTTPStatus: testResult.Status,
		Data: map[string]any{"models": len(testResult.Models), "preview": testResult.Preview},
	}
	if !modelOK {
		modelStep.Detail = firstNonEmpty(testResult.Error, testResult.Preview)
		modelStep.Hint = "实现 /models 或返回标准模型目录；若 Connector 不支持目录，可仅用 Provider.DefaultModel"
	}
	report.Steps = append(report.Steps, modelStep)
	if modelOK {
		_, _ = s.router.UpdateProviderModels(created.ID, testResult.Models, "healthy")
	}

	// Step 3: 创建 Route。
	routeStarted := time.Now()
	route, err := s.router.AddRoute(domain.Route{
		Name:           "Connector Lab " + runID,
		ProviderID:     created.ID,
		OutputProtocol: created.Protocol,
	})
	if err != nil {
		report.Steps = append(report.Steps, connectorLabStep{
			ID: "create_route", Title: "创建临时 Route", Passed: false,
			Detail: err.Error(), LatencyMs: time.Since(routeStarted).Milliseconds(),
		})
		return report
	}
	routeID = route.ID
	routeCreated = true
	report.RouteID = route.ID
	report.Steps = append(report.Steps, connectorLabStep{
		ID: "create_route", Title: "创建临时 Route", Passed: true,
		LatencyMs: time.Since(routeStarted).Milliseconds(),
		Data:      map[string]any{"routeId": route.ID, "outputProtocol": route.OutputProtocol},
	})

	// Step 4: 创建临时 API Key。
	keyStarted := time.Now()
	key, err := s.router.AddAPIKey(domain.APIKey{
		Name:          "connector-lab-" + runID,
		RouteID:       route.ID,
		StreamEnabled: true,
		Enabled:       true,
	})
	if err != nil {
		report.Steps = append(report.Steps, connectorLabStep{
			ID: "create_api_key", Title: "创建临时 API Key", Passed: false,
			Detail: err.Error(), LatencyMs: time.Since(keyStarted).Milliseconds(),
		})
		return report
	}
	keyID = key.ID
	keyCreated = true
	report.APIKeyID = key.ID
	report.Steps = append(report.Steps, connectorLabStep{
		ID: "create_api_key", Title: "创建临时 API Key", Passed: true,
		LatencyMs: time.Since(keyStarted).Milliseconds(),
		Data:      map[string]any{"apiKeyId": key.ID},
	})

	model := connectorLabModel(created, payload.Model)
	message := strings.TrimSpace(payload.Message)
	if message == "" {
		message = "请只回复 lab-ok"
	}

	// Step 5: 真实客户端入口逐协议测试。这里刻意不用 internal test header：
	// 验证临时 Key、Route、网关输出入口和上游 Connector 的完整链路。
	for _, protocol := range report.Protocols {
		report.Steps = append(report.Steps, s.runConnectorLabProtocol(r, protocol, route, created, key, model, message))
	}

	return report
}

func (s *Server) runConnectorLabProtocol(r *http.Request, protocol domain.Protocol, route domain.Route, provider domain.Provider, key domain.APIKey, model, message string) connectorLabStep {
	started := time.Now()
	step := connectorLabStep{
		ID: "protocol_" + string(protocol), Title: "真实入口 " + protocol.DisplayName(),
	}
	endpointURL, err := s.gatewayOutputURL(protocol)
	if err != nil {
		step.Detail = err.Error()
		return step
	}
	var payload map[string]any
	switch protocol {
	case domain.ProtocolOpenAIChat:
		payload = map[string]any{
			"model": model, "stream": false,
			"messages": []map[string]any{{"role": "user", "content": message}},
		}
	case domain.ProtocolOpenAIResponses:
		payload = map[string]any{"model": model, "stream": false, "input": message}
	case domain.ProtocolClaude:
		payload = map[string]any{
			"model": model, "max_tokens": 64, "stream": false,
			"messages": []map[string]any{{"role": "user", "content": message}},
		}
	default:
		step.Detail = "unsupported protocol"
		return step
	}
	body, err := json.Marshal(payload)
	if err != nil {
		step.Detail = err.Error()
		return step
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpointURL, bytes.NewReader(body))
	if err != nil {
		step.Detail = err.Error()
		return step
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	applyConnectorLabClientAuth(request, protocol, key.Key)
	client := &http.Client{Timeout: 120 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		step.Detail = err.Error()
		step.Hint = "检查 Connector 是否在运行、地址是否可达"
		step.LatencyMs = time.Since(started).Milliseconds()
		return step
	}
	defer response.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 256*1024))
	step.LatencyMs = time.Since(started).Milliseconds()
	step.HTTPStatus = response.StatusCode
	if readErr != nil {
		step.Detail = readErr.Error()
		return step
	}
	step.Passed = connectorLabResponseShapeOK(protocol, response.StatusCode, responseBody)
	step.Detail = connectorLabResponseSummary(responseBody)
	step.Data = map[string]any{
		"endpoint": endpointURL, "routeId": route.ID, "providerId": provider.ID,
		"model": model, "responsePreview": connectorLabPreview(responseBody),
	}
	if !step.Passed {
		step.Hint = connectorLabResponseHint(protocol)
	}
	return step
}

func connectorLabRunID() string {
	buf := make([]byte, 5)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func normalizeConnectorLabProtocols(input []domain.Protocol, fallback domain.Protocol) []domain.Protocol {
	if len(input) == 0 {
		if fallback != "" {
			return []domain.Protocol{fallback}
		}
		return nil
	}
	out := make([]domain.Protocol, 0, len(input))
	seen := map[domain.Protocol]bool{}
	for _, protocol := range input {
		if !isConnectorLabProtocol(protocol) || seen[protocol] {
			continue
		}
		seen[protocol] = true
		out = append(out, protocol)
	}
	return out
}

func isConnectorLabProtocol(protocol domain.Protocol) bool {
	switch protocol {
	case domain.ProtocolOpenAIChat, domain.ProtocolOpenAIResponses, domain.ProtocolClaude:
		return true
	default:
		return false
	}
}

func connectorLabModel(provider domain.Provider, requested string) string {
	if strings.TrimSpace(requested) != "" {
		return strings.TrimSpace(requested)
	}
	if strings.TrimSpace(provider.DefaultModel) != "" {
		return strings.TrimSpace(provider.DefaultModel)
	}
	if len(provider.Models) > 0 {
		return strings.TrimSpace(provider.Models[0].ID)
	}
	return "request-model-not-set"
}

func connectorLabResponseShapeOK(protocol domain.Protocol, status int, body []byte) bool {
	if status < 200 || status >= 300 {
		return false
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return false
	}
	switch protocol {
	case domain.ProtocolClaude:
		_, ok := payload["content"]
		return ok
	case domain.ProtocolOpenAIResponses:
		_, ok := payload["output"]
		return ok || payload["output_text"] != nil
	default:
		_, ok := payload["choices"]
		return ok
	}
}

func connectorLabResponseSummary(body []byte) string {
	var payload map[string]any
	if json.Unmarshal(body, &payload) == nil {
		if errValue, ok := payload["error"]; ok && errValue != nil {
			return fmt.Sprint(errValue)
		}
	}
	return connectorLabPreview(body)
}

func connectorLabPreview(body []byte) string {
	text := strings.TrimSpace(strings.ReplaceAll(string(body), "\n", " "))
	if len(text) > 700 {
		return text[:700] + "…"
	}
	return text
}

func connectorLabResponseHint(protocol domain.Protocol) string {
	switch protocol {
	case domain.ProtocolClaude:
		return "标准 Claude 响应应包含 content 数组；请检查 Connector 的 /v1/messages 转换"
	case domain.ProtocolOpenAIResponses:
		return "标准 Responses 响应应包含 output/output_text；请检查 Connector 的 /v1/responses 转换"
	default:
		return "标准 Chat 响应应包含 choices；请检查 Connector 的 /v1/chat/completions 转换"
	}
}

func connectorLabAllRequiredPassed(steps []connectorLabStep) bool {
	if len(steps) == 0 {
		return false
	}
	for _, step := range steps {
		if step.Skipped {
			continue
		}
		if !step.Passed && step.ID != "provider_models" {
			return false
		}
	}
	return true
}

// applyConnectorLabClientAuth 按三种客户端协议写各自最常见的鉴权头：
// Claude 用 x-api-key，OpenAI 用 Authorization Bearer。网关三种头都接受，
// 这里按协议写是为了让 Lab 顺便验证真实客户端的请求形状。
func applyConnectorLabClientAuth(request *http.Request, protocol domain.Protocol, token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	if protocol == domain.ProtocolClaude {
		request.Header.Set("x-api-key", token)
		return
	}
	request.Header.Set("Authorization", "Bearer "+token)
}
