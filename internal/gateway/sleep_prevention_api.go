package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// sleepPreventionStatus combines the persisted user choice with the live OS
// assertion. A true Enabled + false Active means the setting is saved but the
// platform manager could not hold it (for example after an OS-level failure).
type sleepPreventionStatus struct {
	Enabled   bool   `json:"enabled"`
	Available bool   `json:"available"`
	Active    bool   `json:"active"`
	Platform  string `json:"platform"`
	Message   string `json:"message,omitempty"`
}

func (s *Server) currentSleepPreventionStatus() sleepPreventionStatus {
	enabled := s.router != nil && s.router.PreventSleep()
	if s.sleepGuard == nil {
		return sleepPreventionStatus{
			Enabled: enabled,
			Message: "当前网关没有配置系统休眠管理器",
		}
	}
	status := s.sleepGuard.Status()
	return sleepPreventionStatus{
		Enabled:   enabled,
		Available: status.Available,
		Active:    status.Active,
		Platform:  status.Platform,
		Message:   status.Message,
	}
}

// handleSleepPrevention is local-only: changing the host's power policy from
// a remote browser would be surprising and could be used to drain a laptop.
func (s *Server) handleSleepPrevention(w http.ResponseWriter, r *http.Request) {
	if !isLocalAdminBypass(r) {
		writeOpenAIError(w, http.StatusForbidden, "防止系统休眠只能在本机修改")
		return
	}
	writeJSON(w, http.StatusOK, s.currentSleepPreventionStatus())
}

func (s *Server) handleSetSleepPrevention(w http.ResponseWriter, r *http.Request) {
	if !isLocalAdminBypass(r) {
		writeOpenAIError(w, http.StatusForbidden, "防止系统休眠只能在本机修改")
		return
	}
	var payload struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Enabled == nil {
		writeOpenAIError(w, http.StatusBadRequest, `body must be {"enabled": true|false}`)
		return
	}
	if s.sleepGuard == nil {
		writeOpenAIError(w, http.StatusConflict, "当前网关没有配置系统休眠管理器")
		return
	}
	enabled := *payload.Enabled
	if enabled {
		status := s.sleepGuard.Status()
		if !status.Available {
			writeOpenAIError(w, http.StatusConflict, status.Message)
			return
		}
	}

	previous := false
	if s.router != nil {
		previous = s.router.PreventSleep()
	}
	status, err := s.sleepGuard.Set(enabled)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.router != nil {
		s.router.SetPreventSleep(enabled)
	}
	if err := s.saveState(); err != nil {
		if s.router != nil {
			s.router.SetPreventSleep(previous)
		}
		_, _ = s.sleepGuard.Set(previous)
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save sleep prevention setting: "+err.Error())
		return
	}
	if s.logs != nil {
		s.logs.AddApp("info", "sleep prevention updated", fmt.Sprintf("enabled=%v active=%v platform=%s", enabled, status.Active, status.Platform))
	}
	writeJSON(w, http.StatusOK, s.currentSleepPreventionStatus())
}
