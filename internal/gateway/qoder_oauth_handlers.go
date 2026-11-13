package gateway

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// handleQoderOAuthStart opens Qoder's browser sign-in for the site this provider
// was created as, and waits for the authorization in the background — the same
// shape the console already knows from ZCode and WorkBuddy: a link to open, then
// one status endpoint to poll.
func (s *Server) handleQoderOAuthStart(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderOwnerForUser(w, r, providerID) {
		return
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	site, ok := qoderSiteForAuthType(provider.AuthType)
	if !ok {
		writeOpenAIError(w, http.StatusBadRequest, "该 Provider 不是 Qoder 连接方式")
		return
	}
	authURL, flowID, startErr := s.startQoderOAuth(providerID, site)
	if startErr != nil {
		writeOpenAIError(w, http.StatusBadGateway, startErr.Error())
		return
	}
	s.logs.AddApp("info", "qoder oauth flow started", providerID)
	writeJSON(w, http.StatusOK, map[string]any{
		"authUrl": authURL,
		"flowId":  flowID,
		"mode":    "poll",
		"site":    site.Name,
	})
}

// handleQoderOAuthStatus reports one in-flight browser sign-in. It never carries
// token material: the console sees waiting / signed in / failed, and the account
// name once it is signed in.
func (s *Server) handleQoderOAuthStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireProviderOwnerForUser(w, r, r.PathValue("id")) {
		return
	}
	flowID := strings.TrimSpace(r.URL.Query().Get("flowId"))
	if flowID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "flowId is required")
		return
	}
	status, ok := s.qoderSignIns.status(flowID)
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "unknown or expired oauth flow")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// handleQoderUsage reports the plan and the credit pools behind the account.
// Only the plan name and the used/limit numbers leave the server; every token
// stays here.
func (s *Server) handleQoderUsage(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderAccessForUser(w, r, providerID) {
		return
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if !isQoderProvider(provider) || provider.QoderPAT == nil || provider.QoderPAT.Disconnected {
		writeJSON(w, http.StatusOK, map[string]any{"error": "Qoder 尚未登录", "windows": []any{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	cacheKey := s.oauthUsageCacheKeyFor("qoder", providerID)
	forceRefresh := strings.EqualFold(r.URL.Query().Get("refresh"), "1") || strings.EqualFold(r.URL.Query().Get("refresh"), "true")
	if forceRefresh {
		s.oauthUsageCache.invalidate(cacheKey)
	} else if cached, ok := s.oauthUsageCache.getAllowStale(cacheKey); ok {
		if quota, ok := cached.(qoderQuota); ok {
			writeJSON(w, http.StatusOK, quota)
			return
		}
	}
	unlock := s.lockOAuthUsageFetch(cacheKey)
	defer unlock()
	if !forceRefresh {
		if cached, ok := s.oauthUsageCache.get(cacheKey); ok {
			if quota, ok := cached.(qoderQuota); ok {
				writeJSON(w, http.StatusOK, quota)
				return
			}
		}
	}
	updated, quota := s.qoderQuotaFor(ctx, provider)
	if quota.Error == "" {
		s.oauthUsageCache.setForAccount(cacheKey, oauthUsageAccountID(updated), quota)
	}
	view := s.checkinViewFor(provider)
	quota.Checkin = &view
	writeJSON(w, http.StatusOK, quota)
}
