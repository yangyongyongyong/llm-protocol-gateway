# Qoder 反代逻辑对齐报告

对照基线：`shuishuipingan/qoder2api-hub` @ `5cac9f3`（`VERSION = "1.7.24"`，`qoder_proxy.py` 13444 行）
本仓库基线：`internal/gateway/qoder_*.go`（本轮改动前）
参考库本地副本：`.cache/qoder2api-hub/`（已 gitignore，供复查）

## 0. 结论摘要

| 层 | 对齐度 | 本轮处理 |
|---|---|---|
| L1 传输与签名（COSY 信封 / 编码 / 设备指纹） | 已基本对齐 | 版本、客户端身份、常量头、四类机器指纹、会话级密钥缓存、紧凑 JSON、模型列表带体、**双区主机候选切换** |
| L2 请求体与会话身份（前缀缓存稳定化） | 主体对齐 | 23 键骨架、窗口级 `session_id`、提问级 `business.id`、`model_key` 计费归因、工具结果截短、工具历史双轨、**官方 system prompt 内嵌与替换语义** |
| L3 流式与工具回读 | 主要形态已对齐 | 切分器改状态机：标记（含全部括号变体）、DSML、引号修复、截断吞掉、散文透传、32 KB/200 帧上限、无名调用扣住、信封状态码容错 |
| L4 错误归类 / 重试 / 排队 | 归类、重试、排队、记账均对齐 | session dead、110 日额度、418 瞬时、英文去毒提示、29 MB 本地 413；排队原地等待（进度帧 / 3 轮 / 600 s）+ 瞬时重开 2 次 + 重开重签；**单号池 3 s / 额度类固定软化 / blame 延迟落账（Qoder 专属）**；余：三族退避数值、HTTP 层排队、跨区 failover |
| L5 账号 / 模型 / 额度 / 签到 | 头口径已对齐 | **签到/活动头分离**（账号身份零 cosy 头、活动头补 `cosy-version 0.4.8` + 原生六头 + 401/403 兜底）；余：身份自愈/多数表决、积分三段、峰谷价 |
| L6 可观测性 / 运维面 | 遥测三件套已对齐 | `risk` 计数器、内层错误分类计数、`/__qoder/diag/queue` 探针；流量守卫与 Web 工具未做 |

一句话：**「怎么签出去」「请求体长什么样」「回来的字节怎么拆成工具调用」「排队/瞬时故障怎么等」「失败以后怎么记账」这五层已经和参考实现一致；剩下的是边角形态与需要实机验证的参数。**

## 1. 对照方法

参考实现是单文件 Python 单体（`qoder_proxy.py` 627 KB），本仓库是 Go 多包。逐层对照时只认**行为等价**，不认代码同形：

- 参考侧证据一律给 `文件名:行号`（如 `qoder_sign.py:651-681`）。
- 本仓库侧证据一律给 `internal/gateway/文件名:行号`。
- 规格来源：`docs/internals.md`、`docs/api.md`、`docs/risk-control-and-queueing.md`、`CHANGELOG.md` 与代码；**代码与文档冲突时以代码为准**（参考库自身有 7 处文档漂移，见 §7.3）。

四条链路先分清，避免把两套身份混用（这是参考库最容易踩的坑，也是我们原先踩的）：

| 链路 | 主机 | 身份 | `cosy-clienttype` | `cosy-version` | UA |
|---|---|---|---|---|---|
| 推理 SSE `/algo/api/v2/service/pro/sse/agent_chat_generation` | intl `api3→api2→api1` / cn `gateway.qoder.com.cn` | CLI | **5** | **1.1.75** | `undici` |
| 模型列表 `/algo/api/v2/model/list` | 同上 | CLI | 5 | 1.1.75 | `undici` |
| 签到 `/sash/api/v1/me/daily-check-in/*`、quota、userinfo | `openapi.*` | 纯 Bearer | 无 cosy 头 | 无 | `QoderWork/1.1.75`（cn）/ `Qoder/1.1.75`（intl） |
| 活动 `/sash/api/v1/me/campaigns*` | `openapi.*` | 桌面端 | **10** | **0.4.8** | `Qoder` |

## 2. L1 传输与签名

### 2.1 已对齐（本轮改写）

| 项 | 参考实现 | 本仓库 | 说明 |
|---|---|---|---|
| 自定义 Base64 字母表 | `qoder_sign.py:64` | `qoder_protocol.go:428` | 64 字符表逐字节相同，`$` 作 padding |
| 三段轮转（尾/中/首） | `qoder_sign.py:79-82` | `qoder_protocol.go:434` | `a=⌊n/3⌋`，`R = S[n-a:] + S[a:n-a] + S[:a]` |
| 编码金标向量 | `qoder_sign.py:76-96` | 新增 `TestQoderEncodeBodyMatchesTheClientsCodec` | 9 条向量全部吻合（`"{}"`→`$kwm` 等） |
| 签名原文 | `qoder_sign.py:643` | `qoder_protocol.go` `qoderCOSYSignature` | `payload\ncosyKey\ndate\nbody\npath`，MD5 小写 hex |
| 签名 path | `qoder_sign.py:631-633` | `qoderSignaturePath` | 只取 path、丢掉 query、去掉 `/algo` 前缀 |
| RSA 公钥 | `qoder_sign.py:463-468` | `qoderRSAPublicKeyPEM` | 同一把 1024-bit 公钥 |
| AES 形态 | `qoder_sign.py:210-224` | `qoderUserBlob` | AES-128-CBC、**key == IV**、PKCS#7、16 个 ASCII hex 字符 |
| cosy-key 长度 | `172` 字符 | 同 | 128 字节 RSA 密文的 base64 |
| 版本 | `QD_COSY_VERSION` 默认 `1.1.75`（`qoder_sign.py:525`） | `qoder_protocol.go:60` | 原 `1.1.49`，会导致上游 430 版本门 |
| 客户端类型 | CLI `5`（`qoder_sign.py:532`） | `qoder_protocol.go:67` | 原 `10`（桌面端），模型授权口径不同 |
| `cosy-business-product` | `cli` | 同 | 原 `app` |
| `cosy-scene` | `assistant` | 同 | 原 `app` |
| `cosy-data-policy` | `agree` | 同 | 原 `disagree` |
| `cosy-clientip` | 常量 `169.254.198.161`（`qoder_sign.py:668`） | `qoder_protocol.go:75` | **原来塞的是 machineId（UUID 进 IP 字段）** |
| `user-agent` | `undici` | `qoderInferenceUserAgent` | 原来无 UA |
| `accept-encoding` | 显式 `identity` | 同 | 避免 Go 自动 gzip |
| `login-version` | `v2` | 同 | — |
| 机器 id | `derive_id(uid,"machine")` UUIDv4 形态 | `qoderDeriveID` | 原来每请求随机/nil 报错 |
| 机器 token | `base64url(sha512("machinetoken:"+uid))[:43]` | `qoderDeriveMachineToken` | 原来直接复用 machineId |
| 机器 type | `derive_id(uid,"machinetype")` 去横线取 18 | `qoderDeriveMachineType` | 原来常量 `"5"` |
| 机器 hostname | `"DESKTOP-"+upper(md5("hostname:"+uid)[:7])` | `qoderDeriveMachineHostname` | 原来**完全缺失** |
| 派生向量 | `qoder_fingerprint.py:43-84` | `TestQoderDerivedDeviceIdentity` | 4 条向量全部吻合 |
| COSY 会话缓存 | 按 uid 复用，access token 变化即重建（`qoder_sign.py:684-718`） | `qoderCosySessions` + `TestQoderCOSYSessionIsReusedUntilTheTokenChanges` | 原来每请求重建 cosy-key/info |
| identity blob | 9 键、键序排序、紧凑、`ensure_ascii=False`（`qoder_sign.py:578-585,612-627`） | `qoderUserBlob` + `qoderCompactJSON` | 原 5 键且含 `email`、缺 `yx_uid/org/user_type/refresh_token`；Go 默认 HTML 转义会让签名与上游不一致 |
| 模型列表带体 | GET 带 body `$kwm` 且参与签名（`qoder_proxy.py:3050,3059`） | `qoderFetchListing` | 原来 GET 无体、签空串 |

