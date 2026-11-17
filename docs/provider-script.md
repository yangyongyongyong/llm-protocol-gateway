# 自定义协议 Provider 脚本

Provider 页面支持两层自定义连接器：

1. **自定义连接器**（例如 `ocean`）：管理员定义一次标准协议、鉴权头、
   并发/超时和转换脚本；
2. **Provider 实例**：在“添加输入 Provider”目录里选择 `ocean`，只填写该实例的
   上游 URL 和 Key。

脚本负责把厂商私有协议转换成网关已有的三种标准协议之一，实例仍按
`openai_chat` / `openai_responses` / `claude` 工作。这样 Ocean、WorkBuddy 或
其他私有协议不会持续污染主协议转换流程。

边界必须保持清晰：主 Go 代码只负责通用协议转换、Provider 调度和脚本进程
生命周期，不识别任何厂商域名，也不实现某一家私有参数。以下逻辑都属于
`adapter.py`：

- 厂商路径、Azure deployment、鉴权头和模型名展开；
- 私有字段与三家标准协议的字段映射；
- `tool_choice`、`tools`、`tool_calls`、`tool` 消息的厂商级校验与修补；
- reasoning 档位、最大 token、工具 schema 等厂商特有约束；
- 厂商错误码到标准 HTTP/JSON 错误的映射。

新增私有协议时不应修改 `internal/gateway` 的协议转换主流程。主流程只需要
通过自定义连接器调用脚本，脚本对外返回 OpenAI Chat / OpenAI
Responses / Claude 之一。

## 使用流程

1. 在 Provider 页面点击“添加输入 Provider”。
2. 在目录的“自定义连接器”分区点击“＋ 添加自定义连接器”。
3. 弹窗只提供一段完整的 AI Prompt，不提供手填表单；点击“复制给 AI”。
4. AI 通过 `GET/POST/PATCH/DELETE /__custom-providers` 查询、新建、编辑、删除连接器，并调用脚本校验、实例创建和测试 API 完成验收。
5. 连接器创建完成后，目录会自动出现对应磁贴；选择磁贴只填写该实例的上游 URL 和 Key。
6. 连接器磁贴右侧三点菜单提供“导出连接器”和“删除连接器”；编辑连接器统一交给 AI/API。
7. 保存实例后网关会启动脚本、做健康检查，并自动请求 `/v1/models` 回填模型目录。

Provider 实例对客户端暴露的 Base URL 自动设为：

```text
http://127.0.0.1:<derived-port>/v1
```

私有厂商地址只通过 `UPSTREAM_BASE_URL` 注入脚本，不写进实例的公开 Base URL。
连接器更新后会同步刷新所有引用它的实例，并重启对应脚本进程；不需要重启主网关。
脚本进程的启动、停止和重启用下面的 Provider 实例接口完成：

```text
POST /__providers/{id}/script/start
POST /__providers/{id}/script/restart
POST /__providers/{id}/script/stop
GET  /__providers/{id}/script/status
GET  /__providers/{id}/script/logs
```

因此升级某个厂商适配器时，只更新自定义连接器脚本并重启对应实例，正在运行的
网关和其他 Provider 不受影响。

## 脚本落盘位置

连接器脚本不只保存在数据库里，还会写入 App 的用户数据目录：

```text
~/Library/Application Support/llm-protocol-gateway/custom-providers/<type-id>/adapter.py
~/Library/Application Support/llm-protocol-gateway/custom-providers/<type-id>/requirements.txt
```

运行时每个 Provider 实例的临时适配目录位于：

```text
~/Library/Application Support/llm-protocol-gateway/provider-runtime/<provider-id>/
```

可用 `GATEWAY_CUSTOM_PROVIDER_DIR` 覆盖类型脚本根目录。脚本文件会以 `0600`
权限写入，App 启动时按数据库中的类型模板自动补齐。

## 必需参数

网关启动脚本时注入以下环境变量：

| 变量 | 说明 |
|---|---|
| `PROVIDER_SCRIPT_PORT` | 必须监听的 loopback 端口；脚本不得自行改端口 |
| `PROVIDER_SCRIPT_PROVIDER_ID` | Provider ID |
| `PROVIDER_SCRIPT_PROTOCOL` | `openai_chat` / `openai_responses` / `claude` |
| `PROVIDER_SCRIPT_HEALTH_PATH` | 健康检查路径，默认 `/health` |
| `PROVIDER_SCRIPT_MAX_CONCURRENCY` | 网关并发上限，`0` 表示未配置 |
| `PROVIDER_SCRIPT_REQUEST_TIMEOUT_SEC` | 单次请求超时，`0` 表示未配置 |
| `PROVIDER_SCRIPT_ENTRYPOINT` | 入口文件名，通常 `adapter.py` |
| `UPSTREAM_BASE_URL` | 私有厂商协议根地址 |
| `UPSTREAM_API_KEY` | 私有厂商 Key，可能为空 |
| `UPSTREAM_AUTH_HEADER` | 上游鉴权头名，默认 `Authorization` |

鉴权约定：

