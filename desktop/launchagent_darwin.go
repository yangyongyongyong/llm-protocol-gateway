//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
)

func stopLaunchAgent(label string) (string, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return "", fmt.Errorf("empty launch agent label")
	}
	uid := ""
	if u, err := user.Current(); err == nil {
		uid = u.Uid
	}
	if uid == "" {
		uid = fmt.Sprintf("%d", os.Getuid())
	}
	target := fmt.Sprintf("gui/%s/%s", uid, label)

	// Prefer bootout; fall back to unload for older launchd.
	out, err := exec.Command("launchctl", "bootout", target).CombinedOutput()
	if err == nil {
		return fmt.Sprintf("已停止 LaunchAgent：%s", label), nil
	}
	msg := strings.TrimSpace(string(out))
	if strings.Contains(msg, "No such process") || strings.Contains(msg, "Could not find service") || strings.Contains(msg, "not found") {
		return fmt.Sprintf("LaunchAgent %s 未在运行（或已停止）", label), nil
	}

	home, _ := os.UserHomeDir()
	plist := fmt.Sprintf("%s/Library/LaunchAgents/%s.plist", home, label)
	if _, statErr := os.Stat(plist); statErr == nil {
		out2, err2 := exec.Command("launchctl", "unload", plist).CombinedOutput()
		if err2 == nil {
			return fmt.Sprintf("已 unload LaunchAgent：%s", plist), nil
		}
		return "", fmt.Errorf("bootout failed: %s; unload failed: %s (%v)", msg, strings.TrimSpace(string(out2)), err2)
	}
	return "", fmt.Errorf("停止 LaunchAgent 失败：%s (%v)", msg, err)
}
