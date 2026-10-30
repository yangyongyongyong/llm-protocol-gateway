#!/usr/bin/env bash
#
# deploy-linux.sh — 一键部署 llm-protocol-gateway 到 Linux 服务器
#
# 流程：本机交叉编译（前端 + 后端）→ 打包 → scp 上传 → 服务器替换 → 重启 → 健康检查
#       健康检查失败会自动回滚到部署前的版本。
#
# 用法：
#   ./deploy/deploy-linux.sh                # 完整部署（构建 + 上传 + 重启 + 验证）
#   ./deploy/deploy-linux.sh --skip-web     # 跳过前端构建（只更后端）
#   ./deploy/deploy-linux.sh --allow-dirty  # 明知工作区有未提交改动仍构建（产物不可回滚到 commit）
#   ./deploy/deploy-linux.sh --build-only   # 只构建 + 校验溯源，不上传（CI / 提前排错）
#   ./deploy/deploy-linux.sh --skip-go      # 跳过后端构建（只更前端）
#   ./deploy/deploy-linux.sh --rollback     # 回滚到服务器上最近一次备份
#   ./deploy/deploy-linux.sh --status       # 只查看服务器当前状态，不改动任何东西
#
# 溯源规则（round35 P0-1）：默认只允许从干净工作区部署，且构建后必须校验
#   二进制内嵌的 vcs.modified=false、vcs.revision == git rev-parse HEAD，否则拒绝上传。
#   显式跳过（--allow-dirty / --skip-go 复用旧产物 / 无 vcs 信息）只能靠
#   ALLOW_UNVERIFIED_BUILD=1 放行，并会打印醒目的警告。校验脚本：scripts/verify-build-provenance.sh
#
# 可用环境变量覆盖默认配置（无需改脚本）：
#   SERVER_HOST   目标服务器 IP/域名      必填（无默认值）
#   SERVER_USER   SSH 用户                默认 root
#   SSH_KEY       私钥路径                默认 ~/.ssh/greencloud_new
#   SSH_PORT      SSH 端口                默认 22
#   INSTALL_DIR   服务器安装目录          默认 /opt/llm-protocol-gateway
#   SERVICE_NAME  systemd 服务名          默认 llm-protocol-gateway
#   GOARCH_TARGET 目标架构                默认 amd64（arm64 服务器请改）
#   INSTALL_RTK   rtk CLI 安装策略        默认 auto（缺失才装），可选 always/never
#   RTK_VERSION   rtk 版本（如 v0.50.0）  默认空=安装最新版
#   PUBLIC_URL    对外验证地址           默认 https://llm.lucadesign.uk（网关只监听回环，
#                                         IP:18093 已不再对外，留空则跳过公网验证）
#   ALLOW_UNVERIFIED_BUILD  1=允许溯源校验不通过继续部署（仅应急，务必记录原因）
#
# 示例：
#   SERVER_HOST=1.2.3.4 SSH_KEY=~/.ssh/id_ed25519 ./deploy/deploy-linux.sh
#   SERVER_HOST=1.2.3.4 GOARCH_TARGET=arm64 ./deploy/deploy-linux.sh
#
set -euo pipefail

# ───────────────────────── 配置区 ─────────────────────────
SERVER_HOST="${SERVER_HOST:-}"
SERVER_USER="${SERVER_USER:-root}"
SSH_KEY="${SSH_KEY:-$HOME/.ssh/greencloud_new}"
SSH_PORT="${SSH_PORT:-22}"
INSTALL_DIR="${INSTALL_DIR:-/opt/llm-protocol-gateway}"
DATA_DIR="${DATA_DIR:-/var/lib/llm-protocol-gateway}"
SERVICE_NAME="${SERVICE_NAME:-llm-protocol-gateway}"
GOARCH_TARGET="${GOARCH_TARGET:-amd64}"
KEEP_BACKUPS="${KEEP_BACKUPS:-5}"   # 服务器上保留的历史版本份数
# RTK 是可选依赖：Provider 开启「RTK 压缩」时才用得到。auto 只在缺失时安装，
# always 每次部署都升级到最新/指定版本，never 完全不碰服务器上的 rtk。
INSTALL_RTK="${INSTALL_RTK:-auto}"
RTK_VERSION="${RTK_VERSION:-}"
ALLOW_UNVERIFIED_BUILD="${ALLOW_UNVERIFIED_BUILD:-0}"
# 网关由 Caddy 反代、只监听 127.0.0.1，公网验证必须走 HTTPS 域名。
PUBLIC_URL="${PUBLIC_URL-https://llm.lucadesign.uk}"

