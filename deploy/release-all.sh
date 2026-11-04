#!/usr/bin/env bash
#
# release-all.sh — 一键发布到两台 VPS
#
# 两台机器的发布机制不同，本脚本把两条链路串起来：
#   sjc (173.249.199.226)  Docker 蓝绿：push 一个 v* tag → GitHub Actions 出镜像
#                          → Release 发布 → webhook → /opt/lpg-update/update.sh 自动切换
#   gc  (173.249.207.202)  systemd 裸机：本机交叉编译 → 溯源校验 → scp → 备份替换
#                          → 重启 → 健康检查（就是 deploy/deploy-linux.sh）
#
# 一次运行做的事：
#   1. 前置检查（git 仓库 / 分支 / 远程 / 版本号可用性）
#   2. 有改动就提交（-m 指定说明，缺省用 "chore(release): <tag>"）
#   3. push main 到两个仓库
#   4. 打 tag 并 push 到两个仓库 —— 这一步触发 sjc 的流水线
#   5. 跑 deploy/deploy-linux.sh 发布 gc
#   6. 等 sjc 的公网版本号变成刚打的 tag（可 --no-wait 跳过）
#
# 用法：
#   ./deploy/release-all.sh -m "feat(workbuddy): 任务中心"        # 常规：提交+推送+打tag+两台发布
#   ./deploy/release-all.sh -m "fix: x" --bump minor              # 版本号 v1.0.8 → v1.1.0
#   ./deploy/release-all.sh --tag v1.2.0 -m "release: 1.2.0"      # 指定 tag
#   ./deploy/release-all.sh --dry-run -m "test"                   # 只打印将要执行的命令
#   ./deploy/release-all.sh --skip-gc -m "only sjc"               # 只发 sjc
#   ./deploy/release-all.sh --skip-sjc -m "only gc"               # 只发 gc
#   ./deploy/release-all.sh --no-wait -m "不等待上线"             # 触发后立即返回
#
# 可用环境变量覆盖默认配置：
#   REMOTES        推送的远程名（空格分隔）        默认 "origin fork"
#   RELEASE_BRANCH 发布分支                        默认 main
#   GC_HOST        gc 的 IP/域名                   默认 173.249.207.202
#   SJC_URL        sjc 的公网地址（等版本用）      默认 https://gateway.baibaiwuchang.xyz
#   WAIT_TIMEOUT   等 sjc 上线的超时秒数           默认 1200（20 分钟）
#   SSH_KEY / SSH_PORT / SERVER_USER               透传给 deploy-linux.sh
#
# 注意：tag 会推到 REMOTES 里的**每一个**仓库；sjc 的 webhook 只挂在 fork
# （cus45/llm-protocol-gateway）上，所以 REMOTES 至少要包含它，否则流水线不会触发。
#
set -euo pipefail

# ───────────────────────── 配置区 ─────────────────────────
REMOTES="${REMOTES:-origin fork}"
RELEASE_BRANCH="${RELEASE_BRANCH:-main}"
GC_HOST="${GC_HOST:-173.249.207.202}"
SJC_URL="${SJC_URL:-https://gateway.baibaiwuchang.xyz}"
WAIT_TIMEOUT="${WAIT_TIMEOUT:-1200}"

SKIP_GC=0
SKIP_SJC=0
NO_WAIT=0
DRY_RUN=0
BUMP="patch"
TAG=""
COMMIT_MESSAGE=""

# ───────────────────────── 工具函数 ─────────────────────────
c_red=$'\e[31m'; c_green=$'\e[92m'; c_yellow=$'\e[33m'; c_cyan=$'\e[96m'; c_gray=$'\e[90m'; c_none=$'\e[0m'
info() { printf '%s\n' "${c_cyan}▶ $*${c_none}"; }
ok()   { printf '%s\n' "${c_green}✓ $*${c_none}"; }
warn() { printf '%s\n' "${c_yellow}! $*${c_none}"; }
die()  { printf '%s\n' "${c_red}✗ $*${c_none}" >&2; exit 1; }
step() { printf '\n%s\n' "${c_cyan}==== $* ====${c_none}"; }
dim()  { printf '%s\n' "${c_gray}$*${c_none}"; }

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

