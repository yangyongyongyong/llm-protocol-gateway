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

import json
import os
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
}


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


def normalize_completion(data: dict[str, Any], model: str) -> dict[str, Any]:
    """补全云函数返回的 OpenAI Chat completion，容忍外层 result 包装。"""
    if isinstance(data.get("result"), dict):
        data = data["result"]
    if isinstance(data.get("error"), dict):
        return data
    choices = data.get("choices")
    if not isinstance(choices, list) or not choices:
        message = data.get("text") if isinstance(data.get("text"), str) else ""
        data = {
            "id": data.get("id") or new_id("chatcmpl-cloudbase-"),
            "object": "chat.completion",
            "created": data.get("created") or int(time.time()),
            "model": data.get("model") or model,
            "choices": [{"index": 0, "message": {"role": "assistant", "content": message}, "finish_reason": "stop"}],
            "usage": data.get("usage") or {},
        }
    data["model"] = data.get("model") or model
    return data


def completion_details(completion: dict[str, Any]) -> tuple[str, list[dict[str, Any]], str, dict[str, Any]]:
    """从 Chat completion 提取文本、工具调用、结束原因和 usage。"""
    choice = (completion.get("choices") or [{}])[0] or {}
    message = choice.get("message") or {}
    text = content_text(message.get("content"))
    tool_calls: list[dict[str, Any]] = []
    for index, item in enumerate(message.get("tool_calls") or []):
        if not isinstance(item, dict):
            continue
        function = item.get("function") or {}
        name = str(function.get("name") or "").strip()
        if not name:
            continue
        arguments = function.get("arguments")
        if not isinstance(arguments, str):
            arguments = json.dumps(arguments or {}, ensure_ascii=False, separators=(",", ":"))
        tool_calls.append({
            "id": str(item.get("id") or f"call_{int(time.time() * 1000)}_{index}"),
            "type": "function",
            "function": {"name": name, "arguments": arguments},
        })
    finish_reason = str(choice.get("finish_reason") or ("tool_calls" if tool_calls else "stop"))
    usage = completion.get("usage") if isinstance(completion.get("usage"), dict) else {}
    return text, tool_calls, finish_reason, usage


def usage_numbers(usage: dict[str, Any]) -> tuple[int, int, int]:
    """把不同协议的 usage 字段归一成输入、输出、总 token。"""
    prompt = safe_int(usage.get("prompt_tokens") or usage.get("input_tokens"))
    output = safe_int(usage.get("completion_tokens") or usage.get("output_tokens"))
    total = safe_int(usage.get("total_tokens"), prompt + output)
    return prompt, output, total


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
    completion = normalize_completion(data, model)
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


def remember_response(response_id: str, messages: list[dict[str, Any]]) -> None:
    """保存 Responses 的上一轮消息，供 previous_response_id 续接。"""
    now = time.time()
    with RESPONSE_STORE_LOCK:
        expired = [key for key, value in RESPONSE_STORE.items() if now - float(value.get("at", 0)) > RESPONSE_STORE_TTL_SEC]
        for key in expired:
            RESPONSE_STORE.pop(key, None)
        RESPONSE_STORE[response_id] = {"messages": messages, "at": now}
        while len(RESPONSE_STORE) > RESPONSE_STORE_LIMIT:
            RESPONSE_STORE.pop(next(iter(RESPONSE_STORE)))


def recall_response(response_id: str) -> list[dict[str, Any]]:
    """读取 previous_response_id 对应的历史消息；过期或不存在时返回空列表。"""
    if not response_id:
        return []
    with RESPONSE_STORE_LOCK:
        item = RESPONSE_STORE.get(response_id)
        if not item:
            return []
        if time.time() - float(item.get("at", 0)) > RESPONSE_STORE_TTL_SEC:
            RESPONSE_STORE.pop(response_id, None)
            return []
        return [dict(message) for message in item.get("messages", []) if isinstance(message, dict)]