### 2.2 未对齐

| # | 缺口 | 参考实现 | 本仓库 | 影响 |
|---|---|---|---|---|
| ~~1.1~~ | ~~双区推理主机候选与切换~~ | `qoder_accounts.py:70-71,993-1003`：intl `api3 → api2 → api1`，仅**传输层瞬态**故障换域名；签名只覆盖 path，换主机不影响 | **已对齐**：`qoderSite.APIFallbacks` + `qoderPost` 逐主机尝试；HTTP 拒绝不轮换（同一请求在任何主机都同答）；上下文取消即停；模型列表任一失败即换下一个（`qoderFetchListing`） | — |
| 1.2 | 客户端标识 | intl `e883ade2-…` / cn `1c5e33e1-…`（`qoder_accounts.py:48,75`），redirect `qoder://aicoding…` / `qoder-work-cn://` | global `732aef47-…`、CN 复用 intl 的 `e883ade2-…` | 设备授权页面向的客户端不对，风控画像与官方不一致。**需实机验证后改**：改错会让现有设备流登录直接失败 |
| 1.3 | nonce 形态 | cn 带横线 uuid、intl 32-hex（`nonce_dashed`，`qoder_accounts.py:58/84`） | 一律 `newUUIDV4()`（带横线） | intl 侧与官方不符 |
| 1.4 | 账号元数据未采集 | `user_type / organization_id / organization_name` 来自 userinfo，进 identity blob（`qoder_sign.py:612-622`） | `qoderAccount` 无对应字段，恒空 → `user_type` 恒 `personal_professional_trial` | 团队/企业号的 blob 与真实不符，可能影响计费归因与活动资格 |
| 1.5 | `QD_DEVICE_ID_LEGACY` 等价开关 | 关时 UUID 形态，开时 32-hex（`qoder_fingerprint.py:31-40`） | 无开关 | 存量账号无法在两种形态间迁移 |
| 1.6 | `cosy-machineos` 取值口径 | 默认 Windows `x86_64_win32` / 其余 `x86_64_linux`，`auto` 才探测（`qoder_sign.py:562-574`） | `qoderMachineOS()` 恒按运行时探测 | 在 ARM/macOS 上跑会得到官方客户端不会有的组合 |
| 1.7 | 覆盖变量体系 | `QD_COSY_VERSION / _UA / _CLIENTTYPE / _MACHINE_OS / _OS_HEADERS` | 全部硬编码常量 | 上游改版本时只能改代码重编 |
| 1.8 | 签到链路无 COSY 头 | 见 §5.1 | 见 §5.1 | — |

## 3. L2 请求体与会话身份

### 3.1 已对齐（本轮改写，`qoder_body.go`）

| 项 | 参考实现 | 本仓库 |
|---|---|---|
| 23 键骨架 | `qoder_proxy.py:6212-6382` + `baseprompt.json` 结构 | `qoderRequestBody`（`qoder_body.go:224`） |
| `chat_context.text` / `originalContent` | `qoder_proxy.py:6313-6318` | 同（取最后一条 user） |
| `business.model_key` | `qoder_proxy.py:6378-6382`（#119 计费归因） | 同，`info.Key` |
| `business.name = prompt[:30]` | `qoder_proxy.py:6374` | 同（rune 安全截断） |
| `business.begin_at` 毫秒且复用 | `qoder_proxy.py:6370` | 同 |
| 窗口级 `session_id` | `qoder_proxy.py:6544-6582,8033-8039`（#136） | `qoderConversationIdentity`：客户端键优先，否则 `win:`+sha1(model␀firstUser[:400])，**排除 system** |
| 提问级 `business.id`/`chat_record_id` | `qoder_proxy.py:6278,6369-6370` | 同（按 turn 键缓存，TTL 6h / 4096 条） |
| `request_id`/`request_set_id` 每请求唯一 | `qoder_proxy.py:6273,6279` | 同 |
| `model_config` 10 键 | `qoder_proxy.py:6284-6308` | `qoderModelConfig`，`max_input_tokens` 由窗口决定 |
| 工具结果截短 | `qoder_proxy.py:5834-5926`（keep 2000 / tail 1 / 固定标记 / 幂等） | `qoderShrinkToolResult`：换行边界、`len ≤ 4×keep` 的完整 JSON 保原样、固定 41 字符标记 |
| `tools` 恒存在（空则 `[]`） | `qoder_proxy.py:6351` | 同 |
| system 单条字符串消息 | `qoder_proxy.py:6247-6259` | 同（原实现另发顶层 `system` 数组，属自造字段） |
| 工具结果图片走 user turn | `qoder_proxy.py:6130-6145` | 已有等价实现（`qoderMessagesOf`） |

