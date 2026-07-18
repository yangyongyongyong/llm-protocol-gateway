# Bearer 令牌 Provider 说明

本文档说明如何在本网关中配置、使用与运维 **Bearer 令牌（API Key）** 类型的
Provider，以及它的鉴权、健康探测、故障转移与"自注册"机制。适用于对接任意
OpenAI Chat / OpenAI Responses / Claude 协议、且用一串密钥做鉴权的上游。

> 与 OAuth 类 Provider（`claude_oauth` / `cursor_oauth` / `chatgpt_oauth`）的区别：
> Bearer Provider 用一个固定密钥直接发往上游，`BaseURL` 可自由设置；OAuth 类
> 由平台托管令牌刷新且 `BaseURL` 被固定到各自上游，不能自注册。

---

## 1. 它是什么

Bearer Provider 对应 `AuthType = "api_key"`（或留空 `""`，等价默认）。核心字段：

| 字段 | 说明 |
| --- | --- |
| `protocol` | 上游实现的协议：`openai_chat` / `openai_responses` / `claude` |
| `baseUrl` | 上游入口 URL（可为你的 Cloudflare 域名、第三方中转等） |
| `apiKeySource` | 密钥来源，见下文格式 |
| `authHeader` | 承载密钥的请求头，默认 `Authorization`；Claude 惯例用 `x-api-key` |
| `defaultModel` | 缺省模型名（客户端未显式指定时兜底） |

对应代码：`internal/domain/types.go`（`Provider` 结构、`AuthTypeAPIKey`）。

---

## 2. 密钥来源 `apiKeySource` 格式

`resolveProviderAuth`（`internal/gateway/server.go`）按前缀解析：

- `literal:<token>` —— 明文密钥，直接落盘保存（当前实现）。
- `env:<ENV_NAME>` —— 从进程环境变量读取，密钥不落盘（推荐用于敏感场景）。
- `keychain:<...>` —— 预留（macOS Keychain，尚未实现，返回空）。
- 无前缀 —— 整个字符串按原始密钥值使用。

示例：

```
apiKeySource = "literal:sk-abc123..."
apiKeySource = "env:MY_UPSTREAM_KEY"
```

---

## 3. 鉴权头如何注入

`applyProviderAuth`（`internal/gateway/server.go`）逻辑：

1. 取 `authHeader`（为空则用 `Authorization`）。
2. 若头是 `Authorization` 且值未以 `bearer ` 开头，自动补 `Bearer ` 前缀。
3. 若 Provider 未配置密钥而客户端请求自带 `Authorization`，则透传客户端的头。

因此：

- OpenAI 系上游：`authHeader = Authorization`，最终发出 `Authorization: Bearer <token>`。
- Claude 上游：`authHeader = x-api-key`，最终发出 `x-api-key: <token>`（不加 Bearer 前缀）。
- 自定义头（如 `X-Api-Key`、`api-key`）也支持，按原样设置、不加前缀。

> 控制台"创建 Provider"表单的默认约定：Claude 协议默认 `x-api-key`，其余默认
> `Authorization`。

---

## 4. BaseURL 与"获取模型"地址推导

拉取模型列表 / 可用性探测会基于 `baseUrl` 推导出一个 `/models` 地址
（`deriveModelsURL`，`internal/gateway/server.go`）：

- host 含 `anthropic.com` → `/v1/models`
- Azure `/deployments/.../chat/completions` → 取 deployments 前缀 + `/models`
- path 含 `/chat/completions` | `/responses` | `/messages` → 就地替换为 `/models`
- 其余情况 → 在 path 末尾追加 `/models`

所以 `baseUrl` 建议直接填到"协议入口"这一层，例如：

```
https://api.example.com/v1/chat/completions   → 探测 https://api.example.com/v1/models
https://api.example.com/v1                     → 探测 https://api.example.com/v1/models
```

---

## 5. 健康探测与"异常"状态（重要）

### 5.1 手动检测
控制台的"获取模型 / 对话测试"按钮，或自注册脚本调用 self-check 接口，会发起一次
真实的连通性+鉴权检查（`fetchProviderModels` / `testProviderChat`）。

### 5.2 被标记为"异常"的条件
一次真实转发请求失败且属于"可故障转移"类型时，Provider 会被标记为 `unavailable`
（控制台显示"异常"），判定见 `shouldFailoverProvider`（`internal/gateway/provider_failover.go`）：

- 传输层错误（连接失败/超时等）
- HTTP 429 / 529 / 502 / 503 / 504
- HTTP 401 / 403（凭证或账号问题）
- 带配额/限流关键字的 400（quota、rate limit、billing、exceeded 等）

> 注意：普通的 400（如 thinking 块签名类错误）**不会**标记异常——它们由 thinking
> 整流器就地修复重试，见 `internal/gateway/thinking_rectifier.go`。

### 5.3 异常后的周期性重探
后台协程 `StartProviderFailoverRecovery`（开机启动，`internal/app/runtime.go`）：

