package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (s *Server) handleChatGPTOAuthStart(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if _, err := s.router.ProviderByID(providerID); err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}

	verifier, challenge, err := generateChatGPTPKCE()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to generate pkce: "+err.Error())
		return
	}
	state, err := generateChatGPTOAuthState()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to generate oauth state: "+err.Error())
		return
	}
	flowID, err := generateChatGPTOAuthState()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to generate flow id: "+err.Error())
		return
	}

	pending := chatgptOAuthPending{
		Verifier:  verifier,
		State:     state,
		FlowID:    flowID,
		CreatedAt: time.Now(),
	}
	s.attachChatGPTOAuthLocalListener(&pending)
	s.pendingChatGPTOAuth.put(providerID, pending)
	s.pendingChatGPTOAuth.setStatus(flowID, "pending", "等待浏览器授权…")

	authURL := buildChatGPTAuthorizeURL(challenge, state)
	s.logs.AddApp("info", "chatgpt oauth flow started", providerID)
	writeJSON(w, http.StatusOK, map[string]any{
		"authUrl": authURL,
		"state":   state,
		"flowId":  flowID,
		"mode":    "localhost",
	})
}

func (s *Server) handleChatGPTOAuthStatus(w http.ResponseWriter, r *http.Request) {
	flowID := strings.TrimSpace(r.URL.Query().Get("flowId"))
	if flowID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "flowId is required")
		return
	}
	status, ok := s.pendingChatGPTOAuth.status(flowID)
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "unknown or expired oauth flow")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleChatGPTOAuthCallback(w http.ResponseWriter, r *http.Request) {
	callbackState := strings.TrimSpace(r.URL.Query().Get("state"))
	if oauthError := strings.TrimSpace(r.URL.Query().Get("error")); oauthError != "" {
		desc := strings.TrimSpace(r.URL.Query().Get("error_description"))
		message := oauthError
		if desc != "" {
			message += ": " + desc
		}
		s.pendingChatGPTOAuth.setStatusByState(callbackState, "error", message)
		http.Error(w, message, http.StatusBadRequest)
		return
	}

	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	if callbackState == "" {
		http.Error(w, "missing state", http.StatusBadRequest)
		return
	}

	providerID, pending, ok := s.pendingChatGPTOAuth.getByState(callbackState)
	if !ok {
		http.Error(w, "oauth flow expired or unknown state; start again from Protocol Gateway", http.StatusBadRequest)
		return
	}
	if err := s.finishChatGPTOAuthExchange(providerID, pending.FlowID, pending, code); err != nil {
		s.logs.AddApp("error", "chatgpt oauth callback exchange failed", err.Error())
		http.Error(w, "failed to exchange oauth code", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte("<!doctype html><html><body style='font-family:sans-serif;padding:32px'><h2>ChatGPT 账号已连接</h2><p>可以关闭此页面并返回 Protocol Gateway。</p></body></html>"))
}

func (s *Server) handleChatGPTOAuthComplete(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if _, err := s.router.ProviderByID(providerID); err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	var payload struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	code, state := parseChatGPTOAuthInput(payload.Code, payload.State)
	if code == "" {
		writeOpenAIError(w, http.StatusBadRequest, "code is required")
		return
	}

	pending, ok := s.pendingChatGPTOAuth.take(providerID)
	if !ok {
		writeOpenAIError(w, http.StatusBadRequest, "no pending chatgpt oauth flow for this provider (it may have expired); start again")
		return
	}
	if state == "" {
		state = pending.State
	}
	if state != pending.State {
		writeOpenAIError(w, http.StatusBadRequest, "oauth state mismatch; start the flow again")
		return
	}
	if err := s.finishChatGPTOAuthExchange(providerID, "", pending, code); err != nil {
		s.logs.AddApp("error", "chatgpt oauth code exchange failed", err.Error())
		writeOpenAIError(w, http.StatusBadGateway, "failed to exchange chatgpt oauth code: "+err.Error())
		return
	}

	updated, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, redactProviderForClient(updated))
}

func (s *Server) handleChatGPTOAuthDisconnect(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	updated, err := s.router.ClearProviderChatGPTOAuth(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "chatgpt oauth disconnected", providerID)
	writeJSON(w, http.StatusOK, redactProviderForClient(updated))
}

func (s *Server) attachChatGPTOAuthLocalListener(pending *chatgptOAuthPending) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", s.handleChatGPTOAuthCallback)
	server := &http.Server{Addr: "127.0.0.1:1455", Handler: mux}
	flowID := pending.FlowID
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.pendingChatGPTOAuth.setStatus(flowID, "error", fmt.Sprintf("无法在 localhost:1455 监听（%v）。请用手动粘贴 code 完成连接。", err))
		}
	}()
	pending.shutdown = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}
}

func parseChatGPTOAuthInput(rawCode, rawState string) (code string, state string) {
	raw := strings.TrimSpace(rawCode)
	if raw == "" {
		return "", strings.TrimSpace(rawState)
	}
	if strings.Contains(raw, "://") {
		if parsed, err := url.Parse(raw); err == nil {
			q := parsed.Query()
			if c := strings.TrimSpace(q.Get("code")); c != "" {
				code = c
			}
			if st := strings.TrimSpace(q.Get("state")); st != "" {
				state = st
			}
		}
	}
	if code == "" {
		code = raw
	}
	if state == "" {
		state = strings.TrimSpace(rawState)
	}
	return code, state
}
