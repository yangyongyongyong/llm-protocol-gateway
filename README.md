# LLM Protocol Gateway

LLM Protocol Gateway 是一个面向 macOS 桌面应用形态的协议转换/透传网关原型。

## 解决的痛点

除了协议转换/透传，本项目重点解决使用自定义模型（尤其是 Cursor 等本地 Agent）时的两类控制缺失：

- **无法指定 thinking 深度**：Cursor 使用自定义模型时无法指定推理/思考（thinking）深度。本项目支持在 Provider 上配置**默认 thinking 深度**，转发时自动补齐，让自定义模型也能用上受控的思考深度。
- **模型名与 thinking 深度都可被网关接管**：模型名和 thinking 深度都支持两种模式：
  - **默认值**：请求未携带时，用 Provider/Route 配置的默认模型名、默认 thinking 深度兜底。
  - **强制覆盖**：可在 App 内强行指定模型名和 thinking 深度，**忽略请求体里携带的模型名和 thinking 深度**，统一以网关配置为准。

  即：模型和思考深度都既可以设默认值，也可以覆盖请求参数。

> 实现说明：thinking 深度映射到 OpenAI Chat 的 `reasoning_effort` 字段。Provider 可配置 `defaultThinkingDepth`，Route 可配置 `thinkingDepth` 及 `forceModel` / `forceThinkingDepth` 开关。当前该能力作用于 `OpenAI Chat -> OpenAI Chat` 透传路径；其余协议转换仍为占位。
>
> 生效优先级：
> - **模型名**：`ForceModel` 开启时以 Route 配置（ActiveModel → DefaultModel → Provider.DefaultModel）为准并忽略请求；关闭时保持 ActiveModel > 请求模型 > 默认值 的既有优先级。
> - **thinking 深度**：配置值取 `route.thinkingDepth`，为空则回退 `provider.defaultThinkingDepth`；`ForceThinkingDepth` 开启且配置非空时覆盖请求，否则请求已带则用请求值，都没有则用配置默认值兜底。

## 产品目标

- 上游 Provider 输入协议支持：
  - OpenAI Chat
  - OpenAI Responses
  - Claude
- 本地 Agent 输出协议支持：
  - OpenAI Chat
  - Claude
- 输入输出协议相同则透传。
- 输入输出协议不同则转换。
- 模型名与 thinking 深度支持"默认值 + 覆盖请求参数"两种模式（见上文痛点）。
- 可按需开启 Cloudflare 公网访问；未配置自有域名时走随机 Tunnel 域名，配置自有域名 + Tunnel Token 时以 token 方式运行命名隧道并暴露自有域名。
- 附加流量监控、token 监控、模型列表自动获取、菜单栏模型/Route 快速切换等能力。

## 当前 MVP

当前已完成：

- Go 后端骨架。
- React/Vite 前端骨架（侧边栏切换的多页面布局）。
- Provider、Route、Output Endpoint、Protocol、Model、Traffic Log 数据模型。
- 路由决策：协议相同 `pass_through`，协议不同 `convert`。
- OpenAI Chat 输出入口：
  - `GET /v1/models`
  - `POST /v1/chat/completions`（`OpenAI Chat -> OpenAI Chat` 真实转发，其余协议组合暂返回占位错误）。
- Provider 增删改查、克隆、可用性测试与模型列表自动获取。
- 配置持久化（Provider / Route / 模型 / 日志级别 / 公网设置写入本地 JSON，重启不丢失）。
- Cloudflare 公网访问：随机 quick tunnel + token 方式的自有域名命名隧道。
- 应用日志与 UI 内日志级别切换、流量请求日志。
- 前端协议网关 UI。

- Provider 默认 thinking 深度、模型名/thinking 深度的默认值兜底与强制覆盖（作用于 `OpenAI Chat -> OpenAI Chat` 透传路径）。

尚未实现：非 `OpenAI Chat -> OpenAI Chat` 的协议转换。

## Cloudflare 公网访问配置

默认关闭公网访问；开启后可在 UI 中选择随机 Cloudflare quick tunnel 域名，或填写已托管在 Cloudflare 的自有域名（例如 `gateway.lucadesign.uk`）。

- **随机模式（`random_tunnel`）**：运行 `cloudflared tunnel --url http://127.0.0.1:18093`，解析其输出中的 `trycloudflare.com` URL 作为公网地址，无需 Cloudflare 账号。
- **自有域名模式（`custom_domain`）**：采用 token 方式的命名隧道。需先在 Cloudflare Zero Trust 面板创建 Tunnel，为自有域名添加指向 `http://127.0.0.1:18093` 的 Public Hostname，拿到 Tunnel Token 后填入 App。启动时运行 `cloudflared tunnel run --token <token>`，隧道向边缘注册后即以自有域名对外暴露。未提供 token 时会返回明确的一次性设置指引，而不会伪造成功。
- Tunnel Token 属于敏感信息，当前按明文持久化在本地配置中（后续改进方向：接入 macOS Keychain）。

## 开发运行

推荐一键启动（会编译网关，并在缺少/过期时自动构建 `web/dist`）：

```bash
cd web
npm install
npm run dev
```

- 本地开发 UI：`http://127.0.0.1:5173`（Vite HMR）
- 网关 API / 公网隧道入口：`http://127.0.0.1:18093`（同时托管打包后的管理页）

