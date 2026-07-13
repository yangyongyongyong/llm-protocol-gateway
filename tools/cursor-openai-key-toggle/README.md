# OpenAI Key Toggle

快捷键切换 Cursor 设置里的 **OpenAI API Key** 开关（走内部命令 `aiSettings.usingOpenAIKey.toggle`，改内存态）。

## 快捷键

| 平台 | 快捷键 |
|------|--------|
| macOS | `⌘ ⌥ ⇧ K` |
| Windows / Linux | `Ctrl+Alt+Shift+K` |

> 不用 `⌘⇧K`：Agent 界面里它是「Scroll Agent Panel Up」（会话向上滚）。

也可在命令面板搜索：`Toggle OpenAI API Key`

## 安装 / 更新

```bash
./install.sh
```

然后在 Cursor 执行 **Developer: Reload Window**。

## 前提

- 已在 Cursor Settings → Models 里配置过 API Key（和可选的 Base URL）
- 若从未保存过 Key，内部命令会弹出配置框而不是切换
