//go:build !darwin

package autostart

import "fmt"

type unavailableManager struct{}

func newManager() Manager { return unavailableManager{} }

func (unavailableManager) Status() Status {
	return Status{
		Available: false,
		Message:   "开机自动启动仅支持 macOS App",
	}
}

func (unavailableManager) Set(bool) (Status, error) {
	return unavailableManager{}.Status(), fmt.Errorf("开机自动启动仅支持 macOS App")
}

func (unavailableManager) OpenLoginItems() error {
	return fmt.Errorf("登录项设置仅支持 macOS")
}

func (unavailableManager) EnsureDefault() error { return nil }
