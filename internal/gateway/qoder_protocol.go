package gateway

// Qoder's subscription protocol, ported from the Qoder support that magpie
// ships (internal/qoder, which ports CLIProxyAPI's reverse-engineered client,
// MIT). A Qoder account is served through the API Qoder's own desktop client
// talks to — not through a documented OpenAI-compatible endpoint — so every
// call has to carry the client's COSY envelope and its request body has to go
// out in the client's body codec.
//
// Qoder runs two sites whose accounts exist only on their own: qoder.com
// (Google/GitHub/email sign-in) and qoder.cn, "Qoder CN" (Alibaba Cloud or a
// phone number). Same protocol, different hosts and a different device-flow
// client id, hence the two connection kinds.

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Qoder's endpoints, relative to the site's three hosts (web = the sign-in
// page, openAPI = accounts and tokens, api = models and chat).
const (
	qoderDeviceSelectPath  = "/device/selectAccounts"
	qoderDevicePollPath    = "/api/v1/deviceToken/poll"
	qoderDeviceRefreshPath = "/api/v1/deviceToken/refresh"
	qoderJobTokenPath      = "/api/v1/me/jobToken"
	qoderJobRefreshPath    = "/api/v1/jobToken/refresh"
	qoderJobExchangePath   = "/api/v1/jobToken/exchange"
	qoderUserInfoPath      = "/api/v1/userinfo"
	qoderUsagePath         = "/sash/api/v2/me/usage"

	// qoderChatPath asks for the LLM result to be fetched, names the common
	// agent, and Encode=1 says the request body arrives in Qoder's own codec.
	// The reply is plain server-sent events: only the request is encoded.
	qoderChatPath   = "/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"
	qoderModelsPath = "/algo/api/v2/model/list?Encode=1"

	// qoderCOSYVersion is the client version the envelope claims to be. The
	// signature covers it, so it must match what the endpoint expects, and a
	// version left too far behind comes back as Qoder's 430 "upgrade" refusal
	// whatever the rest of the envelope looks like.
	qoderCOSYVersion = "1.1.75"

	// Qoder carries two client identities over the same COSY envelope: the CLI
	// (5), which inference and the model list are served to, and the desktop app
	// (10), which only the credit and campaign endpoints name. Inferring with
	// the app's identity is not the same request as far as the endpoint is
	// concerned — the CLI one is what the model list is authorized for.
	qoderClientTypeCLI = "5"
	// qoderClientTypeDesktop is what the credit/campaign link claims to be (see
	// qoder_checkin.go, which sends it itself).
	qoderClientTypeDesktop = "10"

	// qoderClientIP is what Cosy-ClientIp carries. Qoder's client sends one
	// fixed link-local address there rather than this machine's own, and the
	// field is a string: a device id in it reads as a malformed client.
	qoderClientIP = "169.254.198.161"

	// qoderInferenceUserAgent is what the official CLI puts in User-Agent: it
	// sets none itself, so its HTTP library's default is what reaches Qoder.
	qoderInferenceUserAgent = "undici"

	// qoderDefaultUserType is the identity blob's user_type when the account
	// does not name one, as Qoder's own client falls back.
	qoderDefaultUserType = "personal_professional_trial"
)

// qoderSite is one Qoder deployment.
type qoderSite struct {
	// AuthType is the connection kind a provider of this site is stored under.
	AuthType string
	Name     string
	Web      string
	OpenAPI  string
	API      string
	// APIFallbacks are the site's other inference hosts, tried in order when the
	// primary cannot be reached at the transport layer. Qoder's international
	// deployment runs several gateways behind one name, and a TLS hiccup on one
	// is not this account's problem.
	APIFallbacks []string
	// ClientID is the device-flow client the sign-in names. Qoder CN's is the
	// one Qoder CN's own CLI sends, because Qoder CN may not have registered
	// the desktop client's id.
	ClientID string
	// RedirectURI goes on the authorization page; Qoder CN's CLI sends none.
	RedirectURI string
	// DeviceChatFallback marks a site where a refused job-token exchange is
	// expected rather than fatal: Qoder CN's CLI chats on the device token
	// itself, never making a job token, so an account there can work the same
	// way when qoder.cn won't trade the device token (a 4xx).
	DeviceChatFallback bool
}

var (
	qoderGlobalSite = qoderSite{
		AuthType:     domain.AuthTypeQoderPAT,
		Name:         "Qoder",
		Web:          "https://qoder.com",
		OpenAPI:      "https://openapi.qoder.sh",
		API:          "https://api3.qoder.sh",
		APIFallbacks: []string{"https://api2.qoder.sh", "https://api1.qoder.sh"},
		ClientID:     "732aef47-9cf2-46a2-95fe-4cebb5d0d1fa",
		RedirectURI:  "qoder-app://",
	}
	qoderCNSite = qoderSite{
		AuthType:           domain.AuthTypeQoderCN,
		Name:               "Qoder CN",
		Web:                "https://qoder.cn",
		OpenAPI:            "https://openapi.qoder.com.cn",
		API:                "https://gateway.qoder.com.cn",
		ClientID:           "e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb",
		DeviceChatFallback: true,
	}
)

// qoderHosts is the site's inference hosts in the order they are tried: the
// primary first, then whatever the deployment offers behind it. Qoder CN has
// one gateway and no fallback, so a failure there is final.
func (s qoderSite) qoderHosts() []string {
	var hosts []string
	seen := map[string]bool{}
	for _, host := range append([]string{s.API}, s.APIFallbacks...) {
		host = strings.TrimRight(strings.TrimSpace(host), "/")
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		hosts = append(hosts, host)
	}
	return hosts
}

func qoderSiteForAuthType(authType string) (qoderSite, bool) {
	switch authType {
	case domain.AuthTypeQoderPAT:
		return qoderGlobalSite, true
	case domain.AuthTypeQoderCN:
		return qoderCNSite, true
	default:
		return qoderSite{}, false
	}
}

// qoderSiteOf is the site a provider signs in on. Anything that is not the CN
// kind is the global site, matching qoderSiteForAuthType's default.
func qoderSiteOf(provider domain.Provider) qoderSite {
	site, _ := qoderSiteForAuthType(provider.AuthType)
	return site
}

func isQoderAuthType(authType string) bool {
	_, ok := qoderSiteForAuthType(authType)
	return ok
}

func isQoderProvider(provider domain.Provider) bool {
	return isQoderAuthType(provider.AuthType)
}

// qoderChatURL is the site's inference endpoint.
func (s qoderSite) qoderChatURL() string { return s.API + qoderChatPath }

// qoderModelsURL is the site's model-listing endpoint.
func (s qoderSite) qoderModelsURL() string { return s.API + qoderModelsPath }

// qoderPost signs and sends one already-encoded body to endpoint, and walks the
// site's inference hosts when the transport itself fails.
//
// Only a transport failure moves on to the next host: an HTTP answer — a
// refusal, a rate limit, a version gate — came from Qoder and would come from
// every host the same way. The signature covers the path alone, so a different
// host changes nothing about the envelope.
func qoderPost(ctx context.Context, site qoderSite, path, wire, modelKey, modelSource string, account qoderAccount) (*http.Response, error) {
	var lastErr error
	hosts := site.qoderHosts()
	if len(hosts) == 0 {
		return nil, errors.New("qoder: 该站点没有可用的推理主机")
	}
	for _, host := range hosts {
		response, err := qoderPostOnce(ctx, host+path, wire, modelKey, modelSource, account)
		if err == nil {
			return response, nil
		}
		lastErr = err
		// A canceled request is the client's own doing: no host will answer it.
		if ctx.Err() != nil {
			break
		}
	}
	return nil, lastErr
}

// qoderPostOnce is one signed attempt at one host.
func qoderPostOnce(ctx context.Context, endpoint, wire, modelKey, modelSource string, account qoderAccount) (*http.Response, error) {
	headers, err := qoderCOSYHeaders(endpoint, account, wire, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(wire))
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Cache-Control", "no-cache")
	request.Header.Set("X-Model-Key", modelKey)
	if modelSource != "" {
		request.Header.Set("X-Model-Source", modelSource)
	}
	return http.DefaultClient.Do(request)
}

// qoderRSAPublicKeyPEM is Qoder's embedded 1024-bit RSA public key, dumped from
// the client's COSY source. It wraps the per-request AES key that encrypts the
// account blob inside the Authorization header — the endpoint checks the
// envelope, so a made-up key yields 403 rather than a useful error.
const qoderRSAPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

var qoderRSAKey = mustQoderRSAKey()

func mustQoderRSAKey() *rsa.PublicKey {
	block, _ := pem.Decode([]byte(qoderRSAPublicKeyPEM))
	if block == nil {
		panic("qoder: failed to decode embedded RSA public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		panic("qoder: parse RSA public key: " + err.Error())
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		panic("qoder: embedded key is not an RSA public key")
	}
	return key
}

// qoderAccount is the signed-in account a COSY envelope is built for.
type qoderAccount struct {
	UID       string
	Name      string
	Email     string
	Token     string
	MachineID string
	// RefreshToken, UserType and the two organization fields exist only inside
	// the encrypted identity blob. Qoder's client always writes all nine keys
	// of it, so the blob is built with them even when an account fills none in.
	RefreshToken     string
	UserType         string
	OrganizationID   string
	OrganizationName string
}

// qoderCOSYHeaders signs a call to rawURL whose body is already in its wire
// (codec) form; an empty body is what a GET signs.
//
// The device identity in it is derived from the uid rather than stored: Qoder's
// own client reports a machine id, machine token, machine type and hostname
// that are all functions of the account, and the endpoint reads them as one
// coherent machine. A random id per machine, or the same value in every one of
// those fields, is what a client Qoder has never shipped looks like.
func qoderCOSYHeaders(rawURL string, account qoderAccount, wireBody string, timestamp int64) (map[string]string, error) {
	if account.UID == "" || strings.TrimSpace(account.Token) == "" {
		return nil, fmt.Errorf("qoder: 签名缺少 uid 或令牌")
	}
	if timestamp == 0 {
		timestamp = time.Now().Unix()
	}
	info, key, err := qoderUserBlob(account)
	if err != nil {
		return nil, err
	}
	payloadJSON, err := qoderCompactJSON(map[string]any{
		"version":     "v1",
		"requestId":   newUUIDV4(),
		"info":        info,
		"cosyVersion": qoderCOSYVersion,
		"ideVersion":  "",
	})
	if err != nil {
		return nil, fmt.Errorf("qoder: marshal COSY payload: %w", err)
	}
	payload := base64.StdEncoding.EncodeToString(payloadJSON)
	signature := qoderCOSYSignature(payload, key, timestamp, wireBody, qoderSignaturePath(rawURL))
	// The machine id this account signed in with stays its own; an account
	// stored before one was kept falls back to the derived value so the
	// envelope is never left without one.
	machineID := strings.TrimSpace(account.MachineID)
	if machineID == "" {
		machineID = qoderDeriveID(account.UID, "machine")
	}
	return map[string]string{
		"Accept":                "application/json",
		"Accept-Encoding":       "identity",
		"Content-Type":          "application/json",
		"Authorization":         "Bearer COSY." + payload + "." + signature,
		"Cosy-Business-Product": "cli",
		"Cosy-Business-Type":    "agent",
		"Cosy-ClientIp":         qoderClientIP,
		"Cosy-ClientType":       qoderClientTypeCLI,
		"Cosy-Data-Policy":      "agree",
		"Cosy-Date":             fmt.Sprintf("%d", timestamp),
		"Cosy-Key":              key,
		"Cosy-MachineHostname":  qoderDeriveMachineHostname(account.UID),
		"Cosy-MachineId":        machineID,
		"Cosy-MachineToken":     qoderDeriveMachineToken(account.UID),
		"Cosy-MachineType":      qoderDeriveMachineType(account.UID),
		"Cosy-MachineOS":        qoderMachineOS(),
		"Cosy-Scene":            "assistant",
		"Cosy-User":             account.UID,
		"Cosy-Version":          qoderCOSYVersion,
		"Login-Version":         "v2",
		"User-Agent":            qoderInferenceUserAgent,
	}, nil
}

// qoderSignaturePath is the path the signature covers: no query, and no
// leading /algo — the gateway host serves /algo/x but signs it as /x.
func qoderSignaturePath(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return strings.TrimSpace(rawURL)
	}
	path := parsed.Path
	if strings.HasPrefix(path, "/algo") {
		path = strings.TrimPrefix(path, "/algo")
	}
	return path
}

func qoderCOSYSignature(payload, key string, timestamp int64, wireBody, path string) string {
	sum := md5.Sum([]byte(fmt.Sprintf("%s\n%s\n%d\n%s\n%s", payload, key, timestamp, wireBody, path)))
	return hex.EncodeToString(sum[:])
}

// qoderCosySession is one account's COSY key pair: the AES key that encrypts
// its identity blob, that key wrapped by Qoder's public key, and the blob
// itself. Qoder's client builds it once per signed-in session and reuses it for
// every request, so only the date, the request id and the signature change
// between frames.
type qoderCosySession struct {
	// token is the access token the pair was built for: a rotated token means
	// the identity blob carries a token the endpoint no longer knows, so the
	// pair has to be rebuilt rather than reused.
	token string
	info  string
	key   string
}

var qoderCosySessions sync.Map // uid -> qoderCosySession

// qoderUserBlob is the account as the envelope carries it: JSON encrypted with
// AES-128-CBC under a fresh key that doubles as its own IV, that key then
// wrapped with Qoder's RSA public key. Both halves come back base64'd, and both
// are reused until the access token changes.
func qoderUserBlob(account qoderAccount) (info string, key string, err error) {
	if cached, ok := qoderCosySessions.Load(account.UID); ok {
		if session, ok := cached.(qoderCosySession); ok && session.token == account.Token {
			return session.info, session.key, nil
		}
	}
	userType := strings.TrimSpace(account.UserType)
	if userType == "" {
		userType = qoderDefaultUserType
	}
	// All nine keys Qoder's client writes, in the order its own encoder sorts
	// them. A blob missing the optional ones still decrypts, but its shape no
	// longer matches what the client produces.
	raw, err := qoderCompactJSON(map[string]any{
		"aid":                  account.UID,
		"name":                 account.Name,
		"organization_id":      account.OrganizationID,
		"organization_name":    account.OrganizationName,
		"refresh_token":        account.RefreshToken,
		"security_oauth_token": account.Token,
		"uid":                  account.UID,
		"user_type":            userType,
		"yx_uid":               "",
	})
	if err != nil {
		return "", "", fmt.Errorf("qoder: marshal user blob: %w", err)
	}
	// The AES key doubles as the IV, and is 16 hex characters because that is
	// the length the client's key has.
	aesKey := []byte(randomHex(8))
	if len(aesKey) != aes.BlockSize {
		return "", "", fmt.Errorf("qoder: 生成 COSY 的 AES 密钥失败")
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return "", "", fmt.Errorf("qoder: aes cipher: %w", err)
	}
	padLen := aes.BlockSize - len(raw)%aes.BlockSize
	padded := append(append([]byte{}, raw...), bytes.Repeat([]byte{byte(padLen)}, padLen)...)
	encrypted := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, aesKey).CryptBlocks(encrypted, padded)
	wrapped, err := rsa.EncryptPKCS1v15(rand.Reader, qoderRSAKey, aesKey)
	if err != nil {
		return "", "", fmt.Errorf("qoder: rsa encrypt key: %w", err)
	}
	info = base64.StdEncoding.EncodeToString(encrypted)
	key = base64.StdEncoding.EncodeToString(wrapped)
	qoderCosySessions.Store(account.UID, qoderCosySession{token: account.Token, info: info, key: key})
	return info, key, nil
}

