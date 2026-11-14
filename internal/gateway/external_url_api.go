package gateway

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/desktopupdate"
)

// handleDesktopOpenExternalURL opens a small allowlist of trusted HTTPS pages
// through the native desktop shell. The Wails WKWebView blocks window.open, so
// login pages and release pages must go through the OS browser instead.
func (s *Server) handleDesktopOpenExternalURL(w http.ResponseWriter, r *http.Request) {
	if !isLocalAdminBypass(r) {
		writeOpenAIError(w, http.StatusForbidden, "外部链接只能在本机 App 中打开")
		return
	}
	if s.externalURLOpener == nil {
		writeOpenAIError(w, http.StatusConflict, "当前没有可用的系统浏览器打开器")
		return
	}
	var payload struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid external URL json: "+err.Error())
		return
	}
	raw := strings.TrimSpace(payload.URL)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		writeOpenAIError(w, http.StatusBadRequest, "外部链接必须是 HTTPS URL")
		return
	}
	host := strings.ToLower(u.Hostname())
	allowed := host == "dash.cloudflare.com" || strings.HasSuffix(host, ".cloudflare.com")
	if host == "github.com" && strings.HasPrefix(u.Path, "/"+desktopupdate.DefaultRepo+"/releases/") {
		allowed = true
	}
	if !allowed {
		writeOpenAIError(w, http.StatusBadRequest, "外部链接不属于允许的 Cloudflare / GitHub 页面")
		return
	}
	if err := s.externalURLOpener(raw); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "打开外部链接失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
