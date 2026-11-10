package gateway

// Qoder's daily credits, claimed the way Qoder's own client claims them.
//
// Both sites list an account's campaigns on their openapi host; the day's
// credits are the campaign whose actionType is CLAIM_BENEFIT, and a POST to
// …/campaigns/{id}/claim takes them. The server decides what may be claimed, so
// the gateway asks instead of assuming an amount, and it asks again inside the
// same day: a campaign's own window can roll over at any hour, not at Beijing
// midnight, which is why a success carries the window end
// (domain.CheckinRecord.Until) and is re-read once that passes.
//
// Two things about the credential matter here. The account pages take the
// *device* token, not the job token that signs inference; a connection made by
// pasting a personal access token has no device token, and Qoder accepts its job
// token on these pages instead — nothing is rotated on that path, so a refusal
// is the answer. And the international site checks the device identity of the
// machine Qoder is installed on, which only Qoder's native helper can produce: a
// gateway on a server without Qoder cannot check in there, and says so instead
// of pretending. Qoder CN asks for no such header.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

const (
	qoderCampaignsPath = "/sash/api/v1/me/campaigns"
	// The three answers Qoder's campaigns page gives about one campaign.
	qoderClaimAction   = "CLAIM_BENEFIT"
	qoderClaimable     = "CLAIMABLE"
	qoderCampaignClaim = "CLAIMED"
	// qoderCreditBenefit is the benefit of the day's credits. CLAIM_BENEFIT
	// also covers one-off gifts (REDEMPTION_CODE), which must not be read as
	// "today's check-in is in".
	qoderCreditBenefit = "CREDITS"
	// qoderMachineTTL is how long a read device identity is reused; Qoder's own
	// desktop client renews its native identity hourly.
	qoderMachineTTL = time.Hour
	// qoderMachineTimeout bounds one read of the helper: it is a local binary,
	// and a check-in must not hang on it.
	qoderMachineTimeout = 20 * time.Second
)

// qoderCampaign is one campaign as the account sees it. startAt/endAt bound its
// claim window, which can cross Beijing midnight; Qoder has been seen giving
// both seconds and milliseconds.
type qoderCampaign struct {
	ID          string  `json:"campaignId"`
	ActionType  string  `json:"actionType"`
	ClaimStatus string  `json:"claimStatus"`
	StartAt     float64 `json:"startAt"`
	EndAt       float64 `json:"endAt"`
	Benefit     struct {
		// Kind is CREDITS for the day's credits and REDEMPTION_CODE for the
		// one-off gifts Qoder also files under CLAIM_BENEFIT.
		Kind   string  `json:"kind"`
		Amount float64 `json:"amount"`
	} `json:"benefit"`
}

// qoderCredits says whether a campaign is the day's credits. A campaign is not
// the check-in just because it is CLAIM_BENEFIT: Qoder files a one-off gift
// under the same action, and it stays claimed for weeks — counting it made the
// console say "today is in (+1)" while the day's 100 credits were still
// CLAIMABLE, which is what a check-in that never happened looks like.
func (c qoderCampaign) credits() bool {
	return c.Benefit.Kind == "" || strings.EqualFold(c.Benefit.Kind, qoderCreditBenefit)
}

func (c qoderCampaign) active(now time.Time) bool {
	return !now.Before(qoderMoment(c.StartAt)) && now.Before(qoderMoment(c.EndAt))
}

// qoderMoment is a time Qoder gave as a number, in seconds or milliseconds.
func qoderMoment(value float64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	if value >= 1e12 {
		return time.UnixMilli(int64(value))
	}
	return time.Unix(int64(value), 0)
}

// qoderClaimReply is a claim's answer. Qoder wraps it in {data} on one shape of
// reply and answers plainly on another.
type qoderClaimReply struct {
	Status  string `json:"status"`
	Benefit struct {
		Amount float64 `json:"amount"`
	} `json:"benefit"`
	Data *qoderClaimReply `json:"data"`
}

// qoderCheckinAccountKey names the account a record belongs to: the two sites
// can hold the same uid, and a sign-in that moved to another account must not
// inherit today's answer.
func qoderCheckinAccountKey(site qoderSite, credential domain.QoderCredential) string {
	uid := strings.TrimSpace(credential.UID)
	if uid == "" {
		return ""
	}
	return site.AuthType + "|" + uid
}

