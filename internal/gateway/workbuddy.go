package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// WorkBuddy is Tencent's CodeBuddy subscription, which its WorkBuddy desktop
// app signs in to; WorkBuddy AI is the same plan's international build. Both
// serve it on an OpenAI-compatible endpoint, authenticated by the account's
// own JWT access token plus the account id and domain in headers.
//
// Sign-in is WorkBuddy's external-link flow: ask the plan for a state and a
// page, have the user sign in there, then poll for the token and the account.
// A near-expired access token is traded for a fresh one with the refresh
// token, as the desktop app does.
//
// Two upstream quirks are enforced here rather than left to the client, both
// from WorkBuddy's own refusals: a request must stream ("Non-stream chat
// request is currently not supported"), and it reads a request whose opening
// message isn't the system prompt, whose role is OpenAI's "developer", or whose
// system prompt carries another agent's own words, as an unapproved channel
// (code 11128).

const (
	// Desktop client versions aligned with active workbuddy2api-panel references
	// (CN install-manifest / intl 5.9.4 + CLI product.json genieVersion).
	workBuddyClientVersionCN        = "5.9.3"
	workBuddyClientVersionIntl      = "5.9.4"
	workBuddyCLIVersion             = "2.165.0"
	workBuddyDefaultConcurrencyCN   = 3
	workBuddyDefaultConcurrencyIntl = 2

	// workBuddyDefaultQueueWaitSeconds is how long a request over the account's
	// in-flight cap waits for a slot. Waiting is what keeps a burst on one
	// account from being spread over the others by the failover loop: a queued
	// request stays where it was sent, a failed-over one puts a second account
	// under the same burst.
	workBuddyDefaultQueueWaitSeconds = 5

	// workBuddySignInVersion is the CLI version passed to the browser sign-in page.
	workBuddySignInVersion = workBuddyCLIVersion

	workBuddyChatPath   = "/v2"
	workBuddyConfigPath = "/v3/config"

	// The sign-in and refresh endpoints, all under the plan's API root.
	workBuddyStatePath   = "/v2/plugin/auth/state"
	workBuddyTokenPath   = "/v2/plugin/auth/token"
	workBuddyAccountPath = "/v2/plugin/login/account"
	workBuddyRefreshPath = "/v2/plugin/auth/token/refresh"

	// workBuddyRetryTokenCode / workBuddyRetryAccountCode answer while the
	// user has not finished signing in; they mean "ask again", not "failed".
	workBuddyRetryTokenCode   = 11217
	workBuddyRetryAccountCode = 12151

	workBuddyPollInterval   = time.Second
	workBuddySignInDeadline = 5 * time.Minute
	subscriptionPendingTTL  = 15 * time.Minute
	// workBuddyTokenExpirySkew refreshes slightly before the token lapses.
	workBuddyTokenExpirySkew = time.Minute
)

// The two builds' API roots; vars so tests can point them at an httptest
// server.
var (
	workBuddyCNEndpoint   = "https://copilot.tencent.com"
	workBuddyIntlEndpoint = "https://www.workbuddy.ai"
)

// workBuddySite is one WorkBuddy build: its endpoint, the platform its
// sign-in names, whether it carries WorkBuddy AI's extra client headers, and
// the models its plan is known to serve.
type workBuddySite struct {
	authType   string
	name       string
	platform   string
	intl       bool
	endpoint   func() string
	models     []workBuddyModel
	wbClientID string
}

type workBuddyModel struct {
	ID      string
	Context int
}

var (
	workBuddyCNSite = workBuddySite{
		authType: domain.AuthTypeWorkBuddy,
		name:     "WorkBuddy",
		platform: "workbuddy",
		endpoint: func() string { return workBuddyCNEndpoint },
		models: []workBuddyModel{
			{ID: "auto", Context: 168_000},
			{ID: "hy4-preview-f", Context: 1_000_000},
			{ID: "hy3", Context: 192_000},
			{ID: "hy3-x", Context: 192_000},
			{ID: "deepseek-v4.1-flash", Context: 1_000_000},
			{ID: "glm-5.3", Context: 1_000_000},
			{ID: "glm-5.3-flash", Context: 1_000_000},
			{ID: "glm-5.2", Context: 1_000_000},
			{ID: "glm-5.1", Context: 200_000},
			{ID: "glm-5v-turbo", Context: 200_000},
			{ID: "minimax-m3", Context: 512_000},
			{ID: "kimi-k3-1", Context: 1_000_000},
			{ID: "kimi-k2.7", Context: 256_000},
			{ID: "kimi-k2.6", Context: 256_000},
			{ID: "deepseek-v4-pro", Context: 1_000_000},
		},
	}
	workBuddyIntlSite = workBuddySite{
		authType: domain.AuthTypeWorkBuddyAI,
		name:     "WorkBuddy AI",
		platform: "workbuddy-ai",
		intl:     true,
		endpoint: func() string { return workBuddyIntlEndpoint },
		models: []workBuddyModel{
			{ID: "default-model", Context: 176_000},
			{ID: "fast-model", Context: 200_000},
			{ID: "balanced-model", Context: 256_000},
			{ID: "primary-model", Context: 272_000},
			{ID: "deep-model", Context: 176_000},
			{ID: "gpt-5.5", Context: 1_000_000},
			{ID: "gpt-5.4", Context: 272_000},
			{ID: "gpt-5.3-codex", Context: 272_000},
			{ID: "gemini-3.1-pro", Context: 400_000},
			{ID: "gemini-3.5-flash", Context: 1_000_000},
			{ID: "glm-5.3", Context: 1_000_000},
			{ID: "glm-5.2", Context: 1_000_000},
			{ID: "hy3", Context: 192_000},
			{ID: "kimi-k3", Context: 1_000_000},
			{ID: "kimi-k2.6", Context: 256_000},
			{ID: "minimax-m3", Context: 512_000},
		},
	}
)

func workBuddySiteForAuthType(authType string) (workBuddySite, bool) {
	switch strings.TrimSpace(authType) {
	case domain.AuthTypeWorkBuddy:
		return workBuddyCNSite, true
	case domain.AuthTypeWorkBuddyAI:
		return workBuddyIntlSite, true
	}
	return workBuddySite{}, false
}

// isWorkBuddyAuthType reports whether an auth type authenticates with a
// WorkBuddy subscription, whichever build it is.
func isWorkBuddyAuthType(authType string) bool {
	_, ok := workBuddySiteForAuthType(authType)
	return ok
}

// isWorkBuddyProvider reports whether a provider authenticates with a
// WorkBuddy subscription, whichever build it is.
func isWorkBuddyProvider(provider domain.Provider) bool {
	return isWorkBuddyAuthType(provider.AuthType)
}

// chatBaseURL is the OpenAI-compatible root the plan is served on; the
// gateway appends /chat/completions to it like it does for any OpenAI-Chat
// provider.
func (w workBuddySite) chatBaseURL() string {
	return strings.TrimRight(w.endpoint(), "/") + workBuddyChatPath
}

func (w workBuddySite) api() string { return strings.TrimRight(w.endpoint(), "/") }

func (w workBuddySite) clientVersion() string {
	return workBuddyDesktopVersion(w.intl)
}

func (w workBuddySite) uaBrand() string {
	if w.intl {
		return "workbuddy-ai"
	}
	return "WorkBuddy"
}

// chatUserAgent is the three-segment UA WorkBuddy's chat SDK sends.
func (w workBuddySite) chatUserAgent() string {
	ver := w.clientVersion()
	return "WorkBuddy/" + ver + " " + w.uaBrand() + "/" + ver + " CLI/" + workBuddyCLIVersionValue()
}

// rpcUserAgent is the desktop RPC layer (refresh, plugin auth, …).
func (w workBuddySite) rpcUserAgent() string {
	ver := w.clientVersion()
	brand := w.uaBrand()
	return brand + "/" + ver + " " + brand + "/" + ver + " CLI/" + workBuddyCLIVersionValue()
}

// v3ConfigUserAgent is what /v3/config model discovery sends.
func (w workBuddySite) v3ConfigUserAgent() string {
	return "WorkBuddy/" + w.clientVersion() + " CLI/" + workBuddyCLIVersionValue()
}

func workBuddyDefaultConcurrency(authType string) int {
	site, ok := workBuddySiteForAuthType(authType)
	if !ok {
		return 0
	}
	if site.intl {
		return workBuddyDefaultConcurrencyIntl
	}
	return workBuddyDefaultConcurrencyCN
}

// defaultModels is the plan's known model list, used until the product
// config answers with the live one.
func (w workBuddySite) defaultModels(providerID string) []domain.Model {
	out := make([]domain.Model, 0, len(w.models))
	for _, item := range w.models {
		out = append(out, domain.Model{
			ID:            item.ID,
			ProviderID:    providerID,
			Protocol:      domain.ProtocolOpenAIChat,
			ContextLength: item.Context,
			InMenu:        true,
		})
	}
	return out
}

// workBuddyDomain is the X-Domain a request carries: the account's own
// domain, or the endpoint's authority when the account did not say.
func workBuddyDomain(w workBuddySite, credential domain.WorkBuddyCredential) string {
	if domainName := strings.TrimSpace(credential.Domain); domainName != "" {
		return domainName
	}
	if parsed, err := url.Parse(w.api()); err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return ""
}

// ---- signing in ------------------------------------------------------------

// The three stores below are the state of an external-link sign-in, which a
// subscription that polls for its result shares: WorkBuddy and ZCode both hand
// the browser a page and then ask their own site until the account is ready, so
// one "waiting / done / failed" record per provider and one status per flow
// serves both. Named by provider id, which is what a flow belongs to.

type subscriptionOAuthPending struct {
	State     string
	FlowID    string
	CreatedAt time.Time
}

