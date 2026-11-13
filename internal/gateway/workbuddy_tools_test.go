package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// TestShapeWorkBuddyChatBodyFitsTools: a tool WorkBuddy refuses by name or by
// the shape of its schema is mended, the history's calls are named as the
// tool now is, and the names it gave come back for the answer.
func TestShapeWorkBuddyChatBodyFitsTools(t *testing.T) {
	body := `{"model":"glm-5.3","stream":true,` +
		`"tool_choice":{"type":"function","function":{"name":"mcp.echo:v1"}},` +
		`"tools":[{"type":"function","function":{"name":"mcp.echo:v1","parameters":{` +
		`"type":"object","properties":{` +
		`"mode":{"enum":["fast","slow"]},` +
		`"tuple":{"type":"array","items":[{"type":"string"},{"type":"integer"}]},` +
		`"flag":true,` +
		`"code":{"type":"string","pattern":"\\0abc"}},` +
		`"required":"nope"}}}],` +
		`"messages":[{"role":"system","content":"s"},` +
		`{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"mcp.echo:v1","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","name":"mcp.echo:v1","content":"ok"},` +
		`{"role":"user","content":"继续"}]}`
	prepared, aliases, changed := shapeWorkBuddyChatBody([]byte(body))
	if !changed {
		t.Fatal("the body was left alone")
	}
	if aliases["mcp_echo_v1"] != "mcp.echo:v1" {
		t.Fatalf("aliases = %+v, want the safe name to name back", aliases)
	}
	var payload map[string]any
	if err := json.Unmarshal(prepared, &payload); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if payload["tool_choice"] != "mcp_echo_v1" {
		t.Fatalf("tool_choice = %+v, want the safe name", payload["tool_choice"])
	}
	tools, _ := payload["tools"].([]any)
	firstTool, _ := tools[0].(map[string]any)
	function, _ := firstTool["function"].(map[string]any)
	if function["name"] != "mcp_echo_v1" {
		t.Fatalf("tool name = %v, want mcp_echo_v1", function["name"])
	}
	parameters, _ := function["parameters"].(map[string]any)
	properties, _ := parameters["properties"].(map[string]any)
	mode, _ := properties["mode"].(map[string]any)
	if mode["type"] != "string" {
		t.Fatalf("enum type = %v, want string", mode["type"])
	}
	tuple, _ := properties["tuple"].(map[string]any)
	items, _ := tuple["items"].(map[string]any)
	if list, _ := items["anyOf"].([]any); len(list) != 2 {
		t.Fatalf("tuple items = %+v, want one anyOf schema", tuple["items"])
	}
	if flag, _ := properties["flag"].(map[string]any); flag == nil || len(flag) != 0 {
		t.Fatalf("boolean schema = %+v, want {}", properties["flag"])
	}
	code, _ := properties["code"].(map[string]any)
	if pattern, _ := code["pattern"].(string); !strings.HasPrefix(pattern, `\u0000`) {
		t.Fatalf("pattern = %q, want \\0 rewritten", pattern)
	}
	if _, exists := parameters["required"]; exists {
		t.Fatalf("required = %+v, want the non-list dropped", parameters["required"])
	}
	messages, _ := payload["messages"].([]any)
	assistant, _ := messages[1].(map[string]any)
	calls, _ := assistant["tool_calls"].([]any)
	call, _ := calls[0].(map[string]any)
	callFunction, _ := call["function"].(map[string]any)
	if callFunction["name"] != "mcp_echo_v1" {
		t.Fatalf("history call = %v, want the safe name", callFunction["name"])
	}
	tool, _ := messages[2].(map[string]any)
	if tool["name"] != "mcp_echo_v1" {
		t.Fatalf("tool answer name = %v, want the safe name", tool["name"])
	}
}

