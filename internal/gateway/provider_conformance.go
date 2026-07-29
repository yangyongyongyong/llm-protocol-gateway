package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

const (
	conformanceSeverityRequired    = "required"
	conformanceSeverityRecommended = "recommended"

	conformancePrompt         = "2+2等于几"
	conformanceBodyLimit      = 256 * 1024
	conformanceHTTPTimeout    = 120 * time.Second
	conformanceCaseModels     = "models"
	conformanceCaseNonStream  = "nonstream_shape"
	conformanceCaseStream     = "stream_shape"
	conformanceCaseUsage      = "usage_fields"
	conformanceCaseCacheHit   = "cache_hit"
)

// conformanceCaseResult is one protocol probe result.
type conformanceCaseResult struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Severity  string `json:"severity"` // required | recommended
	Passed    bool   `json:"passed"`
	Skipped   bool   `json:"skipped,omitempty"`
	LatencyMs int64  `json:"latencyMs"`
	Detail    string `json:"detail,omitempty"`
	Hint      string `json:"hint,omitempty"`
	TargetURL string `json:"targetUrl,omitempty"`
	HTTPStatus int   `json:"httpStatus,omitempty"`
}

// conformanceReport is the full suite result for one provider.
type conformanceReport struct {
	Success           bool                    `json:"success"` // all required cases passed
	PassedRequired    bool                    `json:"passedRequired"`
	PassedAll         bool                    `json:"passedAll"`
	ProviderID        string                  `json:"providerId"`
	Protocol          string                  `json:"protocol"`
	Model             string                  `json:"model"`
	RequiredTotal     int                     `json:"requiredTotal"`
	RequiredPassed    int                     `json:"requiredPassed"`
	RecommendedTotal  int                     `json:"recommendedTotal"`
	RecommendedPassed int                     `json:"recommendedPassed"`
	LatencyMs         int64                   `json:"latencyMs"`
	Cases             []conformanceCaseResult `json:"cases"`
	Summary           string                  `json:"summary"`
}

func (s *Server) handleProviderConformance(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderOwnerForUser(w, r, providerID) {
		return
	}
	s.writeConformanceReport(w, r, providerID)
}

func (s *Server) handleProviderSelfCheckConformance(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.authenticateSelfRegistrationRequest(w, r, providerID) {
		return
	}
	s.writeConformanceReport(w, r, providerID)
}

