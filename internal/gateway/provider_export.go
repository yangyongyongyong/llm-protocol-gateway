package gateway

import (
	"fmt"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

const providersExportVersion = 1

// ProvidersExportBundle is the JSON shape used by export/import.
type ProvidersExportBundle struct {
	Version    int               `json:"version"`
	ExportedAt string            `json:"exportedAt,omitempty"`
	Providers  []domain.Provider `json:"providers"`
}

// ProvidersImportResult summarizes an import run.
type ProvidersImportResult struct {
	Created []string `json:"created"`
	Updated []string `json:"updated"`
	Skipped []string `json:"skipped"`
	Errors  []string `json:"errors"`
}

// ExportProviders builds an export bundle for the given provider IDs.
// Empty ids exports all providers. Unknown IDs are reported as errors and skipped.
// OAuth credentials are included as currently stored server-side (full fidelity backup).
func (r *Router) ExportProviders(ids []string) (ProvidersExportBundle, []string) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	bundle := ProvidersExportBundle{
		Version:    providersExportVersion,
		ExportedAt: time.Now().Format(time.RFC3339),
		Providers:  make([]domain.Provider, 0),
	}
	var errors []string

	if len(ids) == 0 {
		for _, provider := range r.state.Providers {
			bundle.Providers = append(bundle.Providers, cloneProviderForExport(provider))
		}
		return bundle, errors
	}

	seen := make(map[string]struct{}, len(ids))
	for _, rawID := range ids {
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		provider, ok := r.providerLocked(id)
		if !ok {
			errors = append(errors, fmt.Sprintf("provider %q not found", id))
			continue
		}
		bundle.Providers = append(bundle.Providers, cloneProviderForExport(provider))
	}
	return bundle, errors
}

// ImportProviders upserts providers from a bundle without deleting unrelated ones.
// Match order: existing id → existing name → create (keeping id when possible).
func (r *Router) ImportProviders(bundle ProvidersExportBundle) ProvidersImportResult {
	result := ProvidersImportResult{
		Created: make([]string, 0),
		Updated: make([]string, 0),
		Skipped: make([]string, 0),
		Errors:  make([]string, 0),
	}

	if bundle.Version != 0 && bundle.Version != providersExportVersion {
		result.Errors = append(result.Errors, fmt.Sprintf("unsupported export version %d (expected %d)", bundle.Version, providersExportVersion))
		return result
	}
	if bundle.Providers == nil {
		result.Errors = append(result.Errors, "providers array is required")
		return result
	}

	for index, incoming := range bundle.Providers {
		label := providerImportLabel(incoming, index)
		normalized, err := prepareProviderForImport(incoming)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %s", label, err.Error()))
			continue
		}

		action, id, err := r.upsertProviderImport(normalized)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %s", label, err.Error()))
			continue
		}
		switch action {
		case "created":
			result.Created = append(result.Created, id)
		case "updated":
			result.Updated = append(result.Updated, id)
		default:
			result.Skipped = append(result.Skipped, id)
		}
	}
	return result
}

func prepareProviderForImport(provider domain.Provider) (domain.Provider, error) {
	provider.ID = strings.TrimSpace(provider.ID)
	provider.Name = strings.TrimSpace(provider.Name)
	provider.BaseURL = strings.TrimSpace(provider.BaseURL)
	provider.DefaultModel = strings.TrimSpace(provider.DefaultModel)
	provider.DefaultThinkingDepth = strings.TrimSpace(provider.DefaultThinkingDepth)
	provider.APIKeySource = strings.TrimSpace(provider.APIKeySource)
	provider.AuthType = strings.TrimSpace(provider.AuthType)
	provider.AuthHeader = strings.TrimSpace(provider.AuthHeader)
	provider.ExtraEndpoint = strings.TrimSpace(provider.ExtraEndpoint)
	provider.HealthStatus = strings.TrimSpace(provider.HealthStatus)

	if provider.Name == "" {
		return domain.Provider{}, fmt.Errorf("provider name is required")
	}
	if provider.AuthType == "" {
		provider.AuthType = domain.AuthTypeAPIKey
	}
	switch provider.AuthType {
	case domain.AuthTypeAPIKey, domain.AuthTypeClaudeOAuth, domain.AuthTypeCursorOAuth:
	default:
		return domain.Provider{}, fmt.Errorf("unsupported authType %q", provider.AuthType)
	}
	if provider.Protocol != "" {
		switch provider.Protocol {
		case domain.ProtocolOpenAIChat, domain.ProtocolOpenAIResponses, domain.ProtocolClaude:
		default:
			return domain.Provider{}, fmt.Errorf("unsupported protocol %q", provider.Protocol)
		}
	}
	if provider.BaseURL == "" &&
		provider.AuthType != domain.AuthTypeClaudeOAuth &&
		provider.AuthType != domain.AuthTypeCursorOAuth {
		return domain.Provider{}, fmt.Errorf("provider baseUrl is required")
	}

	provider.ClaudeOAuth = sanitizeImportedClaudeOAuth(provider.ClaudeOAuth)
	provider.CursorOAuth = sanitizeImportedCursorOAuth(provider.CursorOAuth)
	if provider.RequestAdapter != nil {
		// Drop generated display-only curl; it is rebuilt on read.
		adapter := *provider.RequestAdapter
		adapter.CurlExample = ""
		provider.RequestAdapter = normalizeRequestAdapter(&adapter)
	}
	return provider, nil
}

