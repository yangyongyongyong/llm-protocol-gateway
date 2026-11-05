package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// The two agent tools beside chat: web search and page fetch.
//
// Both live on the chat domain and both answer **without** the `{code,msg,data}`
// envelope the rest of the account surface uses — the body is the result itself
// (misakano7545/workbuddy2api-panel internal/upstream/search.go):
//
//	POST /agenttool/v1/search   {query, type:"text2text", max_results:5}
//	  → {query, type, provider, results:[{title,url,snippet,site}], total_results, response_time_ms}
//	POST /agenttool/v1/webfetch {url}
//	  → {url, title, content}   (content is markdown)
//
// Three measured rules decide this file's shape:
//
//   - `provider` is a **response** field. Sending it at all is refused
//     (`code 15003 unsupported provider`), so the request body is rebuilt here from
//     the two fields that are legal rather than forwarded — a client that echoes a
//     previous response back cannot poison its next request.
//   - `type` is only legal as lower-case `text2text`; the server fills it in when it
//     is absent, and this gateway always writes it.
//   - `max_results` is an upper bound, not a promise (the server caps around 10), and
//     the official client sends 5.
//
// Because there is no envelope, the vendor's answer is passed through **as-is**: a
// wrapper invented here would be a different API from the one the client's client was
// written against.

const (
	workBuddySearchPath   = "/agenttool/v1/search"
	workBuddyWebFetchPath = "/agenttool/v1/webfetch"
	// workBuddySearchOfficialResults is the count the official client asks for.
	workBuddySearchOfficialResults = 5
	// workBuddySearchMaxResultsCap mirrors the server's own hard cap.
	workBuddySearchMaxResultsCap = 10
)

// workBuddySearchRequest rebuilds a search body from what the client may legally say.
//
// Only `query` and `max_results` survive. `type` is written here (lower-case
// `text2text`), and `provider` is dropped whatever it was — that field is the one the
// upstream refuses outright.
func workBuddySearchRequest(body []byte) (map[string]any, bool) {
	var incoming struct {
		Query      string      `json:"query"`
		MaxResults json.Number `json:"max_results"`
	}
	if len(body) > 0 {
		if json.Unmarshal(body, &incoming) != nil {
			return nil, false
		}
	}
	query := strings.TrimSpace(incoming.Query)
	if query == "" {
		return nil, false
	}
	maxResults := int64(workBuddySearchOfficialResults)
	// Zero means absent; only a number the client actually sent overrides 5.
	if value := workBuddyInt64Of(incoming.MaxResults); value > 0 {
		maxResults = value
	}
	if maxResults > workBuddySearchMaxResultsCap {
		maxResults = workBuddySearchMaxResultsCap
	}
	return map[string]any{
		"query":       query,
		"type":        "text2text",
		"max_results": maxResults,
	}, true
}

// workBuddyWebFetchRequest rebuilds a fetch body: one url, nothing else.
func workBuddyWebFetchRequest(body []byte) (map[string]any, bool) {
	if len(body) == 0 {
		return nil, false
	}
	var incoming struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(body, &incoming) != nil {
		return nil, false
	}
	target := strings.TrimSpace(incoming.URL)
	if target == "" {
		return nil, false
	}
	return map[string]any{"url": target}, true
}

// executeWorkBuddyAgentTool calls one agent tool and writes the vendor's answer
// through unchanged.
//
// No envelope is expected on the way back, so nothing is unwrapped: a refusal arrives
// as the vendor's own status and body, and a success arrives as the result document.
func (s *Server) executeWorkBuddyAgentTool(
	w http.ResponseWriter,
	r *http.Request,
	provider domain.Provider,
	path string,
	body []byte,
) (int, TokenUsage, []byte, error) {
	site, ok := workBuddySiteForAuthType(provider.AuthType)
	if !ok {
		return 0, TokenUsage{}, nil, errWorkBuddyNotAnAccount
	}
	status, payload, err := s.workBuddyEndpointCall(r.Context(), provider,
		workBuddyEndpointURL(site, path), body, "application/json", "application/json")
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	if w != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write(payload)
	}
	return status, TokenUsage{}, payload, nil
}

// handleWorkBuddySearch serves POST /v1/search.
func (s *Server) handleWorkBuddySearch(w http.ResponseWriter, r *http.Request) {
	body, err := readGatewayRequestBodyLimited(r, 1<<20)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	request, ok := workBuddySearchRequest(body)
	if !ok {
		writeOpenAIError(w, http.StatusBadRequest, "query is required")
		return
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "failed to build search request")
		return
	}
	s.serveWorkBuddyTool(w, r, workBuddySearchPath, encoded)
}

// handleWorkBuddyWebFetch serves POST /v1/webfetch.
func (s *Server) handleWorkBuddyWebFetch(w http.ResponseWriter, r *http.Request) {
	body, err := readGatewayRequestBodyLimited(r, 1<<20)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	request, ok := workBuddyWebFetchRequest(body)
	if !ok {
		writeOpenAIError(w, http.StatusBadRequest, "url is required")
		return
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "failed to build fetch request")
		return
	}
	s.serveWorkBuddyTool(w, r, workBuddyWebFetchPath, encoded)
}

// serveWorkBuddyTool runs one agent tool through the shared media-route machinery,
// which already resolves the key and the account for these endpoints.
func (s *Server) serveWorkBuddyTool(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	r.Body = io.NopCloser(bytesReader(body))
	r = r.WithContext(withWorkBuddyMediaRoute(r.Context(), workBuddyMediaRoute{path: path}))
	s.handleOpenAIImagesGenerations(w, r)
}

// readGatewayRequestBodyLimited reads a body with an explicit cap.
func readGatewayRequestBodyLimited(r *http.Request, limit int64) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	buffer := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	for int64(len(buffer)) < limit {
		read, err := r.Body.Read(chunk)
		if read > 0 {
			buffer = append(buffer, chunk[:read]...)
		}
		if err != nil {
			break
		}
	}
	if int64(len(buffer)) > limit {
		buffer = buffer[:limit]
	}
	return buffer, nil
}
