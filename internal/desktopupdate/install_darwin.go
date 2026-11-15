//go:build darwin

package desktopupdate

import (
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const expectedBundleID = "com.wails.llm-protocol-gateway"

var plistKeyValue = regexp.MustCompile(`(?s)<key>(CFBundleIdentifier|CFBundleShortVersionString|CFBundleExecutable)</key>\s*<string>(.*?)</string>`)

// stageDownload uses ditto rather than archive/zip: ditto preserves the
// executable bits and bundle metadata that a code signature expects.
func stageDownload(ctx context.Context, zipPath, dir, wantVersion string) (string, error) {
	out := filepath.Join(dir, "unpack")
	_ = os.RemoveAll(out)
	if err := os.MkdirAll(out, 0o700); err != nil {
		return "", err
	}
	if b, err := exec.CommandContext(ctx, "ditto", "-x", "-k", zipPath, out).CombinedOutput(); err != nil {
		_ = os.RemoveAll(out)
		return "", fmt.Errorf("解压更新包失败：%s %w", strings.TrimSpace(string(b)), err)
	}
	app, err := findAppBundle(out)
	if err != nil {
		_ = os.RemoveAll(out)
		return "", err
	}
	if err := validateApp(app, wantVersion); err != nil {
		_ = os.RemoveAll(out)
		return "", err
	}
	payload := filepath.Join(dir, "payload")
	_ = os.Remove(payload)
	if err := os.Rename(app, payload); err != nil {
		_ = os.RemoveAll(out)
		return "", fmt.Errorf("准备更新目录失败：%w", err)
	}
	_ = os.RemoveAll(out)
	_ = os.Remove(zipPath)
	return payload, nil
}

func findAppBundle(root string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && strings.HasSuffix(d.Name(), ".app") && found == "" {
			found = path
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", errors.New("更新包里没有找到 .app")
	}
	return found, nil
}

func validateApp(app string, wantVersion string) error {
	info, err := os.ReadFile(filepath.Join(app, "Contents", "Info.plist"))
	if err != nil {
		return fmt.Errorf("更新包缺少 Info.plist：%w", err)
	}
	values := map[string]string{}
	for _, m := range plistKeyValue.FindAllStringSubmatch(string(info), -1) {
		if len(m) == 3 {
			values[m[1]] = html.UnescapeString(strings.TrimSpace(m[2]))
		}
	}
	if values["CFBundleIdentifier"] != expectedBundleID {
		return fmt.Errorf("更新包的应用标识不匹配：%q", values["CFBundleIdentifier"])
	}
	if got := strings.TrimPrefix(values["CFBundleShortVersionString"], "v"); got != cleanVersion(wantVersion) {
		return fmt.Errorf("更新包版本不匹配：发布清单为 %s，App Info.plist 为 %s。该 Release 的安装包元数据有误，请发布修复版后再更新", cleanVersion(wantVersion), got)
	}
	exeName := values["CFBundleExecutable"]
	if exeName == "" || strings.Contains(exeName, "/") {
		return errors.New("更新包没有有效的 CFBundleExecutable")
	}
	exe := filepath.Join(app, "Contents", "MacOS", exeName)
	if fi, err := os.Stat(exe); err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
		return errors.New("更新包的可执行文件无效")
	}
	if _, err := exec.LookPath("codesign"); err == nil {
		if b, err := exec.Command("codesign", "--verify", "--deep", "--strict", app).CombinedOutput(); err != nil {
			return fmt.Errorf("更新包签名校验失败：%s", strings.TrimSpace(string(b)))
		}
	}
	return nil
}

func installBundle(staged, bundle string) (string, error) {
	if _, err := os.Stat(staged); err != nil {
		return "", fmt.Errorf("更新目录不存在：%w", err)
	}
	if _, err := os.Stat(bundle); err != nil {
		return "", fmt.Errorf("当前 App 目录不存在：%w", err)
	}
	parent := filepath.Dir(bundle)
	old := filepath.Join(parent, fmt.Sprintf(".llm-protocol-gateway-previous-%d.app", time.Now().UnixNano()))
	if err := os.Rename(bundle, old); err != nil {
		return "", fmt.Errorf("移动旧 App 失败：%w", err)
	}
	if err := os.Rename(staged, bundle); err != nil {
		_ = os.Rename(old, bundle)
		return "", fmt.Errorf("放入新 App 失败：%w", err)
	}
	// The detached watchdog keeps this bundle until the new build has passed its
	// health window. It is the actual rollback source, not just a startup spare.
	return old, nil
}

