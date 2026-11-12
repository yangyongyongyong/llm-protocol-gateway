# WorkBuddy 反代链路对齐（对照 misakano7545/workbuddy2api-panel）

参考基线一：`misakano7545/workbuddy2api-panel` `main@6f19b76`（Go 网关实现，本地参考副本
`.cache/ref/workbuddy2api-panel`，已 gitignore）。
参考基线二：`magpie-community/plugins` `main@bf959aa` 的 `packages/workbuddy` 0.2.21
（magpie 网关的 WorkBuddy 插件，`index.mjs` 200KB + `REFUSED.md` 实测记录，本地副本
`.cache/ref/magpie-plugins`）。两份实现互相独立，冲突处的处理见第三节。

本文记录两件事：**这一轮把哪些链路差异真正对齐了**，以及**还有哪些没对齐、值不值得做**。

## 当前协议能力（2026-11-12）

- **国内版**：`/v2/chat/completions` 覆盖主要模型；同账号根另有 `/v1/messages`，但只实测支持
  `claude-opus-5.5` / `claude-sonnet-5.5` / `claude-opus-5` / `claude-opus-4.8-1m` 四个 Claude
  模型，且仅限流式。其它模型即使发到 Messages 路由也会 400/500。
- **国际版 WorkBuddy AI**：没有 `/v1/messages`，该路径恒为 404，只能走 OpenAI Chat。
- **本仓识别方式**：通用协议探测针对 API Key / 对端 Provider 的“同一 baseUrl 派生多条路径”
  生效；WorkBuddy OAuth 不加入通用探测，因为它的 Messages 能力是**模型级 + 仅流式**，
  不能用一个 Provider 级协议徽标表示。WorkBuddy 单独由 `workBuddyMessagesApplies` 按
  “国内版 + 白名单模型 + stream=true”分流。下游继承的 peer Provider 只能看到上游实际
  透传的 `protocolSupport`，不会自行猜测 WorkBuddy 具备全模型多协议。

对齐的判定口径是「出站字节形态」和「上游是否接受」，不是功能多少：头集合对上、头顺序
对上、TLS 指纹对上、请求体形态对上、审核黑名单漂白对上，才算一条链对齐。

## 一、这一轮已落地

| 层 | 文件 | 内容 |
| --- | --- | --- |
| 头顺序 | `internal/gateway/workbuddy_headorder.go` | 按路径的官方头顺序表（chat 43 头 + report / busy / config 三族）、`reorderWorkBuddyHeadBlock` 纯函数、`Connection` 头补发 |
| 头顺序落线 | `internal/gateway/workbuddy_headorder_conn.go` | `net.Conn` 包装：Write 侧重排头块、Read 侧跟踪响应边界以支持连接复用；全程 best-effort，判不出就原样透传 |
| TLS 指纹 | `internal/gateway/workbuddy_tls.go` | utls 复刻 Node/OpenSSL ClientHello（18 cipher、9 sigalg、10 扩展、无 ALPN），JA3 `71dc8c…`、JA4 `t13d0a1200_…`；`WORKBUDDY_TLS_FINGERPRINT=go` 可回滚 |
| 单出口 + 连接池 | `internal/gateway/workbuddy_exit.go` | 每个账号一份 transport（含代理与指纹档位作为 key）、拼写归一 + Connection 的单一出口、账号侧调用（billing/growth/report/签到）不再走 `http.DefaultClient` |
| 请求体 | `internal/gateway/workbuddy_body_fields.go` | 轮级 `conversationRequestId` / `startsNewUserRequest` / `agent:cli`、`prompt_cache_key`、`stream_options.include_usage` |
| 脱敏 | `internal/gateway/workbuddy.go` | 补 4 条模板句规则（Codex 身份句、反馈句、`11128`→`11-128`、身份句式正则 7 动词）、全局规则扩到所有角色、新增 reasoning 与 tool_calls 参数净化、tools 描述净化 |
| 会话 id | `internal/gateway/workbuddy_chat_headers.go` | `workBuddyStableUUID` 改为锚定 2026-01-01 的伪时间 UUIDv7（原先时间位直接取哈希，解码出 1974/5000 年） |

关键改动点与依据：

1. **头顺序必须在连接层做**。Go 的 `net/http` 自己序列化头（`Header.writeSubset` 按 key
   排序），RoundTripper 拿不到「按顺序写字节」的控制权。上游同样是在 conn 层重排
   （`headorder_conn.go`），本仓此前只在注释里引用过一个不存在的
   `workbuddy_headorder.go`——注释与实现脱节。
2. **TLS 指纹要接管整个 TLS 拨号**（`DialTLSContext` 返回已握手的 conn），否则
   `Transport` 在 `DialContext` 之上自己握手，写进 conn 的是加密记录，头顺序看不到也改不了。
   代价：`Transport.TLSHandshakeTimeout` / `TLSClientConfig` 失效，超时与 SNI 由拨号钩子
   自行保证（`workBuddyTLSHandshakeTimeout`）。
3. **账号侧调用共用连接池是真实的关联线索**。`workBuddyDo` 原先走 `http.DefaultClient`，
   一台机器上所有账号的 billing/report/growth 共用一个池——「同一出口两个账号共用一条
   TCP/TLS 连接」正是上游明确要避免的形态。现在按 `(host, 账号摘要)` 分池，摘要只做哈希，
   不落明文。
4. **体里的轮级字段与头必须一致**。客户端把「这一轮属于哪个 request-id」同时写在头和体里；
   只写头、体里一条没有，本身就是可判别的形态。历史轮的 id 不编造——对不上的 id 比缺失更
   容易被抓出来。
5. **`prompt_cache_key` 是纯费用项**。上游实测同一段 8k 前缀：不带 → 命中 0 token、
   credit≈0.34；带 → 命中 7808 token、credit≈0.02（约 17 倍）。本仓此前只有 ChatGPT 侧
   有缓存键逻辑，WorkBuddy 没有。
6. **`11128` 字面量本身是拦截条件**。上游只要请求体里出现裸 `11128` 就整单拦，与上下文无关。
   客户端想讨论这个错误码必须先漂白，否则永远 400。

### 验证

```bash
go build ./... && go vet ./internal/gateway/
go test ./internal/gateway/ -run "WorkBuddy" -count=1
go test ./... -count=1          # 全量，gateway 包约 89s
```

