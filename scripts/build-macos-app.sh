#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
ARCH="${ARCH:-arm64}"
# 版本号：CI 从 tag 传进来，本地构建时取 CURRENT_APP_VERSION 或退回 0.0.0-dev。
# 前端与二进制都要带同一个值，否则控制台的升级判断会错位（release.yml 会校验）。
APP_VERSION="${GATEWAY_APP_VERSION:-${CURRENT_APP_VERSION:-0.0.0-dev}}"
export GATEWAY_APP_VERSION="$APP_VERSION"
LDFLAGS="-X github.com/luca/llm-protocol-gateway/internal/gateway.BuildVersion=$APP_VERSION -X github.com/luca/llm-protocol-gateway/desktop.version=$APP_VERSION"
if [[ -n "${UPDATE_PUBLIC_KEY:-}" ]]; then
  LDFLAGS="$LDFLAGS -X github.com/luca/llm-protocol-gateway/internal/desktopupdate.SignaturePublicKey=$UPDATE_PUBLIC_KEY"
fi
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

echo "==> embedding web UI into the desktop binary"
bash "$ROOT/scripts/bundle-desktop-assets.sh"

echo "==> go mod tidy (desktop)"
(
  cd "$ROOT/desktop"
  go mod tidy
)

PLATFORM="darwin/$ARCH"
echo "==> wails build ($PLATFORM, version $APP_VERSION)"
(
  cd "$ROOT/desktop"
  # 后端 BuildVersion 与控制台版本必须一致：release.yml 会用 strings 校验产物里
  # 确实带着本次 tag 的版本号，缺了这一步 macOS 包会全部判失败。
  "$WAILS_BIN" build -platform "$PLATFORM" -clean \
    -tags desktop_ui \
    -ldflags "$LDFLAGS"
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

# Wails takes productVersion from wails.json unless the bundle is patched. The
# build version is the single source of truth: stamp both plist keys and fail
# the build if Wails or a future template silently keeps its placeholder.
/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $APP_VERSION" "$APP_PATH/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleVersion $APP_VERSION" "$APP_PATH/Contents/Info.plist"
SHORT_VERSION="$(/usr/libexec/PlistBuddy -c "Print :CFBundleShortVersionString" "$APP_PATH/Contents/Info.plist")"
BUNDLE_VERSION="$(/usr/libexec/PlistBuddy -c "Print :CFBundleVersion" "$APP_PATH/Contents/Info.plist")"
if [[ "$SHORT_VERSION" != "$APP_VERSION" || "$BUNDLE_VERSION" != "$APP_VERSION" ]]; then
  echo "App 版本戳不一致：期望 $APP_VERSION，Info.plist=$SHORT_VERSION/$BUNDLE_VERSION" >&2
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
