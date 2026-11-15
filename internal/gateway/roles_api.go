package gateway

import (
	"errors"
	"net/http"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// handleListRoles returns every assignable role with its permission set.
func (s *Server) handleListRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := s.listRoles()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	annotated := make([]roleWithUsage, 0, len(roles))
	counts := s.usersPerRole()
	for _, role := range roles {
		annotated = append(annotated, roleWithUsage{Role: role, UserCount: counts[role.ID]})
	}
	writeJSON(w, http.StatusOK, annotated)
}

type roleWithUsage struct {
	domain.Role
	UserCount int `json:"userCount"`
}

func (s *Server) usersPerRole() map[string]int {
	counts := map[string]int{}
	if s.userStore == nil {
		return counts
	}
	users, err := s.userStore.ListUsers()
	if err != nil {
		return counts
	}
	for _, user := range users {
		counts[user.RoleID]++
	}
	return counts
}

// handleRoleCatalog returns the permission catalog the role editor renders.
func (s *Server) handleRoleCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"modules": domain.PermissionCatalog(),
		"scopes": []map[string]string{
			{"id": string(domain.ScopeOwn), "label": "自己的 + 管理员分配", "description": "自己创建的 Provider 与密钥，外加管理员在用户管理里分配的 Provider"},
			{"id": string(domain.ScopeAll), "label": "全部资源", "description": "与管理员一样看到实例上的所有资源（慎用）"},
		},
	})
}

