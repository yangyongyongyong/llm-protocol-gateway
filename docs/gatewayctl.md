# gatewayctl：给 AI 与脚本用的网关 CLI

`gatewayctl` 是网关管理 API 的薄封装。默认输出 JSON，适合 AI 工具调用、Shell
脚本和 CI；所有动作与浏览器控制台走同一套鉴权、RBAC、审计和持久化路径。

## 获取

- 压缩包：与 `gateway` 同级，文件名为 `gatewayctl`（Windows 为 `gatewayctl.exe`）
- macOS App：`/Applications/LLM Protocol Gateway.app/Contents/Resources/bin/gatewayctl`
- Docker：`docker exec llm-protocol-gateway /app/gatewayctl status`
- 源码：`go run ./cmd/gatewayctl`

```bash
gatewayctl --help
gatewayctl status
```

本机 `127.0.0.1` 请求沿用控制台的 loopback 管理员免密规则。远程网关先保存登录会话：

```bash
printf '%s\n' "$ADMIN_PASSWORD" | \
  gatewayctl --url https://gateway.example.com login --username admin --password-stdin
```

会话 cookie 保存在用户配置目录的 `llm-protocol-gateway/gatewayctl.json`，权限
`0600`；CLI 不保存管理员密码。也可用 `GATEWAYCTL_URL`、`GATEWAYCTL_USERNAME`、
`GATEWAYCTL_PASSWORD` 做非交互调用。

## 上游继承

注册上游后，上游授权的 Provider、模型和卡片快照会自动继承到本机：

```bash
printf '%s\n' "$UPSTREAM_PASSWORD" | gatewayctl upstream add \
  --url https://parent.example.com \
  --username peer-mac-mini \
  --name vps1 \
  --password-stdin \
  --provider provider-a,provider-b

gatewayctl upstream list
gatewayctl upstream refresh --id <providerId>
```

注册用户名和密码保存在本机 SQLite。重新注册/重连可省略账号密码，服务端自动填入：

```bash
gatewayctl upstream reconnect --id <providerId>
```

显式查看和复制由本机记住的凭据：

```bash
gatewayctl upstream credentials --id <providerId>
```

该命令和对应 HTTP 接口都要求 `peers.manage`。普通列表只返回
`credentialsStored: true/false`，不会批量下发密码；`/__state` 和 Provider 导出也
不会混入这组登录密码。

## 下游审批与授权

```bash
gatewayctl downstream list
gatewayctl downstream policy --require-approval
gatewayctl downstream approve --id <userId> --all
gatewayctl downstream authorize --id <userId> --provider provider-a,provider-b
gatewayctl downstream revoke --id <userId>
```

`approve` 会启用下游账号并恢复访问；`authorize` 只改 Provider 授权。
`policy --require-approval` 让新注册/重新注册的下游先进入「待审批」；
`policy --auto-approve` 恢复注册后立即可用。
`revoke` 会立即删掉下游流量凭据，但保留账号、归属和授权记录。被吊销的下游用本机
保存的用户名密码重新注册即可恢复。

## 内网穿透 URL

```bash
gatewayctl tunnel status
gatewayctl tunnel url
gatewayctl tunnel url --raw
gatewayctl tunnel url --ui --raw
gatewayctl tunnel start
gatewayctl tunnel stop
```

`url` 会优先返回运行中的隧道地址，其次返回已保存的公网基址。`--raw` 只输出 URL，
便于传给另一个命令。

## Provider 与通用 API

```bash
gatewayctl provider list
printf '%s\n' "$UPSTREAM_API_KEY" | gatewayctl provider add \
  --name openai-main --protocol openai_chat \
  --base-url https://api.openai.com/v1 --api-key-stdin

gatewayctl api GET /__state
gatewayctl api PATCH /__settings/peer-max-hops --data '{"hops":4}'
```

`api` 是给 AI 的逃生口：已有或未来新增的控制台 JSON API 都能直接调用，不需要每次
扩展 CLI 子命令。注意不要把密码、API Key 放进可被 shell 历史或进程列表长期保留的
参数里，优先使用 stdin 或受保护的临时文件。

## 相关 HTTP 接口

| 动作 | 接口 |
| --- | --- |
| 本机已连接的上游 | `GET /__peer-upstreams` |
| 注册/继承上游 | `POST /__peer-upstreams` |
| 自动填入凭据重连 | `POST /__peer-upstreams/{id}/reconnect` |
| 读取保存的登录凭据 | `GET /__peer-upstreams/{id}/credentials` |
| 下游列表 | `GET /__peers` |
| 注册审批策略 | `PATCH /__settings/peer-approval` |
| 审批下游 | `POST /__peers/{id}/approve` |
| 修改下游授权 | `PATCH /__peers/{id}/grants` |
| 吊销下游 | `DELETE /__peers/{id}` |
| 公网隧道状态/URL | `GET /__public` |
