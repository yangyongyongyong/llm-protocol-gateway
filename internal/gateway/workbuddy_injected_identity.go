package gateway

import (
	"regexp"
	"strings"
)

// The identity instruction the upstream injects server-side, and the snowball it causes.
//
// Measured upstream (issue #187, from a 2.73 MB request-body dump): the service splices a
// sentence into the model's visible context —
//
//	When asked about your model identity, identify yourself as Tencent Hunyuan Hy.
//
// That sentence never appears in an outgoing body: it is added on their side. But the
// model **echoes it into `reasoning_content`**, the client carries that reasoning back as
// history, and the next turn sends it upstream again — so each round the request names the
// identity once more and the model confirms it once more. That loop is why a conversation
// that never asked about identity keeps announcing one.
//
// Two pieces fix it, and both only touch the carriers of the loop:
//
//   - the sentence itself is rewritten to a neutral statement that asserts nothing;
//   - the `<model_identity>` wrapper is removed (its text is kept), because the model
//     treats the tag as "there is an identity block here" and mentions it again.
//
// The scope is deliberate: an **assistant** body, and **any** message's
// `reasoning_content` / `reasoning`. A system or user message saying the same thing is the
// user's own persona instruction ("When asked about your identity, identify yourself as
// Aria, the Acme assistant.") — rewriting that would be editing the user's prompt.

// workBuddyInjectedIdentityRe matches the injected sentence as a whole.
//
// The pattern is deliberately narrow: matching a bare `identify yourself as` would hit
// legitimate instructions ("Do not identify yourself as an AI." is a valid thing to ask),
// while the full prefix essentially never occurs naturally.
//
// The terminator is a period **not followed by a letter or digit**, because a model name may
// contain dots (GPT-4.1, Claude 3.5): stopping at the first dot would leave "1 by OpenAI." as
// a fragment. The character after the period is captured and restored rather than looked
// ahead at, since RE2 has no lookahead — and in the echoed form that character is often a
// quote or a newline that must survive.
var workBuddyInjectedIdentityRe = regexp.MustCompile(
	`(?i)when asked about your[^.\n]{0,60}?identity[^.\n]{0,60}?,\s*identify yourself as\s+[^\n]{1,80}?\.(\W|$)`)

// workBuddyInjectedIdentityReplacement says nothing about identity and keeps the context
// usable.
const workBuddyInjectedIdentityReplacement = "When asked about your capabilities, answer using only the information available in this conversation.${1}"

// The two probes are matched case-insensitively: the regular expression is (?i), and a
// case-sensitive gate would let an "Identify yourself as" restatement through.
const (
	workBuddyInjectedIdentityProbe = "identify yourself as"
	workBuddyModelIdentityTagProbe = "model_identity"
)

// workBuddyModelIdentityTagRe removes the `<model_identity>` wrapper, keeping the text.
var workBuddyModelIdentityTagRe = regexp.MustCompile(`(?i)</?model_identity>`)

// scrubWorkBuddyInjectedIdentity rewrites the injected sentence and drops the wrapper tag.
//
// It does not trim: this runs on a reasoning trace where leading whitespace is the model's,
// and a text that matches nothing comes back unchanged.
func scrubWorkBuddyInjectedIdentity(text string) string {
	if text == "" {
		return text
	}
	if strings.Contains(strings.ToLower(text), workBuddyInjectedIdentityProbe) {
		text = workBuddyInjectedIdentityRe.ReplaceAllString(text, workBuddyInjectedIdentityReplacement)
	}
	if strings.Contains(strings.ToLower(text), workBuddyModelIdentityTagProbe) {
		text = workBuddyModelIdentityTagRe.ReplaceAllString(text, "")
	}
	return text
}
