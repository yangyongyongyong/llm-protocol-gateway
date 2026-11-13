package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// pointWorkBuddyAt redirects both WorkBuddy builds at a test server until the
// returned restore function runs.
func pointWorkBuddyAt(t *testing.T, cn, ai string) {
	t.Helper()
	oldCN, oldCNChat, oldAI := workBuddyCNEndpoint, workBuddyCNChatEndpoint, workBuddyIntlEndpoint
	// Chat follows the same httptest server, so a test that points the account
	// endpoints at one server gets chat there too.
	workBuddyCNEndpoint, workBuddyCNChatEndpoint, workBuddyIntlEndpoint = cn, cn, ai
	t.Cleanup(func() {
		workBuddyCNEndpoint, workBuddyCNChatEndpoint, workBuddyIntlEndpoint = oldCN, oldCNChat, oldAI
	})
}

// messageContent reads one message's text, whichever shape the body left it in.
func messageContent(t *testing.T, payload map[string]any, index int) string {
	t.Helper()
	messages, _ := payload["messages"].([]any)
	if index >= len(messages) {
		t.Fatalf("messages = %d, want more than %d", len(messages), index)
	}
	message, _ := messages[index].(map[string]any)
	switch content := message["content"].(type) {
	case string:
		return content
	case []any:
		text := ""
		for _, item := range content {
			part, _ := item.(map[string]any)
			if piece, _ := part["text"].(string); piece != "" {
				text += piece
			}
		}
		return text
	}
	t.Fatalf("message %d has no text content: %+v", index, message)
	return ""
}