新增回归：`TestQoderChatBodyCarriesTheClientsOwnShape`、`TestQoderChatBodyKeepsTheWindowAndMovesTheTurn`、`TestQoderChatBodyUsesTheClientsOwnSession`、`TestQoderChatBodyTrimsOlderToolResults`。

### 3.2 未对齐

| # | 缺口 | 参考实现 | 本仓库 | 影响 |
|---|---|---|---|---|
| ~~2.1~~ | 官方 system prompt | `baseprompt.json`（59,643 B）里的官方 CLI 系统提示词 12,324 字符，`qoder_proxy.py:335-342`；**客户端给了 system 就整体替换**，没给才用它（`6247-6259`） | **已对齐**：`qoder_base_prompt.txt`（原字节内嵌，`//go:embed`）+ `qoderSystemText` 改为替换语义。**未内嵌模板骨架**：`response_meta`/`reasoning_content_signature` 两个附加键、14 个桌面工具、两条样例 user 轮（参考本身也要丢弃），以及 `chat_context` 的模板常量——我们的 23 键骨架已逐字段等价 |
| 2.1b | 官方工具的补充语 | 官方 prompt 已含工具使用协议；参考不再额外加话 | 保留本项目实测得到的 `You must call an available function in this response.`（有 tools 时追加）——这是本仓库的经验结论，删掉会让 Qoder 倾向以散文作答；属**有意保留的差异** |
| **2.2 工具历史形态双轨** | `STRUCTURED_TOOL_HISTORY_WHITELIST=("qwen","glm","gm4","gm5","deepseek","dfmodel")`，仅 `realm=="cn"` 且命中才结构化；否则文本化信封 `[assistant 请求调用工具]\n<JSON 数组>` / `[工具结果 (name)]\n<正文>`，且**任一 tool_call_id 缺失即整体退回**（`qoder_proxy.py:5961-6023,6026-6148`） | **已对齐**：`qoderStructuredToolHistory`（cn + 白名单 + id 完整才结构化），文本化走 `qoderTextualAssistantContent` / `qoderTextualToolResult`；`auto/on/off` 三档对应 `QD_STRUCTURED_TOOL_HISTORY` | — |
| 2.3 | `reasoning_content` 回写 | 仅 DeepSeek 系保留 + `backfill_reasoning_content()` 补齐（`qoder_proxy.py:3883-3931`） | 历史里完全不带 `reasoning_content` | 多轮思考模型上下文退化；参考为此专门写了回填 |
| 2.4 | `max_tokens` 上限学习 | 学到 `"Range of max_tokens"` 后 clamp（#101，`qoder_proxy.py:375-379`） | 无 | 请求超上限时整轮 400，参考会自行压到合法区间 |
| 2.5 | namespace / MCP 展平 | `type:"namespace"` 递归展平（深度 4）、容器不出站、回程在 `response.output_item.added` 补 `namespace`、`additional_tools` 合并（#127，`qoder_proxy.py:8835-9009`） | 无 | Codex 类客户端的多命名空间工具全部失效 |
| 2.6 | 前缀 session 缓存后端 | `QD_SESSION_CACHE=memory|redis`，键 = HMAC(隔离域 + 累计前缀)，最长前缀优先（`qoder_session_cache.py:134-167`） | 只有进程内窗口键 | 多实例部署下缓存亲和失效；重启即丢 |
| 2.7 | `chat_context` 其余字段 | `chatPrompt`/`features`/`extra.context` 为模板固定值 | 同（已按参考置空/空数组） | 已对齐，保留说明 |

## 4. L3 流式与工具回读（最大缺口）

### 4.1 信封解码（已基本对齐，有 3 处差异）

| 项 | 参考 | 本仓库 | 差异 |
|---|---|---|---|
| `body` 是字符串 | `qoder_proxy.py:8499-8506` | `qoderEnvelopeBody` | 一致（含"对象形态也接受"的宽容分支，属增强） |
| `statusCodeValue` 兼容 `int`/`"200"`/缺省 | `qoder_proxy.py:8494-8498` | `qoderEnvelopeStatus`（本轮修） | 已对齐：原先按 `int` 解析，一个带引号的状态会让整帧不可解析而被静默丢弃，等同于"拒绝凭空消失" |
| `[DONE]` 结束 | `break` | `r.finish()` + `done` | 一致 |
| 多行 `data:` 不拼接 | 无累加器 | `bufio.Scanner` 逐行 | 一致 |
| `:` 注释丢弃 | `strip_data_prefix` | `consume` 只认 `data:` | 一致 |

### 4.2 工具调用泄漏回读（本轮已重写，主体对齐）

参考有 13 种正文形态（`qoder_proxy.py:4024-4676`）。本轮把切分器改成状态机（`qoder_tool_echo.go` + `qoder_stream.go` 的 `qoderText`），覆盖情况：

| 形态 | 参考 | 本仓库（本轮后） |
|---|---|---|
| **`[assistant 请求调用工具]` + JSON 数组** | 主力形态（`qoder_proxy.py:4081,4577-4676`） | **已支持** |
| 标记变体（`[<assistant…]`、`<assistant…>`、缺尾括号、`(assistant…)`、无括号） | `_LEAK_MARKER_RE` 全覆盖 | **已支持**（同一正则口径） |
| 跨帧拼接 | 17 字符暂存窗 | **已支持**（词形感知的暂存尾） |
| 内层引号未转义 | `_salvage_echo_array` + `_repair_json_quotes` | **已支持**（先原样、后修复，单调用，同参考） |
| DeepSeek DSML | `DSML_CALLS_RE` / `DSML_INVOKE_RE` / `DSML_PARAM_RE` | **已支持**（同正则口径） |
| 参数非法（外层数组合法、内层 args 不是 JSON） | 协议级失败 | **已支持** |
| 截断回声 | 严格条件吞掉（#9） | **已支持**（吞掉，finish 为 stop） |
| 只有标记 / 标记后无 JSON | 吞掉（`_marker_only_echo`） | **已支持** |
| 散文提到标记 | 原样透传 | **已支持**（`qoderMarkerOpensJSON` 区分） |
| 代码围栏内 | 剥围栏后按泄漏处理 | **更保守**：围栏内一律当正文（不剥围栏、不还原），避免把教学示例变成调用 |
| 暂存上限 | 32 KB / 200 空转帧，超限 fail-open | **已支持**（同数值） |
| 无名工具调用扣住 | `_sse_guard_nameless_tool_calls` | **已支持**：名下到达前不上线，流末仍未到名则丢，finish 降级 stop |
| `tool_call.id` | 迟到真 id 覆盖收敛 | **部分**：id 在首次上线前到达会被采用；上线后再来的真 id 不追发（极少发生） |
| 数组无标记 | 虚拟补标记后走同一路径 | 未支持（要求有标记，宁可当正文） |
| 标记后丢 `[` 的多调用 | 重包后严格校验 | 未支持（单调用经 loose 形态可救） |
| `[工具结果 (name)]…` 回声 | 吞掉（#11） | 未支持 |
| `QD_TOOL_ECHO_MODE`（strict/lenient） | 收尾策略开关 | 未支持（固定 strict） |