func (s *Server) writeConformanceReport(w http.ResponseWriter, r *http.Request, providerID string) {
	started := time.Now()
	report := s.runProviderConformance(r, providerID, started)
	s.logs.AddApp("info", "provider conformance", fmt.Sprintf(
		"provider=%s protocol=%s success=%v required=%d/%d recommended=%d/%d latency=%dms",
		report.ProviderID, report.Protocol, report.Success,
		report.RequiredPassed, report.RequiredTotal,
		report.RecommendedPassed, report.RecommendedTotal,
		report.LatencyMs,
	))
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) runProviderConformance(r *http.Request, providerID string, started time.Time) conformanceReport {
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		return conformanceReport{
			Success:    false,
			ProviderID: providerID,
			LatencyMs:  time.Since(started).Milliseconds(),
			Summary:    err.Error(),
			Cases: []conformanceCaseResult{{
				ID:       "provider",
				Title:    "定位 Provider",
				Severity: conformanceSeverityRequired,
				Passed:   false,
				Detail:   err.Error(),
			}},
			RequiredTotal: 1,
		}
	}
	if provider.AuthType == domain.AuthTypeClaudeOAuth ||
		provider.AuthType == domain.AuthTypeCursorOAuth ||
		provider.AuthType == domain.AuthTypeChatGPTOAuth {
		return conformanceReport{
			Success:    false,
			ProviderID: provider.ID,
			Protocol:   string(provider.Protocol),
			LatencyMs:  time.Since(started).Milliseconds(),
			Summary:    "协议 conformance 仅面向 Bearer（api_key）自建上游；OAuth Provider 请用控制台对话/缓存测试",
			Cases: []conformanceCaseResult{{
				ID:       "auth_type",
				Title:    "鉴权类型",
				Severity: conformanceSeverityRequired,
				Passed:   false,
				Detail:   "unsupported authType: " + string(provider.AuthType),
				Hint:     "请对 self_register / api_key Provider 使用本接口",
			}},
			RequiredTotal: 1,
		}
	}

	model := strings.TrimSpace(provider.DefaultModel)
	if model == "" && len(provider.Models) > 0 {
		model = strings.TrimSpace(provider.Models[0].ID)
	}
	if model == "" {
		model = "request-model-not-set"
	}

	cases := make([]conformanceCaseResult, 0, 8)
	cases = append(cases, s.conformanceCaseModels(r, provider, started))
	cases = append(cases, s.conformanceCaseNonStream(r, provider, model))
	cases = append(cases, s.conformanceCaseStream(r, provider, model))
	cases = append(cases, s.conformanceCaseUsage(r, provider, model))
	cases = append(cases, s.conformanceCaseCacheHit(r, provider, model))

	report := conformanceReport{
		ProviderID: provider.ID,
		Protocol:   string(provider.Protocol),
		Model:      model,
		Cases:      cases,
		LatencyMs:  time.Since(started).Milliseconds(),
	}
	for _, c := range cases {
		if c.Skipped {
			continue
		}
		switch c.Severity {
		case conformanceSeverityRequired:
			report.RequiredTotal++
			if c.Passed {
				report.RequiredPassed++
			}
		case conformanceSeverityRecommended:
			report.RecommendedTotal++
			if c.Passed {
				report.RecommendedPassed++
			}
		}
	}
	report.PassedRequired = report.RequiredTotal > 0 && report.RequiredPassed == report.RequiredTotal
	report.PassedAll = report.PassedRequired &&
		(report.RecommendedTotal == 0 || report.RecommendedPassed == report.RecommendedTotal)
	report.Success = report.PassedRequired
	report.Summary = summarizeConformance(report)
	return report
}

func summarizeConformance(report conformanceReport) string {
	if report.PassedAll {
		return "全部用例通过（含建议项）"
	}
	if report.PassedRequired {
		return fmt.Sprintf("必过项已通过；建议项 %d/%d 未过（不影响接入，但可能缺 cache/usage 细节）",
			report.RecommendedTotal-report.RecommendedPassed, report.RecommendedTotal)
	}
	var failed []string
	for _, c := range report.Cases {
		if c.Severity == conformanceSeverityRequired && !c.Passed && !c.Skipped {
			failed = append(failed, c.ID)
		}
	}
	return "必过项未通过: " + strings.Join(failed, ", ")
}

func (s *Server) conformanceCaseModels(r *http.Request, provider domain.Provider, started time.Time) conformanceCaseResult {
	caseStarted := time.Now()
	result := s.fetchProviderModels(r, provider, started)
	out := conformanceCaseResult{
		ID:         conformanceCaseModels,
		Title:      "模型列表 / 鉴权连通",
		Severity:   conformanceSeverityRequired,
		Passed:     result.Success,
		LatencyMs:  time.Since(caseStarted).Milliseconds(),
		TargetURL:  result.ModelsURL,
		HTTPStatus: result.Status,
	}
	if result.Success {
		out.Detail = fmt.Sprintf("models=%d", len(result.Models))
		return out
	}
	out.Detail = firstNonEmpty(result.Error, result.Preview, fmt.Sprintf("HTTP %d", result.Status))
	out.Hint = "检查 baseUrl 推导的 /models、SHARED_SECRET 与 authHeader 是否匹配"
	return out
}

