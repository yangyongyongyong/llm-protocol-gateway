package gateway

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/clidesktop"
)

// handleSetupClients 把「运行网关的这台机器」上的 Claude Code CLI 与
// Claude Desktop 指向本网关。
//
// 这是一条会写本机文件的接口，比别的管理接口更要紧：它落盘的内容里带 API
// 密钥。所以除了管理员身份，还要求请求来自回环地址——反代后面的网关永远看到
// 127.0.0.1 的对端，光看身份会把公网域名上的管理员当成坐在本机的人。
//
// 写入逻辑本身在 internal/clidesktop：那里只改各自负责的键、改前备份、遇到
// 坏 JSON 直接放弃，不会把用户的配置覆盖成空对象。
func (s *Server) handleSetupClients(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if !isLocalAdminBypass(r) {
		writeOpenAIError(w, http.StatusForbidden,
			"clients can only be configured from the machine running the gateway")
		return
	}
	var input struct {
		BaseURL    string `json:"baseUrl"`
		GatewayURL string `json:"gatewayUrl"`
		Token      string `json:"token"`
		Model      string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(input.BaseURL) == "" || strings.TrimSpace(input.Token) == "" {
		writeOpenAIError(w, http.StatusBadRequest, "baseUrl and token are required")
		return
	}
	res, err := clidesktop.Apply(clidesktop.Endpoint{
		BaseURL:    strings.TrimSpace(input.BaseURL),
		GatewayURL: strings.TrimSpace(input.GatewayURL),
		Token:      strings.TrimSpace(input.Token),
		Model:      strings.TrimSpace(input.Model),
	})
	payload := map[string]any{"ok": err == nil, "result": res}
	if err != nil {
		payload["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, payload)
}
