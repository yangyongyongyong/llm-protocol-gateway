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
	return resolveProviderChatURL(provider, model)
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
			url = strings.TrimRight(baseURL, "/") + "/chat/completions"
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
	provider.RequestAdapter.CurlExample = generateRequestAdapterCurl(
		provider.RequestAdapter,
		firstNonEmpty(provider.BaseURL, "https://example.invalid"),
		clientModel,
		`{"model":"`+clientModel+`","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":64}`,
	)
}
