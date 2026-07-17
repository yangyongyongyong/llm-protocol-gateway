package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

const providerCacheRound2UserPrompt = "继续"

type providerChatTestRequest struct {
	Model          string `json:"model"`
	Message        string `json:"message"`
	SystemPrompt   string `json:"systemPrompt"`
	UserPrompt     string `json:"userPrompt"`
	ThinkingField  string `json:"thinkingField"`
	ThinkingValue  string `json:"thinkingValue"`
}

func resolveProviderTestModel(model string) string {
	return strings.TrimSpace(model)
}

func buildProviderChatMessages(req providerChatTestRequest) []map[string]any {
	systemPrompt := strings.TrimSpace(req.SystemPrompt)
	userPrompt := strings.TrimSpace(req.UserPrompt)
	if userPrompt == "" {
		userPrompt = strings.TrimSpace(req.Message)
	}
	if userPrompt == "" {
		userPrompt = "1+1等于几"
	}
	messages := make([]map[string]any, 0, 2)
	if systemPrompt != "" {
		messages = append(messages, map[string]any{"role": "system", "content": systemPrompt})
	}
	messages = append(messages, map[string]any{"role": "user", "content": userPrompt})
	return messages
}

func buildProviderCacheRound2Messages(req providerChatTestRequest, assistantContent string) []map[string]any {
	userPrompt := strings.TrimSpace(req.UserPrompt)
	if userPrompt == "" {
		userPrompt = strings.TrimSpace(req.Message)
	}
	if userPrompt == "" {
		userPrompt = "1+1等于几"
	}
	roundMessages := []map[string]any{{"role": "user", "content": userPrompt}}
	if strings.TrimSpace(assistantContent) != "" {
		roundMessages = append(roundMessages, map[string]any{"role": "assistant", "content": assistantContent})
	} else {
		roundMessages = append(roundMessages, map[string]any{"role": "assistant", "content": "（第一轮 assistant 回复）"})
	}
	roundMessages = append(roundMessages, map[string]any{"role": "user", "content": providerCacheRound2UserPrompt})
	return buildOpenAIChatCacheMessages(req, roundMessages)
}

func defaultProviderCacheSystemPrompt(req providerChatTestRequest) string {
	if systemPrompt := strings.TrimSpace(req.SystemPrompt); systemPrompt != "" {
		return systemPrompt
	}
	// Claude prompt cache generally needs a sizable prefix; keep the default long
	// enough to create a cache breakpoint in provider/route cache tests.
	return strings.Repeat("Cache test context prefix for prompt caching validation. ", 80)
}

func buildOpenAIChatCacheMessages(req providerChatTestRequest, roundMessages []map[string]any) []map[string]any {
	messages := []map[string]any{
		{
			"role": "system",
			"content": []map[string]any{
				{
					"type":          "text",
					"text":          defaultProviderCacheSystemPrompt(req),
					"cache_control": map[string]any{"type": "ephemeral"},
				},
			},
		},
	}
	return append(messages, roundMessages...)
}

func buildClaudeCachePayload(model string, req providerChatTestRequest, messages []map[string]any) map[string]any {
	resolvedModel := resolveProviderTestModel(model)
	if resolvedModel == "" {
		resolvedModel = "request-model-not-set"
	}
	return map[string]any{
		"model":      resolvedModel,
		"max_tokens": 256,
		"stream":     false,
		"system": []map[string]any{
			{
				"type":          "text",
				"text":          defaultProviderCacheSystemPrompt(req),
				"cache_control": map[string]any{"type": "ephemeral"},
			},
		},
		"messages": messages,
	}
}

func buildProviderCacheRound1UserPrompt(req providerChatTestRequest) string {
	userPrompt := strings.TrimSpace(req.UserPrompt)
	if userPrompt == "" {
		userPrompt = strings.TrimSpace(req.Message)
	}
	if userPrompt == "" {
		userPrompt = "1+1等于几"
	}
	return userPrompt
}

func buildProviderChatPayload(model string, messages []map[string]any) map[string]any {
	resolvedModel := resolveProviderTestModel(model)
	if resolvedModel == "" {
		resolvedModel = "request-model-not-set"
	}
	return map[string]any{
		"model":    resolvedModel,
		"stream":   false,
		"messages": messages,
	}
}