- 每 **2 分钟**一轮（`providerFailoverRecoveryInterval`，首轮开机后 30s）。
- `reprobeUnavailableProviders`：对当前"异常"的 Provider 各发一次 **带 Bearer 的
  `GET /models`**（30s 超时），成功即清除异常标记并停止重探；仍失败则把倒计时顺延
  到下一轮。
- `recoverAPIKeyPreferredProviders`：当某个 Key 已切到备用 Provider 时，探测更高
  优先级的前置 Provider，可用即自动回切。

**关键结论：** 只有"异常期间"的 Provider 才会被后台探测（约每 2 分钟 1 次 `GET
/models`），恢复即停；**健康 Provider 无任何后台探测流量**。控制台的 `nextRetryAt`
即下一次重探时间。

### 5.4 Cloudflare 域名的注意事项
若 `baseUrl` 是你的 Cloudflare 隧道域名：异常期间约 30 次/小时的 `/models` 探测量
很低，不会因频率触发 Cloudflare 侧封禁。真正需要担心的"网络管控封禁"通常是域名/边缘
IP 被 GFW 识别，和请求频率无关。如需降频，可调大 `providerFailoverRecoveryInterval`
（`provider_failover.go`）。另需知悉：网关自身公网隧道健康探测 `healLoop`
（`internal/tunnel/manager.go`，默认每 15s 打 `/__health`）是一直在跑的、频率更高。

---

## 6. 故障转移（多 Provider 备选）

给 API Key 配置 `fallbackProviderIds` 后：主 Provider 失败（同 5.2 判定）会自动切到
下一个备选；前置 Provider 恢复后由 5.3 的回切逻辑自动切回。链路见
`executeProtocolFlowWithFailover`（`internal/gateway/provider_failover.go`）。

---

## 7. 自注册（Self-Registration）——为轮转 URL 的自建后端准备

当你的上游跑在一个会变动的反代/内网穿透地址后面（如 quick tunnel 每次重启换 URL），
可以让上游自己的脚本在地址变化时回填 `baseUrl` / `apiKeySource`，无需登录控制台。

> 仅支持 `api_key`（Bearer）类型；OAuth 类 Provider 会被明确拒绝。

### 7.1 生成自注册令牌（控制台会话鉴权，owner/admin）
```
POST /__providers/{id}/self-register-token
→ { "token": "<原始令牌，仅此一次可见>", "preview": "....abcd", "createdAt": ... }
```
令牌仅在生成时返回一次，不可再取回；重新生成会使旧令牌立即失效。撤销：
```
POST /__providers/{id}/self-register-token/revoke
```

### 7.2 机器侧回填（仅用自注册令牌鉴权，无 Cookie/CSRF）
```
PATCH /__providers/{id}/self-register
Authorization: Bearer <自注册令牌>
Content-Type: application/json

{
  "baseUrl": "https://new-address.example.com/v1/chat/completions",
  "apiKeySource": "literal:sk-...",     // 可选
  "protocol": "openai_chat",             // 可选：openai_chat|openai_responses|claude
  "authHeader": "Authorization"          // 可选；改 protocol 未给时按惯例推导
}
```
- 四个字段至少提供一个。
- `baseUrl` 有 SSRF 防护（`validateSelfRegisterBaseURL`）：必须 http/https，且不允许
  loopback / 私网 IP / `localhost` / `*.local` / `*.internal` 等内网目标。
- 成功回填后会自动清除该 Provider 的"异常"标记。

### 7.3 自检接口（用自注册令牌，供脚本联调）
```
POST /__providers/{id}/self-check/health   # 连通性+鉴权（等价"获取模型"）
POST /__providers/{id}/self-check/chat      # 端到端对话（固定 prompt "2+2等于几"）
```
建议脚本对 health 连续调用 3 次并要求全部成功，再认为已正确接入。

---

## 8. curl 速查

创建（管理员，示例走控制台内部接口）：
```
POST /__providers
{ "name":"MyUpstream", "protocol":"openai_chat",
  "baseUrl":"https://api.example.com/v1/chat/completions",
  "apiKeySource":"literal:sk-...", "authHeader":"Authorization",
  "defaultModel":"gpt-4o" }
```

测试可用性：
```
POST /__providers/{id}/test
```

机器侧自注册（轮转地址回填）：
```
curl -X PATCH https://<网关公网域名>/__providers/<id>/self-register \
  -H "Authorization: Bearer <自注册令牌>" \
  -H "Content-Type: application/json" \
  -d '{"baseUrl":"https://new.example.com/v1/chat/completions"}'
```

---

## 9. 代码索引

- 结构与常量：`internal/domain/types.go`
- 鉴权注入 / 密钥解析：`internal/gateway/server.go`（`applyProviderAuth` / `resolveProviderAuth`）
- 模型地址推导：`internal/gateway/server.go`（`deriveModelsURL` / `fetchProviderModels`）
- 异常判定与后台重探：`internal/gateway/provider_failover.go`
- 后台恢复循环启动：`internal/app/runtime.go`（`StartProviderFailoverRecovery`）
- 自注册 / 自检：`internal/gateway/provider_self_register.go`
- thinking 签名整流器：`internal/gateway/thinking_rectifier.go`