func TestPrepareWorkBuddyChatBody(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantSame bool
		check    func(t *testing.T, payload map[string]any)
	}{
		{
			name: "非流式且无 system：强制流式并补上 system",
			body: `{"model":"glm-5.3","stream":false,"messages":[{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, payload map[string]any) {
				if payload["stream"] != true {
					t.Fatalf("stream = %v, want true", payload["stream"])
				}
				messages, _ := payload["messages"].([]any)
				first, _ := messages[0].(map[string]any)
				if first["role"] != "system" {
					t.Fatalf("first message role = %v, want system", first["role"])
				}
				if len(messages) != 2 {
					t.Fatalf("messages = %d, want 2", len(messages))
				}
			},
		},
		{
			name: "已带 system 且流式：原样不动",
			body: `{"model":"glm-5.3","stream":true,"messages":[{"role":"system","content":"be nice"},{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, payload map[string]any) {
				messages, _ := payload["messages"].([]any)
				if len(messages) != 2 {
					t.Fatalf("messages = %d, want 2 (system not duplicated)", len(messages))
				}
			},
			wantSame: true,
		},
		{
			name: "带 messages 但 stream 缺省：补 stream",
			body: `{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, payload map[string]any) {
				if payload["stream"] != true {
					t.Fatalf("stream = %v, want true", payload["stream"])
				}
			},
		},
		{
			name:     "不是聊天请求（count_tokens 之类）：原样返回",
			body:     `{"model":"kimi-k3"}`,
			check:    func(*testing.T, map[string]any) {},
			wantSame: true,
		},
		{
			name: "Claude Code 自带的提示词：换成能过渠道校验的说法",
			body: `{"model":"glm-5.3","stream":true,"messages":[{"role":"system","content":"x-anthropic-billing-header: cc_version=2.1.280; cc_entrypoint=cli\nYou are Claude Code, Anthropic's official CLI for Claude.\nMain branch (you will usually use this for PRs): main"},{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, payload map[string]any) {
				text := messageContent(t, payload, 0)
				if strings.Contains(text, "x-anthropic-billing-header") {
					t.Fatalf("billing header survived: %q", text)
				}
				if strings.Contains(text, "official CLI for Claude") {
					t.Fatalf("the flagged wording survived: %q", text)
				}
				if !strings.Contains(text, "You are Claude Code, Anthropic's CLI for Claude") {
					t.Fatalf("rewrite lost its meaning: %q", text)
				}
				if !strings.Contains(text, "Main branch (usually the base for PRs): main") {
					t.Fatalf("main-branch line untouched: %q", text)
				}
			},
		},
		{
			// The two scopes differ by design. The global rules (billing header,
			// client template sentences, the literal refusal code) match the body
			// as a whole, so they rewrite wherever the text appears — the upstream
			// does not look at the role. The identity rules stay on the turns that
			// declare who the agent is: a user asking about a brand was measured
			// not to trigger the channel check, and rewriting their words would
			// drop what they said.
			name: "全局规则不分角色，身份规则只动声明身份的那轮",
			body: `{"model":"glm-5.3","stream":true,"messages":[{"role":"system","content":"You are an AI agent powered by DeepSeek Harness."},{"role":"user","content":"You are Claude Code, Anthropic's official CLI for Claude. Both are DeepSeek Harness users."}]}`,
			check: func(t *testing.T, payload map[string]any) {
				if got := messageContent(t, payload, 1); got != "You are Claude Code, Anthropic's CLI for Claude. Both are DeepSeek Harness users." {
					t.Fatalf("user turn: global rules must rewrite, identity rules must not: %q", got)
				}
				if got := messageContent(t, payload, 0); got != "You are an AI agent, powered by DeepSeek Harness." {
					t.Fatalf("system turn identity sentence: %q", got)
				}
			},
		},
		{
			name: "整条 system 只剩计费头：丢掉这一轮",
			body: `{"model":"glm-5.3","stream":true,"messages":[{"role":"system","content":"x-anthropic-billing-header: cc_version=2.1.280"},{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, payload map[string]any) {
				messages, _ := payload["messages"].([]any)
				if len(messages) != 2 {
					t.Fatalf("messages = %d, want 2 (the emptied system turn goes, the filler comes)", len(messages))
				}
				first, _ := messages[0].(map[string]any)
				if first["role"] != "system" || first["content"] != workBuddySystemPrompt {
					t.Fatalf("first = %+v", first)
				}
			},
		},
		{
			name: "分块的 content 也改写",
			body: `{"model":"glm-5.3","stream":true,"messages":[{"role":"system","content":[{"type":"text","text":"Main branch (you will usually use this for PRs): main"}]},{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, payload map[string]any) {
				messages, _ := payload["messages"].([]any)
				first, _ := messages[0].(map[string]any)
				parts, _ := first["content"].([]any)
				part, _ := parts[0].(map[string]any)
				if text, _ := part["text"].(string); text != "Main branch (usually the base for PRs): main" {
					t.Fatalf("part = %q", text)
				}
			},
		},
		{
			name: "模型把身份行引回 assistant 轮：同样改写（否则整段会话永久 11128）",
			body: `{"model":"glm-5.3","stream":true,"messages":[{"role":"system","content":"be nice"},{"role":"user","content":"hi"},{"role":"assistant","content":"You are Claude Code, Anthropic's official CLI for Claude."},{"role":"user","content":"再试一次"}]}`,
			check: func(t *testing.T, payload map[string]any) {
				got := messageContent(t, payload, 2)
				if strings.Contains(got, "official CLI for Claude") {
					t.Fatalf("the flagged wording survived in an assistant turn: %q", got)
				}
				if got != "You are Claude Code, Anthropic's CLI for Claude." {
					t.Fatalf("assistant turn = %q", got)
				}
			},
		},
		{
			// Only a system turn is dropped when nothing is left. An assistant turn
			// keeps its place even empty: it holds the round's position, and removing
			// it would move the round onto a different message. The reference WorkBuddy
			// implementation draws the line the same way (magpie packages/workbuddy
			// index.mjs `unflag`: `if (t.trim() || m.role !== "system") m.content = t`).
			name: "assistant 轮只剩计费头又没有工具调用：保留这一轮，内容清空",
			body: `{"model":"glm-5.3","stream":true,"messages":[{"role":"system","content":"be nice"},{"role":"user","content":"hi"},{"role":"assistant","content":"x-anthropic-billing-header: cc_version=2.1.280;"},{"role":"user","content":"继续"}]}`,
			check: func(t *testing.T, payload map[string]any) {
				messages, _ := payload["messages"].([]any)
				assistantSeen := 0
				for _, item := range messages {
					message, _ := item.(map[string]any)
					if message["role"] != "assistant" {
						continue
					}
					assistantSeen++
					if content, _ := message["content"].(string); strings.TrimSpace(content) != "" {
						t.Fatalf("the billing header survived: %+v", message)
					}
				}
				if assistantSeen != 1 {
					t.Fatalf("assistant turns = %d, want the emptied one kept", assistantSeen)
				}
			},
		},
		{
			name: "assistant 轮只剩计费头但带 tool_calls：调用留下来",
			body: `{"model":"glm-5.3","stream":true,"messages":[{"role":"system","content":"be nice"},{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.280;"}],"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"}]}`,
			check: func(t *testing.T, payload map[string]any) {
				messages, _ := payload["messages"].([]any)
				found := false
				for _, item := range messages {
					message, _ := item.(map[string]any)
					if message["role"] != "assistant" {
						continue
					}
					found = true
					if calls, _ := message["tool_calls"].([]any); len(calls) != 1 {
						t.Fatalf("tool_calls = %v, want the one call kept", message["tool_calls"])
					}
				}
				if !found {
					t.Fatal("an assistant turn carrying a tool call was dropped")
				}
			},
		},
		{
			name: "Codex 的 developer 轮按 system 发",
			body: `{"model":"glm-5.3","stream":true,"messages":[{"role":"developer","content":"You are a helpful assistant."},{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, payload map[string]any) {
				messages, _ := payload["messages"].([]any)
				first, _ := messages[0].(map[string]any)
				if first["role"] != "system" {
					t.Fatalf("first role = %v, want system", first["role"])
				}
				if len(messages) != 2 {
					t.Fatalf("messages = %d, want 2 (no system prepended twice)", len(messages))
				}
			},
		},
		{
			name:     "改写过的请求再过一遍：不再改动",
			body:     `{"model":"glm-5.3","stream":true,"messages":[{"role":"system","content":"You are Claude Code, Anthropic's CLI for Claude.\nThe Codex CLI is an open source project led by OpenAI."},{"role":"user","content":"hi"}]}`,
			check:    func(t *testing.T, payload map[string]any) {},
			wantSame: true,
		},
		{
			name: "Anthropic 的工具块混在 Chat 里：读成 tool_calls 与 role:tool",
			body: `{"model":"glm-5.3","stream":true,"messages":[` +
				`{"role":"system","content":"be nice"},` +
				`{"role":"assistant","content":[{"type":"text","text":"我先读文件"},{"type":"tool_use","id":"toolu_01ABC","name":"Read","input":{"path":"a.txt"}}]},` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01ABC","content":[{"type":"text","text":"a 的内容"}]},{"type":"text","text":"继续"}]}]}`,
			check: func(t *testing.T, payload map[string]any) {
				messages, _ := payload["messages"].([]any)
				if len(messages) != 4 {
					t.Fatalf("messages = %d, want system/assistant/tool/user: %+v", len(messages), messages)
				}
				assistant, _ := messages[1].(map[string]any)
				calls, _ := assistant["tool_calls"].([]any)
				if len(calls) != 1 {
					t.Fatalf("the call stayed a content part: %+v", assistant)
				}
				call, _ := calls[0].(map[string]any)
				if call["id"] != "toolu_01ABC" || call["type"] != "function" {
					t.Fatalf("call = %+v", call)
				}
				function, _ := call["function"].(map[string]any)
				if function["name"] != "Read" || !strings.Contains(function["arguments"].(string), "a.txt") {
					t.Fatalf("function = %+v", function)
				}
				if assistant["content"] != "我先读文件" {
					t.Fatalf("the assistant's own text moved: %v", assistant["content"])
				}
				answered, _ := messages[2].(map[string]any)
				if answered["role"] != "tool" || answered["tool_call_id"] != "toolu_01ABC" || answered["content"] != "a 的内容" {
					t.Fatalf("answer = %+v", answered)
				}
				next, _ := messages[3].(map[string]any)
				if next["role"] != "user" || next["content"] != "继续" {
					t.Fatalf("the user's own words = %+v", next)
				}
			},
		},
		{
			name: "并行结果乱序、一个调用没结果：重排，并补上被打断的那份答复",
			body: `{"model":"glm-5.3","stream":true,"messages":[` +
				`{"role":"system","content":"s"},` +
				`{"role":"assistant","content":"","tool_calls":[` +
				`{"id":"c1","type":"function","function":{"name":"Bash","arguments":"{\"a\":1}"}},` +
				`{"id":"c2","type":"function","function":{"name":"Read","arguments":"{}"}},` +
				`{"id":"c3","type":"function","function":{"name":"Grep","arguments":"{}"}}]},` +
				`{"role":"tool","tool_call_id":"c2","content":"二"},` +
				`{"role":"tool","tool_call_id":"c1","content":"一"},` +
				`{"role":"user","content":"下一步"}]}`,
			check: func(t *testing.T, payload map[string]any) {
				messages, _ := payload["messages"].([]any)
				if len(messages) != 6 {
					t.Fatalf("messages = %d, want the third answer synthesized: %+v", len(messages), messages)
				}
				for position, want := range []string{"c1", "c2", "c3"} {
					tool, _ := messages[2+position].(map[string]any)
					if tool["role"] != "tool" || tool["tool_call_id"] != want {
						t.Fatalf("answer %d = %+v, want %s", position, tool, want)
					}
				}
				closing, _ := messages[4].(map[string]any)
				if !strings.Contains(closing["content"].(string), "turn was interrupted") {
					t.Fatalf("an unanswered call got no closing answer: %+v", closing)
				}
				tail, _ := messages[5].(map[string]any)
				if tail["role"] != "user" {
					t.Fatalf("the user's turn moved: %+v", tail)
				}
			},
		},
		{
			name: "答复找不到对应调用（会话被压缩掉了）：结果留成用户轮，不编造调用",
			body: `{"model":"glm-5.3","stream":true,"messages":[` +
				`{"role":"system","content":"s"},` +
				`{"role":"tool","tool_call_id":"gone","content":"孤儿结果"},` +
				`{"role":"user","content":"继续"}]}`,
			check: func(t *testing.T, payload map[string]any) {
				messages, _ := payload["messages"].([]any)
				if len(messages) != 3 {
					t.Fatalf("messages = %d, want the orphan turned into a user turn", len(messages))
				}
				for _, item := range messages {
					if message, _ := item.(map[string]any); message["role"] == "tool" {
						t.Fatalf("an answer to no call stayed a tool turn: %+v", messages)
					}
				}
				orphan, _ := messages[1].(map[string]any)
				if orphan["role"] != "user" || orphan["content"] != "孤儿结果" {
					t.Fatalf("orphan = %+v, want the result to survive as the user's", orphan)
				}
			},
		},
		{
			name: "同一调用两份答复：第一份留着",
			body: `{"model":"glm-5.3","stream":true,"messages":[` +
				`{"role":"system","content":"s"},` +
				`{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"Bash","arguments":"{}"}}]},` +
				`{"role":"tool","tool_call_id":"c1","content":"第一份"},` +
				`{"role":"tool","tool_call_id":"c1","content":"第二份"}]}`,
			check: func(t *testing.T, payload map[string]any) {
				messages, _ := payload["messages"].([]any)
				if len(messages) != 3 {
					t.Fatalf("messages = %d, want the second answer dropped", len(messages))
				}
				tool, _ := messages[2].(map[string]any)
				if tool["content"] != "第一份" {
					t.Fatalf("the wrong answer stood: %+v", tool)
				}
			},
		},
		{
			name: "tool_choice 的对象写法：换成工具名",
			body: `{"model":"glm-5.3","stream":true,"tool_choice":{"type":"function","function":{"name":"Bash"}},` +
				`"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, payload map[string]any) {
				if payload["tool_choice"] != "Bash" {
					t.Fatalf("tool_choice = %+v", payload["tool_choice"])
				}
			},
		},
		{
			name: "tool_choice 为 none：连 tools 一起不收",
			body: `{"model":"glm-5.3","stream":true,"tool_choice":"none","tools":[{"type":"function","function":{"name":"Bash"}}],` +
				`"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, payload map[string]any) {
				if _, exists := payload["tool_choice"]; exists {
					t.Fatal("tool_choice none survived")
				}
				if _, exists := payload["tools"]; exists {
					t.Fatal("tools survived a choice of none")
				}
			},
		},
		{
			name:     "已经配平的工具轮与 auto：一个字都不动",
			body:     `{"model":"glm-5.3","stream":true,"tool_choice":"auto","messages":[{"role":"system","content":"s"},{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"Bash","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"ok"},{"role":"user","content":"继续"}]}`,
			check:    func(t *testing.T, payload map[string]any) {},
			wantSame: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, changed := prepareWorkBuddyChatBody([]byte(testCase.body))
			if changed == testCase.wantSame {
				t.Fatalf("changed = %v, want changed = %v", changed, !testCase.wantSame)
			}
			var payload map[string]any
			if err := json.Unmarshal(got, &payload); err != nil {
				t.Fatalf("result is not valid JSON: %v", err)
			}
			testCase.check(t, payload)
		})
	}
}

// TestWorkBuddyPromptRefusalAsksTheNextOneButRestsNobody: WorkBuddy turning the
// agent's own prompt away is not the account's fault, so the backup chain moves
// on and the provider is never painted 异常.
func TestWorkBuddyPromptRefusalAsksTheNextOneButRestsNobody(t *testing.T) {
	refusal := []byte(`{"code":11128,"msg":"Illegal API invocation from an unapproved channel","displayMsg":{"zh":"请求被拦截，请重新发送"}}`)
	class, reason := classifyProviderFailover(http.StatusBadRequest, refusal, nil, nil)
	if class != failoverPromptRefusal || reason != "prompt_refused" {
		t.Fatalf("class = %v reason = %q, want the prompt refusal", class, reason)
	}
	if !shouldFailoverProvider(http.StatusBadRequest, refusal, nil) {
		t.Fatal("a refusal another provider may take did not fail over")
	}
	server := &Server{}
	for attempt := 0; attempt < 3; attempt++ {
		if !server.applyProviderFailoverOutcome("workbuddy-1", http.StatusBadRequest, refusal, "", nil) {
			t.Fatal("the backup chain did not move on")
		}
	}
	if _, unavailable := server.providerNextRetry("workbuddy-1"); unavailable {
		t.Fatal("a prompt the vendor turns away rested the account")
	}
	server.providerAvailabilityMu.Lock()
	count := server.providerSoftFailures["workbuddy-1"].count
	server.providerAvailabilityMu.Unlock()
	if count != 0 {
		t.Fatalf("soft failures counted %d for a refusal of the prompt", count)
	}
	if note := summarizeUpstreamHTTPError(http.StatusBadRequest, refusal); !strings.Contains(note, WorkBuddyRefusedNote) {
		t.Fatalf("the client is told %q, with no way out", note)
	}
	// An ordinary bad request stays the client's own problem.
	if class, _ := classifyProviderFailover(http.StatusBadRequest, []byte(`{"error":{"message":"tools[0].type is illegal"}}`), nil, nil); class != failoverNone {
		t.Fatalf("a bad schema class = %v, want none", class)
	}
}

// serveAsClaude posts one Anthropic request through the gateway against a single
// provider, on its own route and API key, and returns what the client got.
func serveAsClaude(t *testing.T, provider domain.Provider, model string, stream bool, system string) *httptest.ResponseRecorder {
	t.Helper()
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{provider},
		Routes: []domain.Route{{
			ID: "r1", Name: "R1", ProviderID: provider.ID,
			OutputProtocol: domain.ProtocolClaude, Enabled: true,
		}},
		APIKeys: []domain.APIKey{{ID: "k1", Name: "K1", Key: "sk-gw", RouteID: "r1", Enabled: true, StreamEnabled: true}},
	})
	body, err := json.Marshal(map[string]any{
		"model": model, "max_tokens": 32, "stream": stream,
		"system":   []map[string]any{{"type": "text", "text": system}},
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(string(body)))
	request.Header.Set("x-api-key", "sk-gw")
	request.Header.Set("anthropic-version", "2023-06-01")
	recorder := httptest.NewRecorder()
	NewServer(router, monitor.NewStore()).handleClaudeMessages(recorder, request)
	return recorder
}

// TestClaudeMessagesToWorkBuddyShapesTheUpstreamRequest: a Claude client reaches
// a WorkBuddy plan through its own converter, which once built and sent the
// request by hand and so skipped everything WorkBuddy asks for — the streaming
// request, a leading system turn, the words its channel check turns away, and
// the account's own headers. What the plan sees is now what a Chat client's own
// path sends, and a client that asked for one object still gets one.
func TestClaudeMessagesToWorkBuddyShapesTheUpstreamRequest(t *testing.T) {
	var gotPath string
	var gotAuth, gotUserID, gotDomain, gotIDEType, gotUserAgent string
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth, gotUserID = r.Header.Get("Authorization"), r.Header.Get("X-User-Id")
		gotDomain, gotIDEType = r.Header.Get("X-Domain"), r.Header.Get("X-IDE-Type")
		gotUserAgent = r.Header.Get("User-Agent")
		raw, _ := io.ReadAll(r.Body)
		gotBody = map[string]any{}
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"id":"c1","created":1700000000,"model":"glm-5.3","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
			`data: {"id":"c1","created":1700000000,"model":"glm-5.3","choices":[{"index":0,"delta":{"content":"收到"}}]}`,
			`data: {"id":"c1","created":1700000000,"model":"glm-5.3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`,
			`data: [DONE]`, "", "",
		}, "\n"))
	}))
	defer upstream.Close()
	pointWorkBuddyAt(t, upstream.URL, upstream.URL)

	provider := domain.Provider{
		ID: "wb1", Name: "WB", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeWorkBuddy, BaseURL: upstream.URL + "/v2",
		WorkBuddy: &domain.WorkBuddyCredential{
			AccessToken: "wb-access", RefreshToken: "wb-refresh", UserID: "u-1",
			Domain:    "copilot.tencent.com",
			ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}

	flagged := "x-anthropic-billing-header: cc_version=2.1.280\n" +
		"You are Claude Code, Anthropic's official CLI for Claude.\n" +
		"Main branch (you will usually use this for PRs): main"

	recorder := serveAsClaude(t, provider, "glm-5.3", false, flagged)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if gotPath != "/v2/chat/completions" {
		t.Fatalf("upstream path = %q", gotPath)
	}
	if gotBody["stream"] != true {
		t.Fatalf("WorkBuddy was asked for a non-stream chat: %v", gotBody["stream"])
	}
	messages, _ := gotBody["messages"].([]any)
	if len(messages) == 0 {
		t.Fatal("no messages reached the plan")
	}
	first, _ := messages[0].(map[string]any)
	if first["role"] != "system" {
		t.Fatalf("first message role = %v, want system", first["role"])
	}
	text, _ := first["content"].(string)
	if strings.Contains(text, "x-anthropic-billing-header") || strings.Contains(text, "official CLI for Claude") {
		t.Fatalf("the flagged wording still goes to the plan: %q", text)
	}
	if !strings.Contains(text, "You are Claude Code, Anthropic's CLI for Claude") ||
		!strings.Contains(text, "Main branch (usually the base for PRs)") {
		t.Fatalf("the rewrite lost the prompt's meaning: %q", text)
	}
	if gotAuth != "Bearer wb-access" || gotUserID != "u-1" || gotDomain != "copilot.tencent.com" ||
		gotIDEType != "WorkBuddy" || gotUserAgent != workBuddyCNSite.chatUserAgent() {
		t.Fatalf("the plan was not signed as its account: auth=%q user=%q domain=%q ide=%q ua=%q",
			gotAuth, gotUserID, gotDomain, gotIDEType, gotUserAgent)
	}
	var completion map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &completion); err != nil {
		t.Fatalf("a non-streaming Claude client did not get one object: %v (%s)", err, recorder.Body.String())
	}
	if completion["type"] != "message" {
		t.Fatalf("client answer = %+v, want a Claude message", completion)
	}
	blocks, _ := completion["content"].([]any)
	if len(blocks) == 0 {
		t.Fatalf("no content block came back: %s", recorder.Body.String())
	}
	block, _ := blocks[0].(map[string]any)
	if block["text"] != "收到" {
		t.Fatalf("content = %+v, want the rebuilt answer", block)
	}

	if streamed := serveAsClaude(t, provider, "glm-5.3", true, flagged); streamed.Code != http.StatusOK ||
		!strings.Contains(streamed.Body.String(), "event:") {
		t.Fatalf("a streaming Claude client got %d: %s", streamed.Code, streamed.Body.String())
	}
}