// qoderCheckin claims the account's claimable campaigns for the day. It hands
// back the credential as it stands at the end, because rotating a refused device
// token spends a refresh token that has to be kept.
func qoderCheckin(ctx context.Context, server *Server, provider domain.Provider, site qoderSite, credential domain.QoderCredential, now time.Time) (checkinResult, domain.QoderCredential) {
	result := checkinResult{AccountKey: qoderCheckinAccountKey(site, credential)}
	base := strings.TrimRight(site.OpenAPI, "/") + qoderCampaignsPath
	sender := newQoderCampaignSender(server, provider.ID, site, credential)
	if sender.token == "" {
		result.Outcome = domain.CheckinFailed
		result.Message = "这条 Qoder 连接没有可用于账号页面的令牌，请重新登录"
		return result, sender.credential
	}
	var list struct {
		Campaigns []qoderCampaign `json:"campaigns"`
	}
	if err := sender.call(ctx, http.MethodGet, base, &list); err != nil {
		return sender.failure(err), sender.credential
	}
	daily := []qoderCampaign{}
	// credits is the subset that is the day's check-in. Claiming still walks
	// every CLAIM_BENEFIT campaign — a one-off gift is worth taking — but only
	// these may answer "today is in" and set the window end.
	credits := []qoderCampaign{}
	for _, campaign := range list.Campaigns {
		if campaign.ActionType != qoderClaimAction {
			continue
		}
		daily = append(daily, campaign)
		if campaign.credits() {
			credits = append(credits, campaign)
		}
	}
	if len(daily) == 0 {
		result.Outcome = domain.CheckinInactive
		// An empty list on a request that could not carry this machine's identity
		// is the vendor filtering the account, not a day with nothing on offer.
		if sender.identityNote != "" {
			result.Message = "活动列表是空的，并且这次请求没能带上本机设备身份（" + sender.identityNote +
				"）：Qoder 会把无法识别的客户端整条过滤掉。装一次 Qoder 桌面版或用 QODER_RUNTIME_INFO 指到 runtime-info 可执行文件后重试。"
		}
		return result, sender.credential
	}

	var (
		credit          float64
		claimed, failed int
		why             string
		earliest        time.Time
	)
	for _, campaign := range daily {
		if campaign.credits() && campaign.active(now) {
			if end := qoderMoment(campaign.EndAt); !end.IsZero() && (earliest.IsZero() || end.Before(earliest)) {
				earliest = end
			}
		}
		if campaign.ClaimStatus != qoderClaimable || campaign.ID == "" {
			continue
		}
		var reply qoderClaimReply
		endpoint := base + "/" + url.PathEscape(campaign.ID) + "/claim"
		err := sender.call(ctx, http.MethodPost, endpoint, &reply)
		if reply.Data != nil {
			reply = *reply.Data
		}
		if err == nil && reply.Status != qoderCampaignClaim {
			err = fmt.Errorf("签到请求得到的回答是 %q", reply.Status)
		}
		if err != nil {
			failed++
			why = err.Error()
			continue
		}
		claimed++
		amount := reply.Benefit.Amount
		if amount <= 0 {
			amount = campaign.Benefit.Amount
		}
		if amount > 0 {
			credit += amount
		}
	}
	switch {
	case failed > 0:
		result.Outcome = domain.CheckinFailed
		result.Message = why
	case claimed > 0:
		result.Outcome = domain.CheckinClaimed
		result.Credit = credit
		result.Until = earliest
	case len(credits) == 0:
		// Only one-off gifts right now: no day's credits are on offer, so
		// there is nothing to be "in" for and nothing to retry for.
		result.Outcome = domain.CheckinInactive
	default:
		// Nothing claimable left: today's are in when a campaign that is
		// already claimed is still inside its window.
		for _, campaign := range credits {
			if campaign.ClaimStatus == qoderCampaignClaim && campaign.active(now) {
				result.Outcome = domain.CheckinDone
				result.Credit = campaign.Benefit.Amount
				result.Until = earliest
				return result, sender.credential
			}
		}
		result.Outcome = domain.CheckinFailed
		result.Message = "今天的签到积分还没放出来"
	}
	return result, sender.credential
}