func buildProviderClaudeChatPayload(model string, req providerChatTestRequest) map[string]any {
	userPrompt := strings.TrimSpace(req.UserPrompt)
	if userPrompt == "" {
		userPrompt = strings.TrimSpace(req.Message)
	}
	if userPrompt == "" {
		userPrompt = "1+1等于几"
	}
	resolvedModel := resolveProviderTestModel(model)
	if resolvedModel == "" {
		resolvedModel = "request-model-not-set"
	}
	payload := map[string]any{
		"model":      resolvedModel,
		"max_tokens": 4096,
		"stream":     false,
		"messages":   []map[string]any{{"role": "user", "content": userPrompt}},
	}
	if systemPrompt := strings.TrimSpace(req.SystemPrompt); systemPrompt != "" {
		payload["system"] = systemPrompt
	}
	return payload
}

// buildProviderResponsesChatPayload builds a minimal OpenAI Responses API
// request body for a plain api_key (non-ChatGPT-OAuth) provider. Mirrors
// buildProviderClaudeChatPayload's shape/defaults for the OpenAI Chat sibling.
func buildProviderResponsesChatPayload(model string, req providerChatTestRequest) map[string]any {
	userPrompt := strings.TrimSpace(req.UserPrompt)
	if userPrompt == "" {
		userPrompt = strings.TrimSpace(req.Message)
	}
	if userPrompt == "" {
		userPrompt = "1+1等于几"
	}
	resolvedModel := resolveProviderTestModel(model)
	if resolvedModel == "" {
		resolvedModel = "request-model-not-set"
	}
	input := []map[string]any{{"role": "user", "content": userPrompt}}
	if systemPrompt := strings.TrimSpace(req.SystemPrompt); systemPrompt != "" {
		input = append([]map[string]any{{"role": "system", "content": systemPrompt}}, input...)
	}
	return map[string]any{
		"model":  resolvedModel,
		"stream": false,
		"input":  input,
	}
}

func applyProviderThinkingPayload(payload map[string]any, provider domain.Provider, field, value string) {
	field = strings.TrimSpace(field)
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	switch field {
	case "", "reasoning_effort":
		payload["reasoning_effort"] = value
	case "thinking.type":
		payload["thinking"] = map[string]any{"type": value}
	case "thinking.budget_tokens":
		budget, err := strconv.Atoi(value)
		if err != nil {
			payload["thinking"] = map[string]any{"type": "enabled", "budget_tokens": value}
			return
		}
		payload["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
	case "thinking":
		var thinking map[string]any
		if err := json.Unmarshal([]byte(value), &thinking); err == nil {
			payload["thinking"] = thinking
			return
		}
		payload["thinking"] = map[string]any{"type": value}
	default:
		payload[field] = value
	}
}

func thinkingOptionsForProvider(provider domain.Provider) map[string]any {
	switch provider.Protocol {
	case domain.ProtocolClaude:
		return map[string]any{
			"protocol":     provider.Protocol,
			"defaultField": "thinking.type",
			"fields": []map[string]any{
				{"key": "thinking.type", "label": "thinking.type", "presets": []string{"enabled", "disabled"}},
				{"key": "thinking.budget_tokens", "label": "thinking.budget_tokens", "presets": []string{"1024", "4096", "10000"}, "custom": true},
				{"key": "thinking", "label": "thinking (JSON)", "presets": []string{`{"type":"enabled","budget_tokens":4096}`}, "custom": true},
			},
		}
	default:
		return map[string]any{
			"protocol":     provider.Protocol,
			"defaultField": "reasoning_effort",
			"fields": []map[string]any{
				{"key": "reasoning_effort", "label": "reasoning_effort", "presets": []string{"low", "medium", "high"}, "custom": true},
				{"key": "thinking.type", "label": "thinking.type", "presets": []string{"enabled", "disabled"}, "custom": true},
			},
		}
	}
}

func extractCacheUsage(responseBody []byte) map[string]any {
	var payload map[string]any
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return nil
	}
	usageValue, ok := payload["usage"]
	if !ok {
		return nil
	}
	usage, ok := usageValue.(map[string]any)
	if !ok {
		return nil
	}
	result := map[string]any{}
	for _, key := range []string{
		"prompt_tokens", "completion_tokens", "total_tokens",
		"prompt_cache_hit_tokens", "prompt_cache_miss_tokens",
		"input_tokens", "output_tokens",
		"cache_read_input_tokens", "cache_creation_input_tokens",
	} {
		if value, exists := usage[key]; exists {
			result[key] = value
		}
	}
	if detailsValue, exists := usage["prompt_tokens_details"]; exists {
		result["prompt_tokens_details"] = detailsValue
	}
	if detailsValue, exists := usage["input_tokens_details"]; exists {
		result["input_tokens_details"] = detailsValue
	}
	return result
}