// TestClaudeMessagesToTraeSignsItsAgentCall: Trae is not an OpenAI endpoint, so a
// Claude client's request has to reach Trae's own signed agent call and its event
// stream has to come back as Claude events. The converted path used to post a
// plain chat/completions to Trae's host with nothing but a bearer.
func TestClaudeMessagesToTraeSignsItsAgentCall(t *testing.T) {
	var paths []string
	var gotAuth, gotDevice, gotUID string
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		gotAuth, gotDevice = r.Header.Get("Authorization"), r.Header.Get("x-device-id")
		gotUID = r.Header.Get("x-uid")
		raw, _ := io.ReadAll(r.Body)
		gotBody = map[string]any{}
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: output\n"+`data: {"response":"收到"}`+"\n\n"+
			"event: done\n"+`data: {"finish_reason":"stop"}`+"\n\n")
	}))
	defer upstream.Close()

	site := traeSites["trae-cn"]
	oldAPI := site.api
	site.api = upstream.URL
	traeSites["trae-cn"] = site
	t.Cleanup(func() {
		site.api = oldAPI
		traeSites["trae-cn"] = site
	})

	provider := domain.Provider{
		ID: "trae1", Name: "Trae", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeTraeCN, DefaultModel: "glm-5.2",
		Trae: &domain.TraeCredential{
			AccessToken: "trae-jwt", RefreshToken: "trae-refresh", UserID: "u-9",
			DeviceID: "1234567890123456789", MachineID: strings.Repeat("ab", 16),
			ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}

	recorder := serveAsClaude(t, provider, "glm-5.2", false, "You are a helpful assistant.")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	for _, path := range paths {
		if path != traeChatPath {
			t.Fatalf("Trae was asked %q, want only its agent call %q", path, traeChatPath)
		}
	}
	if gotAuth != "Cloud-IDE-JWT trae-jwt" || gotUID != "u-9" || gotDevice != "1234567890123456789" {
		t.Fatalf("the agent call was not signed as the account: auth=%q uid=%q device=%q", gotAuth, gotUID, gotDevice)
	}
	if function, _ := gotBody["function"].(string); function == "" {
		t.Fatalf("no agent function was named: %+v", gotBody)
	}
	if gotBody["stream"] != true {
		t.Fatalf("Trae only answers a stream, request was: %+v", gotBody)
	}
	if session, _ := gotBody["session_id"].(string); len(session) != 36 {
		t.Fatalf("session_id = %q, want the conversation's id", session)
	}
	var completion map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &completion); err != nil {
		t.Fatalf("a non-streaming Claude client did not get one message: %v (%s)", err, recorder.Body.String())
	}
	blocks, _ := completion["content"].([]any)
	if completion["type"] != "message" || len(blocks) == 0 {
		t.Fatalf("client answer = %s", recorder.Body.String())
	}
	block, _ := blocks[0].(map[string]any)
	if block["text"] != "收到" {
		t.Fatalf("content = %+v, want Trae's answer rebuilt", block)
	}

	streamed := serveAsClaude(t, provider, "glm-5.2", true, "You are a helpful assistant.")
	if streamed.Code != http.StatusOK || !strings.Contains(streamed.Body.String(), "event:") {
		t.Fatalf("a streaming Claude client got %d: %s", streamed.Code, streamed.Body.String())
	}
}