// qoderCampaignSender asks the campaigns pages as one account. A refused device
// token is rotated once and the request made again — the same way the quota read
// handles it — because Qoder records no lifetime for that token.
type qoderCampaignSender struct {
	server     *Server
	providerID string
	site       qoderSite
	credential domain.QoderCredential
	token      string
	rotatable  bool
	// rotationUsed is the one device-token rotation a sender is allowed: the
	// refresh token it spends is single-use, so a second refusal is the answer.
	rotationUsed bool
	// identityNote is why this machine's identity could not be sent, if it could
	// not: an empty campaign list is the vendor filtering an account it cannot
	// recognise, and the operator needs to know that rather than think the day's
	// credits were all claimed.
	identityNote string
	headers      map[string]string
}

func newQoderCampaignSender(server *Server, providerID string, site qoderSite, credential domain.QoderCredential) *qoderCampaignSender {
	token, rotatable := qoderCampaignToken(credential)
	return &qoderCampaignSender{server: server, providerID: providerID, site: site, credential: credential, token: token, rotatable: rotatable}
}

func (sender *qoderCampaignSender) call(ctx context.Context, method, endpoint string, dst any) error {
	status, err := sender.ask(ctx, method, endpoint, dst)
	if err == nil {
		return nil
	}
	refused := status == http.StatusUnauthorized || status == http.StatusForbidden
	if refused && sender.rotatable && !sender.rotationUsed {
		sender.rotationUsed = true
		rotated, refreshErr := sender.server.rotateQoderDeviceTokenFor(ctx, sender.providerID, sender.site, sender.credential)
		if refreshErr == nil {
			sender.credential = rotated
			sender.headers = nil // the identity note may change with the account
			if token, _ := qoderCampaignToken(rotated); token != "" {
				sender.token = token
			}
			if _, retryErr := sender.ask(ctx, method, endpoint, dst); retryErr == nil {
				return nil
			}
		}
	}
	if refused {
		// Some accounts' campaign pages answer the plain account identity even
		// when the app's is refused: try it before reporting a failure the user
		// can do nothing about.
		if _, retryErr := qoderCampaignDo(ctx, method, endpoint, qoderAccountHeaders(sender.site, sender.credential), sender.token, dst); retryErr == nil {
			return nil
		}
	}
	return err
}

// ask sends one request with this machine's identity resolved once per sender,
// keeping the reason when there is none to send.
func (sender *qoderCampaignSender) ask(ctx context.Context, method, endpoint string, dst any) (int, error) {
	if sender.headers == nil {
		machine, note := qoderCampaignMachineIdentity(ctx, sender.site, sender.credential)
		sender.identityNote = note
		sender.headers = qoderCampaignHeaders(sender.site, machine)
	}
	return qoderCampaignDo(ctx, method, endpoint, sender.headers, sender.token, dst)
}

func (sender *qoderCampaignSender) failure(err error) checkinResult {
	return checkinResult{
		Outcome:    domain.CheckinFailed,
		Message:    err.Error(),
		Offline:    checkinUnanswered(err),
		AccountKey: qoderCheckinAccountKey(sender.site, sender.credential),
	}
}

// qoderCampaignToken is the bearer the account pages take, and whether a refused
// one can be rotated.
func qoderCampaignToken(credential domain.QoderCredential) (string, bool) {
	if device := strings.TrimSpace(credential.DeviceToken); device != "" {
		return device, strings.TrimSpace(credential.DeviceRefresh) != ""
	}
	// A device-chat account signs its requests with the device token itself,
	// which lives in AccessToken there; a pasted-token account has only its job
	// token, and no second token to rotate.
	rotatable := strings.TrimSpace(credential.DeviceRefresh) != ""
	return strings.TrimSpace(credential.AccessToken), rotatable
}

// qoderCampaignCall asks one campaigns URL as the account's desktop client.
//
// The six machine headers go out when the native helper can name this machine,
// and are left off when it cannot: a request without them is answered, while a
// request carrying a made-up identity is filtered. The site that does not ask for
// them is asked without them.
func qoderCampaignCall(ctx context.Context, site qoderSite, method, endpoint, bearer string, credential domain.QoderCredential, dst any) (int, error) {
	machine, _ := qoderCampaignMachineIdentity(ctx, site, credential)
	return qoderCampaignDo(ctx, method, endpoint, qoderCampaignHeaders(site, machine), bearer, dst)
}