新增用例覆盖：头顺序（含未知头保序、未捕获路径不动、畸形块透传、跨 write 拆头）、
连接包装（一次重排后 body 透传、响应分帧 Content-Length/chunked/不可判定）、
请求体字段（轮级标记只打最后一条 user、客户端已有值优先、缓存键稳定且跨账号隔离、uuid 形态与
时间位合理）、脱敏（工具参数与 reasoning、身份句式整体改写、`11128` 邻码不受影响）、
TLS（spec 形状与 JA3/JA4 锚点、真实握手 + 包装、档位选择）、transport 形状（h2 真禁、
代理与档位进池 key）、出口隔离（两账号不同池、同账号复用）。

## 二、还没对齐的部分（按 ROI 排序）

### P0：直接影响「请求能不能过」与积分到账

1. **非流式聚合会吞掉中途错误**（`workbuddy.go` `buildWorkBuddyCompletion`）
   已有 chunk 之后收到 `error` 帧时，代码记录 message 后 `continue`，最终按 200 返回部分
   内容；上游是 error 帧透传/首帧否决。另有：finish 缺省默认 `"stop"`（无 `[DONE]` 也报
   stop，应报 `length`）、顶层 `{code,msg}` 无 choices 的失败帧被静默忽略、非 delta 的
   `message` 形态不支持、tool_calls 缺 index 时用数组下标归槽（同帧多调用并槽）。
2. **残缺工具参数原样下发**。上游有 `truncation.go`：`finish_reason=="length"` 或流未正常
   结束时检测 tool arguments JSON 不完整并剔除；本仓直拼下发，客户端拿到会解析失败。
3. **错误码分类缺失导致罚错对象**。本仓 `classifyProviderFailover` 只看 status + 文案，
   缺 `6000/6004/14003`（模型级限流）、`6020/6021`（上游繁忙）、`11102`（模型级负缓存）、
   `12153`（session dead）、`11115/11133/11135/11140/11148/14017/14018/15021/402`。
   后果：单模型限流被当成账号 429 → 整号被罚；死号没有「判定死亡」语义 → 反复入选。
4. **新手礼包 / 连登兑换 / 抽奖 / 补签全缺**
   （`/billing/meter/check-gift-claimed`、`/claim-gift`、`/activity/growth/redeem`、
   `/lottery/summary`、`/lottery/draw`、`/growth/heatmap`、`/makeup-cards/use`）。
   这些是纯 GET/POST、零对话成本、直接积分，是目前本仓积分收益最大的空缺。

### P1：路由与稳定性

5. **续写（continue）能力缺**。上游 `server/continue.go` 551 行：空回复/`length` 截断时
   自动发续写段（上限 3 段），并在静默 10s 补 `: keepalive` 帧。本仓无此能力，长答案被上游
   输出上限截断后客户端只看到半截。
6. **积分保底 / 余额预留 / 到期加权都没有进选号**。上游 `credit_floor`、`reserve_credits`、
   `expiring urgency`（sqrt 映射 1.5→20.0，且「只加不减」）都是路由输入；本仓到期数据只用于
   展示（`workbuddy_expiry.go`），不参与选号，一笔就能打穿号池。
7. **模型级冷却台账缺**。上游 `modelCooldowns` 每账号每模型独立冷却 + 手动停牌；本仓只有
   Provider 级熔断。
8. **桌面埋点指纹三元组未按 realm 分档**（`workbuddy_report.go`）：国际号会盖上国内包的
   `commit/releaseDate/version`。
9. **活跃上报无回读校验**。上游上报后回读 `/activity/growth/streak` 判断是否真计分（200 但
   未计分是已知行为，小程序事件还会回滚）；本仓上报即认为成功。
10. **上下文窗口四级查找链只做到第一级**。本仓模型表静态带 Context，缺「上游动态 →
    model.json 缓存 → 静态种子 → models.dev」的后三级兜底；也未用 `/v3/config` 的
    `data.agents[].models`（客户端菜单口径）裁剪对外暴露的模型列表。

### P2：能力面

11. **能力端点缺**：`/v2/images/edits`、`/v2/videos/generations`、`/v2/videos/tasks`、
    `/agenttool/v1/search`、`/agenttool/v1/webfetch`、`/agenttool/v1/agentic_search`、
    `/mcp`、去水印裁切。已有的 `/v1/images/generations`、`/v1/embeddings` 路径能拼上，
    但走的是通用 `resolveProviderAuth`，没有 WorkBuddy 的 Bearer + `X-User-Id` + 桌面头族，
    对 WorkBuddy 账号实际不可用。
12. **猫猫旅行**（`buddy/info`、`travel/status|depart|claim`）、**开学季券码**、
    **国际版 trial / 注册补地区 / 网页 agent 跑活** 全缺。
13. **`X-Enterprise-Id` / `X-Tenant-Id`** 企业头未实现（上游也无，个人号无此头，属企业号场景）。
14. **`/v2/chat/busy` 模型繁忙度观测**缺，无法在选号时避让繁忙模型。

## 三、第二参考实现（magpie packages/workbuddy 0.2.21）带来的修正与新缺口

magpie 的 WorkBuddy 插件是另一条独立实现路线，且带一手实测记录（`REFUSED.md`）。逐条比完之后，
它推翻了我第一轮的两处「改过头」，并暴露了四个本仓完全没有的链路能力。

### 3.1 已按 magpie 证据修正（本轮第二次改动）