usage() { sed -n '/^# 一次运行做的事/,/^set -euo pipefail$/{/^set -euo pipefail$/d;p}' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0; }

# run 在 dry-run 下只打印命令；数组形式传参，避免再拼一次引号。
run() {
  if [[ "$DRY_RUN" == "1" ]]; then
    printf '%s  $ %s\n' "${c_gray}dry-run${c_none}" "$*"
    return 0
  fi
  "$@"
}

# ───────────────────────── 参数解析 ─────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    -m|--message) COMMIT_MESSAGE="${2:-}"; shift 2 ;;
    --message=*)  COMMIT_MESSAGE="${1#*=}"; shift ;;
    --tag)        TAG="${2:-}"; shift 2 ;;
    --tag=*)      TAG="${1#*=}"; shift ;;
    --bump)       BUMP="${2:-}"; shift 2 ;;
    --bump=*)     BUMP="${1#*=}"; shift ;;
    --remotes)    REMOTES="${2:-}"; shift 2 ;;
    --remotes=*)  REMOTES="${1#*=}"; shift ;;
    --skip-gc)    SKIP_GC=1; shift ;;
    --skip-sjc)   SKIP_SJC=1; shift ;;
    --no-wait)    NO_WAIT=1; shift ;;
    --dry-run)    DRY_RUN=1; shift ;;
    -h|--help)    usage ;;
    *) die "未知参数: $1（--help 查看用法）" ;;
  esac
done

# ───────────────────────── 1. 前置检查 ─────────────────────────
step "1. 前置检查"
command -v git >/dev/null || die "缺少 git"
command -v go  >/dev/null || die "缺少 go（deploy-linux.sh 的溯源校验要用）"
command -v curl >/dev/null || die "缺少 curl（等 sjc 上线要用）"

git rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "当前目录不是 git 仓库: $ROOT"
BRANCH="$(git rev-parse --abbrev-ref HEAD)"
[[ "$BRANCH" != "HEAD" ]] || die "处于 detached HEAD，先切回 $RELEASE_BRANCH"
if [[ "$BRANCH" != "$RELEASE_BRANCH" ]]; then
  warn "当前分支是 $BRANCH，发布分支约定为 $RELEASE_BRANCH"
  warn "继续会推 $BRANCH 到各远端的 $RELEASE_BRANCH；Ctrl-C 可中止"
fi

# 远程必须存在，且至少有一个是 sjc 的 hook 所在仓库（fork）。
REMOTE_LIST=($REMOTES)
[[ ${#REMOTE_LIST[@]} -gt 0 ]] || die "REMOTES 为空"
for remote in "${REMOTE_LIST[@]}"; do
  git remote get-url "$remote" >/dev/null 2>&1 || die "远程不存在: $remote（git remote -v 查看）"
done
if [[ "$SKIP_SJC" != "1" ]]; then
  has_fork=0
  for remote in "${REMOTE_LIST[@]}"; do
    if [[ "$(git remote get-url "$remote")" == *cus45/llm-protocol-gateway* ]]; then has_fork=1; fi
  done
  [[ "$has_fork" == "1" ]] || die "REMOTES 里没有 cus45/llm-protocol-gateway —— sjc 的 webhook 挂在那里，tag 打到别的仓库不会触发发布（要只发 gc 就用 --skip-sjc）"
fi

# 拿到各远程的最新 tag，版本号按全量 tag 递增
info "同步远程 tag…"
for remote in "${REMOTE_LIST[@]}"; do
  run git fetch --tags --quiet "$remote" || warn "从 $remote 拉取 tag 失败（继续用本地 tag）"
done

latest_tag() { git tag --list 'v[0-9]*' --sort=-v:refname | head -1; }

if [[ -z "$TAG" ]]; then
  current="$(latest_tag)"
  [[ -n "$current" ]] || die "仓库里没有任何 vX.Y.Z tag，用 --tag 指定第一个版本"
  base="${current#v}"
  IFS='.' read -r major minor patch <<<"$base"
  [[ "$major" =~ ^[0-9]+$ && "$minor" =~ ^[0-9]+$ && "$patch" =~ ^[0-9]+$ ]] || die "最新 tag 不是 vX.Y.Z 形态: $current"
  case "$BUMP" in
    patch) patch=$((patch + 1)) ;;
    minor) minor=$((minor + 1)); patch=0 ;;
    major) major=$((major + 1)); minor=0; patch=0 ;;
    *) die "--bump 只能是 patch / minor / major（当前: $BUMP）" ;;
  esac
  TAG="v${major}.${minor}.${patch}"
