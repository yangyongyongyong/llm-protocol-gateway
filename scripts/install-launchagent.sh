#!/usr/bin/env bash
# Install / reload LaunchAgent so the gateway survives Cursor quit and reboot.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LABEL="com.luca.llm-protocol-gateway"
PLIST_SRC="$ROOT/scripts/${LABEL}.plist.template"
PLIST_DST="$HOME/Library/LaunchAgents/${LABEL}.plist"
WRAPPER="$ROOT/scripts/run-gateway-service.sh"

chmod +x "$WRAPPER" "$ROOT/scripts/ensure-gateway.sh" "$ROOT/scripts/install-launchagent.sh"

mkdir -p "$HOME/Library/LaunchAgents" "$ROOT/.cache"
sed "s|__ROOT__|${ROOT}|g" "$PLIST_SRC" >"$PLIST_DST"

# Stop any ad-hoc gateway first so LaunchAgent owns the port.
if [[ -f "$ROOT/.cache/gateway.pid" ]]; then
  old_pid="$(cat "$ROOT/.cache/gateway.pid" || true)"
  if [[ -n "${old_pid}" ]] && kill -0 "$old_pid" 2>/dev/null; then
    kill "$old_pid" 2>/dev/null || true
    sleep 0.4
  fi
fi
if lsof -ti tcp:18093 >/dev/null 2>&1; then
  lsof -ti tcp:18093 | xargs kill 2>/dev/null || true
  sleep 0.4
fi

launchctl bootout "gui/$(id -u)/${LABEL}" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$PLIST_DST"
launchctl enable "gui/$(id -u)/${LABEL}" 2>/dev/null || true
launchctl kickstart -k "gui/$(id -u)/${LABEL}"

for _ in $(seq 1 40); do
  if curl -sf "http://127.0.0.1:18093/__health" 2>/dev/null | grep -q '"status":"ok"'; then
    echo "LaunchAgent installed and gateway healthy"
    echo "plist: $PLIST_DST"
    echo "manage: launchctl kickstart -k gui/$(id -u)/${LABEL}"
    echo "unload: launchctl bootout gui/$(id -u)/${LABEL}"
    exit 0
  fi
  sleep 0.25
done

echo "LaunchAgent installed but gateway health check failed; see:" >&2
echo "  $ROOT/.cache/gateway-launchd.err.log" >&2
echo "  $ROOT/.cache/gateway.log" >&2
exit 1
