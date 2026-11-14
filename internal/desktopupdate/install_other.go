//go:build !darwin

package desktopupdate

import (
	"context"
	"errors"
)

func stageDownload(context.Context, string, string, string) (string, error) {
	return "", errors.New("当前平台暂不支持自动安装桌面 App 更新")
}

func installBundle(string, string) error {
	return errors.New("当前平台暂不支持自动安装桌面 App 更新")
}

func cleanupOldBundles(string) {}

// Relaunch is a no-op outside the macOS desktop shell.
func Relaunch(string) error {
	return errors.New("当前平台暂不支持自动重启桌面 App")
}