SKIP_WEB=0
ALLOW_DIRTY=0
SKIP_GO=0
MODE="deploy"

# ───────────────────────── 工具函数 ─────────────────────────
c_red=$'\e[31m'; c_green=$'\e[92m'; c_yellow=$'\e[33m'; c_cyan=$'\e[96m'; c_gray=$'\e[90m'; c_none=$'\e[0m'
info()  { printf '%s\n' "${c_cyan}▶ $*${c_none}"; }
ok()    { printf '%s\n' "${c_green}✓ $*${c_none}"; }
warn()  { printf '%s\n' "${c_yellow}! $*${c_none}"; }
die()   { printf '%s\n' "${c_red}✗ $*${c_none}" >&2; exit 1; }
step()  { printf '\n%s\n' "${c_cyan}==== $* ====${c_none}"; }

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# ssh 用 -p 指定端口，scp 用 -P（小写 -p 在 scp 里是「保留时间戳」，写错会把端口号当成文件名）
SSH_BASE_OPTS=(-i "$SSH_KEY" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=15)
SSH_OPTS=("${SSH_BASE_OPTS[@]}" -p "$SSH_PORT")
SCP_OPTS=("${SSH_BASE_OPTS[@]}" -P "$SSH_PORT")
REMOTE="${SERVER_USER}@${SERVER_HOST}"
rsh() { ssh "${SSH_OPTS[@]}" "$REMOTE" "$@"; }

usage() { sed -n '/^# 流程：/,/^set -euo pipefail$/{/^set -euo pipefail$/d;p}' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0; }

# ───────────────────────── 参数解析 ─────────────────────────
for arg in "$@"; do
  case "$arg" in
    --skip-web) SKIP_WEB=1 ;;
    --skip-go)  SKIP_GO=1 ;;
    --allow-dirty) ALLOW_DIRTY=1 ;;
    --build-only) MODE="build" ;;
    --rollback) MODE="rollback" ;;
    --status)   MODE="status" ;;
    -h|--help)  usage ;;
    *) die "未知参数: $arg（用 --help 查看用法）" ;;
  esac
done

# ───────────────────────── 前置检查 ─────────────────────────
step "0. 前置检查"
if [[ "$MODE" != "build" && -z "$SERVER_HOST" ]]; then
  die "未指定目标服务器：SERVER_HOST=1.2.3.4 ./deploy/deploy-linux.sh（SSH 用户另用 SERVER_USER 指定）"
fi
if [[ "$MODE" != "build" ]]; then
  [[ -f "$SSH_KEY" ]] || die "找不到 SSH 私钥: $SSH_KEY（用 SSH_KEY=... 指定）"
  command -v ssh >/dev/null || die "缺少 ssh 命令"
  command -v scp >/dev/null || die "缺少 scp 命令"
fi
command -v tar >/dev/null || die "缺少 tar 命令"
command -v go >/dev/null || die "缺少 go 命令（溯源校验需要 go version -m）"
command -v git >/dev/null || die "缺少 git 命令（溯源校验需要 git rev-parse）"

if [[ "$MODE" == "deploy" ]]; then
  [[ "$SKIP_GO"  == "1" ]] || command -v go >/dev/null  || die "缺少 go 命令（或用 --skip-go）"
  [[ "$SKIP_WEB" == "1" ]] || command -v npm >/dev/null || die "缺少 npm 命令（或用 --skip-web）"
elif [[ "$MODE" == "build" ]]; then
  [[ "$SKIP_WEB" == "1" ]] || command -v npm >/dev/null || die "缺少 npm 命令（或用 --skip-web）"
fi

