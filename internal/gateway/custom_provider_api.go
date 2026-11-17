package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

const customConnectorBundleKind = "llm-protocol-gateway/custom-connector"

type customConnectorBundle struct {
	Kind           string                `json:"kind"`
	Version        int                   `json:"version"`
	ExportedAt     string                `json:"exportedAt,omitempty"`
	CustomProvider domain.CustomProvider `json:"customProvider"`
}

func (s *Server) handleListCustomProviders(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"customProviders": decorateCustomProvidersForClient(s.router.CustomProviders())})
}

func (s *Server) handleCustomProviderAIPrompt(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": providerScriptABIVersion,
		"prompt":  customProviderAIPrompt(),
		"abi":     providerScriptABI,
	})
}

func (s *Server) handleExportCustomProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	provider, err := s.router.CustomProviderByID(r.PathValue("id"))
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	provider.ScriptDir = ""
	provider.Script.UpstreamBaseURL = ""
	bundle := customConnectorBundle{
		Kind:           customConnectorBundleKind,
		Version:        1,
		ExportedAt:     time.Now().UTC().Format(time.RFC3339),
		CustomProvider: provider,
	}
	filename := safeScriptDirName(firstNonEmpty(provider.ID, provider.Name)) + ".custom-connector.json"
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	writeJSON(w, http.StatusOK, bundle)
}

func (s *Server) handleImportCustomProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid connector json: "+err.Error())
		return
	}
	provider, err := decodeCustomProviderImport(raw)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateCustomProviderDefinition(provider); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	replace := strings.EqualFold(r.URL.Query().Get("replace"), "true") || r.URL.Query().Get("replace") == "1"
	var imported domain.CustomProvider
	if existing, lookupErr := s.router.CustomProviderByID(provider.ID); lookupErr == nil {
		if !replace {
			writeOpenAIError(w, http.StatusConflict, fmt.Sprintf("custom connector %q already exists; retry with ?replace=true to overwrite", existing.ID))
			return
		}
		imported, err = s.router.UpdateCustomProvider(existing.ID, provider)
	} else {
		imported, err = s.router.AddCustomProvider(provider)
	}
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	scriptDir, err := writeCustomProviderScriptFiles(imported)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to write custom connector script: "+err.Error())
		return
	}
	imported.ScriptDir = scriptDir
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	if replace {
		if err := s.syncCustomProviderInstances(r, imported); err != nil {
			writeOpenAIError(w, http.StatusBadGateway, "custom connector imported but instances not fully refreshed: "+err.Error())
			return
		}
	}
	s.logs.AddApp("info", "custom connector imported", imported.ID)
	writeJSON(w, http.StatusOK, customConnectorBundle{Kind: customConnectorBundleKind, Version: 1, CustomProvider: imported})
}

func decodeCustomProviderImport(raw json.RawMessage) (domain.CustomProvider, error) {
	var bundle customConnectorBundle
	if err := json.Unmarshal(raw, &bundle); err == nil && (bundle.CustomProvider.ID != "" || bundle.CustomProvider.Name != "") {
		if bundle.Kind != "" && bundle.Kind != customConnectorBundleKind {
			return domain.CustomProvider{}, fmt.Errorf("unsupported connector bundle kind %q", bundle.Kind)
		}
		if bundle.Version > 1 {
			return domain.CustomProvider{}, fmt.Errorf("unsupported connector bundle version %d", bundle.Version)
		}
		return bundle.CustomProvider, nil
	}
	var provider domain.CustomProvider
	if err := json.Unmarshal(raw, &provider); err != nil {
		return domain.CustomProvider{}, fmt.Errorf("invalid connector json: %w", err)
	}
	return provider, nil
}