### 4.3 其余流式差异

| 项 | 参考 | 本仓库 |
|---|---|---|
| 失败帧 | Chat：`data: {"error":{...,"type":"invalid_tool_arguments","code":502}}` + `[DONE]`（`qoder_proxy.py:12992-13013`） | `emitError` 只给 `message`/`type:api_error`，无 `code` |
| usage 帧序 | `[content]→[finish]→[usage]→[DONE]`（`qoder_proxy.py:5328-5339`） | 并入 finish 帧（`qoder_stream.go:300`） |
| `finish_reason` | 透传 + 回读成功改 `tool_calls` | 归一为 `stop`/`tool_calls`/`length` |
| 心跳 | 5 s（`QD_SSE_HEARTBEAT`），空闲 900 s 报错 | 通用 `sseHeartbeatInterval = 15s`（`sse_heartbeat.go:14`），Qoder 走 `passthrough_stream.go:643` 已生效 |
| 首字等待 | 40–70 s 常态，靠心跳保活 | 同机制，间隔更长 |

## 5. L4 错误归类 / 重试 / 排队

### 5.1 本轮已对齐

| 信号 | 参考 | 本仓库（改后） |
|---|---|---|
| 10605 / `isQueued` / `RESOURCE_LIMIT` / `serviceAvailable=false` | 排队，**零惩罚、不解绑亲和**，429/503 + `Retry-After`（`qoder_proxy.py:7173-7180,8180-8213`） | 503 + `Retry-After` + `upstream_busy`（failover 前进不冷却）+ 英文 hint；**且先在原地等（进度帧 + 3 轮 / 600 s 预算）** |
| `112` / `pricingUrl` | `quota_exhausted` → 429 + hint（`qoder_proxy.py:7060-7064`） | 同（`code=="112"` 或 message 含 `pricingurl`） |
| `110` / `Billing daily count exceeded` | `quota_daily_limit` → 429 + 次日恢复（`qoder_proxy.py:7066-7070`） | 同（新增分支） |
| `TOKEN_EXPIRE` / `12153` / `Offline user session not found` | 会话死亡 → 401 + 停用账号（`qoder_accounts.py:935,8226-8236`） | 401 + `[auth: …]` hint（停用留给通用 failover） |
| `418` | 瞬时上游故障（`TRANSIENT_HTTP_CODES`，`qoder_proxy.py:6645`） | 映射 502 → 通用 failover `failoverSoft` |
| 出站体积 > 29 MB | 本地 413 `payload_too_large`，不发、不换号（`qoder_proxy.py:7969-7976`） | 同（`qoderMaxBodyBytes`） |
| 错误体带数字 `code` | `{"error":{message,type,code}}` | 已补 `code` 字段 |

### 5.2 未对齐

| # | 缺口 | 参考 | 本仓库 | 影响 |
|---|---|---|---|---|
| 4.1 | ~~同账号瞬时重试~~ | 2 次，1s/2s（`qoder_proxy.py:6646,8117-8127`） | **已对齐**：信层 5xx/418 且未发数据时同账号重开 2 次，退避 1s/2s | — |
| 4.2 | ~~信封层 in-place 重开~~ | 未向客户端发字节时可重开 2 次；排队 3 轮（`qoder_proxy.py:7511-7537`） | **已对齐**：`qoderChunkReader.retryQueued/retryTransient` + `reopen`，重开时重新签名（date 在签名内，必须刷新） | — |
| 4.3 | ~~排队等待~~ | 预算 600 s、单轮 ≤60+1+jitter 2 s、`retryAfterSeconds` 缺失兜底 30 s、发 `qoder_queue` 帧（`qoder_proxy.py:6657-6715,9982-10013`） | **信封层已对齐**：3 轮 / 单轮 ≤180 s / 总预算 600 s / 抖动 0-2 s / 进度帧带 `qoder_queue{queued,waited_seconds,wait_seconds,budget_seconds,message}`；**HTTP 层（非 2xx）仍是 failover 驱动**——那个位置还没有响应头，发不出进度帧 | 少数场景会立刻换号而不是等 |
| ~~4.4~~ | ~~冷却阶梯（单号池 / 额度类）~~ | 单号池 3 s clamp；额度 112/110 不吃 clamp、不停用、不喂降权（`qoder_accounts.py:1972-1987`；`qoder_proxy.py:7138-7158`） | **已对齐（Qoder 专属）**：`qoderCooldownPolicy` 在 `applyProviderFailoverOutcomeCtx` 里接管——单号池 3 s / 额度类固定 30 min 且 `hard=false`、不计软失败；**其余 provider 完全走原阶梯** |
| ~~4.5~~ | ~~额度类不吃 clamp~~ | 同上 | **已对齐**：额度判定先于 hard 分级，因此拿到 429+额度文案时不会被升级成「账号坏了」 |
| ~~4.6~~ | ~~blame 延迟落账~~ | 同请求后续换号成功才记账；整池失败一律不记（`qoder_proxy.py:8001-8002,8157-8163`） | **已对齐**：`qoderBlameBucket` 挂在请求上下文，失败先入账本，**有账号答成才 flush**、无人答成则不落账；无账本（直接调用/测试）回退通用阶梯 |
| 4.7 | 429 指数退避 / 熔断 / 降权 三族数值 | `soft_backoff 600→7200`、`breaker 3 次 1800→21600`、`degrade 5 次 600→7200`（`qoder_accounts.py:1109-1139`） | 用通用 `providerRateRestBase` 阶梯（阈值不同）；Qoder 现在只接管「单号池 / 额度 / 延迟落账」三件事，三族数值未替换 |
| 4.8 | 跨区 failover | 每请求最多 1 次切区（`qoder_proxy.py:7234-7250`） | 无自动双区路由 | 一个区排队时无法借另一个区 |
| 4.9 | 超时 | `urlopen timeout=600`，SSE 空闲 900 s（`qoder_proxy.py:8066,8347`） | 通用 `upstream_timeout.go` 参数不同 | 长思考请求可能被提前掐断 |
| 4.10 | 入站体积 | 50 MB（`QD_MAX_PAYLOAD_BYTES`） | 走通用限制 | 差异小 |

