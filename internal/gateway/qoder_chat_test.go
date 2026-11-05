package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func qoderTestModelInfo(t *testing.T) qoderModelInfo {
	t.Helper()
	provider := domain.Provider{
		ID:       "qoder",
		AuthType: domain.AuthTypeQoderPAT,
		QoderPAT: &domain.QoderCredential{Models: json.RawMessage(qoderSampleListing)},
	}
	info, ok := qoderModelFor(provider, "ultimate")
	if !ok {
		t.Fatal("sample listing must carry Ultimate")
	}
	return info
}

func decodeQoderBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("Qoder's body is not valid JSON: %v", err)
	}
	return body
}

// A caller that sent its own system prompt gets exactly that. Qoder's own
// prompt is not appended to it: two system prompts give the model two sets of
// instructions, and the endpoint routes on the first one it finds.
func TestQoderChatBodyUsesTheCallersSystemPrompt(t *testing.T) {
	info := qoderTestModelInfo(t)
	raw, err := qoderChatBody("ultimate", []byte(`{
		"model":"ultimate",
		"messages":[
			{"role":"system","content":"Be terse."},
			{"role":"user","content":"hi"}
		]}`), info)
	if err != nil {
		t.Fatal(err)
	}
	body := decodeQoderBody(t, raw)
	messages := body["messages"].([]any)
	first := messages[0].(map[string]any)
	if first["role"] != "system" {
		t.Fatalf("the body's first turn must be the system block, got %v", first["role"])
	}
	// Qoder's client sends the system prompt as one string, not as blocks.
	text, ok := first["content"].(string)
	if !ok {
		t.Fatalf("the system turn holds one string, got %T", first["content"])
	}
	if text != "Be terse." {
		t.Fatalf("the caller's system prompt must replace Qoder's, got %q", text)
	}
	if _, repeated := body["system"]; repeated {
		t.Fatal("the system prompt belongs in the turns only, not beside them")
	}
	for _, item := range messages[1:] {
		if role := item.(map[string]any)["role"]; role == "system" {
			t.Fatal("the caller's system turn must not be repeated as a message")
		}
	}
	// Everything the endpoint keys on besides the messages.
	if body["agent_id"] != "agent_common" || body["task_id"] != "common" || body["session_type"] != qoderSessionType {
		t.Fatalf("agent identity wrong: %v", body)
	}
	if _, ok := body["model_config"].(map[string]any); !ok {
		t.Fatal("the model's own configuration has to travel with the request")
	}
	params := body["parameters"].(map[string]any)
	if params["max_tokens"] != float64(qoderDefaultMaxTokens) {
		t.Fatalf("max_tokens = %v, want the default the client asks with", params["max_tokens"])
	}
}

// A caller that sent no system prompt gets Qoder's own, and the endpoint routes
// on the line it opens with.
func TestQoderChatBodySendsQodersOwnPromptWhenTheCallerSendsNone(t *testing.T) {
	info := qoderTestModelInfo(t)
	raw, err := qoderChatBody("ultimate", []byte(`{
		"model":"ultimate",
		"messages":[{"role":"user","content":"hi"}]}`), info)
	if err != nil {
		t.Fatal(err)
	}
	body := decodeQoderBody(t, raw)
	first := body["messages"].([]any)[0].(map[string]any)
	text, _ := first["content"].(string)
	if !strings.HasPrefix(text, qoderSystemLine) {
		t.Fatalf("the routed-on line is missing: %q", text[:minInt(len(text), 120)])
	}
	if len(text) < 5000 {
		t.Fatalf("the embedded prompt is %d chars, which is not Qoder's own", len(text))
	}
	if text != qoderBaseSystemPrompt {
		t.Fatal("the body must carry the embedded prompt verbatim")
	}
}

