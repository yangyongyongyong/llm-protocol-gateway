# 腾讯云 CloudBase 成长计划自定义连接器

这个连接器把腾讯云 CloudBase「小程序成长计划」的模型额度接到 LLM Protocol Gateway。
它不是新增的网关协议：网关仍只看到标准协议，CloudBase 专属逻辑全部封装在
本地 ProviderScript 和 CloudBase 云函数 `aiProxy` 中。

## 为什么不直连

CloudBase 的成长计划额度只允许小程序 SDK / 云开发 SDK 通道。使用 API Key 直接请求：

```text
https://<ENV_ID>.api.tcloudbasegateway.com/v1/ai/cloudbase/chat/completions
https://<ENV_ID>.api.tcloudbasegateway.com/v1/ai/cloudbase/responses
https://<ENV_ID>.api.tcloudbasegateway.com/v1/ai/cloudbase/v1/messages
```

都会返回：

```json
{
  "code": "AI_CHANNEL_NOT_ALLOWED",
  "message": "小程序成长计划仅支持小程序 SDK 和云开发 SDK 调用。"
}
```

因此连接器采用：

```text
Agent → Gateway → 本地 adapter.py → aiProxy 云函数 → @cloudbase/node-sdk → 模型
```

云函数的公网入口只返回一次性 JSON，adapter 会在客户端要求流式时按对应协议合成 SSE。

## 原生协议入口

本地 adapter 对网关暴露三种标准协议，最终都复用同一条云函数通道：

| 协议 | 本地入口 | 说明 |
| --- | --- | --- |
| OpenAI Chat Completions | `POST /v1/chat/completions` | 文本、SSE、工具调用 |
| OpenAI Responses | `POST /v1/responses` | 文本、SSE、function_call；支持本地缓存的 `previous_response_id` |
| Anthropic Messages | `POST /v1/messages` | 文本、SSE、tool_use / tool_result；本地提供 `count_tokens` 估算 |

网关创建 Route 时把输出协议设为 `Claude` 或 `OpenAI Responses`，并选择这个 Provider，
就会优先走对应原生入口。后台协议探测会通过 `/v1/responses` 和 `/v1/messages` 的
非 404 响应识别额外协议，Provider 卡片会显示 `+Claude` / `+Responses`。

## 部署云函数

当前环境 ID 已写在 `cloud-function/cloudbaserc.json`：

```bash
tcb login --cloudbase-api-key "$CLOUDBASE_API_KEY" -e dev-d5gze50axaadb179e
cd connectors/tencent-cloudbase-growth/cloud-function
npx -y -p @cloudbase/cli@latest tcb fn deploy aiProxy \
  --dir . --force --yes \
  -e dev-d5gze50axaadb179e \
  -r ap-shanghai
```

云函数使用 Node.js 20.19、`@cloudbase/node-sdk`，并支持：

- `{"action":"ping"}` 健康检查，不消耗模型额度
- OpenAI Chat 请求字段 `model` / `messages` / `tools` / `tool_choice`
- `providerHints`，默认对混元模型先试 `hunyuan-v3`，再试 `cloudbase`
- `AI_PROXY_ALLOWED_MODELS` 模型白名单（仅显式设置时生效，默认不限制模型）
- `AI_PROXY_PROVIDERS` provider 顺序覆盖
- `AI_PROXY_MAX_STEPS` / `event.maxSteps` 步数上限（默认不限制）

## 导入连接器

在网关控制台「添加自定义连接器 → 导入连接器」选择：

```text
tencent-cloudbase-growth.custom-connector.json
```

创建实例时填写：

| 字段 | 值 |
| --- | --- |
| 名称 | 腾讯云 CloudBase AI |
| 上游地址 | `https://<ENV_ID>.api.tcloudbasegateway.com/v1/functions/aiProxy` |
| API Key | CloudBase 服务端 API Key |

## 已验证

- `aiProxy` ping 返回 200
- Chat：`hy3`、`hy4-flash`、`deepseek-v4.1-flash` 均经云函数调用成功，`usage` 正常返回
- Responses：非流式响应、标准事件流、工具调用输出
- Claude：非流式响应、标准事件流、工具调用输出
- `previous_response_id` 可在 adapter 进程存活期间续接历史
- 成长计划额度面板会随着真实调用增加已用 Token

## 限制

- adapter 兼容 `Content-Length` 与 `Transfer-Encoding: chunked` 请求体；不对消息条数、消息长度或请求体大小做数量拦截，由上游/云函数自身决定可接受范围。
- 云函数 HTTP 入口不支持真流式；客户端看到的是 adapter 按协议合成的 SSE，首字延迟约等于完整生成时间。
- `@cloudbase/ai` 的 `generateText` 当前没有可靠的 `max_tokens` 入参，网关传入的输出上限不会严格限制云端生成长度；云函数 120 秒超时是最终硬边界。
- Responses 的 `previous_response_id` 只保存在 adapter 内存中，TTL 24 小时、最多 128 条；adapter 重启后需要客户端重新发送完整历史。
- 图片输入目前只保留文本占位；工具结果里的图片不会完整还原成多模态内容。
- 工具调用、Responses 状态语义和 Claude cache 字段是兼容层实现，不等同于 CloudBase 官方直连的完整云端语义。
- 云函数默认 120 秒超时，超长输出仍可能被截断。
- 成长计划额度有有效期；到期后需切换资源点套餐或把云函数 provider 改为 `cloudbase`。