func (s *Server) conformanceCaseNonStream(r *http.Request, provider domain.Provider, model string) conformanceCaseResult {
	caseStarted := time.Now()
	req := providerChatTestRequest{UserPrompt: conformancePrompt}
	payload := buildConformanceNonStreamPayload(provider.Protocol, model, req)
	round := s.executeProviderProtocolHTTP(r, provider, model, payload, "application/json", caseStarted)
	out := conformanceCaseResult{
		ID:         conformanceCaseNonStream,
		Title:      "非流式响应形状",
		Severity:   conformanceSeverityRequired,
		LatencyMs:  time.Since(caseStarted).Milliseconds(),
		TargetURL:  round.TargetURL,
		HTTPStatus: round.Status,
	}
	if round.Error != "" {
		out.Detail = round.Error
		out.Hint = "上游连接失败或超时"
		return out
	}
	if round.Status < 200 || round.Status >= 300 {
		out.Detail = fmt.Sprintf("HTTP %d: %s", round.Status, truncateConformancePreview(round.ResponseBody))
		out.Hint = "上游返回非 2xx；核对 protocol 与路径是否一致"
		return out
	}
	ok, detail, hint := validateConformanceNonStream(provider.Protocol, []byte(round.ResponseBody))
	out.Passed = ok
	out.Detail = detail
	out.Hint = hint
	return out
}

func (s *Server) conformanceCaseStream(r *http.Request, provider domain.Provider, model string) conformanceCaseResult {
	caseStarted := time.Now()
	req := providerChatTestRequest{UserPrompt: conformancePrompt}
	payload := buildConformanceNonStreamPayload(provider.Protocol, model, req)
	payload["stream"] = true
	round := s.executeProviderProtocolHTTP(r, provider, model, payload, "text/event-stream", caseStarted)
	out := conformanceCaseResult{
		ID:         conformanceCaseStream,
		Title:      "流式 SSE 形状",
		Severity:   conformanceSeverityRequired,
		LatencyMs:  time.Since(caseStarted).Milliseconds(),
		TargetURL:  round.TargetURL,
		HTTPStatus: round.Status,
	}
	if round.Error != "" {
		out.Detail = round.Error
		out.Hint = "上游流式连接失败或超时"
		return out
	}
	if round.Status < 200 || round.Status >= 300 {
		out.Detail = fmt.Sprintf("HTTP %d: %s", round.Status, truncateConformancePreview(round.ResponseBody))
		out.Hint = "流式请求返回非 2xx；确认 Accept/stream 实现"
		return out
	}
	ok, detail, hint := validateConformanceStream(provider.Protocol, []byte(round.ResponseBody))
	out.Passed = ok
	out.Detail = detail
	out.Hint = hint
	return out
}

func (s *Server) conformanceCaseUsage(r *http.Request, provider domain.Provider, model string) conformanceCaseResult {
	caseStarted := time.Now()
	req := providerChatTestRequest{UserPrompt: conformancePrompt}
	payload := buildConformanceNonStreamPayload(provider.Protocol, model, req)
	round := s.executeProviderProtocolHTTP(r, provider, model, payload, "application/json", caseStarted)
	out := conformanceCaseResult{
		ID:         conformanceCaseUsage,
		Title:      "usage 字段",
		Severity:   conformanceSeverityRecommended,
		LatencyMs:  time.Since(caseStarted).Milliseconds(),
		TargetURL:  round.TargetURL,
		HTTPStatus: round.Status,
	}
	if round.Error != "" || round.Status < 200 || round.Status >= 300 {
		out.Detail = firstNonEmpty(round.Error, fmt.Sprintf("HTTP %d", round.Status))
		out.Hint = "先保证 nonstream_shape 通过"
		return out
	}
	ok, detail, hint := validateConformanceUsage(provider.Protocol, []byte(round.ResponseBody))
	out.Passed = ok
	out.Detail = detail
	out.Hint = hint
	return out
}

