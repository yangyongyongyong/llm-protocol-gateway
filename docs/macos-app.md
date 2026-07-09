# macOS App（Wails）与 Web 暴露开关

## 产物

| 产物 | 用途 |
|------|------|
| `desktop/build/bin/LLM Protocol Gateway.app` | 日常本机 App（窗口 + 菜单栏） |
| `cmd/gateway` + LaunchAgent | 无 UI 常驻（可选） |

二者**二选一**占用 `18093` 端口，不要同时跑。

## 分发能力（别人拿到 App 能否直接用）

`./scripts/build-macos-app.sh` 会把下列内容打进 `.app/Contents/Resources`：

| 内容 | 路径 |
|------|------|
| 管理页 UI | `Resources/web-dist/` |
| cloudflared | `Resources/bin/cloudflared` |
| bun | `Resources/bin/bun` |
| Cursor bridge | `Resources/cursor-bridge/`（含 node_modules） |

运行时优先使用包内二进制，**无需**本机安装 Go / Node / brew / 仓库源码。

仍需注意：

- 目标机架构需匹配（当前默认 `arm64`；Intel 用 `ARCH=amd64 ./scripts/build-macos-app.sh`）
- 未做 Apple 公证时，首次打开可能需「右键 → 打开」或去掉隔离属性
- Cloudflare 登录证书仍写在用户目录 `~/.cloudflared`（正常）

## 用户数据目录（更新 App 不丢数据）

配置、SQLite、隧道配置、Cursor token 等**全部写在用户目录**，不在 `.app` 包内。替换 / 重装 `LLM Protocol Gateway.app` 不会删除这些数据。

| 内容 | macOS 默认路径 |
|------|----------------|
| 数据根目录 | `~/Library/Application Support/llm-protocol-gateway/` |
| 配置 | `…/config.json` |
| SQLite | `…/gateway.db` |
| Cloudflare 隧道配置 | `…/cloudflare/` |
| Cursor access token | `…/cursor/access-token` |
| cloudflared 登录证书 | `~/.cloudflared/` |

可用环境变量覆盖：`GATEWAY_CONFIG`、`GATEWAY_DB`。

设置页「数据目录」卡片会列出上述绝对路径并支持复制；菜单「文件 → 打开数据目录」可在 Finder 中打开根目录。API：`GET /__settings/paths`（也包含在 `GET /__state` 的 `dataPaths` 字段）。

## 构建

```bash
# 需要：Go、Node、Wails CLI（go install github.com/wailsapp/wails/v2/cmd/wails@latest）
./scripts/build-macos-app.sh
open "desktop/build/bin/LLM Protocol Gateway.app"
```

## Web 访问开关

- **关闭（默认，新装 App）**：HTTP 只绑 `127.0.0.1`，局域网 / 穿透打不开管理页；本机 App / 浏览器仍可用。
- **开启**：绑 `0.0.0.0`，可用局域网 URL；再开 Cloudflare 隧道可远程操作。
- UI 入口：「公网访问」页顶部，或「设置」页；菜单栏「网关 → 开启/关闭 Web 访问」。
- API：`PATCH /__settings/web-exposed`，body：`{"enabled":true|false}`。
- 关闭 Web 时若公网隧道在跑，会自动停止隧道。

安全提示：管理页**无登录**，勿对不可信网络长期暴露；远程优先走 Cloudflare，并仅在必要时段开启。

## 与 LaunchAgent 切换

LaunchAgent 标签：`com.luca.llm-protocol-gateway`。

- App 启动时若检测到 `18093` 被占用，会提示是否停止后台服务并由 App 托管。
- 也可菜单「网关 → 停止 LaunchAgent 后台服务」，或手动：

```bash
launchctl bootout "gui/$(id -u)/com.luca.llm-protocol-gateway"
```

从 App 切回无头：

```bash
# 先退出 App，再
launchctl bootstrap "gui/$(id -u)" ~/Library/LaunchAgents/com.luca.llm-protocol-gateway.plist
# 或按你现有的 install 脚本重新加载
```

## 无头模式

```bash
go build -o .cache/gateway-dev ./cmd/gateway
GATEWAY_ADDR=0.0.0.0:18093 ./.cache/gateway-dev
```

`GATEWAY_ADDR` 仍可覆盖绑定地址；此时 UI 切换 `webExposed` 会持久化偏好，但需去掉环境变量后重启才能真正 rebind。
