//go:build !darwin && !windows

package sleepguard

import (
	"errors"
	"runtime"
)

type unavailableManager struct {
	platform string
}

// New returns an explicit unsupported manager rather than silently pretending
// a Linux/other host is protected.
func New() Manager {
	return unavailableManager{platform: runtime.GOOS}
}

func (m unavailableManager) Status() Status {
	return Status{
		Available: false,
		Platform:  m.platform,
		Message:   "防止系统休眠目前仅支持 macOS 和 Windows",
	}
}

func (m unavailableManager) Set(enabled bool) (Status, error) {
	if !enabled {
		return m.Status(), nil
	}
	return m.Status(), errors.New("prevent system sleep is only supported on macOS and Windows")
}

func (m unavailableManager) Close() error {
	return nil
}
