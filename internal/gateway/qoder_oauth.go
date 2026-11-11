package gateway

// Qoder sign-in and token lifecycle.
//
// A Qoder account is signed in the way Qoder's own desktop client signs in: a
// PKCE device flow that opens qoder.com's (or qoder.cn's) account page, a poll
// of its openapi host until the user authorizes, then the device token is
// traded for the job token that signs the model calls. Qoder's /api/v1/userinfo
// answers who the account is, so the console gets a real name rather than a
// token tail.
//
// A personal access token is the second way in: it is exchanged for the same
// job token without a browser, which is what a headless deployment needs. It
// stays the fallback rather than the default, because Qoder records no expiry
// for it and a pasted secret is worse to hold than a session.
//
// A job token's refresh token is spent on use — asking twice with one pair
// costs the pair — so every renewal is serialized per provider and saved the
// moment it answers.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

const (
	// qoderSignInDeadline is how long a browser sign-in may take; Qoder's page
	// keeps a device flow open for about this long.
	qoderSignInDeadline = 15 * time.Minute
	// qoderDevicePollInterval is what Qoder's client waits between polls.
	qoderDevicePollInterval = 2 * time.Second
	// qoderRefreshLead renews this far ahead of the job token's end, so a
	// request never goes out with a token that expires in flight.
	qoderRefreshLead = 5 * time.Minute
	// qoderCallTimeout bounds one account-endpoint call.
	qoderCallTimeout = 20 * time.Second
	// qoderPendingFlowTTL drops an abandoned sign-in's PKCE verifier.
	qoderPendingFlowTTL = 20 * time.Minute

	qoderSignInModeDevice = "device"
	qoderSignInModePAT    = "pat"
)

// qoderDeviceToken is what a finished device flow returns. Qoder answers with
// either field name for the token and gives no lifetime at all sometimes, so
// both spellings and both lifetime shapes are read.
type qoderDeviceToken struct {
	Token        string `json:"token"`
	DeviceToken  string `json:"device_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	UserName     string `json:"user_name,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
}

func (t qoderDeviceToken) accessToken() string {
	return firstNonEmpty(strings.TrimSpace(t.Token), strings.TrimSpace(t.DeviceToken))
}

// expiry is when the device token lapses: its expires_at date, else its
// expires_in seconds, else a day — the way Qoder's CLI reads it. A device
// token used for chat that says nothing is treated as short-lived so it gets
// rotated rather than silently aging.
func (t qoderDeviceToken) expiry(now time.Time) time.Time {
	if at, err := time.Parse(time.RFC3339, strings.TrimSpace(t.ExpiresAt)); err == nil && at.After(now) {
		return at
	}
	if t.ExpiresIn > 0 {
		return now.Add(time.Duration(t.ExpiresIn) * time.Second)
	}
	return now.Add(24 * time.Hour)
}

