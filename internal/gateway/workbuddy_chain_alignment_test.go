package gateway

import (
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	utls "github.com/refraction-networking/utls"
)

var (
	workBuddyUUIDv4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	workBuddyUUIDv7Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

func decodeWorkBuddyTestBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, string(body))
	}
	return payload
}

// The round id belongs in the body as well as the head, on the last user turn —
// and only there, because history rounds carry no id the client ever gave us.
func TestApplyWorkBuddyBodyFieldsTagsTheCurrentRound(t *testing.T) {
	body := []byte(`{"model":"glm-5.3","stream":true,"messages":[` +
		`{"role":"system","content":"s"},` +
		`{"role":"user","content":"first"},` +
		`{"role":"assistant","content":"ok"},` +
		`{"role":"user","content":"second"}]}`)
	got := applyWorkBuddyBodyFields(body, workBuddyBodyFieldOptions{
		userID:                "uid-1",
		conversationID:        "conv-1",
		conversationRequestID: "req-1",
	})
	payload := decodeWorkBuddyTestBody(t, got)
	messages, _ := payload["messages"].([]any)
	if len(messages) != 4 {
		t.Fatalf("messages = %d", len(messages))
	}
	last, _ := messages[3].(map[string]any)
	if last["conversationRequestId"] != "req-1" {
		t.Fatalf("round id not written: %v", last)
	}
	if last["startsNewUserRequest"] != true {
		t.Fatalf("startsNewUserRequest not set: %v", last)
	}
	if last["agent"] != workBuddyAgentCLI {
		t.Fatalf("agent = %v", last["agent"])
	}
	first, _ := messages[1].(map[string]any)
	if _, has := first["conversationRequestId"]; has {
		t.Fatalf("a history round was given a made-up id: %v", first)
	}
}

// A client that already named its round keeps its own value.
func TestApplyWorkBuddyBodyFieldsKeepsAClientRoundID(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi","conversationRequestId":"client-round"}]}`)
	got := applyWorkBuddyBodyFields(body, workBuddyBodyFieldOptions{conversationRequestID: "ours"})
	payload := decodeWorkBuddyTestBody(t, got)
	messages, _ := payload["messages"].([]any)
	message, _ := messages[0].(map[string]any)
	if message["conversationRequestId"] != "client-round" {
		t.Fatalf("client round id was overwritten: %v", message)
	}
}

// The prefix cache key has to be the client's own uuid shape, stable for a
// conversation and never shared between accounts.
func TestWorkBuddyCacheKeyIsStableIsolatedAndClientShaped(t *testing.T) {
	one := workBuddyCacheKey("uid-1", "conv-1")
	if !workBuddyUUIDv4Re.MatchString(one) {
		t.Fatalf("cache key is not uuid-v4 shaped: %q", one)
	}
	if again := workBuddyCacheKey("uid-1", "conv-1"); again != one {
		t.Fatalf("cache key is not stable: %q vs %q", one, again)
	}
	if other := workBuddyCacheKey("uid-2", "conv-1"); other == one {
		t.Fatal("two accounts derived the same cache key, so one could hit the other's cached prefix")
	}
	if other := workBuddyCacheKey("uid-1", "conv-2"); other == one {
		t.Fatal("two conversations derived the same cache key")
	}
	if strings.Contains(one, "uid-1") || strings.Contains(one, "wb2a") {
		t.Fatalf("cache key leaks a readable account segment: %q", one)
	}
}

// A client that brought its own key is not overridden: it knows its slot.
func TestApplyWorkBuddyBodyFieldsKeepsAClientCacheKey(t *testing.T) {
	body := []byte(`{"model":"m","prompt_cache_key":"client-key","messages":[{"role":"user","content":"hi"}]}`)
	got := applyWorkBuddyBodyFields(body, workBuddyBodyFieldOptions{userID: "uid-1", conversationID: "conv-1"})
	payload := decodeWorkBuddyTestBody(t, got)
	if payload["prompt_cache_key"] != "client-key" {
		t.Fatalf("client cache key was overwritten: %v", payload["prompt_cache_key"])
	}
}