| 项 | 第一轮做法（依据 workbuddy2api-panel） | 修正后（依据 magpie 实测） |
| --- | --- | --- |
| 身份句处理 | 整句替换成 `You are a helpful AI assistant.` | 只插入逗号：`You are an AI agent, powered by X.`。实测整句被拒（400）、加逗号通过（200），而**加词无效**（`…DeepSeek Harness CLI.` 仍 400）。整句替换会把 harness 名字从唯一说明「哪个 agent 在跑」的那行里删掉——`REFUSED.md` 明确把这种替换记为 bug |
| 品牌词 | 把 `DeepSeek Harness` 改写成 `DeepSeekRuntime` | 不改。逗号已足够，改名是超出必要范围的语义损失（`workBuddyIdentityBrands` 现为空表，保留常量位以免将来误加） |
| 空内容轮 | system 与 assistant 都整条丢 | 只丢 system 轮；其它角色保留空内容（magpie `unflag`：`if (t.trim() \|\| m.role !== "system")`）。空 assistant 轮占的是轮次位置，删掉会把当前轮挪到别的消息上 |
| `rpcUserAgent` | intl 版形如 `workbuddy-ai/5.9.4 …` | 改为 `WorkBuddy/5.9.4 workbuddy-ai/5.9.4 CLI/2.165.0`。实测 **UA 不以 `WorkBuddy/<version>` 开头会被回错误码 10085**；只有中段品牌才分构建 |
| DSH 指纹 | 只有 `Tokens prefixed with @` 那一段 | 增补第二段：`Web GUI whose URL the runtime context names. When the user refers`（magpie #291 实测 400→200） |

### 3.2 两个参考源冲突的地方，以及取值理由

1. **角色作用域**。`workbuddy2api-panel` 的 `sanitize.go` 写「上游按连续原文匹配、不分角色」；
   magpie 的 `REFUSED.md`（面向用户的说明）写「只读 system 与 assistant 消息」，而它的
   `index.mjs` 实现注释写「WorkBuddy reads **every message**, not only the system prompt
   (Claude Code puts its main branch in the first user message)」——实现与自身文档不一致。
   取值：**实现优先**，全局规则作用于所有角色（与两份实现的代码一致），身份类规则只作用于
   system/developer/assistant。理由是两份实现都这么写，且用户轮里的品牌词实测不触发渠道检查。
2. **身份句作用域**。`workbuddy2api-panel` 认为骨架本身是特征（品牌换了也可能 400），
   magpie 认为「加逗号即可，且必须保留名字」。取值：**骨架要动、名字要留** —— 用逗号断开
   骨架，正是两者结论的交集。
3. **`11128` 字面量**。只有 `workbuddy2api-panel` 记录「请求体里出现裸 11128 即整单拦截」，
   magpie 没有这条规则。取值：保留（它是保护性的，且改写只动数字形态、不动语义）。

### 3.3 magpie 暴露的、本仓完全没有的链路能力（按价值排序）

| # | 能力 | 证据 | 影响 |
| --- | --- | --- | --- |
| 1 | **国内版 Claude 模型走 Anthropic Messages 路由** `<endpoint>/v1/messages`（仅流式） | magpie README §Requests：`claude-opus-5.5` / `claude-sonnet-5.5` / `claude-opus-5` / `claude-opus-4.8-1m` 在该路由应答；`glm-5.3`、`deepseek-v4.1-flash` 返 500，`gpt-5.5`、`kimi-k3-1` 返 400。**国际版无此路由**（`/v1/messages` = 404 Route Not Found） | 本仓所有 WorkBuddy 模型一律走 `/v2/chat/completions`，Claude 系模型因此**拿不到 `output_config.effort`**。实测差距极大：同一问题 `max` 档中位 30850 输出 token vs `low` 档 5748；而 chat 路由的 `reasoning_effort` 几乎不起作用（3884 / 5144 / 5715，三档几乎无差） |
| 2 | **Messages 路由的专用语义** | 同上：不分角色重写、不加默认 system、不搬 system、保留 billing 行（Messages 不返 11128）；但必须**丢弃没有 `signature` 的 `thinking` 块**，否则一旦同请求里有签名块就 11159 `Invalid signature in thinking block … bound to a different conversation` | 若接 Messages 路由，必须同时实现 `thinking` 块过滤，否则跨路由续接的会话会整单被拒 |
| 3 | **免费模型日窗**（`deepseek-v4.1-flash`，国际版） | magpie：`FREE_WINDOW_AI = {model:"deepseek-v4.1-flash", aliases:["deepseek-v4.1-flash-sg"], cap:1e8}`，24h 固定窗（非滑动），计数口径 = prompt + **cache_read** + completion；超窗返 429 `usage exceeds frequency limit` | 本仓无此计数：免费模型被 429 后仍会继续打，且 `deepseek-v4.1-flash-sg` 与免费 id **共享同一窗口**（换了 id 也照样超） |
| 4 | **`10085` 错误码** | 见 3.1：UA 前缀不合规 | 本轮已修 UA 形态，但错误码本身仍未进分类表（命中时会被当普通 400） |
| 5 | **`11102` / `11159` 错误码** | magpie 对两者都有专门处置 | 本仓缺（`11102` 此前已记为 P0 缺口） |
| 6 | **企业微信登录页版本** | 提交记录 `a90b7aa`：登录页按桌面端版本打开，企业微信登录入口才出现 | 影响企业号首次登录，本仓登录链路未处理 |
| 7 | **免费模型过滤开关** | magpie「Free models only」：列表只留当前免费的模型，其它模型直接 404 不发；价目表读不出来时一律 503（因为无法判定是否免费），价目表最多每分钟重读一次 | 本仓无「只发免费模型」模式，也无「价目不可读时不发」的保守策略 |

## 四、明确「不跟」的部分

- **渲染器 Chromium 的 TLS 指纹**（16 cipher / 18 扩展 / ALPN=h2）不复刻：本仓出站没有
  渲染器形态的请求，且 h2 是刻意禁掉的（半死连接）。上游同样不跟。
- **`sanitize_brand_scope=all`**（身份类规则对所有角色生效）不采用：默认取 `identity`
  作用域，只作用于 system/developer/assistant。实测 user 轮里的品牌词不触发渠道检查，改写
  用户原话反而是数据损失。全局规则（计费头、模板句、`11128`）对所有角色生效——这两条
  作用域是分开的。
- **上游的账号池状态机**（disabled/paused/cooling/breaker/degraded/sessionDead + 加权路由）
  不整包移植：本仓的路由单元是 Provider，多账号 = 多个 Provider，已有
  `provider_schedule` 五种模式。可移植的是「按模型冷却」「积分保底」「到期加权」三件，
  见 P1。

## 五、线上验证（gc，2026-11-05）

发布方式：`gc`（173.249.207.202）手工复刻 `deploy/deploy-linux.sh` 的服务器端步骤（本机无
bash/WSL，原脚本跑不了）：交叉编译 `linux/amd64` + `npm run build` → 打包 → scp → 备份 →
替换 → `systemctl restart` → 健康检查。版本 `20261105.075133`，服务
`llm-protocol-gateway.service` active，`providers=59 routes=25 apiKeys=12`。

