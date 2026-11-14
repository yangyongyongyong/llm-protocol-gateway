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
真实转发失败时先经 `classifyProviderFailover`（`internal/gateway/provider_failover.go`）分成
**软故障 / 硬故障**；两者都会触发备用 Provider 切换，但控制台"异常"更保守：

**硬故障（立刻标异常）**
- 明确的订阅/账单耗尽文案（`usage limit`、`insufficient_quota`、`credit balance` 等）
- 明确的凭证失效（`invalid_grant`、`revoked`、`invalid api key` 等）
- 对 `claude_oauth`：若缓存的 5h utilization ≥ 95%，上述 usage 文案保持硬故障；**< 95% 则降级为软故障**（避免额度面板未满却整卡标红）

**软故障（连续 3 次 / 60s 才标异常）**
- 传输层错误（连接失败/超时等）
- HTTP 429 / 529 / 502 / 503 / 504
- HTTP 401 / 403（无硬故障关键字时，例如 token 刷新竞态）
- 带短期限流关键字的 400（`rate_limit` / `overloaded` 等）

> 注意：普通的 400（如 thinking 块签名类错误）**不会** failover / 标记异常——它们由 thinking
> 整流器就地修复重试，见 `internal/gateway/thinking_rectifier.go`。
> 打标时 app log 会写 `provider marked unavailable kind=soft|hard reason=...`。

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

备选模型规则按协议区分：

- **与主 Provider 同协议**：请求里的模型名优先原样带给备选；若备选的模型目录里没有该模型
  且配置了 `defaultModel`，会用备选自己的默认模型兜底。
- **当前 Provider 支持客户端协议**：优先按客户端协议直通；`ProtocolSupport` 已实测支持的
  多协议上游会在当前 Provider 内先尝试同协议直通。直通失败且尚未向客户端提交内容时，才回退
  到该 Provider 的声明协议做转换。
- **当前 Provider 不支持客户端协议**：网关自动做协议转换，不要求用户锁定模型。
  `fallbackModelOverrides` 是可选项，仅在希望该备选始终使用某个模型时配置；未配置时仍按
  “请求模型 → 备选目录 → 备选默认模型”的顺序自动解析。

### 6.1 尝试顺序：整条链都可以回头试
一次请求先把链路过滤成**候选集**（`failoverAttemptOrder`，`internal/gateway/provider_schedule.go`）：
被撤销授权的、被禁用的都不进候选；跨协议备选不会因为没锁模型而被排除，协议转换由网关负责。
被标"异常"的候选排到最后但**不会被丢掉**（异常来自一次真实失败或探测，可能已经过期，
而它有时是唯一能服务该模型的账号）。

Key 的 `activeProviderId`（粘滞）只决定起点：链被旋转成"粘滞的那条在最前"，因此
既不会每次都回去骚扰首选，又能在粘滞备选也失败时**在同一请求内回头试前序 Provider**。
旧实现是从粘滞下标一直往后走，停在链尾，前序 Provider 要等 5.3 的回切探测（最长 2 分钟）才用得上。

Provider 的使用顺序完全由 Key 的调度规则决定：首选、备选、会话 pin、`rotate` /
`least_used` / `spread` 等选出的顺序不会因为协议是否可直通而改变。协议直通/转换只在
**当前被选中的 Provider 内部**判断，不参与 Provider 之间的排序。

### 6.2 会话粘性（prompt cache 优先于均衡）
`ActiveProviderId` 是 Key 级的，一把 Key 同时跑几个 Agent 时会互相顶掉，
每次换账号都把上游已经建好的 prompt cache 作废。现在按会话记：

- 会话标识取自请求自己报的身份：`Session_id` / `X-Session-Id` / `X-Claude-Code-Session-Id` 头，
  body 的 `session_id`，`metadata.session_id`，Claude Code 的 `metadata.user_id` 里
  `_session_` 之后的后缀，以及 Codex 用来路由缓存的 `prompt_cache_key`（`requestSessionID`，
  `internal/gateway/provider_affinity.go`）。裸 `user_id` 不算会话，它把多个会话合在一起。
- 哈希成 `keyID + 首选 Provider + 调度模式 + 完整备选链 + session` 的摘要后查 pin 表
  （纯内存，30 分钟滑动过期，上限 1 万条）。谁回答了这一轮，pin 就跟到谁；pin 指向的
  Provider 挂了或已不在链上，pin 立刻作废。改首选、切换调度模式、增删备选或调整备选
  顺序都会生成新的命名空间，旧 pin 不会继续压过新配置；空的默认模式与显式 `order`
  共用同一命名空间。`spread` 模式下窗口放宽到 2 小时，并在请求发出前先占位，避免两个
  同时开场的新会话都读到空表而挤到同一个账号。