// TestClaudeMessagesToQoderReachesItsSender: Qoder takes a signed, encoded agent
// request rather than a chat/completions body, and needs the account's model
// listing to name the configuration it sends with it. Reaching that listing is
// what proves a Claude client flows through Qoder's own sender.
func TestClaudeMessagesToQoderReachesItsSender(t *testing.T) {
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	site := qoderGlobalSite
	oldAPI := site.API
	site.API = upstream.URL
	qoderGlobalSite = site
	t.Cleanup(func() {
		site.API = oldAPI
		qoderGlobalSite = site
	})

	provider := domain.Provider{
		ID: "qoder1", Name: "Qoder", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeQoderPAT, DefaultModel: "ultimate",
		QoderPAT: &domain.QoderCredential{
			AccessToken: "qoder-token", UID: "q-1", MachineID: strings.Repeat("cd", 16),
			ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}

	recorder := serveAsClaude(t, provider, "ultimate", false, "You are a helpful assistant.")
	for _, path := range paths {
		if path == "/chat/completions" {
			t.Fatalf("Qoder got a bare chat/completions POST: %v", paths)
		}
	}
	if len(paths) == 0 {
		t.Fatal("the Claude client's request never reached Qoder's sender")
	}
	if recorder.Code == http.StatusOK {
		t.Fatalf("an unusable Qoder listing answered 200: %s", recorder.Body.String())
	}
}

func TestBuildWorkBuddyCompletionRebuildsStream(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-1","created":1700000000,"model":"glm-5.3","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":"你"}}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":"好"}}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"get_time","arguments":"{\"z"}}]}}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"one\"}"}}]}}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`,
		`data: [DONE]`,
		"",
	}, "\n")

	completion, err := buildWorkBuddyCompletion([]byte(stream), "glm-5.3")
	if err != nil {
		t.Fatalf("build completion: %v", err)
	}
	if completion["object"] != "chat.completion" {
		t.Fatalf("object = %v, want chat.completion", completion["object"])
	}
	if completion["id"] != "chatcmpl-1" {
		t.Fatalf("id = %v, want chatcmpl-1", completion["id"])
	}
	choices, _ := completion["choices"].([]map[string]any)
	if len(choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(choices))
	}
	message, _ := choices[0]["message"].(map[string]any)
	if message["content"] != "你好" {
		t.Fatalf("content = %v, want 你好", message["content"])
	}
	if choices[0]["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %v, want tool_calls", choices[0]["finish_reason"])
	}
	calls, _ := message["tool_calls"].([]map[string]any)
	if len(calls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(calls))
	}
	function, _ := calls[0]["function"].(map[string]any)
	if function["name"] != "get_time" {
		t.Fatalf("name = %v, want get_time", function["name"])
	}
	if function["arguments"] != `{"zone"}` {
		t.Fatalf("arguments = %v, want %q", function["arguments"], `{"zone"}`)
	}
	usage, _ := completion["usage"].(map[string]any)
	if usage["total_tokens"] != 18 {
		t.Fatalf("total_tokens = %v, want 18", usage["total_tokens"])
	}
}

func TestBuildWorkBuddyCompletionRejectsEmptyStream(t *testing.T) {
	if _, err := buildWorkBuddyCompletion([]byte("data: [DONE]\n\n"), "glm-5.3"); err == nil {
		t.Fatal("expected an error for a stream with no chunks")
	}
}

func TestWorkBuddyCallSurfacesBusinessErrorCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"code":11217,"msg":"token not ready","data":null}`)
	}))
	defer server.Close()

	err := workBuddyCall(t.Context(), workBuddyCNSite, http.MethodGet, server.URL+"/v2/plugin/auth/token", nil, nil, nil)
	code, ok := workBuddyErrorCode(err)
	if !ok || code != workBuddyRetryTokenCode {
		t.Fatalf("err = %v (code %d, ok %v), want retry code %d", err, code, ok, workBuddyRetryTokenCode)
	}
}