type subscriptionOAuthFlowStatus struct {
	Status    string    `json:"status"`
	Message   string    `json:"message,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type subscriptionOAuthStore struct {
	mu       sync.Mutex
	pending  map[string]subscriptionOAuthPending
	statuses map[string]subscriptionOAuthFlowStatus
}

func newSubscriptionOAuthStore() *subscriptionOAuthStore {
	return &subscriptionOAuthStore{
		pending:  map[string]subscriptionOAuthPending{},
		statuses: map[string]subscriptionOAuthFlowStatus{},
	}
}

func (store *subscriptionOAuthStore) put(providerID string, entry subscriptionOAuthPending) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.pending[providerID] = entry
}

func (store *subscriptionOAuthStore) take(providerID string) (subscriptionOAuthPending, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.pending[providerID]
	if !ok {
		return subscriptionOAuthPending{}, false
	}
	delete(store.pending, providerID)
	if time.Since(entry.CreatedAt) > subscriptionPendingTTL {
		return subscriptionOAuthPending{}, false
	}
	return entry, true
}

func (store *subscriptionOAuthStore) finish(providerID string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.pending, providerID)
}

func (store *subscriptionOAuthStore) setStatus(flowID, status, message string) {
	if strings.TrimSpace(flowID) == "" {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.statuses[flowID] = subscriptionOAuthFlowStatus{
		Status:    status,
		Message:   message,
		UpdatedAt: time.Now(),
	}
}

func (store *subscriptionOAuthStore) status(flowID string) (subscriptionOAuthFlowStatus, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.statuses[flowID]
	return entry, ok
}

// workBuddySignInURL is the page the user signs in on: WorkBuddy's own
// authUrl plus the app version and a fresh login session id its page expects.
func workBuddySignInURL(authURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(authURL))
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "https" {
		return "", errors.New("WorkBuddy 没有给出有效的登录页面")
	}
	session := make([]byte, 16)
	if _, err = rand.Read(session); err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("version", workBuddyCLIVersionValue())
	query.Set("loginSessionId", hex.EncodeToString(session))
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

type workBuddyCredentialPayload struct {
	AccessToken      string `json:"accessToken"`
	RefreshToken     string `json:"refreshToken"`
	ExpiresAt        int64  `json:"expiresAt"`
	ExpiresIn        int64  `json:"expiresIn"`
	RefreshExpiresAt int64  `json:"refreshExpiresAt"`
	RefreshExpiresIn int64  `json:"refreshExpiresIn"`
	Domain           string `json:"domain"`
	TokenType        string `json:"tokenType"`
}

// toCredential turns what WorkBuddy's token endpoint answered into the stored
// credential, normalising the two ways it can express an expiry (an absolute
// millisecond timestamp, or a lifetime in seconds from now).
func (p workBuddyCredentialPayload) toCredential(account domain.WorkBuddyCredential) domain.WorkBuddyCredential {
	now := time.Now()
	out := account
	out.AccessToken = strings.TrimSpace(p.AccessToken)
	if refresh := strings.TrimSpace(p.RefreshToken); refresh != "" {
		out.RefreshToken = refresh
	}
	if domainName := strings.TrimSpace(p.Domain); domainName != "" {
		out.Domain = domainName
	}
	out.SignInAt = now.UTC().Format(time.RFC3339)
	switch {
	case p.ExpiresAt > 0:
		out.ExpiresAt = time.UnixMilli(p.ExpiresAt).UTC().Format(time.RFC3339)
	case p.ExpiresIn > 0:
		out.ExpiresAt = now.Add(time.Duration(p.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	return out
}

// workBuddyAccountLabel names a signed-in account for the console: its
// nickname, its phone number, or its id.
func workBuddyAccountLabel(nickname, phone, uid string) string {
	for _, candidate := range []string{nickname, phone, uid} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return workBuddyHidablePhone(trimmed)
		}
	}
	return "WorkBuddy"
}

// workBuddyHidablePhone keeps only the first four and last four digits of a
// phone number. The label is what tells one WorkBuddy account from the next in
// every picker, so it has to stay unique; what it must not do is put a whole
// phone number on screen (a screenshot of the console is a shared artifact),
// which is why the account is stored as 1815****4947 rather than 181512344947.
func workBuddyHidablePhone(label string) string {
	if !workBuddyAllDigits(label) || len(label) < 7 {
		return label
	}
	return label[:4] + "****" + label[len(label)-4:]
}

func workBuddyAllDigits(value string) bool {
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

// ---- the plan's JSON API ---------------------------------------------------

// workBuddyError is a WorkBuddy business error. Its code lets the sign-in
// poll tell a "not ready yet, ask again" answer from a real failure; status is
// the HTTP status when the gateway page refused the request outright, which is
// how a dead access token shows up (401, often as an HTML page rather than an
// envelope).
type workBuddyError struct {
	code   int
	status int
	msg    string
}

func (e *workBuddyError) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("错误码 %d", e.code)
}

func workBuddyErrorCode(err error) (int, bool) {
	var target *workBuddyError
	if errors.As(err, &target) {
		return target.code, true
	}
	return 0, false
}

// workBuddyCall asks one of WorkBuddy's JSON endpoints, which wrap what they
// say in {code, msg, data}; code 0 is a success and dst reads data.
func workBuddyCall(ctx context.Context, site workBuddySite, method, endpoint string, headers map[string]string, body, dst any) error {
	var reader io.Reader
	if body != nil {
		encoded, _ := json.Marshal(body)
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", site.rpcUserAgent())
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	var envelope struct {
		Code json.Number     `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(payload, &envelope)
	code, _ := envelope.Code.Int64()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if code != 0 {
			return &workBuddyError{code: int(code), status: response.StatusCode, msg: envelope.Msg}
		}
		if envelope.Msg != "" {
			return &workBuddyError{code: response.StatusCode, status: response.StatusCode, msg: envelope.Msg}
		}
		return &workBuddyError{code: response.StatusCode, status: response.StatusCode,
			msg: fmt.Sprintf("WorkBuddy 返回 HTTP %d", response.StatusCode)}
	}
	if code != 0 {
		return &workBuddyError{code: int(code), msg: envelope.Msg}
	}
	if dst == nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}
	return json.Unmarshal(envelope.Data, dst)
}

// ---- the sign-in flow ------------------------------------------------------

func (s *Server) startWorkBuddyOAuth(providerID string, site workBuddySite) (authURL, flowID string, err error) {
	var state struct {
		State   string `json:"state"`
		AuthURL string `json:"authUrl"`
	}
	// The state endpoint sits behind the plan's auth gate even though nothing
	// is signed in yet; these headers are how WorkBuddy says "ask it bare".
	headers := map[string]string{
		"X-No-Authorization":   "true",
		"X-No-User-Id":         "true",
		"X-No-Enterprise-Id":   "true",
		"X-No-Department-Info": "true",
	}
	ctx, cancel := context.WithTimeout(context.Background(), workBuddySignInDeadline)
	endpoint := site.api() + workBuddyStatePath + "?platform=" + url.QueryEscape(site.platform)
	if callErr := workBuddyCall(ctx, site, http.MethodPost, endpoint, headers, map[string]any{}, &state); callErr != nil {
		cancel()
		return "", "", fmt.Errorf("WorkBuddy 登录初始化失败：%w", callErr)
	}
	if strings.TrimSpace(state.State) == "" || strings.TrimSpace(state.AuthURL) == "" {
		cancel()
		return "", "", errors.New("WorkBuddy 没有给出登录页面")
	}
	resolved, urlErr := workBuddySignInURL(state.AuthURL)
	if urlErr != nil {
		cancel()
		return "", "", urlErr
	}
	idBytes := make([]byte, 16)
	if _, err = rand.Read(idBytes); err != nil {
		cancel()
		return "", "", err
	}
	flowID = hex.EncodeToString(idBytes)
	s.subscriptionOAuthFlows.put(providerID, subscriptionOAuthPending{
		State:     state.State,
		FlowID:    flowID,
		CreatedAt: time.Now(),
	})
	s.subscriptionOAuthFlows.setStatus(flowID, "pending", "等待浏览器授权…")
	go s.pollWorkBuddyOAuthInBackground(ctx, cancel, providerID, flowID, site, state.State)
	return resolved, flowID, nil
}

// pollWorkBuddyOAuthInBackground waits for the user to finish signing in, then
// takes the token and the account the sign-in produced and stores them on the
// provider. WorkBuddy hands the two out on separate endpoints, each answered
// with a "not ready" code until the browser flow completes.
func (s *Server) pollWorkBuddyOAuthInBackground(
	ctx context.Context,
	cancel context.CancelFunc,
	providerID, flowID string,
	site workBuddySite,
	state string,
) {
	defer cancel()
	fail := func(message string) {
		s.subscriptionOAuthFlows.setStatus(flowID, "failed", message)
		s.subscriptionOAuthFlows.finish(providerID)
		s.logs.AddApp("warn", "workbuddy oauth flow failed", fmt.Sprintf("provider=%s %s", providerID, message))
	}

	var token workBuddyCredentialPayload
	tokenEndpoint := site.api() + workBuddyTokenPath + "?state=" + url.QueryEscape(state)
	if err := s.pollWorkBuddyJSON(ctx, site, tokenEndpoint, nil, workBuddyRetryTokenCode, func(payload json.RawMessage) error {
		return json.Unmarshal(payload, &token)
	}); err != nil {
		fail("WorkBuddy 登录失败：" + err.Error())
		return
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		fail("WorkBuddy 没有返回访问令牌")
		return
	}

	var account domain.WorkBuddyCredential
	credential := token.toCredential(account)
	headers := map[string]string{
		"Authorization":      "Bearer " + credential.AccessToken,
		"X-Domain":           workBuddyDomain(site, credential),
		"X-No-User-Id":       "true",
		"X-No-Enterprise-Id": "true",
	}
	var who struct {
		UID         string `json:"uid"`
		Nickname    string `json:"nickname"`
		PhoneNumber string `json:"phoneNumber"`
	}
	accountEndpoint := site.api() + workBuddyAccountPath + "?state=" + url.QueryEscape(state)
	if err := s.pollWorkBuddyJSON(ctx, site, accountEndpoint, headers, workBuddyRetryAccountCode, func(payload json.RawMessage) error {
		return json.Unmarshal(payload, &who)
	}); err != nil {
		fail("WorkBuddy 登录失败：" + err.Error())
		return
	}
	if strings.TrimSpace(who.UID) == "" {
		fail("WorkBuddy 没有返回账号信息")
		return
	}
	credential.UserID = strings.TrimSpace(who.UID)
	credential.AccountLabel = workBuddyAccountLabel(who.Nickname, who.PhoneNumber, who.UID)

	updated, err := s.router.SetProviderWorkBuddy(providerID, site.authType, credential)
	if err != nil {
		fail("WorkBuddy 登录失败：" + err.Error())
		return
	}
	if err := s.persistProviderOAuth(updated); err != nil {
		s.logs.AddApp("warn", "failed to persist workbuddy oauth token", err.Error())
	}
	if err := s.saveState(); err != nil {
		fail("WorkBuddy 登录失败：配置保存失败 " + err.Error())
		return
	}
	if synced, syncErr := s.syncWorkBuddyModels(providerID); syncErr == nil {
		updated = synced
	} else {
		s.logs.AddApp("warn", "workbuddy model sync failed", syncErr.Error())
	}
	s.subscriptionOAuthFlows.finish(providerID)
	s.subscriptionOAuthFlows.setStatus(flowID, "connected", "connected")
	s.logs.AddApp("info", "workbuddy oauth connected", fmt.Sprintf("provider=%s account=%s", providerID, credential.AccountLabel))
}

