#!/usr/bin/env python3
"""腾讯云 CloudBase 成长计划自定义连接器。

对网关暴露三种原生协议：
1. OpenAI Chat Completions：POST /v1/chat/completions
2. OpenAI Responses：POST /v1/responses
3. Anthropic Messages：POST /v1/messages

三种协议最终都通过 CloudBase aiProxy 云函数的 Node SDK 调用成长计划模型。
云函数 HTTP 入口只返回一次性 JSON，所以这里的流式响应由本机按各协议标准
合成；事件形状是原生协议，底层不是逐 token 真流式。

本脚本不包含 CloudBase API Key，也不会把 Key、消息正文或工具参数写入日志。
"""
from __future__ import annotations

import hashlib
import json
import os
import re
import signal
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any
from urllib import error as urlerror
from urllib import request as urlrequest

HOST = "127.0.0.1"
PORT = int(os.environ["PROVIDER_SCRIPT_PORT"])
UPSTREAM_BASE = os.environ.get("UPSTREAM_BASE_URL", "").strip().rstrip("/")
UPSTREAM_API_KEY = os.environ.get("UPSTREAM_API_KEY", "").strip()
UPSTREAM_AUTH_HEADER = (os.environ.get("UPSTREAM_AUTH_HEADER", "Authorization") or "Authorization").strip()
MAX_CONCURRENCY = max(0, int(os.environ.get("PROVIDER_SCRIPT_MAX_CONCURRENCY", "0") or 0))
REQUEST_TIMEOUT = max(0, int(os.environ.get("PROVIDER_SCRIPT_REQUEST_TIMEOUT_SEC", "0") or 0))
DEFAULT_TIMEOUT = REQUEST_TIMEOUT or 150
SLOTS = threading.BoundedSemaphore(MAX_CONCURRENCY) if MAX_CONCURRENCY > 0 else None
LOG_LOCK = threading.Lock()
HEALTH_LOCK = threading.Lock()
ID_LOCK = threading.Lock()
ID_SEQ = 0
HEALTH_AT = 0.0
HEALTH_OK = False

# Responses 的 previous_response_id 需要服务端保存上一轮消息。这里只保存在
# adapter 进程内，不落盘；adapter 重启后旧 ID 会失效，客户端应重新发全量历史。
RESPONSE_STORE: dict[str, dict[str, Any]] = {}
RESPONSE_STORE_LOCK = threading.Lock()
RESPONSE_STORE_LIMIT = 128
RESPONSE_STORE_TTL_SEC = 24 * 60 * 60

# 成长计划当前覆盖的文本模型。上下文和输出上限来自腾讯控制台的模型目录。
MODELS: dict[str, dict[str, Any]] = {
    "hy3": {"name": "Hunyuan 3", "context": 256_000, "output": 16_384},
    "hy3-preview": {"name": "Hunyuan 3 Preview", "context": 256_000, "output": 16_384},
    "hy4-flash": {"name": "Hunyuan 4 Flash", "context": 1_000_000, "output": 64_000},
    "hy4": {"name": "Hunyuan 4", "context": 1_000_000, "output": 16_384},
    "hy4-preview": {"name": "Hunyuan 4 Preview", "context": 1_000_000, "output": 16_384},
    "deepseek-v4.1-flash": {"name": "DeepSeek V4.1 Flash", "context": 1_000_000, "output": 16_384},
    "deepseek-v4.1-flash-1017": {"name": "DeepSeek V4.1 Flash 1017", "context": 1_000_000, "output": 16_384},
}
STATUS_BY_CODE = {
    "INVALID_REQUEST": 400,
    "MODEL_NOT_ALLOWED": 400,
    "AI_MODEL_NOT_SUPPORTED": 400,
    "AI_MODEL_NOT_FOUND": 404,
    "AI_MODEL_DISABLED": 403,
    "PERMISSION_DENIED": 401,
    "EXCEED_TOKEN_QUOTA_LIMIT": 429,
    "EXCEED_CONCURRENT_REQUEST_LIMIT": 429,
    "AI_CHANNEL_NOT_ALLOWED": 502,
    "TCB_ENV_MISSING": 500,
    "UPSTREAM_ERROR": 502,
    "INVALID_UPSTREAM_RESPONSE": 502,
}
INCOMPLETE_REASONS = {"length": "max_output_tokens", "content_filter": "content_filter"}
TOOL_SEARCH_KEY = ("tool_search", "", "tool_search")
ResponsesToolKey = tuple[str, str, str]


def log(message: str) -> None:
    """写一条不含密钥和正文的运行日志。"""
    with LOG_LOCK:
        print(message, file=sys.stderr, flush=True)


def new_id(prefix: str) -> str:
    """生成带前缀的稳定形状 ID，并用进程内序号避免同毫秒碰撞。"""
    global ID_SEQ
    with ID_LOCK:
        ID_SEQ += 1
        seq = ID_SEQ
    return f"{prefix}{int(time.time() * 1000)}{os.getpid() % 1000}{seq % 1_000_000}"


def safe_int(value: Any, default: int = 0) -> int:
    """把上游可能返回的字符串或空值安全转成整数。"""
    try:
        return int(value)
    except (TypeError, ValueError):
        return default


def auth_value() -> str:
    """按配置生成 CloudBase HTTP API 的鉴权值。"""
    if UPSTREAM_AUTH_HEADER.lower() == "authorization":
        if UPSTREAM_API_KEY.lower().startswith("bearer "):
            return UPSTREAM_API_KEY
        return "Bearer " + UPSTREAM_API_KEY
    return UPSTREAM_API_KEY


def upstream_headers() -> dict[str, str]:
    """构造只发给 CloudBase 云函数的请求头。"""
    headers = {
        "Accept": "application/json",
        "Content-Type": "application/json",
        "User-Agent": "llm-protocol-gateway-cloudbase-adapter/2.0",
    }
    if UPSTREAM_API_KEY:
        headers[UPSTREAM_AUTH_HEADER] = auth_value()
    return headers


def error_payload(code: str, message: str) -> dict[str, Any]:
    """构造 OpenAI 风格错误体。"""
    return {"error": {"code": code, "message": message, "type": "cloudbase_connector_error"}}


def claude_error_payload(code: str, message: str) -> dict[str, Any]:
    """构造 Anthropic Messages 风格错误体。"""
    if code in ("EXCEED_TOKEN_QUOTA_LIMIT", "EXCEED_CONCURRENT_REQUEST_LIMIT"):
        error_type = "rate_limit_error"
    elif code in ("UPSTREAM_ERROR", "TCB_ENV_MISSING", "AI_CHANNEL_NOT_ALLOWED"):
        error_type = "api_error"
    else:
        error_type = "invalid_request_error"
    return {"type": "error", "error": {"type": error_type, "message": message, "code": code}}


