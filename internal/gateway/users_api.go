package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"golang.org/x/crypto/bcrypt"
)

// requireAdmin resolves the authenticated identity and rejects non-admins.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) (sessionIdentity, bool) {
	identity, ok := identityFromRequest(r)
	if !ok {
		writeOpenAIError(w, http.StatusUnauthorized, "authentication required")
		return sessionIdentity{}, false
	}
	if !identity.isAdmin() {
		writeOpenAIError(w, http.StatusForbidden, "admin permission required")
		return sessionIdentity{}, false
	}
	return identity, true
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.userStore == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "user store is not configured")
		return
	}
	users, err := s.userStore.ListUsers()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, users)
}

type userPayload struct {
	Username           string   `json:"username"`
	Password           string   `json:"password"`
	AllowedProviderIDs []string `json:"allowedProviderIds"`
	Enabled            *bool    `json:"enabled"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.userStore == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "user store is not configured")
		return
	}
	var payload userPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	username := strings.TrimSpace(payload.Username)
	if username == "" {
		writeOpenAIError(w, http.StatusBadRequest, "username is required")
		return
	}
	if strings.EqualFold(username, "admin") {
		writeOpenAIError(w, http.StatusBadRequest, "username \"admin\" is reserved")
		return
	}
	if len(strings.TrimSpace(payload.Password)) < adminPasswordMinLen {
		writeOpenAIError(w, http.StatusBadRequest, fmt.Sprintf("password must be at least %d characters", adminPasswordMinLen))
		return
	}
	if _, err := s.userStore.UserByUsername(username); err == nil {
		writeOpenAIError(w, http.StatusConflict, "username already exists")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(strings.TrimSpace(payload.Password)), bcrypt.DefaultCost)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	existing, _ := s.userStore.ListUsers()
	user := domain.User{
		ID: uniqueID(slug(username), func(id string) bool {
			if id == legacyAdminUserID {
				return true
			}
			for _, item := range existing {
				if item.ID == id {
					return true
				}
			}
			return false
		}),
		Username:           username,
		PasswordHash:       string(hash),
		Role:               domain.UserRoleUser,
		AllowedProviderIDs: s.sanitizeProviderIDs(payload.AllowedProviderIDs),
		Enabled:            true,
		CreatedAt:          nowRFC3339(),
	}
	if payload.Enabled != nil {
		user.Enabled = *payload.Enabled
	}
	if err := s.userStore.CreateUser(user); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logs.AddApp("info", "user created", user.Username)
	writeJSON(w, http.StatusCreated, user)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.userStore == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "user store is not configured")
		return
	}
	user, err := s.userStore.UserByID(r.PathValue("id"))
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, "user not found")
		return
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if value, ok := raw["username"]; ok {
		var username string
		if err := json.Unmarshal(value, &username); err == nil {
			username = strings.TrimSpace(username)
			if username != "" && !strings.EqualFold(username, "admin") {
				if other, err := s.userStore.UserByUsername(username); err == nil && other.ID != user.ID {
					writeOpenAIError(w, http.StatusConflict, "username already exists")
					return
				}
				user.Username = username
			}
		}
	}
	if value, ok := raw["allowedProviderIds"]; ok {
		var ids []string
		if err := json.Unmarshal(value, &ids); err == nil {
			user.AllowedProviderIDs = s.sanitizeProviderIDs(ids)
		}
	}
	if value, ok := raw["enabled"]; ok {
		var enabled bool
		if err := json.Unmarshal(value, &enabled); err == nil {
			user.Enabled = enabled
		}
	}
	if err := s.userStore.UpdateUser(user); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logs.AddApp("info", "user updated", user.Username)
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.userStore == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "user store is not configured")
		return
	}
	userID := r.PathValue("id")
	user, err := s.userStore.UserByID(userID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, "user not found")
		return
	}
	// Keys owned by the removed user fall back to admin ownership so traffic
	// and history stay intact.
	state := s.router.State()
	for _, key := range state.APIKeys {
		if key.OwnerUserID != userID {
			continue
		}
		if updated, err := s.router.UpdateAPIKeyOwner(key.ID, ""); err == nil && s.apiKeyStore != nil {
			_ = s.apiKeyStore.UpdateAPIKey(updated)
		}
	}
	if err := s.userStore.DeleteUser(userID); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logs.AddApp("info", "user deleted", user.Username)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) handleResetUserPassword(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.userStore == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "user store is not configured")
		return
	}
	user, err := s.userStore.UserByID(r.PathValue("id"))
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, "user not found")
		return
	}
	var payload struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if len(strings.TrimSpace(payload.Password)) < adminPasswordMinLen {
		writeOpenAIError(w, http.StatusBadRequest, fmt.Sprintf("password must be at least %d characters", adminPasswordMinLen))
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(strings.TrimSpace(payload.Password)), bcrypt.DefaultCost)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	user.PasswordHash = string(hash)
	if err := s.userStore.UpdateUser(user); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logs.AddApp("info", "user password reset", user.Username)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// sanitizeProviderIDs keeps only IDs of providers that actually exist.
func (s *Server) sanitizeProviderIDs(ids []string) []string {
	if len(ids) == 0 {
		return []string{}
	}
	known := map[string]bool{}
	for _, provider := range s.router.State().Providers {
		known[provider.ID] = true
	}
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] || !known[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