验证账号：**`workbuddy-订阅-陌`**。用临时 route + 临时 key 钉住该账号，验完立即删除
（不动现网任何配置）。

| 验证项 | 结果 | 说明 |
| --- | --- | --- |
| Provider 模型拉取（`/v3/config`） | **200，709ms**，模型表完整 | 走新链路：TLS 指纹 + 头序 + UA + 单出口，对真实上游可用 |
| 真实对话（Claude 协议，免费模型 `hy4free`） | **200，1665ms**，返回「在的」 | 请求日志 `id=143901 provider_id=workbuddy-订阅-陌 model=hy4free status=200` |
| **DSH 身份句脱敏**（system 带 `You are an AI agent powered by DeepSeek Harness.`） | **200，2725ms** | 改前该行必 11128。这是脱敏规则在真实账号上的直接证据 |
| 流式链路（SSE） | **200，1422ms**，事件序列完整 | `message_start → content_block_start/delta/stop → message_delta → message_stop`，21 个 `thinking_delta` 正常透出 |

未在本轮验证的：付费模型（避免消耗账号额度）、模型级冷却与免费日窗（需触发限流才有数据）、
`/v1/messages` 直连路由（当时未实现；后续第八次发布已补齐，见 3.3 第 1 条）。

## 六、配置

| 变量 | 取值 | 默认 | 说明 |
| --- | --- | --- | --- |
| `WORKBUDDY_TLS_FINGERPRINT` | `node` / `go` | `node` | `go` 退回 crypto/tls 原生 ClientHello（排障/回滚用），其余任何值都按 `node` |
| `WORKBUDDY_PREFER_EXPIRING` | `1` / 空 | 关闭 | 开启后选号优先花掉**最早到期**的额度 |
| `WORKBUDDY_CREDIT_FLOOR` | 数字（credits） | `0`（关闭） | 低于该值的账号在还有别的可选时排到最后；全链都低于阈值时不动顺序 |

后两项只读缓存（`workBuddyPackagesCache`，10 分钟 TTL），未知账号保持原相对位置；
排序稳定，`mode` 仍是管理员口径。**默认关闭是有意的**：现网 59 个 Provider 在跑，
静默改变「哪个账号来回答这次请求」不该由一次发布决定。

Provider 行上的 `proxyUrl` 现在对 WorkBuddy chat 生效（`http://`、`https://`、`socks5://`），
并作为连接池 key 的一部分：换代理会换池，不会复用旧路由的连接。

## 七、本轮（第二次发布）新增

| 能力 | 落地 | 行为变化 |
| --- | --- | --- |
| 免费模型日窗 | `workbuddy_freewindow.go` | `429 usage exceeds frequency limit` 判为「账号健康、额度用完」，**不再罚整个 Provider**（此前按软限流会逐步判异常）；并从报文里解析 `reset at … UTC±N`，精确休息该模型到窗口重开，无时间戳退回短休息、上限 24h；只有带该措辞的 429 才走此分支 |
| 积分保底 / 到期加权 | `workbuddy_credit_route.go` | 作为 mode 之后的偏好层（默认关闭，见上表） |


### 第三次发布新增：积分任务域

| 能力 | 落地 | 说明 |
| --- | --- | --- |
| 积分任务域 | `workbuddy_rewards.go` | 新手礼包（`check-gift-claimed` / `claim-gift`，**走裸 billing 根，带 `/v2` 会 404**）、连登兑换（`growth/streak` + `growth/redeem`）、抽奖（`lottery/summary` + `lottery/draw`）。三个域各自独立，一个被拒不影响其它；只认真实回执里的 `credit`，空回执按「已领」处理；redeem/draw 带 uuid-v4 形状的 `client_token` 做幂等 |
| 接线 | `workbuddy_tasks_api.go` | 接入每日保活链，返回 `rewardsCredit` 与 `rewardsNotes` |

线上实测（陌账号 `POST /__providers/{id}/workbuddy-oauth/keepalive`）：第一次返回 `refreshed/activity/checkin:done`，
但 `rewardsNotes` 报 `streak: cannot unmarshal string into … next_tier of type int` —— **上游把 `next_tier` 写成字符串**，
强类型 int 会让整条 streak 读取失败。修正后（数值字段全部容错 + 回归用例）同一调用不再报错，说明 streak 端点已被调用并解析成功；
该账号当前无待领礼包、无可兑换档位、无抽奖次数，故本次积分为 0。

### 第四次发布新增：续写

| 能力 | 落地 | 说明 |
| --- | --- | --- |
| 续写 | `workbuddy_continue.go` | 非流式答案被输出上限截断时自动续接，最多 3 段。触发条件是 `finish_reason` 为 `length` 或 `max_tokens`（两侧同义，上游原样透传），或正文为空；只走聚合路径（流式客户端字节即到即发）；不续工具调用 |
| 客户端意图 | `server.go` handleClaudeMessages | 在入口把 `stream:false` 记进 context：转换链会把 `stream` 强制成 true、`Accept` 也会被改成 SSE，两者都不能当判据（这一条是线上调试出来的） |

线上实测（陌账号 `hy4free`，`max_tokens=64` 非流式）：`output_tokens` **64 → 256**（首段 + 3 段），
耗时 1.5s → 5.8~10.7s。开关 `WORKBUDDY_CONTINUE=1`（gc 已通过 systemd drop-in 开启）。

顺带测到一个独立问题：`hy4free` 这类带思考的模型在小 `max_tokens` 下会把预算全花在 thinking 上，
正文为空（`text_len=0`）——续写只能续思考。这属于「预算分配」问题而非续写缺陷，需要单独决策（抬 `max_tokens` 或压 reasoning 档位）。


1. `/v1/messages` 直连路由（后续已实现）：国内版 Claude 模型（`claude-opus-5.5` 等）走该路由才吃
   `output_config.effort`。**当前目标账号 `workbuddy-订阅-陌` 的 17 个模型里没有 Claude 系**，
   这条路由在本账号上无法验证，需要一个有 Claude 模型的国内账号才能量出效果。
