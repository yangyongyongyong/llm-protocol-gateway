package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// oauthUsageCacheKey is the cache key for a provider's usage/quota report:
// "<kind>:<providerID>". The account identity is not part of the key; the cache
// drops entries whose account no longer matches (see oauthUsageCache.bindAccount),
// so swapping the account behind a provider never serves the old account's
// report — and never feeds the old account's balance to scheduling either.
func oauthUsageCacheKey(kind, providerID string) string {
	return kind + ":" + providerID
}

// oauthUsageCacheKeyFor is oauthUsageCacheKey bound to the account provider
// holds right now: an entry fetched for the account that used to be behind the
// provider is dropped first, so no reader (console or scheduler) can be served
// it. Callers must therefore ask for the key before touching the cache.
func (s *Server) oauthUsageCacheKeyFor(kind, providerID string) string {
	key := oauthUsageCacheKey(kind, providerID)
	if s == nil || s.oauthUsageCache == nil || s.router == nil {
		return key
	}
	if provider, err := s.router.ProviderByID(providerID); err == nil {
		s.oauthUsageCache.bindAccount(key, oauthUsageAccountID(provider))
	}
	return key
}

// oauthUsageAccountID identifies the account a usage report belongs to, derived
// from the stored credential. Swapping a provider's OAuth account changes this
// id; rotating the access token of the same account does not unless the refresh
// token was reissued too. It is a fingerprint (SHA-256 prefix), never the token
// itself, and the raw token is deliberately not the identity: background token
// rotation would then evict a healthy report every fifteen minutes.
func oauthUsageAccountID(provider domain.Provider) string {
	var material string
	switch provider.AuthType {
	case domain.AuthTypeClaudeOAuth:
		if provider.ClaudeOAuth != nil {
			material = "refresh:" + strings.TrimSpace(provider.ClaudeOAuth.RefreshToken)
			if material == "refresh:" {
				material = "access:" + strings.TrimSpace(provider.ClaudeOAuth.AccessToken)
			}
		}
	case domain.AuthTypeCursorOAuth:
		if provider.CursorOAuth != nil {
			material = "refresh:" + strings.TrimSpace(provider.CursorOAuth.RefreshToken)
			if material == "refresh:" {
				material = "access:" + strings.TrimSpace(provider.CursorOAuth.AccessToken)
			}
		}
	case domain.AuthTypeChatGPTOAuth:
		if provider.ChatGPTOAuth != nil {
			// The account id is the vendor's own identity when the login
			// recorded it; the refresh token is rotating, so it only stands in
			// when there is nothing better.
			if id := strings.TrimSpace(provider.ChatGPTOAuth.ChatGPTAccountID); id != "" {
				material = "account:" + id
			} else {
				material = "refresh:" + strings.TrimSpace(provider.ChatGPTOAuth.RefreshToken)
				if material == "refresh:" {
					material = "access:" + strings.TrimSpace(provider.ChatGPTOAuth.AccessToken)
				}
			}
		}
	case domain.AuthTypeQoderPAT, domain.AuthTypeQoderCN:
		if provider.QoderPAT != nil {
			// Qoder's UID is the stable account identity. The access/device
			// tokens rotate independently of the account.
			if id := strings.TrimSpace(provider.QoderPAT.UID); id != "" {
				material = "account:" + id
			} else if email := strings.TrimSpace(provider.QoderPAT.Email); email != "" {
				material = "email:" + strings.ToLower(email)
			} else if pat := strings.TrimSpace(provider.QoderPAT.PAT); pat != "" {
				material = "pat:" + pat
			} else {
				material = "device:" + strings.TrimSpace(provider.QoderPAT.DeviceToken)
			}
		}

	case domain.AuthTypeWorkBuddy, domain.AuthTypeWorkBuddyAI:
		if provider.WorkBuddy != nil {
			if id := strings.TrimSpace(provider.WorkBuddy.UserID); id != "" {
				material = "account:" + id
			} else {
				material = "refresh:" + strings.TrimSpace(provider.WorkBuddy.RefreshToken)
				if material == "refresh:" {
					material = "access:" + strings.TrimSpace(provider.WorkBuddy.AccessToken)
				}
			}
		}
	default:
		material = "key:" + strings.TrimSpace(resolveProviderAuth(provider))
	}
	if material == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:8])
}