def post_function(payload: dict[str, Any], timeout: int) -> tuple[int, bytes, str]:
    """调用 CloudBase aiProxy 云函数并返回状态、响应体和内容类型。"""
    body = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    request = urlrequest.Request(UPSTREAM_BASE, data=body, headers=upstream_headers(), method="POST")
    try:
        with urlrequest.urlopen(request, timeout=timeout) as response:
            return response.status, response.read(), response.headers.get("Content-Type", "application/json")
    except urlerror.HTTPError as exc:
        return exc.code, exc.read(), exc.headers.get("Content-Type", "application/json")
    except (TimeoutError, OSError) as exc:
        status = 504 if isinstance(exc, TimeoutError) else 502
        return status, json.dumps(error_payload("UPSTREAM_ERROR", str(exc) or "upstream request failed"), ensure_ascii=False).encode("utf-8"), "application/json"


def ping_function(timeout: int = 30) -> tuple[bool, str]:
    """检查 aiProxy 云函数是否存在；结果缓存 60 秒，避免每次请求都 ping。"""
    global HEALTH_AT, HEALTH_OK
    with HEALTH_LOCK:
        if time.time() - HEALTH_AT < 60:
            return HEALTH_OK, ""
        if not UPSTREAM_BASE or not UPSTREAM_API_KEY:
            HEALTH_AT = time.time()
            HEALTH_OK = False
            return False, "UPSTREAM_BASE_URL or UPSTREAM_API_KEY is missing"
        status, body, _ = post_function({"action": "ping"}, timeout)
        HEALTH_AT = time.time()
        if status < 200 or status >= 300:
            HEALTH_OK = False
            return False, f"aiProxy returned HTTP {status}: {body[:300].decode('utf-8', 'replace')}"
        try:
            data = json.loads(body.decode("utf-8"))
        except Exception:
            HEALTH_OK = False
            return False, "aiProxy returned non-JSON health response"
        HEALTH_OK = bool(data.get("ok"))
        return HEALTH_OK, "" if HEALTH_OK else "aiProxy health response was not ok"


def provider_hints(model: str) -> list[str]:
    """按模型系列给出 CloudBase SDK provider 的优先顺序。"""
    if model.lower().startswith("hy"):
        return ["hunyuan-v3", "cloudbase"]
    return ["cloudbase", "hunyuan-v3"]


def content_text(content: Any) -> str:
    """把 OpenAI / Responses / Claude 的文本内容统一成字符串。"""
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts: list[str] = []
        for item in content:
            if not isinstance(item, dict):
                continue
            block_type = str(item.get("type") or "")
            if block_type in ("text", "input_text", "output_text", "text_delta"):
                text = item.get("text")
                if isinstance(text, str):
                    parts.append(text)
            elif block_type == "image":
                parts.append("[image]")
        return "".join(parts)
    if content is None:
        return ""
    return str(content)


def tool_output_text(content: Any) -> str:
    """把工具结果转成 Chat tool message 要求的字符串。"""
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        text = content_text(content)
        if text:
            return text
    try:
        return json.dumps(content, ensure_ascii=False, separators=(",", ":"))
    except Exception:
        return str(content or "")


def normalize_completion(data: dict[str, Any], model: str, history_length: int = 0) -> dict[str, Any]:
    """在读取云函数响应时校验工具形状、补齐调用 ID；各协议随后只读。"""
    if isinstance(data.get("result"), dict):
        data = data["result"]
    if isinstance(data.get("error"), dict):
        return data
    choices = data.get("choices")
    if choices is None:
        # 容忍直接返回 SDK 结果的 aiProxy，但不能把无有效输出的响应伪装成成功。
        raws = data.get("rawResponses")
        raw = raws[-1] if isinstance(raws, list) and raws and isinstance(raws[-1], dict) else {}
        sdk_usage = data.get("usage") if isinstance(data.get("usage"), dict) else {}
        raw_usage = raw.get("usage") if isinstance(raw.get("usage"), dict) else {}
        # 裸 SDK 结果只能从已知的请求历史边界之后取 assistant，避免重发历史工具调用。
        if "choices" in raw:
            data = {**raw, "usage": {**sdk_usage, **raw_usage}}
        else:
            messages = data.get("messages")
            last = messages[-1] if isinstance(messages, list) and len(messages) > history_length else None
            message = dict(last) if isinstance(last, dict) and last.get("role") == "assistant" else {"role": "assistant"}
            if message.get("content") is None and isinstance(data.get("text"), str):
                message["content"] = data["text"]
            data = {**data, "choices": [{"index": 0, "message": message, "finish_reason": data.get("finish_reason")}]}
    if not isinstance(data.get("choices"), list) or not data["choices"]:
        return error_payload("UPSTREAM_ERROR", "aiProxy returned an invalid completion choices list")
    choice = data["choices"][0]
    if not isinstance(choice, dict) or not isinstance(choice.get("message"), dict):
        return error_payload("UPSTREAM_ERROR", "aiProxy returned a completion without an assistant message")
    message = dict(choice["message"])
    content = message.get("content")
    if message.get("role", "assistant") != "assistant" or (content is not None and not isinstance(content, (str, list))):
        return error_payload("UPSTREAM_ERROR", "aiProxy returned an invalid assistant message")
    finish_reason = str(choice.get("finish_reason") or "").strip().lower()
    finish_reason = {
        "max_tokens": "length", "max_output_tokens": "length", "sensitive": "content_filter",
        "function_call": "tool_calls", "tool_call": "tool_calls",
    }.get(finish_reason, finish_reason)
    if finish_reason not in ("", "stop", "tool_calls", *INCOMPLETE_REASONS):
        return error_payload("UPSTREAM_ERROR", "aiProxy returned an unsupported finish reason")
    try:
        # 截断的工具参数可能不是完整 JSON，不向任何协议交付可执行的调用。
        calls = [] if finish_reason in INCOMPLETE_REASONS else normalize_completion_calls(message.get("tool_calls"))
    except ValueError as exc:
        return error_payload("UPSTREAM_ERROR", str(exc))
    if calls:
        finish_reason = "tool_calls"
        message["tool_calls"] = calls
    else:
        message.pop("tool_calls", None)
        if finish_reason == "tool_calls":
            return error_payload("UPSTREAM_ERROR", "aiProxy reported a tool call without a callable tool")
        if finish_reason not in INCOMPLETE_REASONS and not content_text(content):
            return error_payload("UPSTREAM_ERROR", "aiProxy returned no assistant text or tool calls")
    message["role"] = "assistant"
    return {
        **data, "id": data.get("id") or new_id("chatcmpl-cloudbase-"), "object": "chat.completion",
        "created": data.get("created") or int(time.time()), "model": data.get("model") or model,
        "choices": [{**choice, "index": 0, "message": message, "finish_reason": finish_reason or "stop"}],
    }