func sanitizeImportedClaudeOAuth(cred *domain.ClaudeOAuthCredential) *domain.ClaudeOAuthCredential {
	if cred == nil {
		return nil
	}
	access := strings.TrimSpace(cred.AccessToken)
	refresh := strings.TrimSpace(cred.RefreshToken)
	expires := strings.TrimSpace(cred.ExpiresAt)
	scope := strings.TrimSpace(cred.Scope)
	label := strings.TrimSpace(cred.AccountLabel)
	// Redacted client copies only have Connected/metadata — treat as "no secret payload".
	if access == "" && refresh == "" {
		if expires == "" && scope == "" && label == "" && !cred.Connected {
			return nil
		}
		return &domain.ClaudeOAuthCredential{
			ExpiresAt:    expires,
			Scope:        scope,
			AccountLabel: label,
			Connected:    false,
		}
	}
	return &domain.ClaudeOAuthCredential{
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresAt:    expires,
		Scope:        scope,
		AccountLabel: label,
	}
}

func sanitizeImportedCursorOAuth(cred *domain.CursorOAuthCredential) *domain.CursorOAuthCredential {
	if cred == nil {
		return nil
	}
	access := strings.TrimSpace(cred.AccessToken)
	refresh := strings.TrimSpace(cred.RefreshToken)
	expires := strings.TrimSpace(cred.ExpiresAt)
	label := strings.TrimSpace(cred.AccountLabel)
	if access == "" && refresh == "" {
		if expires == "" && label == "" && !cred.Connected {
			return nil
		}
		return &domain.CursorOAuthCredential{
			ExpiresAt:    expires,
			AccountLabel: label,
			Connected:    false,
		}
	}
	return &domain.CursorOAuthCredential{
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresAt:    expires,
		AccountLabel: label,
	}
}

func (r *Router) upsertProviderImport(incoming domain.Provider) (action string, id string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	targetIndex := -1
	if incoming.ID != "" {
		for index := range r.state.Providers {
			if r.state.Providers[index].ID == incoming.ID {
				targetIndex = index
				break
			}
		}
	}
	if targetIndex < 0 {
		for index := range r.state.Providers {
			if strings.EqualFold(r.state.Providers[index].Name, incoming.Name) {
				targetIndex = index
				break
			}
		}
	}

	if targetIndex >= 0 {
		updated := mergeProviderImport(r.state.Providers[targetIndex], incoming)
		normalizeProvider(&updated)
		r.state.Providers[targetIndex] = updated
		r.rebuildModelsLocked()
		return "updated", updated.ID, nil
	}

	created := incoming
	if created.ID == "" {
		created.ID = uniqueID(slug(created.Name), func(id string) bool {
			for _, item := range r.state.Providers {
				if item.ID == id {
					return true
				}
			}
			return false
		})
	} else {
		for _, item := range r.state.Providers {
			if item.ID == created.ID {
				return "", "", fmt.Errorf("provider id %q already exists", created.ID)
			}
		}
	}
	normalizeProvider(&created)
	r.state.Providers = append(r.state.Providers, created)
	r.rebuildModelsLocked()
	return "created", created.ID, nil
}

