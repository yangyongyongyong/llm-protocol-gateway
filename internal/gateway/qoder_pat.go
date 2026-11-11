package gateway

// Qoder's personal-access-token path: the second way into the same account,
// kept because a headless deployment cannot open a browser. A pasted token is
// exchanged for the job token that signs inference calls; Qoder never rotates
// the token itself, so it is carried forward on every exchange and is what
// mints a new pair when the refresh tokens lapse.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

const (
	// qoderTokenFallbackTTL is the floor used when Qoder names no lifetime at
	// all. The observed real lifetime is ~24h, so a short floor only costs an
	// extra exchange, never a stale token.
	qoderTokenFallbackTTL = time.Hour
	// qoderTokenExpirySkew is how early a token is treated as spent.
	qoderTokenExpirySkew = 5 * time.Minute
)

// isPlaceholderBaseURL reports whether a base URL carries no real routing
// information: either empty, or still the create form's example.com prefill.
func isPlaceholderBaseURL(baseURL string) bool {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return true
	}
	return strings.Contains(strings.ToLower(baseURL), "example.com")
}

// qoderAccountLabelFor is the console-facing name of a connection: the account
// Qoder's own endpoint reported, falling back to its uid, and only then to the
// pasted token's last four — enough to tell two accounts apart when the account
// endpoint refused to say who it was.
func qoderAccountLabelFor(site qoderSite, credential *domain.QoderCredential) string {
	if credential == nil {
		return site.Name
	}
	if name := firstNonEmpty(credential.Email, credential.Name, credential.UID); name != "" {
		return name
	}
	if pat := qoderPersonalToken(credential); pat != "" && len(pat) >= 4 {
		return site.Name + " · ****" + pat[len(pat)-4:]
	}
	return site.Name
}

// exchangeQoderJobToken trades a personal access token for a job token and
// reads who the account is, so a token-connected account carries the same
// identity fields as a browser-signed-in one.
func exchangeQoderJobToken(ctx context.Context, site qoderSite, base domain.QoderCredential, pat string) (domain.QoderCredential, error) {
	pat = strings.TrimSpace(pat)
	if pat == "" {
		return base, fmt.Errorf("Qoder 个人访问令牌为空")
	}
	// The upstream takes the token in the body as personal_token; an empty body
	// is rejected with "personal_token is required".
	payload, err := json.Marshal(map[string]string{"personal_token": pat})
	if err != nil {
		return base, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, site.OpenAPI+qoderJobExchangePath, strings.NewReader(string(payload)))
	if err != nil {
		return base, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return base, err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Never interpolate the token here: exchange errors flow into request logs.
		return base, &qoderError{Message: qoderErrorMessage(body, response.StatusCode), Status: response.StatusCode}
	}
	var job qoderJobToken
	if err := json.Unmarshal(body, &job); err != nil {
		return base, fmt.Errorf("解析 Qoder 任务令牌失败: %w", err)
	}
	access := job.accessToken()
	if access == "" {
		return base, fmt.Errorf("Qoder 的回应里没有任务令牌")
	}
	credential := base
	credential.SignInMode = qoderSignInModePAT
	credential.PAT = pat
	credential.AccessToken = access
	credential.RefreshToken = job.refreshToken()
	credential.DeviceChat = false
	credential.ExpiresAt = qoderJobExpiry(job, time.Now())
	if credential.MachineID == "" {
		credential.MachineID = newUUIDV4()
	}
	// The account endpoint answers with the job token too, which is how a pasted
	// token gets a name and a uid — the uid is what the COSY envelope signs
	// with, so an account that cannot be named cannot be asked.
	if info, infoErr := qoderFetchUserInfo(ctx, site, access); infoErr == nil {
		credential.Email = strings.TrimSpace(info.Email)
		if credential.Name == "" {
			credential.Name = info.displayName()
		}
		if credential.UID == "" {
			credential.UID = info.accountUID()
		}
		// The plan identity goes into the COSY blob; without it an enterprise
		// account is answered as a personal one.
		info.applyPlanIdentity(&credential)
	}
	if strings.TrimSpace(credential.UID) == "" {
		return base, fmt.Errorf("%s 无法确认这个令牌属于哪个账号，请重新登录", site.Name)
	}
	credential.AccountLabel = qoderAccountLabelFor(site, &credential)
	credential.Connected = true
	return credential, nil
}

// qoderJobExpiry is a job token's end as an RFC3339 string: Qoder's own
// lifetime if it named one, else the fallback floor, minus the skew so a
// renewal always happens ahead of the real expiry.
func qoderJobExpiry(job qoderJobToken, now time.Time) string {
	expiresAt := job.expiry(now)
	if job.ExpiresIn <= 0 && strings.TrimSpace(job.ExpireTime) == "" && strings.TrimSpace(job.ExpiresAt) == "" {
		expiresAt = now.Add(qoderTokenFallbackTTL)
	}
	if !expiresAt.After(now) {
		expiresAt = now.Add(qoderTokenFallbackTTL)
	}
	return expiresAt.Add(-qoderTokenExpirySkew).UTC().Format(time.RFC3339)
}

// BackfillQoderAccountLabels refreshes account labels for already connected
// Qoder providers (one-shot at startup). Labels are derived from what is
// stored, so this needs no network call; it runs because a label is only
// written when a credential is made or renewed, and an idle connection would
// otherwise keep showing what an older version derived from it.
func (s *Server) BackfillQoderAccountLabels() {
	for _, provider := range s.router.State().Providers {
		if provider.Deleted || !isQoderProvider(provider) || provider.QoderPAT == nil {
			continue
		}
		want := qoderAccountLabelFor(qoderSiteOf(provider), provider.QoderPAT)
		if provider.QoderPAT.AccountLabel == want {
			continue
		}
		credential := *provider.QoderPAT
		credential.AccountLabel = want
		updated, err := s.router.SetProviderQoderPAT(provider.ID, credential)
		if err != nil {
			continue
		}
		if err := s.persistProviderOAuth(updated); err != nil {
			s.logs.AddApp("warn", "failed to persist qoder account label", err.Error())
			continue
		}
		s.logs.AddApp("info", "qoder account label backfilled",
			fmt.Sprintf("provider=%s label=%s", provider.ID, want))
	}
}

// qoderLiteralPAT returns a personal access token pasted as the provider's
// literal API key, if any. A plain source or "literal:"-prefixed source counts
// as literal; env and keychain sources are dynamic and keep using the stored
// credential.
func qoderLiteralPAT(provider domain.Provider) string {
	source := strings.TrimSpace(provider.APIKeySource)
	if source == "" {
		return ""
	}
	for _, prefix := range []string{"env:", "literal:", "keychain:"} {
		if !strings.HasPrefix(source, prefix) {
			continue
		}
		if prefix == "literal:" {
			return strings.TrimSpace(strings.TrimPrefix(source, "literal:"))
		}
		return ""
	}
	if strings.HasPrefix(source, "pt-") {
		return source
	}
	return ""
}
