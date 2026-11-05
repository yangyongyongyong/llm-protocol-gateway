package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	if isWorkBuddyProvider(provider) {
		// The three ids are pinned here, outside every retry a closure below may
		// make, so all attempts of one user send carry the same values (see
		// applyWorkBuddyChatHeaders).
		turn := workBuddyRequestID()
		ctx = withWorkBuddyTurnRequestID(ctx, turn)
		// Conversation and root are session-scoped, not turn-scoped: reuse the
		// client's own session header when it sent one, so a conversation keeps
		// its ids across turns instead of looking like a new conversation on
		// every request.
		session := requestSessionID(r, nil)
		if session == "" {
			session = turn
		}
		ctx = withWorkBuddyConversationID(ctx,
			workBuddyStableUUID("conversation:"+provider.ID+":"+session))
		ctx = withWorkBuddySessionRootID(ctx,
			workBuddyStableHex("root:"+provider.ID+":"+session))
	}
	if isTraeProvider(provider) {
		// Trae is not an OpenAI endpoint: its agent function is chosen per
		// model and its reply is Trae's own event stream. sendTraeChat shapes
		// the request, retries the other functions when Trae says the model is
		// served elsewhere, and hands back an OpenAI-shaped response, so every
		// converter below sees what it already knows how to read.
		model := chatBodyModel(body)
		if model == "" {
			model = strings.TrimSpace(provider.DefaultModel)
		}
		response, _, err := s.sendTraeChat(ctx, r, provider, model, body, skipIncomingAuth)
		if response == nil {
			return nil, err
		}
		return response, nil
	}
	if isQoderProvider(provider) {
		// Qoder is the same case: its agent takes a signed request in its own
		// codec and answers in its own envelopes, and sendQoderChat hands back an
		// OpenAI-shaped response so the converters below see what they already
		// know how to read.
		qoderModel := chatBodyModel(body)
		if qoderModel == "" {
			qoderModel = strings.TrimSpace(provider.DefaultModel)
		}
		response, err := s.sendQoderChat(ctx, provider, qoderModel, body)
		if response == nil {
			return nil, err
		}
		return response, nil
	}
	if provider.AuthType == domain.AuthTypeChatGPTOAuth {
		refreshed, err := s.ensureFreshChatGPTToken(provider)
		if err != nil {
			return nil, err
		}
		provider = refreshed
		prepared, _, prepErr := prepareChatGPTCodexRequestBody(body, codexForceFastFrom(ctx))
		if prepErr != nil {
			return nil, prepErr
		}
		body = prepared
		accept = "text/event-stream"
	}
	// A call that came with no id would otherwise be replayed as "" forever,
	// which strict upstreams refuse for the whole conversation.
	body = chatToolCallIDs(body)
	var workBuddyAliases map[string]string
	if isWorkBuddyProvider(provider) {
		// Both WorkBuddy builds require a leading system message and a
		// streaming request; idempotent, so a caller that already shaped the
		// body is unaffected. The aliases it returns name the model's calls
		// back to the client's own names.
		body, workBuddyAliases, _ = shapeWorkBuddyChatBody(body)
		// The client writes the round id into the body as well as the head, and
		// leans on the upstream prefix cache across a conversation's turns (see
		// workbuddy_body_fields.go).
		body = applyWorkBuddyBodyFields(body, workBuddyBodyFieldOptions{
			userID:                workBuddyCredentialUserID(provider),
			conversationID:        workBuddyConversationIDFrom(ctx),
			conversationRequestID: workBuddyTurnRequestIDFrom(ctx),
		})
	}
	if isOpenCodeZenProvider(provider) {
		// Zen's free tier is served to a streaming request that offers OpenCode's
		// two tools, so the body is shaped before the URL is asked too.
		body, _ = prepareOpenCodeZenChatBody(provider, body)
	}
	// Extract model from body for header placeholder substitution when possible.
	model := ""
	var payload map[string]any
	if json.Unmarshal(body, &payload) == nil {
		model, _ = payload["model"].(string)
	}
	incomingAuth := ""
	if !skipIncomingAuth {
		incomingAuth = r.Header.Get("Authorization")
	}
	send := func(requestBody []byte) (*http.Response, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(requestBody))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		if accept != "" {
			request.Header.Set("Accept", accept)
		} else {
			request.Header.Set("Accept", "application/json")
		}
		if provider.AuthType == domain.AuthTypeChatGPTOAuth {
			applyChatGPTCodexHeaders(request, provider)
			// Codex backend routes prompt-cache shards by session headers, not by
			// the body field: lift prompt_cache_key out of the body and isolate it
			// per ChatGPT account (sub2api isolateOpenAIUpstreamSessionID).
			if cacheKey := extractPromptCacheKeyFromBody(requestBody); cacheKey != "" {
				accountID := ""
				if provider.ChatGPTOAuth != nil {
					accountID = provider.ChatGPTOAuth.ChatGPTAccountID
				}
				isolated := chatgptSessionIDForCache(provider.ID, accountID, cacheKey)
				request.Header.Set("session_id", isolated)
				request.Header.Set("conversation_id", isolated)
				request.Header.Set("x-client-request-id", isolated)
			}
		} else {
			applyProviderAuth(request, provider, incomingAuth)
		}
		applyOpenCodeZenHeaders(request, provider, requestBody)
		applyRequestAdapterHeaders(request, provider, model)
		return doHTTPWithTiming(ctx, &http.Client{Timeout: 0}, request)
	}
	if isWorkBuddyProvider(provider) {
		// Each retry carries the provider it was decided for.
		return s.sendWorkBuddyChat(provider, model, body, workBuddyAliases, func(sendProvider domain.Provider, requestBody []byte) (*http.Response, error) {
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(requestBody))
			if err != nil {
				return nil, err
			}
			request.Header.Set("Content-Type", "application/json")
			if accept != "" {
				request.Header.Set("Accept", accept)
			} else {
				request.Header.Set("Accept", "application/json")
			}
			applyProviderAuth(request, sendProvider, incomingAuth)
			applyOpenCodeZenHeaders(request, sendProvider, requestBody)
			applyRequestAdapterHeaders(request, sendProvider, model)
			// The account's own connection pool, so no two accounts are ever seen
			// on one TCP/TLS connection, plus the CN endpoint fallback — see
			// workbuddy_transport.go.
			return doHTTPWithTiming(ctx, workBuddyChatClient(sendProvider), request)
		},
			// Only a client that wants one JSON object can have a cut answer joined up:
			// a streaming client gets bytes as they arrive, so a continuation there
			// would mean holding the whole answer back (see workbuddy_continue.go).
			workBuddyWantsAggregatedAnswer(ctx))
	}
	return send(body)
}