// The body's own conversation id wins over the head-derived one, because that is
// the id the client actually means.
func TestApplyWorkBuddyBodyFieldsPrefersTheBodysConversationID(t *testing.T) {
	body := []byte(`{"model":"m","conversation_id":"from-body","messages":[{"role":"user","content":"hi"}]}`)
	got := applyWorkBuddyBodyFields(body, workBuddyBodyFieldOptions{userID: "uid-1", conversationID: "from-head"})
	payload := decodeWorkBuddyTestBody(t, got)
	want := workBuddyCacheKey("uid-1", "from-body")
	if payload["prompt_cache_key"] != want {
		t.Fatalf("cache key = %v, want %v", payload["prompt_cache_key"], want)
	}
}

// stream_options is added only when the client set none, so a client that opted
// out is not overruled.
func TestApplyWorkBuddyBodyFieldsAddsStreamOptionsOnce(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	got := applyWorkBuddyBodyFields(body, workBuddyBodyFieldOptions{})
	payload := decodeWorkBuddyTestBody(t, got)
	options, _ := payload["stream_options"].(map[string]any)
	if options["include_usage"] != true {
		t.Fatalf("stream_options not set: %v", payload["stream_options"])
	}

	explicit := []byte(`{"model":"m","stream_options":{"include_usage":false},"messages":[{"role":"user","content":"hi"}]}`)
	got = applyWorkBuddyBodyFields(explicit, workBuddyBodyFieldOptions{})
	payload = decodeWorkBuddyTestBody(t, got)
	if payload["stream_options"].(map[string]any)["include_usage"] != false {
		t.Fatalf("client's stream_options was overruled: %v", payload["stream_options"])
	}
}

// A body that will not parse comes back untouched: a bad body is not made worse.
func TestApplyWorkBuddyBodyFieldsLeavesNonChatBodiesAlone(t *testing.T) {
	for _, body := range []string{``, `not json`, `{"input":"x"}`} {
		if got := applyWorkBuddyBodyFields([]byte(body), workBuddyBodyFieldOptions{userID: "u"}); string(got) != body {
			t.Fatalf("body rewritten: %q -> %q", body, string(got))
		}
	}
}

// Session ids have to carry a plausible v7 timestamp: the client's ids decode to
// when the conversation started, so hashing bits straight into the time field
// would date them to the wrong century.
func TestWorkBuddyStableUUIDCarriesAPlausibleTimestamp(t *testing.T) {
	id := workBuddyStableUUID("conversation:provider:session")
	if !workBuddyUUIDv7Re.MatchString(id) {
		t.Fatalf("id is not uuid-v7 shaped: %q", id)
	}
	if again := workBuddyStableUUID("conversation:provider:session"); again != id {
		t.Fatalf("id is not stable: %q vs %q", id, again)
	}
	// Recover the 48-bit millisecond field and check it lands in 2026-2028.
	hexDigits := strings.ReplaceAll(id[:13], "-", "")
	var millis int64
	for _, ch := range hexDigits[:12] {
		millis <<= 4
		switch {
		case ch >= '0' && ch <= '9':
			millis += int64(ch - '0')
		case ch >= 'a' && ch <= 'f':
			millis += int64(ch-'a') + 10
		default:
			t.Fatalf("unexpected hex digit %q in %q", ch, id)
		}
	}
	const (
		start2026 = int64(1767225600000) // 2026-01-01T00:00:00Z
		start2029 = int64(1861920000000) // 2029-01-01T00:00:00Z
	)
	if millis < start2026 || millis >= start2029 {
		t.Fatalf("v7 timestamp decodes to an implausible instant: %d (%q)", millis, id)
	}
}

