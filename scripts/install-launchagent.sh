#!/usr/bin/env bash
# Install / reload LaunchAgent so the gateway survives Cursor quit and reboot.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LABEL="com.luca.llm-protocol-gateway"
PLIST_SRC="$ROOT/scripts/${LABEL}.plist.template"
PLIST_DST="$HOME/Library/LaunchAgents/${LABEL}.plist"
WRAPPER="$ROOT/scripts/run-gateway-service.sh"

chmod +x "$WRAPPER" "$ROOT/scripts/ensure-gateway.sh" "$ROOT/scripts/install-launchagent.sh"

# 装之前先确认启动脚本真的在：plist 里的路径是写死的，脚本没了 launchd 会
# 无限重拉一个必然失败的作业并持续写 stderr。宁可这里失败，也别留下这种东西。
if [[ ! -f "$WRAPPER" ]]; then
  echo "启动脚本不存在：$WRAPPER" >&2
  echo "LaunchAgent 指向的是源码目录，请先补回该文件（或在源码目录里重新安装）。" >&2
  exit 1
fi

mkdir -p "$HOME/Library/LaunchAgents" "$ROOT/.cache"
sed -e "s|__ROOT__|${ROOT}|g" -e "s|__HOME__|${HOME}|g" "$PLIST_SRC" >"$PLIST_DST"

# 写入后回读一遍：模板与 ROOT 里如果有字符把 XML 弄坏（路径带 & 之类），
# launchctl bootstrap 只会给一句含糊的报错，不如在这里说清楚。
if command -v plutil >/dev/null 2>&1; then
  plutil -lint "$PLIST_DST" >/dev/null || { echo "生成的 plist 不合法：$PLIST_DST" >&2; exit 1; }
fi

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