func (s *Server) proxyOpenAIResponses(w http.ResponseWriter, r *http.Request, provider domain.Provider, model string, body []byte, skipIncomingAuth bool) (int, TokenUsage, []byte, error) {
	upstreamURL := resolveProviderResponsesURL(provider, model)
	clientStream := requestBodyWantsStream(body)
	if provider.AuthType == domain.AuthTypeChatGPTOAuth {
		// Upstream Codex always streams; pass SSE through to the client.
		clientStream = true
	}
	response, err := s.doOpenAIProviderRequest(r.Context(), r, provider, upstreamURL, body, r.Header.Get("Accept"), skipIncomingAuth)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	// Strategy 3: when the same API key switches forwarding schemes mid-conversation
	// the provider/backend changes, but Codex replays prior-turn reasoning items whose
	// reasoning.encrypted_content was signed by the *previous* backend. A pure-passthrough
	// route forwards that ciphertext verbatim, so the new backend returns
	// invalid_encrypted_content. Detect that specific rejection, strip the reasoning
	// items from `input`, and retry once. Same-provider replays are accepted upstream,
	// so this never triggers there and reasoning continuity is preserved.
	response = s.retryResponsesWithoutReasoningIfNeeded(r.Context(), r, provider, upstreamURL, body, r.Header.Get("Accept"), skipIncomingAuth, response)
	defer response.Body.Close()
	return writePassThroughResponse(w, response, clientStream, usageStreamResponses, ParseResponsesUsage, responsesStreamInterrupter{})
}

// retryResponsesWithoutReasoningIfNeeded implements Strategy 3 for the Responses
// passthrough path (see proxyOpenAIResponses). It only acts on an upstream error
// that mentions encrypted_content; otherwise the original response is returned
// untouched (its buffered body restored so the caller can stream it as-is).
func (s *Server) retryResponsesWithoutReasoningIfNeeded(
	ctx context.Context,
	r *http.Request,
	provider domain.Provider,
	upstreamURL string,
	body []byte,
	accept string,
	skipIncomingAuth bool,
	response *http.Response,
) *http.Response {
	if response == nil || response.StatusCode < 400 {
		return response
	}
	// Error bodies are small; buffer so we can inspect and, if we do not retry,
	// still replay the exact upstream error to the client.
	errBody, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	restore := func() *http.Response {
		response.Body = io.NopCloser(bytes.NewReader(errBody))
		return response
	}
	if readErr != nil || !responsesErrorLooksLikeEncryptedContent(errBody) {
		return restore()
	}
	strippedBody, changed := stripResponsesInputReasoning(body)
	if !changed {
		return restore()
	}
	retried, err := s.doOpenAIProviderRequest(ctx, r, provider, upstreamURL, strippedBody, accept, skipIncomingAuth)
	if err != nil {
		// Retry transport failed: surface the original upstream error, not a
		// synthetic one, so the client/log sees what actually happened.
		return restore()
	}
	return retried
}