func (s *Server) conformanceCaseCacheHit(r *http.Request, provider domain.Provider, model string) conformanceCaseResult {
	caseStarted := time.Now()
	out := conformanceCaseResult{
		ID:       conformanceCaseCacheHit,
		Title:    "Prompt Cache 命中",
		Severity: conformanceSeverityRecommended,
	}
	req := providerChatTestRequest{UserPrompt: conformancePrompt}
	var round1, round2 providerChatHTTPResult
	switch provider.Protocol {
	case domain.ProtocolClaude:
		payload1 := buildClaudeCachePayload(model, req, []map[string]any{{"role": "user", "content": buildProviderCacheRound1UserPrompt(req)}})
		round1 = claudeRoundToProviderResult(s.executeClaudeMessagesHTTP(r, provider, payload1, caseStarted))
		assistant := extractClaudeAssistantContent([]byte(round1.ResponseBody))
		messages2 := []map[string]any{
			{"role": "user", "content": buildProviderCacheRound1UserPrompt(req)},
		}
		if strings.TrimSpace(assistant) != "" {
			messages2 = append(messages2, map[string]any{"role": "assistant", "content": assistant})
		} else {
			messages2 = append(messages2, map[string]any{"role": "assistant", "content": "ok"})
		}
		messages2 = append(messages2, map[string]any{"role": "user", "content": providerCacheRound2UserPrompt})
		payload2 := buildClaudeCachePayload(model, req, messages2)
		round2 = claudeRoundToProviderResult(s.executeClaudeMessagesHTTP(r, provider, payload2, time.Now()))
	case domain.ProtocolOpenAIResponses:
		payload1 := buildResponsesCachePayload(model, req, nil)
		round1 = s.executeProviderProtocolHTTP(r, provider, model, payload1, "application/json", caseStarted)
		assistant := extractResponsesAssistantText([]byte(round1.ResponseBody))
		payload2 := buildResponsesCachePayload(model, req, &assistant)
		round2 = s.executeProviderProtocolHTTP(r, provider, model, payload2, "application/json", time.Now())
	default: // openai_chat
		userPrompt := buildProviderCacheRound1UserPrompt(req)
		messages1 := buildOpenAIChatCacheMessages(req, []map[string]any{{"role": "user", "content": userPrompt}})
		payload1 := buildProviderChatPayload(model, messages1)
		round1 = s.executeProviderProtocolHTTP(r, provider, model, payload1, "application/json", caseStarted)
		assistant := extractAssistantContent([]byte(round1.ResponseBody))
		messages2 := buildProviderCacheRound2Messages(req, assistant)
		payload2 := buildProviderChatPayload(model, messages2)
		round2 = s.executeProviderProtocolHTTP(r, provider, model, payload2, "application/json", time.Now())
	}
	out.LatencyMs = time.Since(caseStarted).Milliseconds()
	out.TargetURL = round2.TargetURL
	out.HTTPStatus = round2.Status
	if round1.Error != "" || round1.Status < 200 || round1.Status >= 300 {
		out.Detail = "第一轮失败: " + firstNonEmpty(round1.Error, fmt.Sprintf("HTTP %d", round1.Status))
		out.Hint = "cache 探测需要两轮成功请求"
		return out
	}
	if round2.Error != "" || round2.Status < 200 || round2.Status >= 300 {
		out.Detail = "第二轮失败: " + firstNonEmpty(round2.Error, fmt.Sprintf("HTTP %d", round2.Status))
		out.Hint = "cache 探测需要两轮成功请求"
		return out
	}
	hit := cacheHitFromConformanceBody(provider.Protocol, []byte(round2.ResponseBody))
	if hit > 0 {
		out.Passed = true
		out.Detail = fmt.Sprintf("cache hit tokens=%.0f", hit)
		return out
	}
	out.Passed = false
	out.Detail = "第二轮未检测到 cache 命中（cached_tokens / prompt_cache_hit_tokens / cache_read_input_tokens 为 0 或缺失）"
	out.Hint = "实现 prompt cache 并在 usage 中回报命中字段；不影响必过项，但控制台缓存命中会一直为 0"
	return out
}

func buildConformanceNonStreamPayload(protocol domain.Protocol, model string, req providerChatTestRequest) map[string]any {
	switch protocol {
	case domain.ProtocolClaude:
		return buildProviderClaudeChatPayload(model, req)
	case domain.ProtocolOpenAIResponses:
		return buildProviderResponsesChatPayload(model, req)
	default:
		return buildProviderChatPayload(model, buildProviderChatMessages(req))
	}
}

