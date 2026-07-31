package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// handleQoderPATComplete accepts a pasted personal access token, exchanges it
// once to prove it works, and stores both tiers. There is no /start endpoint:
// Qoder has no browser round-trip.
func (s *Server) handleQoderPATComplete(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderOwnerForUser(w, r, providerID) {
		return
	}
	if _, err := s.router.ProviderByID(providerID); err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "failed to read body: "+err.Error())
		return
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	pat := strings.TrimSpace(payload.Token)
	if pat == "" {
		writeOpenAIError(w, http.StatusBadRequest, "token is required")
		return
	}
	// Exchange up front so a bad token fails here rather than on the first
	// proxied request.
	credential, err := exchangeQoderJobToken(pat)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	updated, err := s.router.SetProviderQoderPAT(providerID, credential)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	if synced, err := s.syncQoderProviderModels(providerID); err == nil {
		updated = synced
	} else {
		s.logs.AddApp("warn", "qoder model sync failed", err.Error())
	}
	s.logs.AddApp("info", "qoder personal access token connected", providerID)
	writeJSON(w, http.StatusOK, redactProviderForClient(updated))
}

// handleQoderPATStatus reports connection state and job-token freshness only. It
// never returns token material. There is no quota endpoint to report on.
func (s *Server) handleQoderPATStatus(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderOwnerForUser(w, r, providerID) {
		return
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	status := map[string]any{"connected": false}
	if provider.QoderPAT != nil && strings.TrimSpace(provider.QoderPAT.RefreshToken) != "" {
		status["connected"] = true
		status["expiresAt"] = provider.QoderPAT.ExpiresAt
		status["accountLabel"] = provider.QoderPAT.AccountLabel
		status["tokenStale"] = qoderTokenNeedsRefresh(provider.QoderPAT)
		status["models"] = len(provider.Models)
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleQoderPATDisconnect(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderOwnerForUser(w, r, providerID) {
		return
	}
	updated, err := s.router.ClearProviderQoderPAT(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	// Blank the persisted columns too, otherwise the stale PAT is reloaded on
	// the next restart.
	_ = s.persistProviderOAuth(updated.ID, nil, nil, nil, &domain.QoderPATCredential{})
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "qoder personal access token disconnected", providerID)
	writeJSON(w, http.StatusOK, redactProviderForClient(updated))
}
