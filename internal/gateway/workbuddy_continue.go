package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Continuing an answer the upstream cut off.
//
// WorkBuddy's chat endpoint stops at the model's output ceiling and says so in
// finish_reason: a long answer arrives as "length", not as an error. A client that
// asked for one JSON object then gets a half answer with no way to know it was cut,
// and every retry hits the same ceiling (the reference implementation solves this
// with an explicit continuation loop: misakano7545/workbuddy2api-panel
// internal/server/continue.go).
//
// The rule this gateway follows is narrow on purpose:
//
//   - only the aggregated path continues. A streaming client gets its bytes as they
//     arrive and a continuation there would mean holding the whole answer back,
//     which is the thing this gateway's streaming design exists to avoid;
//   - only a text answer continues. A truncated tool call is a different problem
//     (an unusable arguments string, not a half sentence) and re-asking would spend
//     credits on a call nobody can run;
//   - segments are capped, so a model that answers "…" forever cannot spend an
//     account's credits in a loop;
//   - the nudge forbids repeating itself, because a naive "continue" makes most
//     models restate the last paragraph, which would show the client duplicated
//     text instead of a longer answer.
//
// It is off unless WORKBUDDY_CONTINUE=1. Turning it on changes what a truncated
// answer costs (up to three more calls) and what it looks like (one joined answer),
// which is a decision for the operator, not a release.

const (
	// workBuddyContinueEnv turns the loop on.
	workBuddyContinueEnv = "WORKBUDDY_CONTINUE"
	// workBuddyMaxContinueSegments caps how many extra calls one answer may cost.
	workBuddyMaxContinueSegments = 3
	// workBuddyContinueBodyCap bounds what is read while aggregating a segment.
	workBuddyContinueBodyCap = 8 << 20
)

// workBuddyAggregateAnswerKey marks a request whose client wants one JSON object,
// even though the upstream call itself always streams.
//
// The Accept header cannot carry this: the gateway sets it to text/event-stream on
// the way out precisely because the upstream refuses a non-stream call, so it says
// what the upstream will produce, not what the client asked for.
type workBuddyAggregateAnswerKey struct{}

// withWorkBuddyAggregateAnswer marks the request as one to rebuild into a single
// completion.
func withWorkBuddyAggregateAnswer(ctx context.Context, aggregate bool) context.Context {
	if !aggregate {
		return ctx
	}
	return context.WithValue(ctx, workBuddyAggregateAnswerKey{}, true)
}

// workBuddyWantsAggregatedAnswer reports the mark.
func workBuddyWantsAggregatedAnswer(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	marked, _ := ctx.Value(workBuddyAggregateAnswerKey{}).(bool)
	return marked
}