def normalize_completion_calls(value: Any) -> list[dict[str, Any]]:
    """保留完整的并行工具调用，拒绝不合法的条目而不是悄悄少发工具。"""
    if value is None:
        return []
    if not isinstance(value, list):
        raise ValueError("aiProxy returned an invalid tool_calls list")
    calls = []
    seen: set[str] = set()
    for item in value:
        function = item.get("function") if isinstance(item, dict) else None
        name = function.get("name") if isinstance(function, dict) else None
        if not isinstance(name, str) or not name.strip():
            raise ValueError("aiProxy returned a tool call without a valid name")
        arguments = function.get("arguments")
        if arguments is not None and not isinstance(arguments, (str, dict)):
            raise ValueError("aiProxy returned invalid tool call arguments")
        if not isinstance(arguments, str):
            arguments = json.dumps(arguments or {}, ensure_ascii=False, separators=(",", ":"))
        call_id = item.get("id")
        if not isinstance(call_id, str) or not call_id.strip():
            call_id = new_id("call_")
        if call_id in seen:
            raise ValueError("aiProxy returned duplicate tool call IDs")
        seen.add(call_id)
        calls.append({"id": call_id, "type": "function", "function": {"name": name.strip(), "arguments": arguments or "{}"}})
    return calls


def completion_details(completion: dict[str, Any]) -> tuple[str, list[dict[str, Any]], str, dict[str, Any]]:
    """从已归一的 Chat completion 读取正文、工具状态和 usage，不重新生成 ID。"""
    choice = completion["choices"][0]
    message = choice["message"]
    usage = completion.get("usage") if isinstance(completion.get("usage"), dict) else {}
    return content_text(message.get("content")), message.get("tool_calls") or [], choice["finish_reason"], usage


def apply_responses_output_phases(output: list[dict[str, Any]], done: bool) -> None:
    """按 OpenAI Responses 的语义给 assistant message 标注 phase。

    Codex 会把没有 phase 的 assistant message 当成最终答复；工具调用前的说明
    因此会让回合看起来“已经说完”。工具调用前或另一个非 reasoning 输出前的
    message 是 commentary，只有正常结束时的最后一个非 reasoning item 才是
    final_answer。截断内容仍是 commentary；上游若已给出 phase 则原样保留。
    """
    if not isinstance(output, list):
        return
    last = -1
    for index, item in enumerate(output):
        if isinstance(item, dict) and item.get("type") != "reasoning":
            last = index
    for index, item in enumerate(output):
        if not isinstance(item, dict) or item.get("type") != "message":
            continue
        if item.get("role") not in (None, "assistant"):
            continue
        if item.get("phase"):
            continue
        if index < last or not done:
            item["phase"] = "commentary"
        elif index == last:
            item["phase"] = "final_answer"


def usage_numbers(usage: dict[str, Any]) -> tuple[int, int, int]:
    """把不同协议的 usage 字段归一成输入、输出、总 token。"""
    prompt = safe_int(usage.get("prompt_tokens") or usage.get("input_tokens"))
    output = safe_int(usage.get("completion_tokens") or usage.get("output_tokens"))
    total = safe_int(usage.get("total_tokens"), prompt + output)
    return prompt, output, total


def cache_read_tokens(usage: dict[str, Any]) -> int:
    """读取上游的提示缓存命中数，兼容 OpenAI / Responses / Claude 三种命名。"""
    if not isinstance(usage, dict):
        return 0
    for key in ("cache_read_input_tokens", "prompt_cache_hit_tokens"):
        value = safe_int(usage.get(key))
        if value > 0:
            return value
    for key in ("prompt_tokens_details", "input_tokens_details"):
        details = usage.get(key)
        if isinstance(details, dict):
            value = safe_int(details.get("cached_tokens"))
            if value > 0:
                return value
    return 0


def cache_creation_tokens(usage: dict[str, Any]) -> int:
    """读取上游的提示缓存写入数。"""
    if not isinstance(usage, dict):
        return 0
    for key in ("cache_creation_input_tokens", "cache_write_input_tokens"):
        value = safe_int(usage.get(key))
        if value > 0:
            return value
    return 0


def normalize_chat_tools(tools: Any) -> list[dict[str, Any]]:
    """把 Chat / Responses / Claude 工具定义统一成 Chat tools。"""
    if not isinstance(tools, list):
        return []
    normalized: list[dict[str, Any]] = []
    for item in tools:
        if not isinstance(item, dict):
            continue
        nested = item.get("function") if isinstance(item.get("function"), dict) else None
        name = str((nested or item).get("name") or "").strip()
        if not name:
            continue
        parameters = (nested or item).get("parameters")
        if not isinstance(parameters, dict):
            parameters = item.get("input_schema") if isinstance(item.get("input_schema"), dict) else {"type": "object", "properties": {}}
        normalized.append({
            "type": "function",
            "function": {
                "name": name,
                "description": str((nested or item).get("description") or ""),
                "parameters": parameters,
            },
        })
    return normalized


def normalize_tool_choice(value: Any) -> Any:
    """把三种协议的 tool_choice 映射成 CloudBase SDK 接受的值。"""
    if value == "none":
        return "none"
    if value in ("auto", "required", "custom"):
        return "auto"
    if isinstance(value, dict):
        return "auto"
    return None


def normalize_messages(messages: Any) -> list[dict[str, Any]]:
    """校验并浅拷贝 Chat messages，避免修改调用方对象。"""
    if not isinstance(messages, list) or not messages:
        raise ValueError("messages is required")
    out: list[dict[str, Any]] = []
    for message in messages:
        if not isinstance(message, dict) or not str(message.get("role") or "").strip():
            raise ValueError("message.role is required")
        out.append(dict(message))
    return out


def call_cloudbase_chat(
    messages: list[dict[str, Any]],
    model: str,
    tools: list[dict[str, Any]] | None = None,
    tool_choice: Any = None,
    temperature: Any = None,
    top_p: Any = None,
    max_tokens: Any = None,
) -> tuple[dict[str, Any] | None, tuple[int, str, str] | None]:
    """调用云函数，返回 Chat completion 或 (HTTP 状态, 错误码, 错误信息)。"""
    payload: dict[str, Any] = {
        "action": "chat",
        "model": model,
        "messages": messages,
        "stream": False,
        "providerHints": provider_hints(model),
    }
    if tools:
        payload["tools"] = tools
    if tool_choice is not None:
        payload["tool_choice"] = tool_choice
    if isinstance(temperature, (int, float)):
        payload["temperature"] = temperature
    if isinstance(top_p, (int, float)):
        payload["top_p"] = top_p
    if isinstance(max_tokens, int) and max_tokens > 0:
        payload["max_tokens"] = max_tokens

    status, body, _ = post_function(payload, DEFAULT_TIMEOUT)
    try:
        data = json.loads(body.decode("utf-8"))
    except Exception:
        return None, (502, "UPSTREAM_ERROR", "aiProxy returned non-JSON response")
    if not isinstance(data, dict):
        return None, (502, "UPSTREAM_ERROR", "aiProxy returned an invalid response")
    if status < 200 or status >= 300:
        error = data.get("error") if isinstance(data.get("error"), dict) else {}
        code = str(error.get("code") or "UPSTREAM_ERROR")
        message = str(error.get("message") or f"aiProxy returned HTTP {status}")
        return None, (status, code, message)
    completion = normalize_completion(data, model, len(messages))
    if isinstance(completion.get("error"), dict):
        error = completion["error"]
        code = str(error.get("code") or "UPSTREAM_ERROR")
        message = str(error.get("message") or "CloudBase model call failed")
        return None, (STATUS_BY_CODE.get(code, 502), code, message)
    return completion, None


