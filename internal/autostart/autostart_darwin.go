//go:build darwin

package autostart

import (
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/launchagent"
)

const (
	launchAgentLabel = "com.wails.llm-protocol-gateway.autostart"
	loginItemsURL    = "x-apple.systempreferences:com.apple.LoginItems-Settings.extension"

	smNotRegistered    = 0
	smEnabled          = 1
	smRequiresApproval = 2
	smNotFound         = 3
	// Before macOS 13, or a build without cgo, there is nothing to ask.
	smUnknown = -1
)

const appBundleID = "com.wails.llm-protocol-gateway"

var bundleIdentifierRE = regexp.MustCompile(`(?s)<key>CFBundleIdentifier</key>\s*<string>([^<]+)</string>`)

// These are vars so the package can be exercised with a temporary HOME and a
// fake executable instead of touching the real login items during tests.
var (
	userHomeDir     = os.UserHomeDir
	userConfigDir   = os.UserConfigDir
	appExecutable   = os.Executable
	applicationsDir = "/Applications"
	runCommand      = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput()
	}
	// legacyLaunchAgentStatus is filled by the cgo build on macOS 13+. It asks
	// ServiceManagement whether the user switched the background item off in
	// System Settings; launchctl print-disabled does not follow that switch.
	legacyLaunchAgentStatus = func(string) int { return smUnknown }
)

type darwinManager struct{}

func newManager() Manager { return darwinManager{} }

func (darwinManager) Status() Status {
	return readStatus()
}

func (darwinManager) Set(enabled bool) (Status, error) {
	if enabled {
		if err := enable(); err != nil {
			return readStatus(), err
		}
	} else if err := disable(); err != nil {
		return readStatus(), err
	}
	status := readStatus()
	if enabled && !status.Enabled {
		return status, fmt.Errorf("启动项写入后系统仍未识别，请检查 ~/Library/LaunchAgents/%s.plist", launchAgentLabel)
	}
	return status, nil
}

// EnsureDefault turns the item on once for a fresh install. The marker lives in
// Application Support, outside the LaunchAgent: deleting the login record later
// must mean "off", not "uninitialized".
func (darwinManager) EnsureDefault() error {
	marker := defaultMarkerPath()
	if _, err := os.Stat(marker); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查开机自动启动默认标记失败：%w", err)
	}

	_, appPath, err := recordTarget()
	if err != nil || !isInstalledAppPath(appPath) {
		// Not a stable installed copy yet. Leave the marker absent so moving the
		// App to Applications and opening it again can apply the default.
		return nil
	}
	status := readStatus()
	if !status.Available {
		return nil
	}

	// Write the choice marker before touching the system. If enabling fails, remove
	// it so the next launch can retry instead of permanently claiming the default
	// was applied.
	if err := writeAtomic(marker, []byte("1\n")); err != nil {
		return fmt.Errorf("写入开机自动启动默认标记失败：%w", err)
	}
	if !status.Enabled {
		if _, err := (darwinManager{}).Set(true); err != nil {
			_ = os.Remove(marker)
			return fmt.Errorf("应用开机自动启动默认值失败：%w", err)
		}
	}
	return nil
}

func defaultMarkerPath() string {
	dir, err := userConfigDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		home, _ := userHomeDir()
		dir = filepath.Join(home, "Library", "Application Support")
	}
	return filepath.Join(dir, "llm-protocol-gateway", "autostart-default-applied")
}

func isInstalledAppPath(appPath string) bool {
	if strings.TrimSpace(appPath) == "" {
		return false
	}
	home, _ := userHomeDir()
	for _, dir := range []string{applicationsDir, filepath.Join(home, "Applications")} {
		if dir == "" {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			dir = resolved
		}
		rel, err := filepath.Rel(dir, appPath)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (darwinManager) OpenLoginItems() error {
	out, err := runCommand("open", loginItemsURL)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("打开系统登录项失败：%s", msg)
	}
	return nil
}

func readStatus() Status {
	_, appPath, err := recordTarget()
	if err != nil {
		return Status{Available: false, Message: err.Error(), AppPath: appPath}
	}

	path := launchAgentPath()
	st := Status{
		Available: true,
		AppPath:   appPath,
		Mechanism: "launchAgent",
	}
	raw, readErr := os.ReadFile(path)
	switch {
	case errors.Is(readErr, os.ErrNotExist):
		return st
	case readErr != nil:
		st.Message = fmt.Sprintf("读取启动项失败：%v", readErr)
		return st
	}

	plist, parseErr := launchagent.Parse(raw)
	if parseErr != nil {
		st.Message = fmt.Sprintf("启动项文件无法解析：%v", parseErr)
		return st
	}
	if plist.Label != launchAgentLabel || len(plist.ProgramArguments) == 0 || strings.TrimSpace(plist.ProgramArguments[0]) == "" {
		st.Message = "启动项文件不完整，关闭后重新开启可修复"
		return st
	}

	st.Enabled = true
	if current := strings.TrimSpace(plist.ProgramArguments[0]); current != "" {
		if _, statErr := os.Stat(current); statErr != nil {
			st.Message = fmt.Sprintf("启动项仍指向已不存在的文件：%s；关闭后重新开启可重新绑定当前 App", current)
		}
	}
	if legacyLaunchAgentStatus(path) == smRequiresApproval {
		st.NeedsApproval = true
	}
	return st
}

func enable() error {
	exe, _, err := recordTarget()
	if err != nil {
		return err
	}
	return writeAtomic(launchAgentPath(), launchAgentPlist(exe))
}

func disable() error {
	path := launchAgentPath()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除启动项失败：%w", err)
	}
	// The job is normally only loaded at login, so bootout often reports that no
	// process exists. That is success for this operation; anything else is not
	// worth turning into a failed toggle because removing the plist is enough
	// for the next login.
	uid := ""
	if u, err := user.Current(); err == nil {
		uid = u.Uid
	}
	if uid == "" {
		uid = fmt.Sprintf("%d", os.Getuid())
	}
	_, _ = runCommand("launchctl", "bootout", fmt.Sprintf("gui/%s/%s", uid, launchAgentLabel))
	return nil
}

