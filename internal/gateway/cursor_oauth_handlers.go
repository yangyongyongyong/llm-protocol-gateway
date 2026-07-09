package gateway

import (
	"net/http"
	"strings"
	"time"
)

func (s *Server) handleCursorOAuthStart(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if _, err := s.router.ProviderByID(providerID); err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}

	verifier, challenge, err := generateCursorPKCE()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to generate pkce: "+err.Error())
		return
	}
	uuid, err := generateCursorOAuthUUID()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to generate uuid: "+err.Error())
		return
	}
	flowID, err := generateCursorOAuthFlowID()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to generate flow id: "+err.Error())
		return
	}

	s.pendingCursorOAuth.put(providerID, cursorOAuthPending{
		Verifier:  verifier,
		UUID:      uuid,
		FlowID:    flowID,
		CreatedAt: time.Now(),
	})
	s.pendingCursorOAuth.setStatus(flowID, "pending", "等待浏览器授权…")
	go s.pollCursorOAuthInBackground(providerID, flowID, uuid, verifier)

	loginURL := buildCursorLoginURL(challenge, uuid)
	s.logs.AddApp("info", "cursor oauth flow started", providerID)
	writeJSON(w, http.StatusOK, map[string]any{
		"authUrl": loginURL,
		"flowId":  flowID,
		"mode":    "poll",
	})
}

func (s *Server) handleCursorOAuthStatus(w http.ResponseWriter, r *http.Request) {
	flowID := strings.TrimSpace(r.URL.Query().Get("flowId"))
	if flowID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "flowId is required")
		return
	}
	status, ok := s.pendingCursorOAuth.status(flowID)
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "unknown or expired oauth flow")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleCursorOAuthDisconnect(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	updated, err := s.router.ClearProviderCursorOAuth(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "cursor oauth disconnected", providerID)
	writeJSON(w, http.StatusOK, redactProviderForClient(updated))
}