def is_probe_request(payload: dict[str, Any]) -> bool:
    """识别网关协议探测的极小 token 上限请求，避免探测真实消耗模型额度。"""
    for key in ("max_tokens", "max_completion_tokens", "max_output_tokens"):
        value = payload.get(key)
        if isinstance(value, int) and value <= 1:
            return True
    return False


def remember_response(response_id: str, messages: list[dict[str, Any]], tools: ResponsesTools | None) -> None:
    """保存 Responses 的上一轮消息和工具名映射，供 previous_response_id 续接。"""
    now = time.time()
    with RESPONSE_STORE_LOCK:
        expired = [key for key, value in RESPONSE_STORE.items() if now - float(value.get("at", 0)) > RESPONSE_STORE_TTL_SEC]
        for key in expired:
            RESPONSE_STORE.pop(key, None)
        RESPONSE_STORE[response_id] = {"messages": messages, "tools": tools, "at": now}
        while len(RESPONSE_STORE) > RESPONSE_STORE_LIMIT:
            RESPONSE_STORE.pop(next(iter(RESPONSE_STORE)))


def recall_response(response_id: str) -> tuple[list[dict[str, Any]], ResponsesTools | None]:
    """读取 previous_response_id 的历史与别名；过期或不存在时返回空结果。"""
    if not response_id:
        return [], None
    with RESPONSE_STORE_LOCK:
        item = RESPONSE_STORE.get(response_id)
        if not item:
            return [], None
        if time.time() - float(item.get("at", 0)) > RESPONSE_STORE_TTL_SEC:
            RESPONSE_STORE.pop(response_id, None)
            return [], None
        return list(item["messages"]), item["tools"]


class ResponsesTools:
    """在 Chat 的扁平工具名和 Codex 的原始工具身份间双向转换。"""

    def __init__(self, declared: Any, items: list[Any], previous: ResponsesTools | None) -> None:
        """收集显式和动态加载的工具；优先保留普通函数的合法原名。"""
        self.aliases = dict(previous.aliases) if previous else {}
        self.keys = {alias: key for key, alias in self.aliases.items()}
        definitions: dict[ResponsesToolKey, dict[str, Any]] = {}
        self.collect(declared, definitions)
        for item in items:
            if isinstance(item, dict) and item.get("type") in ("additional_tools", "tool_search_output"):
                self.collect(item.get("tools"), definitions)
        for key in sorted(definitions, key=lambda key: (key[0] != "function" or bool(key[1]), key)):
            self.alias_for(key)
        self.chat_tools = [
            {"type": "function", "function": {**definition, "name": self.aliases[key]}}
            for key, definition in definitions.items()
        ]

    def collect(self, tools: Any, definitions: dict[ResponsesToolKey, dict[str, Any]], namespace: str = "") -> None:
        """展开 namespace，把 custom 的原始字符串放到单一 input 属性。"""
        if not isinstance(tools, list):
            return
        for tool in tools:
            if not isinstance(tool, dict):
                continue
            kind = tool.get("type") or "function"
            nested = tool.get("function")
            definition = nested if isinstance(nested, dict) else tool
            name = str(definition.get("name") or "").strip()
            if kind == "namespace" and name and not namespace:
                self.collect(tool.get("tools") or tool.get("children"), definitions, name)
            if kind == "tool_search" and not namespace and tool.get("execution") != "server":
                definitions.setdefault(TOOL_SEARCH_KEY, {
                    "description": "Search and load tools for the current task.",
                    "parameters": {"type": "object", "properties": {"query": {"type": "string"}, "limit": {"type": "integer"}}, "required": ["query"]},
                })
            if not name or kind not in ("function", "custom"):
                continue
            description = str(definition.get("description") or "")
            parameters = definition.get("parameters")
            if kind == "custom":
                description += "\nPass the original tool's raw input as a string, preserving whitespace."
                if tool.get("format") is not None:
                    description += " Input format: " + json.dumps(tool["format"], ensure_ascii=False)
                parameters = {"type": "object", "properties": {"input": {"type": "string"}}, "required": ["input"], "additionalProperties": False}
            if not isinstance(parameters, dict):
                parameters = {"type": "object", "properties": {}}
            definitions.setdefault((kind, namespace, name), {"description": description, "parameters": parameters})

    def alias_for(self, key: ResponsesToolKey) -> str:
        """分配不超过 64 字符的合法 Chat 别名，避免同名或截断碰撞。"""
        if key in self.aliases:
            return self.aliases[key]
        _, namespace, name = key
        label = f"{namespace}__{name}" if namespace else name
        alias = label
        if not re.fullmatch(r"[A-Za-z0-9_-]{1,64}", alias) or alias in self.keys:
            prefix = re.sub(r"[^A-Za-z0-9_-]", "_", label)[:46]
            identity = json.dumps(key, ensure_ascii=False).encode("utf-8")
            index = 0
            while True:
                suffix = hashlib.sha256(identity + b":" + str(index).encode()).hexdigest()[:16]
                alias = f"{prefix}__{suffix}"
                if alias not in self.keys:
                    break
                index += 1
        self.aliases[key] = alias
        self.keys[alias] = key
        return alias


