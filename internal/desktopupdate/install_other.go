//go:build !darwin

package desktopupdate

import (
	"context"
	"errors"
	"time"
)

func stageDownload(context.Context, string, string, string) (string, error) {
	return "", errors.New("当前平台暂不支持自动安装桌面 App 更新")
}

func installBundle(string, string) (string, error) {
	return "", errors.New("当前平台暂不支持自动安装桌面 App 更新")
}

// Relaunch is a no-op outside the macOS desktop shell.
func Relaunch(string) error {
	return errors.New("当前平台暂不支持自动重启桌面 App")
}

func launchRollbackWatchdog(context.Context, rollbackArtifacts, rollbackJournal, int, time.Duration) error {
	return errors.New("当前平台暂不支持自动回退桌面 App")
}

func restoreRollbackBundle(string, string) error {
	return errors.New("当前平台暂不支持自动回退桌面 App")
}
