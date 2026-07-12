#!/usr/bin/env bash
# Persistent wrapper used by LaunchAgent. Builds if needed, then execs gateway.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ADDR="${GATEWAY_ADDR:-0.0.0.0:18093}"
CACHE_DIR="$ROOT/.cache"
BIN="$CACHE_DIR/gateway-dev"
LOG_FILE="$CACHE_DIR/gateway.log"
PID_FILE="$CACHE_DIR/gateway.pid"

mkdir -p "$CACHE_DIR"

# LaunchAgent 不会继承交互式 shell 的环境变量。
# 不能 source ~/.zshrc（zsh 语法会卡住 bash）。改为解析常见 profile / .env
# 中的 `export KEY=VALUE`，仅导入 Provider 常用密钥变量。
import_env_exports_from_file() {
  local file="$1"
  [[ -f "$file" ]] || return 0
  local line key value
  while IFS= read -r line || [[ -n "$line" ]]; do
    # Match: export KEY=VALUE / export KEY="VALUE" / KEY=VALUE
    [[ "$line" =~ ^[[:space:]]*(export[[:space:]]+)?([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]] || continue
    key="${BASH_REMATCH[2]}"
    value="${BASH_REMATCH[3]}"
    case "$key" in
      TUYA_COMPANY_API_KEY|TUYA_AI_GATEWAY_API_KEY|ANTHROPIC_API_KEY|OPENAI_API_KEY|DEEPSEEK_API_KEY) ;;
      *) continue ;;
    esac
    # Strip surrounding quotes.
    if [[ "$value" =~ ^\"(.*)\"[[:space:]]*$ ]]; then
      value="${BASH_REMATCH[1]}"
    elif [[ "$value" =~ ^\'(.*)\'[[:space:]]*$ ]]; then
      value="${BASH_REMATCH[1]}"
    else
      value="${value%%#*}"
      value="${value%"${value##*[![:space:]]}"}"
    fi
    [[ -n "$value" ]] || continue
    export "$key=$value"
  done <"$file"
}

load_user_env() {
  set +u
  import_env_exports_from_file "$HOME/.bash_profile"
  import_env_exports_from_file "$HOME/.profile"
  import_env_exports_from_file "$HOME/.zprofile"
  import_env_exports_from_file "$HOME/.zshrc"
  import_env_exports_from_file "$ROOT/.env"
  set -u
}
load_user_env

# LaunchAgent PATH is minimal; ensure user-local CLIs (bun/opencode/cloudflared) are visible.
prepend_path_if_dir() {
  local dir="$1"
  [[ -d "$dir" ]] || return 0
  case ":${PATH:-}:" in
    *":$dir:"*) ;;
    *) export PATH="$dir:${PATH:-/usr/bin:/bin}" ;;
  esac
}
prepend_path_if_dir "$HOME/.bun/bin"
prepend_path_if_dir "$HOME/.local/bin"
prepend_path_if_dir "$HOME/.opencode/bin"
prepend_path_if_dir "/opt/homebrew/bin"
prepend_path_if_dir "/usr/local/bin"

if [[ ! -x "$BIN" ]] || [[ "$ROOT/cmd/gateway" -nt "$BIN" ]] || find "$ROOT/internal" -newer "$BIN" -print -quit | grep -q .; then
  echo "[gateway-service] building -> $BIN" >>"$LOG_FILE"
  (cd "$ROOT" && go build -o "$BIN" ./cmd/gateway)
fi

WEB_DIST="$ROOT/web/dist"
if [[ ! -f "$WEB_DIST/index.html" ]] \
  || find "$ROOT/web/src" -newer "$WEB_DIST/index.html" -print -quit 2>/dev/null | grep -q . \
  || [[ "$ROOT/web/index.html" -nt "$WEB_DIST/index.html" ]] \
  || [[ "$ROOT/web/vite.config.ts" -nt "$WEB_DIST/index.html" ]]; then
  echo "[gateway-service] building web UI -> $WEB_DIST" >>"$LOG_FILE"
  (cd "$ROOT/web" && npm run build >>"$LOG_FILE" 2>&1)
fi

echo $$ >"$PID_FILE"
export GATEWAY_ADDR="$ADDR"
export GATEWAY_REPO_ROOT="$ROOT"
exec "$BIN"