// The embedded prompt is data next to the code, and data can be the wrong file:
// it has to still open with the line the endpoint routes on.
func TestQoderEmbeddedPromptStartsWithTheRoutedLine(t *testing.T) {
	if !strings.HasPrefix(qoderBaseSystemPrompt, qoderSystemLine) {
		t.Fatalf("the embedded prompt opens with %q", qoderBaseSystemPrompt[:minInt(len(qoderBaseSystemPrompt), 120)])
	}
	if !strings.Contains(qoderBaseSystemPrompt, "</example>") {
		t.Fatal("the prompt looks truncated: Qoder's own ends with its examples")
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Every key a Qoder client writes is present, so the endpoint is handed the
// shape its own client produces rather than a plain OpenAI body.
func TestQoderChatBodyCarriesTheClientsOwnShape(t *testing.T) {
	info := qoderTestModelInfo(t)
	raw, err := qoderChatBody("ultimate", []byte(`{
		"model":"ultimate",
		"messages":[{"role":"user","content":"hi"}]}`), info)
	if err != nil {
		t.Fatal(err)
	}
	body := decodeQoderBody(t, raw)
	for _, key := range []string{
		"request_id", "request_set_id", "chat_record_id", "stream", "chat_task",
		"chat_context", "image_urls", "is_reply", "is_retry", "session_id",
		"code_language", "source", "version", "chat_prompt", "parameters",
		"aliyun_user_type", "session_type", "agent_id", "task_id", "model_config",
		"messages", "tools", "business",
	} {
		if _, ok := body[key]; !ok {
			t.Fatalf("%q is missing from the body: %v", key, body)
		}
	}
	if body["stream"] != true {
		t.Fatal("the upstream only answers as a stream")
	}
	chatContext := body["chat_context"].(map[string]any)
	if text := chatContext["text"].(map[string]any); text["text"] != "hi" {
		t.Fatalf("chat_context.text must carry the prompt, got %v", chatContext["text"])
	}
	business := body["business"].(map[string]any)
	if business["model_key"] != info.Key {
		t.Fatalf("the turn is billed against its model key, got %v", business["model_key"])
	}
	if business["product"] != qoderBusinessProduct || business["version"] != qoderCOSYVersion {
		t.Fatalf("business identity wrong: %v", business)
	}
	config := body["model_config"].(map[string]any)
	if config["format"] != "openai" || config["source"] != "system" || config["model"] != "" {
		t.Fatalf("model_config shape wrong: %v", config)
	}
}

// A conversation keeps one window id between turns, while each new question
// gets its own turn id: the first keys the prompt cache, the second groups one
// answer's tool steps for billing.
func TestQoderChatBodyKeepsTheWindowAndMovesTheTurn(t *testing.T) {
	info := qoderTestModelInfo(t)
	build := func(messages string) map[string]any {
		t.Helper()
		raw, err := qoderChatBody("ultimate", []byte(`{"model":"ultimate","messages":`+messages+`}`), info)
		if err != nil {
			t.Fatal(err)
		}
		return decodeQoderBody(t, raw)
	}
	first := build(`[{"role":"user","content":"write a parser"}]`)
	second := build(`[{"role":"user","content":"write a parser"},{"role":"assistant","content":"done"},{"role":"user","content":"and now add tests"}]`)
	if first["session_id"] != second["session_id"] {
		t.Fatalf("the window must survive the next turn: %v then %v", first["session_id"], second["session_id"])
	}
	if first["chat_record_id"] == second["chat_record_id"] {
		t.Fatal("a new question must be its own turn")
	}
	if first["request_id"] == second["request_id"] {
		t.Fatal("every request is its own request")
	}
	// Re-sending the same question (a retry, or a tool step) reuses its turn.
	again := build(`[{"role":"user","content":"write a parser"}]`)
	if again["chat_record_id"] != first["chat_record_id"] {
		t.Fatal("the same question must reuse its turn id")
	}
}

// A client that names its own conversation keeps that name as the window.
func TestQoderChatBodyUsesTheClientsOwnSession(t *testing.T) {
	info := qoderTestModelInfo(t)
	raw, err := qoderChatBodyWith("ultimate", []byte(`{
		"model":"ultimate",
		"messages":[{"role":"user","content":"hi"}]}`), info, qoderBodyContext{SessionKey: "session-42"})
	if err != nil {
		t.Fatal(err)
	}
	if body := decodeQoderBody(t, raw); body["session_id"] != "session-42" {
		t.Fatalf("session_id = %v, want the client's own id", body["session_id"])
	}
}

// qoderCNStructuredInfo is a model the CN site serves and that reads structured
// tool turns: its key is one the gateway keeps on the structured path.
func qoderCNStructuredInfo() qoderModelInfo {
	return qoderModelInfo{
		Key:            "qwen3.8-max",
		DisplayName:    "Qwen3.8-Max",
		MaxInputTokens: 180000,
		Efforts:        []string{"low", "high"},
		DefaultEffort:  "high",
		Thinks:         true,
	}
}

// Only the last tool result reaches the endpoint whole; older ones are cut to
// the same fixed bytes on every later turn, which is what keeps the prefix
// stable enough for the cache to hit.
func TestQoderChatBodyTrimsOlderToolResults(t *testing.T) {
	info := qoderCNStructuredInfo()
	long := strings.Repeat("detailed output line\n", 400)
	body := func() string {
		return `[{"role":"user","content":"read both"},` +
			`{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}},{"id":"call_2","type":"function","function":{"name":"read","arguments":"{}"}}]},` +
			`{"role":"tool","tool_call_id":"call_1","content":` + jsonString(long) + `},` +
			`{"role":"tool","tool_call_id":"call_2","content":` + jsonString(long) + `}]`
	}
	first := qoderToolResultsOf(t, build(t, body(), info, qoderBodyContext{Site: qoderCNSite}))
	second := qoderToolResultsOf(t, build(t, body(), info, qoderBodyContext{Site: qoderCNSite}))
	if len(first) != 2 {
		t.Fatalf("tool turns wrong: %v", first)
	}
	if first[0] == long {
		t.Fatal("an older result must be trimmed")
	}
	if !strings.Contains(first[0], strings.TrimSpace(qoderToolResultMarker)) {
		t.Fatalf("the trim marker is missing: %q", first[0])
	}
	if first[1] != long {
		t.Fatal("the result the turn is answering must stay whole")
	}
	if first[0] != second[0] {
		t.Fatal("a trimmed result must send the same bytes on every turn")
	}
}

func TestQoderChatBodyCarriesToolsAndToolTurns(t *testing.T) {
	info := qoderCNStructuredInfo()
	raw, err := qoderChatBodyWith("qwen3.8-max", []byte(`{
		"model":"qwen3.8-max",
		"messages":[
			{"role":"user","content":"read it"},
			{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"the file says hi"},
			{"role":"user","content":[{"type":"text","text":"and now?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}
		],
		"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{}}}}]
	}`), info, qoderBodyContext{Site: qoderCNSite})
	if err != nil {
		t.Fatal(err)
	}
	body := decodeQoderBody(t, raw)
	tools := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools wrong: %v", body["tools"])
	}
	function := tools[0].(map[string]any)["function"].(map[string]any)
	// Qoder validates the description as a string while the OpenAI spec makes it
	// optional and clients omit it — Claude Code's web_search tool is one.
	if _, ok := function["description"]; !ok {
		t.Fatal("a missing description must still be sent as an empty string")
	}
	if function["description"] != "" {
		t.Fatalf("description = %v", function["description"])
	}

	messages := body["messages"].([]any)
	var assistant, toolTurn, lastUser map[string]any
	for _, item := range messages {
		message := item.(map[string]any)
		switch message["role"] {
		case "assistant":
			assistant = message
		case "tool":
			toolTurn = message
		case "user":
			lastUser = message
		}
	}
	if assistant == nil {
		t.Fatal("the assistant turn with the call was dropped")
	}
	calls := assistant["tool_calls"].([]any)
	if len(calls) != 1 || calls[0].(map[string]any)["id"] != "call_1" {
		t.Fatalf("tool call not carried: %v", assistant["tool_calls"])
	}
	if toolTurn == nil || toolTurn["tool_call_id"] != "call_1" {
		t.Fatalf("the result must answer its call by id, got %v", toolTurn)
	}
	if _, ok := toolTurn["content"].(string); !ok {
		t.Fatalf("a tool turn holds text only, got %T", toolTurn["content"])
	}
	// A user turn's image survives as an image_url block.
	blocks := lastUser["content"].([]any)
	var sawImage bool
	for _, block := range blocks {
		if item := block.(map[string]any); item["type"] == "image_url" {
			sawImage = true
		}
	}
	if !sawImage {
		t.Fatalf("image dropped: %v", lastUser["content"])
	}
}

// A client that says tools must not be used still gets no tools: Qoder answers a
// request that offers them with a call whatever the choice said.
func TestQoderChatBodyDropsToolsWhenChoiceIsNone(t *testing.T) {
	info := qoderTestModelInfo(t)
	raw, err := qoderChatBody("ultimate", []byte(`{
		"model":"ultimate",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"function","function":{"name":"read_file","description":"read","parameters":{}}}],
		"tool_choice":"none"}`), info)
	if err != nil {
		t.Fatal(err)
	}
	body := decodeQoderBody(t, raw)
	// The key is present but empty: Qoder's client always sends its tools key,
	// and an absent one is not the body it writes.
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 0 {
		t.Fatalf("tools must be taken off the request: %v", body["tools"])
	}
}

func TestQoderChatBodyAsksForRequiredToolChoiceInWords(t *testing.T) {
	info := qoderTestModelInfo(t)
	raw, err := qoderChatBody("ultimate", []byte(`{
		"model":"ultimate",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"function","function":{"name":"read_file","description":"read","parameters":{}}}],
		"tool_choice":"required"}`), info)
	if err != nil {
		t.Fatal(err)
	}
	body := decodeQoderBody(t, raw)
	messages := body["messages"].([]any)
	text, _ := messages[0].(map[string]any)["content"].(string)
	if !strings.Contains(text, "You must call an available function") {
		t.Fatalf("the vendor's API is told the answer's format in words, got %q", text)
	}
}

// build runs one OpenAI request through the body builder.
func build(t *testing.T, messages string, info qoderModelInfo, context qoderBodyContext) map[string]any {
	t.Helper()
	raw, err := qoderChatBodyWith("ultimate", []byte(`{"model":"ultimate","messages":`+messages+`}`), info, context)
	if err != nil {
		t.Fatal(err)
	}
	return decodeQoderBody(t, raw)
}

// jsonString renders a Go string as a JSON string literal.
func jsonString(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(raw)
}

// qoderToolResultsOf reads the tool turns out of a built body, in order.
func qoderToolResultsOf(t *testing.T, body map[string]any) []string {
	t.Helper()
	var out []string
	for _, item := range body["messages"].([]any) {
		message := item.(map[string]any)
		if message["role"] != "tool" {
			continue
		}
		text, _ := message["content"].(string)
		out = append(out, text)
	}
	return out
}

// The level asked for has to become one the model actually offers, and thinking
// has to be turned off only for a model that can.
func TestQoderEffortSelection(t *testing.T) {
	offering := qoderTestModelInfo(t) // low, high (default), max; can be turned off
	alwaysThinking := qoderModelInfo{Thinks: true, AlwaysThinks: true, Efforts: []string{"low", "medium", "high"}}
	noThinking := qoderModelInfo{}
	cases := []struct {
		name         string
		asked        string
		info         qoderModelInfo
		wantThinking bool
		wantEffort   string
	}{
		{"nothing asked uses the model's default", "", offering, true, "high"},
		{"an offered level is passed through", "max", offering, true, "max"},
		{"nearest level, higher on a tie", "xhigh", offering, true, "max"},
		{"a model without thinking never thinks", "high", noThinking, false, ""},
		{"asking none on a model that can stop", "none", offering, false, ""},
		{"a model that always thinks thinks at its lowest", "none", alwaysThinking, true, "low"},
		{"an unnamed level falls back to the default", "turbo", offering, true, "high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			thinking, effort := qoderEffort(tc.asked, tc.info)
			if thinking != tc.wantThinking || effort != tc.wantEffort {
				t.Fatalf("qoderEffort(%q) = (%v, %q), want (%v, %q)", tc.asked, thinking, effort, tc.wantThinking, tc.wantEffort)
			}
		})
	}
}