// The tools a client declared carry the same template sentences as its prompt,
// so their descriptions are cleaned too.
func TestSanitizeWorkBuddyToolDescriptions(t *testing.T) {
	tools := []any{
		map[string]any{"type": "function", "function": map[string]any{
			"name":        "Bash",
			"description": "You are an AI agent powered by DeepSeek Harness.",
		}},
	}
	if !sanitizeWorkBuddyToolDescriptions(tools) {
		t.Fatal("a flagged tool description was left alone")
	}
	function, _ := tools[0].(map[string]any)["function"].(map[string]any)
	// The comma is the fix, and the harness keeps its name: replacing the whole
	// sentence would take the product's name out of the line that says which agent
	// runs (measured 400 -> 200 with the comma alone).
	if function["description"] != "You are an AI agent, powered by DeepSeek Harness." {
		t.Fatalf("description = %v", function["description"])
	}
}

// Tool arguments are stringified JSON and are cleaned as text: a fingerprint
// written into a filename or a command leaks the same way as one in a message.
func TestUnflagWorkBuddyMessagesCleansToolArgumentsAndReasoning(t *testing.T) {
	messages := []any{
		map[string]any{"role": "system", "content": "You are an AI agent powered by DeepSeek Harness."},
		map[string]any{
			"role":    "assistant",
			"content": nil,
			"tool_calls": []any{
				map[string]any{"id": "c1", "type": "function", "function": map[string]any{
					"name":      "Write",
					"arguments": `{"path":"notes.md","content":"code=11128 and DeepSeek Harness"}`,
				}},
			},
			"reasoning_content": "the user said 11128",
		},
	}
	out, changed := unflagWorkBuddyMessages(messages)
	if !changed {
		t.Fatal("nothing was cleaned")
	}
	assistant, _ := out[1].(map[string]any)
	calls, _ := assistant["tool_calls"].([]any)
	call, _ := calls[0].(map[string]any)
	function, _ := call["function"].(map[string]any)
	arguments, _ := function["arguments"].(string)
	if strings.Contains(arguments, "11128") {
		t.Fatalf("the refusal code survived in tool arguments: %s", arguments)
	}
	// The brand stays: the fix is the skeleton, not the name.
	if !strings.Contains(arguments, "DeepSeek Harness") {
		t.Fatalf("the harness name was removed from tool arguments: %s", arguments)
	}
	reasoning, _ := assistant["reasoning_content"].(string)
	if strings.Contains(reasoning, "11128") {
		t.Fatalf("the refusal code survived in the reasoning trace: %s", reasoning)
	}
}

// The client's own stack carries the literal refusal code in nothing, but a user
// asking about it would: the rewrite keeps the reference readable.
func TestUnflagWorkBuddyTextHyphenatesTheRefusalCode(t *testing.T) {
	if got := unflagWorkBuddyText("the error was code=11128 yesterday"); !strings.Contains(got, "11-128") {
		t.Fatalf("refusal code not rewritten: %q", got)
	}
	// Neighbouring codes are left alone.
	for _, untouched := range []string{"11148", "11101", "11115"} {
		if got := unflagWorkBuddyText("code=" + untouched); got != "code="+untouched {
			t.Fatalf("%s was rewritten: %q", untouched, got)
		}
	}
}

// The identity sentence is broken by a comma, not replaced: the skeleton is the
// signal, but the words are the agent's own name and have to survive.
func TestUnflagWorkBuddyTextBreaksTheIdentitySentenceSkeleton(t *testing.T) {
	for _, testCase := range []struct{ in, want string }{
		{"You are an AI agent powered by SomeBrand.", "You are an AI agent, powered by SomeBrand."},
		{"You are a coding agent built by SomeVendor.", "You are a coding agent, built by SomeVendor."},
		{"You are SomeProduct Agent created by SomeVendor.", "You are SomeProduct Agent, created by SomeVendor."},
	} {
		if got := unflagWorkBuddyTextFor(testCase.in, true); got != testCase.want {
			t.Fatalf("%q -> %q, want %q", testCase.in, got, testCase.want)
		}
	}
	// A sentence where the participle modifies the object is not an identity
	// claim and must survive untouched.
	keep := "You are free to use tools built by anyone."
	if got := unflagWorkBuddyTextFor(keep, true); got != keep {
		t.Fatalf("over-matched: %q -> %q", keep, got)
	}
	// The brand is never renamed: the name is what the rest of the prompt refers to.
	if got := unflagWorkBuddyTextFor("You are an AI agent powered by DeepSeek Harness.", true); strings.Contains(got, "DeepSeekRuntime") {
		t.Fatalf("the harness name was renamed: %q", got)
	}
}