// TestShapeWorkBuddyChatBodyLiftsMessageTools: a tool a message carries joins
// the request's own tools once by name, and the message that carried only
// tools does not go on as an empty system turn.
func TestShapeWorkBuddyChatBodyLiftsMessageTools(t *testing.T) {
	body := `{"model":"glm-5.3","stream":true,` +
		`"tools":[{"type":"function","function":{"name":"Bash","parameters":{"type":"object","properties":{}}}}],` +
		`"messages":[` +
		`{"role":"system","content":"s"},` +
		`{"role":"system","content":"","tools":[` +
		`{"type":"function","function":{"name":"Bash","parameters":{"type":"object","properties":{}}}},` +
		`{"type":"function","function":{"name":"Read","parameters":{"type":"object","properties":{}}}}]},` +
		`{"role":"user","content":"hi"}]}`
	prepared, _, changed := shapeWorkBuddyChatBody([]byte(body))
	if !changed {
		t.Fatal("the body was left alone")
	}
	var payload map[string]any
	if err := json.Unmarshal(prepared, &payload); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	tools, _ := payload["tools"].([]any)
	names := []string{}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		function, _ := tool["function"].(map[string]any)
		names = append(names, stringValue(function["name"]))
	}
	if len(names) != 2 || names[0] != "Bash" || names[1] != "Read" {
		t.Fatalf("tools = %v, want Bash once and Read", names)
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want the tools-only system turn dropped", len(messages))
	}
	first, _ := messages[0].(map[string]any)
	if _, exists := first["tools"]; exists {
		t.Fatal("tools stayed in a message")
	}
}

