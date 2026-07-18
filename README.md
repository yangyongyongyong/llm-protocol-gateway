# LLM Protocol Gateway

**一个 macOS 桌面网关：在 OpenAI Chat / OpenAI Responses / Claude 三种协议之间自由互转与透传，
把本地/第三方/官方账号统一收敛成一个可对外访问的 OpenAI 或 Claude 端点。**

面向 Cursor、Codex 等自定义模型客户端，解决"协议对不上、无法跨网络访问、无法控制模型名与
思考深度、单一上游不稳定"等一系列实际痛点。

---

## ✨ 核心特性

### 1. 三协议互转
OpenAI Chat ⇄ OpenAI Responses ⇄ Claude 任意组合：协议相同则透传，协议不同则做**点对点直转**
（不经有损中间层，完整保留工具调用、流式、以及 Claude 的签名 thinking 块）。客户端说什么协议、
上游用什么协议，互不感知。

### 2. 一键公网访问（Cloudflare）
内置 Cloudflare Tunnel：可选随机 `trycloudflare.com` 域名（零配置），或用 Token 绑定你自己的
域名。**让 Cursor / Codex 或其它机器跨网络直接访问你本地的网关**，无需公网 IP、无需自己搭反代。

### 3. 本地也能当"模型提供方"（Bearer 自注册）
Provider 支持 Bearer 令牌接入任意上游；更进一步，提供**机器侧自注册接口**
（`PATCH /__providers/{id}/self-register`，仅用 Provider 级 Bearer 令牌鉴权）：你在本地/内网跑的
模型服务，即使地址会随隧道轮转，也能用脚本自动回填地址，把**本地模型稳定地对外提供出去**。
详见 [Bearer Provider 文档](docs/bearer-provider.md)。

### 4. 同一 Key 多 Provider，异常自动切换
一个对外 API Key 可绑定一条 **Provider 备选链**：主 Provider 出现限流 / 5xx / 鉴权失败 /
连接错误时**自动故障转移**到下一个；前置 Provider 恢复后**自动回切**。后台每 2 分钟对"异常"
Provider 做一次轻量探测，恢复即停，健康 Provider 无额外流量。

### 5. 覆盖模型名与推理深度
模型名、思考（thinking / `reasoning_effort`）深度都支持两种模式：
- **默认值兜底**：客户端没带时，用 Provider/Route 的默认值补齐（比如给 Cursor 的自定义模型补上思考深度）。
- **强制覆盖**：忽略请求体里的模型名 / 思考深度，一律以网关配置为准。

### 6. 多种账号接入
Provider 不止支持 API Key，还支持 **OAuth 账号**直连：
- **Claude 账号**（Claude.ai 订阅，OAuth）
- **OpenAI / ChatGPT 账号**（Codex CLI OAuth）
- **Cursor 账号**（Cursor 订阅）

令牌刷新由网关托管，密钥/令牌永不下发到前端。

### 7. Claude 账号缓存命中优化
针对 Claude（尤其 OAuth 账号）转发，网关会在正确位置**自动注入 `cache_control` 断点**
（工具尾块、system 尾块、倒数第二条 user 消息），保证 Anthropic 的 prompt 缓存能稳定**命中**，
显著降低长会话的费用与延迟——解决了转发/转换过程中缓存断点丢失、命中率骤降的问题。
（实现见 `internal/gateway/claude_oauth_cloak.go`）

---

## 🖼️ 界面预览

> 截图请放到 `docs/images/`（见该目录说明），并确认已脱敏（不含密钥 / Tunnel Token /
> 账号邮箱 / 私有域名）。放好后取消下面对应行的注释即可显示。

<!-- ![总览 / 协议互转](docs/images/overview.png) -->
<!-- ![Provider 管理](docs/images/providers.png) -->
<!-- ![同 Key 多 Provider 切换](docs/images/api-keys.png) -->
<!-- ![公网访问（Cloudflare）](docs/images/public-url.png) -->
<!-- ![用量 / 日志](docs/images/usage.png) -->

---

## 🚀 快速开始

一键启动（编译网关，并在缺失/过期时自动构建 `web/dist`）：

```bash
cd web
npm install
npm run dev
```

- 本地开发 UI：`http://127.0.0.1:5173`（Vite HMR）
- 网关 API / 公网入口 / 管理页：`http://127.0.0.1:18093`

仅起后端：

```bash
go run ./cmd/gateway
# 自定义端口
GATEWAY_ADDR=127.0.0.1:18090 go run ./cmd/gateway
```

> 只跑 `go run ./cmd/gateway` 时，公网管理页依赖网关内的 `web/dist`，需先 `cd web && npm install && npm run build`，
> 否则公网 UI 域名会 404（`npm run dev` / `scripts/ensure-gateway.sh` 会自动构建）。

配置存储：`~/Library/Application Support/llm-protocol-gateway/`（SQLite）。

---

## ☁️ Cloudflare 公网访问

默认关闭；在 UI 开启后可选：

- **随机模式**：`cloudflared tunnel --url http://127.0.0.1:18093`，取输出的 `trycloudflare.com` 地址，无需账号。
- **自有域名模式**：在 Cloudflare Zero Trust 建 Tunnel，为域名添加指向 `http://127.0.0.1:18093`
  的 Public Hostname，把 Tunnel Token 填入 App，网关以 `cloudflared tunnel run --token <token>` 运行。

> Tunnel Token 属敏感信息，当前明文持久化在本地（后续计划接入 Keychain）。

---

## 📚 文档

- [Bearer 令牌 Provider 说明](docs/bearer-provider.md) —— 配置、鉴权、健康探测、故障转移、自注册
- [macOS App](docs/macos-app.md)

---

## 🔌 验证接口

```bash
curl http://127.0.0.1:18093/__health
curl http://127.0.0.1:18093/__state
curl http://127.0.0.1:18093/v1/models

curl -s http://127.0.0.1:18093/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}'
```

---

## 🧩 已知问题 / 踩坑记录

### 1. Claude thinking 尾块导致连环静默（已修复）
某轮模型在 thinking 后被打断，历史里留下以裸 `thinking` 结尾的 assistant 消息，下一轮转换出的请求
非法（`assistant` 消息不能以 thinking 收尾），且模型倾向继续只输出 thinking，造成会话卡死。已通过
`dropTrailingAssistantThinking` 清理尾部 thinking、`thinking_rectifier` 匹配该类 400 剥离重试、以及
"仅 thinking 空响应"自动重试解决。

### 2. thinking 块 "cannot be modified" 报错（已修复）
Codex（Responses→Claude）回放历史/最新 thinking 块时与 Anthropic 原始签名不一致会触发
`blocks ... cannot be modified` 类 400。网关的 thinking 签名整流器现已覆盖**转换路径**（此前只覆盖
原生透传），命中即剥离 thinking 块并同 Provider 透明重试一次（对齐 `farion1231/cc-switch` 思路）。

### 3. Codex `remote_compaction_v2` 在自定义/第三方 Provider 下无法保证成功（非本项目缺陷）
Codex 远程压缩要求响应"恰好 1 个输出项"，而带 thinking 的模型天然拆成 `reasoning` + `message` 两项，
无法满足。这是 Codex 客户端侧的强校验，MiniMax / DeepSeek 等官方 `/v1/responses` 也复现。**应对**：
`codex features disable remote_compaction_v2`（本项目生成 Codex 配置时已默认带上）。