// qoderCompactJSON is the JSON Qoder's client signs: no HTML escaping and no
// trailing newline. Go's encoder escapes <, > and & by default, which would
// put different bytes under the signature than the client hashes whenever an
// account's name or an organization carries one.
func qoderCompactJSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// qoderDeriveID is the device identity Qoder's client reports: the account's
// uid under a salt, hashed and printed in UUIDv4 shape so it reads as a machine
// id. Every account gets its own, and the same account gets the same one for
// as long as its uid lives.
func qoderDeriveID(uid, salt string) string {
	subject := strings.TrimSpace(uid)
	if subject == "" {
		subject = "anonymous"
	}
	sum := md5.Sum([]byte(salt + ":" + subject))
	shaped := hex.EncodeToString(sum[:])
	// version 4 in the high nibble of the seventh byte, variant a in the ninth.
	shaped = shaped[:12] + "4" + shaped[13:16] + "a" + shaped[17:]
	return shaped[0:8] + "-" + shaped[8:12] + "-" + shaped[12:16] + "-" + shaped[16:20] + "-" + shaped[20:32]
}

// qoderDeriveMachineToken is the machine token Qoder's client reports: a
// url-safe base64 SHA-512 of the uid, cut to the 43 characters one base64 block
// of a 256-bit digest occupies.
func qoderDeriveMachineToken(uid string) string {
	subject := strings.TrimSpace(uid)
	if subject == "" {
		subject = "anonymous"
	}
	sum := sha512.Sum512([]byte("machinetoken:" + subject))
	token := base64.RawURLEncoding.EncodeToString(sum[:])
	if len(token) > 43 {
		token = token[:43]
	}
	return token
}

