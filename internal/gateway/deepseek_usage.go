package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DeepSeek account-balance query.
//
// Like Zhipu, DeepSeek is an *api_key* provider, so the balance call
// authenticates with the provider's own key — but unlike Zhipu it DOES use the
// standard "Bearer " prefix.
//
//	GET https://api.deepseek.com/user/balance
//	Authorization: Bearer <api_key>
//	→ {"is_available":true,"balance_infos":[
//	     {"currency":"CNY","total_balance":"110.00",
//	      "granted_balance":"10.00","topped_up_balance":"100.00"}]}
//
// Amounts come back as decimal *strings*; they are kept as strings end-to-end so
// no precision is lost formatting money through a float.
const deepSeekBalancePath = "/user/balance"

// isDeepSeekBaseURL reports whether a provider points at DeepSeek. Third-party
// relays that merely serve deepseek-* models are intentionally not matched:
// they do not implement /user/balance.
func isDeepSeekBaseURL(baseURL string) bool {
	return strings.Contains(strings.ToLower(baseURL), "deepseek.com")
}

// deepSeekBalanceBase resolves the origin to query. The balance endpoint lives
// at the root, so any configured path (…/v1) is stripped; scheme+host is kept as
// given rather than hardcoded, so a self-hosted or test origin still works.
func deepSeekBalanceBase(baseURL string) string {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return "https://api.deepseek.com"
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return "https://api.deepseek.com"
	}
	scheme := parsed.Scheme
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + parsed.Host
}

// DeepSeekBalanceInfo is one currency's balance breakdown.
type DeepSeekBalanceInfo struct {
	Currency string `json:"currency"`
	// Amounts stay strings exactly as returned upstream (decimal money).
	TotalBalance    string `json:"total_balance"`
	GrantedBalance  string `json:"granted_balance,omitempty"`
	ToppedUpBalance string `json:"topped_up_balance,omitempty"`
}

// DeepSeekBalanceReport is the client-safe snapshot for a DeepSeek provider.
type DeepSeekBalanceReport struct {
	Available bool `json:"available"`
	// Unsupported means the endpoint is absent (e.g. a relay that only proxies
	// deepseek-* models). Not an auth failure — forwarding still works.
	Unsupported bool   `json:"unsupported,omitempty"`
	Error       string `json:"error,omitempty"`
	FetchedAt   string `json:"fetchedAt,omitempty"`
	// IsAvailable mirrors upstream's is_available: false means the account can no
	// longer serve API calls (out of credit), which is distinct from our own
	// Available flag meaning "we successfully read the balance".
	IsAvailable  bool                  `json:"isAvailable"`
	BalanceInfos []DeepSeekBalanceInfo `json:"balance_infos,omitempty"`
}

// fetchDeepSeekBalance queries the account balance endpoint.
func fetchDeepSeekBalance(ctx context.Context, baseURL, apiKey string) (DeepSeekBalanceReport, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return DeepSeekBalanceReport{Available: false, Error: "missing api key"}, nil
	}
	url := deepSeekBalanceBase(baseURL) + deepSeekBalancePath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return DeepSeekBalanceReport{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// Transient transport failure: let the caller keep the last good value.
		return DeepSeekBalanceReport{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return DeepSeekBalanceReport{Available: false, Error: fmt.Sprintf("Authentication failed (HTTP %d)", resp.StatusCode)}, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return DeepSeekBalanceReport{
			Available:   false,
			Unsupported: true,
			Error:       "该地址不支持余额查询（可能是第三方中转）",
			FetchedAt:   time.Now().UTC().Format(time.RFC3339),
		}, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return DeepSeekBalanceReport{}, err // transient read failure
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return DeepSeekBalanceReport{Available: false, Error: fmt.Sprintf("API error (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))}, nil
	}

	var parsed struct {
		IsAvailable  bool `json:"is_available"`
		BalanceInfos []struct {
			Currency        string `json:"currency"`
			TotalBalance    string `json:"total_balance"`
			GrantedBalance  string `json:"granted_balance"`
			ToppedUpBalance string `json:"topped_up_balance"`
		} `json:"balance_infos"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return DeepSeekBalanceReport{Available: false, Error: "Failed to parse response"}, nil
	}
	report := DeepSeekBalanceReport{
		Available:   true,
		FetchedAt:   time.Now().UTC().Format(time.RFC3339),
		IsAvailable: parsed.IsAvailable,
	}
	for _, info := range parsed.BalanceInfos {
		report.BalanceInfos = append(report.BalanceInfos, DeepSeekBalanceInfo{
			Currency:        info.Currency,
			TotalBalance:    info.TotalBalance,
			GrantedBalance:  info.GrantedBalance,
			ToppedUpBalance: info.ToppedUpBalance,
		})
	}
	return report, nil
}