- 优先级：**会话 pin > 调度模式 > Key 粘滞**。pin 生效的那一轮不回写 `activeProviderId`，
  否则两个会话会把 Key 级粘滞来回抖动、每请求一次落库写。
- 没带任何会话标识的请求行为不变，仍走 Key 级粘滞。

### 6.3 调度模式 `scheduleMode`
Key 级（也可存进"方案" `KeyProfile`，切方案一起切），空 = 现状：

| 值 | 行为 |
| :-- | :-- |
| `` / `order` | 按链路顺序，粘滞的 Provider 排最前 |
| `rotate` | 每个请求换下一个候选，内存游标，重启从头开始 |
| `least_used` | 在飞请求数最少的先用，其次看近 6 分钟实际处理过的 token（**缓存命中不计**） |
| `quota` | 额度余量多的先用，数据来自额度面板已缓存的快照（Claude / ChatGPT / Cursor / 智谱 / DeepSeek / OpenCode Go），读不到的排在中位，**绝不在请求路径上打上游** |
| `spread` | **会话分散**：新会话落到「当前挂着的活跃会话数最少」的账号，同数再按在飞/近 6 分钟负载、最后按链路顺序；定下后由会话 pin 钉住，两小时滑动。两个会话两个账号就各占一个，失败照常沿链漂移并改钉到新账号 |

非法取值在保存时被拒绝（`normalizeScheduleMode`），不会静默退化成 `order`。

### 6.4 首包超时（假死的上游换掉，而不是拖死）
Provider 上的 `firstPacketTimeoutSec`：网关等这台**响应头**的最长时间，超时即放弃这次尝试、
继续下一个候选。定时器只管到响应头到达为止，所以开播之后思考两分钟的模型不会被误伤
（`doUpstreamHTTP`，`internal/gateway/upstream_timeout.go`；上限 600s，0 = 使用网关默认 600s）。

实现上它返回的是自定义错误而不是 `context.Canceled`——后者会被 5.2 的判定当成
"客户端已经断开"而停止 failover。专用接口保存：`POST /__providers/{id}/first-packet-timeout`
`{"seconds": N}`（Provider 的 PATCH 是增量的，数字 0 分不清"没传"和"关掉"）。

### 6.5 并发闸门（先由网关拒绝，别让厂商判你异常）
Provider 上的 `maxConcurrentRequests` + `queueWaitSeconds`：

- 超出的请求**在发出前**被拒，返回 429 且带 `Retry-After`；若链上还有别的候选，
  先自动换过去，换不出去才把 429 交给客户端。
- 被闸门挡住**不算 Provider 故障**：不写软故障计数、不标"异常"，忙碌的健康账号不会被误判。
- `queueWaitSeconds > 0` 才排队，等不到位置同样 429；排队用广播唤醒，等待者超时或客户端断开都会退出队列。
- 专用接口：`POST /__providers/{id}/concurrency` `{"maxConcurrentRequests": N, "queueWaitSeconds": N?}`，
  上限 1000 / 排队上限 60s；把上限清 0 时排队秒数一并归零（`internal/gateway/provider_lanes.go`）。

### 6.6 调度轨迹（为什么是它、换了谁）
每次请求把决策过程记进请求日志的 `analysis` 里（与上下文统计同一个 JSON，无需新表）：
`scheduleMode` / `candidates`（排好序的候选）/ `sessionPinned` / `attempts[]`
（每个候选：provider、model、status、`reason`、耗时、`deferred` 回答被丢弃、`skipped` 根本没发出）。
控制台在 API 日志详情的「调度轨迹」里逐条列出；早于该功能的日志没有这些字段，
页面直说"未记录"，不补一个看起来像真相的空链。reason 取值见 `internal/gateway/schedule_trace.go`。

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

### 7.3 协议 conformance（推荐）与旧自检
机器侧用自注册令牌调用：
```
POST /__providers/{id}/self-check/conformance
Authorization: Bearer <自注册令牌>
```
按 Provider 当前 `protocol` 跑一套用例：