def responses_input_to_chat(payload: dict[str, Any]) -> list[dict[str, Any]]:
    """把 OpenAI Responses 的 input 转成 Chat messages。"""
    messages = recall_response(str(payload.get("previous_response_id") or ""))
    instructions = content_text(payload.get("instructions"))
    if instructions:
        messages.append({"role": "system", "content": instructions})

    raw_input = payload.get("input")
    if isinstance(raw_input, str):
        messages.append({"role": "user", "content": raw_input})
        return messages
    if isinstance(raw_input, dict):
        raw_input = [raw_input]
    if not isinstance(raw_input, list):
        return messages

    pending_calls: list[dict[str, Any]] = []

    def flush_calls() -> None:
        """把连续出现的 Responses function_call 合并成一条 assistant 消息。"""
        nonlocal pending_calls
        if pending_calls:
            messages.append({"role": "assistant", "tool_calls": pending_calls})
            pending_calls = []

    for item in raw_input:
        if not isinstance(item, dict):
            continue
        item_type = str(item.get("type") or "")
        if item_type in ("function_call", "custom_tool_call"):
            call_id = str(item.get("call_id") or item.get("id") or new_id("call_"))
            name = str(item.get("name") or "")
            arguments = item.get("arguments")
            if not isinstance(arguments, str):
                arguments = json.dumps(arguments or {}, ensure_ascii=False, separators=(",", ":"))
            pending_calls.append({
                "id": call_id,
                "type": "function",
                "function": {"name": name, "arguments": arguments},
            })
            continue
        if item_type in ("function_call_output", "custom_tool_call_output"):
            flush_calls()
            messages.append({
                "role": "tool",
                "tool_call_id": str(item.get("call_id") or item.get("id") or ""),
                "content": tool_output_text(item.get("output")),
            })
            continue
        if item_type in ("message", "") and item.get("role"):
            flush_calls()
            role = "system" if item.get("role") == "developer" else str(item.get("role"))
            messages.append({"role": role, "content": content_text(item.get("content"))})
    flush_calls()
    return messages


def chat_message_from_completion(completion: dict[str, Any]) -> dict[str, Any]:
    """把 Chat completion 的 assistant message 还原成可再次发送的 Chat message。"""
    text, tool_calls, _, _ = completion_details(completion)
    message: dict[str, Any] = {"role": "assistant"}
    if text:
        message["content"] = text
    if tool_calls:
        message["tool_calls"] = tool_calls
    if not text and not tool_calls:
        message["content"] = ""
    return message


