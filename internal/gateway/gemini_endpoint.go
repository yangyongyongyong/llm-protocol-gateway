package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// handleGeminiGenerateContent serves Google's native generateContent endpoint:
//
//	POST /v1beta/models/{model}:generateContent
//	POST /v1beta/models/{model}:streamGenerateContent
//
// The model is in the path (Google's shape), the body is Gemini's own, and the
// answer is either one JSON response or an SSE stream of chunks. Everything
// else — key resolution, route output protocol, key quota, the account's lane —
// is the same as the other model entry points.
func (s *Server) handleGeminiGenerateContent(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	// ServeMux wildcards can't express Gemini's "/{model}:{action}" tail, so the
	// path is parsed here.
	model, action, ok := parseGeminiPath(r.URL.Path)
	if !ok {
		writeGeminiError(w, http.StatusNotFound, fmt.Sprintf("unknown Gemini endpoint %q", r.URL.Path))
		return
	}
	stream := action == "streamGenerateContent"

	body, tooLarge, err := readGatewayRequestBody(r)
	if tooLarge {
		writeGeminiError(w, http.StatusRequestEntityTooLarge, requestBodyTooLargeMessage)
		return
	}
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	var geminiReq map[string]any
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &geminiReq); err != nil {
			writeGeminiError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
	} else {
		geminiReq = map[string]any{}
	}
	if stream {
		geminiReq["stream"] = true
	}

	var route domain.Route
	var matchedKey domain.APIKey
	var gatewayKeyMatched bool
	if testRouteID, ok := internalTestRouteID(r); ok {
		route, err = s.router.RouteByID(testRouteID)
	} else if token := extractGeminiAPIKey(r); token != "" {
		if key, ok := s.router.APIKeyByToken(token); ok {
			matchedKey = key
			gatewayKeyMatched = true
			if key.RouteID != "" {
				route, err = s.router.RouteByID(key.RouteID)
			} else {
				err = fmt.Errorf("api key %q has no route configured", key.ID)
			}
			if s.apiKeyStore != nil {
				s.apiKeyToucher.Touch(key.ID)
			}
		} else {
			err = fmt.Errorf("invalid api key")
		}
	} else {
		err = fmt.Errorf("missing api key")
	}
	if err != nil {
		writeGeminiError(w, http.StatusUnauthorized, err.Error())
		return
	}

	if gatewayKeyMatched && route.OutputProtocol != domain.ProtocolGemini {
		writeGeminiError(w, http.StatusBadRequest, fmt.Sprintf("API key is bound to route %q with output protocol %s, but this endpoint is Gemini", route.ID, route.OutputProtocol.DisplayName()))
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, route.ProviderID, "", "rejected", route.OutputProtocol.DisplayName()+" endpoint mismatch", r.URL.Path, http.StatusBadRequest, TokenUsage{}, body, nil)
		return
	}

	logModel := model
	if gatewayKeyMatched && keyHasQuota(matchedKey) {
		// The reservation is what keeps N concurrent requests from all fitting
		// under the remaining quota at once: with a prompt of 0 (as this call
		// used to pass) only finished usage gates the key, and a burst on a key
		// at 900/1000 tokens all goes through. Every other protocol entry
		// reserves the real prompt estimate.
		admission := s.admitKeyQuota(matchedKey, estimateKeyQuotaPrompt(geminiReq), time.Now())
		if refusal := admission.refusal; refusal != nil {
			writeGeminiError(w, http.StatusTooManyRequests, refusal.Message)
			s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, route.ProviderID, logModel, "convert", "gemini", r.URL.Path, http.StatusTooManyRequests, TokenUsage{}, body, []byte(refusal.Message))
			return
		}
		defer admission.release()
	}

	decision, err := s.router.Decide(route.ID)
	if err != nil {
		writeGeminiError(w, http.StatusBadGateway, err.Error())
		return
	}
	status, usage, responseLog, execErr := s.executeProtocolFlow(w, r, route, decision, model, geminiReq, domain.ProtocolGemini, false)
	if execErr != nil {
		requestLogLevel := "error"
		s.logs.AddApp(requestLogLevel, "gemini request failed", execErr.Error())
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, "convert", "gemini", r.URL.Path, upstreamErrorStatus(execErr), usage, body, []byte(execErr.Error()))
		// The flow already wrote the body when it reached the upstream; only a
		// pre-flight failure leaves the response untouched.
		if status == 0 {
			writeGeminiError(w, upstreamErrorStatus(execErr), execErr.Error())
		}
		return
	}
	_ = responseLog
	s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, "convert", "gemini", r.URL.Path, status, usage, body, responseLog)
}

// extractGeminiAPIKey also accepts Google's ?key= form. The key is read from the
// query only here: no other entry point takes a credential from a URL, where it
// would land in logs.
func extractGeminiAPIKey(r *http.Request) string {
	if key := extractConsumerAPIKey(r); key != "" {
		return key
	}
	return strings.TrimSpace(r.URL.Query().Get("key"))
}

// parseGeminiPath splits "/v1beta/models/<model>:<action>" (and the "/v1"
// alias Google also accepts). The model may itself contain slashes or colons
// for tuned models, so the action is split off the last colon.
func parseGeminiPath(path string) (string, string, bool) {
	trimmed := strings.TrimSpace(path)
	for _, prefix := range []string{"/v1beta/models/", "/v1/models/", "/gemini/v1beta/models/", "/gemini/v1/models/"} {
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		tail := strings.TrimPrefix(trimmed, prefix)
		index := strings.LastIndex(tail, ":")
		if index <= 0 || index == len(tail)-1 {
			return "", "", false
		}
		model := tail[:index]
		action := tail[index+1:]
		switch action {
		case "generateContent", "streamGenerateContent":
			return model, action, true
		}
		return "", "", false
	}
	return "", "", false
}

// writeGeminiError writes Google's error envelope: {"error": {"code",
// "message", "status"}}. Google's SDKs read the code from there, so a gateway
// failure looks like any other API failure to them.
func writeGeminiError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
			"status":  geminiErrorStatus(code),
		},
	})
}

func geminiErrorStatus(code int) string {
	switch code {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case http.StatusRequestEntityTooLarge:
		return "INVALID_ARGUMENT"
	}
	return "INTERNAL"
}