仅起后端：

```bash
go run ./cmd/gateway
```

如需自定义端口：

```bash
GATEWAY_ADDR=127.0.0.1:18090 go run ./cmd/gateway
```

配置文件默认路径：`~/Library/Application Support/llm-protocol-gateway/config.json`（可用 `GATEWAY_CONFIG` 覆盖）。

### Cloudflare 公网访问前置

Cloudflare Tunnel 转发到网关的 `18093`，**不会**走 Vite `5173`。因此公网管理页（如 `https://user.example.com/login`）依赖网关内的 `web/dist`。

- 使用 `npm run dev` / `scripts/ensure-gateway.sh` / `scripts/run-gateway-service.sh` 时会自动构建 `web/dist`
- 若只跑 `go run ./cmd/gateway`，请先执行：

```bash
cd web && npm install && npm run build
```

否则公网 UI 域名会返回 404。

## 已知问题 / 踩坑记录

### 1. Claude 上游 thinking 尾块导致连环静默（已修复）

**现象**：长会话下，Codex 某一轮"想了一下"但完全不回复、`task_complete` 里 `last_agent_message` 为 `null`，之后每一轮都重复该现象，会话事实上卡死；严格一点的上游会直接 400（`messages.N: The final block in an assistant message cannot be 'thinking'.`），宽松的第三方中转则不报错但静默复读同样的空转。

**根因**：某一轮模型在 thinking 后被打断（无 text/tool_use），历史里就留了一条以裸 `thinking` 块结尾的 assistant 消息。下一轮网关把 Responses `reasoning` item 还原成 Claude `thinking` 块时，如果后面紧跟的是 user 消息（没有真实输出垫在中间），转换出来的请求就是"assistant 消息以 thinking 收尾"——这本身是非法请求，且模型看到裸 thinking 结尾的历史后，倾向于继续只输出 thinking，形成连环静默。

**修复**：
- `internal/gateway/convert_responses_claude.go` 的 `dropTrailingAssistantThinking`：转换后清理掉 assistant 消息末尾的裸 thinking/redacted_thinking 块；纯 thinking 的 assistant 轮整条丢弃并合并相邻同角色消息。尾部 thinking 模型不会回读，零上下文损失。
- `internal/gateway/thinking_rectifier.go`：签名整流重试的错误匹配增加了这个错误文案，命中同类 400 时也会走剥离重试。
- 增加了"上游 200 但输出仅有 thinking、无 text/tool_use"的检测（`isThinkingOnlyEmptyOutput`）：流式和非流式转换都会在判定为该情况时自动同 Provider 重试一次（`timing_flags` 里能看到 `thinking_only_retry`），重试仍失败才把原样结果透传给客户端。

### 2. Codex `remote_compaction_v2` 在自定义/第三方 Provider 下无法保证成功（未修复，非本项目缺陷）

**现象**：长会话触发 Codex 的远程上下文压缩时，客户端报 `Error running remote compact task: Fatal error: remote compaction v2 expected exactly one compaction output item, got 0 from N output items`。网关侧这次请求往往是正常的 200，`timing_flags` 也没有异常标记——即响应本身不是空的，只是形状不满足 Codex 的要求。

**根因**：Codex 的 remote compaction 校验器要求响应里**恰好 1 个**它认可的输出项；但带 thinking 的模型天然会把回答拆成 `reasoning` + `message` 两个 output item，天然凑不齐"恰好 1 个"。这是 Codex 客户端侧未公开的强校验，属于所有自定义/第三方 Provider 的通用缺陷，不是本网关的转换 bug——参考 [`farion1231/cc-switch`](https://github.com/farion1231/cc-switch) 的 [#4030](https://github.com/farion1231/cc-switch/issues/4030)、[#4725](https://github.com/farion1231/cc-switch/issues/4725) 等多个 issue，MiniMax / DeepSeek 等原生支持 `/v1/responses` 的官方 Provider 也同样复现，社区折腾数月未能在代理层可靠绕过。

**应对方式**：关掉 Codex 的 `remote_compaction_v2` 特性，让 Codex 走本地（客户端侧）压缩，绕开这条对输出形状有强要求的远程链路：

```bash
codex features disable remote_compaction_v2
```

本项目在生成 Codex 配置时已经默认带上这一步（见 `web/src/main.tsx` 的 `buildApiKeyCodexConfigPatchScript`）：
- **"复制修改脚本"**：写完 provider 配置后会自动尝试执行上述命令（找不到 `codex` 命令或执行失败只打印提示，不影响 provider 配置本身写入）。
- **"仅复制配置内容"**：因为是纯文本、无法执行命令，只在文件头部加了一行注释提醒手动执行。
- **"还原为官方 provider"**：对称地会执行 `codex features enable remote_compaction_v2` 恢复默认值（官方 Provider 下一般没有这个兼容性问题）。

若手动配置 Codex（未走本项目生成的脚本），需要自己执行一次上述命令；改动后需重启 Codex 桌面 App 才会生效。

## 验证接口

```bash
curl http://127.0.0.1:18093/__health
curl http://127.0.0.1:18093/__state
curl http://127.0.0.1:18093/v1/models
```

Chat 转发：

```bash
curl -s http://127.0.0.1:18093/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}'
```
