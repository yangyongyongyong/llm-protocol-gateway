package gateway

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// providerGroupPayload 是分组创建/重命名的请求体。
type providerGroupPayload struct {
	Name string `json:"name"`
}

// providerGroupAssignPayload 批量移动 Provider；GroupID 为空表示移出分组。
type providerGroupAssignPayload struct {
	ProviderIDs []string `json:"providerIds"`
	GroupID     string   `json:"groupId"`
}

// providerGroupsReorderPayload 是分组拖拽/上下移动后的完整顺序。
type providerGroupsReorderPayload struct {
	IDs []string `json:"ids"`
}

// handleListProviderGroups 返回管理员侧 Provider 分组。
// 分组只是控制台归类元数据，普通用户不可见，也不参与请求鉴权。
func (s *Server) handleListProviderGroups(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providerGroups": s.router.ProviderGroups()})
}

// handleCreateProviderGroup 创建管理员分组。
func (s *Server) handleCreateProviderGroup(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var payload providerGroupPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid provider group json: "+err.Error())
		return
	}
	group, err := s.router.AddProviderGroup(payload.Name)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "provider group created", group.ID)
	writeJSON(w, http.StatusCreated, group)
}

// handleUpdateProviderGroup 重命名管理员分组。Provider 的 groupId 不变。
func (s *Server) handleUpdateProviderGroup(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var payload providerGroupPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid provider group json: "+err.Error())
		return
	}
	group, err := s.router.UpdateProviderGroup(r.PathValue("id"), payload.Name)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "provider group updated", group.ID)
	writeJSON(w, http.StatusOK, group)
}

// handleDeleteProviderGroup 删除管理员分组。组内 Provider 自动变成未分组，
// 不会删除 Provider，也不会改变任何用户授权。
func (s *Server) handleDeleteProviderGroup(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	groupID := strings.TrimSpace(r.PathValue("id"))
	moved, err := s.router.DeleteProviderGroup(groupID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "provider group deleted", groupID)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "ungroupedProviderCount": moved})
}

// handleReorderProviderGroups 保存管理员调整后的分组顺序。
func (s *Server) handleReorderProviderGroups(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var payload providerGroupsReorderPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid provider group order json: "+err.Error())
		return
	}
	s.router.ReorderProviderGroups(payload.IDs)
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providerGroups": s.router.ProviderGroups()})
}

// handleAssignProviderGroup 批量移动 Provider。普通用户永远走不到这里；
// 管理员把 Private/共享分组怎么使用，完全由自己决定，分组本身不构成 ACL。
func (s *Server) handleAssignProviderGroup(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var payload providerGroupAssignPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid provider group assignment json: "+err.Error())
		return
	}
	if len(payload.ProviderIDs) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "providerIds is required")
		return
	}
	updated, err := s.router.AssignProvidersToGroup(payload.ProviderIDs, payload.GroupID)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "providers moved to group", strings.TrimSpace(payload.GroupID))
	writeJSON(w, http.StatusOK, map[string]any{"updated": updated, "groupId": strings.TrimSpace(payload.GroupID)})
}

// providerGroupExists 供 Provider 创建时校验管理员选中的分组。
func (s *Server) providerGroupExists(groupID string) bool {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return true
	}
	if s.router == nil {
		return false
	}
	_, err := s.router.ProviderGroupByID(groupID)
	return err == nil
}

// stripProviderGroupsForUser 去掉普通用户不应感知的管理员分组信息。
// 这里只影响展示状态，不参与任何权限判断。
func stripProviderGroupsForUser(state *domain.GatewayState) {
	if state == nil {
		return
	}
	state.ProviderGroups = []domain.ProviderGroup{}
	for index := range state.Providers {
		state.Providers[index].GroupID = ""
	}
}