// responsesErrorLooksLikeEncryptedContent reports whether an upstream error body
// is the "replayed reasoning ciphertext this backend did not sign" failure
// (OpenAI: invalid_encrypted_content). Kept lenient so backends that phrase it
// differently still match, as long as they name encrypted_content.
func responsesErrorLooksLikeEncryptedContent(errBody []byte) bool {
	return bytes.Contains(bytes.ToLower(errBody), []byte("encrypted_content"))
}

// stripResponsesInputReasoning removes `reasoning` items (which carry the opaque
// reasoning.encrypted_content) from a Responses request `input` array, and drops
// any stray encrypted_content left on surviving items. Returns the rewritten
// body and whether anything changed.
func stripResponsesInputReasoning(body []byte) ([]byte, bool) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, false
	}
	input, ok := payload["input"].([]any)
	if !ok || len(input) == 0 {
		return body, false
	}
	filtered := make([]any, 0, len(input))
	changed := false
	for _, raw := range input {
		if item, ok := raw.(map[string]any); ok {
			itemType := strings.TrimSpace(strings.ToLower(stringValue(item["type"])))
			if itemType == "reasoning" {
				changed = true
				continue
			}
			// agent_message / inter-agent carriers may keep encrypted_content as
			// the task body. Only strip stray ciphertext on other item types
			// (OpenAI rejects foreign reasoning ciphertext).
			if itemType != "agent_message" && itemType != "inter_agent_communication" {
				if _, has := item["encrypted_content"]; has {
					delete(item, "encrypted_content")
					changed = true
				}
			}
		}
		filtered = append(filtered, raw)
	}
	if !changed {
		return body, false
	}
	payload["input"] = filtered
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return body, false
	}
	return rewritten, true
}

func (s *Server) proxyResponsesToOpenAIChat(w http.ResponseWriter, r *http.Request, provider domain.Provider, model string, openAIReq map[string]any, skipIncomingAuth bool) (int, TokenUsage, []byte, error) {
	responsesReq, err := openAIChatToResponsesRequest(openAIReq, model)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	return s.proxyConvertedThroughResponses(w, r, provider, model, responsesReq, skipIncomingAuth, responsesToOpenAIChatResponse, streamResponsesToOpenAIChatEvents)
}

func (s *Server) proxyOpenAIChatToResponses(w http.ResponseWriter, r *http.Request, provider domain.Provider, model string, responsesReq map[string]any, skipIncomingAuth bool) (int, TokenUsage, []byte, error) {
	chatReq, toolCtx, err := responsesToOpenAIChatRequest(responsesReq, model)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	r, clientStopSequences := rememberConvertedStopSequences(r, chatReq["stop"])
	// Cursor OAuth injects bridge quirks; other openai_chat providers stay standard
	// so Claude→Responses (and generic Chat→Responses) are not coupled to Cursor fixes.
	opts := StandardChatToResponsesStreamOptions()
	if provider.AuthType == domain.AuthTypeCursorOAuth {
		opts = CursorChatToResponsesStreamOptions()
	}
	opts.ToolContext = toolCtx
	streamConvert := func(w http.ResponseWriter, reader io.Reader, model string) (TokenUsage, error) {
		return streamOpenAIChatToResponsesEventsWithOptions(w, reader, model, opts)
	}
	return s.proxyConvertedThroughChat(w, r, provider, model, chatReq, skipIncomingAuth, func(chatBody []byte, model string) ([]byte, TokenUsage, error) {
		body, usage, err := openAIChatToResponsesResponseWithTools(chatBody, model, toolCtx)
		return fillNonStreamStopSequence(body, clientStopSequences), usage, err
	}, streamConvert)
}

func (s *Server) proxyResponsesToClaudeMessages(w http.ResponseWriter, r *http.Request, provider domain.Provider, model string, claudeReq map[string]any, skipIncomingAuth bool) (int, TokenUsage, []byte, error) {
	// Direct Claude↔Responses conversion (no Chat IR hop).
	responsesReq, err := claudeToResponsesRequestDirect(claudeReq, model)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	// Stable per-conversation cache key keeps upstream prompt-cache shards
	// aligned across turns. ChatGPT OAuth turns this into session headers at
	// send time; plain Responses providers keep it in the body.
	if cacheKey := s.promptCacheKeyForClaudeRequest(r, claudeReq); cacheKey != "" {
		responsesReq["prompt_cache_key"] = cacheKey
	}
	// stop_sequences 不发给 Responses 上游（它没有这个参数），由网关记住并在
	// 回包时本地匹配回填：直接转换器看不到客户端原始字段，只能从这里注入。
	r, clientStopSequences := rememberConvertedStopSequences(r, claudeReq["stop_sequences"])
	return s.proxyConvertedThroughResponses(w, r, provider, model, responsesReq, skipIncomingAuth, func(body []byte, model string) ([]byte, TokenUsage, error) {
		return claudeToResponsesResponseDirect(body, model, nil, clientStopSequences...)
	}, streamResponsesToClaudeEventsDirect)
}