// workBuddyContinueEnabled reports whether the loop is on.
func workBuddyContinueEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(workBuddyContinueEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// workBuddyContinueNudge is the user turn that asks for the rest.
//
// It says three things the models otherwise get wrong: do not repeat, do not
// announce, do not summarise — each of which was a way to get a useless "second
// half" that reads like a duplicated answer.
const workBuddyContinueNudge = "Your previous reply was cut off at the output limit. " +
	"Continue from the exact point where it stopped: do not repeat anything already written, " +
	"do not add a preamble or a summary, and do not mention that you were interrupted."

// workBuddyCompletionText reads the answer text out of an aggregated completion.
func workBuddyCompletionText(completion map[string]any) string {
	choices, _ := completion["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	switch content := message["content"].(type) {
	case string:
		return content
	case []any:
		// Some builds answer with parts even on the chat route.
		var builder strings.Builder
		for _, item := range content {
			part, _ := item.(map[string]any)
			if text, ok := part["text"].(string); ok {
				builder.WriteString(text)
			}
		}
		return builder.String()
	}
	return ""
}

// workBuddyCompletionFinish reads finish_reason out of an aggregated completion.
func workBuddyCompletionFinish(completion map[string]any) string {
	choices, _ := completion["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	choice, _ := choices[0].(map[string]any)
	reason, _ := choice["finish_reason"].(string)
	return strings.TrimSpace(reason)
}

// workBuddyCompletionHasToolCalls reports whether the answer ended in a tool call.
func workBuddyCompletionHasToolCalls(completion map[string]any) bool {
	choices, _ := completion["choices"].([]any)
	if len(choices) == 0 {
		return false
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	calls, _ := message["tool_calls"].([]any)
	return len(calls) > 0
}

// workBuddyNeedsContinuation decides whether the answer was cut off.
//
// "length" is the upstream's own admission. An empty answer with no tool call is
// the other shape worth retrying: the model produced nothing, and asking once more
// is cheaper than returning an empty message to the client.
func workBuddyNeedsContinuation(completion map[string]any) bool {
	if workBuddyCompletionHasToolCalls(completion) {
		return false
	}
	reason := strings.ToLower(workBuddyCompletionFinish(completion))
	// "length" is OpenAI's word for it and "max_tokens" is Anthropic's; this upstream
	// passes whichever its own stream carries straight through, so both mean the same
	// thing here (measured live: a truncated answer reported max_tokens).
	if reason == "length" || reason == "max_tokens" {
		return true
	}
	if reason == "" || reason == "stop" || reason == "end_turn" {
		// An answer with no text is worth one more ask — a model whose whole budget
		// went to a reasoning trace is the shape that produces it.
		return strings.TrimSpace(workBuddyCompletionText(completion)) == ""
	}
	return false
}

// workBuddyContinueBody appends the unfinished answer and the nudge, so the next
// call picks up where the model stopped.
//
// A body that will not parse comes back nil: a continuation of an unreadable
// request would be a second, invented request.
func workBuddyContinueBody(body []byte, partial string) []byte {
	if len(body) == 0 {
		return nil
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil || payload == nil {
		return nil
	}
	messages, ok := payload["messages"].([]any)
	if !ok {
		return nil
	}
	trimmed := strings.TrimSpace(partial)
	if trimmed != "" {
		messages = append(messages, map[string]any{"role": "assistant", "content": partial})
	}
	messages = append(messages, map[string]any{"role": "user", "content": workBuddyContinueNudge})
	payload["messages"] = messages
	// The answer is being rebuilt by us, so the segment must stream for the same
	// reason every other WorkBuddy call streams.
	payload["stream"] = true
	encoded := encodeJSONWithoutEscaping(payload)
	if encoded == nil {
		return nil
	}
	return encoded
}

// workBuddyMergeContinuation joins a continued segment onto the answer so far.
//
// Text is concatenated; usage is summed so the client is billed for what was
// actually spent; the finish reason follows the last segment (a second "length"
// still says so, which is how a client learns the answer was capped again).
func workBuddyMergeContinuation(base, extra map[string]any) map[string]any {
	if base == nil {
		return extra
	}
	if extra == nil {
		return base
	}
	baseChoices, _ := base["choices"].([]any)
	extraChoices, _ := extra["choices"].([]any)
	if len(baseChoices) == 0 || len(extraChoices) == 0 {
		return base
	}
	baseChoice, _ := baseChoices[0].(map[string]any)
	extraChoice, _ := extraChoices[0].(map[string]any)
	baseMessage, _ := baseChoice["message"].(map[string]any)
	extraMessage, _ := extraChoice["message"].(map[string]any)
	if baseMessage == nil || extraMessage == nil {
		return base
	}
	joined := workBuddyCompletionText(base) + workBuddyCompletionText(extra)
	baseMessage["content"] = joined
	// A continued answer that produced nothing adds nothing: keep the reason we had
	// rather than overwriting it with an empty segment's "stop".
	if strings.TrimSpace(workBuddyCompletionText(extra)) != "" {
		if reason, _ := extraChoice["finish_reason"].(string); reason != "" {
			baseChoice["finish_reason"] = reason
		}
	}
	baseChoice["message"] = baseMessage
	baseChoices[0] = baseChoice
	base["choices"] = baseChoices

	if baseUsage, ok := base["usage"].(map[string]any); ok {
		if extraUsage, ok := extra["usage"].(map[string]any); ok {
			for _, key := range []string{"completion_tokens", "total_tokens"} {
				baseUsage[key] = workBuddyAnyFloat(baseUsage[key]) + workBuddyAnyFloat(extraUsage[key])
			}
		}
	}
	return base
}

// continueWorkBuddyAnswer drives the loop for one aggregated answer.
//
// It returns the response to hand back: either the original one (untouched, when no
// continuation was needed or the first attempt failed to aggregate), or one whose
// body is the joined completion. Failures after the first segment are not errors —
// the client already has a usable half answer, and replacing it with an error would
// lose what the account paid for.
func (s *Server) continueWorkBuddyAnswer(
	provider domain.Provider,
	model string,
	body []byte,
	send workBuddySender,
	response *http.Response,
) *http.Response {
	if !workBuddyContinueEnabled() || response == nil || response.StatusCode >= 400 {
		return response
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, workBuddyContinueBodyCap))
	_ = response.Body.Close()
	if err != nil {
		response.Body = io.NopCloser(bytes.NewReader(payload))
		return response
	}

	completion := workBuddyAggregateToMap(payload, model)
	if completion == nil {
		// Not an answer this loop understands: give the original bytes back.
		response.Body = io.NopCloser(bytes.NewReader(payload))
		return response
	}

	current := completion
	currentBody := body
	joined := false
	for segment := 0; segment < workBuddyMaxContinueSegments; segment++ {
		if !workBuddyNeedsContinuation(current) {
			break
		}
		nextBody := workBuddyContinueBody(currentBody, workBuddyCompletionText(current))
		if nextBody == nil {
			break
		}
		next, sendErr := send(provider, nextBody)
		if sendErr != nil || next == nil || next.StatusCode >= 400 {
			if next != nil && next.Body != nil {
				_ = next.Body.Close()
			}
			break
		}
		nextPayload, readErr := io.ReadAll(io.LimitReader(next.Body, workBuddyContinueBodyCap))
		_ = next.Body.Close()
		if readErr != nil {
			break
		}
		nextCompletion := workBuddyAggregateToMap(nextPayload, model)
		if nextCompletion == nil {
			break
		}
		current = workBuddyMergeContinuation(current, nextCompletion)
		currentBody = nextBody
		joined = true
	}

	if !joined {
		// Nothing was joined: the original bytes are still the honest answer.
		response.Body = io.NopCloser(bytes.NewReader(payload))
		return response
	}
	encoded := encodeJSONWithoutEscaping(current)
	if encoded == nil {
		response.Body = io.NopCloser(bytes.NewReader(payload))
		return response
	}
	response.Body = io.NopCloser(bytes.NewReader(encoded))
	response.ContentLength = int64(len(encoded))
	response.Header.Set("Content-Type", "application/json; charset=utf-8")
	return response
}

// workBuddyAggregateToMap turns one SSE segment into a completion, or nil when the
// bytes are not a stream this gateway can read.
func workBuddyAggregateToMap(streamBody []byte, model string) map[string]any {
	if len(streamBody) == 0 || !bytes.Contains(streamBody, []byte("data:")) {
		return nil
	}
	encoded, err := workBuddyStreamAsCompletion(streamBody, model)
	if err != nil {
		return nil
	}
	var completion map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if decoder.Decode(&completion) != nil || completion == nil {
		return nil
	}
	return completion
}

// workBuddyContinueTimeout bounds one continuation call, so a stalled segment cannot
// hold the request open after the client already has a usable answer.
const workBuddyContinueTimeout = 5 * time.Minute