func buildResponsesCachePayload(model string, req providerChatTestRequest, priorAssistant *string) map[string]any {
	systemText := defaultProviderCacheSystemPrompt(req)
	userPrompt := buildProviderCacheRound1UserPrompt(req)
	input := []map[string]any{
		{"role": "system", "content": systemText},
		{"role": "user", "content": userPrompt},
	}
	if priorAssistant != nil {
		input = append(input,
			map[string]any{"role": "assistant", "content": firstNonEmpty(strings.TrimSpace(*priorAssistant), "ok")},
			map[string]any{"role": "user", "content": providerCacheRound2UserPrompt},
		)
	}
	resolvedModel := resolveProviderTestModel(model)
	if resolvedModel == "" {
		resolvedModel = "request-model-not-set"
	}
	return map[string]any{
		"model":  resolvedModel,
		"stream": false,
		"input":  input,
	}
}

func cacheHitFromConformanceBody(protocol domain.Protocol, body []byte) float64 {
	switch protocol {
	case domain.ProtocolOpenAIResponses:
		usage := ParseResponsesUsage(body)
		return float64(usage.CacheTokens)
	case domain.ProtocolClaude:
		usage := ParseClaudeUsage(body)
		return float64(usage.CacheTokens)
	default:
		return cacheHitTokenCount(extractCacheUsage(body))
	}
}

func extractResponsesAssistantText(body []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	output, _ := payload["output"].([]any)
	var b strings.Builder
	for _, raw := range output {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if stringValue(item["type"]) != "" && stringValue(item["type"]) != "message" {
			continue
		}
		content, _ := item["content"].([]any)
		for _, part := range content {
			block, ok := part.(map[string]any)
			if !ok {
				continue
			}
			switch stringValue(block["type"]) {
			case "output_text", "text", "":
				if text := stringValue(block["text"]); text != "" {
					b.WriteString(text)
				}
			}
		}
		if text := stringValue(item["content"]); text != "" && b.Len() == 0 {
			b.WriteString(text)
		}
	}
	return b.String()
}

// executeProviderProtocolHTTP is like executeProviderChatHTTP but allows a custom Accept
// (e.g. text/event-stream) and a larger body cap for SSE conformance probes.
func (s *Server) executeProviderProtocolHTTP(r *http.Request, provider domain.Provider, model string, payload map[string]any, accept string, started time.Time) providerChatHTTPResult {
	body, err := json.Marshal(payload)
	if err != nil {
		return providerChatHTTPResult{Error: err.Error(), LatencyMs: time.Since(started).Milliseconds()}
	}
	resolvedModel, _ := payload["model"].(string)
	if resolvedModel == "" {
		resolvedModel = model
	}
	upstreamURL := resolveProviderChatURLWithAdapter(provider, resolvedModel)
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return providerChatHTTPResult{Error: err.Error(), TargetURL: upstreamURL, RequestBody: string(body), LatencyMs: time.Since(started).Milliseconds()}
	}
	request.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(accept) != "" {
		request.Header.Set("Accept", accept)
	} else {
		request.Header.Set("Accept", "application/json")
	}
	if authValue := resolveProviderAuth(provider); authValue != "" {
		header := provider.AuthHeader
		if header == "" {
			header = "Authorization"
		}
		if strings.EqualFold(header, "Authorization") && !strings.HasPrefix(strings.ToLower(authValue), "bearer ") {
			authValue = "Bearer " + authValue
		}
		request.Header.Set(header, authValue)
	} else if incomingAuth := r.Header.Get("Authorization"); incomingAuth != "" {
		request.Header.Set("Authorization", incomingAuth)
	}

	client := &http.Client{Timeout: conformanceHTTPTimeout}
	response, err := client.Do(request)
	if err != nil {
		return providerChatHTTPResult{
			Error:       err.Error(),
			TargetURL:   upstreamURL,
			RequestBody: string(body),
			LatencyMs:   time.Since(started).Milliseconds(),
		}
	}
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, conformanceBodyLimit))
	return providerChatHTTPResult{
		Status:       response.StatusCode,
		LatencyMs:    time.Since(started).Milliseconds(),
		ResponseBody: string(responseBody),
		RequestBody:  string(body),
		TargetURL:    upstreamURL,
	}
}

