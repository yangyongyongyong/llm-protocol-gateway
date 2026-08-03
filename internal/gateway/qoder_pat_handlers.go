package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
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
	// proxied request. 400, not 502: this is Qoder rejecting the *client's*
	// PAT (bad format, revoked, wrong account) — a request-input problem, not
	// this gateway failing to reach an upstream. Using 502 here previously
	// made every rejection indistinguishable, over the public tunnel, from a
	// genuine Cloudflare "origin unreachable" page (both render as a generic
	// 502): the real reason from Qoder never reached the browser.
	credential, err := exchangeQoderJobToken(pat)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
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
		status["hasStoredToken"] = true
		status["disconnected"] = provider.QoderPAT.Disconnected
		status["connected"] = !provider.QoderPAT.Disconnected
		status["expiresAt"] = provider.QoderPAT.ExpiresAt
		status["accountLabel"] = provider.QoderPAT.AccountLabel
		status["tokenStale"] = qoderTokenNeedsRefresh(provider.QoderPAT)
		status["models"] = len(provider.Models)
	}
	writeJSON(w, http.StatusOK, status)
}

// handleQoderPATDisconnect pauses the connection: forwarding is blocked (see
// ensureFreshQoderToken) and the console shows it as disconnected, but the
// stored personal access token itself is kept so handleQoderPATReconnect can
// bring it back with no re-paste.
func (s *Server) handleQoderPATDisconnect(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderOwnerForUser(w, r, providerID) {
		return
	}
	updated, err := s.router.DisconnectProviderQoderPAT(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if updated.QoderPAT != nil {
		if err := s.persistProviderOAuth(updated.ID, nil, nil, nil, updated.QoderPAT); err != nil {
			s.logs.AddApp("warn", "failed to persist qoder disconnect", err.Error())
		}
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "qoder connection disconnected (personal access token retained for reconnect)", providerID)
	writeJSON(w, http.StatusOK, redactProviderForClient(updated))
}

// handleQoderPATReconnect re-activates a disconnected connection using the
// already-stored personal access token, with no body required.
func (s *Server) handleQoderPATReconnect(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderOwnerForUser(w, r, providerID) {
		return
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	pat := ""
	if provider.QoderPAT != nil {
		pat = strings.TrimSpace(provider.QoderPAT.RefreshToken)
	}
	if pat == "" {
		writeOpenAIError(w, http.StatusBadRequest, "no saved personal access token to reconnect with; paste one instead")
		return
	}
	credential, err := exchangeQoderJobToken(pat)
	if err != nil {
		// See handleQoderPATComplete: 400, not 502 — a rejected PAT is a
		// client-input problem, and 502 was indistinguishable from a genuine
		// Cloudflare origin-unreachable page over the public tunnel.
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := s.router.SetProviderQoderPAT(providerID, credential)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.persistProviderOAuth(updated.ID, nil, nil, nil, updated.QoderPAT); err != nil {
		s.logs.AddApp("warn", "failed to persist qoder reconnect", err.Error())
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
	s.logs.AddApp("info", "qoder reconnected using saved personal access token", providerID)
	writeJSON(w, http.StatusOK, redactProviderForClient(updated))
}
