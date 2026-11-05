package gateway

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// The single exit every WorkBuddy request goes out through.
//
// The reference gateway converges all thirteen outbound sites on one function
// (`Client.do`) so that three things can never be forgotten: the header
// spelling fix, the explicit Connection header, and the per-account connection
// pool. This file does the same job for this gateway, plus the two layers that
// live below HTTP — the reproduced TLS ClientHello and the header reorder.
//
// Why per-account pools matter here: Go's default transport pools connections
// process-wide, so two accounts taking turns would be seen on one TCP/TLS
// connection carrying two different Authorization headers. The reference
// gateway names that a cross-account correlation clue and keys its pool by
// credential digest so it can never happen ("同一个出口上的两个账号绝不共用一条
// TCP/TLS 连接", wb_upstream_pool.py). Chat already had its own transport; the
// account-facing surface (billing, growth tasks, report, check-in) did not — it
// went out through http.DefaultClient and shared one pool for every account on
// the box.

const (
	// workBuddyDialTimeout bounds establishing the TCP connection.
	workBuddyDialTimeout = 10 * time.Second
	// workBuddyDialKeepAlive is the TCP keepalive probe interval.
	workBuddyDialKeepAlive = 15 * time.Second
	// workBuddyTLSHandshakeTimeout lives in workbuddy_tls.go, next to the hook
	// that has to enforce it.
	//
	// workBuddyIdleConnTimeout is how long an idle connection stays pooled.
	// Shorter than Go's 90s default: after a WAF burst the upstream likes to drop
	// long-idle sockets, and a reused-but-dead connection costs a request.
	workBuddyIdleConnTimeout = 30 * time.Second
	// Pool sizes: a handful of concurrent turns plus their returns.
	workBuddyMaxIdleConns        = 100
	workBuddyMaxIdleConnsPerHost = 20
	// workBuddyExpectContinueTimeout mirrors the default; kept explicit so the
	// whole transport shape is readable from one place.
	workBuddyExpectContinueTimeout = 1 * time.Second
)

// workBuddyTransportSpec identifies one connection pool.
//
// The proxy and the fingerprint profile are part of the identity: changing
// either changes the bytes on the wire, so it must not reuse a pool built under
// the old setting.
type workBuddyTransportSpec struct {
	providerID string
	userID     string
	proxyURL   string
	mode       string
}

// newWorkBuddyTransport builds one account-scoped transport.
//
// Hardening, all of it measured rather than guessed:
//   - h2 truly off: an empty (non-nil) TLSNextProto map is what does it.
//     ForceAttemptHTTP2=false alone is not enough — Go still logs h2 timeouts
//     because the map stays nil and the default h2 transport gets installed.
//   - TLS handshake bounded (the hook in workbuddy_tls.go, since DialTLSContext
//     bypasses Transport.TLSHandshakeTimeout).
//   - TCP connect bounded and kept alive, so a half-open socket cannot sit in
//     the pool unnoticed.
//
// The dial hooks are where the header order lands: Transport writes plaintext
// HTTP into the connection these return, so the reorder sees real header bytes.
func newWorkBuddyTransport(spec workBuddyTransportSpec) *http.Transport {
	dialer := &net.Dialer{Timeout: workBuddyDialTimeout, KeepAlive: workBuddyDialKeepAlive}
	mode := spec.mode
	if mode == "" {
		mode = workBuddyTLSFingerprintMode()
	}

	transport := &http.Transport{
		Proxy: workBuddyProxyFunc(spec.proxyURL),
		// Plain HTTP (tests, and any operator pointing a provider at an internal
		// mirror) still gets the header reorder.
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return newWorkBuddyHeadOrderConn(conn), nil
		},
		DialTLSContext:        workBuddyTLSDialer(dialer, nil, mode),
		TLSNextProto:          make(map[string]func(authority string, conn *tls.Conn) http.RoundTripper),
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          workBuddyMaxIdleConns,
		MaxIdleConnsPerHost:   workBuddyMaxIdleConnsPerHost,
		IdleConnTimeout:       workBuddyIdleConnTimeout,
		ExpectContinueTimeout: workBuddyExpectContinueTimeout,
	}
	return transport
}

