package gateway

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Two things about the connection a WorkBuddy chat leaves on the wire decide
// whether accounts look like separate devices to the upstream, and neither is a
// header:
//
//  1. Connection sharing. Go's default transport pools connections process-wide,
//     so two accounts taking turns would be seen on one TCP/TLS connection with
//     two different Authorization headers — the reference gateway calls that a
//     cross-account correlation clue and keys its pool by credential digest so
//     it can never happen ("同一个出口上的两个账号绝不共用一条 TCP/TLS 连接",
//     wb_upstream_pool.py). This gateway instead gives each account its own
//     transport, and therefore its own pool.
//
//  2. The CN chat endpoint. copilot.tencent.com resets the TLS handshake when it
//     is reached from an overseas egress (the reference exposes
//     WB_CN_CHAT_UPSTREAM for exactly this: "copilot.tencent.com 从海外出口连接时
//     TLS 握手会被复位"), while www.workbuddy.cn serves the same API and is
//     reachable from abroad. Rather than make an operator configure that, the
//     transport tries the alternate host when the primary cannot even be dialled,
//     and holds on to it for a while once it answers.

const (
	workBuddyCNPrimaryHost   = "copilot.tencent.com"
	workBuddyCNAlternateHost = "www.workbuddy.cn"

	// workBuddyEndpointHoldOff is how long the alternate host is used after the
	// primary was found unreachable. Long enough to stop paying a failed
	// handshake on every turn, short enough to come back once a route is fixed.
	workBuddyEndpointHoldOff = 30 * time.Minute

	// The pool a per-account transport keeps. Two idle connections cover "one
	// streaming, one just returned"; more only keeps sockets alive for nothing.
	workBuddyIdleConnsPerHost = 2
	workBuddyIdleConnTimeout  = 90 * time.Second
)

// workBuddyTransportCache holds one transport per account. A transport owns its
// connection pool, so this cache is what keeps accounts apart.
var workBuddyTransportCache sync.Map // workBuddyTransportKey -> *http.Transport

type workBuddyTransportKey struct {
	providerID string
	userID     string
}

// workBuddyTransportFor returns the account's own transport, creating it on
// first use.
func workBuddyTransportFor(providerID, userID string) *http.Transport {
	key := workBuddyTransportKey{
		providerID: strings.TrimSpace(providerID),
		userID:     strings.TrimSpace(userID),
	}
	if cached, ok := workBuddyTransportCache.Load(key); ok {
		if transport, typed := cached.(*http.Transport); typed {
			return transport
		}
	}
	base, _ := http.DefaultTransport.(*http.Transport)
	var transport *http.Transport
	if base != nil {
		transport = base.Clone()
	} else {
		transport = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	transport.MaxIdleConns = workBuddyIdleConnsPerHost * 4
	transport.MaxIdleConnsPerHost = workBuddyIdleConnsPerHost
	transport.IdleConnTimeout = workBuddyIdleConnTimeout
	actual, _ := workBuddyTransportCache.LoadOrStore(key, transport)
	if existing, ok := actual.(*http.Transport); ok {
		return existing
	}
	return transport
}

// workBuddyChatClient is the HTTP client a WorkBuddy chat goes out through: the
// account's own connection pool, and the endpoint fallback described above.
func workBuddyChatClient(provider domain.Provider) *http.Client {
	userID := ""
	if provider.WorkBuddy != nil {
		userID = provider.WorkBuddy.UserID
	}
	transport := workBuddyTransportFor(provider.ID, userID)
	return &http.Client{
		Timeout: 0,
		Transport: &workBuddyFailoverTransport{
			base:     transport,
			switcher: workBuddyCNEndpoints,
		},
	}
}

// workBuddyEndpointSwitcher remembers that the CN primary host was unreachable
// and, for a while, asks the alternate first.
type workBuddyEndpointSwitcher struct {
	mu             sync.Mutex
	alternateUntil time.Time
}

var workBuddyCNEndpoints = &workBuddyEndpointSwitcher{}

// preferAlternate reports whether the alternate host should be asked first.
func (s *workBuddyEndpointSwitcher) preferAlternate(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return now.Before(s.alternateUntil)
}

// holdAlternate records that the alternate answered after the primary could not
// be reached.
func (s *workBuddyEndpointSwitcher) holdAlternate(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alternateUntil = now.Add(workBuddyEndpointHoldOff)
}

// clearAlternate records that the primary answered, so the next request asks it
// first again.
func (s *workBuddyEndpointSwitcher) clearAlternate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alternateUntil = time.Time{}
}

