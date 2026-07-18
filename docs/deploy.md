# 容器部署（Docker / Docker Compose / Kubernetes）

网关核心是一个**纯 Go、无 CGO 的静态二进制**（SQLite 用 `modernc.org/sqlite` 纯 Go 实现），
`cmd/gateway` 为无界面（headless）入口，处理 `SIGTERM` 优雅退出——天然适合容器化。
运行镜像约 **57MB**，支持 `linux/amd64` 与 `linux/arm64`。

> 桌面版的「Cloudflare 公网访问」依赖本机 `cloudflared`，容器里**不需要**它：用 K8s Ingress /
> 反向代理 / 云负载均衡对外暴露即可。

## 镜像地址

- **Docker Hub**：[`yangyongyong/llm-protocol-gateway`](https://hub.docker.com/r/yangyongyong/llm-protocol-gateway)
- 支持标签：`latest`、`X.Y.Z`（对应 GitHub tag `vX.Y.Z`）、`X.Y`
- 架构：`linux/amd64`、`linux/arm64`

```bash
docker pull yangyongyong/llm-protocol-gateway:latest
```

## 关键环境变量

| 变量 | 默认（镜像内） | 说明 |
| --- | --- | --- |
| `GATEWAY_ADDR` | `0.0.0.0:18093` | 监听地址 |
| `GATEWAY_DB` | `/data/gateway.db` | SQLite 路径（挂卷持久化） |
| `GATEWAY_WEB_DIST` | `/app/web-dist` | 管理页静态资源目录（镜像已内置） |

## 一、Docker 一键运行

```bash
docker run -d --name llm-protocol-gateway \
  -p 18093:18093 \
  -v llm-gateway-data:/data \
  --restart unless-stopped \
  yangyongyong/llm-protocol-gateway:latest

curl -s http://127.0.0.1:18093/__health   # {"status":"ok",...}
```

打开 `http://<服务器IP>:18093/`：非本机访问会跳转 `/login`，**首次需设置管理员密码**
（本机 `127.0.0.1` 直连才自动免密）。之后在控制台添加 Provider / API Key。

## 二、Docker Compose

仓库根目录已提供 `docker-compose.yml`（默认拉取 Docker Hub 镜像；如需本地构建取消 `build: .` 注释）：

```bash
docker compose up -d
docker compose logs -f
```

## 三、Kubernetes

仓库提供 `deploy/k8s.yaml`（Namespace + PVC + Deployment + Service，含 `/__health` 探针）：

```bash
kubectl apply -f deploy/k8s.yaml
kubectl -n llm-gateway get pods
# 对外暴露：自行加 Ingress，或临时端口转发：
kubectl -n llm-gateway port-forward svc/llm-protocol-gateway 18093:80
```

> **单副本**：SQLite 是本地文件，请勿多副本共享同一 PVC（`strategy: Recreate` 已设）。

## 四、发布镜像（GitHub Actions → Docker Hub）

`.github/workflows/docker-publish.yml`：**打 tag（如 `v0.2.1`）即自动构建 `linux/amd64,linux/arm64`
多架构镜像并推送到 Docker Hub**。镜像与 GitHub 版本一一对应（tag `v0.2.1` → 镜像 `0.2.1`/`0.2`/`latest`）。

需在 GitHub 仓库 **Settings → Secrets and variables → Actions** 添加两个 secret：

| Secret | 值 |
| --- | --- |
| `DOCKERHUB_USERNAME` | `yangyongyong` |
| `DOCKERHUB_TOKEN`    | Docker Hub Access Token（Account Settings → Security → New Access Token，权限 Read/Write） |

```bash
git tag v0.2.1 && git push origin v0.2.1     # 触发自动构建发布
```

本地手动多架构发布（需已 `docker login`）：

```bash
docker buildx build --platform linux/amd64,linux/arm64 \
  -t yangyongyong/llm-protocol-gateway:0.2.1 \
  -t yangyongyong/llm-protocol-gateway:latest \
  --push .
```

## 注意事项

- **持久化**：务必挂载 `/data`，否则容器重建会丢失 Provider / Key / 日志配置。
- **管理员密码**：经 Service/Ingress（非 loopback）访问时不免密，首访设密码即可。
- **OAuth 类 Provider**（Claude / ChatGPT / Cursor）登录需交互式浏览器回调，headless
  容器不便完成；容器场景建议用 **API Key（Bearer）Provider**，或在桌面端登录后用
  「导出 / 导入 Provider」把已授权配置带入容器。
- **出站 HTTPS**：镜像已内置 `ca-certificates`，可正常访问上游 API。
