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

// Protocol conversion orchestration.
//
// Isolation rules:
//   - Transport/auth (cursor-bridge vs Claude OAuth) is provider-specific.
//   - Chat→Responses stream engine is SHARED; inject quirks via
//     ChatToResponsesStreamOptions (Cursor vs Standard). Never hardcode
//     Cursor-only behavior into the default path used by Claude→Responses.

func resolveProviderResponsesURL(provider domain.Provider, model string) string {
	resolvedModel := strings.TrimSpace(model)
	if resolvedModel == "" {
		resolvedModel = strings.TrimSpace(provider.DefaultModel)
	}
	if resolvedModel == "" {
		resolvedModel = "request-model-not-set"
	}
	upstreamURL := strings.ReplaceAll(strings.TrimSpace(provider.BaseURL), "{model}", resolvedModel)
	lowerURL := strings.ToLower(upstreamURL)
	if provider.Protocol == domain.ProtocolOpenAIResponses && !strings.Contains(lowerURL, "/responses") && !strings.Contains(provider.BaseURL, "{model}") {
		upstreamURL = strings.TrimRight(upstreamURL, "/") + "/responses"
	}
	return upstreamURL
}

func (s *Server) doOpenAIProviderRequest(ctx context.Context, r *http.Request, provider domain.Provider, upstreamURL string, body []byte, accept string, skipIncomingAuth bool) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if accept != "" {
		request.Header.Set("Accept", accept)
	} else {
		request.Header.Set("Accept", "application/json")
	}
	applyProviderAuth(request, provider, func() string {
		if skipIncomingAuth {
			return ""
		}
		return r.Header.Get("Authorization")
	}())
	// Extract model from body for header placeholder substitution when possible.
	model := ""
	var payload map[string]any
	if json.Unmarshal(body, &payload) == nil {
		model, _ = payload["model"].(string)
	}
	applyRequestAdapterHeaders(request, provider, model)
	return doHTTPWithTiming(ctx, &http.Client{Timeout: 0}, request)
}

func (s *Server) proxyOpenAIResponses(w http.ResponseWriter, r *http.Request, provider domain.Provider, model string, body []byte, skipIncomingAuth bool) (int, TokenUsage, []byte, error) {
	upstreamURL := resolveProviderResponsesURL(provider, model)
	response, err := s.doOpenAIProviderRequest(r.Context(), r, provider, upstreamURL, body, r.Header.Get("Accept"), skipIncomingAuth)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	defer response.Body.Close()
	return writePassThroughResponse(w, response, requestBodyWantsStream(body), ParseResponsesUsage)
}

func (s *Server) proxyResponsesToOpenAIChat(w http.ResponseWriter, r *http.Request, provider domain.Provider, model string, openAIReq map[string]any, skipIncomingAuth bool) (int, TokenUsage, []byte, error) {
	responsesReq, err := openAIChatToResponsesRequest(openAIReq, model)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	return s.proxyConvertedThroughResponses(w, r, provider, model, responsesReq, skipIncomingAuth, responsesToOpenAIChatResponse, streamResponsesToOpenAIChatEvents)
}

func (s *Server) proxyOpenAIChatToResponses(w http.ResponseWriter, r *http.Request, provider domain.Provider, model string, responsesReq map[string]any, skipIncomingAuth bool) (int, TokenUsage, []byte, error) {
	chatReq, err := responsesToOpenAIChatRequest(responsesReq, model)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	// Cursor OAuth injects bridge quirks; other openai_chat providers stay standard
	// so Claude→Responses (and generic Chat→Responses) are not coupled to Cursor fixes.
	opts := StandardChatToResponsesStreamOptions()
	if provider.AuthType == domain.AuthTypeCursorOAuth {
		opts = CursorChatToResponsesStreamOptions()
	}
	streamConvert := func(w http.ResponseWriter, reader io.Reader, model string) (TokenUsage, error) {
		return streamOpenAIChatToResponsesEventsWithOptions(w, reader, model, opts)
	}
	return s.proxyConvertedThroughChat(w, r, provider, model, chatReq, skipIncomingAuth, openAIChatToResponsesResponse, streamConvert)
}