// workBuddyFailoverTransport asks the CN primary host, falls back to the
// alternate when the primary cannot be dialled, and stays on whichever answered.
type workBuddyFailoverTransport struct {
	base     http.RoundTripper
	switcher *workBuddyEndpointSwitcher
}

func (t *workBuddyFailoverTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t.base == nil || request == nil || request.URL == nil {
		return t.base.RoundTrip(request)
	}
	// Spelling first, on every path below: Go canonicalises header keys, while
	// the client's wire bytes are its own literals (see
	// officialWorkBuddyHeaderSpelling).
	request = normalizeWorkBuddyHeaderSpelling(request)
	if !workBuddyIsCNHost(request.URL.Host) {
		return t.base.RoundTrip(request)
	}
	now := time.Now()
	if t.switcher != nil && t.switcher.preferAlternate(now) {
		// Already known unreachable: ask the alternate first rather than pay a
		// failed handshake every turn.
		if response, err := t.roundTripOn(request, workBuddyCNAlternateHost); err == nil {
			return response, nil
		}
		// The alternate did not answer either. Try the route the operator
		// configured, and let a success end the hold.
		response, err := t.base.RoundTrip(request)
		if err == nil {
			t.switcher.clearAlternate()
		}
		return response, err
	}
	response, err := t.base.RoundTrip(request)
	if err == nil {
		if t.switcher != nil {
			t.switcher.clearAlternate()
		}
		return response, nil
	}
	if !workBuddyDialFailure(err) || request.GetBody == nil {
		// Either the request may already have reached the upstream (so a retry
		// could bill twice), or its body cannot be replayed.
		return response, err
	}
	alternate, altErr := t.roundTripOn(request, workBuddyCNAlternateHost)
	if altErr != nil {
		// Report the primary's failure: that is the route the operator configured.
		return response, err
	}
	if t.switcher != nil {
		t.switcher.holdAlternate(time.Now())
	}
	return alternate, nil
}

// roundTripOn replays the request against another host of the same endpoint.
func (t *workBuddyFailoverTransport) roundTripOn(request *http.Request, host string) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Host = host
	clone.URL = &url.URL{
		Scheme:   request.URL.Scheme,
		Host:     host,
		Path:     request.URL.Path,
		RawPath:  request.URL.RawPath,
		RawQuery: request.URL.RawQuery,
	}
	body, err := request.GetBody()
	if err != nil {
		return nil, err
	}
	clone.Body = body
	return t.base.RoundTrip(clone)
}

// workBuddyIsCNHost reports whether host is either name of the CN chat endpoint.
func workBuddyIsCNHost(host string) bool {
	name := strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(name); err == nil {
		name = h
	}
	return name == workBuddyCNPrimaryHost || name == workBuddyCNAlternateHost
}

// workBuddyDialFailure reports whether err says the request never left this
// machine: a dial that failed, or a TLS handshake that did not complete. Only
// these are safe to replay on another host — anything later may already have
// been processed upstream, and a retry would then be a second billable call.
func workBuddyDialFailure(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && strings.EqualFold(opErr.Op, "dial") {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"tls handshake",
		"tls: ",
		"remote error: tls",
		"connection refused",
		"no such host",
		"i/o timeout",
		"network is unreachable",
		"no route to host",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