// pollWorkBuddyJSON asks endpoint every second until it answers with data,
// reading it through consume, and gives up (with the last error) when the
// flow's context ends. A retry code means the user has not finished yet.
func (s *Server) pollWorkBuddyJSON(
	ctx context.Context,
	site workBuddySite,
	endpoint string,
	headers map[string]string,
	retryCode int,
	consume func(json.RawMessage) error,
) error {
	for {
		select {
		case <-ctx.Done():
			return errors.New("登录超时，请重新开始")
		case <-time.After(workBuddyPollInterval):
		}
		var raw json.RawMessage
		err := workBuddyCall(ctx, site, http.MethodGet, endpoint, headers, nil, &raw)
		switch {
		case ctx.Err() != nil:
			return errors.New("登录超时，请重新开始")
		case err == nil && len(raw) > 0 && string(raw) != "null":
			return consume(raw)
		case err == nil:
			continue // ready but empty: ask again
		}
		if code, ok := workBuddyErrorCode(err); ok && code == retryCode {
			continue // not signed in yet
		}
		return err
	}
}

// ---- token refresh ---------------------------------------------------------

// workBuddyAccessTokenLifetime is how long the credential is assumed to stay
// valid when WorkBuddy signed it in without saying. Its access tokens last
// hours, not the month its recorded expiry — the refresh token's — suggests,
// and a token treated as never-expiring is only ever replaced after a 401 has
// already cost one request.
const workBuddyAccessTokenLifetime = 30 * time.Minute

// workBuddyTokenNeedsRefresh reports whether the access token is close enough
// to its expiry to renew.
//
// 判定写成这几行是有原因的：Go 1.26.1（Windows 工具链）会把「在同一函数里比较
// 两个相邻时刻」的写法编错，把明显在未来的时刻判成已到（memory
// go126-windows-slice-compare-miscompile）。续期锁的失败结论要靠「过期 / 没过
// 期」决定放行还是拦下，判反了会让一次抖动手撕掉所有排队的请求，所以这里只留
// 最简单的形态，别把它折叠回一行。
func workBuddyTokenNeedsRefresh(credential *domain.WorkBuddyCredential) bool {
	if credential == nil {
		return true
	}
	expiresAt := strings.TrimSpace(credential.ExpiresAt)
	if expiresAt == "" {
		// WorkBuddy signed the account in without an expiry. Trusting it
		// forever means never refreshing before the vendor refuses it, so age
		// it from the last sign-in instead; with nothing to go on either, the
		// 401 path below is what replaces it.
		signIn, err := time.Parse(time.RFC3339, strings.TrimSpace(credential.SignInAt))
		if err != nil {
			return false
		}
		return time.Now().After(signIn.Add(workBuddyAccessTokenLifetime))
	}
	parsed, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return false // unparseable expiry: fail open, the request will show it
	}
	return time.Now().Add(workBuddyTokenExpirySkew).After(parsed)
}

func workBuddyRefreshRequestHeaders(site workBuddySite, credential domain.WorkBuddyCredential) map[string]string {
	return map[string]string{
		"X-Refresh-Token":       credential.RefreshToken,
		"X-Auth-Refresh-Source": "plugin",
		"X-Domain":              workBuddyDomain(site, credential),
	}
}

// refreshWorkBuddyCredential trades the refresh token for a fresh access
// token, as WorkBuddy's own auth provider does.
func refreshWorkBuddyCredential(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential) (domain.WorkBuddyCredential, error) {
	var got workBuddyCredentialPayload
	endpoint := site.api() + workBuddyRefreshPath
	err := workBuddyCall(ctx, site, http.MethodPost, endpoint, workBuddyRefreshRequestHeaders(site, credential), map[string]any{}, &got)
	if err != nil {
		return credential, fmt.Errorf("WorkBuddy 令牌刷新失败: %w", err)
	}
	if strings.TrimSpace(got.AccessToken) == "" {
		return credential, errors.New("WorkBuddy 没有返回刷新后的令牌")
	}
	return got.toCredential(credential), nil
}

// refreshWorkBuddyTokenLocked trades current for a fresh credential and
// persists it. The caller holds the renew lock keyed by current.RefreshToken,
// which is what keeps two concurrent refreshes from spending the same rotating
// refresh token.
func (s *Server) refreshWorkBuddyTokenLocked(provider domain.Provider, site workBuddySite, current domain.WorkBuddyCredential) (domain.Provider, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	refreshed, err := refreshWorkBuddyCredential(ctx, site, current)
	if err != nil {
		s.recordRenewal(provider, "workbuddy", time.Now(), err)
		return provider, err
	}
	updated, err := s.router.SetProviderWorkBuddy(provider.ID, site.authType, refreshed)
	if err != nil {
		return provider, err
	}
	s.recordRenewal(updated, "workbuddy", time.Now(), nil)
	if err := s.persistProviderOAuth(updated); err != nil {
		s.logs.AddApp("warn", "failed to persist workbuddy token refresh", err.Error())
	}
	markTimingFlag(context.Background(), timingFlagOAuthRefresh)
	markTimingFlag(context.Background(), timingFlagSaveState)
	return updated, nil
}

// ensureFreshWorkBuddyToken guarantees the provider holds a usable access
// token. Called on every proxied request, so the unexpired path performs no
// network I/O at all.
func (s *Server) ensureFreshWorkBuddyToken(provider domain.Provider) (domain.Provider, error) {
	site, ok := workBuddySiteForAuthType(provider.AuthType)
	if !ok {
		return provider, nil
	}
	credential := provider.WorkBuddy
	if credential == nil || strings.TrimSpace(credential.AccessToken) == "" {
		return domain.Provider{}, errors.New("WorkBuddy 尚未登录，请先在控制台完成登录")
	}
	if !workBuddyTokenNeedsRefresh(credential) {
		return provider, nil
	}
	if strings.TrimSpace(credential.RefreshToken) == "" {
		// 没有可续期的令牌：未过期还能用（上面的 needsRefresh 已放行），
		// 已过期就只剩一枚死令牌——明确报错，别让客户端去读上游的 401
		// "invalid token"，那看起来像它自己配错了。
		if workBuddyCredentialExpired(credential) {
			return provider, errors.New("WorkBuddy 令牌已过期且没有可续期的凭证，请重新登录")
		}
		return provider, nil
	}
	// WorkBuddy 的 refresh token 轮换：并发续期里后到的那个会被判无效。按令牌
	// 串行；等锁期间别人续好的直接用。
	renewLock, settled := beginOauthRenew(provider.ID, credential.RefreshToken, s.oauthRenewProviderReader(provider.ID))
	defer renewLock.Unlock()
	if settled != nil && settled.err != nil {
		// 刚刚（等锁时）有人拿这同一枚 refresh token 去换并失败了：不再花第二次。
		// 交回的是路由里当前的凭证，而不是那位调用方手里的旧快照。
		return settledOauthRenewFailure(settled, provider)
	}
	// 等锁期间别人可能已经续过了：以路由里的最新凭证为准，续期也只花它。手上
	// 这枚 refresh token 可能已被别人花掉、整条令牌链都换了，再拿它去换只会被
	// 判无效（严重的还会把链作废）。新令牌若仍判过期，用新令牌再续一次。
	current := *credential
	if fresh, ok := s.freshWorkBuddyCredential(provider.ID); ok {
		current = fresh
		if !workBuddyTokenNeedsRefresh(&fresh) {
			provider.WorkBuddy = &fresh
			return provider, nil
		}
	}
	updated, err := s.refreshWorkBuddyTokenLocked(provider, site, current)
	if err != nil {
		// 未过期时一次续期抖动不该拦请求（手上这枚或许还能用）；已过期就没有
		// 可用的令牌了，报错（与 Trae 6626556 同口径）。
		s.logs.AddApp("warn", "workbuddy token refresh failed", fmt.Sprintf("provider=%s %s", provider.ID, err.Error()))
		// 失败也记下来：排在这枚令牌后面的请求不该各花一次。结论里要带「续期时
		// 这枚令牌是否已过期」——续期者自己据此决定拦不拦，等锁方必须同一个口径，
		// 否则一次抖动会把排队的请求全打掉，而出去探路的那个照常服务。
		expired := workBuddyCredentialExpired(&current)
		settleOauthRenew(provider.ID, current.RefreshToken, provider, err, expired)
		if expired {
			s.logs.AddApp("error", "workbuddy token refresh failed and the token expired", fmt.Sprintf("provider=%s %s", provider.ID, err.Error()))
			return provider, fmt.Errorf("WorkBuddy 令牌已过期且续期失败：%w", err)
		}
		return provider, nil
	}
	retireOauthRenewLock(provider.ID, current.RefreshToken, renewLock)
	return updated, nil
}

// workBuddyCredentialExpired reports whether the stored access token is past its
// expiry (not merely inside the refresh lead): a failed renewal then has
// nothing left to serve with. 与 workBuddyTokenNeedsRefresh 一样只留最简单的
// 比较形态（见那里关于 Go 1.26 Windows 工具链的说明）。
func workBuddyCredentialExpired(credential *domain.WorkBuddyCredential) bool {
	if credential == nil {
		return true
	}
	expires, err := time.Parse(time.RFC3339, strings.TrimSpace(credential.ExpiresAt))
	if err != nil {
		return false
	}
	return time.Now().After(expires)
}