func (s *Server) handleCreateCustomProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var provider domain.CustomProvider
	if err := json.NewDecoder(r.Body).Decode(&provider); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid custom provider json: "+err.Error())
		return
	}
	if err := validateCustomProviderDefinition(provider); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := s.router.AddCustomProvider(provider)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	scriptDir, err := writeCustomProviderScriptFiles(created)
	if err != nil {
		_ = s.router.DeleteCustomProvider(created.ID)
		writeOpenAIError(w, http.StatusInternalServerError, "failed to write custom provider script: "+err.Error())
		return
	}
	created.ScriptDir = scriptDir
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "custom provider created", created.ID)
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdateCustomProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var provider domain.CustomProvider
	if err := json.NewDecoder(r.Body).Decode(&provider); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid custom provider json: "+err.Error())
		return
	}
	if err := validateCustomProviderDefinition(provider); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := s.router.UpdateCustomProvider(r.PathValue("id"), provider)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	scriptDir, err := writeCustomProviderScriptFiles(updated)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to write custom provider script: "+err.Error())
		return
	}
	updated.ScriptDir = scriptDir
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	if err := s.syncCustomProviderInstances(r, updated); err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "custom provider saved but instances not fully refreshed: "+err.Error())
		return
	}
	s.logs.AddApp("info", "custom provider updated", updated.ID)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteCustomProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if err := s.router.DeleteCustomProvider(r.PathValue("id")); err != nil {
		writeOpenAIError(w, http.StatusConflict, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	if err := removeCustomProviderScriptFiles(r.PathValue("id")); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "custom provider deleted but script files not removed: "+err.Error())
		return
	}
	s.logs.AddApp("info", "custom provider deleted", r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func validateCustomProviderDefinition(provider domain.CustomProvider) error {
	switch provider.Protocol {
	case domain.ProtocolOpenAIChat, domain.ProtocolOpenAIResponses, domain.ProtocolClaude:
	default:
		return fmt.Errorf("custom provider protocol must be openai_chat, openai_responses or claude")
	}
	if strings.TrimSpace(provider.Script.Source) == "" {
		return fmt.Errorf("custom provider script source is required")
	}
	script := provider.Script
	script.UpstreamBaseURL = ""
	validated := validateProviderScript(script, provider.Protocol)
	if !validated.Valid {
		return fmt.Errorf("invalid custom provider script: %s", strings.Join(validated.Errors, "; "))
	}
	return nil
}

// hydrateProviderFromCustomProvider 把模板和实例 URL/Key 组合成运行时 Provider。
// 脚本端口永远按实例 ID 派生，所以模板里的 ListenPort 被强制清零，避免同一类型
// 的多个实例争抢同一个端口。
func (s *Server) hydrateProviderFromCustomProvider(provider domain.Provider, template domain.CustomProvider, upstreamURL string) (domain.Provider, error) {
	upstreamURL = strings.TrimSpace(upstreamURL)
	parsed, err := url.Parse(upstreamURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return domain.Provider{}, fmt.Errorf("custom provider upstream URL must be a complete http/https URL")
	}
	script := template.Script
	script.UpstreamBaseURL = upstreamURL
	script.ListenPort = 0
	provider.CustomProviderID = template.ID
	provider.Protocol = template.Protocol
	provider.AuthType = domain.AuthTypeAPIKey
	provider.AuthHeader = firstNonEmpty(template.AuthHeader, "Authorization")
	if strings.TrimSpace(provider.DefaultModel) == "" {
		provider.DefaultModel = strings.TrimSpace(template.DefaultModel)
	}
	provider.ProviderScript = &script
	return provider, nil
}

// syncCustomProviderInstances 把模板更新同步到已存在的实例。每个实例保留自己的
// 上游 URL、Key、模型与分组，只替换脚本、协议和鉴权头，然后重启脚本进程。
func (s *Server) syncCustomProviderInstances(r *http.Request, template domain.CustomProvider) error {
	instances := s.router.ProvidersUsingCustomProvider(template.ID)
	if len(instances) == 0 {
		return nil
	}
	updates := make([]struct {
		before domain.Provider
		after  domain.Provider
	}, 0, len(instances))
	for _, before := range instances {
		upstream := ""
		if before.ProviderScript != nil {
			upstream = before.ProviderScript.UpstreamBaseURL
		}
		hydrated, err := s.hydrateProviderFromCustomProvider(before, template, upstream)
		if err != nil {
			return fmt.Errorf("provider %s: %w", before.ID, err)
		}
		hydrated.BaseURL = providerScriptRuntimes.apiBaseURL(hydrated)
		after, err := s.router.updateProviderWithScript(before.ID, hydrated, true)
		if err != nil {
			return fmt.Errorf("provider %s: %w", before.ID, err)
		}
		updates = append(updates, struct {
			before domain.Provider
			after  domain.Provider
		}{before: before, after: after})
	}
	if err := s.saveState(); err != nil {
		return err
	}
	for _, item := range updates {
		if !providerScriptRuntimeConfigChanged(item.before, item.after) {
			continue
		}
		providerScriptRuntimes.stop(item.after.ID)
		refreshed, discovery, err := s.startAndDiscoverProviderScript(r.Context(), item.after, false)
		if err != nil {
			s.logs.AddApp("warn", "custom provider instance restart failed", fmt.Sprintf("provider=%s: %v", item.after.ID, err))
			continue
		}
		s.autoDetectProviderProtocols(refreshed)
		s.logs.AddApp("info", "custom provider instance refreshed", fmt.Sprintf("provider=%s models=%d", item.after.ID, len(discovery.Models)))
	}
	return nil
}
