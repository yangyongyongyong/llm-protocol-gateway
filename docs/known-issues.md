# 已知问题 / 踩坑记录

## 1. Claude thinking 尾块导致连环静默（已修复）

**现象**：长会话下，Codex 某一轮"想了一下"但完全不回复、`task_complete` 里
`last_agent_message` 为 `null`，之后每一轮都重复该现象，会话事实上卡死；严格的上游会直接 400
（`messages.N: The final block in an assistant message cannot be 'thinking'.`），宽松的第三方中转
则不报错但静默复读同样的空转。

**根因**：某轮模型在 thinking 后被打断（无 text/tool_use），历史里留下一条以裸 `thinking` 块结尾
的 assistant 消息。下一轮网关把 Responses `reasoning` item 还原成 Claude `thinking` 块时，若后面
紧跟 user 消息（中间没有真实输出），转换出来的请求就是"assistant 消息以 thinking 收尾"——本身
非法，且模型看到裸 thinking 结尾的历史后倾向继续只输出 thinking，形成连环静默。

**修复**：
- `internal/gateway/convert_responses_claude.go` 的 `dropTrailingAssistantThinking`：清理 assistant
  消息末尾的裸 thinking/redacted_thinking 块；纯 thinking 的 assistant 轮整条丢弃并合并相邻同角色
  消息。尾部 thinking 模型不会回读，零上下文损失。
- `internal/gateway/thinking_rectifier.go`：签名整流重试的错误匹配增加了该错误文案，命中同类 400
  时也走剥离重试。
- 增加"上游 200 但输出仅有 thinking、无 text/tool_use"的检测（`isThinkingOnlyEmptyOutput`）：流式
  与非流式转换都会在判定为该情况时自动同 Provider 重试一次（`timing_flags` 里可见
  `thinking_only_retry`），重试仍失败才把原样结果透传给客户端。

## 2. thinking 块 "cannot be modified" 报错（已修复）

**现象**：Codex（Responses→Claude）回放历史 / 最新 thinking 块时，与 Anthropic 原始签名不一致，
上游返回 `messages.N.content.M: 'thinking' or 'redacted_thinking' blocks in the latest assistant
message cannot be modified. These blocks must remain as they were in the original response.` 类 400。

**根因**：网关有两条发往 Claude 的上游出口，此前只有原生透传（`proxyClaudeMessages`）接了 thinking
签名整流器；Codex 走的**转换路径**（`proxyConvertedThroughClaude`）没接，导致该类 400 直接透传给
客户端。