// qoderCampaignDo sends one request with the given headers.
func qoderCampaignDo(ctx context.Context, method, endpoint string, headers map[string]string, bearer string, dst any) (int, error) {
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader("{}")
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return 0, err
	}
	for key, value := range headers {
		if value == "" {
			continue
		}
		request.Header.Set(key, value)
	}
	// The account's own bearer stays authoritative: the header builders describe
	// the client, not the credential.
	request.Header.Set("Authorization", "Bearer "+bearer)
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, &qoderError{Message: qoderErrorMessage(payload, response.StatusCode), Status: response.StatusCode}
	}
	if dst == nil {
		return response.StatusCode, nil
	}
	if err := json.Unmarshal(payload, dst); err != nil {
		return response.StatusCode, fmt.Errorf("Qoder 的签到接口返回了读不出来的内容（HTTP %d）", response.StatusCode)
	}
	return response.StatusCode, nil
}

// qoderCampaignHeaders is what Qoder's desktop client sends to the campaign
// pages: the app's own client identity, plus this machine's identity when the
// native helper could name it.
func qoderCampaignHeaders(site qoderSite, machine map[string]string) map[string]string {
	headers := map[string]string{
		"Accept":          "application/json",
		"User-Agent":      "Qoder",
		"Cosy-ClientType": qoderClientTypeDesktop,
		// The campaign pages are served to the app, whose version is its own —
		// not the CLI version the inference calls claim.
		"Cosy-Version": qoderDesktopVersion,
	}
	for key, value := range machine {
		headers[key] = value
	}
	return headers
}

// qoderDesktopVersion is the app version the campaign pages are served to.
const qoderDesktopVersion = "0.4.8"

// qoderAccountHeaders is the plain account identity: the one the check-in, quota
// and userinfo endpoints take. It carries no Cosy headers at all — the account
// pages and the app pages are two different clients to Qoder.
func qoderAccountHeaders(site qoderSite, credential domain.QoderCredential) map[string]string {
	uid := strings.TrimSpace(credential.UID)
	referer := strings.TrimRight(site.Web, "/") + "/"
	return map[string]string{
		"Accept":     "application/json, text/plain, */*",
		"User-Agent": qoderAccountUserAgent(site),
		"Origin":     site.Web,
		"Referer":    referer,
		// A fresh request id per call, and ids derived from the account so the
		// same account looks like the same device on every call.
		"X-Request-ID": randomHex(16),
		"X-Machine-ID": qoderDeriveID(uid, "machine"),
		"X-Session-ID": qoderDeriveID(uid, "session"),
	}
}

// qoderAccountUserAgent is the user agent the account endpoints are served to:
// the domestic client calls itself QoderWork, the international one Qoder.
func qoderAccountUserAgent(site qoderSite) string {
	if site.AuthType == domain.AuthTypeQoderCN {
		return "QoderWork/" + qoderCOSYVersion
	}
	return "Qoder/" + qoderCOSYVersion
}

// ---- the device identity Qoder's international site asks for ---------------

var qoderMachineMu sync.Mutex

// qoderMachineIdentities holds the last good read per uid for qoderMachineTTL.
var qoderMachineIdentities = map[string]qoderMachineCacheEntry{}

type qoderMachineCacheEntry struct {
	headers map[string]string
	at      time.Time
}

// printableHeaderValue keeps a helper's answer inside what a header may hold:
// only visible ASCII, at most 4 KiB. Anything else is a broken or hostile
// binary, and it is refused rather than sent upstream.
var printableHeaderValue = regexp.MustCompile(`^[\x20-\x7e]+$`)

// qoderCampaignMachineIdentity is this machine's identity headers for the site
// the account belongs to, or nothing at all on the site that does not ask.
//
// Qoder CN's campaign pages are served without a machine identity. The
// international site prefers one, but a request without it is answered too — a
// headless server has no Qoder desktop install to read one from, and refusing to
// ask at all is how check-in used to fail on every Linux box. The second return
// says why the identity is missing, so an empty campaign list can explain itself
// instead of looking like an account with nothing to claim.
func qoderCampaignMachineIdentity(ctx context.Context, site qoderSite, credential domain.QoderCredential) (map[string]string, string) {
	if site.AuthType != domain.AuthTypeQoderPAT {
		return nil, ""
	}
	headers, err := qoderMachineIdentity(ctx, strings.TrimSpace(credential.UID))
	if err != nil {
		return nil, err.Error()
	}
	return headers, ""
}

