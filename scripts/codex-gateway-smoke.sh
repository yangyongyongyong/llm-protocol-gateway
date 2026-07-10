#!/usr/bin/env bash
# Smoke-test local gateway via Codex CLI (Responses). Does not change default provider.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CODEX_BIN="${CODEX_BIN:-/Applications/ChatGPT.app/Contents/Resources/codex}"
GATEWAY_KEY="${OPENAI_API_KEY:-${GATEWAY_API_KEY:-}}"
MODEL="${MODEL:-gpt-5.3-codex}"
PROMPT="${1:-Reply with exactly: PONG}"
OUT_FILE="${OUT_FILE:-$ROOT/.cache/codex-gateway-smoke-last.txt}"

if [[ -z "$GATEWAY_KEY" || "$GATEWAY_KEY" == "PROXY_MANAGED" ]]; then
  echo "Set OPENAI_API_KEY (or GATEWAY_API_KEY) to a real gateway key (sk-gw-...)" >&2
  exit 1
fi
if [[ ! -x "$CODEX_BIN" ]]; then
  echo "Codex binary not found/executable: $CODEX_BIN" >&2
  exit 1
fi

bash "$ROOT/scripts/ensure-gateway.sh" >/dev/null

# Isolated CODEX_HOME so ~/.codex/auth.json PROXY_MANAGED does not override the key.
SMOKE_HOME="${SMOKE_HOME:-$ROOT/.cache/codex-gateway-smoke-home}"
rm -rf "$SMOKE_HOME"
mkdir -p "$SMOKE_HOME" "$(dirname "$OUT_FILE")"
cat >"$SMOKE_HOME/auth.json" <<EOF
{"OPENAI_API_KEY":"${GATEWAY_KEY}"}
EOF
cat >"$SMOKE_HOME/config.toml" <<EOF
model_provider = "gateway"
model = "${MODEL}"
model_reasoning_effort = "low"
sandbox_mode = "danger-full-access"
[model_providers.gateway]
name = "llm-gateway"
base_url = "http://127.0.0.1:18093/openai/v1"
wire_api = "responses"
requires_openai_auth = true
EOF

export CODEX_HOME="$SMOKE_HOME"
export OPENAI_API_KEY="$GATEWAY_KEY"

exec </dev/null
"$CODEX_BIN" exec \
  --ephemeral \
  --skip-git-repo-check \
  -s danger-full-access \
  -c 'model_reasoning_effort="low"' \
  -c 'features.multi_agent=false' \
  -c 'features.memories=false' \
  -o "$OUT_FILE" \
  "$PROMPT"

echo "---- last message ($OUT_FILE) ----"
cat "$OUT_FILE"
echo