// Relaunch arranges for open -n to run after this process has exited. Doing it
// from a detached shell avoids LaunchServices sending the open event to the
// process that is already gone.
func Relaunch(bundle string) error {
	if strings.TrimSpace(bundle) == "" {
		return errors.New("App 路径为空")
	}
	script := fmt.Sprintf("while kill -0 %d 2>/dev/null; do sleep 0.2; done; open -n %s", os.Getpid(), shellQuote(bundle))
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

const rollbackWatchdogScript = `
new_bundle=$1
old_bundle=$2
prev_version=$3
new_version=$4
commit_marker=$5
request_marker=$6
pid_file=$7
history_file=$8
journal_file=$9
log_file=${10}
old_pid=${11}
timeout_seconds=${12}

umask 077
exec >>"$log_file" 2>&1
echo "[rollback-watchdog] start $(date -u +%Y-%m-%dT%H:%M:%SZ) old_pid=$old_pid timeout=${timeout_seconds}s"

if [ -f "$pid_file" ]; then
  other_pid=$(cat "$pid_file" 2>/dev/null || true)
  if [ -n "$other_pid" ] && kill -0 "$other_pid" 2>/dev/null; then
    echo "[rollback-watchdog] another watchdog is alive: $other_pid"
    exit 0
  fi
fi
printf '%s\n' "$$" >"$pid_file"
cleanup() { rm -f "$pid_file" 2>/dev/null || true; }
trap cleanup EXIT
trap 'exit 143' HUP INT TERM

if [ "$old_pid" -gt 0 ] 2>/dev/null; then
  while kill -0 "$old_pid" 2>/dev/null; do sleep 0.2; done
fi

started=$(date +%s)
seen_new=0
gone_seconds=0
reason=""
while :; do
  if [ -f "$commit_marker" ]; then
    echo "[rollback-watchdog] health window committed"
    exit 0
  fi
  if [ -f "$request_marker" ]; then
    reason=$(tr '\t\r\n' '   ' <"$request_marker" | cut -c1-500)
    [ -n "$reason" ] || reason="App requested rollback"
    break
  fi
  now=$(date +%s)
  elapsed=$((now - started))
  if pgrep -f "^${new_bundle}/Contents/MacOS/LLM Protocol Gateway" >/dev/null 2>&1; then
    seen_new=1
    gone_seconds=0
  elif [ "$seen_new" -eq 1 ]; then
    gone_seconds=$((gone_seconds + 1))
    if [ "$gone_seconds" -ge 20 ]; then
      reason="New App exited during rollback health window"
      break
    fi
  fi
  if [ "$seen_new" -eq 0 ] && [ "$elapsed" -ge 45 ]; then
    reason="New App did not start within 45s"
    break
  fi
  if [ "$elapsed" -ge "$timeout_seconds" ]; then
    reason="New App health window timed out"
    break
  fi
  sleep 1
done

echo "[rollback-watchdog] rollback requested: $reason"
for pid in $(pgrep -f "^${new_bundle}/Contents/MacOS/LLM Protocol Gateway" 2>/dev/null || true); do
  kill "$pid" 2>/dev/null || true
done
sleep 1

if [ ! -d "$old_bundle" ]; then
  echo "[rollback-watchdog] previous bundle missing: $old_bundle"
  rm -f "$request_marker" "$commit_marker" "$journal_file" 2>/dev/null || true
  exit 1
fi

failed_bundle="${new_bundle}.failed-$(date +%s)"
if [ -e "$new_bundle" ]; then
  mv "$new_bundle" "$failed_bundle" || exit 1
fi
if ! mv "$old_bundle" "$new_bundle"; then
  echo "[rollback-watchdog] failed to restore previous bundle"
  [ -e "$failed_bundle" ] && mv "$failed_bundle" "$new_bundle" 2>/dev/null || true
  exit 1
fi

safe_reason=$(printf '%s' "$reason" | tr '\t\r\n' '   ' | cut -c1-500)
printf '%s\t%s\t%s\t%s\n' "$new_version" "$prev_version" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$safe_reason" >>"$history_file"
rm -rf "$failed_bundle" 2>/dev/null || true
rm -f "$request_marker" "$commit_marker" "$journal_file" 2>/dev/null || true
echo "[rollback-watchdog] restored $prev_version and relaunching"
open -n "$new_bundle"
`

func launchRollbackWatchdog(_ context.Context, artifacts rollbackArtifacts, journal rollbackJournal, oldPID int, timeout time.Duration) error {
	if timeout < 5*time.Second {
		timeout = 5 * time.Second
	}
	logFile, err := os.OpenFile(artifacts.WatchdogLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.Command(
		"/bin/sh", "-c", rollbackWatchdogScript, "wt-rollback",
		journal.BundlePath,
		journal.BackupPath,
		journal.PreviousVersion,
		journal.NewVersion,
		artifacts.Commit,
		artifacts.Request,
		artifacts.PID,
		artifacts.History,
		artifacts.Journal,
		artifacts.WatchdogLog,
		strconv.Itoa(oldPID),
		strconv.Itoa(int(timeout.Seconds())),
	)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	return logFile.Close()
}

func restoreRollbackBundle(backup, bundle string) error {
	if _, err := os.Stat(backup); err != nil {
		return fmt.Errorf("上一版本不存在：%w", err)
	}
	failed := filepath.Join(filepath.Dir(bundle), fmt.Sprintf(".llm-protocol-gateway-failed-%d.app", time.Now().UnixNano()))
	if _, err := os.Stat(bundle); err == nil {
		if err := os.Rename(bundle, failed); err != nil {
			return fmt.Errorf("移走异常版本失败：%w", err)
		}
	}
	if err := os.Rename(backup, bundle); err != nil {
		_ = os.Rename(failed, bundle)
		return fmt.Errorf("恢复上一版本失败：%w", err)
	}
	_ = os.RemoveAll(failed)
	return nil
}