func (s *Server) proxyClaudeToResponses(w http.ResponseWriter, r *http.Request, provider domain.Provider, model string, responsesReq map[string]any, skipIncomingAuth bool) (int, TokenUsage, []byte, error) {
	// Capture Codex tool context (namespace / custom / tool_search flatten map)
	// so the response hop can restore identity. Also covers Claude OAuth
	// TitleCase cloaking via ClientToolNames().
	model = applyProviderModelMapping(provider, model)
	claudeReq, toolCtx, err := responsesToClaudeRequestDirect(responsesReq, model, maxOutputTokensOverrideFrom(r.Context()))
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	r, clientStopSequences := rememberConvertedStopSequences(r, claudeReq["stop_sequences"])
	return s.proxyConvertedThroughClaude(w, r, provider, model, claudeReq, skipIncomingAuth, func(claudeBody []byte, model string) ([]byte, TokenUsage, error) {
		return claudeToResponsesResponseDirect(claudeBody, model, toolCtx, clientStopSequences...)
	}, func(w http.ResponseWriter, reader io.Reader, model string) (TokenUsage, error) {
		return streamClaudeToResponsesEventsDirect(w, reader, model, toolCtx)
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

func (p *pipeResponseWriter) Header() http.Header         { return http.Header{} }
func (p *pipeResponseWriter) WriteHeader(int)             {}
func (p *pipeResponseWriter) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *pipeResponseWriter) Flush()                      {}

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
	model = applyProviderModelMapping(provider, model)
	// WorkBuddy refuses a non-stream chat and OpenCode serves its free tier to
	// a streaming request only, so both always stream upstream. The stream is
	// rebuilt into a single chat.completion before the converter runs, so a
	// client that asked for a non-streaming answer still gets one.
	forcedStream := !stream && (isWorkBuddyProvider(provider) || isTraeProvider(provider) || openCodeZenFreeFor(provider, model))
	if forcedStream {
		chatReq["stream"] = true
	}
	// The client wants one JSON object; the upstream call still streams. Marked on
	// the context so the WorkBuddy exit can rebuild and, if the answer was cut,
	// continue it (see workbuddy_continue.go).
	// The client's intent was captured at the handler entry, before conversion and
	// before the stream flag was forced to true for an upstream that refuses
	// non-stream calls (see handleClaudeMessages).
	ctx := r.Context()
	upstreamBody, err := json.Marshal(chatReq)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	accept := "application/json"
	if stream || forcedStream {
		accept = "text/event-stream"
	}
	sendUpstream := func() (*http.Response, error) {
		if provider.AuthType == domain.AuthTypeCursorOAuth {
			baseURL, refreshed, bridgeErr := s.resolveCursorBridgeBaseURL(r.Context(), provider)
			if bridgeErr != nil {
				return nil, bridgeErr
			}
			provider = refreshed
			upstreamURL := strings.TrimRight(baseURL, "/") + "/chat/completions"
			request, reqErr := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(upstreamBody))
			if reqErr != nil {
				return nil, reqErr
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", accept)
			return doHTTPWithTiming(r.Context(), &http.Client{Timeout: 0}, request)
		}
		adaptedBody, bodyErr := applyRequestAdapterBody(provider, model, upstreamBody)
		if bodyErr != nil {
			return nil, bodyErr
		}
		return s.doOpenAIProviderRequest(ctx, r, provider, resolveProviderChatURLWithAdapter(provider, model), adaptedBody, accept, skipIncomingAuth)
	}
	send := sendUpstream
	if forcedStream {
		send = func() (*http.Response, error) {
			response, sendErr := sendUpstream()
			if sendErr != nil || response == nil {
				return response, sendErr
			}
			_ = forcedStreamAsCompletion(response, model, workBuddyStreamAsCompletion)
			return response, nil
		}
	}
	return s.finishConvertedProxyWithEmptyStreamRetry(w, r, model, stream, send, convert, streamConvert)
}

func (s *Server) proxyConvertedThroughResponses(w http.ResponseWriter, r *http.Request, provider domain.Provider, model string, responsesReq map[string]any, skipIncomingAuth bool, convert responseConverter, streamConvert streamConverter) (int, TokenUsage, []byte, error) {
	stream, _ := responsesReq["stream"].(bool)
	if provider.AuthType == domain.AuthTypeChatGPTOAuth {
		responsesReq["store"] = false
		responsesReq["stream"] = true
		stream = true
	}
	upstreamBody, err := json.Marshal(responsesReq)
	if err != nil {
		return 0, TokenUsage{}, nil, err
	}
	accept := "application/json"
	if stream {
		accept = "text/event-stream"
	}
	send := func() (*http.Response, error) {
		return s.doOpenAIProviderRequest(r.Context(), r, provider, resolveProviderResponsesURL(provider, model), upstreamBody, accept, skipIncomingAuth)
	}
	return s.finishConvertedProxyWithEmptyStreamRetry(w, r, model, stream, send, convert, streamConvert)
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
	send := func() (*http.Response, error) {
		response, err := s.doClaudeProviderRequest(r.Context(), r, provider, upstreamBody, accept)
		if err != nil || response == nil || response.StatusCode != http.StatusBadRequest {
			return response, err
		}
		// Thinking 签名整流：Responses<->Claude 转换路径同样可能因回传的历史 /
		// 最新 thinking 块与原始签名不一致而收到该类 400（例如 "blocks in the
		// latest assistant message cannot be modified"）。命中则剥离 thinking 块
		// 后按相同 Accept 对同一 Provider 重试一次，对客户端透明。
		if rectified, ok := s.maybeRectifyClaudeThinkingResend(r, provider, upstreamBody, response, func(b []byte) (*http.Response, error) {
			return s.doClaudeProviderRequest(r.Context(), r, provider, b, accept)
		}); ok {
			return rectified, nil
		}
		// 上游 Claude 拒绝 sampling（temperature/top_p/top_k）时去掉重发一次。
		if sampled, ok := s.maybeRectifySamplingRefusal(r, provider, upstreamBody, response, func(b []byte) (*http.Response, error) {
			return s.doClaudeProviderRequest(r.Context(), r, provider, b, accept)
		}); ok {
			return sampled, nil
		}
		return response, nil
	}
	return s.finishConvertedProxyWithEmptyStreamRetry(w, r, model, stream, send, convert, streamConvert)
}

type upstreamSender func() (*http.Response, error)

func (s *Server) finishConvertedProxyWithEmptyStreamRetry(w http.ResponseWriter, r *http.Request, model string, stream bool, send upstreamSender, convert responseConverter, streamConvert streamConverter) (int, TokenUsage, []byte, error) {
	attempts := 1
	if stream {
		attempts = maxUpstreamEmptyStreamAttempts
	}
	var lastStatus int
	var lastUsage TokenUsage
	var lastBody []byte
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		response, err := send()
		if err != nil {
			return 0, TokenUsage{}, nil, err
		}
		status, usage, body, finishErr, retryable := s.finishConvertedProxy(w, response, model, stream, convert, streamConvert)
		if attempt < attempts {
			// A retry reuses the same client writer (and its failover buffer), so
			// the failed attempt's upstream headers must not outlive it: a stale
			// Retry-After would outlive a 429 that a retry then succeeded past.
			for key := range response.Header {
				w.Header().Del(key)
			}
		}
		_ = response.Body.Close()
		lastStatus, lastUsage, lastBody, lastErr = status, usage, body, finishErr
		if finishErr == nil || !retryable || attempt >= attempts {
			return status, usage, body, finishErr
		}
		flag := timingFlagEmptyStreamRetry
		label := "upstream empty stream retry"
		if isThinkingOnlyEmptyStreamError(finishErr) {
			flag = timingFlagThinkingOnlyRetry
			label = "upstream thinking-only empty response retry"
		}
		markTimingFlag(r.Context(), flag)
		if t := requestTimingFrom(r.Context()); t != nil {
			t.resetUpstreamMarks()
		}
		if s.logs != nil {
			s.logs.AddApp("warn", label, fmt.Sprintf("attempt=%d/%d model=%s err=%s", attempt, attempts, model, finishErr.Error()))
		}
	}
	return lastStatus, lastUsage, lastBody, lastErr
}

