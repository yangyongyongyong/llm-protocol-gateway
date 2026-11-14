#!/usr/bin/env bash
# 生成并打印本次发布的版本号。
#
# 发布版本只有这一个输入；各平台构建脚本必须把它注入所有产物：
#   1. 后端二进制   gateway.BuildVersion 与 desktop.version
#   2. 前端资源     GATEWAY_APP_VERSION → __APP_VERSION__
#   3. macOS 包     Info.plist 的 CFBundleShortVersionString / CFBundleVersion
#
# 格式：YYYYMMDD.HHMMSS（UTC）。等长、可直接按字符串比较，不会因为版本号
# 位数变化而比错。可用 GATEWAY_APP_VERSION 覆盖（手工发版 / 回滚到旧版本）。
#
# 用法：
#   ./scripts/build-version.sh            # 生成并打印版本号
#   ./scripts/build-version.sh --check A  # 判断 A 是否与当前工作区的版本一致
set -euo pipefail

if [[ "${1:-}" == "--check" ]]; then
  want="${2:-}"
  have="$(date -u +%Y%m%d.%H%M%S)"
  if [[ -n "${GATEWAY_APP_VERSION:-}" ]]; then
    have="$GATEWAY_APP_VERSION"
  fi
  [[ "$want" == "$have" ]] && exit 0
  printf '版本不一致：构建时为 %s，当前为 %s\n' "${want:-<空>}" "$have" >&2
  exit 1
fi

if [[ -n "${GATEWAY_APP_VERSION:-}" ]]; then
  printf '%s\n' "$GATEWAY_APP_VERSION"
  exit 0
fi
date -u +%Y%m%d.%H%M%S
