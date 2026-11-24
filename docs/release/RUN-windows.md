# Windows 启动说明

解压后得到：

```
start.cmd        启动器（双击或命令行运行）
gateway.exe      无界面网关
gatewayctl.exe   AI / 脚本管理 CLI（输出 JSON）
web-dist\        管理页
RUN.txt          本文件
VERSION          版本与源码修订号
```

解压后运行 `start.cmd`（双击也可以）：

```powershell
.\start.cmd
```

另一个 PowerShell 窗口可以直接操作同一实例：

```powershell
.\gatewayctl.exe status
.\gatewayctl.exe upstream list
```

它只是把管理页目录指到同级的 `web-dist\` 再启动 `gateway.exe`。直接跑 `gateway.exe` 也能启动，
但管理页会 404 —— 那时需要自己设 `GATEWAY_WEB_DIST`。

默认监听 `0.0.0.0:18093`，浏览器打开 `http://127.0.0.1:18093` 即是管理页；健康检查 `GET /__health`。
首次运行若弹出防火墙询问，允许「专用网络」即可让局域网访问。

## 装成开机自启服务

以管理员身份打开 PowerShell，把 `<解压目录>` 换成实际路径：

```powershell
New-Service -Name LLMProtocolGateway `
  -BinaryPathName '"<解压目录>\gateway.exe"' `
  -DisplayName 'LLM Protocol Gateway' -StartupType Automatic
Start-Service LLMProtocolGateway
```

服务以 `LocalSystem` 身份运行时，数据目录在 `C:\Windows\System32\config\systemprofile\AppData\Roaming\llm-protocol-gateway\`。
更省事的做法是在服务的「登录」页里改成你的用户账号，数据就落在 `%AppData%\llm-protocol-gateway\`。

也可以用 `sc.exe`：

```powershell
sc.exe create LLMProtocolGateway binPath= "\"<解压目录>\gateway.exe\"" start= auto
sc.exe start LLMProtocolGateway
```

## 可选依赖

| 能力 | 需要 |
| --- | --- |
| Cloudflare 公网访问 | 把 `cloudflared.exe` 放进 PATH，或设置 `GATEWAY_CLOUDFLARED` |
| Cursor Provider | 安装 `node`（≥ 18），或把 `bun.exe` 放进 PATH |
| RTK 工具输出压缩 | `rtk` 在 PATH 中 |

缺省时对应功能在控制台里显示不可用，不影响其它协议与账号。

## 数据与升级

- 数据默认写在 `%AppData%\llm-protocol-gateway\`，可用 `GATEWAY_DB` 改到别处。
- 升级：`Stop-Service LLMProtocolGateway` → 用新包内的 `gateway.exe` 与 `web-dist\` 覆盖 → `Start-Service`。数据库自动迁移。
- 该目录包含上游密钥与 OAuth 令牌，请限制访问权限并纳入备份。

## 架构

包名结尾是 `windows_amd64`（x86_64，绝大多数机器）或 `windows_arm64`（骁龙本、部分虚拟机）。

## 桌面 App

想要窗口版而不是无界面服务，下载安装包里的 `llm-protocol-gateway-desktop-windows-amd64.exe`。
它是同一个核心 + Wails 窗口，首次运行同样落在 18093 端口，二者不要同时开。
未签名，首次运行时 SmartScreen 会拦一次：点「更多信息」→「仍要运行」。