// forceRefreshWorkBuddyToken refreshes even when the stored expiry is still in
// the future, because that expiry is the refresh-token lifetime (about a
// month), not proof the access token is still accepted. WorkBuddy answers a
// revoked token with a bare 401, and signing the same account in elsewhere is
// enough to revoke it; without this path the account stays dead until someone
// signs in again by hand.
func (s *Server) forceRefreshWorkBuddyToken(providerID string) (domain.Provider, error) {
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		return domain.Provider{}, err
	}
	site, ok := workBuddySiteForAuthType(provider.AuthType)
	if !ok {
		return provider, nil
	}
	credential := provider.WorkBuddy
	if credential == nil || strings.TrimSpace(credential.AccessToken) == "" {
		return domain.Provider{}, errors.New("WorkBuddy 尚未登录，请在控制台完成登录")
	}
	if strings.TrimSpace(credential.RefreshToken) == "" {
		return domain.Provider{}, errors.New("WorkBuddy 登录已失效（没有 refresh token），请在控制台重新登录")
	}
	renewLock, settled := beginOauthRenew(provider.ID, credential.RefreshToken, s.oauthRenewProviderReader(provider.ID))
	defer renewLock.Unlock()
	if settled != nil && settled.err != nil {
		// This request's own 401 filed its renewal attempt behind another request's,
		// and that attempt failed. Only that attempt's failure is shared.
		current := s.readWorkBuddyCredential(provider)
		if current.AccessToken == credential.AccessToken {
			// No newer credential to retry with yet: spend this request's own
			// probe — the vendor may have recovered — and hand back whatever the
			// router holds afterwards.
			_, _ = s.refreshWorkBuddyProbe(provider, site, current, renewLock)
			current = s.readWorkBuddyCredential(provider)
		}
		// The credential to retry with. A 401 is what brought this request here,
		// so the shared failure describes the token it already lost; whether it
		// is the error to report turns on what is left to retry with, exactly as
		// the caller that ran the shared renewal decides it: a router credential
		// that works (a sign-in, or the probe above) earns the 401 the re-send it
		// exists for. Handing the error back beside it would end the request
		// before any upstream response exists, and the retry a 401 is supposed
		// to get would never happen. Nothing usable left: report the failure
		// (an auth failure, not a transport one), which is what the failover
		// loop acts on.
		latest, _ := s.latestWorkBuddyProvider(provider)
		if current.AccessToken != credential.AccessToken && !workBuddyCredentialExpired(&current) {
			return latest, nil
		}
		return latest, settled.err
	}

	// Another goroutine may have refreshed while this one waited for the lock,
	// or the user signed in again. Read the router right before spending the
	// credential, not the copy this call opened with.
	current := s.readWorkBuddyCredential(provider)
	if current.AccessToken != credential.AccessToken {
		// Read again at the moment of choosing it: a sign-in may have landed
		// while the credential was compared.
		return s.latestWorkBuddyProvider(provider)
	}
	return s.renewWorkBuddyToken(provider, site, current, renewLock, true)
}

// readWorkBuddyCredential reads the credential the renew lock belongs to from
// the router; the caller holds the lock for it.
func (s *Server) readWorkBuddyCredential(provider domain.Provider) domain.WorkBuddyCredential {
	if stored, err := s.router.ProviderByID(provider.ID); err == nil && stored.WorkBuddy != nil {
		return *stored.WorkBuddy
	}
	return *provider.WorkBuddy
}

// latestWorkBuddyProvider is the router's provider, read now.
func (s *Server) latestWorkBuddyProvider(provider domain.Provider) (domain.Provider, error) {
	if latest, err := s.router.ProviderByID(provider.ID); err == nil && latest.WorkBuddy != nil {
		return latest, nil
	}
	return provider, nil
}

// renewWorkBuddyToken trades current for a fresh credential and persists it,
// settling the shared renew entry with the failure or retiring the spent token.
func (s *Server) renewWorkBuddyToken(provider domain.Provider, site workBuddySite, current domain.WorkBuddyCredential, renewLock *sync.Mutex, settleFailure bool) (domain.Provider, error) {
	updated, err := s.refreshWorkBuddyTokenLocked(provider, site, current)
	if err != nil {
		s.logs.AddApp("warn", "workbuddy token refresh failed", fmt.Sprintf("provider=%s %s", provider.ID, err.Error()))
		if settleFailure {
			settleOauthRenew(provider.ID, current.RefreshToken, provider, err, true)
		} else {
			retireSpreadOauthRenewLock(provider.ID, current.RefreshToken, renewLock)
		}
		return provider, fmt.Errorf("%w（WorkBuddy 登录可能已失效，请在控制台重新登录）", err)
	}
	retireOauthRenewLock(provider.ID, current.RefreshToken, renewLock)
	return updated, nil
}

// refreshWorkBuddyProbe is one more forced refresh for a request whose own 401
// was answered by another request's failure. It records no new shared failure
// (the one already there speaks for this attempt too) and a probe that fails
// does not retire anything: the entry still owes its answer to the waiters.
func (s *Server) refreshWorkBuddyProbe(provider domain.Provider, site workBuddySite, current domain.WorkBuddyCredential, renewLock *sync.Mutex) (domain.Provider, error) {
	updated, err := s.refreshWorkBuddyTokenLocked(provider, site, current)
	if err != nil {
		s.logs.AddApp("warn", "workbuddy token refresh failed", fmt.Sprintf("provider=%s %s", provider.ID, err.Error()))
		return provider, fmt.Errorf("%w（WorkBuddy 登录可能已失效，请在控制台重新登录）", err)
	}
	retireSpreadOauthRenewLock(provider.ID, current.RefreshToken, renewLock)
	return updated, nil
}

// ---- request shaping -------------------------------------------------------

// workBuddyRequestID is a new id as WorkBuddy makes them: 32 hex digits.
func workBuddyRequestID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// workBuddySystemPrompt is the system message a chat that has none is sent
// with: WorkBuddy turns one whose first message isn't the system prompt away,
// and scripts and plain chat clients often send none.
const workBuddySystemPrompt = "You are a helpful assistant."

// workBuddyFlagged are the words WorkBuddy turns a chat away for with code
// 11128 "Illegal API invocation from an unapproved channel" — it reads the
// prompt, not the headers, and each replacement below says the same thing in
// words it passes. Measured against copilot.tencent.com on 2026-10-19: the
// billing header, "You are Claude Code, Anthropic's official CLI for Claude"
// and "Main branch (you will usually use this for PRs)" each draw 11128 from a
// system message and answer 200 once replaced. The Codex line still draws it on
// nothing today but the international build was not measured, so it stays.
//
// The rule the replacements follow is the measured one (2026-10-30,
// magpie-community/plugins packages/workbuddy/REFUSED.md): the check matches a
// whole opening line exactly, so moving one character is enough — and a
// replacement that drops what the line said is a bug, not a fix. Adding words
// does not help ("You are an AI agent powered by DeepSeek Harness CLI." is
// refused too), while the same shape naming another product passes.
var workBuddyFlagged = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(?i)x-anthropic-billing-header:[^\n]*\n?`), ""},
	{regexp.MustCompile(`(?i)You are Claude Code, Anthropic['’]s official CLI for Claude`), "You are Claude Code, Anthropic's CLI for Claude"},
	{regexp.MustCompile(`(?i)Main branch \(you will usually use this for PRs\)`), "Main branch (usually the base for PRs)"},
	{regexp.MustCompile(`(?i)(?:the )?Codex CLI is an open source project led by OpenAI\.`), "The Codex CLI is an open source project led by OpenAI."},
	// The harness identity line a coding agent opens with. A comma is the whole
	// fix, and the agent still learns which harness it runs in.
	{regexp.MustCompile(`(?i)You are an AI agent powered by ([^.]+)\.`), "You are an AI agent, powered by ${1}."},
	{regexp.MustCompile(`(?i)You are a coding agent powered by ([^.]+)\.`), "You are a coding agent, powered by ${1}."},
	// One punctuation mark moved, nothing else: "You are X Agent, built by Y."
	{regexp.MustCompile(`(?i)You are ([^.]+) Agent, built by ([^.]+)\.`), "You are ${1} Agent built by ${2}."},
}

var (
	workBuddyBareBillingHeader = regexp.MustCompile(`(?i)x-anthropic-billing-header`)
	workBuddyBareBillingKV     = regexp.MustCompile(`(?i)\bcc_[a-z0-9_]+=[^;\n]*;?\s*`)
	workBuddyDSHPathsRe        = regexp.MustCompile(
		`(?is)Tokens prefixed with @ are paths the user explicitly referenced\..*?path (?:containing|that contains) spaces\.`)
)

const workBuddyDSHPathsReplacement = "Paths beginning with @ refer to files the user explicitly mentioned. " +
	"Relative paths are resolved against the project root; absolute paths point " +
	"to files or directories on the host. A trailing slash marks a directory, so " +
	"list it when its contents matter. Anything else is a file: open it with the " +
	"read tool when its contents are needed, and never claim to have inspected it " +
	"before reading. The @\"...\" form only quotes a path that contains spaces."

// workBuddyRefusedWords is that refusal as WorkBuddy words it, whichever build.
var workBuddyRefusedWords = regexp.MustCompile(`(?i)unapproved channel|illegal api invocation`)

// WorkBuddyRefusedNote tells the user what a refusal that outlasts
// workBuddyFlagged is for: the agent's own prompt, not the account. Kept clear
// of the words classifyProviderFailover reads as quota or throttle.
const WorkBuddyRefusedNote = "WorkBuddy 按提示词内容拦下了这条请求（渠道校验），账号本身没问题；换一个 Agent 用它，或给这个 Key 配上备用 Provider。"

// workBuddyPromptRefused says an upstream answer is that refusal of the prompt.
func workBuddyPromptRefused(status int, body []byte) bool {
	return status >= 400 && workBuddyRefusedWords.Match(body)
}

// unflagWorkBuddyText puts workBuddyFlagged's words in place in one text.
func unflagWorkBuddyText(text string) string {
	for _, item := range workBuddyFlagged {
		text = item.pattern.ReplaceAllString(text, item.replacement)
	}
	text = workBuddyBareBillingKV.ReplaceAllString(text, "")
	text = workBuddyBareBillingHeader.ReplaceAllString(text, "x-anthropic-billing-hdr")
	if strings.Contains(text, "Tokens prefixed with @") {
		text = workBuddyDSHPathsRe.ReplaceAllString(text, workBuddyDSHPathsReplacement)
	}
	return text
}