## 6. L5 账号 / 模型 / 额度 / 签到

| # | 缺口 | 参考 | 本仓库 |
|---|---|---|---|
| ~~5.1~~ | 两条链路的头 | checkin 用 `headers()`：**无任何 cosy 头**，带 `X-Request-ID`/`X-Machine-ID`/`X-Session-ID`/`Origin`/`Referer`；区域 UA `QoderWork/1.1.75`(cn) / `Qoder/1.1.75`(intl)（`qoder_accounts.py:2094-2111`） | **已对齐**：`qoderAccountHeaders`（纯账号身份，零 cosy 头，含四类 id 头与 Origin/Referer），区域 UA 由 `qoderAccountUserAgent` 给出；用作活动页 401/403 的兜底 |
| ~~5.2~~ | 活动页头 | `desktop_headers()`：clienttype 10 + `cosy-version 0.4.8`，**原生身份下六头全发**，无原生身份时一头不发（派生头会被判非官方、把活动整条过滤，`qoder_accounts.py:2113-2180`） | **已对齐**：`qoderCampaignHeaders` 补 `cosy-version: 0.4.8`；六头（id/token/type/code/os/hostname）全发且同生同死；`Content-Type` 只在 claim 的 POST 上；CN 不发明机器身份、intl 缺身份时直接给出可操作报错而不发请求 |
| ~~5.3~~ | 401/403 兜底 | 活动列表 401/403 回退 `headers()`（`qoder_accounts.py:2546-2553`） | **已对齐**：轮换设备令牌之后仍被拒，再用账号身份试一次（顺序与参考一致：先轮换、后兜底） |
| 5.4 | 身份自愈 | 活动列表 `showCampaign=false` 且来源为 runtime-info → 清种子重取（`qoder_accounts.py:2604-2612`） | 无 |
| 5.5 | 身份多数表决 | 首采 3 次全一致才采纳，分歧补 2 次（`qoder_accounts.py:736-798`） | 单次采样 + 1h 缓存 |
| 5.6 | 积分三段 / 缓存标记 | 缓存段系数 0.086、`cached_reported`、`full_price`（v1.7.21-1.7.22） | `qoder_usage.go` 只有配额窗口，无积分三段 |
| 5.7 | 模型目录 | 官方 QMC 加密目录解密 + 峰谷价 + 禁用原因 + 思考档位 | 只用账号 listing，无峰谷价、无禁用原因 |
| 5.8 | 账号调度 | 轮询 + 临期优先 + 限流护栏 + 会话亲和 TTL 2h/5000 | 通用 `provider_affinity.go`，语义接近，参数不同 |

## 7. L6 可观测性与运维面

| # | 缺口 | 参考 | 本仓库 |
|---|---|---|---|
| ~~6.1~~ | 风险遥测 | `/health` 的 `risk{queue,rate,rejected,version_gate,last{kind,at,detail}}`（`qoder_proxy.py:6791-6817,10427`） | **已对齐**：`qoderRisk` 计数器 + `GET /__qoder/telemetry`（`risk` 字段同名）；计数点=「拒绝成为客户端响应」的两处（`qoderFailureResponse`、`emitErrorQueued`），本地 413 不计 |
| ~~6.2~~ | 内层错误计数 | `/usage/perf` 的 `inner_errors{total,kinds,warned}`，5 类分类，只观测不改行为（`qoder_proxy.py:8399-8461,8509`） | **已对齐**：`qoderInner` + 同一端点 `innerErrors`；5 类同名（content_policy / rate_limit / invalid_request / auth / other），每类首次标记 `warned` |
| ~~6.3~~ | 排队诊断 | `POST /diag/queue`：`{kind,retry_after,queue_count,identity_source,advice}`（`qoder_proxy.py:11284-11354`） | **已对齐**：`POST /__qoder/diag/queue` 发 1 轮 8-token 探针，返回 `{ok,kind,status,retryAfterSeconds,detail,elapsedMs,identitySource,advice}`；`kind` 取值 ok/queued/quota_exhausted/daily_limit/auth/version_gate/error；**无 `queue_count`**（上游只在部分帧里给，探针不保证拿得到） |
| 6.4 | 流量守卫 | 单账号并发 1、最小间隔 400 ms、等待 30 s fail-open、租约 600 s 惰性清理（`qoder_proxy.py:7648-7808`） | 通用 `provider_lanes.go`（参数不同、语义为网关侧准入） |
| 6.5 | Web 工具本地执行 | `qoder_webtools.py`：内置 `web_search`/`web_fetch` 改写成本地 function 执行、最多 3 轮回灌、SSRF 三修 | 无 |
| 6.6 | 文档漂移 | 参考库自身 7 处（`QD_QUEUE_MAX_ROUNDS` 代码 3 / 文档 6；`version_gate` 恒 0；`430` 判的是状态码字符串；`_envelope_queue_action` 死代码等） | — |

## 8. 需外部条件才能对齐的项

这三项**不能只靠读代码对齐**，必须先拿到样本：

1. `baseprompt.json`（59,643 B）：官方 CLI 的完整 system prompt + 14 个桌面工具声明 + business 模板。仓库里没有，参考库里有，可原样内嵌为 `internal/gateway/testdata/qoder_baseprompt.json` 后按 §3.2-2.1 套用。
2. `runtime-info` / UMID 原生身份：需要 Qoder 桌面端安装目录或官方 npm 包中的组件；本仓库已有 `qoderRuntimeInfoPath()` 定位逻辑，但缺「多数表决」「落盘缓存 `machine_identity.json`」「清种子自愈」三件套。
3. 真实上游抓包：用于验证 `chat_context` 各字段是否被校验、`parameters.context_length`（本轮已移除）是否曾被使用、以及 `cosy-clientip` 常量在 cn 区是否同样为 `169.254.198.161`。

## 9. 建议的落地顺序

