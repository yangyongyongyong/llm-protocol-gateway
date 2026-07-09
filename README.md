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
