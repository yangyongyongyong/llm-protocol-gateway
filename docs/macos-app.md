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

## 公网访问的三种方式

1. **临时公网地址（Cloudflare Quick Tunnel）**：无需登录，适合测试；地址是临时的 `*.trycloudflare.com`，管理页与 API 共用一个地址。
2. **Cloudflare 自有域名**：登录 Cloudflare 后使用账户内的域名，由网关创建具名隧道以及 API/UI 的 DNS 路由。
3. **外部 DNS / 自有反代**：用户只填写 API/管理页域名。网关不创建隧道、不修改 DNS、不申请证书；DNS、TLS、端口转发和反向代理均由用户在其他平台维护。

外部 DNS 是网关级全局配置，所有用户和 API Key 共用同一组 API / 管理页域名，不按用户或 Key 拆分。该模式仍使用 Host 分离：API 域名只提供模型协议和 `/__health`，管理页域名只提供控制台。`PATCH /__public-access` 保存时 `mode=external_dns`，`provider=external`。

页面底部的「公网开关自动化令牌」只允许本机/局域网脚本调用 `POST /__public/control` 开关公网入口，不是模型 API Key，也不能管理 Provider、密钥或用户。

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

## App 自动更新

桌面 App 内置原生更新器（`internal/desktopupdate`），窗口关闭后仍随 App 进程检查：

- 启动约 5 秒后检查一次，之后按设置每 1 / 6 / 24 小时检查；
- 默认自动检查、自动下载、空闲时自动重启安装；
- 自动安装会等待网关没有模型请求；手动点击安装则立即重启；
- macOS 只替换完整 `.app`，不会覆盖正在运行的可执行文件；
- 更新地址：`https://github.com/yangyongyongyong/llm-protocol-gateway/releases/latest/download/update.json`；
- 如果最新 Release 暂时没有 `update.json`，会回退到 GitHub Releases API；
- 每份安装包必须匹配清单中的 SHA-256；
- 设置页「App 自动更新」可以打开/关闭自动检查、自动下载和空闲自动安装。

发布工作流会为每个 Release 生成 `update.json`。如果仓库配置了更新签名：

1. `UPDATE_PUBLIC_KEY`：Repository variable，放 Ed25519 raw public key 的 base64；
2. `UPDATE_SIGNING_KEY`：Repository secret，放 Ed25519 private key 的 PEM 文本。

签名会生成 `update.json.sig`。公钥固定在本仓库的 `internal/desktopupdate/update_public_key.txt` 并自动编进 App，不依赖构建机环境变量；`UPDATE_SIGNING_KEY` 私钥只保存在 GitHub Secret。`UPDATE_PUBLIC_KEY` 仅作为 CI/发版的显式覆盖值。

生成一对发布签名密钥：

```bash
openssl genpkey -algorithm ED25519 -out update-signing-key.pem
openssl pkey -in update-signing-key.pem -pubout -outform DER \
  | tail -c 32 | base64 > update-public-key.txt
```

`update-public-key.txt` 的内容提交到 `internal/desktopupdate/update_public_key.txt`（如有轮换，同时更新 `UPDATE_PUBLIC_KEY`）；`update-signing-key.pem` 的内容只放到 `UPDATE_SIGNING_KEY`。私钥不要提交到仓库。

## 版本号唯一来源

发布版本的唯一输入是 release tag（例如 `v1.0.20`）。构建脚本必须把同一个版本写入：

- 前端 `__APP_VERSION__`
- 网关 `gateway.BuildVersion`
- 桌面 `desktop.version`
- macOS `Info.plist` 的 `CFBundleShortVersionString` 与 `CFBundleVersion`

`desktop/wails.json` 里的 `productVersion` 只是 Wails 的本地占位值，发布时会被构建脚本覆盖；不要再把它当成版本源。发布 workflow 会在上传前校验以上字段，缺一项即失败，避免出现清单写 1.0.20、安装包写 0.1.3 的错位。
