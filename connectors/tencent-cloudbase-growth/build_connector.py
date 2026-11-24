#!/usr/bin/env python3
"""从 adapter.py 生成可直接导入网关的 Custom Connector JSON。"""
from __future__ import annotations

import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent
ADAPTER = ROOT / "adapter.py"
OUTPUT = ROOT / "tencent-cloudbase-growth.custom-connector.json"

connector = {
    "kind": "llm-protocol-gateway/custom-connector",
    "version": 1,
    "customProvider": {
        "id": "tencent-cloudbase-growth",
        "name": "腾讯云 CloudBase 成长计划",
        "description": "CloudBase 小程序成长计划：原生 Chat / Responses / Claude 三协议入口，经 aiProxy 云函数和 Node SDK 调用，流式事件由本地适配器按协议合成。",
        "protocol": "openai_chat",
        "authHeader": "Authorization",
        "defaultModel": "hy3",
        "script": {
            "language": "python",
            "entrypoint": "adapter.py",
            "source": ADAPTER.read_text(encoding="utf-8"),
            "healthPath": "/health",
            "maxConcurrentRequests": 4,
            "requestTimeoutSec": 150,
        },
    },
}

OUTPUT.write_text(json.dumps(connector, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
print(OUTPUT)