- `UPSTREAM_AUTH_HEADER=Authorization` 时，若 Key 没有 `Bearer ` 前缀，脚本应补上。
- `UPSTREAM_AUTH_HEADER=x-api-key` 等头名时发送原始 Key。
- Key 不得写入源码、日志、异常或响应。

运行时可执行文件默认是 PATH 中的 `python3`。部署环境需要固定解释器时，可设置：

```text
GATEWAY_PROVIDER_SCRIPT_PYTHON=/absolute/path/to/python3
```

## 必须实现的 HTTP 接口

```text
GET  /health
GET  /v1/models
POST /v1/chat/completions     Provider.Protocol=openai_chat
POST /v1/responses            Provider.Protocol=openai_responses
POST /v1/messages             Provider.Protocol=claude
```

至少实现与 Provider 选定协议对应的一个推理入口。最省事的选择是
`openai_chat`：网关会让现有转换器承接 Responses / Claude 客户端到 Chat 的转换。

`GET /v1/models` 返回 OpenAI 风格目录：

```json
{"data":[{"id":"model-id","context_length":200000}]}
```

脚本必须只监听 `127.0.0.1:PROVIDER_SCRIPT_PORT`，不得监听公网。

## 并发、超时与取消

- `PROVIDER_SCRIPT_MAX_CONCURRENCY > 0` 时，脚本内部必须用信号量或队列限制
  同时在飞的上游请求；网关调度层也会取同一个上限，两层都会生效。
- 达到上限时可以短暂排队；超过 `PROVIDER_SCRIPT_REQUEST_TIMEOUT_SEC` 或无法
  排队时应返回 `429`，带 JSON error 和 `Retry-After`，不能无限堆积。
- 客户端断开或网关取消时，脚本应停止读取上游、关闭连接并释放并发槽位。
- 不要用一个全局锁串行化全部请求；健康检查不应被长推理阻塞。
- 请求超时同时约束连接、首包和总等待；`0` 表示脚本设置合理默认值，不是无限等待。

## 流式与协议细节

### OpenAI Chat

- `stream=true` 返回 `Content-Type: text/event-stream`，逐帧 flush。
- 文本增量放在 `choices[0].delta.content`。
- 工具调用放在 `choices[0].delta.tool_calls`，`index` 稳定，
  `function.arguments` 可分帧拼接。
- 最后发送带 `finish_reason` 的 chunk，然后发送 `data: [DONE]` 和空行。

### OpenAI Responses

- 必须输出 Responses 事件形状，不能把 Chat chunk 冒充 Responses。
- 至少正确处理 `response.created`、文本/工具增量、`response.completed`。

### Claude Messages

- 至少正确处理 `message_start`、`content_block_start`、`content_block_delta`、
  `content_block_stop`、`message_delta`、`message_stop`。
- Claude 流以 `message_stop` 结束，不要伪造额外的 `data: [DONE]`。

### 通用要求

- 正确处理 `tools`、`tool_choice`、tool result、并行工具调用、reasoning 字段。
- 非流式响应必须是完整标准 JSON，不能只返回厂商私有结构。
- 上游 400/401/403/404/429/5xx 保留状态码、JSON 错误和 `Retry-After`。
- 网络超时映射 `504`，连接失败映射 `502`，不能静默改成 `200`。
- usage 应尽量返回：
  - OpenAI Chat：`prompt_tokens` / `completion_tokens` / `total_tokens`；
  - Responses：`input_tokens` / `output_tokens`；
  - Claude：`input_tokens` / `output_tokens`；
  - 缓存字段：`cached_tokens` / `cache_read_input_tokens` 等。
- 没有缓存字段时省略，不要编造 0。

## 校验、模型发现与测试

“校验脚本”接口只做静态检查：

- 必要参数、入口文件名、URL、并发/超时范围；
- Provider 协议与脚本端点提示；
- `python3 -m py_compile` 语法检查；
- 不会导入或执行脚本，不会消耗推理额度。

保存后网关会：

1. 启动独立脚本进程；
2. 轮询 `/health`，默认最多 15 秒；
3. 请求 `/v1/models`，成功后回填 `DefaultModel` 和模型目录；
4. 后台探测协议路径；
5. 在“测试并发现模型”里再发一次最小 Chat 请求，默认只要求回复
   `script-ok`。

如果 `/v1/models` 不可枚举，可在 Provider 表单里手动填写默认模型；测试接口
会使用该模型，不会用空 model 发请求。

## 连接器与实例更新语义

自定义连接器使用：

```text
GET    /__custom-providers
GET    /__custom-providers/ai-prompt
POST   /__custom-providers
GET    /__custom-providers/{id}/export
POST   /__custom-providers/import
PATCH  /__custom-providers/{id}
DELETE /__custom-providers/{id}
```