fi

[[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "tag 必须是 vX.Y.Z 形态（GitHub Actions 用 v* 触发），当前: $TAG"
if git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
  die "本地已存在 tag $TAG，换一个（--tag）或删掉它（git tag -d $TAG）"
fi
for remote in "${REMOTE_LIST[@]}"; do
  if git ls-remote --tags --exit-code "$remote" "refs/tags/$TAG" >/dev/null 2>&1; then
    die "$remote 上已存在 tag $TAG，换一个（--tag）"
  fi
done

[[ -f deploy/deploy-linux.sh ]] || die "找不到 deploy/deploy-linux.sh"
if [[ "$SKIP_GC" != "1" ]]; then
  # 用 bash 显式调用：仓库里 deploy-linux.sh 记录的是 644，直接 ./ 在 Linux/macOS
  # 上会因为缺可执行位而失败（Windows 的 Git Bash 看不出来，别依赖这种差异）。
  SERVER_HOST="${SERVER_HOST:-$GC_HOST}"
  export SERVER_HOST
fi

info "远端: ${REMOTE_LIST[*]}"
info "版本: $TAG（旧 tag: $(latest_tag)）"
info "目标: sjc=$( [[ "$SKIP_SJC" == "1" ]] && echo 跳过 || echo "$SJC_URL" )  gc=$( [[ "$SKIP_GC" == "1" ]] && echo 跳过 || echo "$SERVER_HOST" )"
if [[ "$DRY_RUN" == "1" ]]; then warn "dry-run：只打印命令，不提交、不推送、不发布"; fi

# ───────────────────────── 2. 提交 ─────────────────────────
step "2. 提交改动"
CHANGES="$(git status --porcelain)"
if [[ -z "$CHANGES" ]]; then
  info "工作区干净，跳过提交（本次只发版本 $TAG）"
  if [[ "$DRY_RUN" != "1" ]] && [[ -z "$COMMIT_MESSAGE" ]]; then
    : # 无改动时不需要提交说明
  fi
else
  [[ -n "$COMMIT_MESSAGE" ]] || COMMIT_MESSAGE="chore(release): $TAG"
  printf '%s\n' "$CHANGES" | sed 's/^/    /' | head -20
  count="$(printf '%s\n' "$CHANGES" | wc -l | tr -d ' ')"
  [[ "$count" -gt 20 ]] && dim "    …（共 $count 个改动）"
  run git add -A
  run git commit -m "$COMMIT_MESSAGE"
  ok "已提交：$COMMIT_MESSAGE"
fi
[[ -n "$COMMIT_MESSAGE" ]] || COMMIT_MESSAGE="chore(release): $TAG"

HEAD_REV="$(git rev-parse HEAD)"
info "HEAD: ${HEAD_REV:0:7}"

# ───────────────────────── 3. 推送主干 ─────────────────────────
step "3. 推送 $BRANCH"
for remote in "${REMOTE_LIST[@]}"; do
  if run git push "$remote" "HEAD:refs/heads/$RELEASE_BRANCH"; then
    ok "$remote ← $BRANCH"
  else
    die "推送到 $remote 失败（检查凭据/权限；只想发部分仓库就用 --remotes）"
  fi
done

# ───────────────────────── 4. 打 tag ─────────────────────────
step "4. 打 tag $TAG"
run git tag -a "$TAG" -m "$COMMIT_MESSAGE"
for remote in "${REMOTE_LIST[@]}"; do
  if run git push "$remote" "refs/tags/$TAG"; then
    ok "$remote ← $TAG"
  else
    die "推送 tag 到 $remote 失败"
  fi
done
if [[ "$SKIP_SJC" != "1" ]]; then
  info "sjc 流水线：tag $TAG → Docker 镜像 → Release → webhook → 蓝绿切换（约 5–10 分钟）"
  dim "  进度: https://github.com/cus45/llm-protocol-gateway/actions"
fi

# ───────────────────────── 5. 发布 gc ─────────────────────────
if [[ "$SKIP_GC" == "1" ]]; then
  step "5. 发布 gc（已跳过）"
else
  step "5. 发布 gc（deploy-linux.sh）"
  if [[ "$DRY_RUN" == "1" ]]; then
    printf '%s  $ SERVER_HOST=%s bash ./deploy/deploy-linux.sh\n' "${c_gray}dry-run${c_none}" "$SERVER_HOST"
  else
    if ! bash ./deploy/deploy-linux.sh; then
      die "gc 发布失败 —— 服务端已自动回滚到上一版；sjc 的流水线不受影响"
    fi
    ok "gc 已发布"
  fi
fi

# ───────────────────────── 6. 等 sjc 上线 ─────────────────────────
sjc_version() { curl -fsS --max-time 8 "$SJC_URL/__app/version" 2>/dev/null | sed -n 's/.*"version":"\([^"]*\)".*/\1/p'; }

if [[ "$SKIP_SJC" == "1" ]]; then
  step "6. 等待 sjc 上线（已跳过）"
elif [[ "$DRY_RUN" == "1" ]]; then
  step "6. 等待 sjc 上线（dry-run 不等待）"
elif [[ "$NO_WAIT" == "1" ]]; then
  step "6. 等待 sjc 上线（--no-wait）"
  info "当前线上版本: $(sjc_version || echo 未知)"
else
  step "6. 等待 sjc 上线（≤ $((WAIT_TIMEOUT / 60)) 分钟）"
  before="$(sjc_version || true)"
  info "当前线上版本: ${before:-未知}，目标是 $TAG"
  elapsed=0
  interval=15
  while :; do
    sleep "$interval"
    elapsed=$((elapsed + interval))
    now="$(sjc_version || true)"
    if [[ "$now" == "$TAG" ]]; then
      ok "sjc 已切到 $TAG（用时 ${elapsed}s）"
      break
    fi
    if [[ "$elapsed" -ge "$WAIT_TIMEOUT" ]]; then
      warn "等了 ${elapsed}s 仍未看到 $TAG（当前: ${now:-无响应}）"
      warn "流水线可能还在跑，或部署失败：https://github.com/cus45/llm-protocol-gateway/actions"
      break
    fi
    dim "  ${elapsed}s… 当前版本: ${now:-无响应}"
  done
fi

# ───────────────────────── 收尾 ─────────────────────────
step "发布结果"
printf '  %-26s %s\n' "版本 tag:" "$TAG"
printf '  %-26s %s\n' "提交:" "${HEAD_REV:0:7} $COMMIT_MESSAGE"
printf '  %-26s %s\n' "推送:" "${REMOTE_LIST[*]}"
if [[ "$SKIP_SJC" != "1" ]]; then
  printf '  %-26s %s\n' "sjc (Docker 蓝绿):" "$SJC_URL → $(sjc_version || echo 未知)"
fi
if [[ "$SKIP_GC" != "1" ]]; then
  if [[ "$DRY_RUN" == "1" ]]; then
    printf '  %-26s %s\n' "gc (裸机):" "dry-run 未发布"
  else
    gc_ver="$(ssh -i "${SSH_KEY:-$HOME/.ssh/greencloud_new}" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 \
      -p "${SSH_PORT:-22}" "${SERVER_USER:-root}@${SERVER_HOST}" \
      "curl -fsS --max-time 5 http://127.0.0.1:18093/__app/version 2>/dev/null" 2>/dev/null \
      | sed -n 's/.*"version":"\([^"]*\)".*/\1/p' || true)"
    printf '  %-26s %s\n' "gc (裸机):" "$SERVER_HOST → ${gc_ver:-未知}"
  fi
fi
printf '\n'
ok "一键发布完成"