| 优先级 | 项 | 理由 | 状态 |
|---|---|---|---|
| ~~P0~~ | L3 泄漏回读：标记（全变体）+ DSML + 引号修复 + 三态收尾 | 直接决定工具链能否跑通 | **已完成** |
| ~~P0~~ | L3 无名工具调用扣住、暂存上限、信封状态码容错 | 客户端会无限重试同一轮 | **已完成** |
| ~~P1~~ | L4 排队原地等待（进度帧 + 预算 + 抖动）、瞬时重开 2 次、重开重新签名、上游 body 归属修复 | 单账号池体验落差最大 | **已完成（信封层）** |
| P0-残 | L3 数组无标记形态、`[工具结果 (name)]` 回声吞掉、`QD_TOOL_ECHO_MODE` | 参考的边角形态；我们的口径更保守（宁可当正文） | 待做 |
| P1 | L4 HTTP 层排队等待（需提前发响应头，才能在非 2xx 时也有进度帧） | 目前这类拒绝仍走 failover/503 | 待做 |
| ~~P1~~ | L4 单号池 3 s / 额度类不吃 clamp / blame 延迟落账（Qoder 专属注入） | 少一处就是一次「账号莫名不可用」 | **已完成** |
| P1 | L4 三族退避数值（429 600→7200 / 熔断 3×1800 / 降权 5×600）、HTTP 层排队 | 现有阶梯已能兜住，数值与参考不同 | 待做 |
| ~~P1~~ | L2 工具历史双轨（cn 白名单结构化 / 其余文本化 + id 缺失整体退回） | intl 侧模型 400 的根因 | **已完成** |
| ~~P2~~ | L1 双区主机候选切换 | 可用性，不改协议 | **已完成** |
| P2 | L1 客户端 `client_id`/`redirect_uri` 校正、`user_type`/`organization_*` 采集 | 画像一致性；`client_id` 改错会让现有登录失败，需实机验证 | 待做 |
| ~~P2~~ | L2 `baseprompt.json` 内嵌（system prompt + 替换语义） | 模型行为一致性 | **已完成** |
| P2 | L2 模板骨架的附加键（`response_meta` / `reasoning_content_signature`）、namespace/MCP、前缀 session 缓存后端 | 仅在客户端不给 system 时才有差；边角能力 | 待做 |
| ~~P2~~ | L5 签到/活动头分离 + 原生身份六头 + 401/403 兜底 | 活动被过滤的根因 | **已完成** |
| P2 | L5 身份自愈（清种子重取）、多数表决、积分三段、峰谷价 | 活动偶发被过滤后的自恢复；成本可见性 | 待做 |
| ~~P3~~ | L6 遥测三件套（`risk` / `inner_errors` / `/diag/queue`） | 运维可观测 | **已完成** |
| P3 | L2 namespace/MCP、Web 工具 | 面向 Codex 场景 | 待做 |

## 10. 本轮改动清单（可回滚）

| 文件 | 改动 |
|---|---|
| `internal/gateway/qoder_protocol.go` | 版本 1.1.49→1.1.75；新增 CLI/桌面客户端类型、`cosy-clientip` 常量、`undici` UA、默认 user_type；`qoderAccount` 扩 4 字段；`qoderCOSYHeaders` 全量改写（含机器指纹与 hostname）；`qoderUserBlob` 改 9 键 + 会话缓存；新增 `qoderCompactJSON`、`qoderDeriveID/MachineToken/MachineType/MachineHostname`；**`APIFallbacks` + `qoderHosts` + `qoderPost` 逐主机尝试** |
| `internal/gateway/qoder_body.go`（新增） | 23 键请求体、`model_config`、`chat_context`、窗口级 `session_id` 与提问级 `business.id`、会话身份 LRU/TTL、工具结果截短、**工具历史双轨判定（cn + 白名单 + id 完整）与文本信封渲染** |
| `internal/gateway/qoder_base_prompt.go` + `qoder_base_prompt.txt`（新增） | 官方 system prompt 原字节内嵌（12,324 字符） |
| `internal/gateway/qoder_chat.go` | **system 改为替换语义**（客户端给了就用它、没给用官方 prompt，不再拼接）；`qoderFailureResponse` 计数；430 独立成版本门文案 |
| `internal/gateway/qoder_stream.go` | 切分器改状态机（三种形态、跨帧暂存、32 KB/200 帧上限）；信封状态码兼容数字与引号；无名原生调用扣住、id 上线前可变；**信封层排队等待 + 瞬时重开（`retryQueued`/`retryTransient`/`reload`/`emitQueueNotice`）、reader 自己持有并关闭上游 body** |
| `internal/gateway/qoder_tool_echo.go`（新增） | 标记正则（全变体）、DSML 三条正则、JSON 平衡扫描、引号修复、声明工具集校验、暂存尾判定 |
| `internal/gateway/qoder_checkin.go` | **头口径分离**：`qoderCampaignHeaders`（app 身份 + 六机器头 + `cosy-version 0.4.8`）、`qoderAccountHeaders`（纯账号身份零 cosy 头 + X-* 族 + Origin/Referer）、`Content-Type` 仅在 POST、身份缓存扩到六头（含 machineCode/OS/hostname）、401/403 兜底排在轮换之后 |
| `internal/gateway/provider_cooldown_qoder.go`（新增） | Qoder 专属记账：单号池 3 s、额度类固定 30 min 软化、`qoderBlameBucket` 延迟落账（请求上下文）、`qoderPoolSize` 按站点统计 |
| `internal/gateway/qoder_telemetry.go`（新增） | `risk` 计数器（queue/rate/rejected/version_gate + last）、内层错误 5 类计数与首次告警标记 |
| `internal/gateway/provider_failover.go` | 新增 `applyProviderFailoverOutcomeCtx`（旧函数委托，测试不用改）；hard/soft 两条分支前置 Qoder 策略；failover 入口安装账本、成功分支 flush |
| `internal/gateway/qoder_diag_handlers.go`（新增） | `GET /__qoder/telemetry`、`POST /__qoder/diag/queue`（1 轮 8-token 探针 + kind/advice 判定），均 `requireAdmin` |
| `internal/gateway/qoder_models.go` | 模型列表 GET 带体 `$kwm` 并参与签名；`qoderAccountOf` 传 `RefreshToken`；**列表按候选主机逐个尝试** |
| `internal/gateway/protocol_proxy.go`、`server.go` | 把客户端会话键带进 Qoder 上下文 |
| 测试 | `qoder_protocol_test.go` 改写 1 例 + 新增 5 例；`qoder_chat_test.go` 改写 4 例 + 新增 3 例；`qoder_stream_test.go` 新增 1 例；`qoder_tool_echo_test.go`（新增）13 例（含 6 子用例）；`qoder_queue_test.go`（新增）8 例；`qoder_tool_history_test.go`（新增）5 例（含 4 子用例）；`qoder_host_fallback_test.go`（新增）6 例；`qoder_telemetry_test.go`（新增）8 例；`qoder_checkin_headers_test.go`（新增）3 例；`provider_cooldown_qoder_test.go`（新增）6 例；`qoder_chat_test.go` 另新增 system 替换/内嵌 prompt 2 例；`qoder_error_test.go`（新增）3 例 |