def responses_input_to_chat(payload: dict[str, Any]) -> tuple[list[dict[str, Any]], ResponsesTools]:
    """把 Responses 历史和工具定义拆开，并保留前言与工具调用的配对。"""
    messages, previous_tools = recall_response(str(payload.get("previous_response_id") or ""))
    instructions = content_text(payload.get("instructions"))
    if instructions:
        messages.append({"role": "system", "content": instructions})

    raw_input = payload.get("input")
    if isinstance(raw_input, str):
        raw_input = [{"role": "user", "content": raw_input}]
    elif isinstance(raw_input, dict):
        raw_input = [raw_input]
    items = raw_input if isinstance(raw_input, list) else []
    tools = ResponsesTools(payload.get("tools"), items, previous_tools)
    search_ids = {
        call["id"] for message in messages for call in message.get("tool_calls", [])
        if tools.keys.get(call["function"]["name"]) == TOOL_SEARCH_KEY
    }
    pending: dict[str, Any] | None = None

    def flush_calls() -> None:
        """把前言及连续的并行调用作为一条 assistant 消息提交。"""
        nonlocal pending
        if pending is not None:
            messages.append(pending)
            pending = None

    for item in items:
        if not isinstance(item, dict):
            continue
        item_type = str(item.get("type") or "")
        if item_type in ("function_call", "custom_tool_call", "tool_search_call"):
            if item.get("status") in ("incomplete", "in_progress"):
                continue
            call_id = str(item.get("call_id") or item.get("id") or new_id("call_"))
            arguments = item.get("arguments")
            if item_type == "tool_search_call":
                if item.get("execution") == "server":
                    continue
                key = TOOL_SEARCH_KEY
                search_ids.add(call_id)
            else:
                name = str(item.get("name") or "").strip()
                if not name:
                    raise ValueError("tool call name is required")
                kind = "custom" if item_type == "custom_tool_call" else "function"
                key = (kind, str(item.get("namespace") or "").strip(), name)
                if kind == "custom":
                    arguments = {"input": item.get("input", "")}
            if not isinstance(arguments, str):
                arguments = json.dumps(arguments or {}, ensure_ascii=False, separators=(",", ":"))
            if pending is None:
                pending = {"role": "assistant"}
            pending.setdefault("tool_calls", []).append({
                "id": call_id,
                "type": "function",
                "function": {"name": tools.alias_for(key), "arguments": arguments or "{}"},
            })
            continue
        if item_type in ("function_call_output", "custom_tool_call_output", "tool_search_output"):
            call_id = str(item.get("call_id") or item.get("id") or "")
            # 仅携带定义的 tool_search_output 不生成孤立的 tool message。
            if item_type == "tool_search_output" and call_id not in search_ids:
                continue
            flush_calls()
            result = item.get("output", item.get("tools") if item_type == "tool_search_output" else None)
            messages.append({
                "role": "tool",
                "tool_call_id": call_id,
                "content": tool_output_text(result),
            })
            continue
        if item_type in ("message", "") and item.get("role"):
            flush_calls()
            role = "system" if item.get("role") == "developer" else str(item.get("role"))
            message = {"role": role, "content": content_text(item.get("content"))}
            if role == "assistant" and item.get("phase") != "final_answer":
                pending = message
            else:
                messages.append(message)
    flush_calls()
    return messages, tools


def chat_message_from_completion(completion: dict[str, Any]) -> dict[str, Any]:
    """把 Chat completion 的 assistant message 还原成可再次发送的 Chat message。"""
    text, tool_calls, _, _ = completion_details(completion)
    message: dict[str, Any] = {"role": "assistant"}
    reasoning = completion["choices"][0]["message"].get("reasoning_content")
    if isinstance(reasoning, str) and reasoning:
        message["reasoning_content"] = reasoning
    if text:
        message["content"] = text
    if tool_calls:
        message["tool_calls"] = tool_calls
    if not text and not tool_calls:
        message["content"] = ""
    return message


def response_output_items(text: str, tool_calls: list[dict[str, Any]], tools: ResponsesTools | None) -> list[dict[str, Any]]:
    """恢复 Responses 工具类型、名称和 namespace，原样返回 custom 工具的字符串。"""
    output: list[dict[str, Any]] = []
    if text:
        output.append({
            "id": new_id("msg_"),
            "type": "message",
            "status": "completed",
            "role": "assistant",
            "content": [{"type": "output_text", "text": text, "annotations": []}],
        })
    for call in tool_calls:
        function = call["function"]
        alias = function["name"]
        kind, namespace, name = tools.keys.get(alias, ("function", "", alias)) if tools else ("function", "", alias)
        arguments = function["arguments"]
        item = {"id": new_id("fc_"), "status": "completed", "call_id": call["id"]}
        if kind in ("custom", "tool_search"):
            try:
                args = json.loads(arguments)
            except ValueError as exc:
                raise ValueError("aiProxy returned invalid arguments for a Responses client tool") from exc
            if not isinstance(args, dict):
                raise ValueError("aiProxy returned non-object arguments for a Responses client tool")
            if kind == "custom":
                if not isinstance(args.get("input"), str):
                    raise ValueError("aiProxy returned a custom tool without string input")
                item.update(type="custom_tool_call", name=name, input=args["input"])
            else:
                item.update(type="tool_search_call", execution="client", arguments=args)
        else:
            item.update(type="function_call", name=name, arguments=arguments)
        if namespace:
            item["namespace"] = namespace
        output.append(item)
    return output


def completion_to_response(
    completion: dict[str, Any],
    model: str,
    payload: dict[str, Any],
    conversation_messages: list[dict[str, Any]],
    tools: ResponsesTools | None = None,
) -> dict[str, Any]:
    """把 Chat completion 包装成 OpenAI Responses 响应。"""
    text, tool_calls, finish_reason, usage = completion_details(completion)
    prompt, output_tokens, total_tokens = usage_numbers(usage)
    cache_read = cache_read_tokens(usage)
    response_id = new_id("resp_")
    incomplete_reason = INCOMPLETE_REASONS.get(finish_reason)
    status = "incomplete" if incomplete_reason else "completed"
    # 输出截断时参数可能是不完整的 JSON，不向客户端暴露可执行的调用项。
    output = response_output_items(text, [] if incomplete_reason else tool_calls, tools)
    if incomplete_reason:
        for item in output:
            item["status"] = "incomplete"
    apply_responses_output_phases(output, status == "completed")
    response = {
        "id": response_id,
        "object": "response",
        "created_at": int(time.time()),
        "status": status,
        "error": None,
        "incomplete_details": {"reason": incomplete_reason} if incomplete_reason else None,
        "instructions": payload.get("instructions"),
        "max_output_tokens": payload.get("max_output_tokens"),
        "model": model,
        "output": output,
        "parallel_tool_calls": bool(payload.get("parallel_tool_calls", True)),
        "previous_response_id": payload.get("previous_response_id"),
        "reasoning": payload.get("reasoning") or {"effort": None, "summary": None},
        "store": bool(payload.get("store", True)),
        "temperature": payload.get("temperature"),
        "text": payload.get("text") or {"format": {"type": "text"}},
        "tool_choice": payload.get("tool_choice", "auto"),
        "tools": payload.get("tools") or [],
        "top_p": payload.get("top_p"),
        "truncation": payload.get("truncation", "disabled"),
        "usage": {
            "input_tokens": prompt,
            "input_tokens_details": {"cached_tokens": cache_read},
            "output_tokens": output_tokens,
            "output_tokens_details": {"reasoning_tokens": safe_int(usage.get("reasoning_tokens"))},
            "total_tokens": total_tokens,
        },
        "output_text": text,
    }
    if bool(payload.get("store", True)):
        assistant = chat_message_from_completion(completion) if not incomplete_reason else {"role": "assistant", "content": text}
        history = conversation_messages + [assistant] if not incomplete_reason or text else conversation_messages
        remember_response(response_id, history, tools)
    return response