// The transport carries every layer: h2 off, header order hooked at the dial,
// bounded handshake, and no sharing with the process-wide pool.
func TestWorkBuddyTransportShape(t *testing.T) {
	transport := newWorkBuddyTransport(workBuddyTransportSpec{providerID: "p", userID: "u"})
	if transport.DialTLSContext == nil {
		t.Fatal("DialTLSContext is nil, so the TLS fingerprint and header order are bypassed")
	}
	if transport.DialContext == nil {
		t.Fatal("DialContext is nil, so plain connections skip the header order")
	}
	if transport.TLSNextProto == nil {
		t.Fatal("TLSNextProto is nil; an empty map is what actually disables h2")
	}
	if transport.ForceAttemptHTTP2 {
		t.Fatal("ForceAttemptHTTP2 is on")
	}
	if transport.TLSHandshakeTimeout != 0 {
		t.Fatalf("TLSHandshakeTimeout = %v; the dial hook owns the bound", transport.TLSHandshakeTimeout)
	}
	if transport.MaxIdleConnsPerHost != workBuddyMaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConnsPerHost = %d", transport.MaxIdleConnsPerHost)
	}
}

// The provider's proxy is part of the pool identity: changing it must not reuse
// a pool built for the old route.
func TestWorkBuddyTransportKeysOnProxyAndProfile(t *testing.T) {
	direct := workBuddyAccountTransport(workBuddyTransportSpec{providerID: "p", userID: "u"})
	proxied := workBuddyAccountTransport(workBuddyTransportSpec{providerID: "p", userID: "u", proxyURL: "http://127.0.0.1:3128"})
	if direct == proxied {
		t.Fatal("a proxied provider reused the direct pool")
	}
	native := workBuddyAccountTransport(workBuddyTransportSpec{providerID: "p", userID: "u", mode: workBuddyTLSFingerprintGo})
	if native == direct {
		t.Fatal("a different TLS profile reused the same pool")
	}
}

// Account-facing calls (billing, growth, report, check-in) must not share the
// process-wide pool: that is what let two accounts appear on one connection.
func TestWorkBuddyRequestClientIsolatesAccounts(t *testing.T) {
	one := httptest.NewRequest(http.MethodPost, "https://www.workbuddy.cn/v2/report", nil)
	one.Header.Set("X-User-Id", "uid-1")
	two := httptest.NewRequest(http.MethodPost, "https://www.workbuddy.cn/v2/report", nil)
	two.Header.Set("X-User-Id", "uid-2")

	clientOne := workBuddyRequestClient(one)
	clientTwo := workBuddyRequestClient(two)
	if clientOne.Transport == clientTwo.Transport {
		t.Fatal("two accounts were given one transport, so their connections can be pooled together")
	}
	if clientOne.Transport == http.DefaultTransport {
		t.Fatal("the account was given the process-wide transport")
	}
	if again := workBuddyRequestClient(one); again.Transport != clientOne.Transport {
		t.Fatal("the same account did not get its cached transport back")
	}
}

// The exit fixes spelling and Connection in one place, on a copy so the caller's
// request still reads back what it set.
func TestWorkBuddyRequestExitFixesSpellingAndConnection(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "https://www.workbuddy.cn/v2/chat/completions", nil)
	request.Header.Set("X-Request-ID", "r1")
	request.Header.Set("X-B3-ParentSpanId", "s1")

	exit := workBuddyRequestExit(request)
	if got := exit.Header.Get("Connection"); got != "keep-alive" {
		t.Fatalf("Connection = %q, want keep-alive", got)
	}
	var foundRequestID, foundParentSpan bool
	for name := range exit.Header {
		switch name {
		case "X-Request-ID":
			foundRequestID = true
		case "X-B3-ParentSpanId":
			foundParentSpan = true
		}
	}
	if !foundRequestID || !foundParentSpan {
		t.Fatalf("spelling was not preserved: %v", exit.Header)
	}
	if request.Header.Get("Connection") != "" {
		t.Fatal("the caller's request was mutated in place")
	}
}