func TestReplyAfterWorkBuddyTools(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "工具结果之后是用户的话：插一条空回复",
			body: `{"model":"m","messages":[{"role":"system","content":"s"},` +
				`{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"Bash","arguments":"{}"}}]},` +
				`{"role":"tool","tool_call_id":"c1","content":"ok"},` +
				`{"role":"user","content":"继续"}]}`,
			want: true,
		},
		{
			name: "本来就以用户的话结尾：不动",
			body: `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			want: false,
		},
		{
			name: "以工具结果结尾、后面没有用户的话：这是另一回事",
			body: `{"model":"m","messages":[{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"Bash","arguments":"{}"}}]},` +
				`{"role":"tool","tool_call_id":"c1","content":"ok"}]}`,
			want: false,
		},
		{
			name: "以 assistant 结尾：prefill，照样拒",
			body: `{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"半句"}]}`,
			want: false,
		},
		{
			name: "不是聊天：不动",
			body: `{"model":"m"}`,
			want: false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			quiet := replyAfterWorkBuddyTools([]byte(testCase.body))
			if (quiet != nil) != testCase.want {
				t.Fatalf("reply = %s, want changed = %v", quiet, testCase.want)
			}
			if quiet == nil {
				return
			}
			var payload map[string]any
			if err := json.Unmarshal(quiet, &payload); err != nil {
				t.Fatalf("result is not valid JSON: %v", err)
			}
			messages, _ := payload["messages"].([]any)
			last, _ := messages[len(messages)-1].(map[string]any)
			if last["role"] != "user" {
				t.Fatalf("last = %+v, want the user's message", last)
			}
			before, _ := messages[len(messages)-2].(map[string]any)
			if before["role"] != "assistant" || before["content"] != "" {
				t.Fatalf("before last = %+v, want an empty assistant reply", before)
			}
			earlier, _ := messages[len(messages)-3].(map[string]any)
			if earlier["role"] != "tool" {
				t.Fatalf("before the reply = %+v, want the tool result", earlier)
			}
		})
	}
}

func TestWorkBuddyAfterToolsRefused(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{`{"code":11153,"msg":"conversation must end with a user message"}`, true},
		{`{"error":{"message":"The conversation must end with a user message."}}`, true},
		{`{"code":"11153"}`, true},
		{`{"code":11148,"msg":"tool calls and tool results do not match"}`, false},
		{`{"code":111530}`, false},
		{`{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`, false},
	}
	for _, testCase := range cases {
		if got := workBuddyAfterToolsRefused([]byte(testCase.body)); got != testCase.want {
			t.Fatalf("%s: refused = %v, want %v", testCase.body, got, testCase.want)
		}
	}
}

func TestFitWorkBuddyEnumGivesItAType(t *testing.T) {
	cases := []struct {
		name   string
		schema map[string]any
		check  func(t *testing.T, schema map[string]any)
	}{
		{
			name:   "没有 type：按值的类型补上",
			schema: map[string]any{"enum": []any{"a", "b"}},
			check: func(t *testing.T, schema map[string]any) {
				if schema["type"] != "string" {
					t.Fatalf("type = %v, want string", schema["type"])
				}
			},
		},
		{
			name:   "有 null：type 变成两个",
			schema: map[string]any{"enum": []any{"a", nil}},
			check: func(t *testing.T, schema map[string]any) {
				list, _ := schema["type"].([]any)
				if len(list) != 2 || list[0] != "string" || list[1] != "null" {
					t.Fatalf("type = %+v, want [string null]", schema["type"])
				}
			},
		},
		{
			name:   "值有多种类型：拆成 anyOf",
			schema: map[string]any{"enum": []any{1, "a"}},
			check: func(t *testing.T, schema map[string]any) {
				if _, exists := schema["enum"]; exists {
					t.Fatal("enum survived the split")
				}
				list, _ := schema["anyOf"].([]any)
				if len(list) != 2 {
					t.Fatalf("anyOf = %+v, want one enum per type", schema["anyOf"])
				}
			},
		},
		{
			name:   "空 enum：丢掉",
			schema: map[string]any{"enum": []any{}},
			check: func(t *testing.T, schema map[string]any) {
				if _, exists := schema["enum"]; exists {
					t.Fatal("an empty enum survived")
				}
			},
		},
		{
			name:   "已经是 T 或 [T,null]：不动",
			schema: map[string]any{"type": "string", "enum": []any{"a"}},
			check: func(t *testing.T, schema map[string]any) {
				if schema["type"] != "string" {
					t.Fatalf("type = %v", schema["type"])
				}
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fitWorkBuddyEnum(testCase.schema)
			testCase.check(t, testCase.schema)
		})
	}
}

func TestFitWorkBuddyShapeMendsTypes(t *testing.T) {
	schema := map[string]any{
		"type":     "String",
		"required": []any{"a", 3},
		"properties": map[string]any{
			"a": map[string]any{"type": []any{"string", "string", "wat"}},
			"b": map[string]any{"not": true, "additionalProperties": false},
		},
	}
	if !fitWorkBuddyShape(schema, 0) {
		t.Fatal("nothing was mended")
	}
	if schema["type"] != "string" {
		t.Fatalf("type = %v, want string", schema["type"])
	}
	required, _ := schema["required"].([]any)
	if len(required) != 1 || required[0] != "a" {
		t.Fatalf("required = %+v, want the string kept", required)
	}
	properties, _ := schema["properties"].(map[string]any)
	a, _ := properties["a"].(map[string]any)
	if a["type"] != "string" {
		t.Fatalf("a.type = %v, want one string", a["type"])
	}
	b, _ := properties["b"].(map[string]any)
	if not, _ := b["not"].(map[string]any); not == nil || len(not) != 0 {
		t.Fatalf("not = %+v, want {}", b["not"])
	}
	if b["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %v, want false left", b["additionalProperties"])
	}
}

func TestFitWorkBuddyObjectRootFlattensUnions(t *testing.T) {
	cases := []struct {
		name   string
		schema map[string]any
		check  func(t *testing.T, schema map[string]any)
	}{
		{
			name: "anyOf 的对象分支：properties 合并，公共 required 留下",
			schema: map[string]any{"anyOf": []any{
				map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}, "required": []any{"a"}},
				map[string]any{"type": "object", "properties": map[string]any{"b": map[string]any{"type": "integer"}}, "required": []any{"b"}},
			}},
			check: func(t *testing.T, schema map[string]any) {
				if schema["type"] != "object" {
					t.Fatalf("type = %v, want object", schema["type"])
				}
				properties, _ := schema["properties"].(map[string]any)
				if _, ok := properties["a"]; !ok {
					t.Fatal("a did not come over")
				}
				if _, ok := properties["b"]; !ok {
					t.Fatal("b did not come over")
				}
				if _, exists := schema["required"]; exists {
					t.Fatalf("required = %+v, want none (each branch required its own)", schema["required"])
				}
			},
		},
		{
			name: "allOf 的分支：properties 与 required 合并",
			schema: map[string]any{"allOf": []any{
				map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}, "required": []any{"a"}},
				map[string]any{"type": "object", "properties": map[string]any{"b": map[string]any{"type": "integer"}}},
			}},
			check: func(t *testing.T, schema map[string]any) {
				required, _ := schema["required"].([]any)
				if len(required) != 1 || required[0] != "a" {
					t.Fatalf("required = %+v, want a", required)
				}
			},
		},
		{
			name:   "没有 type 的根：当成对象",
			schema: map[string]any{"properties": map[string]any{}},
			check: func(t *testing.T, schema map[string]any) {
				if schema["type"] != "object" {
					t.Fatalf("type = %v, want object", schema["type"])
				}
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if !fitWorkBuddyObjectRoot(testCase.schema, testCase.schema, 0) {
				t.Fatal("nothing was mended")
			}
			testCase.check(t, testCase.schema)
		})
	}
}

func TestRestoreWorkBuddyToolNames(t *testing.T) {
	aliases := map[string]string{"mcp_echo_v1": "mcp.echo:v1"}
	cases := []struct {
		name        string
		contentType string
		body        string
		check       func(t *testing.T, got string)
	}{
		{
			name:        "SSE：模型调用里的名字换回客户端的",
			contentType: "text/event-stream",
			body: "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"mcp_echo_v1\",\"arguments\":\"\"}}]}}]}\n" +
				"\n" +
				"data: [DONE]\n\n",
			check: func(t *testing.T, got string) {
				if !strings.Contains(got, `"name":"mcp.echo:v1"`) {
					t.Fatalf("stream = %s, want the client's name", got)
				}
				if strings.Contains(got, "mcp_echo_v1") {
					t.Fatalf("stream kept the safe name: %s", got)
				}
				if !strings.Contains(got, "data: [DONE]") {
					t.Fatalf("stream lost its terminator: %s", got)
				}
			},
		},
		{
			name:        "JSON：一处对象",
			contentType: "application/json",
			body:        `{"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"mcp_echo_v1","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			check: func(t *testing.T, got string) {
				if !strings.Contains(got, `"name":"mcp.echo:v1"`) {
					t.Fatalf("body = %s, want the client's name", got)
				}
			},
		},
		{
			name:        "没有要换的名字：原样不动",
			contentType: "text/event-stream",
			body:        "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n",
			check: func(t *testing.T, got string) {
				if got != "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" {
					t.Fatalf("body = %q, want it untouched", got)
				}
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{testCase.contentType}},
				Body:       io.NopCloser(strings.NewReader(testCase.body)),
			}
			restoreWorkBuddyToolNames(response, aliases)
			got, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			testCase.check(t, string(got))
		})
	}
}