func launchAgentPath() string {
	home, err := userHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
}

// currentApp returns the executable named by the login record and the enclosing
// .app. A command-line binary or a translocated download has no stable app path,
// so it cannot safely create a login item.
func currentApp() (exe, appPath string, err error) {
	exe, err = appExecutable()
	if err != nil {
		return "", "", fmt.Errorf("无法定位当前 App：%w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(exe); resolveErr == nil {
		exe = resolved
	}
	exe = filepath.Clean(exe)
	if strings.Contains(exe, "/AppTranslocation/") {
		return exe, "", fmt.Errorf("请先把 App 移到「应用程序」文件夹，再开启开机自动启动")
	}
	for dir := filepath.Dir(exe); dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		if strings.HasSuffix(dir, ".app") {
			return exe, dir, nil
		}
	}
	return exe, "", fmt.Errorf("当前不是 macOS .app 运行环境，无法设置开机自动启动")
}

// recordTarget prefers the installed copy over a development build. During a
// Wails build, LaunchServices can run the .app under desktop/build/bin while
// the same app is already installed in /Applications; the login record must
// never point into the build directory where it disappears on the next clean.
func recordTarget() (exe, appPath string, err error) {
	exe, appPath, err = currentApp()
	if err != nil {
		return exe, appPath, err
	}
	installedApp := filepath.Join(applicationsDir, filepath.Base(appPath))
	if installedApp == appPath || bundleIdentifier(installedApp) != appBundleID {
		return exe, appPath, nil
	}
	installedExe := filepath.Join(installedApp, "Contents", "MacOS", filepath.Base(exe))
	if _, statErr := os.Stat(installedExe); statErr != nil {
		return exe, appPath, nil
	}
	if resolved, resolveErr := filepath.EvalSymlinks(installedExe); resolveErr == nil {
		installedExe = resolved
	}
	if resolved, resolveErr := filepath.EvalSymlinks(installedApp); resolveErr == nil {
		installedApp = resolved
	}
	return installedExe, installedApp, nil
}

func bundleIdentifier(appPath string) string {
	raw, err := os.ReadFile(filepath.Join(appPath, "Contents", "Info.plist"))
	if err != nil {
		return ""
	}
	m := bundleIdentifierRE.FindSubmatch(raw)
	if len(m) != 2 {
		return ""
	}
	return string(m[1])
}

func launchAgentPlist(exe string) []byte {
	home, _ := userHomeDir()
	path := strings.Join([]string{
		filepath.Join(home, ".bun", "bin"),
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".opencode", "bin"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
		"/usr/sbin",
		"/sbin",
	}, ":")
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array><string>%s</string></array>
	<key>RunAtLoad</key><true/>
	<key>LimitLoadToSessionType</key><string>Aqua</string>
	<key>ProcessType</key><string>Interactive</string>
	<key>AbandonProcessGroup</key><true/>
	<key>EnvironmentVariables</key>
	<dict><key>PATH</key><string>%s</string></dict>
	<key>AssociatedBundleIdentifiers</key>
	<array><string>com.wails.llm-protocol-gateway</string></array>
</dict>
</plist>
`, launchAgentLabel, html.EscapeString(exe), html.EscapeString(path)))
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建目录失败：%w", err)
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败：%w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o644); err != nil {
		_ = f.Close()
		return fmt.Errorf("设置文件权限失败：%w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("写入文件失败：%w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败：%w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("安装文件失败：%w", err)
	}
	return nil
}