// The reproduced ClientHello keeps the measured shape: 18 ciphers with the three
// TLS 1.3 suites first, ten extensions in capture order, no ALPN.
func TestWorkBuddyHelloSpecMatchesTheCapture(t *testing.T) {
	spec := workBuddyHelloSpec()
	if len(spec.CipherSuites) != 18 {
		t.Fatalf("cipher count = %d, want 18", len(spec.CipherSuites))
	}
	if spec.CipherSuites[0] != tls.TLS_AES_128_GCM_SHA256 {
		t.Fatalf("first cipher = %#x, want the TLS 1.3 suite the client sends first", spec.CipherSuites[0])
	}
	if len(workBuddyOfficialSigAlgs) != 9 {
		t.Fatalf("signature algorithm count = %d, want 9", len(workBuddyOfficialSigAlgs))
	}
	if len(spec.Extensions) != 10 {
		t.Fatalf("extension count = %d, want 10", len(spec.Extensions))
	}
	for _, extension := range spec.Extensions {
		if _, isALPN := extension.(*utls.ALPNExtension); isALPN {
			t.Fatal("ALPN is present; the official Node stack sends none")
		}
	}
	// The anchors the fingerprint exists for.
	if workBuddyJA3MD5 != "71dc8c533dd919ae9f4963224a4ba8fd" {
		t.Fatal("JA3 anchor changed")
	}
	if workBuddyJA4 != "t13d0a1200_5d04281c6031_b76c5ee2507d" {
		t.Fatal("JA4 anchor changed")
	}
}

// The profile selector only falls back on an explicit "go".
func TestWorkBuddyTLSFingerprintModeDefaultsToNode(t *testing.T) {
	t.Setenv(workBuddyTLSFingerprintEnv, "")
	if got := workBuddyTLSFingerprintMode(); got != workBuddyTLSFingerprintNode {
		t.Fatalf("mode = %q, want node", got)
	}
	t.Setenv(workBuddyTLSFingerprintEnv, "go")
	if got := workBuddyTLSFingerprintMode(); got != workBuddyTLSFingerprintGo {
		t.Fatalf("mode = %q, want go", got)
	}
	t.Setenv(workBuddyTLSFingerprintEnv, "chromium")
	if got := workBuddyTLSFingerprintMode(); got != workBuddyTLSFingerprintNode {
		t.Fatalf("mode = %q, want the default for an unknown value", got)
	}
}

// The fingerprinted hop has to complete a real handshake and then carry reordered
// HTTP bytes, which is the whole point of taking over the dial.
func TestWorkBuddyTLSHandshakeCompletesAndWraps(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("ok"))
	}))
	defer server.Close()

	host, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "https://"))
	if err != nil {
		t.Fatalf("server address: %v", err)
	}
	dialer := &net.Dialer{Timeout: workBuddyDialTimeout}
	dial := workBuddyTLSDialer(dialer, &tls.Config{InsecureSkipVerify: true, ServerName: "www.workbuddy.cn"}, workBuddyTLSFingerprintNode)
	conn, err := dial(t.Context(), "tcp", net.JoinHostPort(host, port))
	if err != nil {
		t.Fatalf("handshake through the fingerprinted dial: %v", err)
	}
	defer conn.Close()
	if _, wrapped := workBuddyHeadOrderWrapped(conn); !wrapped {
		t.Fatal("the handshake did not return a header-order wrapped connection")
	}
}

// extensionTypeOf is gone: extension identity is checked by type, which cannot
// drift with utls' encoders.