- 连接器更新会同步所有引用实例，保留各实例自己的 URL、Key、模型和分组；
- 仍有实例引用时禁止删除连接器；
- 导出连接器只包含模板和脚本，不包含任何实例 URL、Key、OAuth token 或用量数据；
- 导入连接器默认不覆盖同 ID 连接器；需要覆盖时调用 `POST /__custom-providers/import?replace=true`；
- 普通用户可以在目录中看到连接器并创建实例，但只有管理员能查看/修改脚本正文。
- “添加自定义连接器”弹窗只展示 AI Prompt；连接器的新增和编辑通过 API 完成。
- AI Prompt 包含临时测试密钥和至少一种标准协议真实验收要求：
  - 创建临时 Provider、Route、API Key；
  - 通过 Chat / Responses / Claude 中至少一种真实入口返回 2xx 有效响应；
  - 不要求脚本声明的所有协议都通过；
  - 验收后删除临时 Key、Route 和 Provider。

`PATCH /__providers/{id}`：

- 自定义连接器实例提交 `customProviderId` + `baseUrl` + `apiKeySource`，协议和
  脚本由连接器模板解析；
- 上游 URL、Key 或连接器模板变化时，网关会停止旧进程、启动新进程并重新发现模型；
- `providerScript: null`：停止脚本并清空脚本配置；同一 PATCH 必须带一个真实的
  普通上游 `baseUrl`，否则返回 400，避免留下指向 `127.0.0.1` 的死 Provider。

## API

```text
GET  /__provider-script/abi
POST /__provider-script/validate
GET  /__custom-providers
POST /__custom-providers
PATCH /__custom-providers/{id}
DELETE /__custom-providers/{id}
POST /__providers/{id}/script/start
POST /__providers/{id}/script/restart
POST /__providers/{id}/script/stop
GET  /__providers/{id}/script/status
GET  /__providers/{id}/script/logs
POST /__providers/{id}/script/test
```

Provider 增删改查继续复用普通接口：

```text
POST   /__providers
PATCH  /__providers/{id}
GET    /__providers/{id}/test
DELETE /__providers/{id}
DELETE /__providers/{id}/purge
```

`GET /__provider-script/abi` 同时返回 `version`、`abi`、`examplePrompt`、
`requiredEnv` 和 `protocols`，外部 AI/自动化应以它为唯一契约来源。

创建自定义连接器：

```json
{
  "name": "ocean",
  "description": "Ocean Dev Azure deployment adapter",
  "protocol": "openai_chat",
  "authHeader": "api-key",
  "defaultModel": "gpt-4o",
  "script": {
    "language": "python",
    "entrypoint": "adapter.py",
    "source": "...adapter.py contents...",
    "maxConcurrentRequests": 4,
    "requestTimeoutSec": 120
  }
}
```

创建实例时只需：

```json
{
  "name": "Ocean dev",
  "customProviderId": "ocean",
  "baseUrl": "http://ocean-dev.tuya-in.net",
  "apiKeySource": "literal:vendor-key"
}
```

## curl 示例

校验：

```bash
curl -sS -X POST http://127.0.0.1:18093/__provider-script/validate \
  -H 'Content-Type: application/json' \
  -d '{"protocol":"openai_chat","script":{"language":"python","entrypoint":"adapter.py","source":"print(1)","upstreamBaseUrl":"https://vendor.example","maxConcurrentRequests":4,"requestTimeoutSec":120}}'
```

创建自定义连接器 `ocean`：

```bash
curl -sS -X POST http://127.0.0.1:18093/__custom-providers \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "ocean",
    "protocol": "openai_chat",
    "authHeader": "api-key",
    "defaultModel": "gpt-4o",
    "script": {
      "language": "python",
      "entrypoint": "adapter.py",
      "source": "...adapter.py contents...",
      "maxConcurrentRequests": 4,
      "requestTimeoutSec": 120
    }
  }'
```

之后创建实例：

```bash
curl -sS -X POST http://127.0.0.1:18093/__providers \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "Ocean dev",
    "customProviderId": "ocean",
    "baseUrl": "http://ocean-dev.tuya-in.net",
    "apiKeySource": "literal:vendor-key",
    "defaultModel": "gpt-4o"
  }'
```

启动、重启、测试、查看日志：

```bash
curl -sS -X POST http://127.0.0.1:18093/__providers/<id>/script/start
curl -sS -X POST http://127.0.0.1:18093/__providers/<id>/script/restart
curl -sS -X POST http://127.0.0.1:18093/__providers/<id>/script/test
curl -sS http://127.0.0.1:18093/__providers/<id>/script/status
curl -sS http://127.0.0.1:18093/__providers/<id>/script/logs
```

清空脚本并改回普通 Provider：

```bash
curl -sS -X PATCH http://127.0.0.1:18093/__providers/<id> \
  -H 'Content-Type: application/json' \
  -d '{"baseUrl":"https://api.example.com/v1","providerScript":null}'
```

## 安全边界

脚本是用户提供的本地进程，不是安全沙箱。它可以访问当前用户可读的文件、本机
网络和注入的环境变量，也能看到 `UPSTREAM_API_KEY`。因此：

- 不要在生产共享主机上运行不可信脚本；
- 不要让脚本打印或外传 Key；
- 正式托管时应考虑容器、资源限制、只读文件系统和网络策略；
- 当前版本不会自动执行 `pip install -r requirements.txt`，优先使用 Python
  标准库；需要第三方依赖时请提前在运行环境安装。