// TestClaudeToolsAreFittedAndNamedBack: a Claude client's tool name WorkBuddy
// refuses goes upstream in the safe name, and the call the model makes comes
// back to the client under the name it gave.
func TestClaudeToolsAreFittedAndNamedBack(t *testing.T) {
	var gotName string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(raw, &payload)
		if tools, _ := payload["tools"].([]any); len(tools) > 0 {
			tool, _ := tools[0].(map[string]any)
			function, _ := tool["function"].(map[string]any)
			gotName, _ = function["name"].(string)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"id":"c1","created":1700000000,"model":"glm-5.3","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"mcp_echo_v1","arguments":"{\"text\":\"hi\"}"}}]}}]}`,
			`data: {"id":"c1","created":1700000000,"model":"glm-5.3","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`,
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
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{provider},
		Routes: []domain.Route{{
			ID: "r1", Name: "R1", ProviderID: provider.ID,
			OutputProtocol: domain.ProtocolClaude, Enabled: true,
		}},
		APIKeys: []domain.APIKey{{ID: "k1", Name: "K1", Key: "sk-gw", RouteID: "r1", Enabled: true, StreamEnabled: true}},
	})
	body := `{"model":"glm-5.3","max_tokens":64,"stream":false,` +
		`"tools":[{"name":"mcp.echo:v1","description":"echo","input_schema":{"type":"object","properties":{"text":{"type":"string"}}}}],` +
		`"messages":[{"role":"user","content":"调用一下"}]}`
	request := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(body))
	request.Header.Set("x-api-key", "sk-gw")
	request.Header.Set("anthropic-version", "2023-06-01")
	recorder := httptest.NewRecorder()
	NewServer(router, monitor.NewStore()).handleClaudeMessages(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if gotName != "mcp_echo_v1" {
		t.Fatalf("upstream tool name = %q, want mcp_echo_v1", gotName)
	}
	if !strings.Contains(recorder.Body.String(), `"name":"mcp.echo:v1"`) {
		t.Fatalf("client got %s, want its own tool name", recorder.Body.String())
	}
}