// sanitizeWorkBuddyAllMessages rewrites DSH @-path fingerprint blocks in any
// message role; upstream matches the contiguous paragraph regardless of role.
func sanitizeWorkBuddyAllMessages(messages []any) ([]any, bool) {
	changed := false
	out := make([]any, 0, len(messages))
	for _, item := range messages {
		message, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		switch content := message["content"].(type) {
		case string:
			if !strings.Contains(content, "Tokens prefixed with @") {
				out = append(out, message)
				continue
			}
			replaced := workBuddyDSHPathsRe.ReplaceAllString(content, workBuddyDSHPathsReplacement)
			if replaced == content {
				out = append(out, message)
				continue
			}
			changed = true
			message["content"] = replaced
		case []any:
			parts, partsChanged := sanitizeWorkBuddyContentParts(content)
			if !partsChanged {
				out = append(out, message)
				continue
			}
			changed = true
			message["content"] = parts
		}
		out = append(out, message)
	}
	return out, changed
}

func sanitizeWorkBuddyContentParts(parts []any) ([]any, bool) {
	changed := false
	out := make([]any, 0, len(parts))
	for _, item := range parts {
		part, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		text, _ := part["text"].(string)
		if text == "" || !strings.Contains(text, "Tokens prefixed with @") {
			out = append(out, part)
			continue
		}
		replaced := workBuddyDSHPathsRe.ReplaceAllString(text, workBuddyDSHPathsReplacement)
		if replaced == text {
			out = append(out, part)
			continue
		}
		changed = true
		part["text"] = replaced
		out = append(out, part)
	}
	return out, changed
}

// unflagWorkBuddyMessages clears channel-check fingerprints from system and
// assistant turns (see workBuddyFlagged). sanitizeWorkBuddyAllMessages also
// rewrites DSH @-path blocks in any role when present.
func unflagWorkBuddyMessages(messages []any) ([]any, bool) {
	changed := false
	out := make([]any, 0, len(messages))
	for _, item := range messages {
		message, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		role, _ := message["role"].(string)
		if role != "system" && role != "assistant" {
			out = append(out, message)
			continue
		}
		switch content := message["content"].(type) {
		case string:
			text := unflagWorkBuddyText(content)
			if text == content {
				break
			}
			changed = true
			if strings.TrimSpace(text) == "" {
				continue
			}
			message["content"] = text
		case []any:
			parts, partsChanged := unflagWorkBuddyParts(content)
			if !partsChanged {
				break
			}
			changed = true
			if len(parts) == 0 {
				// A tool call carries the turn on even with no text left; a
				// text-only turn that said nothing but a flagged line is
				// dropped whole. WorkBuddy takes the two user turns that
				// leaves side by side (measured: answers 200).
				if _, hasCalls := message["tool_calls"]; hasCalls {
					message["content"] = parts
				} else {
					continue
				}
			} else {
				message["content"] = parts
			}
		}
		out = append(out, message)
	}
	return out, changed
}

func unflagWorkBuddyParts(parts []any) ([]any, bool) {
	changed := false
	out := make([]any, 0, len(parts))
	for _, item := range parts {
		part, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		text, isText := part["text"].(string)
		if !isText {
			out = append(out, part)
			continue
		}
		replaced := unflagWorkBuddyText(text)
		if replaced == text {
			out = append(out, part)
			continue
		}
		changed = true
		if strings.TrimSpace(replaced) == "" {
			continue
		}
		part["text"] = replaced
		out = append(out, part)
	}
	return out, changed
}

// prepareWorkBuddyChatBody rewrites an OpenAI-Chat request into the shape
// WorkBuddy accepts: streaming forced on (it refuses a non-stream request), a
// leading system message, "developer" turned into "system", the tools a
// message carries lifted into the request's own, each tool's name and schema
// mended into a shape its models take, a tool_choice named the way it reads,
// and workBuddyFlagged's words out of the system turns. The result is only
// ever a stream, so a client that asked for a single JSON object gets one
// rebuilt by workBuddyStreamAsCompletion.
func prepareWorkBuddyChatBody(body []byte) ([]byte, bool) {
	prepared, _, changed := shapeWorkBuddyChatBody(body)
	return prepared, changed
}

// shapeWorkBuddyChatBody is prepareWorkBuddyChatBody with the names its tools
// were given, each to the name the client knows, so the model's calls can be
// named back in the answer.
func shapeWorkBuddyChatBody(body []byte) ([]byte, map[string]string, bool) {
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil || payload == nil {
		return body, nil, false
	}
	messages, hasMessages := payload["messages"].([]any)
	if !hasMessages {
		return body, nil, false // not a chat request (count_tokens, embeddings, …)
	}
	changed := false
	if maxCompletion, exists := payload["max_completion_tokens"]; exists {
		// WorkBuddy's endpoint takes max_tokens; an OpenAI-compatible client
		// normally sends max_completion_tokens.
		if _, hasMaxTokens := payload["max_tokens"]; !hasMaxTokens {
			payload["max_tokens"] = maxCompletion
		}
		delete(payload, "max_completion_tokens")
		changed = true
	}
	if streaming, _ := payload["stream"].(bool); !streaming {
		payload["stream"] = true
		changed = true
	}
	for _, item := range messages {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := message["role"].(string); role == "developer" {
			// A developer turn itself draws 11128 on the CN build
			// (measured 2026-10-23 against copilot.tencent.com: a clean
			// "You are a helpful assistant." in a developer turn is
			// refused, the same text in a system turn answers 200), so it
			// goes out as system. magpie's plugin made the opposite move
			// in its 0.1.15 ("a system prompt sent as a developer message
			// passes the channel check"); that no longer holds, and
			// copying it here would refuse every Codex client.
			message["role"] = "system"
			changed = true
		}
	}
	if read, wasChanged := workBuddyToolBlocksToCalls(messages, chatDeclaredToolNames(payload)); wasChanged {
		messages, changed = read, true
		payload["messages"] = messages
	}
	if paired, wasChanged := pairWorkBuddyToolTurns(messages); wasChanged {
		messages, changed = paired, true
		payload["messages"] = messages
	}
	if liftWorkBuddyMessageTools(payload) {
		changed = true
		messages, _ = payload["messages"].([]any)
	}
	if unflagged, wasChanged := unflagWorkBuddyMessages(messages); wasChanged {
		messages, changed = unflagged, true
		payload["messages"] = messages
	}
	if scrubbed, wasChanged := sanitizeWorkBuddyAllMessages(messages); wasChanged {
		messages, changed = scrubbed, true
		payload["messages"] = messages
	}
	if workBuddyToolChoiceIsNone(payload["tool_choice"]) {
		// A tool_choice of none means no tools at all: WorkBuddy is not told
		// about tools the answer may not use.
		delete(payload, "tool_choice")
		delete(payload, "tools")
		delete(payload, "functions")
		changed = true
	}
	aliases, fitted := fitWorkBuddyTools(payload)
	if fitted {
		changed = true
	}
	if normalizeWorkBuddyToolChoice(payload) {
		changed = true
	}
	if len(messages) > 0 {
		if first, isObject := messages[0].(map[string]any); !isObject || first["role"] != "system" {
			payload["messages"] = append([]any{map[string]any{"role": "system", "content": workBuddySystemPrompt}}, messages...)
			changed = true
		}
	}
	if !changed {
		return body, nil, false
	}
	prepared := encodeJSONWithoutEscaping(payload)
	if prepared == nil {
		return body, nil, false
	}
	return prepared, aliases, true
}

// ---- one send path for a WorkBuddy chat --------------------------------------

// workBuddySender posts a shaped chat body with the given provider's current
// credentials and returns the upstream response. The provider is a parameter
// rather than captured state so a send can be retried after a token refresh.
type workBuddySender func(provider domain.Provider, body []byte) (*http.Response, error)

// sendWorkBuddyChat sends a shaped WorkBuddy chat and adds magpie's two
// repairs around the send. A chat that ends with tool results and then the
// user's message is refused by some models with 400 code 11153; it is sent
// again with an empty assistant reply between them, and the model is
// remembered for the rest of the run so its next chats carry that reply up
// front. A successful answer's tool calls are named back with aliases.
//
// A 401 is WorkBuddy revoking the access token, which its recorded expiry (the
// refresh token's, about a month away) never predicts, so one forced refresh
// and a single re-send happen here. The refresh also persists the rotated
// token, so the next request starts from the new one.
func (s *Server) sendWorkBuddyChat(provider domain.Provider, model string, body []byte, aliases map[string]string, send workBuddySender) (*http.Response, error) {
	key := provider.ID + "\x00" + strings.ToLower(strings.TrimSpace(model))
	if _, remembered := s.workBuddyAfterTools.Load(key); remembered {
		if quiet := replyAfterWorkBuddyTools(body); quiet != nil {
			body = quiet
		}
	}
	response, err := send(provider, body)
	if err != nil || response == nil {
		return response, err
	}
	if response.StatusCode == http.StatusUnauthorized {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		_ = response.Body.Close()
		refreshed, refreshErr := s.forceRefreshWorkBuddyToken(provider.ID)
		if refreshErr != nil {
			return nil, refreshErr
		}
		provider = refreshed
		response, err = send(provider, body)
		if err != nil || response == nil {
			return response, err
		}
	}
	if response.StatusCode == http.StatusBadRequest {
		original, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		restore := func() (*http.Response, error) {
			response.Body = io.NopCloser(bytes.NewReader(original))
			response.ContentLength = int64(len(original))
			return response, nil
		}
		switch {
		case readErr != nil:
			return restore()
		case workBuddyAfterToolsRefused(original):
			quiet := replyAfterWorkBuddyTools(body)
			if quiet == nil {
				return restore()
			}
			s.workBuddyAfterTools.Store(key, true)
			retried, retryErr := send(provider, quiet)
			if retryErr != nil || retried == nil {
				// Surface the upstream's own refusal, not a transport error.
				return restore()
			}
			response = retried
		case workBuddyPromptRefused(response.StatusCode, original):
			// The rewrites above cover the words measured so far; a prompt with
			// one they don't cover is turned away whole. WorkBuddy reads only
			// the system turn for this check, so the client's instructions are
			// sent once more as the first user turn's, with WorkBuddy's own
			// system prompt in their place — a chat that would otherwise never
			// run. As magpie does (its 0.1.29).
			softened := workBuddySystemPromptAsInstructions(body)
			if softened == nil {
				return restore()
			}
			retried, retryErr := send(provider, softened)
			if retryErr != nil || retried == nil {
				return restore()
			}
			response = retried
		default:
			// Not one of the refusals above: the 400 is the upstream's answer to
			// the request itself (bad parameters, context too long, unknown
			// model, a quota message). Its body was read to match it and is put
			// back as the original bytes, so the client reads the vendor's own
			// message and the failover classifier sees the same text.
			return restore()
		}
	}
	restoreWorkBuddyToolNames(response, aliases)
	return response, nil
}

