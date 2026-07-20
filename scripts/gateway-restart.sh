#!/usr/bin/env bash
# 原子重启 launchd 守护的网关。
# 把「停旧实例 -> 交给 launchd 重启 -> 等健康」作为一个自包含进程串行跑完，
# 即使调用者自己的模型流量走这个网关、切换瞬间连接抖动，机器上这段脚本也会
# 独立跑完把网关恢复，绝不会出现「停了却没拉起」。
#
# 唯一守护者是 launchd 服务 com.luca.llm-protocol-gateway；
# 不要再同时用 ensure-gateway.sh（两个保活器打架 = 双网关/双隧道）。
set -uo pipefail
LABEL="com.luca.llm-protocol-gateway"
UIDN="$(id -u)"
PORT="${GATEWAY_PORT:-18093}"

echo "[gateway-restart] 停当前网关（SIGTERM 优雅，连带停它的 cloudflared）"
pkill -x gateway-dev 2>/dev/null || true
sleep 4

echo "[gateway-restart] 交给 launchd 重启（$LABEL）"
launchctl kickstart -k "gui/$UIDN/$LABEL" 2>/dev/null || true

echo "[gateway-restart] 等健康（<=90s，含可能的重新构建）"
for i in $(seq 1 90); do
  if curl -sf "http://127.0.0.1:${PORT}/__health" >/dev/null 2>&1; then
    echo "[gateway-restart] HEALTHY after ${i}s"; break
  fi
  [ $((i % 15)) -eq 0 ] && launchctl kickstart -k "gui/$UIDN/$LABEL" 2>/dev/null || true
  sleep 1
done

echo "[gateway-restart] launchd:     $(launchctl list | grep "$LABEL" || echo NOT-LOADED)"
echo "[gateway-restart] gateway-dev: $(pgrep -x gateway-dev | tr '\n' ' ')"
echo "[gateway-restart] local:       $(curl -s -o /dev/null -w '%{http_code}' --max-time 5  http://127.0.0.1:${PORT}/__health 2>/dev/null)"
echo "[gateway-restart] public:      $(curl -s -o /dev/null -w '%{http_code}' --max-time 12 https://user.lucadesign.uk/__health 2>/dev/null)"
