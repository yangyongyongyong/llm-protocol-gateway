#!/usr/bin/env bash
# 裸机部署的自动更新：跟随 GitHub 的正式 Release 升级本机网关。
#
# 由 systemd timer 定时触发（每 15 分钟一次）。只认 releases/latest
# （draft 与 prerelease 天然被排除），版本号比 /opt/llm-protocol-gateway/VERSION
# 新才动手，所以重复执行是安全的，也不会把本机降级。
#
#   scripts/auto-update.sh           检查并升级（timer 调用的就是它）
#   scripts/auto-update.sh --check   只报告当前版本与最新版本，不动任何东西
#
# 升级流程（每一步失败都会留下可读的 journal 日志）：
#   下载 linux/<arch> 包 → 校验 SHA256SUMS → 停服 → 备份数据库 →
#   换 bin/gateway 与 web/dist → 起服 → 健康检查（/__app/version 报出新版本）
# 健康检查失败自动回滚到上一份二进制与 web/dist 并重新起服。
#
# 可用环境变量覆盖（一般不需要）：
#   GATEWAY_UPDATE_REPO=owner/repo
#   GATEWAY_UPDATE_ROOT=/opt/llm-protocol-gateway
#   GATEWAY_UPDATE_SERVICE=llm-protocol-gateway
#   GATEWAY_UPDATE_ADDR=127.0.0.1:18093
#   GATEWAY_UPDATE_DB=/var/lib/llm-protocol-gateway/gateway.db
#   GATEWAY_UPDATE_BACKUPS=/var/lib/llm-protocol-gateway/backups
#   GATEWAY_UPDATE_KEEP_BACKUPS=2
set -euo pipefail

REPO="${GATEWAY_UPDATE_REPO:-yangyongyongyong/llm-protocol-gateway}"
ROOT="${GATEWAY_UPDATE_ROOT:-/opt/llm-protocol-gateway}"
SERVICE="${GATEWAY_UPDATE_SERVICE:-llm-protocol-gateway}"
ADDR="${GATEWAY_UPDATE_ADDR:-127.0.0.1:18093}"
DB="${GATEWAY_UPDATE_DB:-/var/lib/llm-protocol-gateway/gateway.db}"
BACKUPS="${GATEWAY_UPDATE_BACKUPS:-/var/lib/llm-protocol-gateway/backups}"
KEEP_BACKUPS="${GATEWAY_UPDATE_KEEP_BACKUPS:-2}"
STATE="$ROOT/VERSION"
BIN="$ROOT/bin/gateway"
WEB="$ROOT/web/dist"
API="https://api.github.com/repos/$REPO/releases/latest"
CHECK_ONLY=0
if [[ "${1:-}" == "--check" ]]; then
  CHECK_ONLY=1
fi

log() { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }

# 只允许一份实例在跑：手动执行撞上 timer 时，后来的直接退出。
exec 9>"${TMPDIR:-/tmp}/llm-gateway-auto-update.lock"
if ! flock -n 9; then
  log "另一个更新实例正在运行，跳过"
  exit 0
fi

current_version() {
  if [[ -f "$STATE" ]]; then
    # VERSION 文件是 release 包里那份的格式：TARGET=/VERSION=/...
    sed -n 's/^VERSION=//p' "$STATE" | head -n1 | tr -d '[:space:]' | sed 's/^v//'
    return
  fi
  # 没有状态文件（首次接管手工部署）：读运行中二进制的版本；开发构建是空串。
  curl -fsS --max-time 5 "http://$ADDR/__app/version" 2>/dev/null \
    | sed -n 's/.*"version":"\([^"]*\)".*/\1/p' | sed 's/^v//' || true
}

# version_gt A B：A 严格大于 B 时返回 0。空版本视为最小。
version_gt() {
  local a="$1" b="$2"
  if [[ -z "$a" ]]; then return 1; fi
  if [[ -z "$b" ]]; then return 0; fi
  if [[ "$a" == "$b" ]]; then return 1; fi
  [[ "$(printf '%s\n%s\n' "$a" "$b" | sort -V | tail -n1)" == "$a" ]]
}

asset_arch() {
  case "$(uname -m)" in
    x86_64 | amd64) echo amd64 ;;
    aarch64 | arm64) echo arm64 ;;
    armv7l | armv7) echo armv7 ;;
    *) fail "不支持的架构：$(uname -m)" ;;
  esac
}

release_json="$(curl -fsSL --retry 3 --retry-delay 5 --max-time 30 \
  -H 'Accept: application/vnd.github+json' \
  -H 'User-Agent: llm-gateway-auto-update' \
  "$API")" || { log "查询 GitHub Release 失败（网络或限流），本次跳过"; exit 0; }

tag="$(printf '%s\n' "$release_json" | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
draft="$(printf '%s\n' "$release_json" | sed -n 's/.*"draft":[[:space:]]*\(true\|false\).*/\1/p' | head -n1)"
prerelease="$(printf '%s\n' "$release_json" | sed -n 's/.*"prerelease":[[:space:]]*\(true\|false\).*/\1/p' | head -n1)"
if [[ -z "$tag" ]]; then
  log "Release 响应里没有 tag_name，本次跳过"
  exit 0
