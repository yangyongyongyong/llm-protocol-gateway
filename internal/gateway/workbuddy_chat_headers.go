package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// The desktop client's chat header family, in the shape captured from the real
// client and recorded byte for byte by the Go reference implementation
// (misakano7545/workbuddy2api-panel: internal/upstream/headers.go and
// testdata/official_chat_head.txt, 5.9.3 CN / 5.9.4 intl captures).
//
// Three kinds of value live here, and they are worth telling apart because the
// upstream treats them differently:
//
//   - fixed values the client always sends (agent intent, runtime, the SDK
//     block) — a missing one is a shape the client never produces;
//   - values the account picks (private-data by edition, domain by realm);
//   - ids the client makes per conversation / per turn / per request, whose
//     *shape* is as much a part of the fingerprint as their presence:
//     conversations and ACP connections are hyphenated UUIDv7s, while request
//     and message ids are 32 hex without hyphens.

const (
	// workBuddyCNChatHost is the X-Domain a CN account declares: a bare host
	// name, no scheme. The intl build declares its own.
	workBuddyCNChatHost   = "www.workbuddy.cn"
	workBuddyIntlChatHost = "www.workbuddy.ai"

	// The SDK block the official chat carries because that path runs the OpenAI
	// Node SDK. Fixed rather than host-derived: the client is a Windows Electron
	// app, so claiming this host's real runtime under a desktop User-Agent would
	// be a worse lie than the client's own constants.
	workBuddySDKLang    = "js"
	workBuddySDKOS      = "Windows"
	workBuddySDKArch    = "x64"
	workBuddySDKRuntime = "node"

	workBuddyAgentRuntimeLocal = "local-agent"
	workBuddyAgentTypeMain     = "main"
	workBuddyAgentTypeAux      = "auxiliary"
	workBuddyAgentIntentCraft  = "craft"
)

// workBuddyConversationIDKey carries the conversation id a turn belongs to.
type workBuddyConversationIDKey struct{}

// workBuddySessionRootIDKey carries the session's root request id.
type workBuddySessionRootIDKey struct{}

// withWorkBuddyConversationID pins the conversation this turn belongs to.
func withWorkBuddyConversationID(ctx context.Context, id string) context.Context {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return ctx
	}
	return context.WithValue(ctx, workBuddyConversationIDKey{}, trimmed)
}

// workBuddyConversationIDFrom is the pinned conversation id, or "".
func workBuddyConversationIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(workBuddyConversationIDKey{}).(string)
	return strings.TrimSpace(id)
}

// withWorkBuddySessionRootID pins the session's root request id.
func withWorkBuddySessionRootID(ctx context.Context, id string) context.Context {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return ctx
	}
	return context.WithValue(ctx, workBuddySessionRootIDKey{}, trimmed)
}

// workBuddySessionRootIDFrom is the pinned root id, or "".
func workBuddySessionRootIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(workBuddySessionRootIDKey{}).(string)
	return strings.TrimSpace(id)
}

// workBuddyStableUUID derives a hyphenated UUIDv7-shaped id from a seed.
//
// The real client's X-Conversation-ID and acp-connection-id are hyphenated,
// time-ordered UUIDv7s (`01a1815d-dacf-75f1-…`), while our request ids are 32
// hex. Sending 32 hex in a field the client always fills with a hyphenated v7 is
// a shape a real capture never shows, so the two families are produced
// separately. Deriving from a seed rather than at random keeps the value stable
// across the turns of one conversation, which is what the client does.
//
// The time bits come from workBuddyTimeOrderedUUID: hashing them straight into
// place would decode to dates in the wrong century, which is its own tell (see
// workbuddy_body_fields.go).
func workBuddyStableUUID(seed string) string {
	return workBuddyTimeOrderedUUID(seed)
}

// workBuddyStableHex derives a 32-hex id from a seed — the shape the client uses
// for request ids, which is what a session's root request id is.
func workBuddyStableHex(seed string) string {
	sum := sha256.Sum256([]byte("workbuddy:" + seed))
	return hex.EncodeToString(sum[:16])
}

// workBuddyPrivateDataValue is the X-Private-Data the client sends.
//
// The client's rule (applyPrivateDataHeaderPolicy, decompiled) is
// `optimizationEnabled ? "false" : "true"`, and whether optimization is on by
// default is a **release** property, not an account property:
//
//	intl 5.9.4 — product.json DefaultModelOptimizationOff = true  → "true"
//	CN   5.9.3 — product.json DefaultModelOptimizationOff = false → "false"
//
// Getting this backwards means every single chat carries the opposite of what
// the client sends. An enterprise seat also turns optimization off and so always
// says "true", but this gateway keeps no enterprise flag, so the release decides.
func workBuddyPrivateDataValue(site workBuddySite) string {
	if site.intl {
		return "true"
	}
	return "false"
}