// handleGrantTargets lists the providers an administrator can assign to a user
// from the users page.
func (s *Server) handleGrantTargets(w http.ResponseWriter, r *http.Request) {
	type target struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		OwnerID      string `json:"ownerId"`
		OwnerName    string `json:"ownerName"`
		Protocol     string `json:"protocol"`
		Disabled     bool   `json:"disabled"`
		GroupID      string `json:"groupId,omitempty"`
		GroupName    string `json:"groupName,omitempty"`
		AccountLabel string `json:"accountLabel,omitempty"`
		Phone        string `json:"phone,omitempty"`
	}
	out := []target{}
	if s.router == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	names := map[string]string{}
	state := s.router.State()
	groupNames := make(map[string]string, len(state.ProviderGroups))
	for _, group := range state.ProviderGroups {
		groupNames[group.ID] = group.Name
	}
	for _, provider := range state.Providers {
		if provider.Deleted {
			continue
		}
		groupID := strings.TrimSpace(provider.GroupID)
		ownerID := strings.TrimSpace(provider.OwnerUserID)
		ownerName, ok := names[ownerID]
		if !ok {
			ownerName = s.resolveUserName(ownerID)
			names[ownerID] = ownerName
		}
		out = append(out, target{
			ID: provider.ID, Name: provider.Name, OwnerID: ownerID, OwnerName: ownerName,
			Protocol: string(provider.Protocol), Disabled: provider.Disabled,
			GroupID: groupID, GroupName: groupNames[groupID],
			AccountLabel: peerProviderAccountLabel(provider), Phone: peerProviderAccountPhone(provider),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type rolePayload struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Scope       string   `json:"scope"`
	Permissions []string `json:"permissions"`
}

// parseRolePayload validates a submitted role and returns it in domain form.
func parseRolePayload(payload rolePayload) (domain.Role, error) {
	name := strings.TrimSpace(payload.Name)
	if name == "" {
		return domain.Role{}, errors.New("role name is required")
	}
	scope, ok := domain.ParseResourceScope(strings.TrimSpace(payload.Scope))
	if !ok {
		scope = domain.ScopeOwn
	}
	perms := make([]domain.Permission, 0, len(payload.Permissions))
	seen := map[domain.Permission]bool{}
	for _, raw := range payload.Permissions {
		key := domain.Permission(strings.TrimSpace(raw))
		if key == "" || seen[key] {
			continue
		}
		// The administrator wildcard can never be granted to a custom role:
		// only the built-in admin role may manage roles and owners.
		if !domain.IsKnownPermission(key) {
			return domain.Role{}, errors.New("unknown permission: " + string(key))
		}
		seen[key] = true
		perms = append(perms, key)
	}
	return domain.Role{Name: name, Description: strings.TrimSpace(payload.Description), Scope: scope, Permissions: perms}, nil
}

func (s *Server) handleCreateRole(w http.ResponseWriter, r *http.Request) {
	var payload rolePayload
	if !decodeJSONBody(w, r, &payload) {
		return
	}
	s.saveNewRole(w, payload)
}

func (s *Server) handleUpdateRole(w http.ResponseWriter, r *http.Request) {
	roleID := r.PathValue("id")
	if domain.IsBuiltinRole(roleID) {
		writeOpenAIError(w, http.StatusBadRequest, "built-in roles cannot be edited")
		return
	}
	var payload rolePayload
	if !decodeJSONBody(w, r, &payload) {
		return
	}
	role, err := parseRolePayload(payload)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	store, ok := s.roleStoreRef()
	if !ok {
		writeOpenAIError(w, http.StatusServiceUnavailable, "role store is not configured")
		return
	}
	current, err := store.RoleByID(roleID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, "role not found")
		return
	}
	role.ID = current.ID
	role.CreatedAt = current.CreatedAt
	role.UpdatedAt = nowRFC3339()
	if err := store.SaveRole(role); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logs.AddApp("info", "role updated", role.ID)
	writeJSON(w, http.StatusOK, role)
}

func (s *Server) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	roleID := r.PathValue("id")
	if domain.IsBuiltinRole(roleID) {
		writeOpenAIError(w, http.StatusBadRequest, "built-in roles cannot be deleted")
		return
	}
	// Refuse to delete a role still in use: its members would silently fall
	// back to "user", which is a permission change nobody asked for.
	if count := s.usersPerRole()[roleID]; count > 0 {
		writeOpenAIError(w, http.StatusConflict, "role is assigned to users; reassign them first")
		return
	}
	store, ok := s.roleStoreRef()
	if !ok {
		writeOpenAIError(w, http.StatusServiceUnavailable, "role store is not configured")
		return
	}
	if _, err := store.RoleByID(roleID); err != nil {
		writeOpenAIError(w, http.StatusNotFound, "role not found")
		return
	}
	if err := store.DeleteRole(roleID); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logs.AddApp("info", "role deleted", roleID)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// handleCopyRole copies an existing role into a new custom role (the
// "以此为基础" action in the role manager).
func (s *Server) handleCopyRole(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("id")
	source := s.roleByID(sourceID)
	if source.ID != sourceID {
		writeOpenAIError(w, http.StatusNotFound, "role not found")
		return
	}
	payload := rolePayload{Name: source.Name + " 副本", Description: source.Description, Scope: string(source.EffectiveScope())}
	for _, perm := range source.Permissions {
		if perm != domain.PermAll {
			payload.Permissions = append(payload.Permissions, string(perm))
		}
	}
	s.saveNewRole(w, payload)
}

// saveNewRole validates and inserts a role with a fresh id.
func (s *Server) saveNewRole(w http.ResponseWriter, payload rolePayload) {
	role, err := parseRolePayload(payload)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	store, ok := s.roleStoreRef()
	if !ok {
		writeOpenAIError(w, http.StatusServiceUnavailable, "role store is not configured")
		return
	}
	custom, _ := store.ListRoles()
	role.ID = uniqueID(slug(role.Name), func(id string) bool {
		if domain.IsBuiltinRole(id) || id == legacyAdminUserID {
			return true
		}
		for _, item := range custom {
			if item.ID == id {
				return true
			}
		}
		return false
	})
	role.CreatedAt = nowRFC3339()
	role.UpdatedAt = role.CreatedAt
	if err := store.SaveRole(role); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logs.AddApp("info", "role created", role.ID)
	writeJSON(w, http.StatusCreated, role)
}