fi
if [[ "$draft" == "true" || "$prerelease" == "true" ]]; then
  log "最新 Release $tag 是 draft/prerelease，跳过"
  exit 0
fi

latest="${tag#v}"
current="$(current_version)"
log "当前版本：${current:-未知}；最新 Release：$latest"

if ! version_gt "$latest" "$current"; then
  log "已是最新（或本机更新），无需升级"
  exit 0
fi

arch="$(asset_arch)"
asset="llm-protocol-gateway_${latest}_linux_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$tag"

if [[ "$CHECK_ONLY" == "1" ]]; then
  log "可升级：$asset"
  exit 0
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

log "下载 $asset"
curl -fsSL --retry 3 --retry-delay 5 --max-time 300 -o "$work/$asset" "$base/$asset" \
  || fail "下载 $asset 失败"
curl -fsSL --retry 3 --retry-delay 5 --max-time 60 -o "$work/SHA256SUMS" "$base/SHA256SUMS" \
  || fail "下载 SHA256SUMS 失败"

expected="$(awk -v name="$asset" '$2 == name {print $1}' "$work/SHA256SUMS" | head -n1)"
if [[ -z "$expected" ]]; then
  fail "SHA256SUMS 里没有 $asset"
fi
actual="$(sha256sum "$work/$asset" | awk '{print $1}')"
if [[ "$expected" != "$actual" ]]; then
  fail "$asset 校验失败：期望 $expected，实际 $actual"
fi
log "校验通过"

mkdir -p "$work/unpack"
tar -xzf "$work/$asset" -C "$work/unpack"
if [[ ! -x "$work/unpack/gateway" ]]; then
  fail "包里没有可执行的 gateway"
fi
if [[ ! -f "$work/unpack/web-dist/index.html" ]]; then
  fail "包里没有 web-dist/index.html"
fi

# ---- 备份与替换 ----------------------------------------------------------
SERVICE_USER="$(systemctl show -p User --value "$SERVICE" 2>/dev/null || true)"
SERVICE_USER="${SERVICE_USER:-llmgw}"

ts="$(date +%Y%m%d-%H%M%S)"
mkdir -p "$BACKUPS"

log "停止 $SERVICE"
systemctl stop "$SERVICE"

# 停服后 WAL 已 checkpoint，数据库是一致的；连 sidecar 一起留一份以防万一。
cp -a "$DB" "$BACKUPS/gateway.db.auto-$ts"
for suffix in -wal -shm; do
  if [[ -f "$DB$suffix" ]]; then
    cp -a "$DB$suffix" "$BACKUPS/gateway.db.auto-$ts$suffix"
  fi
done
chown --reference="$DB" "$BACKUPS/gateway.db.auto-$ts"* 2>/dev/null || true
# 只保留最近 N 份自动备份（手工备份不碰）。没有匹配时 ls 返回非零，pipefail
# 下不能让脚本就此退出。
ls -1t "$BACKUPS"/gateway.db.auto-* 2>/dev/null | tail -n +"$((KEEP_BACKUPS + 1))" | xargs -r rm -f || true

cp -a "$BIN" "$BIN.prev"
if [[ -d "$WEB" ]]; then
  rm -rf "$WEB.prev"
  cp -a "$WEB" "$WEB.prev"
fi

install -m 755 "$work/unpack/gateway" "$BIN"
rm -rf "$WEB"
cp -a "$work/unpack/web-dist" "$WEB"
chown -R "$SERVICE_USER:$SERVICE_USER" "$WEB" 2>/dev/null || true

printf 'TARGET=linux/%s\nVERSION=%s\n' "$arch" "$latest" >"$STATE"

rollback() {
  log "回滚到 ${current:-上一版本}"
  systemctl stop "$SERVICE" || true
  install -m 755 "$BIN.prev" "$BIN"
  if [[ -d "$WEB.prev" ]]; then
    rm -rf "$WEB"
    cp -a "$WEB.prev" "$WEB"
    chown -R "$SERVICE_USER:$SERVICE_USER" "$WEB" 2>/dev/null || true
  fi
  printf 'TARGET=linux/%s\nVERSION=%s\n' "$arch" "$current" >"$STATE"
  systemctl start "$SERVICE" || true
}

log "启动 $SERVICE"
if ! systemctl start "$SERVICE"; then
  rollback
  fail "升级到 $latest 失败（服务起不来），已回滚"
fi

# ---- 健康检查 ------------------------------------------------------------
healthy=0
for _ in $(seq 1 30); do
  reported="$(curl -fsS --max-time 3 "http://$ADDR/__app/version" 2>/dev/null \
    | sed -n 's/.*"version":"\([^"]*\)".*/\1/p' || true)"
  if [[ "${reported#v}" == "$latest" ]]; then
    healthy=1
    break
  fi
  sleep 1
done

if [[ "$healthy" != "1" ]]; then
  log "健康检查失败：/__app/version 没有报出 $latest"
  rollback
  fail "升级到 $latest 失败，已回滚到 ${current:-上一版本}"
fi

log "升级完成：${current:-未知} -> $latest"