2. 续写（continue）：空回复 / `finish_reason=length` 时续 3 段。
3. 桌面埋点三元组的 realm 分档：缺国际版真机抓包的 `commit` / `releaseDate`，**不猜**，
   维持现状并在拿到抓包后补。
4. 成长任务域（旅行 / 连登兑换 / 抽奖 / 礼包 / 开学季券码 / 国际版 trial）与能力端点
   （改图 / 出视频 / 联网检索 / webfetch / MCP / 去水印）。
5. 上下文窗口四级查找链的后三级兜底（上游动态已有，缺 model.json / 静态种子 / models.dev）。
6. 上报回读校验（上报后回读 `growth/streak` 判断是否真计分）。
### 第五次发布新增：图像端点

| 能力 | 落地 | 说明 |
| --- | --- | --- |
| 图像生成 | `workbuddy_endpoints.go` | 修掉既有缺陷：原路径只有裸 Bearer，且把 `/images/generations` 拼在**已含 `/v2` 的 base** 上（`/v2/v2/...`），对 WorkBuddy 账号不可用。现按账号根拼 `/v2/images/generations`，复用 `applyWorkBuddyChatHeaders` 带全量桌面头族与本账号连接池 |
| 图像改图 | `POST /v1/images/edits` | 新路由，复用生成处理器的鉴权/路由/拒绝逻辑，仅用 context 标记区分上游路由；`image` 字段归一为数组 |

线上实测（陌账号，模型故意不存在，只看厂商回应）：

```
generations → 400 {"code":14401,"msg":"...Image model [probe-no-such-image-model] route config not found"}
edits       → 400 {"code":11101,"msg":"...cannot unmarshal string into ... EditImageRequest.image of type []string"}
            → 归一后 → 400 {"code":14401,"msg":"...route config not found"}
```

两条都拿到厂商原话，说明路径、头族、鉴权全部打通；edits 的卡点从「字段类型」前移到「模型不存在」，
符合预期。**注意该账号没有专用图像模型**（只有 hy4free/hy4std），所以这里验证的是链路而非出图结果。

顺带修掉两个自己引入/沿袭的缺陷：workbuddy 图像分支此前只返回不写响应（会丢成功响应）；
非 2xx 被包成 502，客户端看不到厂商原文。

### 第六次发布新增：embeddings

`/v1/embeddings` 与图像同为「通用透传 + 裸 Bearer + 双 `/v2` 前缀」的缺陷，现已改走 `<账号根>/v2/embeddings`
并带全量桌面头族（复用 `executeWorkBuddyEndpoint`）。

线上实测（陌账号）：

```
不存在模型 → 500 {"code":11351,"msg":"Generate embedding failed ... not found"}   # 路径/头族/鉴权全通
bge-m3     → 200，dim = 1024                                                  # 与参考实现记录的规格一致
```

这条链在改动前是**完全不可用**的（`/v2/v2/embeddings` + 无身份头），现在能真实出向量。

### 第七次发布新增：上报回读、上下文窗口解析链

| 能力 | 落地 | 说明 |
| --- | --- | --- |
| 上报回读 | `workbuddy_readback.go` | 上报后回读 `growth/streak`，用内存基线判断是否真的推进。厂家对**静默丢弃**的上报也回 200，只看状态码会永远显示成功而账号什么都没赚到；连登为 0 直接给「疑似 200 未计分」提示。上报失败仍返错，回读失败只记 note |
| 上下文窗口 | `workbuddy.go` | 修 bug：`detail.MaxInputTokens` 直接覆盖解析结果，配置里未给该字段的模型窗口被抹成 0。改为走 `resolveModelContextLength(id, 上游值)`，保留「已知模型表 → 默认值」兜底 |

线上实测（陌账号保活）：

```
{"refreshed":true,"activity":true,"checkin":"done","streakDays":5,
 "message":"令牌已续期 · 活跃已上报（连登 5 天） · 签到：done"}
```

`streakDays: 5` 是真实回读值，也顺带说明该账号的连登是正常计分的（不是被静默丢弃）；
首次无基线时不断言「是否推进」，符合设计。

**关于「四级查找链」的取舍**：参考实现是「上游值 → model.json 持久缓存 → 静态种子表 → models.dev」。
本仓只实现前三级里真正需要的两级半 —— 上游值（`/v3/config` 实时给 `maxInputTokens`）、
已知模型表、默认值。不做持久缓存（缓存一个实时值没有收益）与 models.dev（会给模型同步路径
加外部 HTTP 依赖，而本上游从不省略该字段）。结论写在这里，避免以后被当作遗漏。

### 第八次发布新增：`/v1/messages` 路由

这是本次对齐里**收益最大**的一项：chat 路由的 `reasoning_effort` 几乎不起作用（low / max / 非法值
分别 3884 / 5144 / 5715 中位输出 token），只有 Messages 路由的 `output_config.effort` 被真正执行
（low 5748 vs max 30850）。本仓此前所有 WorkBuddy 模型一律走 chat，Claude 系因此完全拿不到 effort。

| 项 | 实现 |
| --- | --- |
| 开闸范围 | 只对参考实测可在该路由应答的四个模型：`claude-opus-5.5` / `claude-sonnet-5.5` / `claude-opus-5` / `claude-opus-4.8-1m`；其它模型保持 chat（猜未测模型会把握手变成 404） |
| 限定条件 | 仅国内版（国际版 `/v1/messages` 恒 404）、仅流式（该路由只支持流式） |
| 头族 | 复用桌面头族 + `anthropic-version: 2023-06-01`；URL 由账号根拼，不用已含 `/v2` 的 base |
| 11159 防护 | 丢弃**无 `signature`** 的 `thinking` 块；其余原样（该路由不跑渠道检查，客户端 billing 行与身份句都不改写） |
| 开关 | `WORKBUDDY_MESSAGES_ROUTE`（默认开，置 `0` 退回 chat） |

线上探测（陌账号，该账号没有 Claude 模型，故两边都失败——但**失败的来源不同**，这就是证据）：

```
claude-opus-5.5 → 400 {"code":11158,"msg":"model [claude-opus-5.5] is not allowed for user type
                      [personal_free_user]","retry_policy":{...},"displayMsg":{"en":..,"zh":..}}
glm-5.3        → 200，SSE 正常出流（走 chat，未被误路由）
```