// workBuddyProxyFunc turns a provider's proxy setting into the transport hook.
//
// An unparsable value falls back to the environment rather than failing the
// request: a typo in one provider's row must not take that account's chat path
// down entirely.
func workBuddyProxyFunc(raw string) func(*http.Request) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return http.ProxyFromEnvironment
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return http.ProxyFromEnvironment
	}
	return http.ProxyURL(parsed)
}

// workBuddyRequestExit is the one place a WorkBuddy request is finished before
// it leaves: header spelling, then the explicit Connection header.
//
// Spelling first on every path: Go canonicalises header keys, while the
// client's wire bytes are its own literals (see
// officialWorkBuddyHeaderSpelling). A copy is returned, so callers and tests
// still read the original request back.
func workBuddyRequestExit(request *http.Request) *http.Request {
	if request == nil {
		return nil
	}
	normalized := normalizeWorkBuddyHeaderSpelling(request)
	applyWorkBuddyConnectionHeader(normalized)
	return normalized
}

// workBuddyAuxClientCache holds one transport per (host, account) for the
// account-facing surface — billing, growth, report, check-in — which has no
// provider in hand at the call site.
var workBuddyAuxClientCache sync.Map // workBuddyAuxKey -> *http.Transport

type workBuddyAuxKey struct {
	host    string
	account string
}

// workBuddyAuxTransport returns the transport for one host and account.
//
// The account segment is a digest, never the raw id or token: this map lives for
// the process lifetime and must not become a secret store. Two accounts never
// share an entry, which is the whole point — the same rule chat's per-account
// pool follows.
func workBuddyAuxTransport(host, account string) *http.Transport {
	key := workBuddyAuxKey{host: strings.ToLower(strings.TrimSpace(host)), account: account}
	if cached, ok := workBuddyAuxClientCache.Load(key); ok {
		if transport, typed := cached.(*http.Transport); typed {
			return transport
		}
	}
	fresh := newWorkBuddyTransport(workBuddyTransportSpec{providerID: key.host, userID: key.account})
	actual, _ := workBuddyAuxClientCache.LoadOrStore(key, fresh)
	if existing, ok := actual.(*http.Transport); ok {
		return existing
	}
	return fresh
}

// workBuddyRequestClient picks the client a request goes out on, deriving the
// account from the request itself.
//
// Deriving rather than taking a parameter keeps every existing call site
// unchanged: the request already carries the account (X-User-Id, or the bearer
// when that is missing), and the host decides the rest. An anonymous request
// still gets its own pool — one per host — rather than the process-wide default.
func workBuddyRequestClient(request *http.Request) *http.Client {
	if request == nil || request.URL == nil {
		return http.DefaultClient
	}
	account := workBuddyAccountDigest(request)
	transport := workBuddyAuxTransport(request.URL.Host, account)
	return &http.Client{Transport: transport}
}

// workBuddyAccountDigest derives the pool key for a request's account.
func workBuddyAccountDigest(request *http.Request) string {
	identifier := strings.TrimSpace(request.Header.Get("X-User-Id"))
	if identifier == "" {
		identifier = strings.TrimSpace(request.Header.Get("Authorization"))
	}
	sum := sha256.Sum256([]byte("workbuddy-pool\x00" + identifier))
	return hex.EncodeToString(sum[:8])
}

// workBuddyChatTransport is the chat path's transport: the provider row decides
// the proxy, and the profile selector is read once per build.
func workBuddyChatTransport(provider domain.Provider) *http.Transport {
	proxyURL := strings.TrimSpace(provider.ProxyURL)
	return workBuddyAccountTransport(workBuddyTransportSpec{
		providerID: strings.TrimSpace(provider.ID),
		userID:     workBuddyCredentialUserID(provider),
		proxyURL:   proxyURL,
	})
}

// workBuddyCredentialUserID is the account id a pool is keyed on.
func workBuddyCredentialUserID(provider domain.Provider) string {
	if provider.WorkBuddy == nil {
		return ""
	}
	return strings.TrimSpace(provider.WorkBuddy.UserID)
}