func cacheHitTokenCount(usage map[string]any) float64 {
	if usage == nil {
		return 0
	}
	for _, key := range []string{"prompt_cache_hit_tokens", "cache_read_input_tokens"} {
		if value, exists := usage[key]; exists {
			switch typed := value.(type) {
			case float64:
				if typed > 0 {
					return typed
				}
			case int:
				if typed > 0 {
					return float64(typed)
				}
			case int64:
				if typed > 0 {
					return float64(typed)
				}
			}
		}
	}
	if detailsValue, exists := usage["prompt_tokens_details"]; exists {
		if details, ok := detailsValue.(map[string]any); ok {
			if value, exists := details["cached_tokens"]; exists {
				switch typed := value.(type) {
				case float64:
					return typed
				case int:
					return float64(typed)
				case int64:
					return float64(typed)
				}
			}
		}
	}
	return 0
}

func providerChatRoundResult(round providerChatHTTPResult) map[string]any {
	result := map[string]any{
		"status":    round.Status,
		"latencyMs": round.LatencyMs,
		"targetUrl": round.TargetURL,
	}
	if round.Error != "" {
		result["error"] = round.Error
	}
	if round.RequestBody != "" {
		result["requestBody"] = round.RequestBody
	}
	if round.ResponseBody != "" {
		result["responseBody"] = round.ResponseBody
		if usage := extractCacheUsage([]byte(round.ResponseBody)); usage != nil {
			result["usage"] = usage
		}
	}
	return result
}

func (s *Server) testProviderChat(r *http.Request, providerID string, req providerChatTestRequest, started time.Time) (map[string]any, int) {
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error(), "latencyMs": time.Since(started).Milliseconds()}, http.StatusNotFound
	}
	if provider.Protocol == domain.ProtocolClaude && provider.AuthType == domain.AuthTypeClaudeOAuth {
		return s.testClaudeOAuthProviderChat(r, provider, req, started)
	}
	if provider.AuthType == domain.AuthTypeChatGPTOAuth {
		return s.testChatGPTOAuthProviderChat(r, provider, req, started)
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = strings.TrimSpace(provider.DefaultModel)
	}
	if model == "" {
		model = "request-model-not-set"
	}

	// Payload shape follows the provider's own protocol (api_key providers of
	// any of the three supported protocols, not just OpenAI Chat): the
	// transport layer below (executeProviderChatHTTP) is already fully
	// protocol-generic via resolveProviderChatURLWithAdapter/AuthHeader, so
	// only the request-body shape needs to branch here.
	var payload map[string]any
	switch provider.Protocol {
	case domain.ProtocolClaude:
		payload = buildProviderClaudeChatPayload(model, req)
	case domain.ProtocolOpenAIResponses:
		payload = buildProviderResponsesChatPayload(model, req)
	default:
		messages := buildProviderChatMessages(req)
		payload = buildProviderChatPayload(model, messages)
	}
	round := s.executeProviderChatHTTP(r, provider, model, payload, started)
	if round.Error != "" {
		s.logs.AddApp("error", "provider chat test failed", round.Error)
		return map[string]any{
			"success":     false,
			"providerId":  provider.ID,
			"model":       model,
			"targetUrl":   round.TargetURL,
			"requestBody": round.RequestBody,
			"error":       round.Error,
			"latencyMs":   round.LatencyMs,
		}, http.StatusOK
	}

	preview := strings.TrimSpace(strings.ReplaceAll(round.ResponseBody, "\n", " "))
	if len(preview) > 900 {
		preview = preview[:900]
	}
	success := round.Status >= 200 && round.Status < 300
	s.logs.AddApp("info", "provider chat test completed", fmt.Sprintf("provider=%s status=%d latency=%dms", provider.ID, round.Status, round.LatencyMs))
	return map[string]any{
		"success":      success,
		"providerId":   provider.ID,
		"model":        model,
		"status":       round.Status,
		"latencyMs":    round.LatencyMs,
		"preview":      preview,
		"responseBody": round.ResponseBody,
		"targetUrl":    round.TargetURL,
		"requestBody":  round.RequestBody,
	}, http.StatusOK
}