`11158` 的响应形态（`retry_policy` + 双语 `displayMsg`）是 **Messages 路由专属**的，证明请求确实打到了
`/v1/messages`；`glm-5.3` 正常出流证明闸门按模型名精确分流、没有把 chat 模型误送过去。

顺带把 `11158`（账号套餐不含该模型）补进错误码表：判为模型级（换号可能换到有该套餐的账号），
但不永久休息该模型（套餐可升级）。

### 第九次发布新增：出视频与查任务

| 路由 | 上游 | 说明 |
| --- | --- | --- |
| `POST /v1/videos/generations` | `/v2/videos/generations` | 提交任务，透传厂商回执（含任务 id） |
| `POST /v1/videos/tasks` | `/v2/videos/tasks` | 用 `{task_id}` 查询 |
| `GET /v1/videos/{taskID}` | `/v2/videos/tasks` | OpenAI 风格轮询 |

两个关键点：**任务只能被提交账号读**，所以按任务 id 记台账（内存、24h TTL、上限 4096），
未知任务明确报 404 而不是猜一个账号去读别人的任务；**状态原样透传不归一**，`Done`/`Succeeded`
兼容历史拼写 `succeeded`/`success`。解析宽容：`data[].url` 与顶层 `url`/`video_url` 都认，
错误取 `msg`/`message`/`error` 三形态。时长/模型/缺 prompt 的边界由上游判定，网关不重复校验。

线上探测（陌账号，未提交真实任务、不消耗额度）：

```
不存在的模型        → 400 {"code":14407,"msg":"...Video model [probe-...] route config not found"}
minimax-video-h3 1s → 400 {"code":14407,"msg":"unsupported video duration: 1 seconds,
                                             supported range: 4-15 seconds"}
未知任务查询        → 404 {"error":{"message":"unknown video task; submit it through this gateway first"}}
```

第二条是强证据：真实模型被上游**受理**，只因时长越界被拒，错误码与 4-15 秒区间和参考实现记录逐字一致。

### 这一轮同时修掉一个「注册了却 404」的坑

视频路由注册后线上仍回**纯文本 404**（`404 page not found`），而紧邻的图像路由正常。
根因不在注册，而在 `withHostSeparatedServing` 的 default 分支：只有 `isModelProtocolPath` 为真的路径
才转给 API handler，而该白名单缺 `/v1/videos/` 前缀——请求根本没进 mux。
embeddings 当初踩过同一个坑（`ui_embeddings_path_test.go` 即为此而设）。
已补前缀并新增 `ui_videos_path_test.go`（路径判定 + 本地转发 + api/ui 双 host）。

### 第十次发布新增：联网检索与网页抓取

| 路由 | 上游 | 说明 |
| --- | --- | --- |
| `POST /v1/search` | `/agenttool/v1/search` | 联网检索，返回 `results[]` / `total_results` |
| `POST /v1/webfetch` | `/agenttool/v1/webfetch` | 抓正文，返回 markdown |

两条都走 chat 域、**没有 `{code,msg,data}` 信封**，正文即结果，故原样透传不做包装（自己造一层信封
等于换了一个 API）。参考实现实测的三条规则直接落进实现：

- `provider` 是**响应**字段，请求里带任何值都 `400 code 15003` → 请求体由服务端**重建**，只保留
  `query`/`max_results`；客户端回显上一次响应也无法污染下一次请求
- `type` 只有小写 `text2text` 合法 → 固定写入，不采信客户端大小写
- `max_results` 是上限不是保证（服务端硬顶约 10），官方固定 5 → 缺省 5、超过 10 按 10 钳制

线上实测（陌账号，额度极轻）：

```
/v1/search   → 200，total_results=3（真实结果：go-sponge.com、CSDN SSE 文章…）
               响应里 provider 由厂商回填 —— 正是请求该字段被剥离的反证
/v1/webfetch → 200，title="Example Domain"，content=982 字节 markdown
               字段比参考实现记录的更多：content_type/content_length/truncated/status_code/context
```

这是继 embeddings 之后第二条**产出真实业务结果**的新链路。

### 第十一次发布新增：猫猫旅行与补签

| 域 | 端点 | 规则 |
| --- | --- | --- |
| 旅行 | `buddy/info` → `travel/status` → `travel/claim` / `travel/depart` | `arrived` 才领奖，且 `record_id<=0` **不发请求**（0 不是一次领取）；`idle` 且未到日限才派出；`traveling` 等待；地点固定 4（参考实现记录四个地点收益相同） |
| 补签 | `growth/heatmap` → `makeup-cards/use` | **先于连登兑换执行**：补回昨天才是保住多天连登，先兑换等于用一整段连登换一天积分；按最新漏签日往回用，受余额与每轮上限（3）约束 |

两者接入 rewards pass，新增 `TravelCredit` / `MakeupUsed` 并计入 `Total`。

线上实测（陌账号保活）：

```
"rewardsNotes":"travel: 出行中，等到站后领奖"
```

这条 note 同时证明三件事：`buddy/info` 与 `travel/status` **真实调用成功**（否则会是 `travel status: …` 错误）、
该账号**有猫**（不是「还没有猫」分支）、当前状态是 `traveling` 所以按设计不重复派出也不领取。
把状态写进 note 而不是只在失败时留痕，是为了让「静默无事可做」和「端点不可达」在运维界面上可区分。

### 第十二次发布新增：埋点构建三元组按 realm 分档

此前 `commit` / `releaseDate` 是单一常量，国际版账号会盖上**国内构建**的指纹；而 `ideVersion`
早已按 realm 走（5.9.3 CN / 5.9.4 intl），三元组内部因此自相矛盾。

| 项 | 处理 |
| --- | --- |
| 机制 | `workbuddy_report_build.go`：`workBuddyReportBuildFor(site)` 按 realm 取三元组 |
| 国内 | 实测值 `b7f4a19c…` / `1761696000000`；可用 `WORKBUDDY_REPORT_COMMIT_CN` / `_RELEASEDATE_CN` 覆盖 |
| 国际 | **无抓包数据**。可选 `WORKBUDDY_REPORT_COMMIT_INTL` / `_RELEASEDATE_INTL`，两者都给才算实测；否则沿用国内值、返回值标记 `Measured=false`，并在日志里明确告警（每进程一次） |