### 10.1 本轮顺带修掉的真实缺陷

- **上游连接泄漏**：`qoderChunkReader.Close()` 原本是空实现，而 `response.Body` 已被换成 reader，等于每个 Qoder 请求读完都不关上游 TCP 连接。现在 reader 持有上游 body 并在 `Close()`/重开时关闭（`TestQoderReaderClosesTheUpstreamStream`、`TestQoderReloadClosesTheAbandonedStream`）。
- **带引号的信封状态被整帧丢弃**：`statusCodeValue` 按 `int` 解析，上游发 `"403"` 时整帧不可解析 → 拒绝凭空消失（`TestQoderStreamReadsAQuotedEnvelopeStatus`）。
- **无名工具调用上线**：上游分帧给出 name 时，原先会把 `name:""` 发给客户端，导致执行器匹配不到、反复重试同一轮。

验证：`go build ./...`、`go vet ./...` 干净；`go test ./...` 全绿（gateway 88 s）。

### 10.2 线上验证（GC VPS `173.249.207.202`，版本 `20261105.154621`）

构建与发布按 `deploy/deploy-linux.sh` 的步骤复刻（本机 WSL 无发行版，无法直接跑 bash 脚本）：前端带 `GATEWAY_APP_VERSION` 构建、后端交叉编译 `linux/amd64` 并注入 `BuildVersion`、打包 scp、服务器端备份→替换→`systemctl restart`→健康检查（失败自动回滚）。**工作区有未提交改动，故本次发布属 `--allow-dirty` 级别：产物可回滚但无法溯源到 commit**（`vcs.modified=true`，revision 仍是 `7944d84`）。

| 验证项 | 结果 |
|---|---|
| 服务/公网 | `systemctl active`、本地 `health=200`、`https://llm.lucadesign.uk/__health` 200 |
| 版本一致性 | `__app/version` = `20261105.154621`（前后端同值） |
| 账号池 | 线上 41 个 provider，其中 **19 个 Qoder**（cn + pat 两区） |
| `/__qoder/telemetry` | 正常返回 `risk` / `innerErrors` / `queueBudget{rounds:3, waitMaxSecs:180, budgetSecs:600, singleWaitSecs:60}` |
| `POST /__qoder/diag/queue` | 真实账号探针：`kind=ok, status=200, elapsedMs=3538, identitySource=stored` — **签名 / 客户端身份 / 23 键体在真实上游一次通过** |
| 真实对话（纯） | `pong`，1.7 s；`prompt_tokens=2567, cached_tokens=2549` → **前缀缓存命中 99.3%** |
| 真实对话（带 system） | 模型遵守所给 system（"terse test fixture"）→ **替换语义生效** |
| 真实对话（带工具） | `finish_reason=tool_calls`、`get_time` + `call_…` id → 工具声明与回读链路通 |
| 部署后日志 | 无 error / panic；无 Qoder 账号被误冷却；唯一 WARN 是既有的 WorkBuddy 凭证续期（部署前即存在） |

> 说明：`cache-test` 端点对 Qoder 按既有设计跳过（返回 `skipped=true`），缓存效果以 `cached_tokens` 为准，见上表。

## 11. 2026-11-10 排查：签到领不到权益 / 模型只剩一两个

对照参考库最新 `origin/main`（v1.9.4）。**结论：领取权益的地址没有变，模型变少也不是解析问题。**

### 11.1 地址没变（实测）

参考库 v1.9.4 仍然用：

```
PATH_CAMPAIGNS        = "/sash/api/v1/me/campaigns"
PATH_CAMPAIGN_REWARD  = "/sash/api/v1/me/campaigns/%s/reward"
PATH_CAMPAIGN_CLAIM   = "/sash/api/v1/me/campaigns/%s/claim"
COSY_VERSION          = "1.1.75"     # 与我们一致
```

线上直连上游实测（CN 账号 `https://openapi.qoder.com.cn`）：

| 请求头 | HTTP | showCampaign | campaigns |
|---|---|---|---|
| 桌面头（clienttype 10 + version 0.4.8） | 200 | true | **2** |
| 桌面头（不带 version） | 200 | true | 2 |
| **纯账号头（无 cosy）** | 200 | true | **0**（被静默过滤） |

国际站同理：桌面头、**不带任何机器头** → 200、`showCampaign=true`、1 条活动。

### 11.2 签到失败的真因：硬失败，不是地址

`check-in: failed` 每天每账号都在报，原因是 **intl 站点在「读不到本机设备身份」时直接拒绝发送**：

```
Qoder 国际版签到要用本机 Qoder 客户端读设备身份，这台机器上没找到它…
```

GC 是台 Linux 服务器，`/opt/Qoder/resources/umid/runtime-info` 与 `~/.qoder/.bin` 都不存在 —— **永远读不到**。而上面的实测证明：不带机器头，上游照样返回活动列表。所以「必须先有原生身份」这个前提是错的。

> 时间线核对：11-03～11-04（本次对齐改动之前）已有 **494 次** `check-in: failed`，所以这不是本轮引入的回归，是这台无桌面环境的机器从一开始就撞上的设计缺陷。

**已修**（`qoder_checkin.go`）：

- 读不到原生身份 → **照常发请求**，只是不带六个机器头（不发假身份，参考库也警告派生头会被整条过滤）。
- 列表为空且本次没带上身份 → 结果里写明原因和解决办法，而不是一句「今天没活动」。
- 顺序仍是「先轮换设备令牌，后回退账号身份」，与参考一致。
- 回归测试从「必须报错且不发请求」改写为「必须发请求、且空列表要解释自己」。

线上验证（版本 `20261110.024915`）：

| 账号 | 结果 |
|---|---|
| CN `qoder-cn-账号` / `-2` | `done`（今日已领，领取链路正常） |
| INTL `qoder-账号-5` | `inactive` + 明确原因（该账号只有 `VIEW_DETAILS` 活动，无 `CLAIM_BENEFIT`） |

### 11.3 模型变少的真因：上游把 `enable` 置 false

逐个账号读 `oauth_json.models.app`：

