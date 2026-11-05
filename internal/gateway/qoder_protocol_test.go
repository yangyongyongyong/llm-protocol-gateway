package gateway

import (
	"strings"
	"testing"
)

// qoderDecodeBody is the inverse of qoderEncodeBody, written here as an
// independent implementation rather than by reusing the encoder's internals, so
// a round-trip test says something about the codec and not only about itself.
func qoderDecodeBody(t *testing.T, wire string) string {
	t.Helper()
	third := len(wire) / 3
	if third > 0 {
		wire = wire[len(wire)-third:] + wire[third:len(wire)-third] + wire[:third]
	}
	var bits []int
	for group := 0; group+4 <= len(wire); group += 4 {
		values := make([]int, 0, 4)
		for _, char := range wire[group : group+4] {
			if char == '$' {
				continue
			}
			index := strings.IndexRune(qoderBodyAlphabet, char)
			if index < 0 {
				t.Fatalf("encoded body holds %q, which is not in Qoder's alphabet", char)
			}
			values = append(values, index)
		}
		if len(values) == 0 {
			continue
		}
		acc := 0
		for _, value := range values {
			acc = acc<<6 | value
		}
		bytesOut := (6 * len(values)) / 8
		for k := 0; k < bytesOut; k++ {
			shift := uint(6*len(values) - 8*(k+1))
			bits = append(bits, (acc>>shift)&0xFF)
		}
	}
	out := make([]byte, len(bits))
	for index, value := range bits {
		out[index] = byte(value)
	}
	return string(out)
}

func TestQoderEncodeBodyRoundTrips(t *testing.T) {
	cases := []string{
		"",
		`{"a":1}`,
		"hello 你好",
		strings.Repeat("x", 512),
		strings.Repeat("日本語テキスト", 40),
	}
	for _, plaintext := range cases {
		wire := qoderEncodeBody([]byte(plaintext))
		if len(wire)%4 != 0 {
			t.Fatalf("%q encoded to %d chars, not a multiple of four", plaintext, len(wire))
		}
		if got := qoderDecodeBody(t, wire); got != plaintext {
			t.Fatalf("round trip changed the body:\n got %q\nwant %q", got, plaintext)
		}
	}
}

// The outer-thirds swap is what tells Qoder's codec from ordinary base64: a
// body encoded without it is rejected by the endpoint.
func TestQoderEncodeBodySwapsOuterThirds(t *testing.T) {
	plaintext := []byte(`{"messages":[{"role":"user","content":"say something"}]}`)
	wire := qoderEncodeBody(plaintext)
	if !strings.HasSuffix(wire, "$") && len(wire)%4 != 0 {
		t.Fatalf("expected padding to a multiple of four, got %q", wire)
	}
	plain := qoderBodyAlphabetEncodeOnly(plaintext)
	third := len(plain) / 3
	want := plain[len(plain)-third:] + plain[third:len(plain)-third] + plain[:third]
	if wire != want {
		t.Fatalf("outer thirds not swapped:\n got %q\nwant %q", wire, want)
	}
}

