package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// The Anthropic Messages route that sits beside chat on the China build.
//
// WorkBuddy serves its Claude models on Anthropic's Messages API rather than on the
// chat endpoint (magpie-community/plugins packages/workbuddy README §Requests,
// measured 2026-10-24): `claude-opus-5.5`, `claude-sonnet-5.5`, `claude-opus-5` and
// `claude-opus-4.8-1m` answered there, while `glm-5.3` and `deepseek-v4.1-flash`
// answered 500 and `gpt-5.5`, `kimi-k3-1` 400 — so only those four move, and every
// other model stays on chat. WorkBuddy AI has no such route at all (`/v1/messages`
// is "404 Route Not Found" for every model), which is why this is China-only.
//
// The point of the route is effort. A chat carries `reasoning_effort` and the chat
// endpoint answers at about the same length whatever it says (median 3884 output
// tokens at low, 5144 at max over four runs each); Messages carries
// `output_config.effort` and honours it (median 30850 against 5748 at low). A gateway
// that sends every WorkBuddy model to chat therefore cannot express effort for the
// Claude family at all.
//
// A Messages request goes as the agent sent it — no default system message, no role
// rewrites, no tool reshaping, and the client's billing line is kept, because this
// route does not run the channel check that a chat does. One thing is removed: a
// `thinking` block with no `signature`. A conversation that began on chat carries
// such blocks, and once a signed one from this route sits beside them the upstream
// refuses the whole request with 11159 "Invalid signature in thinking block … bound
// to a different conversation".
//
// Only the four measured models are routed here. A model outside that list keeps the
// chat path, because guessing at a model the reference never tested is how a working
// request becomes a 404.

// workBuddyMessagesRouteEnv turns the route on or off. Off is not offered as the
// default: the models it applies to are exactly the ones measured to work, and this
// gateway cannot express effort for them any other way. It exists so an operator can
// fall back to chat without a rebuild if the upstream changes.
const workBuddyMessagesRouteEnv = "WORKBUDDY_MESSAGES_ROUTE"

// workBuddyMessagesModels are the models measured to answer on the Messages route.
var workBuddyMessagesModels = map[string]struct{}{
	"claude-opus-5.5":    {},
	"claude-sonnet-5.5":  {},
	"claude-opus-5":      {},
	"claude-opus-4.8-1m": {},
}

// workBuddyMessagesEnabled reports whether the route is on.
func workBuddyMessagesEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(workBuddyMessagesRouteEnv))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// workBuddyMessagesModel reports whether this model is served on the Messages route.
func workBuddyMessagesModel(model string) bool {
	_, ok := workBuddyMessagesModels[strings.ToLower(strings.TrimSpace(model))]
	return ok
}

// workBuddyMessagesApplies decides whether one request takes the route.
//
// Three conditions, all of them load-bearing: the account is the China build (the
// intl one has no route), the model is one the reference measured there, and the
// client asked for a stream (the route is streamed-only, and the pass-through this
// feeds is a streaming pass-through).
func workBuddyMessagesApplies(provider domain.Provider, model string, stream bool) bool {
	if !workBuddyMessagesEnabled() || !stream {
		return false
	}
	site, ok := workBuddySiteForAuthType(provider.AuthType)
	if !ok || site.intl {
		return false
	}
	return workBuddyMessagesModel(model)
}

// workBuddyMessagesBodyModel reads the model a Messages body names.
func workBuddyMessagesBodyModel(body []byte) string {
	value, _ := claudeBodyField(body, "model").(string)
	return strings.TrimSpace(value)
}

// workBuddyMessagesURL is the account's Messages endpoint.
//
// Built from the site root, not the provider row: the row's base carries `/v2` for
// chat, and the Messages route lives beside it at the root.
func workBuddyMessagesURL(site workBuddySite) string {
	return strings.TrimRight(site.api(), "/") + "/v1/messages"
}

// newWorkBuddyMessagesRequest builds the request for one Messages call.
//
// The head is the desktop client's own (the reference notes this route "carries
// WorkBuddy's headers as a chat does, the same conversation and turn ids included"),
// with Anthropic's version header added because this is the Anthropic API shape.
func newWorkBuddyMessagesRequest(ctx context.Context, provider domain.Provider, url string, body []byte) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	site, ok := workBuddySiteForAuthType(provider.AuthType)
	if !ok {
		return nil, errWorkBuddyNotAnAccount
	}
	applyWorkBuddyChatHeaders(request, site, provider.WorkBuddy, workBuddyAgentTypeMain)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("anthropic-version", "2023-06-01")
	return request, nil
}

// sanitizeWorkBuddyMessagesBody drops the thinking blocks that carry no signature.
//
// Only that. The rest of the body goes as the agent sent it: this route does not run
// the chat channel check, so the client's prompt (billing line included) is not
// rewritten here — doing so would be changing a request the upstream accepts.
//
// A body that will not parse comes back untouched; a bad body is not made worse.
func sanitizeWorkBuddyMessagesBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil || payload == nil {
		return body
	}
	messages, ok := payload["messages"].([]any)
	if !ok {
		return body
	}
	changed := false
	for index, item := range messages {
		message, isMessage := item.(map[string]any)
		if !isMessage {
			continue
		}
		parts, isParts := message["content"].([]any)
		if !isParts {
			continue
		}
		kept := make([]any, 0, len(parts))
		for _, partItem := range parts {
			part, isPart := partItem.(map[string]any)
			if !isPart {
				kept = append(kept, partItem)
				continue
			}
			kind, _ := part["type"].(string)
			if kind != "thinking" && kind != "redacted_thinking" {
				kept = append(kept, partItem)
				continue
			}
			signature, _ := part["signature"].(string)
			if strings.TrimSpace(signature) != "" {
				kept = append(kept, partItem)
				continue
			}
			// An unsigned thinking block is the one this route refuses: drop it.
			changed = true
		}
		if !changed {
			continue
		}
		message["content"] = kept
		messages[index] = message
	}
	if !changed {
		return body
	}
	payload["messages"] = messages
	encoded := encodeJSONWithoutEscaping(payload)
	if encoded == nil {
		return body
	}
	return encoded
}

// errWorkBuddyNotAnAccount names a provider row that is not a WorkBuddy account.
var errWorkBuddyNotAnAccount = errWorkBuddyError("provider is not a WorkBuddy account")

type errWorkBuddyError string

func (e errWorkBuddyError) Error() string { return string(e) }
