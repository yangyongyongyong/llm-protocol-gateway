// UI 诊断日志：把浏览器端实际发生的事上报到后端应用日志。
//
// 背景：两个只在真实浏览器里复现的问题（连接报错内容、筛选框被自动填充
// "admin"）远程排查了多轮仍分歧，靠转述与猜测无法收敛。此文件提供
// POST /__ui-log，前端在关键事件处上报一行结构化描述，管理员在
// 「设置 → 应用日志」或 GET /__app/logs 直接查看，无需用户截图/转述。
//
// 安全：仅记录调用方主动传入的摘要字段；handler 会拒绝把疑似令牌
// （pt-/sk- 前缀）写进日志。
package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

const uiLogMaxField = 400

var uiLogSecretPattern = regexp.MustCompile(`(?:pt|sk|sk-gw)-[A-Za-z0-9_\-]{8,}`)

func uiLogSanitize(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > uiLogMaxField {
		value = value[:uiLogMaxField] + "…"
	}
	return uiLogSecretPattern.ReplaceAllString(value, "***redacted***")
}

// handleUILog ingests one client-side diagnostic event.
func (s *Server) handleUILog(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.sessionIdentity(r)
	if !ok {
		writeOpenAIError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var payload struct {
		Event  string `json:"event"`
		Detail string `json:"detail"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024)).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid ui log json")
		return
	}
	event := uiLogSanitize(payload.Event)
	if event == "" {
		writeOpenAIError(w, http.StatusBadRequest, "event is required")
		return
	}
	s.logs.AddApp("error", "ui-diag: "+event,
		fmt.Sprintf("user=%s %s", identity.UserID, uiLogSanitize(payload.Detail)))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
