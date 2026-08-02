package gateway

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func (s *Server) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.alertStore == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "alert storage unavailable")
		return
	}
	page, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("page")))
	pageSize, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("pageSize")))
	result, err := s.alertStore.QueryAlerts(monitor.AlertQuery{
		Status:   r.URL.Query().Get("status"),
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to query alerts: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleUpdateAlertStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.alertStore == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "alert storage unavailable")
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid alert id")
		return
	}
	var payload struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid alert status json: "+err.Error())
		return
	}
	status := strings.ToLower(strings.TrimSpace(payload.Status))
	switch status {
	case domain.AlertStatusUnread, domain.AlertStatusRead, domain.AlertStatusIgnored:
	default:
		writeOpenAIError(w, http.StatusBadRequest, "status must be unread, read or ignored")
		return
	}
	if err := s.alertStore.UpdateAlertStatus(id, status); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to update alert: "+err.Error())
		return
	}
	alert, found, err := s.alertStore.AlertByID(id)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to read alert: "+err.Error())
		return
	}
	if !found {
		writeOpenAIError(w, http.StatusNotFound, "alert not found")
		return
	}
	writeJSON(w, http.StatusOK, alert)
}

// handleRetryAlertPush re-sends one alert to Telegram. Automatic retries are
// deliberately absent (a bad token would hammer the API every scan), so this is
// the only retry path.
func (s *Server) handleRetryAlertPush(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.alertStore == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "alert storage unavailable")
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid alert id")
		return
	}
	alert, found, err := s.alertStore.AlertByID(id)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to read alert: "+err.Error())
		return
	}
	if !found {
		writeOpenAIError(w, http.StatusNotFound, "alert not found")
		return
	}
	s.pushAlert(alert, s.AlertSettings().Telegram)
	refreshed, _, err := s.alertStore.AlertByID(id)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to read alert: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, refreshed)
}

func (s *Server) handleAlertSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, redactAlertSettingsForClient(s.AlertSettings()))
}

func (s *Server) handleUpdateAlertSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var incoming domain.AlertSettings
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid alert settings json: "+err.Error())
		return
	}
	updated, err := s.UpdateAlertSettings(incoming)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save alert settings: "+err.Error())
		return
	}
	s.logs.AddApp("info", "alert settings updated", "")
	writeJSON(w, http.StatusOK, redactAlertSettingsForClient(updated))
}

// handleTelegramTest sends a probe message so the operator can confirm the bot
// token and chat id work before relying on real alerts.
func (s *Server) handleTelegramTest(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	telegram := s.AlertSettings().Telegram
	if err := sendTelegramMessage(telegram, "✅ LLM Protocol Gateway 告警推送测试成功"); err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "telegram test failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