// qoderJobToken is the task token that authorizes the model calls. Qoder's
// expires_in is in milliseconds — its own client reads it that way — which is
// three orders of magnitude from the seconds every other upstream here uses.
type qoderJobToken struct {
	Token        string `json:"token,omitempty"`
	DeviceToken  string `json:"device_token,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	JobToken     string `json:"jobToken,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	// ExpiresIn is milliseconds; the camel-case spellings are what the PAT
	// exchange has answered with in this gateway's own logs.
	ExpiresIn  int64  `json:"expires_in,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	ExpireTime string `json:"expire_time,omitempty"`
}

func (t qoderJobToken) accessToken() string {
	return firstNonEmpty(
		strings.TrimSpace(t.Token),
		strings.TrimSpace(t.JobToken),
		strings.TrimSpace(t.AccessToken),
		strings.TrimSpace(t.DeviceToken),
	)
}

func (t qoderJobToken) refreshToken() string {
	return strings.TrimSpace(t.RefreshToken)
}

// expiry is when the token runs out: an absolute date Qoder named, else its
// relative lifetime in milliseconds, else a day.
func (t qoderJobToken) expiry(now time.Time) time.Time {
	for _, value := range []string{t.ExpireTime, t.ExpiresAt} {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if at, err := time.Parse(time.RFC3339, value); err == nil && at.After(now) {
			return at
		}
		if seconds := jsonNumberSeconds(value); seconds > 0 {
			if at := time.Unix(seconds, 0); at.After(now) {
				return at
			}
		}
	}
	if t.ExpiresIn > 0 {
		return now.Add(time.Duration(t.ExpiresIn) * time.Millisecond)
	}
	return now.Add(24 * time.Hour)
}

// jsonNumberSeconds reads a lifetime field that Qoder sometimes answers as a
// bare number in a string position.
func jsonNumberSeconds(value string) int64 {
	var seconds int64
	if _, err := fmt.Sscanf(value, "%d", &seconds); err != nil {
		return 0
	}
	if seconds > 1e12 {
		seconds /= 1000
	}
	return seconds
}

// qoderUserInfo is what Qoder's account endpoint says about the signed-in
// account. It is asked with the device token, not the job token.
type qoderUserInfo struct {
	ID       string `json:"id"`
	UserID   string `json:"user_id"`
	UID      string `json:"uid"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Email    string `json:"email"`
	// The account's plan identity. Both spellings are read because the two
	// sites answer with different ones, and the COSY identity blob carries all
	// three: an enterprise account whose organization is missing there looks
	// like a personal one to the vendor, and both the campaign list and the
	// model list are filtered by what that blob claims.
	UserType          string `json:"user_type"`
	UserTypeAlt       string `json:"userType"`
	OrganizationID    string `json:"organization_id"`
	OrganizationIDAlt string `json:"organizationId"`
	OrgName           string `json:"organization_name"`
	OrgNameAlt        string `json:"organizationName"`
}

// userType is the plan kind the account reports, if it names one.
func (u qoderUserInfo) userType() string {
	return firstNonEmpty(strings.TrimSpace(u.UserType), strings.TrimSpace(u.UserTypeAlt))
}

// organizationID is the enterprise the account belongs to, if any.
func (u qoderUserInfo) organizationID() string {
	return firstNonEmpty(strings.TrimSpace(u.OrganizationID), strings.TrimSpace(u.OrganizationIDAlt))
}

// organizationName is that enterprise's display name.
func (u qoderUserInfo) organizationName() string {
	return firstNonEmpty(strings.TrimSpace(u.OrgName), strings.TrimSpace(u.OrgNameAlt))
}

// applyPlanIdentity copies the plan fields onto a credential. A site that
// answers with none (a personal account) leaves the credential as it was.
func (u qoderUserInfo) applyPlanIdentity(credential *domain.QoderCredential) {
	if credential == nil {
		return
	}
	if value := u.userType(); value != "" {
		credential.UserType = value
	}
	if value := u.organizationID(); value != "" {
		credential.OrganizationID = value
	}
	if value := u.organizationName(); value != "" {
		credential.OrganizationName = value
	}
}

func (u qoderUserInfo) accountUID() string {
	return firstNonEmpty(strings.TrimSpace(u.ID), strings.TrimSpace(u.UserID), strings.TrimSpace(u.UID))
}

func (u qoderUserInfo) displayName() string {
	return firstNonEmpty(strings.TrimSpace(u.Name), strings.TrimSpace(u.Username))
}

// qoderError is a refused Qoder call. The status is kept because the sign-in
// code has to tell "this credential is dead" (401/403) from "ask again later".
type qoderError struct {
	Message string
	Status  int
}

func (e *qoderError) Error() string {
	if e.Status == 0 {
		return e.Message
	}
	return fmt.Sprintf("%s（HTTP %d）", e.Message, e.Status)
}