// qoderDeriveMachineType is the 18-character machine type the client reports.
func qoderDeriveMachineType(uid string) string {
	shaped := strings.ReplaceAll(qoderDeriveID(uid, "machinetype"), "-", "")
	if len(shaped) > 18 {
		shaped = shaped[:18]
	}
	return shaped
}

// qoderDeriveMachineHostname is the hostname the inference call reports:
// "DESKTOP-" and seven uppercase hex digits, stable per account and different
// between accounts, the way one machine serving several accounts is not.
func qoderDeriveMachineHostname(uid string) string {
	subject := strings.TrimSpace(uid)
	if subject == "" {
		subject = "anonymous"
	}
	sum := md5.Sum([]byte("hostname:" + subject))
	return "DESKTOP-" + strings.ToUpper(hex.EncodeToString(sum[:])[:7])
}

// qoderBodyAlphabet is Qoder's shuffled base64 alphabet, dumped from a live
// client. '_' is index 0 and '!' index 63; '$' pads without carrying data.
const qoderBodyAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"

// qoderEncodeBody is the wire form of a request body: standard base64
// grouping (four chars per three bytes, MSB-first bit stream) through Qoder's
// alphabet, padded to a multiple of four with '$', then its first and last
// thirds swapped. The endpoint decodes the same way and rejects anything else.
func qoderEncodeBody(plaintext []byte) string {
	var out strings.Builder
	var acc, bits int
	for _, b := range plaintext {
		acc = (acc << 8) | int(b)
		bits += 8
		for bits >= 6 {
			bits -= 6
			out.WriteByte(qoderBodyAlphabet[(acc>>bits)&0x3F])
		}
	}
	if bits > 0 {
		out.WriteByte(qoderBodyAlphabet[(acc<<(6-bits))&0x3F])
	}
	for out.Len()%4 != 0 {
		out.WriteByte('$')
	}
	encoded := out.String()
	third := len(encoded) / 3
	if third == 0 {
		return encoded
	}
	return encoded[len(encoded)-third:] + encoded[third:len(encoded)-third] + encoded[:third]
}

// qoderMachineOS names this machine the way Qoder's desktop clients do:
// Windows is "win32", and the architectures carry their own spellings.
func qoderMachineOS() string {
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "386":
		arch = "x86"
	case "arm64":
		arch = "aarch64"
	}
	os := runtime.GOOS
	if os == "windows" {
		os = "win32"
	}
	return arch + "_" + os
}