func (s *Server) testChatGPTOAuthProviderChat(r *http.Request, provider domain.Provider, req providerChatTestRequest, started time.Time) (map[string]any, int) {
	refreshed, err := s.ensureFreshChatGPTToken(provider)
	if err != nil {
		return map[string]any{
			"success":    false,
			"providerId": provider.ID,
			"error":      err.Error(),
			"latencyMs":  time.Since(started).Milliseconds(),
		}, http.StatusOK
	}
	provider = refreshed

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = strings.TrimSpace(provider.DefaultModel)
	}
	if model == "" && len(provider.Models) > 0 {
		model = provider.Models[0].ID
	}
	if model == "" {
		model = "gpt-5.6-terra"
	}

	userPrompt := strings.TrimSpace(req.UserPrompt)
	if userPrompt == "" {
		userPrompt = "ping"
	}
	payload := map[string]any{
		"model": model,
		"input": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": userPrompt},
				},
			},
		},
		"store":  false,
		"stream": true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return map[string]any{"success": false, "providerId": provider.ID, "error": err.Error(), "latencyMs": time.Since(started).Milliseconds()}, http.StatusOK
	}

	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, chatgptCodexResponsesURL, bytes.NewReader(body))
	if err != nil {
		return map[string]any{"success": false, "providerId": provider.ID, "error": err.Error(), "latencyMs": time.Since(started).Milliseconds()}, http.StatusOK
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	applyChatGPTCodexHeaders(httpReq, provider)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		s.logs.AddApp("error", "chatgpt oauth chat test failed", err.Error())
		return map[string]any{
			"success":     false,
			"providerId":  provider.ID,
			"model":       model,
			"targetUrl":   chatgptCodexResponsesURL,
			"requestBody": string(body),
			"error":       err.Error(),
			"latencyMs":   time.Since(started).Milliseconds(),
		}, http.StatusOK
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	preview := strings.TrimSpace(strings.ReplaceAll(string(responseBody), "\n", " "))
	if len(preview) > 900 {
		preview = preview[:900]
	}
	success := resp.StatusCode >= 200 && resp.StatusCode < 300
	if !success {
		s.logs.AddApp("error", "chatgpt oauth chat test failed", fmt.Sprintf("status=%d preview=%s", resp.StatusCode, preview))
	} else {
		s.logs.AddApp("info", "chatgpt oauth chat test completed", fmt.Sprintf("provider=%s status=%d latency=%dms", provider.ID, resp.StatusCode, time.Since(started).Milliseconds()))
	}
	return map[string]any{
		"success":      success,
		"providerId":   provider.ID,
		"model":        model,
		"status":       resp.StatusCode,
		"latencyMs":    time.Since(started).Milliseconds(),
		"preview":      preview,
		"responseBody": string(responseBody),
		"targetUrl":    chatgptCodexResponsesURL,
		"requestBody":  string(body),
	}, http.StatusOK
}

