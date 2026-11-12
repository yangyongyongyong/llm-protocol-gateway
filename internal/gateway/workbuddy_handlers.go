package gateway

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// handleWorkBuddyOAuthStart begins WorkBuddy's external-link sign-in for a
// provider: it returns the page the user signs in on and polls the plan in the
// background until the account's tokens are ready.
func (s *Server) handleWorkBuddyOAuthStart(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderOwnerForUser(w, r, providerID) {
		return
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	// The site is fixed by the provider's auth type: WorkBuddy and WorkBuddy AI
	// are separate connection kinds, so a signed-in provider never has to say
	// which build it belongs to.
	site, ok := workBuddySiteForAuthType(provider.AuthType)
	if !ok {
		writeOpenAIError(w, http.StatusBadRequest, "该 Provider 不是 WorkBuddy 连接方式")
		return
	}
	authURL, flowID, startErr := s.startWorkBuddyOAuth(providerID, site)
	if startErr != nil {
		writeOpenAIError(w, http.StatusBadGateway, startErr.Error())
		return
	}
	s.logs.AddApp("info", "workbuddy oauth flow started", providerID)
	writeJSON(w, http.StatusOK, map[string]any{
		"authUrl": authURL,
		"flowId":  flowID,
		"mode":    "poll",
	})
}

func (s *Server) handleWorkBuddyOAuthStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireProviderOwnerForUser(w, r, r.PathValue("id")) {
		return
	}
	flowID := strings.TrimSpace(r.URL.Query().Get("flowId"))
	if flowID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "flowId is required")
		return
	}
	status, ok := s.subscriptionOAuthFlows.status(flowID)
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "unknown or expired oauth flow")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// handleWorkBuddyOAuthDisconnect discards the stored session; the next request
// has to sign in through the browser again.
func (s *Server) handleWorkBuddyOAuthDisconnect(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderOwnerForUser(w, r, providerID) {
		return
	}
	updated, err := s.router.ClearProviderWorkBuddy(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.subscriptionOAuthFlows.finish(providerID)
	s.logs.AddApp("info", "workbuddy oauth disconnected", providerID)
	writeJSON(w, http.StatusOK, redactProviderForClient(updated))
}

// handleWorkBuddyUsage reports the account's credit allowance. Tokens never
// leave the server; only the plan name and the used/limit numbers do.
func (s *Server) handleWorkBuddyUsage(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderAccessForUser(w, r, providerID) {
		return
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if !isWorkBuddyProvider(provider) || provider.WorkBuddy == nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": "WorkBuddy 尚未登录", "windows": []any{}})
		return
	}
	refreshed, err := s.ensureFreshWorkBuddyToken(provider)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error(), "windows": []any{}})
		return
	}
	if refreshed.WorkBuddy == nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": "WorkBuddy 尚未登录", "windows": []any{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	quota := s.workBuddyQuota(ctx, refreshed)
	view := s.checkinViewFor(refreshed)
	quota.Checkin = &view
	if quota.Error == "" {
		cacheKey := s.oauthUsageCacheKeyFor("workbuddy", providerID)
		s.oauthUsageCache.setForAccount(cacheKey, oauthUsageAccountID(refreshed), quota)
	}
	writeJSON(w, http.StatusOK, quota)
}