func (s *Server) proxyResponsesToClaudeMessages(w http.ResponseWriter, r *http.Request, provider domain.Provider, model string, claudeReq map[string]any, skipIncomingAuth bool) (int, TokenUsage, []byte, error) {
	// Direct Claude↔Responses conversion (no Chat IR hop).
	responsesReq, err := claudeToResponsesRequestDirect(claudeReq, model)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	return s.proxyConvertedThroughResponses(w, r, provider, model, responsesReq, skipIncomingAuth, responsesToClaudeResponseDirect, streamResponsesToClaudeEventsDirect)
}

func (s *Server) proxyClaudeToResponses(w http.ResponseWriter, r *http.Request, provider domain.Provider, model string, responsesReq map[string]any, skipIncomingAuth bool) (int, TokenUsage, []byte, error) {
	// Capture the tool names the client (e.g. Codex) actually registered so the
	// response hop can restore them. Claude OAuth cloaking renames tools to
	// TitleCase upstream (exec_command → ExecCommand); without the original
	// names the client receives an unknown tool and reports "unsupported call".
	clientToolNames := extractOpenAIToolNames(responsesReq)
	claudeReq, err := responsesToClaudeRequestDirect(responsesReq, model)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	return s.proxyConvertedThroughClaude(w, r, provider, model, claudeReq, skipIncomingAuth, func(claudeBody []byte, model string) ([]byte, TokenUsage, error) {
		return claudeToResponsesResponseDirect(claudeBody, model, clientToolNames)
	}, func(w http.ResponseWriter, reader io.Reader, model string) (TokenUsage, error) {
		return streamClaudeToResponsesEventsDirect(w, reader, model, clientToolNames)
	})
}

type bufferResponseWriter struct {
	buf bytes.Buffer
}

func (b *bufferResponseWriter) Header() http.Header { return http.Header{} }
func (b *bufferResponseWriter) WriteHeader(int)     {}
func (b *bufferResponseWriter) Write(p []byte) (int, error) {
	return b.buf.Write(p)
}

// pipeResponseWriter adapts an io.Writer (the write end of an io.Pipe) into an
// http.ResponseWriter so a stream converter can write its first-hop output
// directly into the pipe. Flush is a no-op: io.Pipe writes are synchronous
// (they block until the reader consumes), so backpressure and real-time
// delivery are inherent.
type pipeResponseWriter struct {
	w io.Writer
}

func (p *pipeResponseWriter) Header() http.Header       { return http.Header{} }
func (p *pipeResponseWriter) WriteHeader(int)           {}
func (p *pipeResponseWriter) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *pipeResponseWriter) Flush()                    {}

// streamTwoHop pipes a two-hop streaming protocol conversion so the second hop
// consumes the first hop's SSE output incrementally instead of waiting for the
// whole first hop to buffer (the old bufferResponseWriter approach, which
// destroyed TTFT on dual-hop flows like Claude↔Responses). The first hop runs
// in a goroutine writing into an io.Pipe; the second hop reads from it and
// writes to the real client w.
//
// Usage precedence matches the previous buffered behavior: on first-hop error,
// return its usage+error; otherwise prefer the first hop's token usage unless
// it is zero, then fall back to the second hop's usage; the returned error is
// the second hop's.
func streamTwoHop(w http.ResponseWriter, upstream io.Reader, model string, firstHop, secondHop streamConverter) (TokenUsage, error) {
	pr, pw := io.Pipe()
	var (
		firstUsage TokenUsage
		firstErr   error
		done       = make(chan struct{})
	)
	go func() {
		defer close(done)
		firstUsage, firstErr = firstHop(&pipeResponseWriter{w: pw}, upstream, model)
		if firstErr != nil {
			_ = pw.CloseWithError(firstErr)
			return
		}
		_ = pw.Close()
	}()

	secondUsage, secondErr := secondHop(w, pr, model)
	// Unblock the first-hop goroutine if the second hop stopped reading early.
	_ = pr.CloseWithError(secondErr)
	<-done

	if firstErr != nil {
		return firstUsage, firstErr
	}
	if firstUsage.InputTokens == 0 && firstUsage.OutputTokens == 0 {
		return secondUsage, secondErr
	}
	return firstUsage, secondErr
}