func validateConformanceNonStream(protocol domain.Protocol, body []byte) (bool, string, string) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return false, "响应不是合法 JSON: " + err.Error(), "非流式必须返回 JSON 对象"
	}
	if errVal, ok := payload["error"]; ok && errVal != nil {
		return false, fmt.Sprintf("响应含 error: %v", errVal), "上游业务错误"
	}
	switch protocol {
	case domain.ProtocolClaude:
		content, _ := payload["content"].([]any)
		if len(content) == 0 {
			return false, "缺少 content[]", "Claude 非流式需返回 content 块数组"
		}
		text := extractClaudeAssistantContent(body)
		if strings.TrimSpace(text) == "" {
			return false, "content 中无文本", "至少包含一个 type=text 的块"
		}
		return true, "content 文本长度=" + fmt.Sprintf("%d", len(text)), ""
	case domain.ProtocolOpenAIResponses:
		if status := stringValue(payload["status"]); status != "" && status != "completed" && status != "incomplete" {
			// still allow if output present
		}
		text := extractResponsesAssistantText(body)
		if strings.TrimSpace(text) == "" {
			output, _ := payload["output"].([]any)
			if len(output) == 0 {
				return false, "缺少 output[] 或无可提取文本", "Responses 非流式需 output 含 message/output_text"
			}
			return false, "output 存在但无文本", "确认 output[].content[].type=output_text"
		}
		return true, "output 文本长度=" + fmt.Sprintf("%d", len(text)), ""
	default:
		choices, _ := payload["choices"].([]any)
		if len(choices) == 0 {
			return false, "缺少 choices[]", "Chat 非流式需 choices[0].message"
		}
		text := extractAssistantContent(body)
		if strings.TrimSpace(text) == "" {
			return false, "choices[0].message.content 为空", "返回 assistant content 字符串"
		}
		return true, "assistant 文本长度=" + fmt.Sprintf("%d", len(text)), ""
	}
}

func validateConformanceStream(protocol domain.Protocol, body []byte) (bool, string, string) {
	text := string(body)
	if !strings.Contains(text, "data:") {
		return false, "响应中无 SSE data: 行", "stream=true 时必须按 text/event-stream 输出 data: ...\\n\\n"
	}
	events := collectSSEJSONPayloads(body)
	if len(events) == 0 {
		return false, "无法解析任何 data JSON", "每个 data: 后应为 JSON（或 [DONE]）"
	}
	switch protocol {
	case domain.ProtocolClaude:
		types := collectEventTypes(events, "type")
		if !types["message_start"] && !types["content_block_delta"] && !types["message_delta"] {
			return false, "未看到 message_start / content_block_delta / message_delta", "Claude SSE 需 Anthropic 事件序列"
		}
		if !types["message_stop"] && !types["message_delta"] {
			return false, "缺少收尾事件 message_stop 或 message_delta", "流结束应有 message_stop"
		}
		return true, fmt.Sprintf("Claude SSE 事件种类=%d", len(types)), ""
	case domain.ProtocolOpenAIResponses:
		types := collectEventTypes(events, "type")
		hasCreated := types["response.created"] || types["response.in_progress"]
		hasDelta := types["response.output_text.delta"] || types["response.content_part.delta"]
		hasDone := types["response.completed"] || types["response.incomplete"] || types["response.failed"]
		if !hasCreated && !hasDelta && !hasDone {
			return false, "未看到 Responses SSE 事件（response.created / output_text.delta / completed）", "实现 OpenAI Responses 流式事件名"
		}
		if !hasDone && !hasDelta {
			return false, "缺少 response.completed 或 output_text.delta", "至少产出文本增量或 completed"
		}
		return true, fmt.Sprintf("Responses SSE 事件种类=%d", len(types)), ""
	default:
		sawChoice := false
		sawDone := false
		for _, payload := range splitSSEDataLines(body) {
			if payload == "[DONE]" {
				sawDone = true
				continue
			}
			var chunk map[string]any
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				continue
			}
			if choices, ok := chunk["choices"].([]any); ok && len(choices) > 0 {
				sawChoice = true
			}
		}
		if !sawChoice {
			return false, "未看到带 choices 的 chunk", "Chat SSE 每个增量应含 choices[].delta"
		}
		if !sawDone {
			return false, "缺少 data: [DONE]", "流结束必须发送 data: [DONE]"
		}
		return true, "Chat SSE 含 choices 与 [DONE]", ""
	}
}

