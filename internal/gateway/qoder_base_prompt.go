package gateway

// Qoder's own system prompt, embedded as it ships.
//
// The endpoint routes on the prompt's opening — "You are Qoder, an interactive
// CLI tool…" — and the rest of it is what shapes the agent's behaviour: the tone
// rules, the code-reference format, the tool-use protocol. A gateway that sends
// a one-line imitation of it is not making the same request, and the model's
// answers show the difference.
//
// It is only used when the client sends no system prompt of its own. A client
// that does send one replaces this entirely rather than being appended to it,
// which is what Qoder's own client does: two system prompts concatenated give
// the model two sets of instructions and neither wins.

import _ "embed"

//go:embed qoder_base_prompt.txt
var qoderBaseSystemPrompt string

// qoderSystemLine is the opening every Qoder request is routed on. It is also
// what the embedded prompt starts with, which a test checks: the file is data,
// and data can be replaced by the wrong file.
const qoderSystemLine = "You are Qoder, an interactive CLI tool that helps users with software engineering tasks."
