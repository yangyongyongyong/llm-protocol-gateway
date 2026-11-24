package gateway

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
)

const maxDesktopSaveTextFileBytes = 8 << 20

type desktopSaveTextFileRequest struct {
	DefaultFilename string `json:"defaultFilename"`
	Content         string `json:"content"`
}

// handleDesktopSaveTextFile 把网关页面的导出动作转交给桌面壳，由 Wails 弹出
// 原生“另存为”对话框。页面本身由 127.0.0.1 网关提供，不能直接调用 Wails 的
// window.go 绑定，所以必须经过这条本机回环桥。
func (s *Server) handleDesktopSaveTextFile(w http.ResponseWriter, r *http.Request) {
	if !isLocalAdminBypass(r) {
		writeOpenAIError(w, http.StatusForbidden, "保存文件只能在本机 App 中执行")
		return
	}
	if s.desktopSaveTextFile == nil {
		writeOpenAIError(w, http.StatusConflict, "当前没有可用的桌面保存对话框")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxDesktopSaveTextFileBytes)
	var payload desktopSaveTextFileRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid desktop save json: "+err.Error())
		return
	}

	filename := strings.TrimSpace(payload.DefaultFilename)
	// 默认文件名只用于填充对话框；不允许客户端夹带目录，最终路径仍由用户选择。
	filename = filepath.Base(strings.ReplaceAll(filename, "\\", "/"))
	if filename == "" || filename == "." || filename == string(filepath.Separator) {
		filename = "export.json"
	}
	if len(filename) > 255 {
		writeOpenAIError(w, http.StatusBadRequest, "默认文件名过长")
		return
	}
	if payload.Content == "" {
		writeOpenAIError(w, http.StatusBadRequest, "导出内容不能为空")
		return
	}

	saved, path, err := s.desktopSaveTextFile(filename, payload.Content)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "打开保存对话框失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"saved":     saved,
		"cancelled": !saved,
		"path":      path,
	})
}
