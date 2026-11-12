package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func normalizeRequestAdapter(adapter *domain.RequestAdapter) *domain.RequestAdapter {
	if adapter == nil {
		return nil
	}
	out := &domain.RequestAdapter{
		URLTemplate:  strings.TrimSpace(adapter.URLTemplate),
		BodyTemplate: strings.TrimSpace(adapter.BodyTemplate),
	}
	if len(adapter.Headers) > 0 {
		out.Headers = make(map[string]string, len(adapter.Headers))
		for key, value := range adapter.Headers {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			out.Headers[key] = strings.TrimSpace(value)
		}
		if len(out.Headers) == 0 {
			out.Headers = nil
		}
	}
	if len(adapter.ModelMapping) > 0 {
		out.ModelMapping = normalizeModelAliases(adapter.ModelMapping)
	}
	if out.URLTemplate == "" && out.BodyTemplate == "" && len(out.Headers) == 0 && len(out.ModelMapping) == 0 {
		return nil
	}
	// CurlExample is generated at response time via enrichProviderAdapterCurl
	// so it can use the provider's real BaseURL / DefaultModel.
	return out
}

func applyProviderModelMapping(provider domain.Provider, model string) string {
	model = strings.TrimSpace(model)
	if provider.RequestAdapter == nil || len(provider.RequestAdapter.ModelMapping) == 0 || model == "" {
		return model
	}
	mapped, _ := resolveAPIKeyModelAlias(provider.RequestAdapter.ModelMapping, model)
	return mapped
}

func applyRequestAdapterPlaceholders(template, baseURL, model string) string {
	replacer := strings.NewReplacer(
		"{model}", model,
		"{baseUrl}", strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		"{baseURL}", strings.TrimRight(strings.TrimSpace(baseURL), "/"),
	)
	return replacer.Replace(template)
}

func resolveProviderChatURLWithAdapter(provider domain.Provider, model string) string {
	model = applyProviderModelMapping(provider, model)
	if provider.RequestAdapter != nil {
		if tmpl := strings.TrimSpace(provider.RequestAdapter.URLTemplate); tmpl != "" {
			return applyRequestAdapterPlaceholders(tmpl, provider.BaseURL, model)
		}
	}
	return resolveOpenCodeZenURL(provider, model, resolveProviderChatURL(provider, model))
}

func applyRequestAdapterHeaders(request *http.Request, provider domain.Provider, model string) {
	if request == nil || provider.RequestAdapter == nil || len(provider.RequestAdapter.Headers) == 0 {
		return
	}
	for key, value := range provider.RequestAdapter.Headers {
		request.Header.Set(key, applyRequestAdapterPlaceholders(value, provider.BaseURL, model))
	}
}

// rewriteRequestBodyModel sets JSON body "model" to the mapped upstream model when present.
func rewriteRequestBodyModel(body []byte, model string) ([]byte, error) {
	model = strings.TrimSpace(model)
	if len(body) == 0 || model == "" {
		return body, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, nil
	}
	if current, ok := payload["model"].(string); ok && strings.TrimSpace(current) == model {
		return body, nil
	}
	payload["model"] = model
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return body, err
	}
	return rewritten, nil
}

func applyRequestAdapterBody(provider domain.Provider, model string, body []byte) ([]byte, error) {
	model = applyProviderModelMapping(provider, model)
	if provider.RequestAdapter == nil {
		return rewriteRequestBodyModel(body, model)
	}
	tmpl := strings.TrimSpace(provider.RequestAdapter.BodyTemplate)
	if tmpl == "" {
		return rewriteRequestBodyModel(body, model)
	}
	rendered := applyRequestAdapterPlaceholders(tmpl, provider.BaseURL, model)
	// Allow embedding the converted JSON body via {body}.
	if strings.Contains(rendered, "{body}") {
		bodyForEmbed, err := rewriteRequestBodyModel(body, model)
		if err != nil {
			return nil, err
		}
		rendered = strings.ReplaceAll(rendered, "{body}", string(bodyForEmbed))
	}
	if !json.Valid([]byte(rendered)) {
		return nil, fmt.Errorf("requestAdapter.bodyTemplate produced invalid JSON")
	}
	return []byte(rendered), nil
}

func generateRequestAdapterCurl(adapter *domain.RequestAdapter, baseURL, model, sampleBody string) string {
	return generateRequestAdapterCurlFor(adapter, baseURL, model, sampleBody, "")
}

