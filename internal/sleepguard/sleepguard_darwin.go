//go:build darwin

package sleepguard

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
)

const darwinCaffeinate = "/usr/bin/caffeinate"

type darwinManager struct {
	mu  sync.Mutex
	cmd *exec.Cmd
}

// New starts no process. caffeinate is a system binary present on macOS; the
// -w argument ties its lifetime to this App, so an unexpected App crash cannot
// leave an orphaned sleep blocker behind.
func New() Manager {
	return &darwinManager{}
}

func (m *darwinManager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked()
}

func (m *darwinManager) statusLocked() Status {
	active := m.cmd != nil && m.cmd.ProcessState == nil
	message := "未开启：系统按原有设置休眠"
	if active {
		message = "已阻止系统因空闲休眠；显示器仍可关闭"
	}
	return Status{
		Available: true,
		Active:    active,
		Platform:  "macos",
		Message:   message,
	}
}

func (m *darwinManager) Set(enabled bool) (Status, error) {
	if !enabled {
		if err := m.stop(); err != nil {
			return m.Status(), err
		}
		return m.Status(), nil
	}

	m.mu.Lock()
	if m.cmd != nil && m.cmd.ProcessState == nil {
		status := m.statusLocked()
		m.mu.Unlock()
		return status, nil
	}
	cmd := exec.Command(darwinCaffeinate, "-i", "-w", strconv.Itoa(os.Getpid()))
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		status := m.statusLocked()
		m.mu.Unlock()
		return status, err
	}
	m.cmd = cmd
	m.mu.Unlock()

	go func() {
		_ = cmd.Wait()
		m.mu.Lock()
		if m.cmd == cmd {
			m.cmd = nil
		}
		m.mu.Unlock()
	}()

	return m.Status(), nil
}

func (m *darwinManager) stop() error {
	m.mu.Lock()
	cmd := m.cmd
	m.cmd = nil
	m.mu.Unlock()
	if cmd == nil || cmd.Process == nil || cmd.ProcessState != nil {
		return nil
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func (m *darwinManager) Close() error {
	return m.stop()
}