func (s *Server) testProviderCacheChat(r *http.Request, providerID string, req providerChatTestRequest, started time.Time) (map[string]any, int) {
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error(), "latencyMs": time.Since(started).Milliseconds()}, http.StatusNotFound
	}
	if provider.Protocol == domain.ProtocolClaude {
		return s.testClaudeProviderCacheChat(r, provider, req, started)
	}
	if provider.AuthType == domain.AuthTypeChatGPTOAuth || provider.Protocol == domain.ProtocolOpenAIResponses {
		return map[string]any{
			"success":    true,
			"skipped":    true,
			"providerId": provider.ID,
			"summary":    "ChatGPT OAuth / Responses provider skips Cache test",
			"latencyMs":  time.Since(started).Milliseconds(),
		}, http.StatusOK
	}
	if provider.Protocol != domain.ProtocolOpenAIChat {
		return map[string]any{"success": false, "providerId": provider.ID, "error": "Cache test supports OpenAI Chat and Claude providers only"}, http.StatusOK
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = strings.TrimSpace(provider.DefaultModel)
	}
	if model == "" {
		model = "request-model-not-set"
	}

	userPrompt := buildProviderCacheRound1UserPrompt(req)
	round1Started := time.Now()
	messages1 := buildOpenAIChatCacheMessages(req, []map[string]any{{"role": "user", "content": userPrompt}})
	payload1 := buildProviderChatPayload(model, messages1)
	round1 := s.executeProviderChatHTTP(r, provider, model, payload1, round1Started)
	assistantContent := extractAssistantContent([]byte(round1.ResponseBody))
	messages2 := buildProviderCacheRound2Messages(req, assistantContent)
	payload2 := buildProviderChatPayload(model, messages2)
	round2 := s.executeProviderChatHTTP(r, provider, model, payload2, time.Now())
	return s.buildProviderCacheChatResult(provider.ID, model, started, round1, round2), http.StatusOK
}

func (s *Server) testClaudeProviderCacheChat(r *http.Request, provider domain.Provider, req providerChatTestRequest, started time.Time) (map[string]any, int) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = strings.TrimSpace(provider.DefaultModel)
	}
	if model == "" {
		model = "request-model-not-set"
	}

	userPrompt := buildProviderCacheRound1UserPrompt(req)
	round1Started := time.Now()
	payload1 := buildClaudeCachePayload(model, req, []map[string]any{{"role": "user", "content": userPrompt}})
	round1 := s.executeClaudeMessagesHTTP(r, provider, payload1, round1Started)
	assistantContent := extractClaudeAssistantContent([]byte(round1.ResponseBody))
	messages2 := []map[string]any{
		{"role": "user", "content": userPrompt},
	}
	if strings.TrimSpace(assistantContent) != "" {
		messages2 = append(messages2, map[string]any{"role": "assistant", "content": assistantContent})
	} else {
		messages2 = append(messages2, map[string]any{"role": "assistant", "content": "（第一轮 assistant 回复）"})
	}
	messages2 = append(messages2, map[string]any{"role": "user", "content": providerCacheRound2UserPrompt})
	payload2 := buildClaudeCachePayload(model, req, messages2)
	round2 := s.executeClaudeMessagesHTTP(r, provider, payload2, time.Now())
	return s.buildProviderCacheChatResult(provider.ID, model, started, claudeRoundToProviderResult(round1), claudeRoundToProviderResult(round2)), http.StatusOK
}

func claudeRoundToProviderResult(round claudeOAuthHTTPResult) providerChatHTTPResult {
	return providerChatHTTPResult{
		Status:       round.Status,
		LatencyMs:    round.LatencyMs,
		ResponseBody: round.ResponseBody,
		RequestBody:  round.RequestBody,
		TargetURL:    round.TargetURL,
		Error:        round.Error,
	}
}

func (s *Server) buildProviderCacheChatResult(providerID, model string, started time.Time, round1, round2 providerChatHTTPResult) map[string]any {
	usage1 := extractCacheUsage([]byte(round1.ResponseBody))
	usage2 := extractCacheUsage([]byte(round2.ResponseBody))
	cacheHitTokens := cacheHitTokenCount(usage2)
	cacheSupported := cacheHitTokens > 0
	round1OK := round1.Error == "" && round1.Status >= 200 && round1.Status < 300
	round2OK := round2.Error == "" && round2.Status >= 200 && round2.Status < 300
	success := round1OK && round2OK

	summary := "第二轮响应未返回 prompt_cache_hit_tokens / cache_read_input_tokens / cached_tokens"
	if usage2 == nil {
		summary = "第二轮响应缺少 usage 字段，无法判断 cache 命中"
	} else if cacheSupported {
		summary = fmt.Sprintf("检测到 cache 命中：%.0f tokens", cacheHitTokens)
	} else if round2OK {
		summary = "第二轮已完成，但 cache 命中为 0（可能前缀未命中或上游未开启 cache）"
	} else if round2.Error != "" {
		summary = "第二轮请求失败：" + round2.Error
	}

	totalLatency := time.Since(started).Milliseconds()
	if round2.Error != "" {
		s.logs.AddApp("error", "provider cache chat test failed", round2.Error)
	} else {
		s.logs.AddApp("info", "provider cache chat test completed", fmt.Sprintf("provider=%s status=%d cacheHit=%.0f latency=%dms", providerID, round2.Status, cacheHitTokens, totalLatency))
	}

	return map[string]any{
		"success":        success,
		"providerId":     providerID,
		"model":          model,
		"status":         round2.Status,
		"latencyMs":      totalLatency,
		"summary":        summary,
		"cacheSupported": cacheSupported,
		"cacheHitTokens": cacheHitTokens,
		"round1":         providerChatRoundResult(round1),
		"round2":         providerChatRoundResult(round2),
		"usageRound1":    usage1,
		"usageRound2":    usage2,
	}
}