def claude_messages_to_chat(payload: dict[str, Any]) -> list[dict[str, Any]]:
    """把 Anthropic Messages 请求转成 Chat messages。"""
    messages: list[dict[str, Any]] = []
    system_text = content_text(payload.get("system"))
    if system_text:
        messages.append({"role": "system", "content": system_text})
    raw_messages = payload.get("messages")
    if not isinstance(raw_messages, list):
        return messages
    for item in raw_messages:
        if not isinstance(item, dict):
            continue
        role = str(item.get("role") or "")
        content = item.get("content")
        if role == "assistant":
            text_parts: list[str] = []
            tool_calls: list[dict[str, Any]] = []
            for block in content if isinstance(content, list) else []:
                if not isinstance(block, dict):
                    continue
                block_type = str(block.get("type") or "")
                if block_type == "text":
                    text_parts.append(str(block.get("text") or ""))
                elif block_type == "tool_use":
                    tool_calls.append({
                        "id": str(block.get("id") or new_id("call_")),
                        "type": "function",
                        "function": {
                            "name": str(block.get("name") or ""),
                            "arguments": json.dumps(block.get("input") or {}, ensure_ascii=False, separators=(",", ":")),
                        },
                    })
            if isinstance(content, str):
                text_parts.append(content)
            message: dict[str, Any] = {"role": "assistant"}
            if text_parts:
                message["content"] = "".join(text_parts)
            if tool_calls:
                message["tool_calls"] = tool_calls
            if text_parts or tool_calls:
                messages.append(message)
            continue
        if role != "user":
            continue
        if isinstance(content, str):
            messages.append({"role": "user", "content": content})
            continue
        text_parts = []
        for block in content if isinstance(content, list) else []:
            if not isinstance(block, dict):
                continue
            block_type = str(block.get("type") or "")
            if block_type == "text":
                text_parts.append(str(block.get("text") or ""))
            elif block_type == "tool_result":
                if text_parts:
                    messages.append({"role": "user", "content": "".join(text_parts)})
                    text_parts = []
                messages.append({
                    "role": "tool",
                    "tool_call_id": str(block.get("tool_use_id") or ""),
                    "content": tool_output_text(block.get("content")),
                })
            elif block_type == "image":
                text_parts.append("[image]")
        if text_parts:
            messages.append({"role": "user", "content": "".join(text_parts)})
    return messages


def completion_to_claude(completion: dict[str, Any], model: str) -> dict[str, Any]:
    """把 Chat completion 包装成 Anthropic Messages 响应。"""
    text, tool_calls, finish_reason, usage = completion_details(completion)
    prompt, output_tokens, _ = usage_numbers(usage)
    cache_read = cache_read_tokens(usage)
    cache_create = cache_creation_tokens(usage)
    # OpenAI Chat / Responses 的 prompt_tokens 已包含缓存读取与写入；Anthropic
    # 的 input_tokens 只算未缓存部分，所以这里要减掉，避免下游重复累计。
    exclusive_input = max(prompt - cache_read - cache_create, 0)
    content: list[dict[str, Any]] = []
    if text:
        content.append({"type": "text", "text": text})
    for call in tool_calls if finish_reason not in ("length", "content_filter") else []:
        function = call.get("function") or {}
        try:
            tool_input = json.loads(function.get("arguments") or "{}")
        except Exception:
            tool_input = {}
        content.append({
            "type": "tool_use",
            "id": call.get("id") or new_id("toolu_"),
            "name": function.get("name") or "",
            "input": tool_input,
        })
    if not content:
        content.append({"type": "text", "text": ""})
    stop_reason = {"length": "max_tokens", "content_filter": "refusal", "tool_calls": "tool_use"}.get(finish_reason, "end_turn")
    result = {
        "id": new_id("msg_"),
        "type": "message",
        "role": "assistant",
        "model": model,
        "content": content,
        "stop_reason": stop_reason,
        "stop_sequence": None,
        "usage": {"input_tokens": exclusive_input, "output_tokens": output_tokens},
    }
    if cache_read:
        result["usage"]["cache_read_input_tokens"] = cache_read
    if cache_create:
        result["usage"]["cache_creation_input_tokens"] = cache_create
    return result