// workBuddyBaggage is the W3C baggage the client attaches to a chat.
//
// Captured form, four members, with the two buddy keys present but empty (that
// is how OTel's W3CBaggagePropagator serialises a key it has no value for —
// dropping them would be a different string):
//
//	buddyId=,buddyName=,codebuddy.session_id=<conv>,codebuddy.conversation_request_id=<req>
func workBuddyBaggage(conversationID, conversationRequestID string) string {
	return "buddyId=,buddyName=" +
		",codebuddy.session_id=" + conversationID +
		",codebuddy.conversation_request_id=" + conversationRequestID
}

// workBuddyACPConnectionID is the acp-connection-id the client sends: one per
// conversation, stable across its turns, and a different value from the
// conversation id itself.
func workBuddyACPConnectionID(conversationID string) string {
	return workBuddyStableUUID("acp:" + conversationID)
}

// workBuddyChatDomain is the X-Domain a chat declares: the account's own domain
// when the credential carries one, otherwise this build's chat host. The header
// is mandatory on a chat — a request without it is a shape the client never
// produces.
func workBuddyChatDomain(site workBuddySite, credential *domain.WorkBuddyCredential) string {
	if credential != nil {
		if domain := strings.TrimSpace(credential.Domain); domain != "" {
			return domain
		}
	}
	if site.intl {
		return workBuddyIntlChatHost
	}
	return workBuddyCNChatHost
}

// applyWorkBuddyChatHeaders writes the whole desktop chat header family.
//
// The order the values are set here does not decide what goes on the wire (Go
// sorts header keys); see workbuddy_headorder.go for that, and for the spelling
// map that keeps `X-Request-ID` from going out as `X-Request-Id`.
func applyWorkBuddyChatHeaders(request *http.Request, site workBuddySite, credential *domain.WorkBuddyCredential, agentType string) {
	ctx := request.Context()

	conversationID := workBuddyConversationIDFrom(ctx)
	if conversationID == "" {
		conversationID = workBuddyStableUUID("conversation:" + workBuddyRequestID())
	}
	// One aggregation id per turn, reused by every request and retry of it (the
	// caller pins it — see withWorkBuddyTurnRequestID).
	conversationRequestID := workBuddyTurnRequestIDFrom(ctx)
	if conversationRequestID == "" {
		conversationRequestID = workBuddyRequestID()
	}
	// The root is a *session* id: one per conversation, not per turn. Falling
	// back to the turn id keeps the header present, which is all a caller with
	// no session can offer.
	rootRequestID := workBuddySessionRootIDFrom(ctx)
	if rootRequestID == "" {
		rootRequestID = conversationRequestID
	}
	// Message and request ids are per request, and stay 32 hex (see the id-shape
	// note at the top of this file).
	messageID := workBuddyRequestID()

	if agentType == "" {
		agentType = workBuddyAgentTypeMain
	}

	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Requested-With", "XMLHttpRequest")
	request.Header.Set("X-Product", "SaaS")
	applyWorkBuddyStainlessHeaders(request)

	request.Header.Set("X-Conversation-ID", conversationID)
	request.Header.Set("X-Agent-Purpose", "conversation")
	request.Header.Set("X-IDE-Type", "WorkBuddy")
	request.Header.Set("X-IDE-Name", "WorkBuddy")
	request.Header.Set("X-IDE-Version", site.clientVersion())
	request.Header.Set("X-Agent-Intent", workBuddyAgentIntentCraft)
	request.Header.Set("X-Agent-Runtime", workBuddyAgentRuntimeLocal)
	request.Header.Set("X-Private-Data", workBuddyPrivateDataValue(site))
	request.Header.Set("acp-connection-id", workBuddyACPConnectionID(conversationID))
	request.Header.Set("x-codebuddy-request", "1")
	request.Header.Set("X-Request-ID", messageID)
	request.Header.Set("X-Conversation-Message-ID", messageID)
	request.Header.Set("X-Conversation-Request-ID", conversationRequestID)
	request.Header.Set("X-Root-Request-ID", rootRequestID)
	request.Header.Set("X-Agent-Type", agentType)
	request.Header.Set("baggage", workBuddyBaggage(conversationID, conversationRequestID))
	applyWorkBuddyTraceHeaders(request)

	request.Header.Set("User-Agent", site.chatUserAgent())
	if credential != nil {
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(credential.AccessToken))
		request.Header.Set("X-User-Id", strings.TrimSpace(credential.UserID))
	}
	request.Header.Set("X-Domain", workBuddyChatDomain(site, credential))

	// Token or error, always one: the client's security SDK either mints a device
	// token or reports that it could not, and a request carrying neither is a
	// state the real client never reaches.
	if token := resolveWorkBuddyDeviceToken(credential); token != "" {
		request.Header.Set("X-Device-Token", token)
	} else {
		request.Header.Set("X-Device-Token-Error", workBuddyDeviceTokenError)
	}
}

