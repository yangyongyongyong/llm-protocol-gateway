package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/luca/llm-protocol-gateway/internal/autostart"
)

// desktopAutostartStatus is the read-only shape returned to the console. The
// status is read from the OS record on every call rather than mirrored into
// GatewayState: a database boolean could disagree with System Settings, which
// is exactly the bug this row must avoid.
type desktopAutostartStatus = autostart.Status

func (s *Server) handleDesktopAutostart(w http.ResponseWriter, r *http.Request) {
	if !isLocalAdminBypass(r) {
		writeOpenAIError(w, http.StatusForbidden, "开机自动启动只能在本机 App 中修改")
		return
	}
	if s.desktopAutostart == nil {
		writeJSON(w, http.StatusOK, desktopAutostartStatus{
			Available: false,
			Message:   "当前网关不是桌面 App，无法设置开机自动启动",
		})
		return
	}
	writeJSON(w, http.StatusOK, s.desktopAutostart.Status())
}

func (s *Server) handleSetDesktopAutostart(w http.ResponseWriter, r *http.Request) {
	if !isLocalAdminBypass(r) {
		writeOpenAIError(w, http.StatusForbidden, "开机自动启动只能在本机 App 中修改")
		return
	}
	if s.desktopAutostart == nil {
		writeOpenAIError(w, http.StatusConflict, "当前网关不是桌面 App，无法设置开机自动启动")
		return
	}
	var payload struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Enabled == nil {
		writeOpenAIError(w, http.StatusBadRequest, `body must be {"enabled": true|false}`)
		return
	}
	status, err := s.desktopAutostart.Set(*payload.Enabled)
	if err != nil {
		if s.logs != nil {
			s.logs.AddApp("warn", "desktop autostart update failed", err.Error())
		}
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.logs != nil {
		s.logs.AddApp("info", "desktop autostart updated", fmt.Sprintf("enabled=%v mechanism=%s needsApproval=%v", status.Enabled, status.Mechanism, status.NeedsApproval))
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleOpenDesktopLoginItems(w http.ResponseWriter, r *http.Request) {
	if !isLocalAdminBypass(r) {
		writeOpenAIError(w, http.StatusForbidden, "登录项设置只能在本机 App 中打开")
		return
	}
	if s.desktopAutostart == nil {
		writeOpenAIError(w, http.StatusConflict, "当前网关不是桌面 App")
		return
	}
	if err := s.desktopAutostart.OpenLoginItems(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