func mergeProviderImport(existing, incoming domain.Provider) domain.Provider {
	updated := existing
	updated.Name = incoming.Name
	if incoming.Protocol != "" {
		updated.Protocol = incoming.Protocol
	}
	updated.BaseURL = incoming.BaseURL
	updated.APIKeySource = incoming.APIKeySource
	updated.DefaultModel = incoming.DefaultModel
	updated.DefaultThinkingDepth = incoming.DefaultThinkingDepth
	if incoming.AuthHeader != "" {
		updated.AuthHeader = incoming.AuthHeader
	}
	updated.ExtraEndpoint = incoming.ExtraEndpoint
	if incoming.AuthType != "" {
		updated.AuthType = incoming.AuthType
	}
	if incoming.HealthStatus != "" {
		updated.HealthStatus = incoming.HealthStatus
	}
	if incoming.Models != nil {
		updated.Models = cloneModels(incoming.Models)
	}
	updated.RequestAdapter = cloneRequestAdapter(incoming.RequestAdapter)
	updated.ClaudeOAuth = mergeClaudeOAuthImport(existing.ClaudeOAuth, incoming.ClaudeOAuth)
	updated.CursorOAuth = mergeCursorOAuthImport(existing.CursorOAuth, incoming.CursorOAuth)
	return updated
}

func mergeClaudeOAuthImport(existing, incoming *domain.ClaudeOAuthCredential) *domain.ClaudeOAuthCredential {
	if incoming == nil {
		return cloneClaudeOAuth(existing)
	}
	if strings.TrimSpace(incoming.AccessToken) != "" || strings.TrimSpace(incoming.RefreshToken) != "" {
		return cloneClaudeOAuth(incoming)
	}
	// Metadata-only import: keep existing secrets, refresh non-secret fields when present.
	if existing == nil {
		return nil
	}
	merged := *existing
	if incoming.ExpiresAt != "" {
		merged.ExpiresAt = incoming.ExpiresAt
	}
	if incoming.Scope != "" {
		merged.Scope = incoming.Scope
	}
	if incoming.AccountLabel != "" {
		merged.AccountLabel = incoming.AccountLabel
	}
	return &merged
}

func mergeCursorOAuthImport(existing, incoming *domain.CursorOAuthCredential) *domain.CursorOAuthCredential {
	if incoming == nil {
		return cloneCursorOAuth(existing)
	}
	if strings.TrimSpace(incoming.AccessToken) != "" || strings.TrimSpace(incoming.RefreshToken) != "" {
		return cloneCursorOAuth(incoming)
	}
	if existing == nil {
		return nil
	}
	merged := *existing
	if incoming.ExpiresAt != "" {
		merged.ExpiresAt = incoming.ExpiresAt
	}
	if incoming.AccountLabel != "" {
		merged.AccountLabel = incoming.AccountLabel
	}
	return &merged
}

func cloneProviderForExport(provider domain.Provider) domain.Provider {
	cloned := provider
	cloned.Models = cloneModels(provider.Models)
	cloned.RequestAdapter = cloneRequestAdapter(provider.RequestAdapter)
	cloned.ClaudeOAuth = cloneClaudeOAuth(provider.ClaudeOAuth)
	cloned.CursorOAuth = cloneCursorOAuth(provider.CursorOAuth)
	if cloned.RequestAdapter != nil {
		cloned.RequestAdapter.CurlExample = ""
	}
	return cloned
}

func cloneModels(models []domain.Model) []domain.Model {
	if models == nil {
		return nil
	}
	out := make([]domain.Model, len(models))
	copy(out, models)
	return out
}

func cloneRequestAdapter(adapter *domain.RequestAdapter) *domain.RequestAdapter {
	if adapter == nil {
		return nil
	}
	cloned := *adapter
	if adapter.Headers != nil {
		cloned.Headers = make(map[string]string, len(adapter.Headers))
		for key, value := range adapter.Headers {
			cloned.Headers[key] = value
		}
	}
	if adapter.ModelMapping != nil {
		cloned.ModelMapping = make(map[string]string, len(adapter.ModelMapping))
		for key, value := range adapter.ModelMapping {
			cloned.ModelMapping[key] = value
		}
	}
	return &cloned
}

func cloneClaudeOAuth(cred *domain.ClaudeOAuthCredential) *domain.ClaudeOAuthCredential {
	if cred == nil {
		return nil
	}
	cloned := *cred
	return &cloned
}

func cloneCursorOAuth(cred *domain.CursorOAuthCredential) *domain.CursorOAuthCredential {
	if cred == nil {
		return nil
	}
	cloned := *cred
	return &cloned
}

func providerImportLabel(provider domain.Provider, index int) string {
	if strings.TrimSpace(provider.ID) != "" {
		return provider.ID
	}
	if strings.TrimSpace(provider.Name) != "" {
		return provider.Name
	}
	return fmt.Sprintf("providers[%d]", index)
}

// ParseProviderExportIDs splits a comma-separated ids query value.
func ParseProviderExportIDs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	ids := make([]string, 0, len(parts))
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}