type streamBridgeRecorder struct {
	http.ResponseWriter
	body bytes.Buffer
}

func (r *streamBridgeRecorder) Write(p []byte) (int, error) {
	r.body.Write(p)
	return len(p), nil
}

type responseConverter func([]byte, string) ([]byte, TokenUsage, error)
type streamConverter func(http.ResponseWriter, io.Reader, string) (TokenUsage, error)

func (s *Server) proxyConvertedThroughChat(w http.ResponseWriter, r *http.Request, provider domain.Provider, model string, chatReq map[string]any, skipIncomingAuth bool, convert responseConverter, streamConvert streamConverter) (int, TokenUsage, []byte, error) {
	stream, _ := chatReq["stream"].(bool)
	upstreamBody, err := json.Marshal(chatReq)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	accept := "application/json"
	if stream {
		accept = "text/event-stream"
	}
	model = applyProviderModelMapping(provider, model)
	var response *http.Response
	if provider.AuthType == domain.AuthTypeCursorOAuth {
		baseURL, refreshed, bridgeErr := s.resolveCursorBridgeBaseURL(r.Context(), provider)
		if bridgeErr != nil {
			return 0, TokenUsage{}, nil, bridgeErr
		}
		provider = refreshed
		upstreamURL := strings.TrimRight(baseURL, "/") + "/chat/completions"
		request, reqErr := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(upstreamBody))
		if reqErr != nil {
			return 0, TokenUsage{}, nil, reqErr
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", accept)
		response, err = doHTTPWithTiming(r.Context(), &http.Client{Timeout: 0}, request)
		if err != nil {
			return 0, TokenUsage{}, nil, err
		}
	} else {
		upstreamBody, bodyErr := applyRequestAdapterBody(provider, model, upstreamBody)
		if bodyErr != nil {
			return 0, TokenUsage{}, nil, bodyErr
		}
		response, err = s.doOpenAIProviderRequest(r.Context(), r, provider, resolveProviderChatURLWithAdapter(provider, model), upstreamBody, accept, skipIncomingAuth)
		if err != nil {
			return 0, TokenUsage{}, nil, err
		}
	}
	defer response.Body.Close()
	return s.finishConvertedProxy(w, response, model, stream, convert, streamConvert)
}

func (s *Server) proxyConvertedThroughResponses(w http.ResponseWriter, r *http.Request, provider domain.Provider, model string, responsesReq map[string]any, skipIncomingAuth bool, convert responseConverter, streamConvert streamConverter) (int, TokenUsage, []byte, error) {
	stream, _ := responsesReq["stream"].(bool)
	upstreamBody, err := json.Marshal(responsesReq)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	accept := "application/json"
	if stream {
		accept = "text/event-stream"
	}
	response, err := s.doOpenAIProviderRequest(r.Context(), r, provider, resolveProviderResponsesURL(provider, model), upstreamBody, accept, skipIncomingAuth)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	defer response.Body.Close()
	return s.finishConvertedProxy(w, response, model, stream, convert, streamConvert)
}

func (s *Server) proxyConvertedThroughClaude(w http.ResponseWriter, r *http.Request, provider domain.Provider, model string, claudeReq map[string]any, skipIncomingAuth bool, convert responseConverter, streamConvert streamConverter) (int, TokenUsage, []byte, error) {
	_ = skipIncomingAuth
	stream, _ := claudeReq["stream"].(bool)
	upstreamBody, err := json.Marshal(claudeReq)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	accept := "application/json"
	if stream {
		accept = "text/event-stream"
	}
	response, err := s.doClaudeProviderRequest(r.Context(), r, provider, upstreamBody, accept)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	defer response.Body.Close()
	return s.finishConvertedProxy(w, response, model, stream, convert, streamConvert)
}