def estimate_tokens(text: str) -> int:
    """给 count_tokens 旁路提供保守的本地估算，不调用模型。"""
    if not text:
        return 0
    return max(1, len(text) // 3)


def chunk_text(text: str, size: int = 200) -> list[str]:
    """按 Unicode 码点切分，避免拆开 emoji 或代理对。"""
    chars = list(text or "")
    if not chars:
        return [""]
    return ["".join(chars[index:index + size]) for index in range(0, len(chars), size)]


def stream_chunk(base: dict[str, Any], delta: dict[str, Any], finish_reason: Any = None, usage: Any = None) -> dict[str, Any]:
    """构造一个 OpenAI Chat streaming chunk。"""
    payload = {
        "id": base["id"],
        "object": "chat.completion.chunk",
        "created": base["created"],
        "model": base["model"],
        "choices": [{"index": 0, "delta": delta, "finish_reason": finish_reason}],
    }
    if usage:
        payload["usage"] = usage
    return payload


class CloudBaseHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt: str, *args: Any) -> None:
        """禁用 BaseHTTPRequestHandler 默认日志，统一由敏感信息过滤日志输出。"""
        return

    def send_json(self, status: int, payload: dict[str, Any]) -> None:
        """发送标准 JSON 响应。"""
        body = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        try:
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            pass
        self.close_connection = True

    def send_protocol_error(self, status: int, protocol: str, code: str, message: str) -> None:
        """按客户端协议发送错误体。"""
        if protocol == "claude":
            self.send_json(status, claude_error_payload(code, message))
        else:
            self.send_json(status, error_payload(code, message))

    def acquire_slot(self) -> bool:
        """获取连接器并发槽；超时返回 429。"""
        if SLOTS is None:
            return True
        if SLOTS.acquire(timeout=DEFAULT_TIMEOUT):
            return True
        self.send_json(429, error_payload("rate_limit", "CloudBase connector concurrency limit reached"))
        return False

    def release_slot(self) -> None:
        """释放连接器并发槽。"""
        if SLOTS is not None:
            SLOTS.release()

    def read_body(self) -> bytes:
        """读取请求体，兼容网关转发时使用的 HTTP chunked 编码。"""
        transfer_encoding = (self.headers.get("Transfer-Encoding", "") or "").lower()
        if "chunked" in transfer_encoding:
            chunks: list[bytes] = []
            while True:
                size_line = self.rfile.readline(65537)
                if not size_line:
                    raise ValueError("incomplete chunked request body")
                try:
                    size = int(size_line.split(b";", 1)[0].strip(), 16)
                except ValueError as exc:
                    raise ValueError("invalid chunked request body") from exc
                if size == 0:
                    # 消费 trailer；标准客户端这里通常只剩一个空行。
                    while True:
                        trailer = self.rfile.readline(65537)
                        if trailer in (b"\r\n", b"\n", b""):
                            break
                    break
                chunk = self.rfile.read(size)
                if len(chunk) != size:
                    raise ValueError("incomplete chunked request body")
                chunks.append(chunk)
                ending = self.rfile.read(2)
                if ending not in (b"\r\n", b"\n"):
                    raise ValueError("invalid chunk terminator")
            return b"".join(chunks)

        length = int(self.headers.get("Content-Length", "0") or 0)
        if length < 0:
            raise ValueError("invalid Content-Length")
        return self.rfile.read(length) if length else b""

    def begin_sse(self) -> None:
        """发送 SSE 响应头，后续由 write_sse_event 写 chunked 帧。"""
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream; charset=utf-8")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("X-Accel-Buffering", "no")
        self.send_header("Transfer-Encoding", "chunked")
        self.send_header("Connection", "close")
        self.end_headers()
        self.close_connection = True

    def write_sse_event(self, event_name: str, payload: dict[str, Any]) -> None:
        """按 HTTP chunked 编码写一个带 event 名称的 SSE 帧。"""
        data = f"event: {event_name}\ndata: {json.dumps(payload, ensure_ascii=False, separators=(',', ':'))}\n\n".encode("utf-8")
        self.wfile.write(f"{len(data):X}\r\n".encode("ascii") + data + b"\r\n")
        self.wfile.flush()

    def write_sse_data(self, payload: dict[str, Any]) -> None:
        """按 HTTP chunked 编码写一个只有 data 的 SSE 帧。"""
        data = f"data: {json.dumps(payload, ensure_ascii=False, separators=(',', ':'))}\n\n".encode("utf-8")
        self.wfile.write(f"{len(data):X}\r\n".encode("ascii") + data + b"\r\n")
        self.wfile.flush()

    def end_chunked(self) -> None:
        """结束 HTTP chunked 响应。"""
        self.wfile.write(b"0\r\n\r\n")
        self.wfile.flush()

    def do_GET(self) -> None:
        """处理健康检查和模型目录。"""
        if self.path == "/health":
            ready, reason = ping_function()
            self.send_json(200 if ready else 503, {"ok": ready, "provider": "tencent-cloudbase-growth", "protocols": ["openai_chat", "openai_responses", "claude"], "reason": reason})
            return
        if self.path == "/v1/models":
            data = [
                {
                    "id": model_id,
                    "object": "model",
                    "name": meta["name"],
                    "context_length": meta["context"],
                    "max_output_tokens": meta["output"],
                }
                for model_id, meta in MODELS.items()
            ]
            self.send_json(200, {"object": "list", "data": data})
            return
        self.send_json(404, error_payload("not_found", "not found"))

    def do_POST(self) -> None:
        """按路径分派三种原生协议。"""
        path = self.path.split("?", 1)[0]
        if path not in ("/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/messages/count_tokens"):
            self.send_json(404, error_payload("not_found", "not found"))
            return
        if not self.acquire_slot():
            return
        started = time.time()
        status = 502
        model = ""
        protocol = "claude" if path.startswith("/v1/messages") else ("responses" if path == "/v1/responses" else "chat")
        try:
            try:
                payload = json.loads(self.read_body().decode("utf-8"))
            except Exception:
                self.send_protocol_error(400, protocol, "invalid_request_error", "request body must be valid JSON")
                status = 400
                return
            if not isinstance(payload, dict):
                self.send_protocol_error(400, protocol, "invalid_request_error", "request body must be a JSON object")
                status = 400
                return
            model = str(payload.get("model") or "").strip()
            if path == "/v1/messages/count_tokens":
                self.handle_count_tokens(payload)
                status = 200
                return
            if not model:
                self.send_protocol_error(400, protocol, "invalid_request_error", "model is required")
                status = 400
                return
            if model not in MODELS:
                self.send_protocol_error(400, protocol, "model_not_allowed", f"model {model!r} is not in the CloudBase growth-plan allowlist")
                status = 400
                return
            if is_probe_request(payload):
                # 网关协议探测会发 max_tokens=1。直接返回参数错误，既能让探测
                # 判断路径存在，又不会真的消耗成长计划额度。
                self.send_protocol_error(400, protocol, "invalid_request_error", "probe request rejected without model call")
                status = 400
                return
            if path == "/v1/chat/completions":
                status = self.handle_chat(payload, model)
            elif path == "/v1/responses":
                status = self.handle_responses(payload, model)
            else:
                status = self.handle_messages(payload, model)
        except (BrokenPipeError, ConnectionResetError):
            pass
        except ValueError as exc:
            self.send_protocol_error(400, protocol, "invalid_request_error", str(exc))
            status = 400
        except Exception as exc:
            if not self.wfile.closed:
                try:
                    self.send_protocol_error(502, protocol, "connector_error", str(exc))
                except Exception:
                    pass
        finally:
            self.release_slot()
            log(f"protocol={protocol} model={model or '-'} status={status} latency_ms={int((time.time() - started) * 1000)}")

    def handle_chat(self, payload: dict[str, Any], model: str) -> int:
        """处理 OpenAI Chat Completions。"""
        messages = normalize_messages(payload.get("messages"))
        tools = normalize_chat_tools(payload.get("tools"))
        completion, error = call_cloudbase_chat(
            messages,
            model,
            tools=tools,
            tool_choice=normalize_tool_choice(payload.get("tool_choice")),
            temperature=payload.get("temperature"),
            top_p=payload.get("top_p"),
            max_tokens=payload.get("max_tokens") or payload.get("max_completion_tokens"),
        )
        if error:
            self.send_protocol_error(error[0], "chat", error[1], error[2])
            return error[0]
        assert completion is not None
        if not payload.get("stream"):
            self.send_json(200, completion)
            return 200
        text, tool_calls, finish_reason, usage = completion_details(completion)
        base = {
            "id": completion.get("id") or new_id("chatcmpl-cloudbase-"),
            "created": completion.get("created") or int(time.time()),
            "model": completion.get("model") or model,
        }
        self.begin_sse()
        self.write_sse_data(stream_chunk(base, {"role": "assistant", "content": ""}))
        for piece in chunk_text(text):
            if piece:
                self.write_sse_data(stream_chunk(base, {"content": piece}))
        for index, call in enumerate(tool_calls):
            function = call.get("function") or {}
            self.write_sse_data(stream_chunk(base, {
                "tool_calls": [{
                    "index": index,
                    "id": call.get("id") or f"call_{index}",
                    "type": "function",
                    "function": {"name": function.get("name") or "", "arguments": function.get("arguments") or "{}"},
                }],
            }))
        self.write_sse_data(stream_chunk(base, {}, finish_reason, usage))
        done = b"data: [DONE]\n\n"
        self.wfile.write(f"{len(done):X}\r\n".encode("ascii") + done + b"\r\n")
        self.wfile.flush()
        self.end_chunked()
        return 200

    def handle_responses(self, payload: dict[str, Any], model: str) -> int:
        """处理 OpenAI Responses。"""
        messages, tools = responses_input_to_chat(payload)
        if not messages:
            raise ValueError("input is required")
        completion, error = call_cloudbase_chat(
            messages,
            model,
            tools=tools.chat_tools,
            tool_choice=normalize_tool_choice(payload.get("tool_choice")),
            temperature=payload.get("temperature"),
            top_p=payload.get("top_p"),
            max_tokens=payload.get("max_output_tokens"),
        )
        if error:
            self.send_protocol_error(error[0], "responses", error[1], error[2])
            return error[0]
        assert completion is not None
        try:
            response = completion_to_response(completion, model, payload, messages, tools)
        except ValueError as exc:
            self.send_protocol_error(502, "responses", "UPSTREAM_ERROR", str(exc))
            return 502
        if not payload.get("stream"):
            self.send_json(200, response)
            return 200
        self.begin_sse()
        self.stream_responses(response)
        return 200

    def stream_responses(self, response: dict[str, Any]) -> None:
        """按 Responses 事件协议合成 SSE。"""
        sequence = 0

        def emit(name: str, payload: dict[str, Any]) -> None:
            """保持 SSE 类型与事件名一致，并递增序号。"""
            nonlocal sequence
            body = {"type": name, "sequence_number": sequence, **payload}
            self.write_sse_event(name, body)
            sequence += 1

        created = dict(response)
        created["status"] = "in_progress"
        created["output"] = []
        created["output_text"] = ""
        created["usage"] = None
        created["incomplete_details"] = None
        emit("response.created", {"response": created})
        emit("response.in_progress", {"response": created})

        for output_index, item in enumerate(response["output"]):
            kind = item["type"]
            added = {**item, "status": "in_progress"}
            fields = {"item_id": item["id"], "output_index": output_index}
            if kind == "message":
                added["content"] = []
            elif kind == "custom_tool_call":
                added["input"] = ""
            else:
                added["arguments"] = {} if kind == "tool_search_call" else ""
            emit("response.output_item.added", {"output_index": output_index, "item": added})
            if kind == "message":
                for content_index, part in enumerate(item["content"]):
                    content_fields = {**fields, "content_index": content_index}
                    text = part["text"]
                    emit("response.content_part.added", {**content_fields, "part": {**part, "text": ""}})
                    for piece in chunk_text(text):
                        emit("response.output_text.delta", {**content_fields, "delta": piece, "logprobs": []})
                    emit("response.output_text.done", {**content_fields, "text": text, "logprobs": []})
                    emit("response.content_part.done", {**content_fields, "part": part})
            elif kind == "custom_tool_call":
                # custom 工具必须用 input 事件，不改写自由文本或补默认 JSON。
                for piece in chunk_text(item["input"]):
                    if piece:
                        emit("response.custom_tool_call_input.delta", {**fields, "delta": piece})
                emit("response.custom_tool_call_input.done", {**fields, "input": item["input"]})
            elif kind == "function_call":
                arguments = item["arguments"]
                emit("response.function_call_arguments.delta", {**fields, "delta": arguments})
                emit("response.function_call_arguments.done", {**fields, "name": item["name"], "arguments": arguments})
            emit("response.output_item.done", {"output_index": output_index, "item": item})

        terminal = "response.incomplete" if response.get("status") == "incomplete" else "response.completed"
        emit(terminal, {"response": response})
        self.end_chunked()

    def handle_messages(self, payload: dict[str, Any], model: str) -> int:
        """处理 Anthropic Messages。"""
        messages = claude_messages_to_chat(payload)
        if not messages:
            raise ValueError("messages is required")
        tools = normalize_chat_tools(payload.get("tools"))
        completion, error = call_cloudbase_chat(
            messages,
            model,
            tools=tools,
            tool_choice=normalize_tool_choice(payload.get("tool_choice")),
            temperature=payload.get("temperature"),
            top_p=payload.get("top_p"),
            max_tokens=payload.get("max_tokens"),
        )
        if error:
            self.send_protocol_error(error[0], "claude", error[1], error[2])
            return error[0]
        assert completion is not None
        response = completion_to_claude(completion, model)
        if not payload.get("stream"):
            self.send_json(200, response)
            return 200
        self.stream_claude(response)
        return 200

    def stream_claude(self, response: dict[str, Any]) -> None:
        """按 Anthropic Messages 事件协议合成 SSE。"""
        usage = response.get("usage") if isinstance(response.get("usage"), dict) else {}
        message = dict(response)
        message["content"] = []
        message["stop_reason"] = None
        message["stop_sequence"] = None
        message["usage"] = {**usage, "output_tokens": 0}
        self.begin_sse()
        self.write_sse_event("message_start", {"type": "message_start", "message": message})
        blocks = response.get("content") if isinstance(response.get("content"), list) else []
        for index, block in enumerate(blocks):
            if not isinstance(block, dict):
                continue
            block_type = block.get("type")
            if block_type == "text":
                text = str(block.get("text") or "")
                self.write_sse_event("content_block_start", {"type": "content_block_start", "index": index, "content_block": {"type": "text", "text": ""}})
                for piece in chunk_text(text):
                    if piece:
                        self.write_sse_event("content_block_delta", {"type": "content_block_delta", "index": index, "delta": {"type": "text_delta", "text": piece}})
                self.write_sse_event("content_block_stop", {"type": "content_block_stop", "index": index})
            elif block_type == "tool_use":
                arguments = json.dumps(block.get("input") or {}, ensure_ascii=False, separators=(",", ":"))
                self.write_sse_event("content_block_start", {"type": "content_block_start", "index": index, "content_block": {"type": "tool_use", "id": block.get("id"), "name": block.get("name"), "input": {}}})
                self.write_sse_event("content_block_delta", {"type": "content_block_delta", "index": index, "delta": {"type": "input_json_delta", "partial_json": arguments}})
                self.write_sse_event("content_block_stop", {"type": "content_block_stop", "index": index})
        self.write_sse_event("message_delta", {"type": "message_delta", "delta": {"stop_reason": response.get("stop_reason"), "stop_sequence": None}, "usage": {"output_tokens": safe_int(usage.get("output_tokens"))}})
        self.write_sse_event("message_stop", {"type": "message_stop"})
        self.end_chunked()

    def handle_count_tokens(self, payload: dict[str, Any]) -> None:
        """本地估算 Claude count_tokens，避免把旁路打到不存在的上游接口。"""
        text = json.dumps({key: payload.get(key) for key in ("system", "messages", "tools")}, ensure_ascii=False)
        self.send_json(200, {"input_tokens": estimate_tokens(text)})


def main() -> None:
    """启动只监听本机回环地址的三协议适配服务。"""
    if not UPSTREAM_BASE:
        raise SystemExit("UPSTREAM_BASE_URL is required")
    server = ThreadingHTTPServer((HOST, PORT), CloudBaseHandler)
    server.daemon_threads = True

    def shutdown(signum: int, _frame: Any) -> None:
        """收到退出信号后异步关闭 HTTP 服务。"""
        log(f"signal={signum} shutting_down")
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGINT, shutdown)
    signal.signal(signal.SIGTERM, shutdown)
    log(f"listening={HOST}:{PORT} upstream={UPSTREAM_BASE} protocols=chat,responses,claude max_concurrency={MAX_CONCURRENCY} timeout={REQUEST_TIMEOUT}")
    server.serve_forever(poll_interval=0.25)
    server.server_close()


if __name__ == "__main__":
    main()
