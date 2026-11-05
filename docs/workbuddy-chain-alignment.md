# WorkBuddy 反代链路对齐（对照 misakano7545/workbuddy2api-panel）

参考基线一：`misakano7545/workbuddy2api-panel` `main@6f19b76`（Go 网关实现，本地参考副本
`.cache/ref/workbuddy2api-panel`，已 gitignore）。
参考基线二：`magpie-community/plugins` `main@bf959aa` 的 `packages/workbuddy` 0.2.21
（magpie 网关的 WorkBuddy 插件，`index.mjs` 200KB + `REFUSED.md` 实测记录，本地副本
`.cache/ref/magpie-plugins`）。两份实现互相独立，冲突处的处理见第三节。

本文记录两件事：**这一轮把哪些链路差异真正对齐了**，以及**还有哪些没对齐、值不值得做**。

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
`/v1/messages` 直连路由（尚未实现，见 3.3 第 1 条）。

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


1. `/v1/messages` 直连路由：国内版 Claude 模型（`claude-opus-5.5` 等）走该路由才吃
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

