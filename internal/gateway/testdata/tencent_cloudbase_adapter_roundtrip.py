"""只用回环 HTTP，覆盖内置 CloudBase adapter 的 Codex 工具续轮。"""
import importlib.util
import json
import re
import sys
import threading
from collections import deque
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError
from urllib.request import Request, urlopen

spec = importlib.util.spec_from_file_location("adapter", sys.argv[1])
adapter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(adapter)

REQUESTS = deque()
REPLIES = deque()
USAGE = {"prompt_tokens": 500, "completion_tokens": 7, "prompt_cache_hit_tokens": 400, "cache_creation_input_tokens": 20}
RAW_INPUT = "  *** Begin Patch\n+雪\n  "
NAMESPACE = "例子/" + "some_namespace_" * 5


class AiProxy(BaseHTTPRequestHandler):
    """记录 adapter 发出的 Chat 请求并返回预置的云函数结果。"""

    def do_POST(self):
        """只消费一条固定响应，不调用任何模型。"""
        REQUESTS.append(json.loads(self.rfile.read(int(self.headers["Content-Length"]))))
        reply = REPLIES.popleft() if REPLIES else {"error": {"message": "unexpected upstream request"}}
        self.send_response(200)
        self.end_headers()
        self.wfile.write(json.dumps(reply).encode())

    def log_message(self, *_):
        """抑制测试服务器默认日志。"""
        pass


@contextmanager
def serve(handler):
    """让每个测试服务用操作系统分配端口；离开作用域时关闭。"""
    with ThreadingHTTPServer(("127.0.0.1", 0), handler) as server:
        worker = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
        worker.start()
        try:
            yield f"http://127.0.0.1:{server.server_port}"
        finally:
            server.shutdown()
            worker.join(timeout=5)


def post(base, path="responses", **payload):
    """发送真实的本地 HTTP 请求，解析 JSON 或已解码的 SSE。"""
    req = Request(f"{base}/v1/{path}", json.dumps({"model": "hy3", **payload}).encode(), {"Content-Type": "application/json"})
    try:
        response = urlopen(req, timeout=5)
    except HTTPError as exc:
        with exc:
            return exc.code, json.load(exc)
    with response:
        if response.headers.get_content_type() != "text/event-stream":
            return response.status, json.load(response)
        frames = []
        for frame in response.read().decode().strip().split("\n\n"):
            event, data = frame.splitlines()
            body = json.loads(data.removeprefix("data: "))
            assert body["type"] == event.removeprefix("event: "), body
            frames.append(body)
        return response.status, frames


def reply(text=None, calls=None, reason="stop"):
    """构造带可控结束原因和缓存数据的 Chat completion。"""
    message = {"role": "assistant", "content": text}
    if calls is not None:
        message["tool_calls"] = calls
    REPLIES.append({"choices": [{"message": message, "finish_reason": reason}], "usage": USAGE})


def assert_responses_stream(events, terminal):
    """断言 SSE 索引、终态和最终 output 引用同一批 item。"""
    types = [event["type"] for event in events]
    assert types[:2] == ["response.created", "response.in_progress"], types
    assert types[-1] == terminal and sum(kind in ("response.completed", "response.incomplete") for kind in types) == 1, types
    assert [event["sequence_number"] for event in events] == list(range(len(events)))
    output = events[-1]["response"]["output"]
    added = [event for event in events if event["type"] == "response.output_item.added"]
    done = [event for event in events if event["type"] == "response.output_item.done"]
    assert [event["item"] for event in done] == output
    for index, (begin, end) in enumerate(zip(added, done)):
        assert begin["sequence_number"] < end["sequence_number"]
        assert begin["output_index"] == end["output_index"] == index
        assert begin["item"]["id"] == end["item"]["id"]
        assert begin["item"].get("phase") == end["item"].get("phase")
    assert len(added) == len(done) == len(output)
    return events[-1]["response"]