// qoderSignInError means the account has to sign in again; nothing stored can
// recover it.
type qoderSignInError struct {
	Message string
	cause   error
}

func (e *qoderSignInError) Error() string { return e.Message }
func (e *qoderSignInError) Unwrap() error { return e.cause }

func qoderSignInGone(message string, cause error) error {
	return &qoderSignInError{Message: message, cause: cause}
}

func qoderErrorMeansSignIn(err error) bool {
	var gone *qoderSignInError
	return errors.As(err, &gone)
}

// qoderAccountCall runs one account-endpoint call on the site's openapi host.
// Only the bearer token is sent as a header; Qoder answers these as plain JSON.
func qoderAccountCall(ctx context.Context, method, endpoint, bearer string, body any, dst any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(raw))
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode >= 400 {
		return &qoderError{Message: qoderErrorMessage(payload, response.StatusCode), Status: response.StatusCode}
	}
	if dst == nil {
		return nil
	}
	if err := json.Unmarshal(payload, dst); err != nil {
		return fmt.Errorf("Qoder 返回了无法解析的内容（HTTP %d）", response.StatusCode)
	}
	return nil
}

// qoderErrorMessage is the shortest truthful line a refused call can produce.
// It never carries the request's token, which is what the caller just sent.
func qoderErrorMessage(payload []byte, status int) string {
	var answer struct {
		Message string          `json:"message"`
		Error   string          `json:"error"`
		Detail  json.RawMessage `json:"detail"`
	}
	text := strings.TrimSpace(string(payload))
	if json.Unmarshal(payload, &answer) == nil {
		if message := firstNonEmpty(strings.TrimSpace(answer.Message), strings.TrimSpace(answer.Error)); message != "" {
			return message
		}
	}
	if text == "" {
		return fmt.Sprintf("HTTP %d", status)
	}
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return text
}

// qoderPendingSignIn is an in-flight browser sign-in: the PKCE pair only the
// poll needs, and the machine id that goes into the account from here on.
type qoderPendingSignIn struct {
	AuthType  string
	Verifier  string
	Nonce     string
	MachineID string
	FlowID    string
	CreatedAt time.Time
}

type qoderSignInStore struct {
	mu       sync.Mutex
	pending  map[string]qoderPendingSignIn
	statuses map[string]qoderFlowStatus
}