// generateRequestAdapterCurlFor 拼一段能直接粘去跑的 curl 示例。
//
// protocol 决定两件事：拼接哪条端点路径、示例体长什么样。以前两样都写死成
// OpenAI Chat，于是 Claude 协议的 provider 会看到「/anthropic/v1/chat/completions」
// 这种把两种协议拼在一起的地址，照着跑必然 404。
func generateRequestAdapterCurlFor(adapter *domain.RequestAdapter, baseURL, model, sampleBody string, protocol domain.Protocol) string {
	if adapter == nil {
		return ""
	}
	mappedModel := model
	if len(adapter.ModelMapping) > 0 {
		if mapped, ok := resolveAPIKeyModelAlias(adapter.ModelMapping, model); ok {
			mappedModel = mapped
		}
	}
	url := strings.TrimSpace(adapter.URLTemplate)
	if url == "" {
		// Prefer substituting {model} inside the provider base URL when present.
		if strings.Contains(baseURL, "{model}") {
			url = baseURL
		} else {
			url = strings.TrimRight(baseURL, "/") + adapterEndpointPath(protocol, baseURL)
		}
	}
	url = applyRequestAdapterPlaceholders(url, baseURL, mappedModel)
	body := sampleBody
	if tmpl := strings.TrimSpace(adapter.BodyTemplate); tmpl != "" {
		body = applyRequestAdapterPlaceholders(tmpl, baseURL, mappedModel)
		body = strings.ReplaceAll(body, "{body}", sampleBody)
	} else if rewritten, err := rewriteRequestBodyModel([]byte(sampleBody), mappedModel); err == nil {
		body = string(rewritten)
	}
	var b strings.Builder
	b.WriteString("curl -sS -X POST ")
	b.WriteString(shellQuote(url))
	b.WriteString(" \\\n  -H 'Content-Type: application/json'")
	keys := make([]string, 0, len(adapter.Headers))
	for key := range adapter.Headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		rendered := applyRequestAdapterPlaceholders(adapter.Headers[key], baseURL, mappedModel)
		b.WriteString(" \\\n  -H ")
		b.WriteString(shellQuote(key + ": " + rendered))
	}
	b.WriteString(" \\\n  -d ")
	b.WriteString(shellQuote(body))
	return b.String()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// adapterEndpointPath 是示例里该拼的端点路径。基地址已经带着它就返回空串，
// 免得拼出 /messages/messages。
func adapterEndpointPath(protocol domain.Protocol, baseURL string) string {
	lower := strings.ToLower(baseURL)
	switch protocol {
	case domain.ProtocolClaude:
		if strings.Contains(lower, "/messages") {
			return ""
		}
		return "/messages"
	case domain.ProtocolOpenAIResponses:
		if strings.Contains(lower, "/responses") {
			return ""
		}
		return "/responses"
	default:
		if strings.Contains(lower, "/chat/completions") {
			return ""
		}
		return "/chat/completions"
	}
}

// adapterSampleBody 是示例请求体。Claude 与 OpenAI 两套形状不能混用：把
// messages+max_completion_tokens 发给 Claude 端点，或者把 max_tokens 发给
// Responses 端点，都会得到一句参数错误，而不是一次成功的示例。
func adapterSampleBody(protocol domain.Protocol, model string) string {
	switch protocol {
	case domain.ProtocolClaude:
		return `{"model":"` + model + `","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
	case domain.ProtocolOpenAIResponses:
		return `{"model":"` + model + `","input":"hi","max_output_tokens":64}`
	default:
		return `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":64}`
	}
}

func enrichProviderAdapterCurl(provider *domain.Provider) {
	if provider == nil || provider.RequestAdapter == nil {
		return
	}
	sampleModel := firstNonEmpty(provider.DefaultModel, "gpt-5.5")
	// Prefer a mapped client model in the example when mapping exists.
	clientModel := sampleModel
	if len(provider.RequestAdapter.ModelMapping) > 0 {
		aliases := make([]string, 0, len(provider.RequestAdapter.ModelMapping))
		for alias := range provider.RequestAdapter.ModelMapping {
			aliases = append(aliases, alias)
		}
		sort.Strings(aliases)
		clientModel = aliases[0]
	}
	provider.RequestAdapter.CurlExample = generateRequestAdapterCurlFor(
		provider.RequestAdapter,
		firstNonEmpty(provider.BaseURL, "https://example.invalid"),
		clientModel,
		adapterSampleBody(provider.Protocol, clientModel),
		provider.Protocol,
	)
}
