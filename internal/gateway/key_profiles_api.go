package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// ownsKey returns true when the identity may manage the given key (admins
// always; users only for keys they own).
func (s *Server) ownsKey(identity sessionIdentity, keyID string) bool {
	if identity.isAdmin() {
		return true
	}
	for _, key := range s.router.State().APIKeys {
		if key.ID == keyID {
			return key.OwnerUserID == identity.UserID
		}
	}
	return false
}

// validateProfileProvidersForUser reuses the key provider whitelist check by
// projecting a profile's routing fields onto a temporary APIKey.
func (s *Server) validateProfileProvidersForUser(identity sessionIdentity, profile domain.KeyProfile) error {
	return s.validateKeyProvidersForUser(identity, domain.APIKey{
		RouteID:             profile.RouteID,
		FallbackProviderIDs: profile.FallbackProviderIDs,
	})
}

// persistKey writes an updated key back to the store (best-effort logging).
func (s *Server) persistKey(w http.ResponseWriter, key domain.APIKey) bool {
	if s.apiKeyStore == nil {
		return true
	}
	if err := s.apiKeyStore.UpdateAPIKey(key); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save api key: "+err.Error())
		return false
	}
	return true
}

func (s *Server) handleCreateKeyProfile(w http.ResponseWriter, r *http.Request) {
	identity := s.requestIdentity(r)
	keyID := r.PathValue("id")
	if !s.ownsKey(identity, keyID) {
		writeOpenAIError(w, http.StatusForbidden, "permission denied")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid profile body: "+err.Error())
		return
	}
	var payload struct {
		domain.KeyProfile
		Activate bool `json:"activate"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid profile json: "+err.Error())
		return
	}
	profile := payload.KeyProfile
	if !jsonHasKey(body, "streamEnabled") {
		profile.StreamEnabled = true
	}
	if !identity.isAdmin() {
		if err := s.validateProfileProvidersForUser(identity, profile); err != nil {
			writeOpenAIError(w, http.StatusForbidden, err.Error())
			return
		}
	}
	updated, err := s.router.AddKeyProfile(keyID, profile, payload.Activate)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.persistKey(w, updated) {
		return
	}
	s.logs.AddApp("info", "api key profile created", updated.ID)
	writeJSON(w, http.StatusCreated, updated)
}

func (s *Server) handleUpdateKeyProfile(w http.ResponseWriter, r *http.Request) {
	identity := s.requestIdentity(r)
	keyID := r.PathValue("id")
	profileID := r.PathValue("pid")
	if !s.ownsKey(identity, keyID) {
		writeOpenAIError(w, http.StatusForbidden, "permission denied")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid profile body: "+err.Error())
		return
	}
	var profile domain.KeyProfile
	if err := json.Unmarshal(body, &profile); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid profile json: "+err.Error())
		return
	}
	if !jsonHasKey(body, "streamEnabled") {
		profile.StreamEnabled = true
	}
	if !identity.isAdmin() {
		if err := s.validateProfileProvidersForUser(identity, profile); err != nil {
			writeOpenAIError(w, http.StatusForbidden, err.Error())
			return
		}
	}
	updated, err := s.router.UpdateKeyProfile(keyID, profileID, profile)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.persistKey(w, updated) {
		return
	}
	s.logs.AddApp("info", "api key profile updated", updated.ID)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteKeyProfile(w http.ResponseWriter, r *http.Request) {
	identity := s.requestIdentity(r)
	keyID := r.PathValue("id")
	profileID := r.PathValue("pid")
	if !s.ownsKey(identity, keyID) {
		writeOpenAIError(w, http.StatusForbidden, "permission denied")
		return
	}
	updated, err := s.router.DeleteKeyProfile(keyID, profileID)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.persistKey(w, updated) {
		return
	}
	s.logs.AddApp("info", "api key profile deleted", updated.ID)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleSwitchKeyProfile(w http.ResponseWriter, r *http.Request) {
	identity := s.requestIdentity(r)
	keyID := r.PathValue("id")
	if !s.ownsKey(identity, keyID) {
		writeOpenAIError(w, http.StatusForbidden, "permission denied")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	var payload struct {
		ProfileID string `json:"profileId"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	updated, err := s.router.SwitchKeyProfile(keyID, strings.TrimSpace(payload.ProfileID))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.persistKey(w, updated) {
		return
	}
	s.logs.AddApp("info", "api key profile switched", updated.ID)
	writeJSON(w, http.StatusOK, updated)
}
