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

func installBundle(staged, bundle string) error {
	if _, err := os.Stat(staged); err != nil {
		return fmt.Errorf("更新目录不存在：%w", err)
	}
	if _, err := os.Stat(bundle); err != nil {
		return fmt.Errorf("当前 App 目录不存在：%w", err)
	}
	parent := filepath.Dir(bundle)
	old := filepath.Join(parent, fmt.Sprintf(".llm-protocol-gateway-old-%d", time.Now().UnixNano()))
	if err := os.Rename(bundle, old); err != nil {
		return fmt.Errorf("移动旧 App 失败：%w", err)
	}
	if err := os.Rename(staged, bundle); err != nil {
		_ = os.Rename(old, bundle)
		return fmt.Errorf("放入新 App 失败：%w", err)
	}
	// Keep the previous bundle until the new process starts. If macOS refuses to
	// launch the new one, the hidden backup is still on disk and can be restored.
	return nil
}

func cleanupOldBundles(bundle string) {
	parent := filepath.Dir(bundle)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), ".llm-protocol-gateway-old-") {
			_ = os.RemoveAll(filepath.Join(parent, entry.Name()))
		}
	}
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