这里的取舍写清楚：**不伪造**。一个从未发布的 commit hash 比复用实测值更假，所以机制做成
「可配置 + 显式标记未实测」，拿到国际版抓包后改环境变量即可生效，不必重新发版。

线上验证：陌账号（国内版）保活全链正常，且**没有出现国际版告警** —— 与设计一致，
该告警只在国际版账号上报时触发一次。

### 仍未对齐（下一轮）

objective 点名的 11 项**已全部落地**：错误码分类、`/v1/messages` 与 thinking 块过滤、免费模型日窗、续写、
积分保底与到期加权、模型级冷却、埋点分档、上报回读、上下文窗口、能力端点（图像/改图/embeddings/视频/
检索/webfetch）、成长任务（礼包/连登/抽奖/旅行/补签）。

以下为**新增服务面/功能扩展**，不属「对齐」范围，且有硬阻塞：

| 项 | 阻塞点 |
| --- | --- |
| MCP 服务端（`/mcp`） | 无状态 JSON-RPC + 六工具面；与现有 `/v1/*` 重叠度高 |
| 去水印裁切 | 需 COS 白名单 + 禁跟随重定向防 SSRF；需真实带水印图才能验 |
| 综述式检索（`agentic_search`） | 单次 40–66s / 约 47K prompt token，成本高 |
| 开学季券码 / 国际版 trial / 注册补地区 | 券码活动已结束；后两者需国际版写入能力 |
| 埋点三元组的**国际版实测值** | 机制已就绪，缺的是一次国际版真机抓包（配置即生效） |

### 仍未对齐（下一轮）

已完成：错误码分类、`/v1/messages`、免费日窗、续写、积分保底与到期加权、模型级冷却、上报回读、
上下文窗口、能力端点（图像/改图/embeddings/视频/检索/webfetch）、成长任务（礼包/连登/抽奖/旅行/补签）。

| 项 | 状态 | 阻塞点 |
| --- | --- | --- |
| MCP 服务端（`/mcp`） | 未实现 | 无状态 JSON-RPC 子集 + 六工具面；与现有 `/v1/*` 端点重叠度高，属新增服务面 |
| 去水印裁切 | 未实现 | 需 COS 域名白名单 + 禁跟随重定向防 SSRF；需真实带水印图才能验 |
| 综述式检索（`agentic_search`） | 未实现 | SSE 长连接、单次 40–66s / 约 47K prompt token，成本高 |
| 开学季券码 / 国际版 trial / 注册补地区 | 未实现 | 券码活动已结束；trial 与注册需国际版写入能力 |
| 埋点三元组按 realm 分档 | 不做（缺证据） | 没有国际版真机抓包的 commit/releaseDate |

### 仍未对齐（下一轮）

已完成：错误码分类、`/v1/messages`、免费日窗、续写、积分保底与到期加权、模型级冷却、上报回读、
上下文窗口、能力端点（图像/改图/embeddings/视频/检索/webfetch）、成长任务（礼包/连登/抽奖）。

| 项 | 状态 | 阻塞点 |
| --- | --- | --- |
| MCP 服务端（`/mcp`） | 未实现 | 无状态 JSON-RPC 子集 + 六个工具面，工作量最大的一块；与已有网关功能重叠度高 |
| 去水印裁切 | 未实现 | 需 COS 域名白名单 + 禁跟随重定向防 SSRF；且要有真实带水印图片才能验 |
| 嵌入 `agentic_search`（综述式检索） | 未实现 | SSE 长连接、单次 40-66s / 约 47K prompt token，成本高 |
| 埋点三元组按 realm 分档 | 不做（缺证据） | 没有国际版真机抓包的 commit/releaseDate |
| 成长任务剩余域（旅行/补签/开学季券码/国际版 trial/注册补地区） | 未实现 | 部分需国际版写入能力；优先级最低 |

### 仍未对齐（下一轮）

已完成（objective 的 11 项中 10 项）：错误码分类、`/v1/messages`、免费日窗、续写、积分保底与到期加权、
模型级冷却、上报回读、上下文窗口、能力端点（图像/改图/embeddings/视频）、成长任务（礼包/连登/抽奖）。

| 项 | 状态 | 阻塞点 |
| --- | --- | --- |
| 联网检索 / webfetch / MCP / 去水印 | 未实现 | 检索类会消耗真实额度且无免费档；去水印需 URL 白名单防 SSRF |
| 埋点三元组按 realm 分档 | 不做（缺证据） | 没有国际版真机抓包的 commit/releaseDate |
| 成长任务剩余域（旅行/补签/开学季券码/国际版 trial/注册补地区） | 未实现 | 部分需国际版写入能力；优先级最低 |

### 仍未对齐（下一轮）

已完成：错误码分类、模型级冷却、免费日窗、积分保底与到期加权、积分任务域、续写、图像端点、
embeddings、上报回读、上下文窗口解析链、**`/v1/messages` 路由**。

| 项 | 状态 | 阻塞点 |
| --- | --- | --- |
| 出视频（`/v2/videos/generations`、`/v2/videos/tasks`） | 未实现 | 需任务台账（只有提交账号能查），且没有可用视频模型可验 |
| 联网检索 / webfetch / MCP / 去水印 | 未实现 | 检索类会消耗真实额度且无免费档；去水印需 URL 白名单防 SSRF |
| 埋点三元组按 realm 分档 | 不做（缺证据） | 没有国际版真机抓包的 commit/releaseDate |
| 成长任务剩余域（旅行/补签/开学季券码/国际版 trial/注册补地区） | 未实现 | 优先级低于已完成的礼包/连登/抽奖 |

### 仍未对齐（下一轮）

已完成：错误码分类、模型级冷却、免费日窗、积分保底与到期加权、积分任务域、续写、
图像端点、embeddings、上报回读、上下文窗口解析链。

