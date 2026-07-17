#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ADDR="${GATEWAY_ADDR:-0.0.0.0:18093}"
PORT="${ADDR##*:}"
# Health checks always hit loopback even when the process binds 0.0.0.0.
HEALTH_ADDR="127.0.0.1:${PORT}"
CACHE_DIR="$ROOT/.cache"
BIN="$CACHE_DIR/gateway-dev"
PID_FILE="$CACHE_DIR/gateway.pid"
LOG_FILE="$CACHE_DIR/gateway.log"
LOCK_DIR="$CACHE_DIR/gateway.lock.d"

mkdir -p "$CACHE_DIR"

# --restart / -r：即使旧进程健康也强制重启（先构建后切换，最小化停机窗口）。
RESTART=0
if [[ "${1:-}" == "--restart" || "${1:-}" == "-r" ]]; then
  RESTART=1
fi

if ! mkdir "$LOCK_DIR" 2>/dev/null; then
  echo "waiting for gateway lock..."
  for _ in $(seq 1 30); do
    if mkdir "$LOCK_DIR" 2>/dev/null; then
      break
    fi
    sleep 0.2
  done
fi
cleanup_lock() {
  rmdir "$LOCK_DIR" 2>/dev/null || true
}
trap cleanup_lock EXIT

health() {
  local response
  response="$(curl -sf "http://${HEALTH_ADDR}/__health" 2>/dev/null || true)"
  [[ "$response" == *'"status":"ok"'* ]]
}

is_our_gateway_pid() {
  local pid="$1"
  [[ -n "$pid" ]] || return 1
  kill -0 "$pid" 2>/dev/null || return 1
  local cmd
  cmd="$(ps -p "$pid" -o command= 2>/dev/null || true)"
  [[ "$cmd" == *"gateway-dev"* || "$cmd" == *"llm-protocol-gateway"* ]]
}

# stop_stale [force]：force=1 时无论旧进程是否健康都停掉（--restart 用）。
# 端口释放用 50ms 轮询代替固定 sleep，缩短切换窗口。
stop_stale() {
  local force="${1:-0}"
  if [[ -f "$PID_FILE" ]]; then
    local old_pid
    old_pid="$(cat "$PID_FILE")"
    if is_our_gateway_pid "$old_pid"; then
      if [[ "$force" -eq 0 ]] && health; then
        return 0
      fi
      echo "stopping gateway pid=${old_pid}"
      kill "$old_pid" 2>/dev/null || true
    fi
    rm -f "$PID_FILE"
  fi
  if lsof -ti "tcp:${PORT}" >/dev/null 2>&1; then
    lsof -ti "tcp:${PORT}" | xargs kill 2>/dev/null || true
  fi
  for _ in $(seq 1 20); do
    lsof -ti "tcp:${PORT}" >/dev/null 2>&1 || return 0
    sleep 0.05
  done
}

if [[ "$RESTART" -eq 0 ]] && health; then
  if [[ -f "$PID_FILE" ]] && is_our_gateway_pid "$(cat "$PID_FILE")"; then
    echo "gateway already healthy at http://${HEALTH_ADDR} (bind ${ADDR})"
    exit 0
  fi
  echo "port ${PORT} is healthy but pid file is stale; restarting gateway"
fi

# Cloudflare / LAN UI is served by the gateway itself from web/dist.
# Vite (5173) only covers local HMR; without a built dist, public hostnames
# like https://user.example.com/login return 404.
ensure_web_dist() {
  local web_dist="$ROOT/web/dist"
  local need_build=0
  if [[ ! -f "$web_dist/index.html" ]]; then
    need_build=1
  elif find "$ROOT/web/src" -newer "$web_dist/index.html" -print -quit 2>/dev/null | grep -q .; then
    need_build=1
  elif [[ "$ROOT/web/index.html" -nt "$web_dist/index.html" ]] \
    || [[ "$ROOT/web/vite.config.ts" -nt "$web_dist/index.html" ]] \
    || [[ "$ROOT/web/package.json" -nt "$web_dist/index.html" ]]; then
    need_build=1
  fi
  if [[ "$need_build" -eq 0 ]]; then
    return 0
  fi
  if ! command -v npm >/dev/null 2>&1; then
    echo "web UI is missing/outdated at $web_dist but npm is not installed; Cloudflare UI hostnames will 404" >&2
    return 0
  fi
  if [[ ! -d "$ROOT/web/node_modules" ]]; then
    echo "installing web dependencies"
    (cd "$ROOT/web" && npm install)
  fi
  echo "building web UI -> $web_dist"
  (cd "$ROOT/web" && npm run build)
}
# ===== 所有构建都在停旧进程之前完成，构建耗时不再计入停机窗口 =====
ensure_web_dist

# 编译到独立文件再原子替换：不能直接覆盖运行中的 $BIN —— macOS 上就地改写
# 运行中的二进制会使其代码签名失效，旧进程可能被系统直接 SIGKILL。
BIN_NEW="$BIN.new"
echo "building gateway -> $BIN_NEW"
(cd "$ROOT" && go build -o "$BIN_NEW" ./cmd/gateway)

# ===== 现在才停旧进程：停机窗口 = 杀进程 + mv + 新进程启动 =====
stop_stale "$RESTART"
mv -f "$BIN_NEW" "$BIN"

echo "starting gateway at ${ADDR}"
# Start in a new session so quitting Cursor/terminal process groups cannot kill it.
GATEWAY_PID="$(
  python3 - "$BIN" "$ADDR" "$LOG_FILE" "$ROOT" <<'PY'
import os, sys
bin_path, addr, log_file, repo_root = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
log = open(log_file, "a", buffering=1)
pid = os.fork()
if pid == 0:
    os.setsid()
    os.environ["GATEWAY_ADDR"] = addr
    os.environ["GATEWAY_REPO_ROOT"] = repo_root
    os.dup2(log.fileno(), 1)
    os.dup2(log.fileno(), 2)
    os.execv(bin_path, [bin_path])
print(pid)
PY
)"
echo "$GATEWAY_PID" >"$PID_FILE"

for _ in $(seq 1 50); do
  if health; then
    echo "gateway ready (pid=${GATEWAY_PID})"
    curl -sf "http://${HEALTH_ADDR}/__state" >/dev/null
    echo "verified /__state"
    exit 0
  fi
  if ! kill -0 "$GATEWAY_PID" 2>/dev/null; then
    echo "gateway exited early; see $LOG_FILE" >&2
    tail -20 "$LOG_FILE" >&2 || true
    exit 1
  fi
  sleep 0.2
done

echo "gateway failed health check; see $LOG_FILE" >&2
exit 1
