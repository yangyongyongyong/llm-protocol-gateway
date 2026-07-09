#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATEWAY_ADDR="${GATEWAY_ADDR:-0.0.0.0:18093}"
GATEWAY_PORT="${GATEWAY_ADDR##*:}"
GATEWAY_HEALTH_ADDR="127.0.0.1:${GATEWAY_PORT}"
WEB_ADDR="${WEB_ADDR:-127.0.0.1:5173}"
WEB_PORT="${WEB_ADDR##*:}"
VITE_LOG="$ROOT/.cache/vite-dev.log"
VITE_PID_FILE="$ROOT/.cache/vite.pid"

http_code() {
  curl -s -o /dev/null -w "%{http_code}" "$1" 2>/dev/null || echo "000"
}

stop_vite() {
  if [[ -f "$VITE_PID_FILE" ]]; then
    local old_pid
    old_pid="$(cat "$VITE_PID_FILE")"
    if kill -0 "$old_pid" 2>/dev/null; then
      kill "$old_pid" 2>/dev/null || true
      sleep 0.4
    fi
    rm -f "$VITE_PID_FILE"
  fi
  if lsof -ti "tcp:${WEB_PORT}" >/dev/null 2>&1; then
    lsof -ti "tcp:${WEB_PORT}" | xargs kill 2>/dev/null || true
    sleep 0.4
  fi
}

start_vite() {
  stop_vite
  mkdir -p "$ROOT/.cache"
  echo "starting vite at http://${WEB_ADDR}"
  (
    cd "$ROOT/web"
    nohup npm run dev:web >>"$VITE_LOG" 2>&1 &
    echo $! >"$VITE_PID_FILE"
  )
  for _ in $(seq 1 40); do
    if [[ "$(http_code "http://${WEB_ADDR}/")" == "200" ]]; then
      return 0
    fi
    sleep 0.2
  done
  echo "vite failed to start; see $VITE_LOG" >&2
  tail -20 "$VITE_LOG" >&2 || true
  return 1
}

check_all() {
  local failed=0
  local code

  code="$(http_code "http://${GATEWAY_HEALTH_ADDR}/__health")"
  if [[ "$code" == "200" ]]; then echo "OK  gateway /__health"; else echo "FAIL gateway /__health status=$code" >&2; failed=1; fi

  code="$(http_code "http://${GATEWAY_HEALTH_ADDR}/__state")"
  if [[ "$code" == "200" ]]; then echo "OK  gateway /__state"; else echo "FAIL gateway /__state status=$code" >&2; failed=1; fi

  code="$(http_code "http://${WEB_ADDR}/__health")"
  if [[ "$code" == "200" ]]; then echo "OK  vite proxy /__health"; else echo "FAIL vite proxy /__health status=$code" >&2; failed=1; fi

  code="$(http_code "http://${WEB_ADDR}/")"
  if [[ "$code" == "200" ]]; then echo "OK  web ui"; else echo "FAIL web ui status=$code" >&2; failed=1; fi

  return "$failed"
}

bash "$ROOT/scripts/ensure-gateway.sh"

if check_all; then
  echo "dev environment ready"
  exit 0
fi

echo "attempting auto-recovery..." >&2
bash "$ROOT/scripts/ensure-gateway.sh"
start_vite

if check_all; then
  echo "dev environment recovered"
  exit 0
fi

echo "dev environment verification failed" >&2
exit 1
