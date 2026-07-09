#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
ARCH="${ARCH:-arm64}"
# Wails packaging needs sysctl (in /usr/sbin); keep a sane PATH for non-interactive shells.
export PATH="/usr/sbin:/usr/bin:/bin:/opt/homebrew/bin:/usr/local/bin:${HOME}/go/bin:${PATH}"

WAILS_BIN="${WAILS_BIN:-}"
if [[ -z "$WAILS_BIN" ]]; then
  if command -v wails >/dev/null 2>&1; then
    WAILS_BIN="$(command -v wails)"
  elif [[ -x "$HOME/go/bin/wails" ]]; then
    WAILS_BIN="$HOME/go/bin/wails"
  else
    echo "wails CLI not found. Install with: go install github.com/wailsapp/wails/v2/cmd/wails@latest" >&2
    exit 1
  fi
fi

echo "==> building web UI"
(
  cd "$ROOT/web"
  npm run build
)

echo "==> syncing desktop frontend shell"
(
  cd "$ROOT/desktop/frontend"
  node ./sync-dist.mjs
)

echo "==> go mod tidy (desktop)"
(
  cd "$ROOT/desktop"
  go mod tidy
)

PLATFORM="darwin/$ARCH"
echo "==> wails build ($PLATFORM)"
(
  cd "$ROOT/desktop"
  "$WAILS_BIN" build -platform "$PLATFORM" -clean
)

APP_PATH="$ROOT/desktop/build/bin/LLM Protocol Gateway.app"
if [[ ! -d "$APP_PATH" ]]; then
  APP_PATH="$(find "$ROOT/desktop/build/bin" -maxdepth 1 -name '*.app' -print -quit || true)"
fi

if [[ -z "${APP_PATH:-}" || ! -d "$APP_PATH" ]]; then
  echo "build finished but .app not found under desktop/build/bin" >&2
  ls -la "$ROOT/desktop/build/bin" || true
  exit 1
fi

chmod +x "$ROOT/scripts/bundle-app-resources.sh"
"$ROOT/scripts/bundle-app-resources.sh" "$APP_PATH" "$ARCH"

# Ad-hoc re-sign after adding binaries (required on Apple Silicon).
if command -v codesign >/dev/null 2>&1; then
  echo "==> ad-hoc codesign"
  codesign --force --deep --sign - "$APP_PATH" 2>/dev/null || true
fi

echo ""
echo "OK: $APP_PATH"
echo "Self-contained: web UI + cloudflared + bun + cursor-bridge"
echo "Tip: if LaunchAgent owns :18093, the App will offer to stop it on launch."
echo "     Or manually: launchctl bootout gui/\$(id -u)/com.luca.llm-protocol-gateway"
