# syntax=docker/dockerfile:1

########## 1) 构建前端 web/dist ##########
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

########## 2) 构建 Go 网关（纯 Go SQLite，CGO 关闭，静态二进制） ##########
# buildx 多架构构建时，本阶段会按目标平台运行，go build 直接产出对应架构静态二进制。
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/gateway ./cmd/gateway

########## 3) 运行时镜像 ##########
FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget \
    && adduser -D -u 10001 app
WORKDIR /app
COPY --from=build /out/gateway /app/gateway
COPY --from=web   /web/dist    /app/web-dist
ENV GATEWAY_ADDR=0.0.0.0:18093 \
    GATEWAY_WEB_DIST=/app/web-dist \
    GATEWAY_DB=/data/gateway.db
RUN mkdir -p /data && chown -R app:app /data /app
USER app
EXPOSE 18093
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:18093/__health | grep -q '"status":"ok"' || exit 1
ENTRYPOINT ["/app/gateway"]