// streamRetryRecorder is the failover loop's buffering response writer. While
// it is in place every attempt is already kept back from the client, so the
// converted-stream path may retry an interrupted stream even on the last
// attempt (the loop closes the half turn itself then) and behaves like the
// plain deferred writer everywhere else.
type streamRetryRecorder interface {
	recordsForRetry()
}

// runStreamConverter converts an upstream stream, turning a body that ended
// short of its announced Content-Length into an interruption. A converter has
// no way to see that on its own: reading a truncated body is just EOF.
func runStreamConverter(w http.ResponseWriter, response *http.Response, model string, streamConvert streamConverter) (TokenUsage, error) {
	// One verdict on the body's end, for every leg (magpie judges it in one place
	// too, gateway.go:4909): a body that carried bytes and did not end with its
	// protocol's closing frame was cut, whichever converter read it. A body with
	// no bytes at all is not a turned-away answer but an account that answered
	// nothing, and the empty-stream retry above owns that case.
	//
	// Each leg still reports the cut it noticed first, in its own protocol's
	// terms; a leg that accepted a body it should not have is caught here.
	reader := newUpstreamStreamReader(response.Body)
	counting := &wroteTrackingWriter{ResponseWriter: w, written: &reader.wrote}
	usage, err := streamConvert(counting, newWholeMessageReader(response, reader), model)
	if err != nil {
		return usage, err
	}
	if cut := reader.cut(); cut != nil && !reader.wrote {
		// Nothing of a turn reached the client: the account answered nothing, and
		// the empty-stream retry above owns that case.
		return usage, cut
	}
	return usage, nil
}

