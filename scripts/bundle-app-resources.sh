#!/usr/bin/env bash
# Copy runtime dependencies into a macOS .app Contents/Resources so the App is
# self-contained (web UI + cloudflared + bun + cursor-bridge).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
APP_PATH="${1:-}"
ARCH="${2:-arm64}"

if [[ -z "$APP_PATH" || ! -d "$APP_PATH" ]]; then
  echo "usage: $0 /path/to/LLM Protocol Gateway.app [arm64|amd64]" >&2
  exit 1
fi

RESOURCES="$APP_PATH/Contents/Resources"
BIN_DIR="$RESOURCES/bin"
WEB_DIST_DST="$RESOURCES/web-dist"
BRIDGE_DST="$RESOURCES/cursor-bridge"
CACHE_DIR="$ROOT/.cache/app-bundle"
mkdir -p "$BIN_DIR" "$CACHE_DIR"

echo "==> bundling web/dist"
if [[ ! -f "$ROOT/web/dist/index.html" ]]; then
  (cd "$ROOT/web" && npm run build)
fi
rm -rf "$WEB_DIST_DST"
cp -R "$ROOT/web/dist" "$WEB_DIST_DST"

resolve_bin() {
  local name="$1"
  if command -v "$name" >/dev/null 2>&1; then
    python3 -c 'import os,sys; print(os.path.realpath(sys.argv[1]))' "$(command -v "$name")"
    return 0
  fi
  return 1
}

echo "==> bundling cloudflared ($ARCH)"
CLOUDFLARED_SRC=""
if CLOUDFLARED_SRC="$(resolve_bin cloudflared)"; then
  echo "    using local: $CLOUDFLARED_SRC"
else
  CF_VER="${CLOUDFLARED_VERSION:-2026.3.0}"
  case "$ARCH" in
    arm64) CF_ASSET="cloudflared-darwin-arm64.tgz" ;;
    amd64|x86_64) CF_ASSET="cloudflared-darwin-amd64.tgz"; ARCH=amd64 ;;
    *) echo "unsupported arch: $ARCH" >&2; exit 1 ;;
  esac
  CF_URL="https://github.com/cloudflare/cloudflared/releases/download/${CF_VER}/${CF_ASSET}"
  CF_TGZ="$CACHE_DIR/$CF_ASSET"
  if [[ ! -f "$CF_TGZ" ]]; then
    echo "    downloading $CF_URL"
    curl -fsSL -o "$CF_TGZ" "$CF_URL"
  fi
  tmp="$(mktemp -d)"
  tar -xzf "$CF_TGZ" -C "$tmp"
  CLOUDFLARED_SRC="$(find "$tmp" -type f -name cloudflared | head -1)"
  if [[ -z "$CLOUDFLARED_SRC" ]]; then
    echo "failed to extract cloudflared from $CF_TGZ" >&2
    exit 1
  fi
fi
cp "$CLOUDFLARED_SRC" "$BIN_DIR/cloudflared"
chmod 755 "$BIN_DIR/cloudflared"
xattr -dr com.apple.quarantine "$BIN_DIR/cloudflared" 2>/dev/null || true

echo "==> bundling bun ($ARCH)"
BUN_SRC=""
if BUN_SRC="$(resolve_bin bun)"; then
  echo "    using local: $BUN_SRC"
else
  BUN_VER="${BUN_VERSION:-1.2.19}"
  case "$ARCH" in
    arm64) BUN_ZIP="bun-darwin-aarch64.zip" ;;
    amd64) BUN_ZIP="bun-darwin-x64.zip" ;;
  esac
  BUN_URL="https://github.com/oven-sh/bun/releases/download/bun-v${BUN_VER}/${BUN_ZIP}"
  BUN_ZIP_PATH="$CACHE_DIR/$BUN_ZIP"
  if [[ ! -f "$BUN_ZIP_PATH" ]]; then
    echo "    downloading $BUN_URL"
    curl -fsSL -o "$BUN_ZIP_PATH" "$BUN_URL"
  fi
  tmp="$(mktemp -d)"
  unzip -q "$BUN_ZIP_PATH" -d "$tmp"
  BUN_SRC="$(find "$tmp" -type f \( -name bun -o -name bun.exe \) | head -1)"
  if [[ -z "$BUN_SRC" ]]; then
    echo "failed to extract bun from $BUN_ZIP_PATH" >&2
    exit 1
  fi
fi
cp "$BUN_SRC" "$BIN_DIR/bun"
chmod 755 "$BIN_DIR/bun"
xattr -dr com.apple.quarantine "$BIN_DIR/bun" 2>/dev/null || true

echo "==> bundling cursor-bridge"
rm -rf "$BRIDGE_DST"
mkdir -p "$BRIDGE_DST"
# Copy sources + lockfile; install production deps into the bundle.
cp "$ROOT/scripts/cursor-bridge/"*.ts "$BRIDGE_DST/" 2>/dev/null || true
cp "$ROOT/scripts/cursor-bridge/"*.mjs "$BRIDGE_DST/" 2>/dev/null || true
cp "$ROOT/scripts/cursor-bridge/package.json" "$BRIDGE_DST/"
[[ -f "$ROOT/scripts/cursor-bridge/bun.lock" ]] && cp "$ROOT/scripts/cursor-bridge/bun.lock" "$BRIDGE_DST/"
if [[ -d "$ROOT/scripts/cursor-bridge/proto" ]]; then
  cp -R "$ROOT/scripts/cursor-bridge/proto" "$BRIDGE_DST/proto"
fi
(
  cd "$BRIDGE_DST"
  "$BIN_DIR/bun" install --production --frozen-lockfile 2>/dev/null \
    || "$BIN_DIR/bun" install --production
)

# Clear quarantine on the whole Resources tree (Gatekeeper friendliness for local copies).
xattr -dr com.apple.quarantine "$RESOURCES" 2>/dev/null || true

echo "==> verifying bundle"
test -f "$WEB_DIST_DST/index.html"
test -x "$BIN_DIR/cloudflared"
test -x "$BIN_DIR/bun"
test -f "$BRIDGE_DST/standalone.ts"
test -d "$BRIDGE_DST/node_modules"

"$BIN_DIR/cloudflared" --version | head -1
"$BIN_DIR/bun" --version

du -sh "$BIN_DIR/cloudflared" "$BIN_DIR/bun" "$BRIDGE_DST" "$WEB_DIST_DST" | sed 's/^/    /'
echo "OK: bundled into $RESOURCES"