// qoderMachineIdentity is this machine's identity as Qoder's native helper reads
// it: the token, its type, the machine code, plus the OS and hostname the client
// reports alongside them. The uid goes over stdin, never on the command line.
//
// All six travel together because they only mean something together: a request
// carrying some of a machine's identity is neither this machine nor a derived
// one, and the vendor treats it as a client it did not ship.
func qoderMachineIdentity(ctx context.Context, uid string) (map[string]string, error) {
	if strings.TrimSpace(uid) == "" {
		return nil, errors.New("Qoder 签到要按账号 uid 读本机设备身份，请重新登录")
	}
	qoderMachineMu.Lock()
	defer qoderMachineMu.Unlock()
	if cached, ok := qoderMachineIdentities[uid]; ok && time.Since(cached.at) < qoderMachineTTL {
		return cached.headers, nil
	}
	helper, err := qoderRuntimeInfoPath()
	if err != nil {
		return nil, err
	}
	args := []string{"3", "--account-stdin"}
	if runtime.GOOS == "linux" {
		// Qoder's CLI on Linux has no stdin variant: it takes the environment
		// number alone and reads the signed-in account by itself.
		args = []string{"3"}
	}
	readCtx, cancel := context.WithTimeout(ctx, qoderMachineTimeout)
	defer cancel()
	command := exec.CommandContext(readCtx, helper, args...)
	var stdout strings.Builder
	command.Stdout = &stdout
	if runtime.GOOS != "linux" {
		if payload, marshalErr := json.Marshal(map[string]string{"account": uid}); marshalErr == nil {
			command.Stdin = strings.NewReader(string(payload) + "\n")
		}
	}
	if runErr := command.Run(); runErr != nil {
		return nil, errors.New("Qoder 签到：本机 Qoder 的设备身份读不出来，请重启或更新 Qoder")
	}
	line := strings.SplitN(strings.TrimSpace(stdout.String()), "\n", 2)[0]
	var identity struct {
		MachineToken string `json:"machineToken"`
		MachineType  string `json:"machineType"`
		MachineCode  string `json:"machineCode"`
	}
	if json.Unmarshal([]byte(line), &identity) != nil {
		return nil, errors.New("Qoder 签到：本机 Qoder 给的设备身份读不出来，请重启或更新 Qoder")
	}
	token := strings.TrimSpace(identity.MachineToken)
	kind := strings.TrimSpace(identity.MachineType)
	code := strings.TrimSpace(identity.MachineCode)
	if token == "" || kind == "" || len(token) > 4096 || len(kind) > 4096 ||
		!printableHeaderValue.MatchString(token) || !printableHeaderValue.MatchString(kind) {
		return nil, errors.New("Qoder 签到：本机 Qoder 给的设备身份不合法，请重启或更新 Qoder")
	}
	if code == "" || len(code) > 4096 || !printableHeaderValue.MatchString(code) {
		// The code is derived from the uid when the helper does not name one,
		// which keeps the six headers coherent instead of five-and-a-half.
		code = qoderDeriveID(uid, "machinecode")
	}
	headers := map[string]string{
		"Cosy-MachineId":       qoderDeriveID(uid, "machine"),
		"Cosy-MachineToken":    token,
		"Cosy-MachineType":     kind,
		"Cosy-MachineCode":     code,
		"Cosy-MachineOS":       qoderMachineOS(),
		"Cosy-MachineHostname": qoderLocalHostname(),
	}
	qoderMachineIdentities[uid] = qoderMachineCacheEntry{headers: headers, at: time.Now()}
	return headers, nil
}

// qoderLocalHostname is this machine's own name, normalised the way the vendor
// client sends it: visible ASCII only, trimmed to what a header takes. An empty
// answer means the header is left out rather than filled with a guess.
func qoderLocalHostname() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	name = strings.TrimSpace(name)
	// Header values are Latin-1 here: anything else is folded to a dash so a
	// machine named in another script still produces a legal value.
	var cleaned strings.Builder
	for _, char := range name {
		if char >= 0x20 && char <= 0x7e {
			cleaned.WriteRune(char)
			continue
		}
		cleaned.WriteByte('-')
	}
	out := strings.Trim(cleaned.String(), "- ")
	if len(out) > 96 {
		out = strings.TrimRight(out[:96], "- ")
	}
	return out
}