| id | 级别 | 含义 |
| --- | --- | --- |
| `models` | required | `/models` 鉴权连通 |
| `nonstream_shape` | required | 非流式 JSON 形状 + 可提取助手文本 |
| `stream_shape` | required | SSE 事件序列（Chat 需 `[DONE]`；Responses/Claude 需对应事件） |
| `usage_fields` | recommended | usage 字段齐全（Responses 需 `input_tokens_details.cached_tokens`，即使为 0） |
| `cache_hit` | recommended | 两轮探测是否回报 cache 命中 |

返回要点：`success`/`passedRequired` 表示必过项全过；`passedAll` 含建议项；`cases[].hint` 给出修复提示。  
脚本应改完再重跑，直到 `success=true`（不要在网关侧重试死循环）。

控制台会话可用：`POST /__providers/{id}/conformance`（owner/admin）。

兼容旧接口（仍可用）：
```
POST /__providers/{id}/self-check/health   # 连通性+鉴权
POST /__providers/{id}/self-check/chat      # 端到端对话（固定 prompt）
```

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

## 10. 成本账本（价格表）

网关按自己的价格表给每次请求算成本，让用量页能回答「这周花了多少」。价格由操作员
按厂商公布的费率卡录入，**不是账单**，也不会去抓第三方价目表：这个网关前置的大多是
编程套餐、订阅登录和国内中转，任何公开按 token 报价的表都盖不住它们，而猜出来的单价
会被静悄悄加进总额——比「没有价」危险得多。

- 单位：**美元 / 百万 token**，四档互斥：`inputUsdPerMTok`（未命中缓存的输入）、
  `outputUsdPerMTok`、`cacheReadUsdPerMTok`、`cacheWriteUsdPerMTok`。
- 键是 `providerId + modelId`，`modelId: "*"` 是该 Provider 的默认档，具体模型行优先。
- 我们记的 `inputTokens` 已包含缓存读与缓存写，计价时**必须先减掉这两块**再按
  未缓存单价算，否则同一批 token 被计两次（`costOf` 有专门的回归测试钉住这点）。
- 成本在请求结束时算定并落到请求日志（`costMicros` + `priced`）与永久日桶
  （`cost_micros`、`cache_write_tokens`、`priced_count`），所以事后改价不会重写历史
  请求的成本；升级之前的历史数据没有成本列，页面显示「未计价」而不是 $0.00。
- 日桶同时记 `priced_count`（当时有价格行的请求数）。**成本总额只累加已计价的那部分**，
  所以金额必须和覆盖率一起读：控制台在 KPI 角标上写「部分未计价」，把「大半没设价」
  显示成「很便宜」是这本账最不能犯的错。
- 积分制订阅（Qoder / WorkBuddy 等）**不要**填美元价：留空即未计价。

接口（管理员）：`GET /__prices`、`POST /__prices`（整体 upsert）、
`DELETE /__prices/{providerId}/{modelId}`。删除 Provider 时它的价格行随之下架
（全量保存时清理），避免回收后的 id 继承到别人的单价。

控制台（用量页）：

- 概览多一张「区间已计价成本」KPI，附覆盖角标与「已计价的每次」均价；份额口径可切到
  **成本份额**，Provider 环图、协议环图、模型排行与密钥/用户排行一起换轨，没有金额的
  行显示 `—`，不写 `$0.0000`。
- 走势图的每日气泡里带当天成本与覆盖率。
- 头部「价格表」打开录入面板：列已设价的行、逐行编辑/删除，并把**本区间有用量但一行价
  都没有**的 Provider 排在前面（按请求量降序）供一键跳转。普通用户看不到入口（`/__prices`
  是管理员接口）。
- API 日志的请求详情里成本是一枚 chip：`$0.0270` 或「未计价」，鼠标悬浮说明「按请求结束时
  的价格表算出，改价不回算」。日志密集表刻意不加第 14 列——列宽是按不横向滚动倒推的，
  成本在详情、日聚合与 CSV 里给。
- CSV 导出（概览与请求面板）都带「已计价成本 USD」与「已计价请求数」两列。

> 加成本列时踩过的坑：日聚合的成本要穿过十几个「一行一行累加」的合并循环，漏一个不报错，
> 只是那一维的金额静默变 0。`internal/monitor/usage_cost_test.go` 与
> `internal/store/model_prices_test.go` 因此把每个维度、每别名合并（`MergeModelDayStats`）
> 和按密钥下钻视图（`UsageStatsRangeForKeys`）都逐一断言了金额，而不只是总数。

---

## 11. 每密钥额度

密钥可以自己带一份预算，用完就在网关被拒，不再打到上游。它是**治理闸门**，不是预付费钱包：
目的是「这把 Key 今天最多花这么多」，不是精确计费。

