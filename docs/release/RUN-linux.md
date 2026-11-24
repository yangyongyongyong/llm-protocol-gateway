# Linux 启动说明

解压后得到：

```
start.sh         启动器（把管理页指到同级 web-dist 后启动）
gateway          无界面网关
gatewayctl       AI / 脚本管理 CLI（输出 JSON）
web-dist/        管理页
RUN.txt          本文件
VERSION          版本与源码修订号
```

自检后直接运行（不需要安装 Go / Node / 数据库）：

```bash
chmod +x start.sh gateway
./start.sh
```

另一个终端可以直接操作同一实例：

```bash
./gatewayctl status
./gatewayctl upstream list
```

`start.sh` 只做一件事：`GATEWAY_WEB_DIST=$(dirname $0)/web-dist` 后 exec `gateway`。
直接跑 `./gateway` 也能启动，但管理页会 404，除非自己设置 `GATEWAY_WEB_DIST`。

默认监听 `0.0.0.0:18093`，浏览器打开 `http://<主机>:18093` 即是管理页；健康检查 `GET /__health`。

安装成 systemd 服务（开机自启、崩溃重启）：

```bash
sudo install -m 0755 gateway /usr/local/bin/llm-protocol-gateway
sudo install -d /opt/llm-protocol-gateway && sudo cp -R web-dist /opt/llm-protocol-gateway/
sudo useradd --system --home /var/lib/llm-protocol-gateway --shell /usr/sbin/nologin gateway || true
sudo install -d -o gateway -g gateway /var/lib/llm-protocol-gateway
```

`/etc/systemd/system/llm-protocol-gateway.service`：

```ini
[Unit]
Description=LLM Protocol Gateway
After=network-online.target

[Service]
User=gateway
Environment=GATEWAY_ADDR=0.0.0.0:18093
Environment=GATEWAY_DB=/var/lib/llm-protocol-gateway/gateway.db
Environment=GATEWAY_WEB_DIST=/opt/llm-protocol-gateway/web-dist
# 控制台按上海时区划分日界，日桶与页面日期才对得上
Environment=TZ=Asia/Shanghai
ExecStart=/usr/local/bin/llm-protocol-gateway
Restart=always
TimeoutStopSec=60

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload && sudo systemctl enable --now llm-protocol-gateway
```

## 可选依赖

| 能力 | 需要 |
| --- | --- |
| Cloudflare 公网访问 | `cloudflared` 在 PATH 中，或设置 `GATEWAY_CLOUDFLARED` |
| Cursor Provider | `node`（≥ 18）或 `bun`，用于运行 Cursor 桥接 |
| RTK 工具输出压缩 | `rtk` 在 PATH 中 |

这些都可缺省，缺省时对应功能在控制台里显示不可用，不影响其它协议与账号。

## 数据与升级

- 数据默认写在 `~/.config/llm-protocol-gateway/`（root 运行时为 `/root/.config/...`），可用 `GATEWAY_DB` 改到别处。
- 升级：停服务 → 用新包内的 `gateway` 与 `web-dist/` 覆盖旧文件 → 起服务。数据库自动迁移，无需手工脚本。
- 该目录包含上游密钥与 OAuth 令牌，请限制文件权限并纳入备份范围。

## 架构

包名结尾是 `linux_amd64`、`linux_arm64` 或 `linux_armv7`，分别对应 x86_64、ARM64（树莓派 4/5、Apple 芯片虚拟机、ARM 服务器）与 32 位 ARM（树莓派 Zero/2 等）。用 `uname -m` 确认：`x86_64` 选 amd64，`aarch64` 选 arm64，`armv7l` 选 armv7。

需要 Docker 的话直接用镜像，省去上面的安装步骤：

```bash
docker run -d --name llm-protocol-gateway -p 18093:18093 \
  -v llm-gateway-data:/data --restart unless-stopped \
  yangyongyong/llm-protocol-gateway:latest
```