# ── 源码必须干净、且构建的必须是已提交的 HEAD ──
# 检查已跟踪文件的改动/新增/删除。未跟踪文件（.cache/、bin/ 等构建产物）不算，
# 否则每轮构建都会拦住部署；但它们会被打包（web/dist、cursor-bridge/dist），见下面的溯源校验。
# 用 diff-index HEAD 而不是 git status（Go 的 buildvcs 也用它判 modified）：
#   git add 过但没提交的改动 status 会显示为 "M "，diff-index HEAD 仍然算脏；
#   而 vcs.modified 只反映未跟踪文件和构建期才发生的改动，单靠它查不出「源码与 HEAD 不一致」。
#   （git ls-files -m 不行：bash 的 [[ -n ]] 会忽略只含空白字符的输出。）
TRACKED_DIRTY="$(git status --porcelain --untracked-files=no)"
commit_now() {
  local rev; rev="$(git rev-parse HEAD 2>/dev/null)"
  [[ -n "$rev" ]] || die "无法解析 git HEAD，拒绝部署（源码不是从 git 仓库构建的）"
  printf '%s' "$rev"
}
# 只接受退出码 0/1：被 git 配置（pager / 彩色输出 / diff 外部工具）干扰产生的其他退出码一律按脏处理。
tree_is_clean() { git -c core.pager=cat -c color.ui=never -c diff.external= diff-index --quiet HEAD --; }
if ! tree_is_clean && [[ "$ALLOW_DIRTY" != "1" ]]; then
  warn "工作区有未提交改动（含已 add 未 commit 的）："
  git status --porcelain --untracked-files=no | sed 's/^/    /'
  die "拒绝构建脏包。先提交改动，或在明知不可回滚时加 --allow-dirty"
fi
EXPECT_REVISION="$(commit_now)"
# 本次发布的版本号：同时注入前端（__APP_VERSION__）与后端（BuildVersion），
# 控制台据此判断「后端已换了一版」，提示用户刷新升级。
APP_VERSION="$(./scripts/build-version.sh)"
info "发布版本: $APP_VERSION"
if ! tree_is_clean; then
  warn "工作区与 HEAD 不一致（--allow-dirty）：产物无法对应到 commit，回滚只能凭这份产物本身"
fi
info "目标 commit: $EXPECT_REVISION  ($(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo detached))"

case "$INSTALL_RTK" in
  auto|always|never) ;;
  *) die "INSTALL_RTK 只能是 auto / always / never（当前: $INSTALL_RTK）" ;;
esac
# 版本号会拼进远程 shell 命令，先挡掉可能的注入字符。
if [[ -n "$RTK_VERSION" && ! "$RTK_VERSION" =~ ^v?[0-9][0-9A-Za-z._-]*$ ]]; then
  die "RTK_VERSION 格式不合法: $RTK_VERSION（示例: v0.50.0）"
fi

if [[ "$MODE" != "build" ]]; then
  info "目标: ${REMOTE}:${SSH_PORT}  目录: ${INSTALL_DIR}  服务: ${SERVICE_NAME}"
  rsh "echo ok" >/dev/null 2>&1 || die "SSH 连接失败，请检查 ${SSH_KEY} 与服务器状态"
  ok "SSH 连接正常"
  [[ $EUID -eq 0 ]] && warn "当前以 root 运行；本地构建其实不需要 root"
fi

# ═════════════════════ 模式：status ═════════════════════
if [[ "$MODE" == "status" ]]; then
  step "服务器状态"
  rsh "
    echo '--- 服务状态 ---';
    systemctl is-active ${SERVICE_NAME} 2>/dev/null || true;
    systemctl is-enabled ${SERVICE_NAME} 2>/dev/null || true;
    echo '--- 二进制 ---';
    ls -la ${INSTALL_DIR}/bin/gateway 2>/dev/null || true;
    echo '--- 备份 ---';
    ls -1 ${INSTALL_DIR}/bin/gateway.bak-* 2>/dev/null | tail -5 || echo '(无备份)';
    echo '--- RTK ---';
    command -v rtk >/dev/null 2>&1 && rtk --version || echo '(未安装)';
    echo '--- 健康检查 ---';
    curl -s --max-time 5 http://127.0.0.1:18093/__health || echo '(无响应)';
    echo;
    echo '--- 最近日志 ---';
    journalctl -u ${SERVICE_NAME} -n 8 --no-pager 2>/dev/null | tail -8;
  "
  exit 0
fi