// newWholeMessageReader hands the converter an account's answer that arrived as
// one whole JSON message even though the request asked for a stream (accounts
// answer either way, and failover mixes them): the message's own bytes, read
// back as the converter's source, so the verdicts taken from its reads — the
// interrupted stream, the turn that said nothing — see the message, not a
// stream that lost its closing frame. Any other body is passed through as it is.
func newWholeMessageReader(response *http.Response, body io.Reader) io.Reader {
	if whole, ok := wholeJSONUpstreamBody(response); ok {
		return io.MultiReader(bytes.NewReader(whole), body)
	}
	return body
}

// wholeJSONUpstreamBody reads a body that announced itself as JSON in full and
// returns it. The body is judged by its header, never by peeking at it: bytes
// taken out of a converter's read are a read the converter never made, and its
// verdicts count on seeing every one.
func wholeJSONUpstreamBody(response *http.Response) ([]byte, bool) {
	if response == nil || response.Body == nil {
		return nil, false
	}
	if !strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "application/json") {
		return nil, false
	}
	whole, err := io.ReadAll(response.Body)
	if err != nil || len(bytes.TrimSpace(whole)) == 0 {
		return nil, false
	}
	return whole, true
}

// upstreamStreamReader is the body a converter reads, remembered up to the cap,
// so the verdict on how it ended is taken from the bytes.
type upstreamStreamReader struct {
	body io.Reader
	seen bytes.Buffer
	over bool // more bytes came than the cap holds
	// wrote says the converter produced output: the attempt became a turn.
	wrote bool
}

func newUpstreamStreamReader(body io.Reader) *upstreamStreamReader {
	return &upstreamStreamReader{body: body}
}

func (r *upstreamStreamReader) Read(p []byte) (int, error) {
	n, err := r.body.Read(p)
	if n > 0 {
		room := closingBodyMax - r.seen.Len()
		if room <= 0 {
			r.over = true
		} else {
			if n < room {
				room = n
			}
			_, _ = r.seen.Write(p[:room])
			if n > room {
				r.over = true
			}
		}
	}
	return n, err
}

// cut reports the body as cut when it carried bytes, is an event stream, and
// ended without its protocol's closing frame. A body longer than the cap is not
// judged: what was dropped is the part that would have said.
func (r *upstreamStreamReader) cut() error {
	if r.over {
		return nil
	}
	body := r.seen.Bytes()
	if len(body) == 0 || !bytes.Contains(body, sseDataPrefixMark) {
		return nil
	}
	if closesUpstreamStream(body) {
		return nil
	}
	return newUpstreamStreamInterruption(io.ErrUnexpectedEOF, len(body))
}