func (s *Server) testProviderThinkingChat(r *http.Request, providerID string, req providerChatTestRequest, started time.Time) (map[string]any, int) {
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error(), "latencyMs": time.Since(started).Milliseconds()}, http.StatusNotFound
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = strings.TrimSpace(provider.DefaultModel)
	}
	if model == "" {
		model = "request-model-not-set"
	}

	thinkingOptions := thinkingOptionsForProvider(provider)
	field := strings.TrimSpace(req.ThinkingField)
	value := strings.TrimSpace(req.ThinkingValue)
	if field == "" {
		if defaultField, ok := thinkingOptions["defaultField"].(string); ok {
			field = defaultField
		}
	}
	if value == "" {
		value = "medium"
		if field == "thinking.type" {
			value = "enabled"
		}
	}

	if provider.Protocol == domain.ProtocolClaude && provider.AuthType == domain.AuthTypeClaudeOAuth {
		payload := buildProviderClaudeChatPayload(model, req)
		applyProviderThinkingPayload(payload, provider, field, value)
		round := s.sendClaudeOAuthMessagesRequest(r.Context(), provider, payload, started)
		success := round.Error == "" && round.Status >= 200 && round.Status < 300
		return map[string]any{
			"success":         success,
			"providerId":      provider.ID,
			"model":           model,
			"status":          round.Status,
			"latencyMs":       round.LatencyMs,
			"thinkingField":   field,
			"thinkingValue":   value,
			"thinkingOptions": thinkingOptions,
			"requestBody":     round.RequestBody,
			"responseBody":    round.ResponseBody,
			"targetUrl":       round.TargetURL,
			"error":           round.Error,
		}, http.StatusOK
	}

	if provider.AuthType == domain.AuthTypeChatGPTOAuth || provider.Protocol == domain.ProtocolOpenAIResponses {
		return map[string]any{
			"success":         true,
			"skipped":         true,
			"providerId":      provider.ID,
			"model":           model,
			"thinkingOptions": thinkingOptions,
			"summary":         "ChatGPT OAuth / Responses provider skips Thinking test",
		}, http.StatusOK
	}

	if provider.Protocol != domain.ProtocolOpenAIChat {
		return map[string]any{
			"success":         false,
			"providerId":      provider.ID,
			"thinkingOptions": thinkingOptions,
			"error":           "Thinking test supports OpenAI Chat providers only",
		}, http.StatusOK
	}

	messages := buildProviderChatMessages(req)
	payload := buildProviderChatPayload(model, messages)
	applyProviderThinkingPayload(payload, provider, field, value)
	round := s.executeProviderChatHTTP(r, provider, model, payload, started)
	success := round.Error == "" && round.Status >= 200 && round.Status < 300
	if round.Error != "" {
		s.logs.AddApp("error", "provider thinking chat test failed", round.Error)
	} else {
		s.logs.AddApp("info", "provider thinking chat test completed", fmt.Sprintf("provider=%s status=%d field=%s value=%s", provider.ID, round.Status, field, value))
	}
	return map[string]any{
		"success":         success,
		"providerId":      provider.ID,
		"model":           model,
		"status":          round.Status,
		"latencyMs":       round.LatencyMs,
		"thinkingField":   field,
		"thinkingValue":   value,
		"thinkingOptions": thinkingOptions,
		"requestBody":     round.RequestBody,
		"responseBody":    round.ResponseBody,
		"targetUrl":       round.TargetURL,
		"error":           round.Error,
	}, http.StatusOK
}