// workBuddySystemPromptAsInstructions is the last repair for a refusal that
// outlasts workBuddyFlagged: the request with the client's system prompt moved
// into the first user turn as instructions, and workBuddySystemPrompt in its
// place. WorkBuddy's channel check reads the system turn, not the user's, so a
// prompt it refuses passes once it is no longer the system turn — at the cost of
// the client's instructions arriving as the user's words, which is why this is
// only ever sent after the request was already refused. nil when there is no
// system text to move, and nothing is changed.
func workBuddySystemPromptAsInstructions(body []byte) []byte {
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil || payload == nil {
		return nil
	}
	messages, ok := payload["messages"].([]any)
	if !ok {
		return nil
	}
	systemIndex := -1
	instructions := ""
	for index, item := range messages {
		message, isMessage := item.(map[string]any)
		if !isMessage || stringValue(message["role"]) != "system" {
			continue
		}
		systemIndex = index
		instructions = workBuddyMessageText(message["content"])
		break
	}
	if systemIndex < 0 || strings.TrimSpace(instructions) == "" || strings.TrimSpace(instructions) == workBuddySystemPrompt {
		return nil
	}
	userIndex := -1
	for index := systemIndex + 1; index < len(messages); index++ {
		message, isMessage := messages[index].(map[string]any)
		if isMessage && stringValue(message["role"]) == "user" {
			userIndex = index
			break
		}
	}
	if userIndex < 0 {
		return nil
	}
	system, _ := messages[systemIndex].(map[string]any)
	user, _ := messages[userIndex].(map[string]any)
	system["content"] = workBuddySystemPrompt
	prefix := "The instructions below were sent as the system prompt; follow them.\n\n" + instructions
	switch content := user["content"].(type) {
	case string:
		user["content"] = prefix + "\n\n" + content
	case []any:
		user["content"] = append([]any{map[string]any{"type": "text", "text": prefix}}, content...)
	default:
		user["content"] = prefix
	}
	return encodeJSONWithoutEscaping(payload)
}

// workBuddyMessageText reads one turn's text, whether it is a string or parts.
func workBuddyMessageText(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		texts := make([]string, 0, len(typed))
		for _, item := range typed {
			part, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if text, isText := part["text"].(string); isText && text != "" {
				texts = append(texts, text)
			}
		}
		return strings.Join(texts, "\n\n")
	default:
		return ""
	}
}

// workBuddyUnansweredResult is what an unanswered call is given, so the exchange
// closes and the turn can go on. The wording is magpie's (its pairToolMessages and
// anthropic.go share it), which is what the upstreams that validate a tool
// exchange have been answered with since.
const workBuddyUnansweredResult = "[The result of this tool call is unavailable: the turn was interrupted.]"

// workBuddyToolBlocksToCalls reads Anthropic's tool blocks out of a Chat request:
// a bridge that hands Claude Code's conversation to a Chat endpoint keeps
// assistant parts as {type:"tool_use"} and answers as a user message of
// {type:"tool_result"} blocks, and WorkBuddy then answers "unsupported content
// type … tool_use" (11101) for the parts it cannot parse and "tool calls and tool
// results do not match" (11148) for calls its history never names. magpie drops
// these parts on a translation — its own IR cannot produce them — but a relay that
// dropped them would lose the conversation's tool history, so they are read into
// the dialect Chat names them by instead. declared is the body's own tools[];
// a call's name it holds is written in its spelling. A body carrying no blocks
// is untouched.
func workBuddyToolBlocksToCalls(messages []any, declared map[string]struct{}) ([]any, bool) {
	changed := false
	out := make([]any, 0, len(messages))
	for index, item := range messages {
		message, isMessage := item.(map[string]any)
		parts, isParts := message["content"].([]any)
		if !isMessage || !isParts {
			out = append(out, item)
			continue
		}
		keeps, calls, results := []any{}, []any{}, []any{}
		for position, raw := range parts {
			block, isBlock := raw.(map[string]any)
			if !isBlock {
				keeps = append(keeps, raw)
				continue
			}
			switch stringValue(block["type"]) {
			case "tool_use":
				// The name goes out as the chat's tools[] spell it. A name the
				// request does not offer is the client's own spelling of a tool
				// this chat does not carry; the model copies whatever it reads in
				// the history, and the client only dispatches the names it
				// registered ("No such tool available: bash. Tool names are
				// case-sensitive: call Bash instead."), so the declared spelling
				// is what has to be written down.
				name := restoreDeclaredToolName(strings.TrimSpace(stringValue(block["name"])), declared)
				if name == "" {
					keeps = append(keeps, raw)
					continue
				}
				arguments := "{}"
				if input, exists := block["input"]; exists {
					if text, isText := input.(string); isText {
						arguments = text
					} else if encoded, err := json.Marshal(input); err == nil {
						arguments = string(encoded)
					}
				}
				id := strings.TrimSpace(stringValue(block["id"]))
				if id == "" {
					// The same call at the same place gets the same id on every
					// turn, so the upstream's prompt cache still holds.
					id = chatStableCallID(index, position, name, arguments)
				}
				calls = append(calls, map[string]any{
					"id": id, "type": "function",
					"function": map[string]any{"name": name, "arguments": arguments},
				})
			case "tool_result":
				results = append(results, map[string]any{
					"role":         "tool",
					"tool_call_id": strings.TrimSpace(stringValue(block["tool_use_id"])),
					"content":      workBuddyResultText(block["content"]),
				})
			default:
				keeps = append(keeps, raw)
			}
		}
		if len(calls) == 0 && len(results) == 0 {
			out = append(out, message)
			continue
		}
		changed = true
		if len(calls) > 0 {
			message["tool_calls"] = calls
			if text, onlyText := workBuddyTextOfParts(keeps); onlyText {
				message["content"] = text
			} else if len(keeps) == 0 {
				message["content"] = ""
			} else {
				message["content"] = keeps
			}
			out = append(out, message)
			continue
		}
		// A tool turn must sit right behind the assistant turn that called, so the
		// answers go ahead of whatever else this turn has to say.
		out = append(out, results...)
		if len(keeps) > 0 {
			if text, onlyText := workBuddyTextOfParts(keeps); onlyText && text != "" {
				out = append(out, map[string]any{"role": message["role"], "content": text})
			} else {
				out = append(out, map[string]any{"role": message["role"], "content": keeps})
			}
		}
	}
	return out, changed
}

