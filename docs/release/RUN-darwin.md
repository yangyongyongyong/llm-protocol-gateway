# macOS 启动说明

解压后得到：

```
start.sh         启动器（把管理页指到同级 web-dist 后启动）
gateway          无界面网关
gatewayctl       AI / 脚本管理 CLI（输出 JSON）
web-dist/        管理页
RUN.txt          本文件
VERSION          版本与源码修订号
```

自检后直接运行：

```bash
chmod +x start.sh gateway
./start.sh
```

另一个终端可以直接操作同一实例：

```bash
./gatewayctl status
./gatewayctl upstream list
```

`start.sh` 只是设定 `GATEWAY_WEB_DIST` 后 exec `gateway`；直接跑 `./gateway` 时管理页会 404，除非自己设这个变量。

默认监听 `0.0.0.0:18093`，浏览器打开 `http://127.0.0.1:18093` 即是管理页；健康检查 `GET /__health`。

首次运行若被 Gatekeeper 拦下（本包未做 Apple 公证），执行一次：

```bash
xattr -dr com.apple.quarantine ./gateway
```

或者用右键 →「打开」。

## 常驻运行

用 launchd 托管，写 `~/Library/LaunchAgents/com.luca.llm-protocol-gateway.plist`：

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.luca.llm-protocol-gateway</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Applications/LLMProtocolGateway/gateway</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>GATEWAY_ADDR</key><string>0.0.0.0:18093</string>
    <key>GATEWAY_WEB_DIST</key><string>/Applications/LLMProtocolGateway/web-dist</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/tmp/llm-protocol-gateway.log</string>
  <key>StandardErrorPath</key><string>/tmp/llm-protocol-gateway.log</string>
</dict>
</plist>
```

```bash
launchctl bootstrap "gui/$(id -u)" ~/Library/LaunchAgents/com.luca.llm-protocol-gateway.plist
```

## 可选依赖

| 能力 | 需要 |
| --- | --- |
| Cloudflare 公网访问 | `cloudflared` 在 PATH 中（`brew install cloudflared`），或设置 `GATEWAY_CLOUDFLARED` |
| Cursor Provider | `node`（≥ 18）或 `bun` |
| RTK 工具输出压缩 | `rtk` 在 PATH 中 |

## 数据与升级

- 数据默认写在 `~/Library/Application Support/llm-protocol-gateway/`，可用 `GATEWAY_DB` 改到别处。
- 升级：退出进程 → 用新包内的 `gateway` 与 `web-dist/` 覆盖 → 重新启动。数据库自动迁移。
- 该目录包含上游密钥与 OAuth 令牌，请纳入备份并限制访问。

## 架构

包名结尾是 `darwin_arm64`（Apple 芯片）或 `darwin_amd64`（Intel）。用「关于本机」确认芯片型号。

## 桌面 App

想要窗口版而不是无界面进程，下载安装包里的 `llm-protocol-gateway-desktop-darwin-arm64.zip`（Intel 用 `-amd64`），
解压后把 `LLM Protocol Gateway.app` 拖进「应用程序」。它自带管理页、cloudflared、bun 与 Cursor 桥接，无需另装依赖。
未公证，首次打开需要右键 →「打开」，或 `xattr -dr com.apple.quarantine "/Applications/LLM Protocol Gateway.app"`。
