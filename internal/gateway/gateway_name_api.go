package gateway

import (
	"encoding/json"
	"net/http"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

type gatewayNameStatus struct {
	Name string `json:"name"`
}

// handleGatewayName exposes the local machine's display name. It is part of
// the gateway identity, not an account display name: the same label is shown
// on downstream peer registrations and in the console.
func (s *Server) handleGatewayName(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, domain.PermSettingsView) {
		return
	}
	writeJSON(w, http.StatusOK, gatewayNameStatus{Name: s.GatewayName()})
}

func (s *Server) handleSetGatewayName(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, domain.PermSettingsEdit) {
		return
	}
	var payload struct {
		Name *string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Name == nil {
		writeOpenAIError(w, http.StatusBadRequest, `body must be {"name":"..."}`)
		return
	}
	if err := s.SetGatewayName(*payload.Name); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.logs != nil {
		s.logs.AddApp("info", "gateway name updated", s.GatewayName())
	}
	writeJSON(w, http.StatusOK, gatewayNameStatus{Name: s.GatewayName()})
}
