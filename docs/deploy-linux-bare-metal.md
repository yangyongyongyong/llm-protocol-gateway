# Linux 裸机部署（systemd + 原生二进制，无 Docker）

把网关直接部署到一台干净的 Linux 服务器上：**纯 Go 静态二进制 + 前端静态资源 + SQLite**，
由 systemd 常驻托管。全程不需要 Docker。

适用系统：Ubuntu / Debian / CentOS / Rocky 等主流发行版，`x86_64` 与 `arm64` 均可
（SQLite 使用纯 Go 实现，无 CGO、无外部动态库依赖）。

> 不想在服务器上装 Go/Node？直接用 [附录 B：本地交叉编译](#附录-b本地交叉编译服务器免装-gonode)，
> 只上传两个产物即可。

---

## 0. 部署形态与目录规划

网关由三部分组成，部署时只需要关注它们的落点：

| 组件 | 说明 | 本文规划路径 |
| --- | --- | --- |
| 网关二进制 | `go build` 产物，单文件 | `/opt/llm-protocol-gateway/bin/gateway` |
| 前端静态资源 | `web/dist`（**不嵌入二进制**，必须单独放置） | `/opt/llm-protocol-gateway/web/dist` |
| 数据 | SQLite（Provider / Key / 日志 / 用量 / 管理密码哈希） | `/var/lib/llm-protocol-gateway/gateway.db` |

服务账号：`llmgw`（无登录 shell 的系统用户）；监听端口：`18093`。

执行步骤总览：装依赖 → 装 Go/Node → 拉源码 → 构建 → 建账号与数据目录 → 前台试跑 →
systemd 常驻 → 防火墙 → 首次登录配置 → 环境验证。每一步都带验证命令。

---

## 1. 系统准备

```bash
sudo apt update
sudo apt install -y ca-certificates curl git
```

CentOS / Rocky 对应：

```bash
sudo dnf install -y ca-certificates curl git
```

`ca-certificates` 必须装：网关要出站 HTTPS 访问上游 API，缺证书会全部报 TLS 错误。

---

## 2. 安装构建工具链（方案 A：服务器上直接构建）

### 2.1 Go 1.26.x

`go.mod` 声明 `go 1.26.1`，推荐直接装官方最新 1.26.x：

```bash
cd /tmp
curl -fLO https://go.dev/dl/go1.26.1.linux-amd64.tar.gz   # arm64 换成 go1.26.1.linux-arm64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.26.1.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh
source /etc/profile.d/go.sh
go version    # 应输出 go version go1.26.1 linux/amd64
```

- 国内下载慢可换 `https://golang.google.cn/dl/`；构建拉模块慢可先 `export GOPROXY=https://goproxy.cn,direct`。
- 若已有较老的 Go（≥1.21），默认 `GOTOOLCHAIN=auto` 会在构建时自动下载匹配工具链，也能编译。

### 2.2 Node 22（只用于构建前端）

```bash
curl -fsSL https://deb.nodesource.com/setup_22.x | sudo -E bash -
sudo apt install -y nodejs
node -v    # v22.x
```

CentOS / Rocky 用 `sudo dnf module install nodejs:22`（或 https://rpm.nodesource.com/setup_22.x）。

国内 npm 慢可换源（可选）：`npm config set registry https://registry.npmmirror.com`

---

## 3. 获取源码

```bash
sudo mkdir -p /opt/llm-protocol-gateway
sudo chown "$USER":"$USER" /opt/llm-protocol-gateway
git clone https://github.com/yangyongyongyong/llm-protocol-gateway.git /opt/llm-protocol-gateway
cd /opt/llm-protocol-gateway
```

可选：固定到某个发布标签，避免跟随 main 上未发版的提交：

```bash
git checkout v0.3.0    # 以 `git tag` 列出的最新标签为准
```

---

## 4. 构建

### 4.1 构建前端（产出 web/dist）

```bash
cd /opt/llm-protocol-gateway/web
npm ci            # 严格按 package-lock.json 安装
npm run build     # tsc -b && vite build → web/dist
ls dist/index.html   # 必须存在；网关靠它识别 UI 目录
```

### 4.2 构建后端（产出静态二进制）

```bash
cd /opt/llm-protocol-gateway
mkdir -p bin
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/gateway ./cmd/gateway
file bin/gateway    # 应显示 "statically linked"
```

> 两个产物缺一不可：网关只对外提供 API 时 `web/dist` 也建议构建——
> 管理控制台（登录、Provider 管理）就在里面。

### 4.3 构建溯源：线上二进制必须能对应到一个 commit

`go build` 会把当前 commit 写进二进制（`vcs.revision` / `vcs.modified`）。**只有从干净工作区
构建的产物才允许上服务器**：带着未提交改动（`vcs.modified=true`）或 revision 落后于 `HEAD`
的包，事后没人能证明线上跑的是哪份代码，也无法按 commit 回滚。

```bash
git status --porcelain --untracked-files=no   # 必须是空；有输出就先提交
go version -m bin/gateway | grep vcs
#   build	vcs=git
#   build	vcs.revision=<完整 commit>
#   build	vcs.modified=false

./scripts/verify-build-provenance.sh bin/gateway HEAD   # 一条命令做完上面两步，失败即非零退出
```

`scripts/verify-build-provenance.sh` 可对任意 Go 二进制运行（包括交叉编译的 linux 产物，只需
`go version -m`，不需要能执行它），也可用于检查已部署的文件：

```bash
ssh root@<服务器> 'go version -m /opt/llm-protocol-gateway/bin/gateway | grep vcs'  # 服务器上有 go 时
sudo ./scripts/verify-build-provenance.sh /path/to/gateway <期望的 commit>           # 或把文件拿回本地查
```

> **构建目录有讲究**：不要在 git 仓库之外的临时目录（如 `/tmp`）里编译——`go build` 找不到
> 仓库时会静默跳过 VCS 标记，二进制里根本没有 `vcs.*`，校验脚本会直接拒绝。
> 用 `deploy/deploy-linux.sh` 部署时以上检查已内置：默认拒绝脏工作区，构建后校验
> `vcs.modified=false` 且 `vcs.revision == git rev-parse HEAD`，任一不满足就不上传；
> 仅应急可用 `ALLOW_UNVERIFIED_BUILD=1` 放行（会打印醒目警告）。

---

## 5. 创建服务账号与数据目录

```bash
sudo useradd -r -s /usr/sbin/nologin -d /var/lib/llm-protocol-gateway llmgw
sudo mkdir -p /var/lib/llm-protocol-gateway
sudo chown -R llmgw:llmgw /var/lib/llm-protocol-gateway
sudo chmod 700 /var/lib/llm-protocol-gateway
```

- 数据目录 700；SQLite 文件由程序自动创建为 600。
- 把 `llmgw` 的家目录指向数据目录，后续可选的 Cloudflare 隧道凭据、Cursor 令牌都会落在这里，备份只需一个目录。

---

## 6. 前台试运行（验证构建产物）

```bash
sudo -u llmgw env \
  GATEWAY_ADDR=0.0.0.0:18093 \
  GATEWAY_DB=/var/lib/llm-protocol-gateway/gateway.db \
  GATEWAY_WEB_DIST=/opt/llm-protocol-gateway/web/dist \
  /opt/llm-protocol-gateway/bin/gateway
```

另开一个终端验证：

```bash
curl -s http://127.0.0.1:18093/__health            # {"status":"ok",...}
curl -s http://127.0.0.1:18093/__state | head -c 300
curl -s http://127.0.0.1:18093/v1/models
```

浏览器打开 `http://<服务器IP>:18093/` 应能看到登录页（此时先不用设置密码）。
确认无误后回第一个终端 `Ctrl+C` 退出。

> 首次启动会在数据目录自动建库并完成 schema 初始化，日志会打印
> `database store path=...` 与 `gateway listening addr=0.0.0.0:18093`。

---

## 7. systemd 常驻

创建 `/etc/systemd/system/llm-protocol-gateway.service`：

```ini
[Unit]
Description=LLM Protocol Gateway
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=llmgw
Group=llmgw
WorkingDirectory=/opt/llm-protocol-gateway
# HOME 指向数据目录：可选的 Cloudflare / Cursor 相关文件也落在这里，备份集中
Environment=HOME=/var/lib/llm-protocol-gateway
Environment=GATEWAY_ADDR=0.0.0.0:18093
Environment=GATEWAY_DB=/var/lib/llm-protocol-gateway/gateway.db
Environment=GATEWAY_WEB_DIST=/opt/llm-protocol-gateway/web/dist
Environment=GATEWAY_REPO_ROOT=/opt/llm-protocol-gateway
ExecStart=/opt/llm-protocol-gateway/bin/gateway
Restart=always
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now llm-protocol-gateway
systemctl status llm-protocol-gateway --no-pager
sudo journalctl -u llm-protocol-gateway -f     # 实时日志，Ctrl+C 退出查看
curl -s http://127.0.0.1:18093/__health
```

说明：

- **设了 `GATEWAY_ADDR` 后**，UI 里的「局域网暴露」开关不会生效（页面会提示需去掉该变量重启）——
  服务器场景这是预期行为，绑定地址以 systemd 配置为准。
- 想再加严隔离，可加 `ProtectSystem=strict` + `ReadWritePaths=/var/lib/llm-protocol-gateway`；
  但若之后要用 UI 里的 Cloudflare 命名隧道（会写 `$HOME/.cloudflared`）或 Cursor 相关功能，
  需要保持 HOME 可写，按需取舍。
- 同一端口只允许一个实例：重复启动会 fail-fast 退出（日志提示 `cannot bind gateway listen address`），
  不会出现两个实例抢隧道的情况。

### 可选：RTK 工具输出压缩

Provider 菜单里的「RTK 压缩」会在转发前调用 [rtk](https://github.com/rtk-ai/rtk) 过滤
`tool_result` 里的命令输出（git status/diff、go test、grep 等）。`deploy/deploy-linux.sh`
默认检测服务器上的 `rtk`，缺失时用官方安装脚本装到 `/usr/local/bin`（SHA-256 校验）：

```bash
# SERVER_HOST 为必填项（脚本不再内置默认主机），所有示例都需带上
SERVER_HOST=<服务器地址> ./deploy/deploy-linux.sh                      # 缺失时自动安装最新版
SERVER_HOST=<服务器地址> RTK_VERSION=v0.50.0 ./deploy/deploy-linux.sh  # 安装/升级到指定版本
SERVER_HOST=<服务器地址> INSTALL_RTK=always ./deploy/deploy-linux.sh   # 每次部署都升级
SERVER_HOST=<服务器地址> INSTALL_RTK=never ./deploy/deploy-linux.sh    # 跳过，不碰服务器上的 rtk
```

手动安装：

```bash
curl -fsSL https://raw.githubusercontent.com/rtk-ai/rtk/refs/heads/master/install.sh \
  | sudo RTK_INSTALL_DIR=/usr/local/bin sh
rtk --version
```

`/usr/local/bin` 在 systemd 默认 PATH 里，上面的 unit 不用改；如果 unit 自定义了
`Environment=PATH=`，补一行 `Environment=GATEWAY_RTK_PATH=/usr/local/bin/rtk`。
装好后在 Provider 菜单开启「RTK 压缩」，网关会在 30 秒内自动探测到 rtk，无需重启；
用量统计页的「RTK 节省」卡片会显示每日节省，压缩失败或超时会自动透传原文。

---

## 8. 防火墙

```bash
# Ubuntu / Debian (ufw)
sudo ufw allow 18093/tcp

# CentOS / Rocky (firewalld)
sudo firewall-cmd --permanent --add-port=18093/tcp && sudo firewall-cmd --reload
```

云服务器还需在控制台安全组放行 18093。更稳妥的做法：只放行内网/办公网段，
或按第 11 节加反向代理走 443 + HTTPS。

---

## 9. 首次登录与配置

1. 浏览器打开 `http://<服务器IP>:18093/`，会自动跳转 `/login`。
2. 首次访问显示「设置管理密码」表单（至少 8 位），提交即完成注册并直接进入控制台
   （对应 `POST /__auth/setup`，该接口只在未设置密码时可用）。
3. 若服务器上不方便开浏览器，也可在服务器本机（SSH 里）用 curl 设置：

   ```bash
   curl -s -X POST http://127.0.0.1:18093/__auth/setup \
     -H 'Content-Type: application/json' \
     -d '{"password":"你的密码至少8位"}'
   ```

   > 从服务器本机 `127.0.0.1` 发起的请求天然免密（loopback 直连即管理员），
   > 这也是忘记密码时的恢复通道（见排障）。

4. 添加 Provider（控制台「Provider 管理」）：

   - **推荐 API Key / Bearer 类**：headless 服务器最稳，无需浏览器往返；
   - **Qoder / Qoder CN**：优先在浏览器里授权登录；headless 服务器改用「粘贴个人访问令牌」，
     填 `pt-` 开头的令牌即可，全程无浏览器往返；
   - **OAuth 类（Claude / ChatGPT / Cursor 账号）**：登录需交互式浏览器回调，
     无桌面服务器建议在桌面端登录后「导出 Provider → 服务器上导入」，或先配好
     公网域名/隧道后再在浏览器完成。

5. 创建对外 API Key，按需配置模型映射、思考深度、Provider 备选链。

6. 客户端接入地址（同一个 18093 端口，三种协议并存）：

   | 客户端协议 | Base URL |
   | --- | --- |
   | OpenAI Chat（Cursor 等） | `http://<服务器IP>:18093/v1` |
   | OpenAI Responses（Codex 等） | `http://<服务器IP>:18093/openai/v1` |
   | Claude（Claude Code 等） | `http://<服务器IP>:18093/anthropic` |

   冒烟验证（把 `$KEY` 换成控制台创建的对外 Key）：

   ```bash
   curl -s http://127.0.0.1:18093/v1/models -H "Authorization: Bearer $KEY"
   ```

---

## 10. VPS 环境验证（接入 Claude 前自检）

> VPS 有独立公网 IP，直连即可，**不需要 Cloudflare 隧道**（见第 12 节）。
> 但若用途是通过网关保持访问 Claude，建议先跑一遍环境自检：确认「服务器环境、
> 网络出口、时区/区域」互相一致——不一致的环境（如 IP 在美国、时区为 UTC 或
> 中国、Locale 为 zh_CN）是风控系统最容易抓的弱信号。

仓库自带只读检查脚本（不改动系统），部署后在服务器上执行：

```bash
bash /opt/llm-protocol-gateway/scripts/verify-vps-env.sh
# 方案 B（只上传产物、无源码）时，把 scripts/verify-vps-env.sh 上传到服务器再执行
```

脚本逐项检查并给出修复命令：

| 检查项 | 期望 | 不通过时的修复 |
| --- | --- | --- |
| 系统时区 / 偏移 | 与 VPS 所在地一致（默认按 `America/Los_Angeles` 校验） | `sudo timedatectl set-timezone America/Los_Angeles` |
| NTP 时钟同步 | 已同步（时钟偏移会导致 TLS 握手失败） | `sudo apt install -y systemd-timesyncd && sudo timedatectl set-ntp true` |
| Locale / Intl | `en_US.UTF-8`（或 C.UTF-8） | `sudo locale-gen en_US.UTF-8 && sudo update-locale LANG=en_US.UTF-8` |
| Emoji / 中文字体 | Emoji 字体存在（渲染为 Noto 风格而非方块） | `sudo apt install -y fonts-noto-color-emoji fonts-noto-cjk` |
| DNS 解析出口 | 递归解析器在美国 | 改用提供商 DNS 或 `1.1.1.1` / `8.8.8.8`（systemd-resolved 配置） |
| 出站 IPv4 归属 | 美国，且与所选区域（西海岸）一致 | 换 IP / 换机房；项目也支持给上游单独走代理（见 `start-with-ipfly.sh`） |
| 出站 IPv6（如有） | 与 IPv4 同区域；不一致建议禁用 v6 出口 | 在 `/etc/sysctl.d/` 禁用 IPv6，或确认 v6 路由同区 |
| Claude 端点连通 | `api.anthropic.com` 返回 401（可达且未被拦）、延迟正常 | 检查出站 443、安全组、DNS |
| IP 类型标记 | 了解该 IP 的 `proxy` / `hosting` 标记 | 机房 IP 属固有弱信号；必要时给上游加代理 |

浏览器侧的检查（**仅当用浏览器访问 claude.ai 网页版时才相关**，纯 API/客户端用法不受影响）：

| 检查项 | 检查入口 | 期望 |
| --- | --- | --- |
| WebRTC 泄露 | `https://browserleaks.com/webrtc` | 只显示 VPS 公网 IP；若在本机浏览器经代理访问而露出本机真实 IP 即泄露（Firefox 可关 `media.peerconnection.enabled`） |
| DNS 泄露 | `https://browserleaks.com/dns` | 解析器全部位于美国 |
| 时区 / Intl / 语言 | `https://browserleaks.com/javascript` | 时区 `America/Los_Angeles`，`navigator.language` 为 `en-US` |
| Emoji 渲染 | 同上页面 | 正常彩色渲染；显示方块即缺字体 |

> 实话实说：环境一致性只能消除**弱信号**，不能保证不被封。对 Claude 账号影响最大的实际因素：
> ① 订阅 OAuth 令牌用于第三方客户端本身就是风控关注项（官方 API Key 用法风险最低）；
> ② 同一账号被多地、多设备并发使用；③ 机房 IP 段被大量同类用途使用。
> 建议：全账号流量统一走这一台 VPS 出口、避免多地区同时登录、优先用 API Key Provider。

## 11. 反向代理 + HTTPS（可选，推荐对外服务时启用）

Caddy（自动申请证书，最省事）：

```
gateway.example.com {
    reverse_proxy 127.0.0.1:18093
}
```

nginx（注意 SSE 流式必须关 buffering）：

```nginx
location / {
    proxy_pass http://127.0.0.1:18093;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_buffering off;
    proxy_read_timeout 3600s;
}
```

配了反代后建议让网关只监听本机：

```bash
sudo systemctl edit llm-protocol-gateway
# 在 [Service] 段写入：
# Environment=GATEWAY_ADDR=127.0.0.1:18093
sudo systemctl restart llm-protocol-gateway
```

---

## 12. Cloudflare 公网访问（可选）
> **VPS 用户注意**：服务器有独立公网 IP 时直连 http://IP:18093 即可，通常不需要本节；仅在想隐藏源站 IP、没有域名/证书、或机器无公网 IP 时才使用。

网关内置 Cloudflare Tunnel 管理（UI「公网访问」页），但需要服务器上装有 `cloudflared`：

```bash
# Debian / Ubuntu
curl -fsSL https://pkg.cloudflare.com/cloudflare-main.gpg | sudo tee /usr/share/keyrings/cloudflare-main.gpg >/dev/null
echo "deb [signed-by=/usr/share/keyrings/cloudflare-main.gpg] https://pkg.cloudflare.com/cloudflared any main" | sudo tee /etc/apt/sources.list.d/cloudflared.list
sudo apt update && sudo apt install -y cloudflared
```

- 装在 `/usr/bin/cloudflared` 即可被服务发现；也可在 systemd 里显式指定
  `Environment=GATEWAY_CLOUDFLARED=/usr/bin/cloudflared`。
- 随机 `trycloudflare.com` 域名零配置；自有域名用 Tunnel Token。
- 已经用第 11 节反代对外暴露的，可跳过本节。

---

## 13. 升级与回滚

```bash
cd /opt/llm-protocol-gateway
sudo systemctl stop llm-protocol-gateway

cp bin/gateway bin/gateway.bak-$(date +%F)     # 回滚备份
git pull                                        # 或 git fetch && git checkout <新标签>

cd web && npm ci && npm run build && cd ..
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/gateway ./cmd/gateway

sudo systemctl start llm-protocol-gateway
curl -s http://127.0.0.1:18093/__health
```

- 数据库 schema 在启动时自动迁移，升级不丢数据；升级前仍建议按第 14 节做一次备份。
- 回滚：停服务，`cp bin/gateway.bak-<日期> bin/gateway`，启动即可（旧二进制兼容新库结构）。

---

## 14. 备份与恢复

需要备份的内容（都在两个目录里）：

| 内容 | 路径 |
| --- | --- |
| SQLite：Provider / Key / 日志 / 用量 / 管理密码哈希 | `/var/lib/llm-protocol-gateway/gateway.db`（连同 `-wal`/`-shm`） |
| Cloudflare 隧道凭据（若启用） | `/var/lib/llm-protocol-gateway/.cloudflared/` |
| Cursor 令牌（若用 Cursor 账号 Provider） | `/var/lib/llm-protocol-gateway/.config/llm-protocol-gateway/cursor/access-token` |

最简可靠做法（停服务拷贝，几秒钟）：

```bash
sudo systemctl stop llm-protocol-gateway
sudo tar czf /root/gateway-backup-$(date +%F).tar.gz -C /var/lib llm-protocol-gateway
sudo systemctl start llm-protocol-gateway
```

恢复：停服务 → 解包覆盖 `/var/lib/llm-protocol-gateway` → 启动。

---

## 15. 排障手册

先看日志：`sudo journalctl -u llm-protocol-gateway -n 200 --no-pager`

| 症状 | 原因与处理 |
| --- | --- |
| 启动失败，日志 `cannot bind gateway listen address` | 端口被占用（常见于另有一个手动实例）。`ss -ltnp \| grep 18093` 找到并停掉旧进程 |
| 打开 `IP:18093` 404 或空白页 | `web/dist` 未构建或路径不对。确认 `GATEWAY_WEB_DIST` 目录里有 `index.html`，没有则回到第 4.1 节构建 |
| 本机能访问，远程访问不了 | 防火墙/安全组没放行；确认监听在 `0.0.0.0`：`ss -ltnp \| grep 18093` 应显示 `0.0.0.0:18093`（只显示 `127.0.0.1:18093` 说明配了 `GATEWAY_ADDR=127.0.0.1:...`） |
| 一直跳 `/login`，无法设置密码 | 说明密码已设置过（`/__auth/setup` 只在未配置时可用，重复调用返回 409）。直接登录即可；忘记密码用下面的恢复命令 |
| 忘记管理密码 | 在服务器本机（SSH）执行，loopback 请求免当前密码校验：<br>`curl -s -X POST http://127.0.0.1:18093/__auth/password -H 'Content-Type: application/json' -d '{"newPassword":"新密码至少8位"}'` |
| 调用上游全部 TLS 失败 | 缺 `ca-certificates`（第 1 节）；或出站 443 被安全组/公司网关拦；检查是否意外设置了 `HTTP_PROXY`/`HTTPS_PROXY` |
| UI 上切换「局域网暴露」无效果 | 因为 systemd 里固定了 `GATEWAY_ADDR`，属于预期行为，改绑定地址请编辑 unit 后重启 |
| OAuth 类 Provider 登录不了 | headless 服务器无法完成浏览器回调，改用 API Key / Qoder 个人访问令牌，或桌面端导出后导入（见 9.4） |
| 自检（Self-check）不可用 | 该功能依赖服务器装有 `opencode` / `codex` / `claude` CLI，属可选功能，未装不影响转发 |

---

## 附录 A：环境变量总表

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `GATEWAY_ADDR` | `0.0.0.0:18093`（headless 首跑） | 监听地址 `host:port`；设置后 UI 暴露开关不生效 |
| `GATEWAY_DB` | `~/.config/llm-protocol-gateway/gateway.db` | SQLite 数据库路径 |
| `GATEWAY_WEB_DIST` | 自动探测 | 管理页静态资源目录（须含 `index.html`） |
| `GATEWAY_CONFIG` | `~/.config/llm-protocol-gateway/config.json` | 旧版 JSON 配置路径（自动迁移进 SQLite）；以 `.db` 结尾时会被当作数据库路径 |
| `GATEWAY_REPO_ROOT` | 自动探测 | 源码根目录（Cursor bridge / web dist 的兜底查找） |
| `GATEWAY_LAN_HOST` | 自动探测 | 覆盖 UI 上展示的局域网 IP |
| `GATEWAY_CLOUDFLARED` | PATH 查找 | cloudflared 可执行文件路径 |
| `GATEWAY_BUN` | PATH 查找 | Cursor bridge 依赖的 bun 路径 |
| `GATEWAY_CURSOR_BRIDGE_DIR` | 仓库 `scripts/cursor-bridge` | Cursor bridge 目录 |

---

## 附录 B：本地交叉编译（服务器免装 Go/Node）

在任意开发机（Windows / macOS / Linux）上构建，只上传两个产物。Go 侧是纯静态编译，
不需要目标机有任何运行时依赖。

开发机上（需 Go 1.26+ 与 Node 20+/22）：

```bash
git clone https://github.com/yangyongyongyong/llm-protocol-gateway.git
cd llm-protocol-gateway
cd web && npm ci && npm run build && cd ..
# amd64 服务器：
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o bin/gateway-linux ./cmd/gateway
# arm64 服务器改用 GOARCH=arm64
```

上传：

```bash
tar czf gateway-dist.tar.gz bin/gateway-linux web/dist
scp gateway-dist.tar.gz user@server:/tmp/
```

服务器上：

```bash
sudo mkdir -p /opt/llm-protocol-gateway/bin /opt/llm-protocol-gateway/web/dist
cd /tmp && tar xzf gateway-dist.tar.gz
sudo mv bin/gateway-linux /opt/llm-protocol-gateway/bin/gateway
sudo mv web/dist/* /opt/llm-protocol-gateway/web/dist/
sudo chmod +x /opt/llm-protocol-gateway/bin/gateway
```

之后从第 5 节（服务账号与数据目录）继续即可。

---

## 附录 C：完整卸载

```bash
sudo systemctl disable --now llm-protocol-gateway
sudo rm /etc/systemd/system/llm-protocol-gateway.service
sudo systemctl daemon-reload
sudo rm -rf /opt/llm-protocol-gateway /var/lib/llm-protocol-gateway   # 删除数据，谨慎
sudo userdel llmgw
```