| 账号区 | app 条目 | `enable: true` | 说明 |
|---|---|---|---|
| CN（12 个账号） | 13 | **13** | 全部可用 |
| INTL（多数） | 16 | **1**（仅 `lmodel` = QoDuck，`is_free: true`, `tags:["free"]`） | 其余 15 个被禁用 |
| INTL `qoder-auto-yd1ykc` | 16 | **16** | 唯一仍全开的国际账号 |

条目里**没有任何 `disable_reason` 字段**，字段集为：`key/display_name/enable/status/is_lite_plan/is_free/tags/is_sensitive/price_factor/is_editable/context_config/format/is_default/is_reasoning/is_vl/max_input_tokens/source/strategies`。

同一份代码、同一条 COSY 身份下，`qoder-auto-yd1ykc` 仍 16/16，CN 全部 13/13 —— 所以**不是客户端版本或身份被降级**，是这些国际账号的套餐/额度不在了，上游只留免费模型。

> 附带结论：这些「只剩 1～2 个模型」的账号，与「活动列表里没有 CLAIM_BENEFIT」的账号高度重合 —— 同一个事实的两种表现：**账号权益已失效**。

### 11.4 顺带发现（未处理）

- **HEAD 上有既有红灯**：`TestWorkBuddyQuotaAsksThePackageDoorOnlyWhenItCounts/personal` 稳定失败（断言 `RunsOut == 18.04`，实际窗口是 `Credits Used: 33.3%`）。工作区未触碰 WorkBuddy，属其他提交遗留，与本轮无关。
- 参考库新增 `/credits/probe` 自动探测与「老网关 404 提示」，暗示**额度**端点可能有过变动；我们的 `/sash/api/v2/me/usage` 尚未验证是否受影响（本次未纳入）。

## 12. 2026-11-11 复核：参考库 v1.9.5 / v1.9.6 的更新与对齐结论

对照 `origin/main` = `b2ef2a4`（v1.9.6）。11-10 的实质改动只有一项：**企业 VPC 专属域**；v1.9.5 是面板动画修复 + README 赞助位。

| 参考侧更新 | 内容 | 对我们的链路 |
|---|---|---|
| **v1.9.6** `QD_CN_VPC_ENDPOINT` | 由实例名（`acme`）推导 CN 的官网 / OpenAPI / 推理网关三个主机；未配置时保持原行为，INTL 不变 | **无需对齐**，除非用户是 Qoder CN 企业 VPC 客户。我们没有按账号/实例覆盖主机的入口，若要支持需新增 provider 级 endpoint 覆盖 |
| v1.9.6 隔离补强 | VPC 不回退公网、重定向只允许本实例 HTTPS 默认端口、实例名 ≤55 字符、不跨区试探令牌 | 只在实现 VPC 时才需要，属安全设计参考 |
| v1.9.5 | 统计页呼吸点/流光动画闪烁修复 + README 赞助区块 | 面板与文档，**与反代无关** |
| v1.9.3 chunked 请求体 | 参考自己手写 `BaseHTTPRequestHandler`，只读 `Content-Length`，chunked 会被当成空 body → Anthropic 报 `messages is required` | **无需对齐**：Go 的 `net/http` 自动解 chunked，`io.ReadAll(io.LimitReader(...))` 与 `MaxBytesReader` 都按解码后字节算，`ContentLength=-1` 也不会误判超限 |
| v1.9.2 模型 app-only | 新增发现只出现在 app 场景的模型（`lmodel` = QoDuck） | **已覆盖**：我们本来就合并 `app` + `chat` 两个场景，实测 CN 账号能拿到 |
| 额度端点自动探测（11 个候选） | INTL 与 CN 额度接口不是一套，参考按候选矩阵探测并缓存 6h | **已覆盖**：实测 `/sash/api/v2/me/usage` 在 CN 与 INTL 均 200（返回 `displayMode`/`qoderUsage`），参考的 v1 候选反而全部 404 |

### 12.1 唯一实际缺口：企业组织字段没进身份 blob（已修）

v1.9.6 的变更说明里有一句「导入时**保留企业组织字段**」。对照后发现我们确实缺：

- 参考从 `/api/v1/userinfo` 取 `user_type` / `organization_id` / `organization_name`，写进 `identity` 再进 COSY blob（`qoder_accounts.py:1910-1911,4783-4786`）。
- 我们的 `qoderUserInfo` 只读 `id/uid/name/email`，凭证里也没有这三个字段 → blob 里 `user_type` 恒为 `personal_professional_trial`、org 恒空。
- 后果：**企业账号在上游看来是个「个人号」**，而活动列表与模型列表都按这个身份过滤。

已修（4 处）：`qoderUserInfo` 增加三种字段并兼容 snake/camel 两种拼写 → `domain.QoderCredential` 增加三个可选字段 → 签入与 PAT 交换两处落地 → `qoderAccount` 传给签名层（`qoderUserBlob` 早已按 9 键输出，只差喂值）。

> 存量账号的三个字段在下一次令牌刷新 / 重新登录前仍为空；重新登录即可补齐。


## 13. 附录：可直接落代码的常量表（参考实现口径）

```go
// 排队
QueueMaxRounds=3; QueueWaitMaxSecs=180; QueueWaitBudgetSecs=600; QueueWaitJitterSecs=2.0
QueueDefaultRetryAfter=30; QueueMode="visible"|"fast"|"wait"
// 冷却
SingleAccountClamp=3s; CooldownAuth=60s; CooldownSessionDead=300s(+停用)
CooldownTransient=15s; CooldownOther4xx=60s; QuotaExhausted112=1800s
QuotaDaily110=到本地 0 点，min 60s、cap 6h、+1s
// 三族
SoftRate  = 600→×2→7200（offset 1）
Breaker   = 3 次 / 1800→×2→21600（offset 3）
Degrade   = 5 次 / 600→×2→7200（offset 5）
ErrorCooldownWaitMax = 10s
// 体积 / 超时
MaxBodyBytes=29MB(413 payload_too_large); MaxPayloadBytes=50MB(413 invalid_request_error)
UpstreamTimeout=600s; SSEHeartbeat=5s; SSEIdleLimit=900s
// 会话 / 截短
ConversationTTL=6h; ConversationMax=4096; WindowKey="win:"+sha1(model␀firstUser[:400])
TurnKey="turn:"+sha1(model␀system[:200]␀lastUser[:400])
ToolResultKeep=2000; TailProtect=1; 标记 41 字符 / 85 字节、不含字数
MarkHoldWindow=17; HoldMaxChars=32768; HoldMaxFrames=200
```
