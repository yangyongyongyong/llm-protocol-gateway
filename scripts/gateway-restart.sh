#!/usr/bin/env bash
# Atomic restart of the launchd-supervised gateway.
# Stop old -> hand off to launchd -> wait for health, as ONE self-contained
# process, so it completes even if the caller's own model traffic routes through
# this gateway and blips during the swap.
#
# Single supervisor = launchd (com.luca.llm-protocol-gateway).
# Do NOT also run ensure-gateway.sh / dev.sh (two supervisors = double tunnel).
#
# NOTE: all variable refs use ${VAR} braces on purpose. A bare $VAR placed next
# to a multibyte (Chinese) character can, under a non-UTF-8 locale + `set -u`,
# absorb the char's first byte into the variable name and abort mid-restart.
set -uo pipefail
export LC_ALL="${LC_ALL:-en_US.UTF-8}"

LABEL="com.luca.llm-protocol-gateway"
UIDN="$(id -u)"
PORT="${GATEWAY_PORT:-18093}"
HEALTH="http://127.0.0.1:${PORT}/__health"

echo "[gateway-restart] stopping current gateway (graceful; also stops its cloudflared)"
pkill -x gateway-dev 2>/dev/null || true
sleep 4

echo "[gateway-restart] restart via launchd: ${LABEL}"
launchctl kickstart -k "gui/${UIDN}/${LABEL}" 2>/dev/null || true

echo "[gateway-restart] waiting for health (<=90s, includes rebuild)"
ok=0
for i in $(seq 1 90); do
  if curl -sf "${HEALTH}" >/dev/null 2>&1; then
    echo "[gateway-restart] HEALTHY after ${i}s"
    ok=1
    break
  fi
  if [ "$(( i % 15 ))" -eq 0 ]; then
    launchctl kickstart -k "gui/${UIDN}/${LABEL}" 2>/dev/null || true
  fi
  sleep 1
done

[ "${ok}" -eq 1 ] || echo "[gateway-restart] WARNING: still unhealthy after 90s; check .cache/gateway-launchd.err.log"

echo "[gateway-restart] launchd:     $(launchctl list | grep "${LABEL}" || echo NOT-LOADED)"
echo "[gateway-restart] gateway-dev: $(pgrep -x gateway-dev | tr '\n' ' ')"
echo "[gateway-restart] local:       $(curl -s -o /dev/null -w '%{http_code}' --max-time 5  "${HEALTH}" 2>/dev/null)"
echo "[gateway-restart] public:      $(curl -s -o /dev/null -w '%{http_code}' --max-time 12 https://user.lucadesign.uk/__health 2>/dev/null)"