// qoderBodyAlphabetEncodeOnly is the un-swapped half of the codec, kept here so
// the swap can be asserted against it.
func qoderBodyAlphabetEncodeOnly(data []byte) string {
	var out strings.Builder
	var acc, bits int
	for _, b := range data {
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
	return out.String()
}

func TestQoderSignaturePath(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"https://api3.qoder.sh/algo/api/v2/model/list?Encode=1", "/api/v2/model/list"},
		{"https://api3.qoder.sh/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=1", "/api/v2/service/pro/sse/agent_chat_generation"},
		{"https://gateway.qoder.com.cn/api/v1/ping", "/api/v1/ping"},
		{"https://api3.qoder.sh/algo", ""},
	}
	for _, tc := range cases {
		if got := qoderSignaturePath(tc.raw); got != tc.want {
			t.Errorf("qoderSignaturePath(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// The envelope's shape is what the endpoint checks: a bearer of the form
// COSY.<payload>.<md5>, the wrapped key beside it, and the account named in
// Cosy-User. A made-up identity gets a 403 with nothing to explain it.
func TestQoderCOSYHeadersEnvelopeShape(t *testing.T) {
	endpoint := qoderGlobalSite.qoderModelsURL()
	headers, err := qoderCOSYHeaders(endpoint, qoderAccount{
		UID:       "user-1",
		Name:      "Example User",
		Email:     "user@example.com",
		Token:     "jt-token",
		MachineID: "machine-1",
	}, "", 1_700_000_000)
	if err != nil {
		t.Fatal(err)
	}
	authorization := headers["Authorization"]
	if !strings.HasPrefix(authorization, "Bearer COSY.") {
		t.Fatalf("unexpected Authorization: %q", authorization)
	}
	parts := strings.Split(strings.TrimPrefix(authorization, "Bearer COSY."), ".")
	if len(parts) != 2 {
		t.Fatalf("expected payload and signature, got %d parts", len(parts))
	}
	if len(parts[1]) != 32 {
		t.Fatalf("signature is not an md5 hex string: %q", parts[1])
	}
	for _, required := range []string{"Cosy-Key", "Cosy-Date", "Cosy-User", "Cosy-MachineId", "Cosy-Version"} {
		if strings.TrimSpace(headers[required]) == "" {
			t.Errorf("%s missing from the envelope", required)
		}
	}
	if headers["Cosy-User"] != "user-1" || headers["Cosy-MachineId"] != "machine-1" {
		t.Fatalf("account identity wrong: %+v", headers)
	}
	if headers["Cosy-Date"] != "1700000000" {
		t.Errorf("Cosy-Date should carry the signed timestamp, got %q", headers["Cosy-Date"])
	}
}

// The device identity is derived from the uid, so an account stored without a
// machine id still signs: Qoder's own client never has one stored, it computes
// it, and the envelope has to look the same either way.
func TestQoderCOSYHeadersDeriveAMachineIdentityWhenNoneIsStored(t *testing.T) {
	headers, err := qoderCOSYHeaders(qoderGlobalSite.qoderChatURL(), qoderAccount{UID: "fx-demo-uid", Token: "t"}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := headers["Cosy-MachineId"]; got != qoderDeriveID("fx-demo-uid", "machine") {
		t.Fatalf("Cosy-MachineId = %q, want the derived machine id", got)
	}
	for _, required := range []string{"Cosy-Key", "Cosy-Date", "Cosy-User", "Cosy-MachineId", "Cosy-MachineToken", "Cosy-MachineType", "Cosy-MachineOS", "Cosy-MachineHostname", "Cosy-Version"} {
		if strings.TrimSpace(headers[required]) == "" {
			t.Errorf("%s missing from the envelope", required)
		}
	}
	// An account without a uid cannot be signed for at all.
	if _, err := qoderCOSYHeaders(qoderGlobalSite.qoderChatURL(), qoderAccount{Token: "t"}, "", 0); err == nil {
		t.Fatal("expected an error when the account has no uid")
	}
}

// The client identity in the envelope is the one the CLI claims, not the app's:
// inference and the model list are served to the CLI, and every field below is
// a fixed value in Qoder's own client.
func TestQoderCOSYHeadersCarryTheClientsIdentity(t *testing.T) {
	headers, err := qoderCOSYHeaders(qoderGlobalSite.qoderChatURL(), qoderAccount{UID: "u1", Token: "t"}, "", 1_700_000_000)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Cosy-Business-Product": "cli",
		"Cosy-Business-Type":    "agent",
		"Cosy-Scene":            "assistant",
		"Cosy-ClientType":       "5",
		"Cosy-ClientIp":         "169.254.198.161",
		"Cosy-Data-Policy":      "agree",
		"Cosy-Version":          "1.1.75",
		"User-Agent":            "undici",
		"Login-Version":         "v2",
		"Accept-Encoding":       "identity",
	}
	for header, value := range want {
		if headers[header] != value {
			t.Errorf("%s = %q, want %q", header, headers[header], value)
		}
	}
	// The machine id, token and type are three different values; sending one in
	// all three is what a client Qoder never shipped looks like.
	if headers["Cosy-MachineId"] == headers["Cosy-MachineToken"] ||
		headers["Cosy-MachineToken"] == headers["Cosy-MachineType"] {
		t.Fatalf("machine identity fields must differ: %+v", headers)
	}
	if len(headers["Cosy-MachineToken"]) != 43 {
		t.Errorf("machine token is 43 characters, got %d", len(headers["Cosy-MachineToken"]))
	}
	if len(headers["Cosy-MachineType"]) != 18 {
		t.Errorf("machine type is 18 characters, got %d", len(headers["Cosy-MachineType"]))
	}
	if hostname := headers["Cosy-MachineHostname"]; !strings.HasPrefix(hostname, "DESKTOP-") || len(hostname) != 15 {
		t.Errorf("machine hostname = %q, want DESKTOP- and seven hex digits", hostname)
	}
}

// The codec is Qoder's own: a shuffled alphabet, the padding character '$', and
// the standard grouping rotated in three parts. These vectors come from the
// vendor client, so a change here is a change the endpoint will refuse.
func TestQoderEncodeBodyMatchesTheClientsCodec(t *testing.T) {
	cases := []struct{ plain, wire string }{
		{"", ""},
		{"a", "$p$#"},
		{"ab", "$SB#"},
		{"abc", "MSK#"},
		{"abcd", "$$KMD_#S"},
		{"{}", "$kwm"},
		{`{"x":1}`, "ep$$BMn%mYKI"},
		{"hello", "q$FruHPH"},
		{"abcdefghijkl", ")POtrHFOD(i#SKMD"},
	}
	for _, tc := range cases {
		if got := qoderEncodeBody([]byte(tc.plain)); got != tc.wire {
			t.Errorf("qoderEncodeBody(%q) = %q, want %q", tc.plain, got, tc.wire)
		}
	}
}

// The derived device identity is a function of the uid alone, and these vectors
// are the client's own output.
func TestQoderDerivedDeviceIdentity(t *testing.T) {
	const uid = "fx-demo-uid"
	if got := qoderDeriveID(uid, "machine"); got != "1aa25689-af59-4701-a8d2-05b206925edd" {
		t.Errorf("machine id = %q", got)
	}
	if got := qoderDeriveMachineToken(uid); got != "65yzHqohRdD9Z3iyZZ_0NrsfofQoU1oB4yGKlLZ8sMF" {
		t.Errorf("machine token = %q", got)
	}
	if got := qoderDeriveMachineType(uid); got != "706b608dd21e4e70a0" {
		t.Errorf("machine type = %q", got)
	}
	if got := qoderDeriveMachineHostname(uid); got != "DESKTOP-5E64A1D" {
		t.Errorf("machine hostname = %q", got)
	}
	// Two accounts must not share a machine identity.
	if qoderDeriveID("uid-a", "machine") == qoderDeriveID("uid-b", "machine") {
		t.Error("two accounts share a machine id")
	}
}

// The identity blob is rebuilt only when the access token changes: Qoder's
// client keeps one COSY key pair per signed-in session.
func TestQoderCOSYSessionIsReusedUntilTheTokenChanges(t *testing.T) {
	account := qoderAccount{UID: "session-uid", Token: "token-1"}
	firstInfo, firstKey, err := qoderUserBlob(account)
	if err != nil {
		t.Fatal(err)
	}
	againInfo, againKey, err := qoderUserBlob(account)
	if err != nil {
		t.Fatal(err)
	}
	if firstInfo != againInfo || firstKey != againKey {
		t.Fatal("the key pair must be reused between requests")
	}
	rotated := account
	rotated.Token = "token-2"
	rotatedInfo, rotatedKey, err := qoderUserBlob(rotated)
	if err != nil {
		t.Fatal(err)
	}
	if rotatedInfo == firstInfo || rotatedKey == firstKey {
		t.Fatal("a rotated token must rebuild the key pair")
	}
}

func TestQoderSiteForAuthType(t *testing.T) {
	global, ok := qoderSiteForAuthType("qoder_pat")
	if !ok || global.API != "https://api3.qoder.sh" || global.RedirectURI == "" {
		t.Fatalf("unexpected global site: %+v", global)
	}
	cn, ok := qoderSiteForAuthType("qoder_cn")
	if !ok || cn.API != "https://gateway.qoder.com.cn" || !cn.DeviceChatFallback || cn.RedirectURI != "" {
		t.Fatalf("unexpected Qoder CN site: %+v", cn)
	}
	if _, ok := qoderSiteForAuthType("trae_cn"); ok {
		t.Fatal("a Trae auth type must not resolve to a Qoder site")
	}
}
