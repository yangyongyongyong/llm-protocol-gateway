package gateway

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/desktopupdate"
)

// desktopUpdater is intentionally a small interface: the desktop shell owns
// the updater, while the gateway only exposes its state to the locally opened
// console. Headless servers leave it nil.
type desktopUpdater interface {
	Status() desktopupdate.Status
	CheckNow() bool
	DownloadNow() bool
	InstallNow() bool
	SetSettings(desktopupdate.Settings) (desktopupdate.Status, error)
}

// SetDesktopUpdater attaches the desktop shell's updater to this gateway.
func (s *Server) SetDesktopUpdater(u desktopUpdater) {
	s.desktopUpdater = u
}

// CurrentInFlight exposes the model-call count for the desktop updater's
// optional idle install. It is deliberately model traffic, not console polls.
func (s *Server) CurrentInFlight() int64 {
	return s.currentInFlight()
}

func unavailableDesktopUpdateStatus() desktopupdate.Status {
	return desktopupdate.Status{
		Available: false,
		State:     desktopupdate.StateUnavailable,
		Message:   "当前网关不是从 macOS .app 启动的桌面版本，无法自动更新 App",
		Settings:  desktopupdate.DefaultSettings(),
	}
}

func (s *Server) desktopUpdateStatus() desktopupdate.Status {
	if s.desktopUpdater == nil {
		return unavailableDesktopUpdateStatus()
	}
	return s.desktopUpdater.Status()
}

func (s *Server) handleDesktopUpdate(w http.ResponseWriter, r *http.Request) {
	if !isLocalAdminBypass(r) {
		writeOpenAIError(w, http.StatusForbidden, "自动更新只能在本机 App 中管理")
		return
	}
	writeJSON(w, http.StatusOK, s.desktopUpdateStatus())
}

func (s *Server) handleDesktopUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalDesktopUpdate(w, r) {
		return
	}
	if !s.desktopUpdater.CheckNow() {
		writeOpenAIError(w, http.StatusConflict, "当前状态不能开始检查更新")
		return
	}
	writeJSON(w, http.StatusAccepted, s.desktopUpdateStatus())
}

func (s *Server) handleDesktopUpdateDownload(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalDesktopUpdate(w, r) {
		return
	}
	if !s.desktopUpdater.DownloadNow() {
		writeOpenAIError(w, http.StatusConflict, "当前没有可下载的更新")
		return
	}
	writeJSON(w, http.StatusAccepted, s.desktopUpdateStatus())
}

func (s *Server) handleDesktopUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalDesktopUpdate(w, r) {
		return
	}
	if !s.desktopUpdater.InstallNow() {
		writeOpenAIError(w, http.StatusConflict, "更新尚未下载完成，或当前 App 无法自动安装")
		return
	}
	if s.logs != nil {
		s.logs.AddApp("info", "desktop update install requested", "replacing the app bundle and restarting")
	}
	writeJSON(w, http.StatusAccepted, s.desktopUpdateStatus())
}

// handleDesktopUpdateOpenRelease opens the current release page in the system
// browser. It deliberately accepts no URL from the caller: the URL comes from
// the updater's own status and must belong to this repository's GitHub Releases.
func (s *Server) handleDesktopUpdateOpenRelease(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalDesktopUpdate(w, r) {
		return
	}
	raw := strings.TrimSpace(s.desktopUpdater.Status().ReleaseURL)
	if raw == "" {
		writeOpenAIError(w, http.StatusConflict, "当前没有可打开的 Release 页面")
		return
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") ||
		!strings.HasPrefix(u.Path, "/"+desktopupdate.DefaultRepo+"/releases/") {
		writeOpenAIError(w, http.StatusBadRequest, "Release 地址不属于本项目的 GitHub Releases")
		return
	}
	if s.externalURLOpener == nil {
		writeOpenAIError(w, http.StatusConflict, "当前没有可用的系统浏览器打开器")
		return
	}
	if err := s.externalURLOpener(raw); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "打开 Release 失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDesktopUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalDesktopUpdate(w, r) {
		return
	}
	var settings desktopupdate.Settings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid update settings: "+err.Error())
		return
	}
	status, err := s.desktopUpdater.SetSettings(settings)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.logs != nil {
		s.logs.AddApp("info", "desktop update settings changed", strings.Join([]string{
			"autoCheck=" + boolString(settings.AutoCheck),
			"autoDownload=" + boolString(settings.AutoDownload),
			"autoInstall=" + boolString(settings.AutoInstall),
		}, " "))
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) requireLocalDesktopUpdate(w http.ResponseWriter, r *http.Request) bool {
	if !isLocalAdminBypass(r) {
		writeOpenAIError(w, http.StatusForbidden, "自动更新只能在本机 App 中操作")
		return false
	}
	if s.desktopUpdater == nil {
		writeOpenAIError(w, http.StatusConflict, "当前不是桌面 App，无法自动更新 App")
		return false
	}
	return true
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
