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
