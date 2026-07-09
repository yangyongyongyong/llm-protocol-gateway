#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WEB_PORT="${WEB_DEV_PORT:-5173}"
WATCHDOG_PID_FILE="$ROOT/.cache/gateway-watchdog.pid"

cleanup() {
  if [[ -f "$WATCHDOG_PID_FILE" ]]; then
    local watchdog_pid
    watchdog_pid="$(cat "$WATCHDOG_PID_FILE")"
    kill "$watchdog_pid" 2>/dev/null || true
    rm -f "$WATCHDOG_PID_FILE"
  fi
}
trap cleanup EXIT INT TERM

bash "$ROOT/scripts/ensure-gateway.sh"

if [[ -f "$WATCHDOG_PID_FILE" ]]; then
  old_watchdog="$(cat "$WATCHDOG_PID_FILE")"
  if kill -0 "$old_watchdog" 2>/dev/null; then
    kill "$old_watchdog" 2>/dev/null || true
    sleep 0.2
  fi
  rm -f "$WATCHDOG_PID_FILE"
fi

nohup bash "$ROOT/scripts/gateway-watchdog.sh" >>"$ROOT/.cache/gateway-watchdog.log" 2>&1 &
echo $! >"$WATCHDOG_PID_FILE"
echo "[dev] gateway watchdog started (pid=$(cat "$WATCHDOG_PID_FILE"))"

if lsof -ti "tcp:${WEB_PORT}" >/dev/null 2>&1; then
  echo "[dev] stopping stale vite on port ${WEB_PORT}"
  lsof -ti "tcp:${WEB_PORT}" | xargs kill 2>/dev/null || true
  sleep 0.4
fi

cd "$ROOT/web"
exec npm run dev:web