func validateConformanceUsage(protocol domain.Protocol, body []byte) (bool, string, string) {
	usageMap := extractCacheUsage(body)
	if usageMap == nil {
		// Responses/Claude may nest differently — try protocol parsers for presence.
		switch protocol {
		case domain.ProtocolOpenAIResponses:
			u := ParseResponsesUsage(body)
			if u.InputTokens > 0 || u.OutputTokens > 0 {
				return true, fmt.Sprintf("input=%d output=%d cached=%d", u.InputTokens, u.OutputTokens, u.CacheTokens), ""
			}
			return false, "缺少 usage.input_tokens / output_tokens", "Responses 应在响应中返回 usage，并尽量带 input_tokens_details.cached_tokens"
		case domain.ProtocolClaude:
			u := ParseClaudeUsage(body)
			if u.InputTokens > 0 || u.OutputTokens > 0 {
				return true, fmt.Sprintf("input=%d output=%d cache_read=%d", u.InputTokens, u.OutputTokens, u.CacheTokens), ""
			}
			return false, "缺少 usage.input_tokens / output_tokens", "Claude 应返回 usage"
		default:
			return false, "响应缺少 usage 对象", "Chat 应返回 usage.prompt_tokens / completion_tokens"
		}
	}
	switch protocol {
	case domain.ProtocolOpenAIResponses:
		in := int64FromAny(usageMap["input_tokens"])
		out := int64FromAny(usageMap["output_tokens"])
		if in == 0 && out == 0 {
			return false, "usage.input_tokens/output_tokens 均为 0 或缺失", "填写真实 token 计数"
		}
		details, _ := usageMap["input_tokens_details"].(map[string]any)
		if details == nil {
			return false, "有 usage 但缺少 input_tokens_details（含 cached_tokens）", "即使 cached_tokens=0 也应返回该对象，便于网关统计缓存"
		}
		return true, fmt.Sprintf("input=%d output=%d cached=%v", in, out, details["cached_tokens"]), ""
	case domain.ProtocolClaude:
		in := int64FromAny(usageMap["input_tokens"])
		out := int64FromAny(usageMap["output_tokens"])
		if in == 0 && out == 0 {
			return false, "usage.input_tokens/output_tokens 均为 0 或缺失", "填写真实 token 计数"
		}
		return true, fmt.Sprintf("input=%d output=%d cache_read=%v", in, out, usageMap["cache_read_input_tokens"]), ""
	default:
		prompt := int64FromAny(usageMap["prompt_tokens"])
		comp := int64FromAny(usageMap["completion_tokens"])
		if prompt == 0 && comp == 0 {
			return false, "usage.prompt_tokens/completion_tokens 均为 0 或缺失", "填写真实 token 计数"
		}
		return true, fmt.Sprintf("prompt=%d completion=%d cache_hit=%v", prompt, comp, usageMap["prompt_cache_hit_tokens"]), ""
	}
}

func collectSSEJSONPayloads(body []byte) []map[string]any {
	out := make([]map[string]any, 0, 8)
	for _, payload := range splitSSEDataLines(body) {
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(payload), &obj); err != nil {
			continue
		}
		out = append(out, obj)
	}
	return out
}

func splitSSEDataLines(body []byte) []string {
	var out []string
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), conformanceBodyLimit)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		out = append(out, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
	}
	return out
}

func collectEventTypes(events []map[string]any, key string) map[string]bool {
	types := make(map[string]bool)
	for _, ev := range events {
		if t := strings.TrimSpace(stringValue(ev[key])); t != "" {
			types[t] = true
		}
	}
	return types
}

func truncateConformancePreview(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 240 {
		return s[:240] + "…"
	}
	return s
}