// officialWorkBuddyHeaderSpelling is the exact spelling each header goes out
// with, keyed by lower-case name.
//
// Go canonicalises header keys when they are set through Header.Set
// (`X-Request-ID` becomes `X-Request-Id`, `X-B3-ParentSpanId` becomes
// `X-B3-Parentspanid`), while the official client is a Node app whose wire bytes
// are the literals in its source. The set of headers and their values can match
// perfectly and the request still reads as a different family byte for byte.
//
// Everything is mapped by name rather than lower-cased wholesale on purpose: Go
// looks up six headers by canonical key when it writes a request (User-Agent,
// Host, Content-Length, Transfer-Encoding, Trailer, Accept-Encoding). Storing
// those lower-case makes `user-agent` invisible to that lookup, which replaces
// the carefully built User-Agent with `Go-http-client/1.1`, sends
// Content-Length twice and adds a second Accept-Encoding. Those six are mapped
// to the canonical form they already have, which is also what the client sends.
var officialWorkBuddyHeaderSpelling = map[string]string{
	"accept":            "Accept",
	"accept-encoding":   "Accept-Encoding",
	"accept-language":   "Accept-Language",
	"authorization":     "Authorization",
	"content-encoding":  "Content-Encoding",
	"content-length":    "Content-Length",
	"content-type":      "Content-Type",
	"cookie":            "Cookie",
	"origin":            "Origin",
	"referer":           "Referer",
	"user-agent":        "User-Agent",
	"x-agent-intent":    "X-Agent-Intent",
	"x-agent-purpose":   "X-Agent-Purpose",
	"x-agent-runtime":   "X-Agent-Runtime",
	"x-agent-type":      "X-Agent-Type",
	"x-b3-sampled":      "X-B3-Sampled",
	"x-domain":          "X-Domain",
	"x-private-data":    "X-Private-Data",
	"x-product":         "X-Product",
	"x-requested-with":  "X-Requested-With",
	"x-user-id":         "X-User-Id",
	"x-client-platform": "X-Client-Platform",

	// The client's literals are lower case for these, the SDK block and the
	// propagation headers among them.
	"x-stainless-arch":            "x-stainless-arch",
	"x-stainless-lang":            "x-stainless-lang",
	"x-stainless-os":              "x-stainless-os",
	"x-stainless-package-version": "x-stainless-package-version",
	"x-stainless-retry-count":     "x-stainless-retry-count",
	"x-stainless-runtime":         "x-stainless-runtime",
	"x-stainless-runtime-version": "x-stainless-runtime-version",
	"acp-connection-id":           "acp-connection-id",
	"x-codebuddy-request":         "x-codebuddy-request",
	"baggage":                     "baggage",
	"traceparent":                 "traceparent",
	"b3":                          "b3",

	// Initialisms the client keeps upper case, and that Go would flatten. These
	// are the most visible Go tell of the whole request.
	"x-conversation-id":         "X-Conversation-ID",
	"x-conversation-message-id": "X-Conversation-Message-ID",
	"x-conversation-request-id": "X-Conversation-Request-ID",
	"x-request-id":              "X-Request-ID",
	"x-root-request-id":         "X-Root-Request-ID",
	"x-trace-id":                "X-Trace-ID",
	"x-ide-type":                "X-IDE-Type",
	"x-ide-name":                "X-IDE-Name",
	"x-ide-version":             "X-IDE-Version",
	"x-b3-traceid":              "X-B3-TraceId",
	"x-b3-parentspanid":         "X-B3-ParentSpanId",
	"x-b3-spanid":               "X-B3-SpanId",
}

// normalizeWorkBuddyHeaderSpelling returns a copy of the request whose header
// keys carry the official spelling.
//
// A copy rather than an in-place rewrite: callers and tests read the request's
// headers back after sending, and Header.Get canonicalises its argument, so an
// in-place map keyed by `X-Request-ID` would make `Get("X-Request-ID")` miss.
func normalizeWorkBuddyHeaderSpelling(request *http.Request) *http.Request {
	if request == nil || len(request.Header) == 0 {
		return request
	}
	clone := request.Clone(request.Context())
	headers := make(http.Header, len(request.Header))
	for name, values := range request.Header {
		if official, ok := officialWorkBuddyHeaderSpelling[strings.ToLower(name)]; ok {
			headers[official] = values
			continue
		}
		// Unknown headers keep their own spelling: guessing is worse than
		// passing through.
		headers[name] = values
	}
	clone.Header = headers
	return clone
}