def tool_round_trip(base):
    """用完整历史和 previous_response_id 两种方式回传多个工具结果。"""
    user = {"role": "user", "content": "检查后继续"}
    tools = [
        {"type": "namespace", "name": NAMESPACE, "tools": [{"type": "function", "name": "lookup", "parameters": {"type": "object", "properties": {"id": {"type": "integer"}}}}]},
        {"type": "namespace", "name": "x", "tools": [{"type": "function", "name": "lookup"}]},
        {"type": "function", "name": "x__lookup", "description": "top-level collision"},
        {"type": "custom", "name": "patch"},
        {"type": "tool_search", "execution": "client"},
    ]
    declared_later = {"type": "additional_tools", "role": "developer", "tools": [{"type": "function", "name": "added_tool"}]}
    loaded = {"type": "tool_search_output", "tools": [{"type": "function", "name": "found_tool"}]}
    # 先从实际提供给模型的 schema 发现别名，随后模拟模型生成这批工具调用。
    reply("ready")
    assert post(base, input=[user, declared_later, loaded], tools=tools)[0] == 200
    sent = REQUESTS.popleft()
    assert sent["messages"] == [user], sent
    declarations = [tool["function"] for tool in sent["tools"]]
    names = [tool["name"] for tool in declarations]
    assert len(names) == len(set(names)) == 7, names
    assert all(re.fullmatch(r"[A-Za-z0-9_-]{1,64}", name) for name in names), names
    assert names[2:] == ["x__lookup", "patch", "tool_search", "added_tool", "found_tool"], names
    assert "id" in declarations[0]["parameters"]["properties"]
    calls = [
        {"type": "function", "function": {"name": names[0], "arguments": '{"id":1}'}},
        {"id": "call_native", "type": "function", "function": {"name": "patch", "arguments": json.dumps({"input": RAW_INPUT})}},
    ]
    reply("先并行检查", calls, reason="stop")  # 上游误标 stop 时仍须发送两条工具调用。
    status, events = post(base, input=[user, declared_later, loaded], tools=tools, stream=True)
    assert status == 200, events
    assert REQUESTS.popleft() == sent
    response = assert_responses_stream(events, "response.completed")
    preamble, lookup, patch = response["output"]
    assert preamble["phase"] == "commentary", preamble
    assert (lookup["type"], lookup["name"], lookup["namespace"]) == ("function_call", "lookup", NAMESPACE), lookup
    assert lookup["call_id"].startswith("call_") and lookup["id"] != lookup["call_id"], lookup
    assert (patch["type"], patch["call_id"], patch["input"]) == ("custom_tool_call", "call_native", RAW_INPUT), patch
    assert "".join(event["delta"] for event in events if event["type"] == "response.custom_tool_call_input.delta") == RAW_INPUT
    assert response["usage"]["input_tokens_details"]["cached_tokens"] == 400
    results = [
        {"type": "function_call_output", "call_id": lookup["call_id"], "output": "lookup result"},
        {"type": "custom_tool_call_output", "call_id": patch["call_id"], "output": "patch result"},
    ]
    for history in ({"input": [user, declared_later, loaded, preamble, {"type": "reasoning"}, lookup, patch, *results]},
                    {"input": results, "previous_response_id": response["id"]}):
        reply("已完成")
        status, events = post(base, tools=tools, stream=True, **history)
        assert status == 200, events
        final = assert_responses_stream(events, "response.completed")["output"]
        assert len(final) == 1 and final[0]["phase"] == "final_answer" and final[0]["content"][0]["text"] == "已完成", final
        original, assistant, *outputs = REQUESTS.popleft()["messages"]
        assert original == user and assistant["content"] == "先并行检查"
        assert [call["id"] for call in assistant["tool_calls"]] == [lookup["call_id"], patch["call_id"]]
        assert assistant["tool_calls"][0]["function"]["name"] == names[0]
        assert json.loads(assistant["tool_calls"][1]["function"]["arguments"])["input"] == RAW_INPUT
        assert [message["tool_call_id"] for message in outputs] == [result["call_id"] for result in results]


def search_and_errors(base):
    """验证工具搜索配对、截断和坏包不被伪装成最终答复。"""
    search = {"type": "tool_search", "execution": "client"}
    reply(calls=[{"id": "search_1", "function": {"name": "tool_search", "arguments": {"query": "tools"}}}], reason="tool_calls")
    status, events = post(base, input="search", tools=[search], stream=True)
    assert status == 200, events
    item, = assert_responses_stream(events, "response.completed")["output"]
    assert item["type"] == "tool_search_call" and item["arguments"] == {"query": "tools"}, item
    REQUESTS.popleft()
    found = {"type": "function", "name": "found"}
    reply("loaded")
    assert post(base, tools=[search], input=[{"role": "user", "content": "search"}, item, {"type": "tool_search_output", "call_id": item["call_id"], "tools": [found]}])[0] == 200
    sent = REQUESTS.popleft()
    assert sent["tools"][1]["function"]["name"] == "found", sent
    assert sent["messages"][-1] == {"role": "tool", "tool_call_id": item["call_id"], "content": json.dumps([found], separators=(",", ":"))}
    for reason, expected in (("length", "max_output_tokens"), ("content_filter", "content_filter")):
        for stream in (True, False):
            reply("未生成完", [{"function": {"name": "read", "arguments": '{"abc":'}}], reason)
            status, body = post(base, input="go", stream=stream)
            assert status == 200, body
            response = assert_responses_stream(body, "response.incomplete") if stream else body
            assert response["status"] == "incomplete" and response["incomplete_details"] == {"reason": expected}, response
            assert len(response["output"]) == 1 and response["output"][0]["phase"] == "commentary", response
            REQUESTS.popleft()
    for calls in ([], "invalid", [{"function": {"name": "read"}}, {"broken": True}]):
        reply("not a final response", calls, "tool_calls")
        status, body = post(base, input="go", stream=True)
        assert status == 502 and body["error"]["code"] == "UPSTREAM_ERROR", (status, body)
        REQUESTS.popleft()
    reply(calls=[{"function": {"name": "patch", "arguments": "{}"}}], reason="tool_calls")
    assert post(base, input="go", tools=[{"type": "custom", "name": "patch"}], stream=True)[0] == 502
    REQUESTS.popleft()


def other_protocols(base):
    """检查同一归一层没有破坏 Chat 工具 ID 和 Claude 流式缓存计数。"""
    reply(calls=[{"function": {"name": "lookup", "arguments": {"id": 7}}}], reason="tool_calls")
    status, chat = post(base, path="chat/completions", messages=[{"role": "user", "content": "go"}])
    assert status == 200 and chat["choices"][0]["message"]["tool_calls"][0]["id"].startswith("call_"), chat
    REQUESTS.popleft()
    reply("ok")
    status, claude = post(base, path="messages", messages=[{"role": "user", "content": "go"}], stream=True)
    assert status == 200 and claude[0]["type"] == "message_start", claude
    assert claude[0]["message"]["usage"] == {"input_tokens": 80, "output_tokens": 0, "cache_read_input_tokens": 400, "cache_creation_input_tokens": 20}, claude[0]
    REQUESTS.popleft()


with serve(AiProxy) as upstream, serve(adapter.CloudBaseHandler) as base:
    adapter.UPSTREAM_BASE = upstream
    tool_round_trip(base)
    search_and_errors(base)
    other_protocols(base)
    assert not REQUESTS and not REPLIES