func (s *Server) finishConvertedProxy(w http.ResponseWriter, response *http.Response, model string, stream bool, convert responseConverter, streamConvert streamConverter) (int, TokenUsage, []byte, error, bool) {
	if stream {
		dw := newDeferredSSEWriter(w)
		dw.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		dw.Header().Set("Cache-Control", "no-cache")
		dw.Header().Set("Connection", "keep-alive")
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			// The upstream refused before any stream began. Answering here — as
			// this did, status and body both — committed the first account's
			// refusal as the client's reply, and the failover loop never saw an
			// error to act on: a busy model (Qoder's 10605 answers 403 JSON on
			// a request that asked for a stream) looked like a dead end with
			// twelve accounts still untried.
			responseBody, readErr := io.ReadAll(response.Body)
			if readErr != nil {
				return response.StatusCode, TokenUsage{}, nil, readErr, false
			}
			converted, _, convErr := convert(responseBody, model)
			if convErr != nil || len(converted) == 0 {
				converted = responseBody
			}
			if _, behindLoop := w.(*failoverResponseWriter); !behindLoop {
				dw.WriteHeader(response.StatusCode)
				_, writeErr := dw.Write(converted)
				return response.StatusCode, TokenUsage{}, converted, writeErr, false
			}
			// Behind the loop: write nothing. The deferred writer commits on the
			// first byte, and committing here is exactly what stopped the chain
			// on its first account. The refusal travels back as the attempt's
			// body so the loop's last attempt can answer with it.
			return response.StatusCode, TokenUsage{}, converted,
				fmt.Errorf("%s", summarizeUpstreamHTTPError(response.StatusCode, converted)), false
		}
		dw.WriteHeader(response.StatusCode)
		// Behind the failover loop the attempt is intercepted rather than sent,
		// so an interrupted stream can still be retried (empty: same provider;
		// cut mid-answer: the backup): the client has seen nothing of it. For
		// the same reason the intercepted stream itself is not what decides
		// whether it was closed — a converting writer may shape the closing
		// event as it likes. On the last attempt there is nowhere to go, so a
		// half answer the converter did not close is sent as it stands.
		if fw, ok := w.(*failoverResponseWriter); ok {
			streamed := newConverterStreamWriter(fw)
			usage, streamErr := runStreamConverter(streamed, response, model, streamConvert)
			streamBody := streamed.Finish()
			if streamed.Committed() {
				fw.FlushError()
				return response.StatusCode, usage, streamBody, streamErr, false
			}
			if isUpstreamStreamInterruption(streamErr) || isThinkingOnlyEmptyStreamError(streamErr) {
				// Nothing of this attempt reached the client — a cut stream, or a
				// turn that opened and said nothing (thinking-only). The latter
				// belongs to the empty-answer retry above as well, or the attempt
				// is committed as an error the client can do nothing with.
				return response.StatusCode, usage, nil, streamErr, true
			}
			if streamErr != nil && busyRefusalText(strings.ToLower(streamErr.Error())) {
				// The model is busy (Qoder's 10605): nothing of this attempt
				// reached the client, so hand the turn back to the failover loop
				// to try the next account — asking this same busy model again a
				// few times in a row only burns the wait.
				return response.StatusCode, usage, nil, streamErr, false
			}
			fw.WriteHeader(response.StatusCode)
			fw.FlushError()
			return response.StatusCode, usage, nil, streamErr, false
		}
		// Keep the downstream SSE path warm during long upstream thinking /
		// quiet gaps so Cloudflare (and similar proxies) do not idle-cut the
		// stream. Heartbeats are SSE comments and do not set wroteBody; once
		// they commit headers, Committed() becomes true and empty-stream
		// retries are intentionally skipped (client already saw a 200).
		stopHeartbeat := startSSEHeartbeat(dw, sseHeartbeatInterval)
		defer stopHeartbeat()
		usage, streamErr := runStreamConverter(dw, response, model, streamConvert)
		if streamErr != nil && (isUpstreamStreamInterruption(streamErr) || isThinkingOnlyEmptyStreamError(streamErr)) && !dw.committed() {
			// 尚未向客户端提交任何字节（含 heartbeat），可安全同 Provider 重试。
			// 「只有 thinking 的空回答」也走这条：它该由调用方的空回答重试节奏再问
			// 一次，而不是被当成截断错误报给客户端。
			return response.StatusCode, usage, nil, streamErr, true
		}
		// Capture before flushing: the flush hands the buffer to the client and
		// empties it, while the caller still logs what the turn returned.
		streamBody := dw.BodyBytes()
		if !dw.Committed() {
			// 成功但转换器未写出（极少见）：仍提交状态，避免悬挂。
			dw.commit(response.StatusCode)
		}
		dw.flushBuffered()
		return response.StatusCode, usage, streamBody, streamErr, false
	}

	responseBody, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return 0, TokenUsage{}, nil, readErr, false
	}
	if up := response.Header.Get("Retry-After"); up != "" {
		// The client's own 429 should say when to come back; the cooldown reads
		// the same header through the failover writer.
		w.Header().Set("Retry-After", up)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		converted, _, convErr := convert(responseBody, model)
		if convErr != nil || len(converted) == 0 {
			converted = responseBody
		}
		// Behind the failover loop the deferred writer keeps a >=400 status in
		// its buffer instead of committing it, so the refusal is written here
		// (the last attempt must still answer with it) and the error is
		// reported alongside: the loop may hand the turn to the next account,
		// and does when the status names a model that is merely busy. Writing
		// without the error — what this did before — committed the first
		// account's refusal as the client's answer with twelve accounts untried.
		w.WriteHeader(response.StatusCode)
		_, writeErr := w.Write(converted)
		if writeErr != nil {
			return response.StatusCode, TokenUsage{}, converted, writeErr, false
		}
		// Only behind the failover loop is the refusal a decision the loop can
		// act on; a direct call keeps its old contract of answering with it.
		if _, behindLoop := w.(*failoverResponseWriter); !behindLoop {
			return response.StatusCode, TokenUsage{}, converted, nil, false
		}
		return response.StatusCode, TokenUsage{}, converted,
			fmt.Errorf("%s", summarizeUpstreamHTTPError(response.StatusCode, converted)), false
	}
	converted, usage, err := convert(responseBody, model)
	if err != nil {
		if isThinkingOnlyEmptyStreamError(err) {
			// 上游 200 但转换后判定为"仅 thinking 无可见输出"，尚未写出任何字节，
			// 可安全同 Provider 重试。
			return response.StatusCode, usage, nil, err, true
		}
		return 0, TokenUsage{}, nil, err, false
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(converted)))
	w.WriteHeader(http.StatusOK)
	if _, writeErr := w.Write(converted); writeErr != nil {
		return http.StatusOK, usage, converted, writeErr, false
	}
	return http.StatusOK, usage, converted, nil, false
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
	// Single chokepoint for the Qoder job-token refresh: every pass-through path
	// and every protocol conversion below flows through here.
	if isQoderProvider(provider) {
		provider, err = s.ensureFreshQoderToken(provider)
		if err != nil {
			return 0, TokenUsage{}, nil, err
		}
	}
	// Same for a WorkBuddy session, whose access token is short-lived.
	if isWorkBuddyProvider(provider) {
		provider, err = s.ensureFreshWorkBuddyToken(provider)
		if err != nil {
			return 0, TokenUsage{}, nil, err
		}
	}
	// A ZCode key does not rotate, so all that can be wrong is that there is
	// none; a Trae JWT rotates on every request path and is renewed here.
	if isZCodeProvider(provider) {
		provider, err = ensureZCodeCredential(provider)
		if err != nil {
			return 0, TokenUsage{}, nil, err
		}
	}
	if isTraeProvider(provider) {
		provider, err = s.ensureFreshTraeToken(provider)
		if err != nil {
			return 0, TokenUsage{}, nil, err
		}
	}

	// The per-provider first-packet deadline rides on the request context so
	// every upstream send below — pass-through, conversion, and the subscription
	// adapters that build their own request — enforces it without knowing about
	// scheduling. Providers that answer headers and then think for minutes are
	// unaffected: the timer only covers the wait for headers.
	if timeout := providerFirstPacketTimeout(provider); timeout > 0 {
		r = r.WithContext(withFirstPacketTimeout(r.Context(), timeout))
	}

	// Admission: this provider's lane, held for the whole upstream exchange so
	// the cap means "requests actually in flight", not "requests started".
	// Releasing on return is correct for streaming too — the stream is written
	// before executeProtocolFlow returns.
	releaseSlot, admissionErr := s.acquireProviderSlot(r.Context(), provider)
	if admissionErr != nil {
		// Tell the client how long this lane thinks it is busy, not a constant:
		// a rate refusal knows when its minute window restarts.
		w.Header().Set("Retry-After", strconv.Itoa(s.providerLanes.retryAfterSecondsFor(provider, time.Now())))
		if errors.Is(admissionErr, errProviderSaturated) {
			return http.StatusTooManyRequests, TokenUsage{}, nil, admissionErr
		}
		return 0, TokenUsage{}, nil, admissionErr
	}
	defer releaseSlot()

	// Provider-level RTK compression runs on the client-format request, before
	// both pass-through marshaling and protocol conversion. The helper returns a
	// new top-level map and only copies the message path it changes, so the
	// failover loop's shallow attempt copies never leak compressed content into
	// another provider's attempt.
	if provider.RTKCompress && s.rtk != nil {
		compressedReq, rtkStats := s.rtk.compressRequest(req, clientProtocol)
		if rtkStats.Commands > 0 {
			req = compressedReq
			s.recordRTKStats(provider.ID, rtkStats)
		}
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
	case domain.ProtocolGemini:
		switch decision.InputProtocol {
		case domain.ProtocolOpenAIChat:
			return s.proxyGeminiToOpenAIChat(w, r, provider, model, req, skipIncomingAuth)
		case domain.ProtocolClaude:
			return s.proxyGeminiToClaudeMessages(w, r, provider, model, req, skipIncomingAuth)
		case domain.ProtocolOpenAIResponses:
			return s.proxyGeminiToOpenAIResponses(w, r, provider, model, req, skipIncomingAuth)
		}
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

// wroteTrackingWriter notes that the converter produced output.
type wroteTrackingWriter struct {
	http.ResponseWriter
	written *bool
}

func (c *wroteTrackingWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		*c.written = true
	}
	return c.ResponseWriter.Write(p)
}

func (c *wroteTrackingWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