func TestWorkBuddyCallUnwrapsData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"msg":"ok","data":{"accessToken":"at","refreshToken":"rt","expiresIn":3600}}`)
	}))
	defer server.Close()

	var got workBuddyCredentialPayload
	if err := workBuddyCall(t.Context(), workBuddyCNSite, http.MethodGet, server.URL, nil, map[string]any{}, &got); err != nil {
		t.Fatalf("call: %v", err)
	}
	if got.AccessToken != "at" || got.RefreshToken != "rt" {
		t.Fatalf("payload = %+v", got)
	}
	credential := got.toCredential(domain.WorkBuddyCredential{})
	expiresAt, err := time.Parse(time.RFC3339, credential.ExpiresAt)
	if err != nil {
		t.Fatalf("expiresAt %q: %v", credential.ExpiresAt, err)
	}
	if delta := time.Until(expiresAt); delta < 55*time.Minute || delta > 61*time.Minute {
		t.Fatalf("expiry computed from expiresIn is %v out; want ~1h", delta)
	}
}

func TestApplyWorkBuddyHeadersCNChatFingerprint(t *testing.T) {
	t.Setenv("WORKBUDDY_DEVICE_TOKEN", "")
	t.Setenv("WORKBUDDY_DEVICE_TOKEN_FILE", "")
	credential := domain.WorkBuddyCredential{
		AccessToken: "jwt-access", UserID: "uid-1", Domain: "copilot.tencent.com",
	}
	request, err := http.NewRequest(http.MethodPost, "https://example.test/v2/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	applyWorkBuddyHeaders(request, domain.Provider{AuthType: domain.AuthTypeWorkBuddy, WorkBuddy: &credential})

	site := workBuddyCNSite
	for header, want := range map[string]string{
		"Authorization":       "Bearer jwt-access",
		"X-User-Id":           "uid-1",
		"X-Domain":            "copilot.tencent.com",
		"X-Product":           "SaaS",
		"X-IDE-Type":          "WorkBuddy",
		"User-Agent":          site.chatUserAgent(),
		"X-Agent-Intent":      "craft",
		"X-Agent-Purpose":     "conversation",
		"X-Agent-Runtime":     workBuddyAgentRuntimeLocal,
		"X-Agent-Type":        workBuddyAgentTypeMain,
		"X-Requested-With":    "XMLHttpRequest",
		"X-IDE-Name":          "WorkBuddy",
		"X-IDE-Version":       workBuddyClientVersionCN,
		"X-Private-Data":      "false",
		"X-CodeBuddy-Request": "1",
		"Accept":              "application/json",
		"x-stainless-lang":    workBuddySDKLang,
		"x-stainless-os":      workBuddySDKOS,
		"x-stainless-arch":    workBuddySDKArch,
		"x-stainless-runtime": workBuddySDKRuntime,
	} {
		if got := request.Header.Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
	// Conversations and ACP connections are hyphenated UUIDv7s; request and
	// message ids are 32 hex. Sending the wrong shape in either is visible.
	if got := request.Header.Get("X-Conversation-ID"); len(got) != 36 || !strings.Contains(got, "-") {
		t.Fatalf("X-Conversation-ID = %q, want a hyphenated UUID", got)
	}
	if got := request.Header.Get("acp-connection-id"); len(got) != 36 || !strings.Contains(got, "-") {
		t.Fatalf("acp-connection-id = %q, want a hyphenated UUID", got)
	}
	for _, header := range []string{"X-Conversation-Request-ID", "X-Conversation-Message-ID", "X-Request-ID", "X-Root-Request-ID", "X-Trace-ID", "X-B3-TraceId"} {
		if got := request.Header.Get(header); len(got) != 32 {
			t.Fatalf("%s = %q, want a 32-hex id", header, got)
		}
	}
	// The client double-sends both propagation formats, the B3 one in four
	// segments with a parent span, which is what tells it from our own trio.
	if got := request.Header.Get("b3"); strings.Count(got, "-") != 3 {
		t.Fatalf("b3 = %q, want trace-span-flags-parent", got)
	}
	if got := request.Header.Get("X-B3-ParentSpanId"); len(got) != 16 {
		t.Fatalf("X-B3-ParentSpanId = %q, want a 16-hex span", got)
	}
	if got, want := request.Header.Get("baggage"), workBuddyBaggage(request.Header.Get("X-Conversation-ID"), request.Header.Get("X-Conversation-Request-ID")); got != want {
		t.Fatalf("baggage = %q, want %q", got, want)
	}
	if request.Header.Get("X-Device-Token") != "" {
		t.Fatalf("X-Device-Token = %q, want omitted without credential/env/file", request.Header.Get("X-Device-Token"))
	}
	// Token or error, never neither: the client's SDK reports the failure it had.
	if got := request.Header.Get("X-Device-Token-Error"); got != workBuddyDeviceTokenError {
		t.Fatalf("X-Device-Token-Error = %q, want %q", got, workBuddyDeviceTokenError)
	}
}

func TestApplyWorkBuddyHeadersIntlChatFingerprint(t *testing.T) {
	credential := domain.WorkBuddyCredential{AccessToken: "jwt", UserID: "uid-2", DeviceToken: "dev-secret"}
	request, err := http.NewRequest(http.MethodPost, "https://www.workbuddy.ai/v2/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	applyWorkBuddyHeaders(request, domain.Provider{AuthType: domain.AuthTypeWorkBuddyAI, WorkBuddy: &credential})

	site := workBuddyIntlSite
	if request.Header.Get("User-Agent") != site.chatUserAgent() {
		t.Fatalf("User-Agent = %q, want %q", request.Header.Get("User-Agent"), site.chatUserAgent())
	}
	wantUA := "WorkBuddy/" + workBuddyClientVersionIntl + " workbuddy-ai/" + workBuddyClientVersionIntl + " CLI/" + workBuddyCLIVersion
	if request.Header.Get("User-Agent") != wantUA {
		t.Fatalf("User-Agent shape = %q, want %q", request.Header.Get("User-Agent"), wantUA)
	}
	if request.Header.Get("X-Agent-Intent") != "craft" {
		t.Fatalf("X-Agent-Intent = %q, want craft", request.Header.Get("X-Agent-Intent"))
	}
	if got := request.Header.Get("X-Domain"); got != "www.workbuddy.ai" {
		t.Fatalf("X-Domain = %q, want www.workbuddy.ai", got)
	}
	if request.Header.Get("X-Private-Data") != "true" {
		t.Fatalf("X-Private-Data = %q, want true for intl", request.Header.Get("X-Private-Data"))
	}
	if request.Header.Get("X-Device-Token") != "dev-secret" {
		t.Fatalf("X-Device-Token = %q, want credential reuse", request.Header.Get("X-Device-Token"))
	}
	if got := request.Header.Get("X-Conversation-ID"); len(got) != 36 || !strings.Contains(got, "-") {
		t.Fatalf("X-Conversation-ID = %q, want a hyphenated UUID", got)
	}
	for _, header := range []string{"X-Conversation-Request-ID", "X-Conversation-Message-ID", "X-Request-ID"} {
		if got := request.Header.Get(header); len(got) != 32 {
			t.Fatalf("%s = %q, want a 32-hex id", header, got)
		}
	}
}

func TestApplyWorkBuddyHeaders(t *testing.T) {
	TestApplyWorkBuddyHeadersCNChatFingerprint(t)
}

func TestApplyWorkBuddyHeadersAIAddsClientHeaders(t *testing.T) {
	TestApplyWorkBuddyHeadersIntlChatFingerprint(t)
}

func TestAddProviderWorkBuddyDefaultConcurrency(t *testing.T) {
	router := NewRouter(domain.GatewayState{})
	cn, err := router.AddProvider(domain.Provider{Name: "WB CN", AuthType: domain.AuthTypeWorkBuddy})
	if err != nil {
		t.Fatal(err)
	}
	if cn.MaxConcurrentRequests != workBuddyDefaultConcurrencyCN {
		t.Fatalf("CN maxConcurrent = %d, want %d", cn.MaxConcurrentRequests, workBuddyDefaultConcurrencyCN)
	}
	intl, err := router.AddProvider(domain.Provider{Name: "WB AI", AuthType: domain.AuthTypeWorkBuddyAI})
	if err != nil {
		t.Fatal(err)
	}
	if intl.MaxConcurrentRequests != workBuddyDefaultConcurrencyIntl {
		t.Fatalf("intl maxConcurrent = %d, want %d", intl.MaxConcurrentRequests, workBuddyDefaultConcurrencyIntl)
	}
}

func TestNormalizeProviderWorkBuddyPinsChatProtocol(t *testing.T) {
	for _, authType := range []string{domain.AuthTypeWorkBuddy, domain.AuthTypeWorkBuddyAI} {
		provider := domain.Provider{ID: "wb", Name: "WB", AuthType: authType, BaseURL: "https://example.com"}
		normalizeProvider(&provider)
		if provider.Protocol != domain.ProtocolOpenAIChat {
			t.Fatalf("%s: protocol = %s, want openai_chat", authType, provider.Protocol)
		}
		site, ok := workBuddySiteForAuthType(authType)
		if !ok {
			t.Fatalf("%s: no site", authType)
		}
		if provider.BaseURL != site.chatBaseURL() {
			t.Fatalf("%s: baseUrl = %q, want the placeholder replaced by %q", authType, provider.BaseURL, site.chatBaseURL())
		}
	}
	// A hand-set endpoint survives: the builds can move, and that should be a
	// config edit rather than a rebuild.
	provider := domain.Provider{ID: "wb", AuthType: domain.AuthTypeWorkBuddy, BaseURL: "https://mirror.example/v2"}
	normalizeProvider(&provider)
	if provider.BaseURL != "https://mirror.example/v2" {
		t.Fatalf("baseUrl = %q, want the operator's endpoint kept", provider.BaseURL)
	}
}

// workBuddySignInServer stands in for a WorkBuddy build: it hands out a sign-in
// page, refuses the token twice before the user "finishes" signing in, then
// answers with the token and the account.
func workBuddySignInServer(t *testing.T, tokenAttempts int) *httptest.Server {
	t.Helper()
	var attempts int
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v2/plugin/auth/state", func(w http.ResponseWriter, r *http.Request) {
		for _, header := range []string{"X-No-Authorization", "X-No-User-Id", "X-No-Enterprise-Id", "X-No-Department-Info"} {
			if r.Header.Get(header) != "true" {
				t.Errorf("state request missing %s", header)
			}
		}
		if r.URL.Query().Get("platform") != "workbuddy" {
			t.Errorf("platform = %q, want workbuddy", r.URL.Query().Get("platform"))
		}
		fmt.Fprint(w, `{"code":0,"msg":"ok","data":{"state":"state-123","authUrl":"https://www.codebuddy.cn/login"}}`)
	})
	mux.HandleFunc("GET /v2/plugin/auth/token", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != "state-123" {
			t.Errorf("token state = %q", r.URL.Query().Get("state"))
		}
		attempts++
		if attempts <= tokenAttempts {
			fmt.Fprintf(w, `{"code":%d,"msg":"not yet"}`, workBuddyRetryTokenCode)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"accessToken":"at-1","refreshToken":"rt-1","expiresIn":7200}}`)
	})
	mux.HandleFunc("GET /v2/plugin/login/account", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-1" {
			t.Errorf("account request authorization = %q", r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, `{"code":0,"data":{"uid":"uid-9","nickname":"郭庆"}}`)
	})
	mux.HandleFunc("GET /v3/config", func(w http.ResponseWriter, r *http.Request) {
		// The flow syncs the plan's models right after signing in.
		if r.Header.Get("Authorization") != "Bearer at-1" {
			t.Errorf("config request authorization = %q", r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, `{"code":0,"data":{"agents":[{"name":"cli","models":["glm-5.3"]}],"models":[{"id":"glm-5.3","maxInputTokens":1000000}]}}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected workbuddy endpoint %s %s", r.Method, r.URL.Path)
	})
	return httptest.NewServer(mux)
}

func TestWorkBuddySignInStoresAccountSession(t *testing.T) {
	const refusedAttempts = 2
	server := workBuddySignInServer(t, refusedAttempts)
	defer server.Close()
	pointWorkBuddyAt(t, server.URL, server.URL)

	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{
		{ID: "wb", Name: "WorkBuddy", AuthType: domain.AuthTypeWorkBuddy},
	}})
	s := &Server{
		router:                 router,
		logs:                   monitor.NewStore(),
		subscriptionOAuthFlows: newSubscriptionOAuthStore(),
	}

	site, _ := workBuddySiteForAuthType(domain.AuthTypeWorkBuddy)
	authURL, flowID, err := s.startWorkBuddyOAuth("wb", site)
	if err != nil {
		t.Fatalf("start sign-in: %v", err)
	}
	if !strings.HasPrefix(authURL, "https://www.codebuddy.cn/login") {
		t.Fatalf("authUrl = %q, want the page WorkBuddy handed out", authURL)
	}
	if !strings.Contains(authURL, "loginSessionId=") || !strings.Contains(authURL, "version="+workBuddySignInVersion) {
		t.Fatalf("authUrl %q lacks the session/version params", authURL)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		status, ok := s.subscriptionOAuthFlows.status(flowID)
		if ok && status.Status == "connected" {
			break
		}
		if ok && status.Status == "failed" {
			t.Fatalf("sign-in failed: %s", status.Message)
		}
		if time.Now().After(deadline) {
			t.Fatalf("sign-in did not finish; last status %+v", status)
		}
		time.Sleep(50 * time.Millisecond)
	}

	provider, err := s.router.ProviderByID("wb")
	if err != nil {
		t.Fatal(err)
	}
	if provider.WorkBuddy == nil {
		t.Fatal("no workbuddy credential stored")
	}
	if provider.WorkBuddy.AccessToken != "at-1" || provider.WorkBuddy.RefreshToken != "rt-1" {
		t.Fatalf("credential = %+v", *provider.WorkBuddy)
	}
	if provider.WorkBuddy.UserID != "uid-9" || provider.WorkBuddy.AccountLabel != "郭庆" {
		t.Fatalf("account = %q / %q, want uid-9 / 郭庆", provider.WorkBuddy.UserID, provider.WorkBuddy.AccountLabel)
	}
	if provider.BaseURL != server.URL+"/v2" {
		t.Fatalf("baseUrl = %q, want the test server's /v2", provider.BaseURL)
	}
	if provider.Protocol != domain.ProtocolOpenAIChat {
		t.Fatalf("protocol = %s, want openai_chat", provider.Protocol)
	}
	// The console must never see the tokens themselves.
	redacted := redactProviderForClient(provider)
	if redacted.WorkBuddy == nil || redacted.WorkBuddy.AccessToken != "" || redacted.WorkBuddy.RefreshToken != "" {
		t.Fatalf("redacted credential leaked tokens: %+v", redacted.WorkBuddy)
	}
	if !redacted.WorkBuddy.Connected {
		t.Fatal("redacted credential should report connected")
	}
}

func TestEnsureFreshWorkBuddyTokenRefreshesExpiringSession(t *testing.T) {
	var refreshed bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != workBuddyRefreshPath {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("X-Refresh-Token") != "rt-old" {
			t.Errorf("X-Refresh-Token = %q", r.Header.Get("X-Refresh-Token"))
		}
		refreshed = true
		fmt.Fprint(w, `{"code":0,"data":{"accessToken":"at-new","refreshToken":"rt-new","expiresIn":7200}}`)
	}))
	defer server.Close()
	pointWorkBuddyAt(t, server.URL, server.URL)

	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{{
		ID: "wb", Name: "WorkBuddy", AuthType: domain.AuthTypeWorkBuddy,
		WorkBuddy: &domain.WorkBuddyCredential{
			AccessToken:  "at-old",
			RefreshToken: "rt-old",
			UserID:       "uid-9",
			// Already lapsed: the next request must trade it in.
			ExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		},
	}}})
	s := &Server{router: router, logs: monitor.NewStore()}

	provider, err := s.router.ProviderByID("wb")
	if err != nil {
		t.Fatal(err)
	}
	refreshedProvider, err := s.ensureFreshWorkBuddyToken(provider)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if !refreshed {
		t.Fatal("refresh endpoint was not called")
	}
	if refreshedProvider.WorkBuddy.AccessToken != "at-new" || refreshedProvider.WorkBuddy.RefreshToken != "rt-new" {
		t.Fatalf("credential = %+v, want the refreshed tokens", *refreshedProvider.WorkBuddy)
	}
	if refreshedProvider.WorkBuddy.UserID != "uid-9" {
		t.Fatalf("userId = %q, want it preserved across a refresh", refreshedProvider.WorkBuddy.UserID)
	}
}

func TestEnsureFreshWorkBuddyTokenKeepsUsableSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a fresh session must not be refreshed")
	}))
	defer server.Close()
	pointWorkBuddyAt(t, server.URL, server.URL)

	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{{
		ID: "wb", AuthType: domain.AuthTypeWorkBuddy,
		WorkBuddy: &domain.WorkBuddyCredential{
			AccessToken: "at", RefreshToken: "rt",
			ExpiresAt: time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		},
	}}})
	s := &Server{router: router, logs: monitor.NewStore()}
	provider, _ := s.router.ProviderByID("wb")
	got, err := s.ensureFreshWorkBuddyToken(provider)
	if err != nil {
		t.Fatalf("ensure fresh: %v", err)
	}
	if got.WorkBuddy.AccessToken != "at" {
		t.Fatalf("token = %q, want the stored one untouched", got.WorkBuddy.AccessToken)
	}
}

func TestEnsureFreshWorkBuddyTokenRefusesSignedOutProvider(t *testing.T) {
	s := &Server{router: NewRouter(domain.GatewayState{Providers: []domain.Provider{
		{ID: "wb", AuthType: domain.AuthTypeWorkBuddy},
	}})}
	provider, _ := s.router.ProviderByID("wb")
	if _, err := s.ensureFreshWorkBuddyToken(provider); err == nil {
		t.Fatal("expected a 'not signed in' error for a provider with no session")
	}
}

func TestFetchWorkBuddyModelsReadsCLIAgentConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != workBuddyConfigPath {
			t.Errorf("path = %s, want %s", r.URL.Path, workBuddyConfigPath)
		}
		if want := workBuddyCNSite.v3ConfigUserAgent(); r.Header.Get("User-Agent") != want {
			t.Errorf("User-Agent = %q, want %q (a bare one gets CodeBuddy IDE's config)", r.Header.Get("User-Agent"), want)
		}
		fmt.Fprint(w, `{"code":0,"data":{
			"agents":[{"name":"ide","models":["ignored-model"]},{"name":"cli","models":["glm-5.3","auto"]}],
			"models":[
				{"id":"glm-5.3","name":"GLM-5.3","maxInputTokens":1000000,"maxOutputTokens":32000,"credits":"x1.00"},
				{"id":"auto","maxInputTokens":200000,"maxOutputTokens":16000,"credits":"0"}],
			"modelPromotions":[{"kind":"discount","modelIds":["glm-5.3"],"discount":{"discountedCredits":"0.50x"}}]}}`)
	}))
	defer server.Close()
	pointWorkBuddyAt(t, server.URL, server.URL)

	site, _ := workBuddySiteForAuthType(domain.AuthTypeWorkBuddy)
	models, err := fetchWorkBuddyModels(t.Context(), site, domain.WorkBuddyCredential{AccessToken: "at", UserID: "uid"})
	if err != nil {
		t.Fatalf("fetch models: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v, want the cli agent's two", models)
	}
	if models[0].ID != "glm-5.3" || models[0].ContextLength != 1_000_000 || models[0].MaxOutputTokens != 32_000 {
		t.Fatalf("first model = %+v", models[0])
	}
	if models[0].Name != "GLM-5.3" {
		t.Fatalf("first model name = %q, want the upstream display name", models[0].Name)
	}
	if models[0].Rate != 0.5 || models[0].RateWas != 1 || models[0].Free {
		t.Fatalf("first model price = free:%v rate:%v was:%v, want the 0.5 discount off 1", models[0].Free, models[0].Rate, models[0].RateWas)
	}
	if models[1].ID != "auto" || models[1].ContextLength == 0 {
		t.Fatalf("second model = %+v, want auto with a fallback context window", models[1])
	}
	if !models[1].Free || models[1].Rate != 0 {
		t.Fatalf("second model price = free:%v rate:%v, want free", models[1].Free, models[1].Rate)
	}
	for _, model := range models {
		if model.Protocol != domain.ProtocolOpenAIChat {
			t.Fatalf("%s protocol = %s", model.ID, model.Protocol)
		}
	}
}

func TestWorkBuddyModelPrice(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	enabled, disabled := true, false
	cases := []struct {
		name     string
		detail   workBuddyModelDetail
		promos   []workBuddyPromotion
		wantFree bool
		wantRate float64
		wantWas  float64
	}{
		{
			name:     "credits 写法 x0.03",
			detail:   workBuddyModelDetail{ID: "m", Credits: "x0.03"},
			wantRate: 0.03,
		},
		{
			name:     "credits 带单位",
			detail:   workBuddyModelDetail{ID: "m", Credits: "0.5 credits"},
			wantRate: 0.5,
		},
		{
			name:     "免费模型",
			detail:   workBuddyModelDetail{ID: "m", Credits: "x0.00"},
			wantFree: true,
		},
		{
			name:   "没有价格就不猜",
			detail: workBuddyModelDetail{ID: "m", Credits: ""},
		},
		{
			name:   "整天折扣给出折后价与原价",
			detail: workBuddyModelDetail{ID: "m", Credits: 2.0},
			promos: []workBuddyPromotion{{Kind: "discount", ModelIDs: []string{"m"}, Enabled: &enabled, Discount: &struct {
				DiscountedCredits any "json:\"discountedCredits\""
				Factor            any "json:\"factor\""
			}{Factor: 0.5}}},
			wantRate: 1,
			wantWas:  2,
		},
		{
			name:   "限时免费按 free 报",
			detail: workBuddyModelDetail{ID: "m", Credits: "x1.00"},
			promos: []workBuddyPromotion{{Kind: "discount", ModelIDs: []string{"m"}, Discount: &struct {
				DiscountedCredits any "json:\"discountedCredits\""
				Factor            any "json:\"factor\""
			}{DiscountedCredits: "0.00x"}}},
			wantFree: true,
			wantWas:  1,
		},
		{
			name:   "夜间免费白天仍报全价",
			detail: workBuddyModelDetail{ID: "m", Credits: "x1.00"},
			promos: []workBuddyPromotion{func() workBuddyPromotion {
				p := workBuddyPromotion{Kind: "discount", ModelIDs: []string{"m"}, Discount: &struct {
					DiscountedCredits any "json:\"discountedCredits\""
					Factor            any "json:\"factor\""
				}{DiscountedCredits: "0.00x"}}
				p.Schedule.Daily = []json.RawMessage{json.RawMessage(`{"start":"23:00","end":"08:00"}`)}
				return p
			}()},
			wantRate: 1,
		},
		{
			name:   "停用与过期的促销不算",
			detail: workBuddyModelDetail{ID: "m", Credits: "x1.00"},
			promos: []workBuddyPromotion{
				{Kind: "discount", ModelIDs: []string{"m"}, Enabled: &disabled, Discount: &struct {
					DiscountedCredits any "json:\"discountedCredits\""
					Factor            any "json:\"factor\""
				}{DiscountedCredits: "0.00x"}},
				func() workBuddyPromotion {
					p := workBuddyPromotion{Kind: "discount", ModelIDs: []string{"m"}, Enabled: &enabled, Discount: &struct {
						DiscountedCredits any "json:\"discountedCredits\""
						Factor            any "json:\"factor\""
					}{DiscountedCredits: "0.00x"}}
					p.Schedule.ValidUntil = "2026-10-20T00:00:00Z"
					return p
				}(),
			},
			wantRate: 1,
		},
		{
			name:   "别的模型的促销不串号",
			detail: workBuddyModelDetail{ID: "m", Credits: "x0.8"},
			promos: []workBuddyPromotion{{Kind: "discount", ModelIDs: []string{"other"}, Discount: &struct {
				DiscountedCredits any "json:\"discountedCredits\""
				Factor            any "json:\"factor\""
			}{DiscountedCredits: "0.00x"}}},
			wantRate: 0.8,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			free, rate, was := workBuddyModelPrice(tc.detail, tc.promos, now)
			if free != tc.wantFree || rate != tc.wantRate || was != tc.wantWas {
				t.Fatalf("price = free:%v rate:%v was:%v, want free:%v rate:%v was:%v", free, rate, was, tc.wantFree, tc.wantRate, tc.wantWas)
			}
		})
	}
}

func TestRewriteWorkBuddyUpstreamStreamedBody(t *testing.T) {
	newResponse := func(status int, contentType, body string) *http.Response {
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{contentType}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}
	}

	t.Run("SSE 流重建成单个 chat.completion", func(t *testing.T) {
		response := newResponse(http.StatusOK, "text/event-stream",
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"}}]}\n\n"+
				"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		if err := rewriteWorkBuddyUpstreamStreamedBody(response, "glm-5.3"); err != nil {
			t.Fatalf("rewrite: %v", err)
		}
		payload, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		var completion map[string]any
		if err := json.Unmarshal(payload, &completion); err != nil {
			t.Fatalf("rebuilt body is not JSON: %v (%s)", err, payload)
		}
		if completion["object"] != "chat.completion" {
			t.Fatalf("object = %v, want chat.completion", completion["object"])
		}
		if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Fatalf("content type = %q, want application/json", got)
		}
		if response.ContentLength != int64(len(payload)) {
			t.Fatalf("content length = %d, body is %d", response.ContentLength, len(payload))
		}
	})

	t.Run("上游错误原样保留，不编造完成对象", func(t *testing.T) {
		response := newResponse(http.StatusForbidden, "application/json", `{"code":11140,"displayMsg":"账号存在异常行为"}`)
		if err := rewriteWorkBuddyUpstreamStreamedBody(response, "glm-5.3"); err != nil {
			t.Fatalf("rewrite: %v", err)
		}
		payload, _ := io.ReadAll(response.Body)
		if !strings.Contains(string(payload), "11140") {
			t.Fatalf("error body = %s, want WorkBuddy's answer kept verbatim", payload)
		}
	})

	t.Run("上游已经给了 JSON 就不动", func(t *testing.T) {
		response := newResponse(http.StatusOK, "application/json", `{"object":"chat.completion"}`)
		if err := rewriteWorkBuddyUpstreamStreamedBody(response, "glm-5.3"); err != nil {
			t.Fatalf("rewrite: %v", err)
		}
		payload, _ := io.ReadAll(response.Body)
		if string(payload) != `{"object":"chat.completion"}` {
			t.Fatalf("body = %s, want it untouched", payload)
		}
	})
}

func TestWorkBuddyQuotaReadsCreditPackages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/billing/meter/get-user-resource-summary":
			fmt.Fprint(w, `{"code":0,"data":{"IsPaidUser":true,"Packages":[
				{"PackageCode":"TCACA_code_002_AkiJS3ZHF5","CycleTotalCapacity":"3300","CycleUsedCapacity":"438.88"}]}}`)
		case "/billing/meter/get-user-resource-paid-packages", "/billing/meter/get-user-resource-free-packages":
			// A personal account's credits come in dated batches, so the quota
			// read opens that door too; here the account has none listed.
			fmt.Fprint(w, `{"code":0,"data":{"Accounts":[]}}`)
		default:
			t.Errorf("unexpected quota endpoint %s", r.URL.Path)
		}
	}))
	defer server.Close()
	pointWorkBuddyAt(t, server.URL, server.URL)

	site, _ := workBuddySiteForAuthType(domain.AuthTypeWorkBuddy)
	quota, _ := workBuddyQuotaFor(t.Context(), site, domain.WorkBuddyCredential{AccessToken: "at", UserID: "uid", AccountLabel: "郭庆"}, nil)
	if quota.Error != "" {
		t.Fatalf("quota error: %s", quota.Error)
	}
	if quota.Plan != "Pro" {
		t.Fatalf("plan = %q, want Pro", quota.Plan)
	}
	if len(quota.Windows) != 1 {
		t.Fatalf("windows = %+v", quota.Windows)
	}
	window := quota.Windows[0]
	if window.Limit != 3300 || window.Amount != 438.88 {
		t.Fatalf("window = %+v, want the string-encoded capacities parsed", window)
	}
	if window.Used < 13 || window.Used > 14 {
		t.Fatalf("used = %.2f%%, want ~13.3%%", window.Used)
	}
}

// The account label is the only thing that tells two WorkBuddy sign-ins apart in
// every picker, so it must stay unique — but it must not put a whole phone
// number on screen either. A sign-in with no nickname falls back to the phone,
// kept as 1815****4947; a nickname is left exactly as the account spells it.
func TestWorkBuddyAccountLabelMasksThePhoneItFallsBackTo(t *testing.T) {
	cases := []struct {
		nickname, phone, uid string
		want                 string
	}{
		{"郭庆", "181512344947", "uid-9", "郭庆"},
		{"", "181512344947", "uid-9", "1815****4947"},
		{"   ", "13900001111", "uid-9", "1390****1111"},
		{"", "", "uid-9", "uid-9"},
		{"", "12345", "uid-9", "12345"},
		{"", "", "", "WorkBuddy"},
	}
	for _, item := range cases {
		if got := workBuddyAccountLabel(item.nickname, item.phone, item.uid); got != item.want {
			t.Errorf("workBuddyAccountLabel(%q, %q, %q) = %q, want %q",
				item.nickname, item.phone, item.uid, got, item.want)
		}
	}
}

// WorkBuddy can sign in without an expiry — 工作台/WorkBuddy AI 的两种 site 里
// 国际站就不给 expiresAt。把它当作永不过期，就只能等厂商先拒一次 401 才换令牌；
// 改为按取得令牌的时间推算：过了假定寿命就主动续。
func TestWorkBuddyTokenRefreshWithoutExpiry(t *testing.T) {
	old := &domain.WorkBuddyCredential{AccessToken: "at", SignInAt: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)}
	if !workBuddyTokenNeedsRefresh(old) {
		t.Fatal("a credential with no expiry but an old sign-in must be refreshed")
	}
	fresh := &domain.WorkBuddyCredential{AccessToken: "at", SignInAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)}
	if workBuddyTokenNeedsRefresh(fresh) {
		t.Fatal("a credential signed in a minute ago must not be refreshed")
	}
	unknown := &domain.WorkBuddyCredential{AccessToken: "at"}
	if workBuddyTokenNeedsRefresh(unknown) {
		t.Fatal("with neither an expiry nor a sign-in time there is nothing to act on")
	}
	minted := workBuddyCredentialPayload{AccessToken: "new"}.toCredential(domain.WorkBuddyCredential{})
	if strings.TrimSpace(minted.SignInAt) == "" {
		t.Fatal("a freshly minted credential must record when it was obtained")
	}
}