**修复**：把签名整流器抽成 resend 无关的核心（`maybeRectifyClaudeThinkingResend`），转换路径在上游
400 时也接入整流+重试，用匹配的 Accept 头同 Provider 透明重试一次；检测文案对齐
[`farion1231/cc-switch`](https://github.com/farion1231/cc-switch)（新增 `cannot be modified`、Gemini
`Thought signature is not valid`、`Extra inputs are not permitted`）。见
`internal/gateway/thinking_rectifier.go`、`internal/gateway/protocol_proxy.go`。

## 3. Codex `remote_compaction_v2` 在自定义/第三方 Provider 下无法保证成功（未修复，非本项目缺陷）

**现象**：长会话触发 Codex 远程上下文压缩时报
`Error running remote compact task: Fatal error: remote compaction v2 expected exactly one compaction
output item, got 0 from N output items`。网关侧这次请求往往是正常 200，`timing_flags` 也无异常
标记——响应本身不空，只是形状不满足 Codex 要求。

**根因**：Codex 的 remote compaction 校验器要求响应里**恰好 1 个**认可的输出项；但带 thinking 的
模型天然把回答拆成 `reasoning` + `message` 两个 output item，凑不齐"恰好 1 个"。这是 Codex 客户端
侧未公开的强校验，属于所有自定义/第三方 Provider 的通用缺陷，不是本网关的转换 bug——参考
[`farion1231/cc-switch`](https://github.com/farion1231/cc-switch) 的
[#4030](https://github.com/farion1231/cc-switch/issues/4030)、
[#4725](https://github.com/farion1231/cc-switch/issues/4725) 等 issue，MiniMax / DeepSeek 等原生
支持 `/v1/responses` 的官方 Provider 也同样复现，社区折腾数月未能在代理层可靠绕过。

**应对方式**：关掉 Codex 的 `remote_compaction_v2`，让它走本地压缩：

```bash
codex features disable remote_compaction_v2
```

本项目生成 Codex 配置时已默认带上（见 `web/src/main.tsx` 的 `buildApiKeyCodexConfigPatchScript`）：
- **"复制修改脚本"**：写完 provider 配置后自动尝试执行上述命令（找不到 `codex` 或失败只打印提示，
  不影响 provider 配置写入）。
- **"仅复制配置内容"**：纯文本无法执行命令，只在文件头加一行注释提醒手动执行。
- **"还原为官方 provider"**：对称执行 `codex features enable remote_compaction_v2` 恢复默认。

手动配置 Codex（未走本项目脚本）需自己执行一次上述命令；改动后重启 Codex 桌面 App 才生效。

## 4. 控制台 Cloudflare 授权轮询的漏定时器（已修复，复现窗口极小）

**现象（代码审查）**：`waitForCloudflareAuthorization` 早先是 `void check()` 之后立刻
`setInterval`，而收尾时清的是「当时已经装上的那只」。若后端响应够慢，让第二次检查先回来、
第一次检查后回来，第一次检查的收尾清掉的就是定时器 id，随后装上来的那只再也没人停 ——
每 2 秒请求一次 `login/status`（授权后还连带取 `zones`），每次 `setState` 重渲染整个控制台。

**为什么记下来**：按旧顺序实测扫过 0/5/50/1000/2500/10000 ms 的后端延迟，以及
`document.hasFocus()` 为假的沙箱环境，都没能稳定复现（多数时序下挂上来的定时器第一跳
正好落在收尾之后，自己把自己清掉了）。所以这是正确性隐患，不是可稳定触发的缺陷。

**修复**：把驱动抽成 `web/src/pollers.ts` 的 `runCloudflareAuthPoll`：等第一次检查回来再
决定装不装；检查在飞时不叠加第二次；收尾统一走 `finish()`（先 `settled` 再清定时器）。
`web/src/lib.test.mjs`（`npm run test:lib`）把「收尾后跳多少次都不再发请求」「在飞不叠加」
钉住，旧顺序下断言会红。

## 5. 只有 thinking 的空回答兜底轮次：兜底账号的答案到不了客户端（已修复）

**现象**：一条转换后的流式请求（如 Chat 客户端 → Claude 上游），第一个账号只发出
开口（`message_start` + 一个空的 `thinking` 块）就结束，被判为「只有 thinking 的空
回答」，本应换兜底账号重答；实际客户端拿到 5xx，兜底账号的完整回答丢失。

**根因**：转换流路径把「只有 thinking 的空回答」与「被截断的流」混为一谈：空回答先按
截断报出，随后这次的错误又被当成传输错误继续走兜底链，于是兜底账号那份完整、合法的
JSON 正文也被判成截断，客户端拿到 502。

**复现**：`internal/gateway/thinking_only_failover_test.go` 的
`TestEmptyTurnWithinHoldLimitGoesToTheBackup`（修复后已去掉 Skip，作为回归），以及
一直成立的不变量 `TestSlowEmptyTurnIsNotAnsweredTwice`。

**修复**（`64d8ab1`）：转换流路径区分两者——空回答按「同 Provider 重试」的语义交给空流
重试与兜底链，兜底账号的完整 JSON 正文不再参与事件流的截断判定。

## 6. Gemini Responses 空回复及工具调用续聊

**现象**：2026-11-25 14:29:05，`pod` Provider 的 `gemini-4-hydrogen` 返回 HTTP 200、
`output_tokens=538`，但 Responses 输出为空、Codex 会话结束。14:59:41 则返回
`response.incomplete`，原因是 `empty_visible_output`。

**证据边界**：这两条日志只保存了转换后的 Responses 流，并未保存 Gemini 原始响应，
不能断言上游一定只返回思考内容，也不能将这两次问题直接归因到某一个已确认的解析错误。

**代码差异与修复**：对照 Magpie 的 Gemini 解码器、Responses 输出及空回复处理：
- `oceanPodProxy` 生成的 Responses 事件补全递增 `sequence_number`、进行中的工具 item、
  `function_call_arguments.delta/done`，并将工具前的助手文字标记为 `commentary`，
  确保 Codex 继续执行工具而非把中间消息视为最终回答。
- Gemini 的 STOP 空回复（含无候选但有用量）在代理中最多重试两次，优先同 key；各次费用
  按实际 key 和区域分别记账。连续为空返回明确的 `empty_response` 错误，不再返回
  `empty_visible_output` 的 incomplete。MAX_TOKENS、安全过滤、429 不按空回复重试。
- Responses 直通空回复保护在判断前检查终态的 `output` / `output_text`，保留 refusal、
  usage；心跳不插入半帧，混合 LF/CRLF 按最早帧边界解析，避免把已有正文误判为空。
- 回归测试覆盖文本与工具同时输出、真实签名回传、空签名尾帧、重试记账、过滤和长度终态；
  附隔离的真实 Codex CLI → 网关 → 模拟 Gemini 工具调用 → 工具结果 → 下一轮测试。

**部署状态**：源码修复需重新构建并替换系统 App，代理改动需在不影响当前会话时重启
PyCharm 管理的进程后生效；不要把磁盘代码与正在运行的版本混为一谈。
