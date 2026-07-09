#!/usr/bin/env bash
# Keeps the Go gateway process alive during local development.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ADDR="${GATEWAY_ADDR:-0.0.0.0:18093}"
PORT="${ADDR##*:}"
HEALTH_ADDR="127.0.0.1:${PORT}"
INTERVAL="${GATEWAY_WATCHDOG_INTERVAL:-2}"

health() {
  local response
  response="$(curl -sf "http://${HEALTH_ADDR}/__health" 2>/dev/null || true)"
  [[ "$response" == *'"status":"ok"'* ]]
}

echo "[gateway-watchdog] monitoring http://${HEALTH_ADDR} (bind ${ADDR}) every ${INTERVAL}s"

while true; do
  if ! health; then
    echo "[gateway-watchdog] gateway unhealthy, restarting..."
    bash "$ROOT/scripts/ensure-gateway.sh" || echo "[gateway-watchdog] restart failed; will retry"
  fi
  sleep "$INTERVAL"
done