// qoderRuntimeInfoPath finds Qoder's native device-identity helper: the desktop
// app's bundle, or the copy Qoder's CLI left in its cache when it ran.
// GATEWAY_QODER_RUNTIME_INFO / QODER_RUNTIME_INFO point at it by hand.
func qoderRuntimeInfoPath() (string, error) {
	for _, key := range []string{"GATEWAY_QODER_RUNTIME_INFO", "QODER_RUNTIME_INFO"} {
		given := strings.TrimSpace(os.Getenv(key))
		if given == "" {
			continue
		}
		if regularFile(given) {
			return given, nil
		}
		return "", fmt.Errorf("Qoder 签到：%s 指的文件不存在（%s）", key, given)
	}
	name := "runtime-info"
	if runtime.GOOS == "windows" {
		name = "runtime-info.exe"
	}
	home, _ := os.UserHomeDir()
	for _, candidate := range qoderHelperInstallPaths(home, name) {
		if regularFile(candidate) {
			return candidate, nil
		}
	}
	if cached, err := qoderHelperCachedPath(home, name); err == nil {
		return cached, nil
	}
	return "", errors.New("Qoder 国际版签到要用本机 Qoder 客户端读设备身份，这台机器上没找到它（装 Qoder 桌面版或跑一次 Qoder CLI 即可，" +
		"也可把 QODER_RUNTIME_INFO 指到 runtime-info 可执行文件）")
}

func qoderHelperInstallPaths(home, name string) []string {
	switch runtime.GOOS {
	case "windows":
		local := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
		if local == "" && home != "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		return []string{filepath.Join(local, "Programs", "Qoder", "resources", "umid", name)}
	case "darwin":
		out := []string{}
		for _, dir := range []string{"/Applications", filepath.Join(home, "Applications")} {
			out = append(out, filepath.Join(dir, "Qoder.app", "Contents", "Resources", "umid", name))
		}
		return out
	case "linux":
		return []string{filepath.Join("/opt", "Qoder", "resources", "umid", name)}
	}
	return nil
}

// qoderHelperCachedPath is the copy Qoder's CLI leaves behind: a
// umid-<os>-<arch>-<hash> folder on Windows, a runtime-info-<os>-<arch>-<hash>
// file elsewhere, under <config>/.bin. The newest one wins, because a CLI update
// leaves the copy it replaced in place.
func qoderHelperCachedPath(home, name string) (string, error) {
	config := strings.TrimSpace(os.Getenv("QODER_CONFIG_DIR"))
	if config == "" {
		root := strings.TrimSpace(os.Getenv("QODER_CLI_HOME"))
		if root == "" {
			root = home
		}
		if root == "" {
			return "", errors.New("Qoder 签到：找不到本机设备身份 helper")
		}
		suffix := strings.TrimSpace(os.Getenv("QODER_CONFIG_DIR_NAME"))
		if suffix == "" {
			suffix = ".qoder"
		}
		config = filepath.Join(root, suffix)
	}
	entries, err := os.ReadDir(filepath.Join(config, ".bin"))
	if err != nil {
		return "", err
	}
	prefix := qoderHelperPrefix(name)
	type found struct {
		path string
		mod  time.Time
	}
	hits := []found{}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		path := filepath.Join(config, ".bin", entry.Name())
		if runtime.GOOS == "windows" {
			path = filepath.Join(path, name)
		} else if strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}
		if !regularFile(path) {
			continue
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			continue
		}
		hits = append(hits, found{path: path, mod: info.ModTime()})
	}
	if len(hits) == 0 {
		return "", errors.New("Qoder 签到：找不到本机设备身份 helper")
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].mod.After(hits[j].mod) })
	return hits[0].path, nil
}

// qoderHelperPrefix names the helper the way Qoder's CLI writes it, in node's os
// and arch spellings, which differ from Go's ("win32", "x64").
func qoderHelperPrefix(name string) string {
	goos := runtime.GOOS
	if goos == "windows" {
		goos = "win32"
	}
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	if runtime.GOOS == "windows" {
		return "umid-" + goos + "-" + arch + "-"
	}
	return name + "-" + goos + "-" + arch + "-"
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