# ═════════════════════ 模式：rollback ═════════════════════
if [[ "$MODE" == "rollback" ]]; then
  step "回滚到最近一次备份"
  rsh "
    set -e
    LATEST=\$(ls -1t ${INSTALL_DIR}/bin/gateway.bak-* 2>/dev/null | head -1 || true)
    if [ -z \"\$LATEST\" ]; then echo '没有可用备份，退出'; exit 1; fi
    TS=\${LATEST##*gateway.bak-}
    WEB=${INSTALL_DIR}/web/dist.bak-\${TS}.tar.gz
    echo \"使用备份: \$LATEST\"
    [ -f \"\$WEB\" ] && echo \"前端备份: \$WEB\" || echo '（无前端备份，仅回滚后端）'
    # 必须先停服务：运行中的进程占用着二进制，直接覆盖会报 'Text file busy'
    systemctl stop ${SERVICE_NAME}
    cp \"\$LATEST\" ${INSTALL_DIR}/bin/gateway
    chmod +x ${INSTALL_DIR}/bin/gateway
    if [ -f \"\$WEB\" ]; then
      rm -rf ${INSTALL_DIR}/web/dist
      mkdir -p ${INSTALL_DIR}/web/dist
      tar xzf \"\$WEB\" -C ${INSTALL_DIR}/web
    fi
    systemctl start ${SERVICE_NAME}
    sleep 3
    systemctl is-active ${SERVICE_NAME}
    curl -s --max-time 5 http://127.0.0.1:18093/__health; echo
  "
  ok "回滚完成"
  exit 0
fi

# ═════════════════════ 模式：deploy ═════════════════════

# ── 1. 构建前端 ──
if [[ "$SKIP_WEB" == "0" ]]; then
  step "1. 构建前端 (web/dist)"
  cd "$ROOT/web"
  # Windows/Git-Bash 上 safe-delete 守卫会阻止 npm 清理 node_modules，
  # 因此只在依赖缺失/损坏时才安装，且安装前把旧目录「移走」而非删除。
  if [[ ! -x "node_modules/.bin/tsc" ]]; then
    if [[ -d "node_modules" ]]; then
      warn "node_modules 不完整，移走后重装"
      mv node_modules "node_modules.broken.$(date +%s)"
    fi
    info "npm ci ..."
    npm ci
  else
    info "依赖已就绪，跳过 npm ci"
  fi
  info "npm run build (版本 $APP_VERSION) ..."
  GATEWAY_APP_VERSION="$APP_VERSION" npm run build
  [[ -f "dist/index.html" ]] || die "前端构建失败：web/dist/index.html 不存在"
  # 版本号必须真的编进 JS：控制台靠 __APP_VERSION__ 和后端 BuildVersion 比对来提示升级。
  # 手工 `npm run build`（没带 GATEWAY_APP_VERSION）产出的 dist 会被当成开发构建，
  # 永久关闭升级提示——曾经就这样发过一版，用户再也没收到刷新提醒。
  grep -q -- "$APP_VERSION" dist/assets/*.js \
    || die "前端产物里找不到版本号 $APP_VERSION（dist 被没有注入版本号的构建覆盖了？拒绝发布）"
  ok "前端构建完成（已校验版本号 $APP_VERSION）"
  cd "$ROOT"
else
  warn "跳过前端构建（--skip-web）"
fi

# ── 2. 构建后端 ──
# 线上二进制必须能对应到一个 commit：带着未提交改动编译出来的包，vcs.modified=true，
# 回滚时没有任何 commit 可回滚，只能凭记忆重编（round26 G26-1 就是这样丢了两次）。
# 脏工作区的拦截已在「0. 前置检查」里做，这里只负责构建与构建后的溯源校验。
if [[ "$SKIP_GO" == "0" ]]; then
  step "2. 交叉编译后端 (linux/${GOARCH_TARGET})"
  mkdir -p bin
  info "go build ..."
  # 不加 -buildvcs 参数，用默认 auto：go build 自己内嵌 vcs.revision / vcs.modified。
  # 注意不要放进 git 仓库*外*的临时目录（如 /tmp）构建：go 会静默跳过 VCS 标记（二进制
  # 里没有 vcs 信息），校验脚本会拒绝这种产物。
  CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH_TARGET" \
    go build -trimpath -ldflags "-s -w -X github.com/luca/llm-protocol-gateway/internal/gateway.BuildVersion=$APP_VERSION" -o bin/gateway-linux ./cmd/gateway
  [[ -f bin/gateway-linux ]] || die "后端构建失败"
  ok "后端构建完成: $(ls -lh bin/gateway-linux | awk '{print $5}')"
else
  warn "跳过后端构建（--skip-go）"
  [[ -f bin/gateway-linux ]] || die "跳过后端构建，但 bin/gateway-linux 不存在，无法上传"
fi

# ── 2.5 溯源校验：二进制必须内嵌 vcs 信息，modified=false，revision == 目标 commit ──
# 拦截的绕过路径：工作区与 HEAD 不一致（--allow-dirty）、复用旧产物（--skip-go 指向的不是刚构建
# 的文件）、在仓库外/临时目录构建（没有 vcs 信息）、构建时漏掉 -trimpath 等。通过才允许上传。
# （vcs.modified 只反映未跟踪文件和构建期改动，「已跟踪文件未提交」由上面的 diff-index 拦截。）
step "2.5 构建溯源校验"
PROVENANCE_OK=0
# --allow-dirty：先构建、再改文件重编一次，让内嵌标记如实反映脏工作区；
# 直接跳过校验会让「脏包」无声无息地上线。（只对已跟踪文件重编：bin/ 之类未跟踪产物不影响标记。）
if ! tree_is_clean; then
  warn "工作区与 HEAD 不一致，重新构建并如实内嵌 vcs.modified=true ..."
  CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH_TARGET" \
    go build -trimpath -ldflags "-s -w -X github.com/luca/llm-protocol-gateway/internal/gateway.BuildVersion=$APP_VERSION" -o bin/gateway-linux ./cmd/gateway
fi
if bash "$ROOT/scripts/verify-build-provenance.sh" bin/gateway-linux "$EXPECT_REVISION"; then
  # 可复现性：同一 commit 两次构建应得到逐字节相同的二进制。缺 -trimpath（例如 GOFLAGS 里
  # 把它覆盖掉、或 .git/go.env 设置了 GOFLAGS）会让产物带本机绝对路径，别人无法复现、
  # 也就无法独立核对服务器上的文件。这里只告警不拦截。
  if ! go version -m bin/gateway-linux 2>/dev/null | grep -q -- '-trimpath=true'; then
    warn "产物缺少 -trimpath（检查 GOFLAGS / .git/go.env）：两次构建不会逐字节相同"
  fi
  PROVENANCE_OK=1
else
  warn "溯源校验未通过（见上）"
  if [[ "$ALLOW_UNVERIFIED_BUILD" != "1" ]]; then
    die "拒绝上传：产物无法对应到 commit $EXPECT_REVISION。修正方式：提交改动后重新部署；应急可用 ALLOW_UNVERIFIED_BUILD=1 跳过（须在发布记录里写明原因）"
  fi
  warn "ALLOW_UNVERIFIED_BUILD=1：跳过溯源校验继续部署——本次上线的产物无法证明来源，请在发布记录里写明原因"
fi

if [[ "$MODE" == "build" ]]; then
  step "构建完成（--build-only，不上传）"
  [[ "$PROVENANCE_OK" == "1" ]] && ok "bin/gateway-linux 溯源校验通过 @ $EXPECT_REVISION" || warn "bin/gateway-linux 溯源校验未通过"
  exit 0
fi

# ── 3. 打包 ──
step "3. 打包产物"
# Cursor bridge 预编译：服务器上往往没有 bun、node 也偏老（跑不了带 enum 的 TS），
# 所以在本机把 TS + 依赖打成单个纯 JS bundle 一起上服务器，node ≥18 直接能跑。
if [[ -x web/node_modules/.bin/rolldown ]]; then
  mkdir -p scripts/cursor-bridge/dist
  web/node_modules/.bin/rolldown scripts/cursor-bridge/standalone.ts \
    --format esm --platform node \
    --file scripts/cursor-bridge/dist/cursor-bridge.mjs >/dev/null 2>&1 \
    || die "Cursor bridge 打包失败（rolldown 报错，见上）"
  cp scripts/cursor-bridge/h2-bridge.mjs scripts/cursor-bridge/dist/h2-bridge.mjs
  ok "已打包 Cursor bridge: scripts/cursor-bridge/dist/cursor-bridge.mjs"
else
  warn "找不到 web/node_modules/.bin/rolldown：跳过 bridge 打包，服务器上将无法使用 Cursor Provider"
fi
# 注意：必须用 POSIX 路径（如 /tmp/xxx）。Git-Bash 下 mktemp -t 会返回
# "C:\...\Temp\xxx" 这种带盘符的 Windows 路径，tar 会把 "C:" 误判为远程主机而报错。
PKG="/tmp/gateway-dist.$$.tar.gz"
trap 'rm -f "$PKG"' EXIT
TAR_ITEMS=()
[[ "$SKIP_GO"  == "0" ]] && TAR_ITEMS+=(bin/gateway-linux)
[[ "$SKIP_WEB" == "0" ]] && TAR_ITEMS+=(web/dist)
[[ -f scripts/cursor-bridge/dist/cursor-bridge.mjs ]] && TAR_ITEMS+=(scripts/cursor-bridge/dist)
tar czf "$PKG" "${TAR_ITEMS[@]}"
ok "已打包: $(ls -lh "$PKG" | awk '{print $5}')  [${TAR_ITEMS[*]}]"

# ── 4. 上传 ──
step "4. 上传到服务器"
REMOTE_TMP="/tmp/gateway-dist-$$.tar.gz"
scp "${SCP_OPTS[@]}" "$PKG" "${REMOTE}:${REMOTE_TMP}"
ok "上传完成"

# ── 5. 服务器部署 ──
step "5. 服务器部署（备份 → 替换 → 安装 rtk → 重启）"
rsh "
  set -e
  TS=\$(date +%F-%H%M%S)
  BK=${INSTALL_DIR}/bin/gateway.bak-\$TS
  WS=/tmp/gw-deploy-\$\$

  mkdir -p \$WS && tar xzf ${REMOTE_TMP} -C \$WS

  # 上一轮如果在「重启等待」里被 Ctrl-C，远程脚本来不及跑自己末尾的清理，
  # 包和解包目录会留在 /tmp。只动 3 小时前的，避免碰正在并行的另一轮部署。
  find /tmp -maxdepth 1 -name 'gateway-dist-*.tar.gz' -mmin +180 -delete 2>/dev/null || true
  find /tmp -maxdepth 1 -type d -name 'gw-deploy-*' -mmin +180 -exec rm -rf {} + 2>/dev/null || true

  # 备份现有产物（用于回滚 / 失败自动恢复）
  if [ -f ${INSTALL_DIR}/bin/gateway ]; then
    cp ${INSTALL_DIR}/bin/gateway \$BK
    echo \"备份二进制: \$BK\"
  fi
  if [ -d ${INSTALL_DIR}/web/dist ]; then
    tar czf ${INSTALL_DIR}/web/dist.bak-\$TS.tar.gz -C ${INSTALL_DIR}/web dist
    echo \"备份前端: ${INSTALL_DIR}/web/dist.bak-\$TS.tar.gz\"
  fi
  # 只保留最近 ${KEEP_BACKUPS} 份备份，避免磁盘无限增长
  ls -1t ${INSTALL_DIR}/bin/gateway.bak-* 2>/dev/null | tail -n +\$(( ${KEEP_BACKUPS} + 1 )) | while read -r old; do
    rm -f \"\$old\"; rm -f \"${INSTALL_DIR}/web/dist.bak-\${old##*gateway.bak-}.tar.gz\"
  done

  # 替换后端
  if [ -f \$WS/bin/gateway-linux ]; then
    mv \$WS/bin/gateway-linux ${INSTALL_DIR}/bin/gateway
    chmod +x ${INSTALL_DIR}/bin/gateway
    echo '已替换后端二进制'
  fi

  # 替换前端（先清旧 assets，避免哈希文件残留）
  if [ -d \$WS/web/dist ]; then
    rm -rf ${INSTALL_DIR}/web/dist/assets
    mkdir -p ${INSTALL_DIR}/web/dist
    cp -r \$WS/web/dist/. ${INSTALL_DIR}/web/dist/
    echo '已替换前端静态资源'
  fi

  # Cursor bridge：预编译 bundle，服务器只需要 node（无需 bun / npm install）
  if [ -d \$WS/scripts/cursor-bridge/dist ]; then
    mkdir -p ${INSTALL_DIR}/scripts/cursor-bridge
    rm -rf ${INSTALL_DIR}/scripts/cursor-bridge/dist
    cp -r \$WS/scripts/cursor-bridge/dist ${INSTALL_DIR}/scripts/cursor-bridge/dist
    echo '已替换 Cursor bridge bundle'
    if command -v node >/dev/null 2>&1; then
      echo \"服务器 node: \$(node -v)\"
    else
      echo '警告: 服务器没有 node，Cursor Provider 将不可用（安装 node >= 18 即可）'
    fi
  fi

  chown -R root:root ${INSTALL_DIR}

  # RTK 工具输出压缩依赖（可选）。装到 /usr/local/bin：systemd 默认 PATH 能命中，
  # 网关的候选路径探测也会找到；安装失败只告警，不能让整次部署回滚。
  if [ '${INSTALL_RTK}' = 'never' ]; then
    echo '跳过 rtk 安装（INSTALL_RTK=never）'
  elif command -v rtk >/dev/null 2>&1 && [ '${INSTALL_RTK}' != 'always' ]; then
    echo \"rtk 已安装: \$(rtk --version)\"
  elif ! command -v curl >/dev/null 2>&1; then
    echo '警告: 服务器没有 curl，无法自动安装 rtk；可手动安装或设 INSTALL_RTK=never'
  else
    RTK_SH=/tmp/rtk-install-\$\$.sh
    echo '安装 rtk CLI ...'
    if ! curl -fsSL -o \$RTK_SH https://raw.githubusercontent.com/rtk-ai/rtk/refs/heads/master/install.sh; then
      echo '警告: rtk 安装脚本下载失败，Provider 的 RTK 压缩暂不可用（其余功能不受影响）'
    elif RTK_INSTALL_DIR=/usr/local/bin ${RTK_VERSION:+RTK_VERSION=$RTK_VERSION} sh \$RTK_SH; then
      echo \"rtk 安装完成: \$(/usr/local/bin/rtk --version)\"
    else
      echo '警告: rtk 安装失败，Provider 的 RTK 压缩暂不可用（其余功能不受影响）'
    fi
    rm -f \$RTK_SH
  fi

  # 重启服务。systemd 在这一步会一直等旧进程退出，最长到 unit 的 TimeoutStopSec
  # （默认 90s）才升级为 SIGKILL；那段时间脚本一个字都不打，看起来就像卡死了。
  # 所以先报一行进度，并且把等待时长也报出来。
  echo '重启服务（等待旧进程优雅退出…）'
  RESTART_TS=\$(date +%s)
  systemctl restart ${SERVICE_NAME}
  echo \"重启耗时 \$(( \$(date +%s) - RESTART_TS ))s\"
  sleep 3

  # 健康检查（失败自动回滚）
  if systemctl is-active --quiet ${SERVICE_NAME} \
     && curl -fsS --max-time 8 http://127.0.0.1:18093/__health >/dev/null; then
    echo 'HEALTH_OK'
    curl -s --max-time 5 http://127.0.0.1:18093/__health; echo
  else
    echo 'HEALTH_FAIL'
    echo '--- 最近日志 ---'
    journalctl -u ${SERVICE_NAME} -n 20 --no-pager | tail -20
    if [ -f \$BK ]; then
      echo '--- 检测到失败，自动回滚 ---'
      systemctl stop ${SERVICE_NAME} || true
      cp \$BK ${INSTALL_DIR}/bin/gateway
      chmod +x ${INSTALL_DIR}/bin/gateway
      systemctl start ${SERVICE_NAME}
      sleep 3
      systemctl is-active ${SERVICE_NAME} || true
      echo '已回滚到部署前版本'
    fi
    rm -rf \$WS ${REMOTE_TMP}
    exit 1
  fi

  rm -rf \$WS ${REMOTE_TMP}
  echo '清理完成'
"

# ── 6. 外部验证 ──
if [[ -n "$PUBLIC_URL" ]]; then
  step "6. 公网访问验证"
  sleep 1
  CODE="$(curl -s -m 10 -o /dev/null -w '%{http_code}' "$PUBLIC_URL/" || echo 000)"
  if [[ "$CODE" =~ ^(200|301|302|401)$ ]]; then
    ok "公网访问正常（HTTP ${CODE}）"
  else
    warn "公网返回 HTTP ${CODE}，请检查 Caddy 站点与证书（PUBLIC_URL=${PUBLIC_URL}）"
  fi
fi

printf '\n%s\n' "${c_green}════════════════════════════════════════${c_none}"
ok "部署完成 → ${PUBLIC_URL:-${SERVER_HOST}}"
printf '%s\n' "${c_green}════════════════════════════════════════${c_none}"