type qoderFlowStatus struct {
	Status    string    `json:"status"`
	Message   string    `json:"message,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func newQoderSignInStore() *qoderSignInStore {
	return &qoderSignInStore{
		pending:  map[string]qoderPendingSignIn{},
		statuses: map[string]qoderFlowStatus{},
	}
}

func (store *qoderSignInStore) put(providerID string, entry qoderPendingSignIn) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.pending[providerID] = entry
}

func (store *qoderSignInStore) take(providerID string) (qoderPendingSignIn, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.pending[providerID]
	if !ok {
		return qoderPendingSignIn{}, false
	}
	delete(store.pending, providerID)
	if time.Since(entry.CreatedAt) > qoderPendingFlowTTL {
		return qoderPendingSignIn{}, false
	}
	return entry, true
}

func (store *qoderSignInStore) finish(providerID string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.pending, providerID)
}

func (store *qoderSignInStore) setStatus(flowID, status, message string) {
	if strings.TrimSpace(flowID) == "" {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.statuses[flowID] = qoderFlowStatus{Status: status, Message: message, UpdatedAt: time.Now()}
}

func (store *qoderSignInStore) status(flowID string) (qoderFlowStatus, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.statuses[flowID]
	return entry, ok
}

// qoderAuthorizationURL is the device-flow page for a site. The challenge is
// the S256 digest of the verifier the poll later sends, so the page never sees
// the secret; the machine id is minted here and kept with the account.
func qoderAuthorizationURL(site qoderSite, verifier, nonce, machineID string) (string, error) {
	if strings.TrimSpace(site.Web) == "" {
		return "", fmt.Errorf("未知的 Qoder 站点")
	}
	sum := sha256.Sum256([]byte(verifier))
	query := url.Values{}
	query.Set("challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
	query.Set("challenge_method", "S256")
	query.Set("nonce", nonce)
	query.Set("machine_id", machineID)
	query.Set("client_id", site.ClientID)
	if site.RedirectURI != "" {
		query.Set("redirect_uri", site.RedirectURI)
	}
	return site.Web + qoderDeviceSelectPath + "?" + query.Encode(), nil
}

// startQoderOAuth opens the browser sign-in for a provider and waits for it in
// the background, so the console only has to poll one status endpoint.
func (s *Server) startQoderOAuth(providerID string, site qoderSite) (authURL, flowID string, err error) {
	verifier := randomHex(32)
	machineID := newUUIDV4()
	nonce := newUUIDV4()
	flowID = randomHex(16)
	if verifier == "" || machineID == "" || nonce == "" || flowID == "" {
		return "", "", errors.New("无法生成登录所需的随机数")
	}
	authURL, err = qoderAuthorizationURL(site, verifier, nonce, machineID)
	if err != nil {
		return "", "", err
	}
	s.qoderSignIns.put(providerID, qoderPendingSignIn{
		AuthType:  site.AuthType,
		Verifier:  verifier,
		Nonce:     nonce,
		MachineID: machineID,
		FlowID:    flowID,
		CreatedAt: time.Now(),
	})
	s.qoderSignIns.setStatus(flowID, "pending", "等待浏览器授权…")
	ctx, cancel := context.WithTimeout(context.Background(), qoderSignInDeadline)
	go s.pollQoderOAuthInBackground(ctx, cancel, providerID, site, verifier, nonce, machineID, flowID)
	return authURL, flowID, nil
}

// pollQoderOAuthInBackground waits for the authorization, finishes the sign-in
// and stores the account. A poll that fails to reach Qoder is retried: the
// user may still be typing their password while the network hiccuped.
func (s *Server) pollQoderOAuthInBackground(
	ctx context.Context,
	cancel context.CancelFunc,
	providerID string,
	site qoderSite,
	verifier, nonce, machineID, flowID string,
) {
	defer cancel()
	fail := func(message string) {
		s.qoderSignIns.setStatus(flowID, "failed", message)
		s.qoderSignIns.finish(providerID)
		s.logs.AddApp("warn", "qoder oauth failed: "+message, providerID)
	}
	s.qoderSignIns.setStatus(flowID, "pending", "等待浏览器授权…")
	deviceToken, err := qoderPollDeviceToken(ctx, site, nonce, verifier)
	if err != nil {
		if ctx.Err() != nil {
			fail(site.Name + " 登录超时，请重新发起")
			return
		}
		fail(site.Name + " 登录失败：" + err.Error())
		return
	}
	credential, err := qoderCompleteSignIn(ctx, site, *deviceToken, machineID)
	if err != nil {
		fail(site.Name + " 登录失败：" + err.Error())
		return
	}
	if err := ctx.Err(); err != nil {
		fail(site.Name + " 登录超时，请重新发起")
		return
	}
	if _, err := s.router.SetProviderQoderPAT(providerID, credential); err != nil {
		fail("保存登录结果失败：" + err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		fail("保存登录结果失败：" + err.Error())
		return
	}
	// The account's own model list is what a request has to carry, so reading
	// it is part of signing in, not a later nicety. A failure here only costs
	// the first request a re-read.
	if _, syncErr := s.syncQoderProviderModels(providerID); syncErr != nil {
		s.logs.AddApp("warn", "qoder model sync failed: "+syncErr.Error(), providerID)
	}
	s.qoderSignIns.finish(providerID)
	s.qoderSignIns.setStatus(flowID, "done", credential.AccountLabel)
	s.logs.AddApp("info", "qoder signed in", fmt.Sprintf("provider=%s account=%s", providerID, credential.AccountLabel))
}

// qoderPollDeviceToken asks for the device token until the user authorizes it,
// ctx ends, or Qoder says the flow is gone.
func qoderPollDeviceToken(ctx context.Context, site qoderSite, nonce, verifier string) (*qoderDeviceToken, error) {
	endpoint := site.OpenAPI + qoderDevicePollPath
	query := url.Values{}
	query.Set("nonce", nonce)
	query.Set("verifier", verifier)
	query.Set("challenge_method", "S256")
	endpoint += "?" + query.Encode()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return nil, err
		}
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		if response.StatusCode == http.StatusOK {
			var token qoderDeviceToken
			if json.Unmarshal(payload, &token) == nil && token.accessToken() != "" {
				return &token, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(qoderDevicePollInterval):
		}
	}
}

// qoderCompleteSignIn turns an authorized device token into an account: the
// job token that signs inference, and who the account is.
func qoderCompleteSignIn(ctx context.Context, site qoderSite, deviceToken qoderDeviceToken, machineID string) (domain.QoderCredential, error) {
	credential := domain.QoderCredential{
		SignInMode:    qoderSignInModeDevice,
		UID:           strings.TrimSpace(deviceToken.UserID),
		MachineID:     machineID,
		DeviceToken:   deviceToken.accessToken(),
		DeviceRefresh: strings.TrimSpace(deviceToken.RefreshToken),
	}
	var job qoderJobToken
	err := qoderAccountCall(ctx, http.MethodPost, site.OpenAPI+qoderJobTokenPath, deviceToken.accessToken(),
		map[string]string{"clientId": site.ClientID}, &job)
	switch {
	case err == nil && job.accessToken() != "":
		credential.AccessToken = job.accessToken()
		credential.RefreshToken = job.refreshToken()
		credential.ExpiresAt = qoderFormatExpiry(job.expiry(time.Now()))
	case site.DeviceChatFallback && qoderErrorIsClient(err):
		// Qoder CN's CLI never makes a job token: its device token is what
		// signs the model calls. An account qoder.cn won't trade is served the
		// CLI's way rather than failed.
		credential.AccessToken = deviceToken.accessToken()
		credential.RefreshToken = strings.TrimSpace(deviceToken.RefreshToken)
		credential.DeviceChat = true
		credential.ExpiresAt = qoderFormatExpiry(deviceToken.expiry(time.Now()))
	case err != nil:
		return domain.QoderCredential{}, err
	default:
		return domain.QoderCredential{}, fmt.Errorf("%s 没有返回任务令牌", site.Name)
	}
	// The account endpoint answers with the email and name; it is asked with
	// the device token, which outlasts the sign-in that just made it. A refusal
	// here costs only the label, so it never costs the sign-in.
	if info, infoErr := qoderFetchUserInfo(ctx, site, deviceToken.accessToken()); infoErr == nil {
		credential.Email = strings.TrimSpace(info.Email)
		credential.Name = info.displayName()
		if credential.UID == "" {
			credential.UID = info.accountUID()
		}
		// The plan identity goes into the COSY blob; without it an enterprise
		// account is answered as a personal one.
		info.applyPlanIdentity(&credential)
	}
	if credential.Name == "" {
		credential.Name = strings.TrimSpace(deviceToken.UserName)
	}
	if strings.TrimSpace(credential.UID) == "" {
		return domain.QoderCredential{}, fmt.Errorf("%s 登录没有给出账号标识，请重新登录", site.Name)
	}
	if strings.TrimSpace(credential.AccessToken) == "" {
		return domain.QoderCredential{}, fmt.Errorf("%s 登录没有拿到可用令牌", site.Name)
	}
	credential.AccountLabel = qoderAccountLabelFor(site, &credential)
	return credential, nil
}

// qoderErrorIsClient reports a refusal of the request (4xx) as against a
// transient or server-side one.
func qoderErrorIsClient(err error) bool {
	var refusal *qoderError
	return errors.As(err, &refusal) && refusal.Status >= 400 && refusal.Status < 500
}

// qoderFetchUserInfo asks the site's account endpoint. Qoder exposes it, which
// is why the console can name an account instead of showing a token tail.
func qoderFetchUserInfo(ctx context.Context, site qoderSite, bearer string) (qoderUserInfo, error) {
	if strings.TrimSpace(bearer) == "" {
		return qoderUserInfo{}, errors.New("Qoder 账号查询缺少令牌")
	}
	var info qoderUserInfo
	err := qoderAccountCall(ctx, http.MethodGet, site.OpenAPI+qoderUserInfoPath, bearer, nil, &info)
	return info, err
}

func qoderFormatExpiry(expiresAt time.Time) string {
	return expiresAt.UTC().Format(time.RFC3339)
}

// qoderPersonalToken is the pasted token a "pat" account re-exchanges with.
// Rows written before the browser flow existed kept it in RefreshToken.
func qoderPersonalToken(credential *domain.QoderCredential) string {
	if credential == nil {
		return ""
	}
	if pat := strings.TrimSpace(credential.PAT); pat != "" {
		return pat
	}
	if credential.SignInMode == "" || credential.SignInMode == qoderSignInModePAT {
		if stored := strings.TrimSpace(credential.RefreshToken); strings.HasPrefix(stored, "pt-") {
			return stored
		}
	}
	return ""
}

// qoderTokenNeedsRefresh reports whether the account's chat token has to be
// renewed before it goes out. A missing or unparseable expiry renews: asking
// with a dead token costs a failed request, and not asking costs one too.
func qoderTokenNeedsRefresh(credential *domain.QoderCredential) bool {
	if credential == nil || strings.TrimSpace(credential.AccessToken) == "" {
		return true
	}
	if strings.TrimSpace(credential.ExpiresAt) == "" {
		return true
	}
	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(credential.ExpiresAt))
	if err != nil {
		return true
	}
	return time.Now().Add(qoderRefreshLead).After(expiresAt)
}

// qoderRenewMu serializes renewals per provider, because a Qoder refresh token
// is spent on use: two requests that both find it near its end must not both
// send it.
var qoderRenewMu sync.Map

func qoderRenewLock(providerID string) *sync.Mutex {
	lock, _ := qoderRenewMu.LoadOrStore(providerID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// ensureFreshQoderToken guarantees the provider holds a usable token. It runs
// on every proxied request, so the unexpired path performs no network I/O.
func (s *Server) ensureFreshQoderToken(provider domain.Provider) (domain.Provider, error) {
	if !isQoderProvider(provider) {
		return provider, nil
	}
	if provider.QoderPAT == nil || provider.QoderPAT.Disconnected {
		return provider, errors.New("Qoder 尚未登录，请先在控制台完成登录")
	}
	if !qoderTokenNeedsRefresh(provider.QoderPAT) {
		return provider, nil
	}
	site := qoderSiteOf(provider)
	mutex := qoderRenewLock(provider.ID)
	mutex.Lock()
	defer mutex.Unlock()
	// Under the lock, trust the router's copy: a request that renewed the pair
	// while this one waited must not spend the token it just replaced.
	current, err := s.router.ProviderByID(provider.ID)
	if err == nil && current.QoderPAT != nil && !current.QoderPAT.Disconnected && !qoderTokenNeedsRefresh(current.QoderPAT) {
		return current, nil
	}
	if err == nil && current.QoderPAT != nil {
		provider = current
	}
	credential := *provider.QoderPAT
	ctx, cancel := context.WithTimeout(context.Background(), qoderCallTimeout)
	defer cancel()
	renewed, err := renewQoderCredential(ctx, site, credential)
	if err != nil {
		// Recorded before the failure is swallowed below, so a silent renewal
		// failure still reaches the monitoring rules.
		s.recordRenewal(provider, "qoder", time.Now(), err)
		if qoderErrorMeansSignIn(err) {
			return provider, err
		}
		// A renewal that only failed now is not a reason to stop serving: the
		// stored token may still work, and the failover path handles a 401.
		s.logs.AddApp("warn", "qoder renewal failed: "+err.Error(), provider.ID)
		return provider, nil
	}
	updated, err := s.router.SetProviderQoderPAT(provider.ID, renewed)
	if err != nil {
		return provider, err
	}
	s.recordRenewal(updated, "qoder", time.Now(), nil)
	if err := s.persistProviderOAuth(updated); err != nil {
		return updated, err
	}
	return updated, nil
}

// renewQoderCredential spends the account's refresh token for a new pair, or
// re-exchanges the personal access token when the pair is gone. A device-chat
// account (Qoder CN's CLI shape) renews its device token instead.
func renewQoderCredential(ctx context.Context, site qoderSite, credential domain.QoderCredential) (domain.QoderCredential, error) {
	if credential.DeviceChat {
		refresh := strings.TrimSpace(credential.RefreshToken)
		if refresh == "" {
			refresh = strings.TrimSpace(credential.DeviceRefresh)
		}
		if refresh == "" {
			return credential, qoderSignInGone(site.Name+" 登录已失效，请重新登录", nil)
		}
		var token qoderDeviceToken
		if err := qoderAccountCall(ctx, http.MethodPost, site.OpenAPI+qoderDeviceRefreshPath, "",
			map[string]string{"refresh_token": refresh}, &token); err != nil {
			if qoderErrorIsClient(err) {
				return credential, qoderSignInGone(site.Name+" 登录已失效，请重新登录", err)
			}
			return credential, err
		}
		access := token.accessToken()
		if access == "" || strings.TrimSpace(token.RefreshToken) == "" {
			return credential, errors.New("Qoder 返回的设备令牌对不完整")
		}
		renewed := credential
		renewed.AccessToken = access
		renewed.RefreshToken = strings.TrimSpace(token.RefreshToken)
		renewed.DeviceToken = access
		renewed.DeviceRefresh = strings.TrimSpace(token.RefreshToken)
		renewed.ExpiresAt = qoderFormatExpiry(token.expiry(time.Now()))
		return renewed, nil
	}
	if refresh := strings.TrimSpace(credential.RefreshToken); refresh != "" {
		var job qoderJobToken
		err := qoderAccountCall(ctx, http.MethodPost, site.OpenAPI+qoderJobRefreshPath, "",
			map[string]string{"refresh_token": refresh}, &job)
		if err == nil {
			access, next := job.accessToken(), job.refreshToken()
			if access == "" || next == "" {
				return credential, errors.New("Qoder 返回的任务令牌对不完整")
			}
			renewed := credential
			renewed.AccessToken = access
			renewed.RefreshToken = next
			renewed.ExpiresAt = qoderFormatExpiry(job.expiry(time.Now()))
			renewed.AccountLabel = qoderAccountLabelFor(site, &renewed)
			return renewed, nil
		}
		if !qoderErrorIsClient(err) {
			return credential, err
		}
		// Qoder refused the refresh token: the sign-in is over, unless this
		// account still holds a personal access token that can mint a new one.
		if pat := qoderPersonalToken(&credential); pat != "" {
			return exchangeQoderJobToken(ctx, site, credential, pat)
		}
		return credential, qoderSignInGone(site.Name+" 登录已失效，请重新登录", err)
	}
	if pat := qoderPersonalToken(&credential); pat != "" {
		return exchangeQoderJobToken(ctx, site, credential, pat)
	}
	return credential, qoderSignInGone(site.Name+" 登录没有可用凭证，请重新登录", nil)
}
