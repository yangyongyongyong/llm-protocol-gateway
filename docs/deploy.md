# 容器部署（Docker / Docker Compose / Kubernetes）

网关核心是一个**纯 Go、无 CGO 的静态二进制**（SQLite 用 `modernc.org/sqlite` 纯 Go 实现），
`cmd/gateway` 为无界面（headless）入口，处理 `SIGTERM` 优雅退出——天然适合容器化。
运行镜像约 **57MB**，支持 `linux/amd64` 与 `linux/arm64`。

> 桌面版的「Cloudflare 公网访问」依赖本机 `cloudflared`，容器里**不需要**它：用 K8s Ingress /
> 反向代理 / 云负载均衡对外暴露即可。

## 关键环境变量

| 变量 | 默认（镜像内） | 说明 |
| --- | --- | --- |
| `GATEWAY_ADDR` | `0.0.0.0:18093` | 监听地址 |
| `GATEWAY_DB` | `/data/gateway.db` | SQLite 路径（挂卷持久化） |
| `GATEWAY_WEB_DIST` | `/app/web-dist` | 管理页静态资源目录（镜像已内置） |

## 一、Docker 一键运行

```bash
# 用已发布镜像（见下方 CI 发布），或先本地 docker build -t llm-protocol-gateway .
docker run -d --name llm-protocol-gateway \
  -p 18093:18093 \
  -v llm-gateway-data:/data \
  --restart unless-stopped \
  ghcr.io/yangyongyongyong/llm-protocol-gateway:latest

curl -s http://127.0.0.1:18093/__health   # {"status":"ok",...}
```

打开 `http://<服务器IP>:18093/`：非本机访问会跳转 `/login`，**首次需设置管理员密码**
（本机 `127.0.0.1` 访问才自动免密）。之后在控制台添加 Provider / API Key。

## 二、Docker Compose

仓库根目录已提供 `docker-compose.yml`：

```bash
docker compose up -d          # 本地构建并启动
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
> 需要高可用时，另议外置存储方案。

## 四、发布镜像给别人用（GitHub Actions → GHCR）

已提供 `.github/workflows/docker-publish.yml`：**打 tag（如 `v0.2.1`）即自动构建
`linux/amd64,linux/arm64` 多架构镜像并推送到 GHCR**，用内置 `GITHUB_TOKEN`，无需额外密钥。

```bash
git tag v0.2.1 && git push origin v0.2.1
# 产物：ghcr.io/yangyongyongyong/llm-protocol-gateway:{0.2.1, 0.2, latest}
```

首次发布后到 GitHub 仓库 → Packages，把该 package 设为 Public，别人即可直接
`docker pull` 使用。若要发到 Docker Hub，把 workflow 的登录与镜像名换成 Docker Hub 账号
并加 `DOCKERHUB_TOKEN` secret 即可。

## 注意事项

- **持久化**：务必挂载 `/data`，否则容器重建会丢失 Provider / Key / 日志配置。
- **管理员密码**：经 Service/Ingress（非 loopback）访问时不免密，首访设密码即可。
- **OAuth 类 Provider**（Claude / ChatGPT / Cursor）登录需交互式浏览器回调，headless
  容器不便完成；容器场景建议用 **API Key（Bearer）Provider**，或在桌面端登录后用
  「导出 / 导入 Provider」把已授权配置带入容器。
- **出站 HTTPS**：镜像已内置 `ca-certificates`，可正常访问上游 API。
