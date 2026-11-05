package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// The media endpoints beside chat.
//
// WorkBuddy serves image generation and editing on its own routes, and — this is
// the part a generic OpenAI-compatible path gets wrong — it authenticates them with
// the **desktop client's header family**, not just the bearer token: X-User-Id,
// X-Domain, X-Product, X-IDE-*, the conversation ids and the device token all
// travel with a render exactly as they do with a chat (misakano7545/workbuddy2api-panel
// internal/upstream/images.go: "带池内真实 token" plus the same head family;
// magpie-community/plugins packages/workbuddy README §Models).
//
// This gateway's image path resolved only the bearer and posted to
// `<base>/images/generations`, where `<base>` is the chat base — which already ends
// in `/v2` — so a WorkBuddy account could not render at all. These helpers are the
// missing half: the account's endpoint root, the right route, and the full head.
//
// Paths are built from the site's own root rather than the provider row: the row's
// base URL carries `/v2` for chat, and appending `/images/generations` to that would
// double the prefix.

const (
	workBuddyImageGenerationsPath = "/v2/images/generations"
	workBuddyImageEditsPath       = "/v2/images/edits"
	workBuddyEmbeddingsPath       = "/v2/embeddings"
)

// workBuddyEndpointURL joins the account's root with one of its routes.
func workBuddyEndpointURL(site workBuddySite, path string) string {
	return strings.TrimRight(site.api(), "/") + path
}

// workBuddyEndpointSite resolves the build a provider belongs to.
func workBuddyEndpointSite(provider domain.Provider) (workBuddySite, bool) {
	return workBuddySiteForAuthType(provider.AuthType)
}

// newWorkBuddyEndpointRequest builds one request with the desktop client's head.
//
// applyWorkBuddyChatHeaders is reused on purpose: the upstream reads the same
// identity fields here as on a chat, and a second hand-rolled header set would drift
// from it the first time either side changed. What it does not set — content type and
// accept — is set here, because a render answers JSON while a chat answers SSE.
func newWorkBuddyEndpointRequest(
	ctx context.Context,
	provider domain.Provider,
	url string,
	body []byte,
	contentType string,
	accept string,
) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/json"
	}
	if strings.TrimSpace(accept) == "" {
		accept = "application/json"
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", accept)
	site, ok := workBuddyEndpointSite(provider)
	if !ok {
		return nil, fmt.Errorf("provider %q is not a WorkBuddy account", provider.ID)
	}
	credential := provider.WorkBuddy
	applyWorkBuddyChatHeaders(request, site, credential, workBuddyAgentTypeMain)
	// The chat head sets Accept: application/json; a render wants the same, but the
	// caller may have asked for something else (an edit can answer multipart), so the
	// value above wins.
	request.Header.Set("Accept", accept)
	request.Header.Set("Content-Type", contentType)
	return request, nil
}

// workBuddyEndpointCall sends one account-authenticated request and hands back the
// response with its body already read.
//
// The body is read rather than streamed because every one of these routes answers a
// single JSON document, and the console wants the vendor's own message on a refusal.
func (s *Server) workBuddyEndpointCall(
	ctx context.Context,
	provider domain.Provider,
	url string,
	body []byte,
	contentType string,
	accept string,
) (int, []byte, error) {
	request, err := newWorkBuddyEndpointRequest(ctx, provider, url, body, contentType, accept)
	if err != nil {
		return 0, nil, err
	}
	client := workBuddyChatClient(provider)
	response, err := client.Do(workBuddyRequestExit(request))
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if readErr != nil {
		return response.StatusCode, nil, readErr
	}
	return response.StatusCode, payload, nil
}

// workBuddyRenderSite reports whether this provider renders through the WorkBuddy
// routes at all. Both builds serve them, so the answer is simply "is this account
// one of ours".
func workBuddyRenderSite(provider domain.Provider) (workBuddySite, bool) {
	return workBuddySiteForAuthType(provider.AuthType)
}

// executeWorkBuddyEndpoint calls one of the image routes and answers like the generic
// image executor does.
func (s *Server) executeWorkBuddyEndpoint(
	w http.ResponseWriter,
	r *http.Request,
	provider domain.Provider,
	path string,
	body []byte,
) (int, TokenUsage, []byte, error) {
	site, ok := workBuddyRenderSite(provider)
	if !ok {
		return 0, TokenUsage{}, nil, fmt.Errorf("provider %q is not a WorkBuddy account", provider.ID)
	}
	if path == workBuddyImageEditsPath {
		body = normalizeWorkBuddyImageEditBody(body)
	}
	status, payload, err := s.workBuddyEndpointCall(r.Context(), provider, workBuddyEndpointURL(site, path), body, "application/json", "application/json")
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	// The status and body go back as they came, refusal included: the vendor's own
	// words are what let a client tell a bad prompt (400) from a dead account (401)
	// from a busy one (429). Wrapping them into a gateway error turned every one of
	// those into an opaque 502.
	//
	// The body is written here because this path owns its response: the generic image
	// executor writes through proxyOpenAIImagesUpstream, and its caller only logs.
	if w != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write(payload)
	}
	return status, TokenUsage{}, payload, nil
}

// workBuddyImagesEditsKey marks a request that came in on /v1/images/edits.
//
// The edits route shares the generation handler — route resolution, key matching and
// the refusal logic are identical — so the only thing that has to travel is which
// upstream route to post to.
type workBuddyImagesEditsKey struct{}

// withWorkBuddyImagesEdits marks the request as an edit.
func withWorkBuddyImagesEdits(ctx context.Context) context.Context {
	return context.WithValue(ctx, workBuddyImagesEditsKey{}, true)
}

// workBuddyIsImagesEdit reports the mark.
func workBuddyIsImagesEdit(ctx context.Context) bool {
	marked, _ := ctx.Value(workBuddyImagesEditsKey{}).(bool)
	return marked
}

// workBuddyImagePathFor picks the render route for a request.
func workBuddyImagePathFor(ctx context.Context) string {
	if workBuddyIsImagesEdit(ctx) {
		return workBuddyImageEditsPath
	}
	return workBuddyImageGenerationsPath
}

// normalizeWorkBuddyImageEditBody makes an edit body the shape the upstream parses.
//
// Measured live (2026-11-05): a single `image` string is refused with
// `code 11101 "cannot unmarshal string into Go struct field EditImageRequest.image of
// type []string"`. The official client always sends the array form, so a client that
// sends one image as a string is the shape that fails — wrapping it in a one-element
// array is the whole fix, and it changes nothing else.
func normalizeWorkBuddyImageEditBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var payload map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil || payload == nil {
		return body
	}
	image, has := payload["image"]
	if !has {
		return body
	}
	if _, isString := image.(string); !isString {
		return body
	}
	payload["image"] = []any{image}
	encoded := encodeJSONWithoutEscaping(payload)
	if encoded == nil {
		return body
	}
	return encoded
}