// workBuddyResultText flattens a tool_result's content, which is a string or a
// list of blocks, into the text a Chat tool turn carries.
func workBuddyResultText(content any) string {
	switch typed := content.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []any:
		text, onlyText := workBuddyTextOfParts(typed)
		if onlyText {
			return text
		}
		if encoded, err := json.Marshal(content); err == nil {
			return string(encoded)
		}
		return text
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

// workBuddyTextOfParts joins the text of a content list and says whether every
// part of it was text.
func workBuddyTextOfParts(parts []any) (text string, onlyText bool) {
	onlyText = true
	lines := make([]string, 0, len(parts))
	for _, raw := range parts {
		part, isPart := raw.(map[string]any)
		if !isPart || stringValue(part["type"]) != "text" {
			onlyText = false
			continue
		}
		if line := stringValue(part["text"]); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n"), onlyText
}

// pairWorkBuddyToolTurns mends a tool exchange for the upstreams that validate it:
// WorkBuddy answers 11148 "tool calls and tool results do not match", Kimi
// "tool_call_id is not found", for the same shapes. The rules are magpie's
// pairToolMessages, copied turn for turn:
//
//   - the answers to one turn's calls go in the calls' order, because an agent
//     returns parallel results out of order;
//   - a second answer to the same call is dropped, the first stands;
//   - an answer to no pending call becomes a user message holding its result, so
//     the result survives with no made-up call and no id used twice;
//   - a call left unanswered gets a synthetic result, so the exchange closes;
//   - an assistant turn with nothing in it is dropped while a call is pending,
//     where it would sit between the call and its answer.
func pairWorkBuddyToolTurns(messages []any) ([]any, bool) {
	var pending []string
	var tools []any
	order := map[string]int{}
	answered := map[string]bool{}
	changed := false
	out := make([]any, 0, len(messages))

	flushTools := func() {
		if len(tools) == 0 {
			return
		}
		before := make([]string, 0, len(tools))
		for _, item := range tools {
			message, _ := item.(map[string]any)
			before = append(before, stringValue(message["tool_call_id"]))
		}
		sort.SliceStable(tools, func(i, j int) bool {
			left, _ := tools[i].(map[string]any)
			right, _ := tools[j].(map[string]any)
			return order[stringValue(left["tool_call_id"])] < order[stringValue(right["tool_call_id"])]
		})
		for position, item := range tools {
			message, _ := item.(map[string]any)
			if stringValue(message["tool_call_id"]) != before[position] {
				changed = true
			}
		}
		out = append(out, tools...)
		tools = nil
	}
	flushPending := func() {
		flushTools()
		for _, id := range pending {
			if !answered[id] {
				out = append(out, map[string]any{"role": "tool", "tool_call_id": id, "content": workBuddyUnansweredResult})
				changed = true
			}
		}
		pending, order, answered = nil, map[string]int{}, map[string]bool{}
	}
	answerAsUser := func(message map[string]any) {
		text := strings.TrimSpace(workBuddyResultText(message["content"]))
		if text == "" {
			text = "Tool result received."
		}
		if last := len(out); last > 0 {
			if previous, isMap := out[last-1].(map[string]any); isMap && stringValue(previous["role"]) == "user" {
				if held, isText := previous["content"].(string); isText {
					// Two user turns in a row are refused by some chat templates.
					previous["content"] = held + "\n\n" + text
					return
				}
			}
		}
		out = append(out, map[string]any{"role": "user", "content": text})
	}

	for _, item := range messages {
		message, isMap := item.(map[string]any)
		if !isMap {
			flushPending()
			out = append(out, item)
			continue
		}
		switch strings.ToLower(stringValue(message["role"])) {
		case "assistant":
			calls, _ := message["tool_calls"].([]any)
			if len(calls) == 0 {
				if len(pending) > 0 && workBuddyEmptyAssistant(message) {
					changed = true
					continue
				}
				flushPending()
				out = append(out, message)
				continue
			}
			flushPending()
			out = append(out, message)
			for position, raw := range calls {
				call, isCall := raw.(map[string]any)
				if !isCall {
					continue
				}
				id := strings.TrimSpace(stringValue(call["id"]))
				pending = append(pending, id)
				order[id] = position
			}
		case "tool":
			id := strings.TrimSpace(stringValue(message["tool_call_id"]))
			if _, known := order[id]; !known {
				flushPending()
				answerAsUser(message)
				changed = true
				continue
			}
			if answered[id] {
				changed = true // a second answer: the first one stands
				continue
			}
			answered[id] = true
			tools = append(tools, message)
		default:
			flushPending()
			out = append(out, message)
		}
	}
	flushPending()
	return out, changed
}

// workBuddyEmptyAssistant reports an assistant turn that says nothing: no text, no
// calls, no reasoning of its own.
func workBuddyEmptyAssistant(message map[string]any) bool {
	if calls, _ := message["tool_calls"].([]any); len(calls) > 0 {
		return false
	}
	for _, key := range []string{"reasoning_content", "reasoning"} {
		if stringValue(message[key]) != "" {
			return false
		}
	}
	switch content := message["content"].(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(content) == ""
	case []any:
		return len(content) == 0
	}
	return false
}

// workBuddyToolChoiceIsNone reads either spelling an OpenAI client uses for "no
// tools".
func workBuddyToolChoiceIsNone(choice any) bool {
	switch typed := choice.(type) {
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "none")
	case map[string]any:
		return strings.EqualFold(stringValue(typed["type"]), "none")
	}
	return false
}

// normalizeWorkBuddyToolChoice turns the object form an OpenAI client names a tool
// with into the string WorkBuddy reads, and drops a choice that names nothing.
func normalizeWorkBuddyToolChoice(payload map[string]any) bool {
	choice, exists := payload["tool_choice"]
	if !exists || choice == nil {
		delete(payload, "tool_choice")
		return exists
	}
	switch typed := choice.(type) {
	case string:
		return false // "auto", "required": WorkBuddy reads the string as it is
	case map[string]any:
		name := stringValue(typed["name"])
		if function, isFunction := typed["function"].(map[string]any); isFunction {
			name = firstNonEmptyString(stringValue(function["name"]), name)
		}
		if strings.EqualFold(stringValue(typed["type"]), "function") && name != "" {
			payload["tool_choice"] = name
			return true
		}
	}
	delete(payload, "tool_choice")
	return true
}

// applyWorkBuddyHeaders authenticates a chat request with the account's
// WorkBuddy session and asks for the desktop client's whole header family. It
// runs after the generic provider auth and overwrites Authorization.
func applyWorkBuddyHeaders(request *http.Request, provider domain.Provider) {
	site, ok := workBuddySiteForAuthType(provider.AuthType)
	if !ok || provider.WorkBuddy == nil {
		return
	}
	applyWorkBuddyChatHeaders(request, site, provider.WorkBuddy, workBuddyAgentTypeMain)
}

// applyWorkBuddyChatClientHeaders keeps the old entry point; the family itself
// lives in workbuddy_chat_headers.go, next to the spelling map that decides how
// each name goes out on the wire.
func applyWorkBuddyChatClientHeaders(request *http.Request, site workBuddySite, credential *domain.WorkBuddyCredential) {
	applyWorkBuddyChatHeaders(request, site, credential, workBuddyAgentTypeMain)
}

// ---- streaming for clients that asked for one JSON object ------------------

// workBuddyStreamAsCompletion rebuilds a chat.completion from the SSE stream
// WorkBuddy sent, for a client that asked for a non-streaming request. The
// gateway always streams upstream because WorkBuddy refuses otherwise, so this
// is what makes a plain "stream": false caller still get a plain JSON answer.
func workBuddyStreamAsCompletion(streamBody []byte, model string) ([]byte, error) {
	completion, err := buildWorkBuddyCompletion(streamBody, model)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(completion)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

type workBuddyStreamChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role             string `json:"role"`
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    *int   `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		TotalTokens         int `json:"total_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// workBuddyToolCall accumulates one streamed tool call: WorkBuddy, like any
// OpenAI-compatible stream, splits a call's arguments across chunks.
type workBuddyToolCall struct {
	id        string
	name      string
	arguments strings.Builder
}

func buildWorkBuddyCompletion(streamBody []byte, model string) (map[string]any, error) {
	var (
		completionID   string
		created        int64
		streamModel    = strings.TrimSpace(model)
		role           = "assistant"
		content        strings.Builder
		reasoning      strings.Builder
		finishReason   string
		usage          map[string]any
		toolCalls      = map[int]*workBuddyToolCall{}
		toolCallOrder  []int
		sawChunk       bool
		upstreamErrMsg string
	)
	for _, line := range strings.Split(string(streamBody), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "data:"))
		if line == "" || line == "[DONE]" {
			continue
		}
		var chunk workBuddyStreamChunk
		if json.Unmarshal([]byte(line), &chunk) != nil {
			continue
		}
		if chunk.Error != nil {
			upstreamErrMsg = strings.TrimSpace(chunk.Error.Message)
			continue
		}
		if chunk.ID != "" {
			completionID = chunk.ID
		}
		if chunk.Created > 0 {
			created = chunk.Created
		}
		if strings.TrimSpace(chunk.Model) != "" {
			streamModel = strings.TrimSpace(chunk.Model)
		}
		sawChunk = true
		if chunk.Usage != nil {
			usage = map[string]any{
				"prompt_tokens":     chunk.Usage.PromptTokens,
				"completion_tokens": chunk.Usage.CompletionTokens,
				"total_tokens":      chunk.Usage.TotalTokens,
			}
			if chunk.Usage.PromptTokensDetails.CachedTokens != 0 {
				usage["prompt_tokens_details"] = map[string]any{"cached_tokens": chunk.Usage.PromptTokensDetails.CachedTokens}
			}
			if chunk.Usage.CompletionTokensDetails.ReasoningTokens != 0 {
				usage["completion_tokens_details"] = map[string]any{"reasoning_tokens": chunk.Usage.CompletionTokensDetails.ReasoningTokens}
			}
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Role != "" {
				role = choice.Delta.Role
			}
			content.WriteString(choice.Delta.Content)
			reasoning.WriteString(choice.Delta.ReasoningContent)
			for index, call := range choice.Delta.ToolCalls {
				slot := 0
				if call.Index != nil {
					slot = *call.Index
				} else {
					slot = index
				}
				entry, ok := toolCalls[slot]
				if !ok {
					entry = &workBuddyToolCall{}
					toolCalls[slot] = entry
					toolCallOrder = append(toolCallOrder, slot)
				}
				if call.ID != "" {
					entry.id = call.ID
				}
				if call.Function.Name != "" {
					entry.name = call.Function.Name
				}
				entry.arguments.WriteString(call.Function.Arguments)
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finishReason = *choice.FinishReason
			}
		}
	}
	if !sawChunk {
		if upstreamErrMsg != "" {
			return nil, fmt.Errorf("WorkBuddy 流式响应出错：%s", upstreamErrMsg)
		}
		return nil, errors.New("WorkBuddy 的流式响应没有内容")
	}
	message := map[string]any{"role": role, "content": content.String()}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	if len(toolCallOrder) > 0 {
		calls := make([]map[string]any, 0, len(toolCallOrder))
		for _, slot := range toolCallOrder {
			entry := toolCalls[slot]
			calls = append(calls, map[string]any{
				"id":   entry.id,
				"type": "function",
				"function": map[string]any{
					"name":      entry.name,
					"arguments": entry.arguments.String(),
				},
			})
		}
		message["tool_calls"] = calls
	}
	if finishReason == "" {
		finishReason = "stop"
	}
	if completionID == "" {
		completionID = "chatcmpl-workbuddy"
	}
	if created == 0 {
		created = time.Now().Unix()
	}
	if usage == nil {
		usage = map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
	}
	out := map[string]any{
		"id":      completionID,
		"object":  "chat.completion",
		"created": created,
		"model":   streamModel,
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
		"usage": usage,
	}
	return out, nil
}

// rewriteWorkBuddyUpstreamStreamedBody turns the upstream SSE body into a
// single chat.completion, and reopens it on the response so the passthrough
// writer can read it as it would any buffered body.
func rewriteWorkBuddyUpstreamStreamedBody(response *http.Response, model string) error {
	return forcedStreamAsCompletion(response, model, workBuddyStreamAsCompletion)
}

// forcedStreamAsCompletion is the shared half of that rewrite, for every
// upstream that only streams and answers in OpenAI's own chunk shape: buffer
// the stream, rebuild one object with `aggregate`, and reopen the response as
// JSON. An upstream error or a body with no events at all is passed through
// untouched, so a failure still reaches the client as the upstream said it.
func forcedStreamAsCompletion(response *http.Response, model string, aggregate func([]byte, string) ([]byte, error)) error {
	payload, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	response.Body.Close()
	if err != nil {
		response.Body = io.NopCloser(bytes.NewReader(payload))
		return err
	}
	if response.StatusCode >= 400 || !bytes.Contains(payload, []byte("data:")) {
		response.Body = io.NopCloser(bytes.NewReader(payload))
		return nil
	}
	completion, err := aggregate(payload, model)
	if err != nil {
		response.Body = io.NopCloser(bytes.NewReader(payload))
		return nil // forward what came back rather than inventing an error
	}
	response.Body = io.NopCloser(bytes.NewReader(completion))
	response.ContentLength = int64(len(completion))
	response.Header.Set("Content-Type", "application/json; charset=utf-8")
	return nil
}

// ---- models ----------------------------------------------------------------

// workBuddyModelDetail is one model in the product config. Credits is the
// multiple WorkBuddy's picker shows ("x0.03", "0" or a number); Promotions
// carries the limited-time discounts the picker badges (限时免费, 夜间折扣).
type workBuddyModelDetail struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	MaxInputTokens  int    `json:"maxInputTokens"`
	MaxOutputTokens int    `json:"maxOutputTokens"`
	Credits         any    `json:"credits"`
}

type workBuddyPromotion struct {
	Kind     string   `json:"kind"`
	ModelIDs []string `json:"modelIds"`
	Enabled  *bool    `json:"enabled"`
	Schedule struct {
		ValidFrom  string            `json:"validFrom"`
		ValidUntil string            `json:"validUntil"`
		Daily      []json.RawMessage `json:"daily"`
	} `json:"schedule"`
	Discount *struct {
		DiscountedCredits any `json:"discountedCredits"`
		Factor            any `json:"factor"`
	} `json:"discount"`
}