// A bigger window can cost more credits, so it is asked for only when the
// conversation needs it — and the reply's own budget counts into it.
func TestQoderWindowFor(t *testing.T) {
	model := qoderModelInfo{MaxInputTokens: 180000, Windows: []int{200000, 400000, 1000000}, DefaultWindow: 200000}
	small, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	var chat map[string]any
	if err := json.Unmarshal(small, &chat); err != nil {
		t.Fatal(err)
	}
	if got := qoderWindowFor(chat, model, 32000); got != 200000 {
		t.Fatalf("a short conversation should keep the default window, got %d", got)
	}
	// The same conversation asking for a long reply needs the next window up:
	// a window holds the prompt and the reply together.
	if got := qoderWindowFor(chat, model, 250000); got != 400000 {
		t.Fatalf("a 250K reply does not fit the default window and should raise it, got %d", got)
	}
	huge := map[string]any{"messages": []any{map[string]any{"role": "user", "content": strings.Repeat("x", 3_000_000)}}}
	if got := qoderWindowFor(huge, model, 32000); got != 1000000 {
		t.Fatalf("an oversized conversation should take the largest window, got %d", got)
	}
	// A model that offers no windows at all is sent none: it is asked with what
	// it says it takes.
	if got := qoderWindowFor(chat, qoderModelInfo{MaxInputTokens: 180000}, 32000); got != 180000 {
		t.Fatalf("no windows should fall back to max_input_tokens, got %d", got)
	}
}

func TestQoderAskedEffortFromEitherDialect(t *testing.T) {
	cases := []struct{ body, want string }{
		{`{"reasoning_effort":"high"}`, "high"},
		{`{"thinking":{"type":"disabled"}}`, "none"},
		{`{"thinking":{"type":"enabled"}}`, ""},
		{`{}`, ""},
	}
	for _, tc := range cases {
		var chat map[string]any
		if err := json.Unmarshal([]byte(tc.body), &chat); err != nil {
			t.Fatal(err)
		}
		if got := qoderAskedEffort(chat); got != tc.want {
			t.Fatalf("qoderAskedEffort(%s) = %q, want %q", tc.body, got, tc.want)
		}
	}
}
