package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// ---- versions ---------------------------------------------------------------

func TestWorkBuddyVersionOverrides(t *testing.T) {
	t.Setenv(workBuddyVersionCNEnv, "")
	t.Setenv(workBuddyVersionIntlEnv, "")
	t.Setenv(workBuddyVersionCLIEnv, "")

	if got := workBuddyCNSite.clientVersion(); got != workBuddyClientVersionCN {
		t.Fatalf("CN default = %q, want %q", got, workBuddyClientVersionCN)
	}
	if got := workBuddyIntlSite.clientVersion(); got != workBuddyClientVersionIntl {
		t.Fatalf("intl default = %q, want %q", got, workBuddyClientVersionIntl)
	}

	t.Setenv(workBuddyVersionCNEnv, "5.8.3")
	t.Setenv(workBuddyVersionIntlEnv, "5.8.4")
	t.Setenv(workBuddyVersionCLIEnv, "2.161.0")

	if got := workBuddyCNSite.chatUserAgent(); got != "WorkBuddy/5.8.3 WorkBuddy/5.8.3 CLI/2.161.0" {
		t.Fatalf("CN UA after override = %q", got)
	}
	if got := workBuddyIntlSite.chatUserAgent(); got != "WorkBuddy/5.8.4 workbuddy-ai/5.8.4 CLI/2.161.0" {
		t.Fatalf("intl UA after override = %q", got)
	}
	if got := workBuddyIntlSite.v3ConfigUserAgent(); got != "WorkBuddy/5.8.4 CLI/2.161.0" {
		t.Fatalf("v3 config UA after override = %q", got)
	}
	// A blank override is not a version: the default stands.
	t.Setenv(workBuddyVersionCNEnv, "   ")
	if got := workBuddyDesktopVersion(false); got != workBuddyClientVersionCN {
		t.Fatalf("blank override = %q, want the default %q", got, workBuddyClientVersionCN)
	}
}

// ---- per-account transports -------------------------------------------------

// Two accounts must never be seen on one TCP/TLS connection: that is the
// cross-account clue the reference gateway's pool key exists to prevent.
func TestWorkBuddyTransportsAreIsolatedPerAccount(t *testing.T) {
	a := workBuddyTransportFor("provider-a", "uid-1")
	b := workBuddyTransportFor("provider-b", "uid-1")
	sameAgain := workBuddyTransportFor("provider-a", "uid-1")

	if a == b {
		t.Fatal("two providers shared one transport, so their connections can be pooled together")
	}
	if a != sameAgain {
		t.Fatal("the same account did not get its own cached transport back")
	}
	if a == http.DefaultTransport {
		t.Fatal("the account was given the process-wide transport")
	}
	if a.MaxIdleConnsPerHost != workBuddyMaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConnsPerHost = %d, want %d", a.MaxIdleConnsPerHost, workBuddyMaxIdleConnsPerHost)
	}
}

// A user id alone is not an account identity: two providers can carry accounts
// with the same uid, and they still must not share connections.
func TestWorkBuddyTransportsKeyOnProviderAndUser(t *testing.T) {
	one := workBuddyTransportFor("provider-x", "uid-same")
	two := workBuddyTransportFor("provider-x", "uid-other")
	if one == two {
		t.Fatal("two different users of one provider shared a transport")
	}
}

func TestWorkBuddyChatClientUsesTheAccountsTransport(t *testing.T) {
	provider := domain.Provider{ID: "wb-provider", AuthType: domain.AuthTypeWorkBuddy,
		WorkBuddy: &domain.WorkBuddyCredential{UserID: "uid-client"}}
	client := workBuddyChatClient(provider)
	failover, ok := client.Transport.(*workBuddyFailoverTransport)
	if !ok {
		t.Fatalf("Transport = %T, want the failover transport", client.Transport)
	}
	if failover.base != workBuddyTransportFor("wb-provider", "uid-client") {
		t.Fatal("the client did not carry the account's own transport")
	}
	if client.Timeout != 0 {
		t.Fatalf("Timeout = %v, want none for a streamed reply", client.Timeout)
	}
}

// ---- dial-failure classification -------------------------------------------