func (s *Server) finishConvertedProxy(w http.ResponseWriter, response *http.Response, model string, stream bool, convert responseConverter, streamConvert streamConverter) (int, TokenUsage, []byte, error) {
	if stream {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(response.StatusCode)
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			responseBody, readErr := io.ReadAll(response.Body)
			if readErr != nil {
				return response.StatusCode, TokenUsage{}, nil, readErr
			}
			converted, _, convErr := convert(responseBody, model)
			if convErr != nil || len(converted) == 0 {
				_, writeErr := w.Write(responseBody)
				return response.StatusCode, TokenUsage{}, responseBody, writeErr
			}
			_, writeErr := w.Write(converted)
			return response.StatusCode, TokenUsage{}, converted, writeErr
		}
		usage, streamErr := streamConvert(w, response.Body, model)
		return response.StatusCode, usage, nil, streamErr
	}

	responseBody, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return 0, TokenUsage{}, nil, readErr
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		converted, _, convErr := convert(responseBody, model)
		if convErr != nil || len(converted) == 0 {
			w.WriteHeader(response.StatusCode)
			_, writeErr := w.Write(responseBody)
			return response.StatusCode, TokenUsage{}, responseBody, writeErr
		}
		w.WriteHeader(response.StatusCode)
		_, writeErr := w.Write(converted)
		return response.StatusCode, TokenUsage{}, converted, writeErr
	}
	converted, usage, err := convert(responseBody, model)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(converted)))
	w.WriteHeader(http.StatusOK)
	if _, writeErr := w.Write(converted); writeErr != nil {
		return http.StatusOK, usage, converted, writeErr
	}
	return http.StatusOK, usage, converted, nil
}

func (s *Server) executeProtocolFlow(
	w http.ResponseWriter,
	r *http.Request,
	route domain.Route,
	decision domain.RouteDecision,
	model string,
	req map[string]any,
	clientProtocol domain.Protocol,
	skipIncomingAuth bool,
) (int, TokenUsage, []byte, error) {
	providerID := strings.TrimSpace(decision.ProviderID)
	if providerID == "" {
		providerID = route.ProviderID
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}

	if decision.Action == "pass_through" {
		body, err := json.Marshal(req)
		if err != nil {
			return 0, TokenUsage{}, nil, err
		}
		switch decision.InputProtocol {
		case domain.ProtocolOpenAIChat:
			return s.proxyOpenAIChat(w, r, provider, model, body, skipIncomingAuth)
		case domain.ProtocolOpenAIResponses:
			return s.proxyOpenAIResponses(w, r, provider, model, body, skipIncomingAuth)
		case domain.ProtocolClaude:
			return s.proxyClaudeMessages(w, r, provider, body)
		default:
			return 0, TokenUsage{}, nil, fmt.Errorf("unsupported provider protocol %s", decision.InputProtocol)
		}
	}

	switch clientProtocol {
	case domain.ProtocolOpenAIChat:
		switch decision.InputProtocol {
		case domain.ProtocolClaude:
			return s.proxyClaudeToOpenAIChat(w, r, provider, model, req, skipIncomingAuth)
		case domain.ProtocolOpenAIResponses:
			return s.proxyResponsesToOpenAIChat(w, r, provider, model, req, skipIncomingAuth)
		}
	case domain.ProtocolClaude:
		switch decision.InputProtocol {
		case domain.ProtocolOpenAIChat:
			return s.proxyOpenAIToClaudeMessages(w, r, provider, model, req, skipIncomingAuth)
		case domain.ProtocolOpenAIResponses:
			return s.proxyResponsesToClaudeMessages(w, r, provider, model, req, skipIncomingAuth)
		}
	case domain.ProtocolOpenAIResponses:
		switch decision.InputProtocol {
		case domain.ProtocolOpenAIChat:
			return s.proxyOpenAIChatToResponses(w, r, provider, model, req, skipIncomingAuth)
		case domain.ProtocolClaude:
			return s.proxyClaudeToResponses(w, r, provider, model, req, skipIncomingAuth)
		}
	}
	return 0, TokenUsage{}, nil, fmt.Errorf("protocol conversion is not implemented: %s", decision.ConversionLabel)
}