// workBuddyProductConfig is the plan's product config: its agents and each
// model's details. Which product answers goes by the User-Agent — WorkBuddy
// sends "CLI/<version> WorkBuddy/<version>", and a bare WorkBuddy/<version>
// gets CodeBuddy IDE's config, which lists no CLI agent.
type workBuddyProductConfig struct {
	Agents []struct {
		Name   string   `json:"name"`
		Models []string `json:"models"`
	} `json:"agents"`
	Models     []workBuddyModelDetail `json:"models"`
	Promotions []workBuddyPromotion   `json:"modelPromotions"`
}

// fetchWorkBuddyModels asks the plan's product config for the models its CLI
// agent is served.
func fetchWorkBuddyModels(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential) ([]domain.Model, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, site.api()+workBuddyConfigPath, nil)
	if err != nil {
		return nil, err
	}
	applyWorkBuddyHeaders(request, domain.Provider{AuthType: site.authType, WorkBuddy: &credential})
	request.Header.Set("User-Agent", site.v3ConfigUserAgent())
	request.Header.Set("X-Requested-With", "XMLHttpRequest")
	request.Header.Set("Accept", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("WorkBuddy 模型列表: HTTP %d", response.StatusCode)
	}
	var envelope struct {
		Code int                    `json:"code"`
		Msg  string                 `json:"msg"`
		Data workBuddyProductConfig `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, fmt.Errorf("WorkBuddy 模型列表: %w", err)
	}
	if envelope.Code != 0 {
		return nil, fmt.Errorf("WorkBuddy 模型列表: %s (%d)", envelope.Msg, envelope.Code)
	}
	out := make([]domain.Model, 0, len(envelope.Data.Models))
	for _, agent := range envelope.Data.Agents {
		if agent.Name != "cli" {
			continue
		}
		for _, id := range agent.Models {
			model := domain.Model{
				ID:            id,
				Protocol:      domain.ProtocolOpenAIChat,
				ContextLength: resolveModelContextLength(id, 0),
				InMenu:        true,
			}
			for _, detail := range envelope.Data.Models {
				if detail.ID != id {
					continue
				}
				model.Name = detail.Name
				model.ContextLength = detail.MaxInputTokens
				model.MaxOutputTokens = detail.MaxOutputTokens
				model.Free, model.Rate, model.RateWas = workBuddyModelPrice(detail, envelope.Data.Promotions, time.Now())
				break
			}
			out = append(out, model)
		}
	}
	for index := range out {
		out[index].ProviderID = ""
	}
	return out, nil
}

// workBuddyCreditsSuffix is the unit WorkBuddy appends to a credits value.
var workBuddyCreditsSuffix = regexp.MustCompile(`\s+credits?$`)

// workBuddyCredits reads a credits value the way WorkBuddy prints it: "x0.03",
// "0.5 credits", 0. A rate it can't read (or none, or negative) is not a price,
// and the model shows no badge rather than a made-up one.
func workBuddyCredits(value any) (float64, bool) {
	text, isText := value.(string)
	if isText {
		text = strings.ToLower(strings.TrimSpace(text))
		text = workBuddyCreditsSuffix.ReplaceAllString(text, "")
		text = strings.TrimPrefix(text, "x")
		if text == "" {
			return 0, false
		}
		value = text
	} else if value == nil {
		return 0, false
	}
	rate, ok := workBuddyJSONNumber(value)
	if !ok || rate < 0 {
		return 0, false
	}
	return rate, true
}

func workBuddyJSONNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, false
		}
		return typed, true
	case string:
		number, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
			return 0, false
		}
		return number, true
	}
	return 0, false
}

// workBuddyDiscount reads a promotion's price: its discountedCredits ("0.50x"),
// else the factor times the model's own credits.
func workBuddyDiscount(p workBuddyPromotion, base float64, baseOK bool) (float64, bool) {
	if p.Discount == nil {
		return 0, false
	}
	said := p.Discount.DiscountedCredits
	if text, ok := said.(string); ok {
		text = strings.TrimSpace(text)
		text = strings.TrimSuffix(strings.TrimSuffix(text, "x"), "X")
		said = text
	}
	if rate, ok := workBuddyCredits(said); ok {
		return rate, true
	}
	factor, ok := workBuddyJSONNumber(p.Discount.Factor)
	if !ok || factor < 0 || !baseOK {
		return 0, false
	}
	return base * factor, true
}

// workBuddyPromotionRunning says whether a promotion is on now: enabled and
// inside validFrom..validUntil. A missing or unparseable bound is ignored, the
// way the picker treats it.
func workBuddyPromotionRunning(p workBuddyPromotion, now time.Time) bool {
	if p.Enabled != nil && !*p.Enabled {
		return false
	}
	if from, err := time.Parse(time.RFC3339, p.Schedule.ValidFrom); err == nil && from.After(now) {
		return false
	}
	if until, err := time.Parse(time.RFC3339, p.Schedule.ValidUntil); err == nil && !until.After(now) {
		return false
	}
	return true
}

// workBuddyModelPrice mirrors magpie's priceOf: a discount that only runs some
// hours of the day (夜间免费) keeps its full price in the list, because a list
// asked for at night would otherwise read "free" all day; a whole-day discount
// becomes the price with the old one as the struck-through RateWas.
func workBuddyModelPrice(detail workBuddyModelDetail, promotions []workBuddyPromotion, now time.Time) (free bool, rate, rateWas float64) {
	base, baseOK := workBuddyCredits(detail.Credits)
	var mine []workBuddyPromotion
	for _, promotion := range promotions {
		if promotion.Kind != "discount" || !workBuddyPromotionRunning(promotion, now) {
			continue
		}
		for _, id := range promotion.ModelIDs {
			if id == detail.ID {
				mine = append(mine, promotion)
				break
			}
		}
	}
	hourly := false
	for _, promotion := range mine {
		if len(promotion.Schedule.Daily) > 0 {
			hourly = true
			break
		}
	}
	if hourly {
		nightFree := false
		for _, promotion := range mine {
			if promotion.Discount == nil || len(promotion.Schedule.Daily) == 0 {
				continue
			}
			if discounted, ok := workBuddyDiscount(promotion, base, baseOK); ok && discounted == 0 {
				nightFree = true
				break
			}
		}
		if !baseOK || (base == 0 && nightFree) {
			return false, 0, 0
		}
		return false, base, 0
	}
	best := math.Inf(1)
	found := false
	for _, promotion := range mine {
		if promotion.Discount == nil {
			continue
		}
		discounted, ok := workBuddyDiscount(promotion, base, baseOK)
		if !ok || (found && discounted >= best) {
			continue
		}
		best, found = discounted, true
	}
	if found {
		rateWas = 0
		if baseOK && base > best {
			rateWas = base
		}
		return best == 0, best, rateWas
	}
	if !baseOK {
		return false, 0, 0
	}
	return base == 0, base, 0
}

// syncWorkBuddyModels stores the plan's live model list on the provider,
// falling back to the known list when the config can't be read.
func (s *Server) syncWorkBuddyModels(providerID string) (domain.Provider, error) {
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		return domain.Provider{}, err
	}
	site, ok := workBuddySiteForAuthType(provider.AuthType)
	if !ok {
		return provider, nil
	}
	credential := provider.WorkBuddy
	if credential == nil {
		return s.storeWorkBuddyModels(provider, site.defaultModels(provider.ID))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	models, err := fetchWorkBuddyModels(ctx, site, *credential)
	if err != nil || len(models) == 0 {
		if err != nil {
			s.logs.AddApp("warn", "workbuddy product config unavailable, using known models", err.Error())
		}
		return s.storeWorkBuddyModels(provider, site.defaultModels(provider.ID))
	}
	for index := range models {
		models[index].ProviderID = providerID
	}
	return s.storeWorkBuddyModels(provider, models)
}

func (s *Server) storeWorkBuddyModels(provider domain.Provider, models []domain.Model) (domain.Provider, error) {
	updated, err := s.router.UpdateProviderModels(provider.ID, models, "healthy")
	if err != nil {
		return provider, err
	}
	if err := s.saveState(); err != nil {
		return updated, err
	}
	return updated, nil
}

// workBuddyQuota is an account's credit allowance, read the way WorkBuddy's
// own usage page reads it.
type workBuddyQuota struct {
	Plan      string              `json:"plan,omitempty"`
	User      string              `json:"user,omitempty"`
	Windows   []workBuddyQuotaWin `json:"windows"`
	Error     string              `json:"error,omitempty"`
	FetchedAt string              `json:"fetchedAt,omitempty"`
	// Checkin rides on the same reply so the card can show today's 签到 without a
	// request of its own.
	Checkin *checkinView `json:"checkin,omitempty"`
}

type workBuddyQuotaWin struct {
	Name   string  `json:"name"`
	Used   float64 `json:"used"`
	Amount float64 `json:"amount"`
	Limit  float64 `json:"limit"`
	Unit   string  `json:"unit"`
	// ResetsAt is when the window starts again (a team account's cycle).
	ResetsAt string `json:"resetsAt,omitempty"`
	// RunsOutAt/RunsOut are when the soonest of what is left is lost and how
	// much: a personal account's credits come in batches that each end on their
	// own day, so they can expire before the cycle resets.
	RunsOutAt string  `json:"runsOutAt,omitempty"`
	RunsOut   float64 `json:"runsOut,omitempty"`
}

// workBuddyNumber reads a capacity the billing API sends as a JSON string
// ("3300", "438.88"), though it occasionally comes as a bare number.
type workBuddyNumber float64

func (n *workBuddyNumber) UnmarshalJSON(raw []byte) error {
	trimmed := strings.Trim(string(raw), `"`)
	if trimmed == "" || trimmed == "null" {
		*n = 0
		return nil
	}
	parsed, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return err
	}
	*n = workBuddyNumber(parsed)
	return nil
}

// workBuddyAccountHeaders are the headers a plan-account JSON call carries: the
// bearer plus the two identity headers the account is asked with. Every account
// endpoint takes them — the credit summary and the daily check-in alike.
func workBuddyAccountHeaders(site workBuddySite, credential domain.WorkBuddyCredential) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + credential.AccessToken,
		"X-User-Id":     credential.UserID,
		"X-Domain":      workBuddyDomain(site, credential),
		"X-Product":     "SaaS",
		"X-IDE-Type":    "WorkBuddy",
	}
}