def response_output_items(text: str, tool_calls: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """把 Chat completion 转成 Responses output 数组。"""
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
        function = call.get("function") or {}
        output.append({
            "id": new_id("fc_"),
            "type": "function_call",
            "status": "completed",
            "call_id": call.get("id") or new_id("call_"),
            "name": function.get("name") or "",
            "arguments": function.get("arguments") or "{}",
        })
    return output


def completion_to_response(
    completion: dict[str, Any],
    model: str,
    payload: dict[str, Any],
    conversation_messages: list[dict[str, Any]],
) -> dict[str, Any]:
    """把 Chat completion 包装成 OpenAI Responses 响应。"""
    text, tool_calls, finish_reason, usage = completion_details(completion)
    prompt, output_tokens, total_tokens = usage_numbers(usage)
    response_id = new_id("resp_")
    status = "incomplete" if finish_reason in ("length", "max_tokens") else "completed"
    response = {
        "id": response_id,
        "object": "response",
        "created_at": int(time.time()),
        "status": status,
        "error": None,
        "incomplete_details": {"reason": "max_output_tokens"} if status == "incomplete" else None,
        "instructions": payload.get("instructions"),
        "max_output_tokens": payload.get("max_output_tokens"),
        "model": model,
        "output": response_output_items(text, tool_calls),
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
            "input_tokens_details": {"cached_tokens": safe_int(usage.get("cache_read_input_tokens"))},
            "output_tokens": output_tokens,
            "output_tokens_details": {"reasoning_tokens": safe_int(usage.get("reasoning_tokens"))},
            "total_tokens": total_tokens,
        },
        "output_text": text,
    }
    if bool(payload.get("store", True)):
        remember_response(response_id, conversation_messages + [chat_message_from_completion(completion)])
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
    content: list[dict[str, Any]] = []
    if text:
        content.append({"type": "text", "text": text})
    for call in tool_calls:
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
    stop_reason = "tool_use" if tool_calls else ("max_tokens" if finish_reason in ("length", "max_tokens") else "end_turn")
    result = {
        "id": new_id("msg_"),
        "type": "message",
        "role": "assistant",
        "model": model,
        "content": content,
        "stop_reason": stop_reason,
        "stop_sequence": None,
        "usage": {"input_tokens": prompt, "output_tokens": output_tokens},
    }
    cache_read = safe_int(usage.get("cache_read_input_tokens"))
    cache_create = safe_int(usage.get("cache_creation_input_tokens"))
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
        messages = responses_input_to_chat(payload)
        if not messages:
            raise ValueError("input is required")
        tools = normalize_chat_tools(payload.get("tools"))
        completion, error = call_cloudbase_chat(
            messages,
            model,
            tools=tools,
            tool_choice=normalize_tool_choice(payload.get("tool_choice")),
            temperature=payload.get("temperature"),
            top_p=payload.get("top_p"),
            max_tokens=payload.get("max_output_tokens"),
        )
        if error:
            self.send_protocol_error(error[0], "responses", error[1], error[2])
            return error[0]
        assert completion is not None
        response = completion_to_response(completion, model, payload, messages)
        if not payload.get("stream"):
            self.send_json(200, response)
            return 200
        self.begin_sse()
        self.stream_responses(response)
        return 200

    def stream_responses(self, response: dict[str, Any]) -> None:
        """按 Responses 事件协议合成 SSE。"""
        text = str(response.get("output_text") or "")
        output = response.get("output") if isinstance(response.get("output"), list) else []
        message_items = [item for item in output if isinstance(item, dict) and item.get("type") == "message"]
        call_items = [item for item in output if isinstance(item, dict) and item.get("type") == "function_call"]
        sequence = 0

        def emit(name: str, payload: dict[str, Any]) -> None:
            nonlocal sequence
            body = {"type": name, "sequence_number": sequence, **payload}
            self.write_sse_event(name, body)
            sequence += 1

        created = dict(response)
        created["status"] = "in_progress"
        created["output"] = []
        created["output_text"] = ""
        emit("response.created", {"response": created})
        emit("response.in_progress", {"response": created})

        output_index = 0
        if text and message_items:
            item = message_items[0]
            part = {"type": "output_text", "text": "", "annotations": []}
            added_item = dict(item)
            added_item["content"] = []
            added_item["status"] = "in_progress"
            emit("response.output_item.added", {"output_index": output_index, "item": added_item})
            emit("response.content_part.added", {"item_id": item.get("id"), "output_index": output_index, "content_index": 0, "part": part})
            for piece in chunk_text(text):
                if piece:
                    emit("response.output_text.delta", {"item_id": item.get("id"), "output_index": output_index, "content_index": 0, "delta": piece, "logprobs": []})
            emit("response.output_text.done", {"item_id": item.get("id"), "output_index": output_index, "content_index": 0, "text": text, "logprobs": []})
            emit("response.content_part.done", {"item_id": item.get("id"), "output_index": output_index, "content_index": 0, "part": {"type": "output_text", "text": text, "annotations": []}})
            emit("response.output_item.done", {"output_index": output_index, "item": item})
            output_index += 1

        for item in call_items:
            arguments = str(item.get("arguments") or "{}")
            added = dict(item)
            added["arguments"] = ""
            added["status"] = "in_progress"
            emit("response.output_item.added", {"output_index": output_index, "item": added})
            emit("response.function_call_arguments.delta", {"item_id": item.get("id"), "output_index": output_index, "delta": arguments})
            emit("response.function_call_arguments.done", {"item_id": item.get("id"), "output_index": output_index, "arguments": arguments})
            emit("response.output_item.done", {"output_index": output_index, "item": item})
            output_index += 1

        emit("response.completed", {"response": response})
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
        message["usage"] = {"input_tokens": safe_int(usage.get("input_tokens")), "output_tokens": 0}
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