func TestWorkBuddyDialFailureOnlyCountsUnsentRequests(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"dial refused", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},
		{"dns", &net.DNSError{Err: "no such host", Name: workBuddyCNPrimaryHost}, true},
		{"tls handshake", errors.New("remote error: tls: handshake failure"), true},
		{"reset by peer", errors.New("tls: connection reset by peer"), true},
		{"context canceled", context.Canceled, false},
		{"plain eof", io.EOF, false},
		// A response that started arriving means the upstream may already have
		// handled the request; retrying would bill it twice.
		{"read after send", &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}, false},
	}
	for _, testCase := range cases {
		if got := workBuddyDialFailure(testCase.err); got != testCase.want {
			t.Errorf("%s: workBuddyDialFailure = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

// ---- endpoint failover ------------------------------------------------------

// recordingTransport answers on the host it is given and records the order.
type recordingTransport struct {
	hosts []string
	fail  func(host string) error
}

func (r *recordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	host := request.URL.Host
	r.hosts = append(r.hosts, host)
	if r.fail != nil {
		if err := r.fail(host); err != nil {
			return nil, err
		}
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("ok")),
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

func newCNChatRequest(t *testing.T, host string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "https://"+host+"/v2/chat/completions", bytes.NewReader([]byte(`{"model":"m"}`)))
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestWorkBuddyFailoverUsesAlternateWhenPrimaryCannotBeDialled(t *testing.T) {
	dialErr := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	base := &recordingTransport{fail: func(host string) error {
		if host == workBuddyCNPrimaryHost {
			return dialErr
		}
		return nil
	}}
	switcher := &workBuddyEndpointSwitcher{}
	transport := &workBuddyFailoverTransport{base: base, switcher: switcher}

	response, err := transport.RoundTrip(newCNChatRequest(t, workBuddyCNPrimaryHost))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer response.Body.Close()
	if got := base.hosts; len(got) != 2 || got[0] != workBuddyCNPrimaryHost || got[1] != workBuddyCNAlternateHost {
		t.Fatalf("hosts asked = %v, want the primary then the alternate", got)
	}
	if !switcher.preferAlternate(time.Now()) {
		t.Fatal("the alternate answered but was not held")
	}
}

// Once held, the alternate is asked first — no failed handshake every turn.
func TestWorkBuddyFailoverAsksAlternateFirstWhileHeld(t *testing.T) {
	base := &recordingTransport{}
	switcher := &workBuddyEndpointSwitcher{}
	switcher.holdAlternate(time.Now())
	transport := &workBuddyFailoverTransport{base: base, switcher: switcher}

	response, err := transport.RoundTrip(newCNChatRequest(t, workBuddyCNPrimaryHost))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer response.Body.Close()
	if got := base.hosts; len(got) != 1 || got[0] != workBuddyCNAlternateHost {
		t.Fatalf("hosts asked = %v, want the alternate only", got)
	}
}

// A failure that may already have reached the upstream is never replayed.
func TestWorkBuddyFailoverDoesNotReplayAfterDial(t *testing.T) {
	readErr := &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}
	base := &recordingTransport{fail: func(string) error { return readErr }}
	switcher := &workBuddyEndpointSwitcher{}
	transport := &workBuddyFailoverTransport{base: base, switcher: switcher}

	if _, err := transport.RoundTrip(newCNChatRequest(t, workBuddyCNPrimaryHost)); err == nil {
		t.Fatal("want the read failure reported")
	}
	if got := base.hosts; len(got) != 1 {
		t.Fatalf("hosts asked = %v, want the request not to be replayed", got)
	}
	if switcher.preferAlternate(time.Now()) {
		t.Fatal("an unsent-safe failure must not switch the endpoint")
	}
}

// A primary that answers clears the hold, so the configured route comes back.
func TestWorkBuddyFailoverClearsHoldOnPrimarySuccess(t *testing.T) {
	base := &recordingTransport{}
	switcher := &workBuddyEndpointSwitcher{}
	switcher.holdAlternate(time.Now())
	transport := &workBuddyFailoverTransport{base: base, switcher: switcher}

	// The held alternate fails, so the primary is tried and answers.
	base.fail = func(host string) error {
		if host == workBuddyCNAlternateHost {
			return errors.New("boom")
		}
		return nil
	}
	response, err := transport.RoundTrip(newCNChatRequest(t, workBuddyCNPrimaryHost))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer response.Body.Close()
	if switcher.preferAlternate(time.Now()) {
		t.Fatal("the primary answered but the alternate is still held")
	}
}

// Other hosts (the intl build, RPC endpoints) are never rewritten.
func TestWorkBuddyFailoverLeavesOtherHostsAlone(t *testing.T) {
	base := &recordingTransport{fail: func(string) error { return errors.New("tls handshake failure") }}
	transport := &workBuddyFailoverTransport{base: base, switcher: &workBuddyEndpointSwitcher{}}

	if _, err := transport.RoundTrip(newCNChatRequest(t, "www.workbuddy.ai")); err == nil {
		t.Fatal("want the failure reported")
	}
	if got := base.hosts; len(got) != 1 || got[0] != "www.workbuddy.ai" {
		t.Fatalf("hosts asked = %v, want the intl host untouched", got)
	}
}
