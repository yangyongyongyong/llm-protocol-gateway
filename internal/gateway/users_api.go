package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

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

// requirePrincipal resolves the RBAC principal of the request.
func (s *Server) requirePrincipal(w http.ResponseWriter, r *http.Request) (principal, bool) {
	p := s.requestPrincipal(r)
	if p.UserID == "" {
		writeOpenAIError(w, http.StatusUnauthorized, "authentication required")
		return principal{}, false
	}
	return p, true
}

// consoleUser is the /__users payload: the account plus its role summary and
// its granted resource ids, so the console renders one row without extra calls.
type consoleUser struct {
	domain.User
	RoleName     string   `json:"roleName"`
	RoleScope    string   `json:"roleScope"`
	ProviderIDs  []string `json:"providerIds,omitempty"`
	Grants       []string `json:"grantedProviderIds,omitempty"`
	AllProviders bool     `json:"allProviders,omitempty"`
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
	grants := s.resourceGrants()
	out := make([]consoleUser, 0, len(users))
	for _, user := range users {
		// 覆盖为内存中的精确活跃时间：落库最多 5 分钟一次，直接读库会偏旧。
		if at := s.userLastActive(user.ID); !at.IsZero() {
			user.LastActiveAt = at.Format(time.RFC3339)
		}
		role := s.roleByID(user.RoleID)
		item := consoleUser{User: user, RoleName: role.Name, RoleScope: string(role.EffectiveScope())}
		if user.IsPeer() {
			item.AllProviders, item.Grants = s.peerGrantState(user.ID)
		} else {
			item.Grants = grants[user.ID]
		}
		if item.Grants == nil {
			item.Grants = []string{}
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

type userPayload struct {
	Username    string   `json:"username"`
	Password    string   `json:"password"`
	RoleID      string   `json:"roleId"`
	DisplayName string   `json:"displayName"`
	AvatarURL   string   `json:"avatarUrl"`
	Enabled     *bool    `json:"enabled"`
	GrantedIDs  []string `json:"grantedProviderIds"`
	// Role is the legacy role field; accepted so old clients keep working.
	Role string `json:"role"`
}

// resolveRoleID validates a requested role id and returns the role plus the
// legacy role value it maps onto.
func (s *Server) resolveRoleID(raw, legacy string) (domain.Role, bool) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		raw = strings.ToLower(strings.TrimSpace(legacy))
	}
	if raw == "" {
		raw = domain.RoleIDUser
	}
	role := s.roleByID(raw, legacy)
	// roleByID falls back to "user" for unknown ids, so check the id survived.
	if role.ID != raw {
		return domain.Role{}, false
	}
	return role, true
}

func legacyRoleFor(role domain.Role) domain.UserRole {
	if role.Has(domain.PermAll) {
		return domain.UserRoleAdmin
	}
	return domain.UserRoleUser
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
	role, validRole := s.resolveRoleID(payload.RoleID, payload.Role)
	if !validRole {
		writeOpenAIError(w, http.StatusBadRequest, "unknown role")
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
		Username:     username,
		PasswordHash: string(hash),
		Role:         legacyRoleFor(role),
		RoleID:       role.ID,
		DisplayName:  strings.TrimSpace(payload.DisplayName),
		AvatarURL:    strings.TrimSpace(payload.AvatarURL),
		Enabled:      true,
		CreatedAt:    nowRFC3339(),
	}
	if payload.Enabled != nil {
		user.Enabled = *payload.Enabled
	}
	if err := s.userStore.CreateUser(user); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if grants := payload.GrantedIDs; len(grants) > 0 {
		if store, ok := s.roleStoreRef(); ok {
			_ = store.SetResourceGrants(user.ID, grants)
		}
	}
	s.logs.AddApp("info", "user created", fmt.Sprintf("%s (role=%s)", user.Username, role.ID))
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
	previousRole := user.RoleID
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
	if _, hasRoleID := raw["roleId"]; hasRoleID || raw["role"] != nil {
		var roleID, legacy string
		if value, ok := raw["roleId"]; ok {
			if err := json.Unmarshal(value, &roleID); err != nil {
				writeOpenAIError(w, http.StatusBadRequest, "roleId must be a string")
				return
			}
		}
		if legacyValue, ok := raw["role"]; ok {
			_ = json.Unmarshal(legacyValue, &legacy)
		}
		role, validRole := s.resolveRoleID(roleID, legacy)
		if !validRole {
			writeOpenAIError(w, http.StatusBadRequest, "unknown role")
			return
		}
		user.RoleID = role.ID
		user.Role = legacyRoleFor(role)
	}
	if value, ok := raw["displayName"]; ok {
		var name string
		if err := json.Unmarshal(value, &name); err == nil {
			user.DisplayName = strings.TrimSpace(name)
		}
	}
	if value, ok := raw["avatarUrl"]; ok {
		var avatar string
		if err := json.Unmarshal(value, &avatar); err == nil {
			user.AvatarURL = strings.TrimSpace(avatar)
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
	if value, ok := raw["grantedProviderIds"]; ok {
		var ids []string
		if err := json.Unmarshal(value, &ids); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "grantedProviderIds must be a string array")
			return
		}
		if store, ok := s.roleStoreRef(); ok {
			if err := store.SetResourceGrants(user.ID, ids); err != nil {
				writeOpenAIError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}
	if user.RoleID != previousRole {
		s.logs.AddApp("info", "user role changed", fmt.Sprintf("%s: %s -> %s", user.Username, previousRole, user.RoleID))
	} else {
		s.logs.AddApp("info", "user updated", user.Username)
	}
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
	// Keys owned by the removed user fall back to the administrator's account
	// (not to an empty owner, which the console would label as a generic
	// "管理员"), so traffic and history stay intact and attributable.
	adminID := s.adminOwnerID()
	state := s.router.State()
	for _, key := range state.APIKeys {
		if key.OwnerUserID != userID {
			continue
		}
		if updated, err := s.router.UpdateAPIKeyOwner(key.ID, adminID); err == nil && s.apiKeyStore != nil {
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