- 三个字段一起才有意义：`quotaWindow`（`day` / `month`）、`quotaTokens`、`quotaCostMicros`。
  两个上限都是 0 时窗口一并清空（不门控任何东西的窗口只是界面噪声）；设了上限却没给窗口一律
  **400 拒绝**，因为这种配置只能回一个「不知何时恢复」的 429。
- 窗口按**网关本机时区**的日历重置：日本地 0 点、月 1 号 0 点，上界独占。生产 VPS 是
  `TZ=Asia/Shanghai`，所以「每天 0 点重新开始」就是他会对用户说的那句话；用 UTC 日界会让
  +08:00 的部署早上 8 点才"新的一天"。
- 判定口径与用量页一致：**输入（已含缓存）+ 输出**。`InputTokens` 落库时已归一化，
  所以额度计算绝不再加 `CacheTokens`，否则缓存命中的那部分被算两遍。
- **入口只预扣 prompt**：请求的 prompt 在入口就能估出来（字符类估算，中文按约 1 token/字，
  不用 `bytes/4`——那会把中文 prompt 少算约 4 倍），而回答的 token 与成本要等上游结束才知道。
  所以一波并发的超出量是「这些在途请求真实产生的量」，下一个请求即被拒。预扣在响应结束后释放。
- 成本额度只跟**已完成**的已计价用量比较：在途请求还没有价格。上游没设价格行时成本恒为 0，
  这条上限永远不会触发，所以要用成本限额最好同时给一个 `quotaTokens`。
- 拒绝是 429 + `Retry-After`（到重置的秒数）+ `X-Quota-Reset-At`（RFC3339），响应体按**客户端
  协议**形状写（Claude 用 `{"type":"error"}`，OpenAI 两种用 `{"error":{...}}`）。它走
  `errKeyQuotaExceeded` → `upstreamErrorStatus`，**不会**被当成上游故障：不换账号（换到哪个
  Provider 都会被同一把 Key 拒）、不把账号判死、不扣上游的额度。
- 拦截点在 `executeProtocolFlowWithFailover` 开头（Chat / Responses / Claude / compact 四条流
  共用一处），`/v1/images/generations` 走自己的路径所以单独门一次；`/v1/messages/count_tokens`
  不打上游，不门。
- 额度只能**管理员**设置：普通用户给自己提预算就是权限提升。`PATCH /__apikeys/{id}` 是部分更新，
  三个额度字段没出现在请求体里时从当前密钥带回（脚本调用不像控制台那样重发全部字段）。

顺带修掉一个同类的老坑：`PATCH` 不带 `enabled` 从前等于把它写成 `false`，密钥会**静默停用**；
停用的密钥在 token 解析阶段就不匹配了，于是流量不再报 401，而是落到路由的默认 Provider 上继续跑。
现在缺省即保留原值，`key_quota_test.go` 钉住这一点。

窗口用量从监控的日桶读（`KeyWindowUsage` 只走窗口内的几天，不重建控制台快照）；重启后由
`usage_daily_buckets` 回填，所以额度不会被一次重启洗白。

## 12. 代码索引

- 结构与常量：`internal/domain/types.go`
- 鉴权注入 / 密钥解析：`internal/gateway/server.go`（`applyProviderAuth` / `resolveProviderAuth`）
- 模型地址推导：`internal/gateway/server.go`（`deriveModelsURL` / `fetchProviderModels`）
- 异常判定与后台重探：`internal/gateway/provider_failover.go`
- 候选排序 / 会话粘性 / 负载与额度 / 并发闸门 / 轨迹：`internal/gateway/provider_schedule.go`、
  `provider_affinity.go`、`provider_load.go`、`provider_lanes.go`、`schedule_trace.go`
- 首包超时：`internal/gateway/upstream_timeout.go`（在 `doHTTPWithTiming` 内生效）
- 成本账本：`internal/gateway/pricing.go`、`internal/store/model_prices.go`
- 每密钥额度：`internal/gateway/key_quota.go`（窗口 / 预扣 / 429 形状），
  `internal/monitor/usage_key_window.go`（窗口用量读取）
- 后台恢复循环启动：`internal/app/runtime.go`（`StartProviderFailoverRecovery`）
- 自注册 / 自检 / conformance：`internal/gateway/provider_self_register.go`、`provider_conformance.go`
- thinking 签名整流器：`internal/gateway/thinking_rectifier.go`