// TestSendWorkBuddyChatAnswersAfterTools: the first send is refused with
// 11153, the same chat with an empty assistant reply is sent again, and the
// model is remembered so its next chat carries the reply up front.
func TestSendWorkBuddyChatAnswersAfterTools(t *testing.T) {
	var bodies []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(raw, &payload)
		bodies = append(bodies, payload)
		if len(bodies) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"code":11153,"msg":"The conversation must end with a user message."}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()

	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	send := func(_ domain.Provider, body []byte) (*http.Response, error) {
		request, err := http.NewRequest(http.MethodPost, upstream.URL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		return http.DefaultClient.Do(request)
	}
	body := []byte(`{"model":"gemini-3.8-flash","stream":true,"messages":[` +
		`{"role":"system","content":"s"},` +
		`{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"Bash","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"ok"},` +
		`{"role":"user","content":"继续"}]}`)
	response, err := server.sendWorkBuddyChat(domain.Provider{ID: "wb1"}, "gemini-3.8-flash", body, nil, send, false)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if len(bodies) != 2 {
		t.Fatalf("sent %d times, want the refusal retried once", len(bodies))
	}
	retried, _ := bodies[1]["messages"].([]any)
	if len(retried) != 5 {
		t.Fatalf("retried messages = %d, want the empty reply added", len(retried))
	}
	reply, _ := retried[3].(map[string]any)
	if reply["role"] != "assistant" || reply["content"] != "" {
		t.Fatalf("reply = %+v, want an empty assistant message", reply)
	}
	if user, _ := retried[4].(map[string]any); user["role"] != "user" {
		t.Fatalf("last = %+v, want the user's message", user)
	}

	// The next chat for the same model goes with the reply up front.
	bodies = bodies[:0]
	response, err = server.sendWorkBuddyChat(domain.Provider{ID: "wb1"}, "gemini-3.8-flash", body, nil, send, false)
	if err != nil {
		t.Fatalf("second send: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if len(bodies) != 1 {
		t.Fatalf("sent %d times, want the remembered shape once", len(bodies))
	}
	remembered, _ := bodies[0]["messages"].([]any)
	if len(remembered) != 5 {
		t.Fatalf("remembered messages = %d, want the empty reply up front", len(remembered))
	}
}