| 项 | 状态 | 阻塞点 |
| --- | --- | --- |
| `/v1/messages` 直连路由 + thinking 块过滤 | 未实现 | 现网带 Claude 模型的 3 个 Provider 是 `claude-订阅账号`×2 与 `cursor-订阅账号`，**没有 WorkBuddy 账号带 Claude 系模型**；实现即死代码 |
| 出视频（`/v2/videos/generations`、`/v2/videos/tasks`） | 未实现 | 需任务台账（只有提交账号能查），且没有可用视频模型可验 |
| 联网检索 / webfetch / MCP / 去水印 | 未实现 | 检索类会消耗真实额度且无免费档；去水印需 URL 白名单防 SSRF |
| 埋点三元组按 realm 分档 | 不做（缺证据） | 没有国际版真机抓包的 commit/releaseDate |
| 成长任务剩余域（旅行/补签/开学季券码/国际版 trial/注册补地区） | 未实现 | 优先级低于已完成的礼包/连登/抽奖 |

### 仍未对齐（下一轮）

已完成：错误码分类、模型级冷却、免费日窗、积分保底与到期加权、积分任务域（礼包/连登/抽奖）、
续写、图像端点（生成/改图）、embeddings。

| 项 | 状态 | 阻塞点 |
| --- | --- | --- |
| `/v1/messages` 直连路由 + thinking 块过滤 | 未实现 | 现网带 Claude 模型的 3 个 Provider 是 `claude-订阅账号`×2 与 `cursor-订阅账号`，**没有 WorkBuddy 账号带 Claude 系模型**；实现即死代码 |
| 出视频（`/v2/videos/generations`、`/v2/videos/tasks`） | 未实现 | 需任务台账（只有提交账号能查），且没有可用视频模型可验 |
| 联网检索 / webfetch / MCP / 去水印 | 未实现 | 工作量较大；去水印需 URL 白名单防 SSRF |
| 上下文窗口四级查找链（后三级） | 未实现 | 上游 `/v3/config` 已给 contextLength，兜底链优先级低 |
| 上报回读校验 | 未实现 | 需额外上游调用回读 `growth/streak` |
| 埋点三元组按 realm 分档 | 不做（缺证据） | 没有国际版真机抓包的 commit/releaseDate |
| 成长任务剩余域（旅行/补签/开学季券码/国际版 trial/注册补地区） | 未实现 | 优先级低于已完成的礼包/连登/抽奖 |

### 仍未对齐（下一轮）

已完成：错误码分类、模型级冷却、免费日窗、积分保底与到期加权、积分任务域（礼包/连登/抽奖）、
续写、图像端点（生成/改图，带头族）。

| 项 | 状态 | 阻塞点 |
| --- | --- | --- |
| `/v1/messages` 直连路由 + thinking 块过滤 | 未实现 | 现网带 Claude 模型的 3 个 Provider 是 `claude-订阅账号`×2 与 `cursor-订阅账号`，**没有 WorkBuddy 账号带 Claude 系模型**；实现即死代码 |
| `/v1/embeddings` 带 WorkBuddy 头族 | 未实现 | 与图像同源缺陷，但 embeddings 走通用透传 `serveOpenAIPassThrough`，接缝未验证 |
| 出视频（`/v2/videos/generations`、`/v2/videos/tasks`） | 未实现 | 需任务台账（只有提交账号能查） |
| 联网检索 / webfetch / MCP / 去水印 | 未实现 | 工作量较大；去水印需 URL 白名单防 SSRF |
| 上下文窗口四级查找链（后三级） | 未实现 | 上游 `/v3/config` 已给 contextLength，兜底链优先级低 |
| 上报回读校验 | 未实现 | 需额外上游调用回读 `growth/streak` |
| 埋点三元组按 realm 分档 | 不做（缺证据） | 没有国际版真机抓包的 commit/releaseDate，硬编比现状更假 |
| 成长任务剩余域（旅行/补签/开学季券码/国际版 trial/注册补地区） | 未实现 | 优先级低于已完成的礼包/连登/抽奖 |


## 八、上游 2026-11-05 之后的跟踪对齐（第二次跟踪）

上游 `workbuddy2api-panel` 期间新增 27 个提交（`c5ce29b..2b210e9`）。逐条比对后，**三处真正影响反代链路**的已对齐：

| 上游提交 | 问题 | 本仓处理 |
| --- | --- | --- |
| `afb5f22` (#197) SSE 粘帧 | 一个 `data:` 行里塞多个 JSON，客户端按 SSE 规范解析必然失败、整轮作废 | 新增 `workbuddy_sse_split.go`：按字节扫描（跟踪字符串/转义/嵌套深度）切分，每段验 JSON，切不动原样返回；聚合路径改走 `workBuddyStreamPayloads` 逐值解析；流式路径加 `workBuddyFrameSplittingBody` 行级重写 |
| `e8df35b` 续写闸门 | 正文里写成文本的工具调用（DSML 等）被当成「话没说完」→ 续写 → 模型再发一次调用 → 客户端再执行一次，实测 33% 的触发是这种死循环 | 新增 `workBuddyHasToolCallMarkup` + 标记表（DSML 全角/半角、`function_calls`、`antml:invoke`、`invoke name=`、`tool_calls`），命中即不续写，闭合与未闭合都排除 |
| `1adfa52` 签到重试 | 厂家对「请稍后重试」需要分钟级沉降，一次失败 = 当天丢失 = 断连登 | 新增 `workbuddy_checkin_retry.go`：仅对瞬时失败（措辞或 5xx）在 +10/+40 分钟各补一次；补签前查当天记录；只排瞬时、不排业务拒绝；offline 已有逐分钟循环故排除；包级去重 + 2h 有界 |

**本仓在这三处原本更差的地方**：

- 粘帧：流式是字节透传，坏帧会原样发给客户端；聚合路径对解析失败的行直接 `continue`，**两段内容静默丢失**（上游至少是透传报错）。
- 续写：本仓 `finish_reason=max_tokens` 时必续写，只要正文以 DSML 结尾就会中同一个循环。
- 签到：**完全没有重试**，瞬时失败即当天丢失。

评估后不跟的：`/v1/models` 的 `promo_*` 字段（本仓已有 `free`/`rate`/`rate_was`，口径等价）、首帧等待期注释帧（本仓已有心跳）、`narration` 式「说一句就停」救援（属新增功能而非对齐）、号池分区（上游自己已 revert）。

线上验证（gc `20261111.055651`，陌账号）：非流式 200（`output_tokens=256`，续写仍生效且未被 markup 闸门误伤）、流式 200 且事件序列完整。

