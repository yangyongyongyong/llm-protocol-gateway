//go:build windows

package sleepguard

import (
	"errors"
	"fmt"
	"runtime"
	"sync"

	"golang.org/x/sys/windows"
)

const (
	esSystemRequired = 0x00000001
	esContinuous     = 0x80000000
)

var (
	kernel32                    = windows.NewLazySystemDLL("kernel32.dll")
	procSetThreadExecutionState = kernel32.NewProc("SetThreadExecutionState")
)

type windowsManager struct {
	mu     sync.Mutex
	active bool
	stop   chan struct{}
	done   chan error
}

func New() Manager {
	return &windowsManager{}
}

func (m *windowsManager) Status() Status {
	m.mu.Lock()
	active := m.active
	m.mu.Unlock()
	message := "未开启：系统按原有设置休眠"
	if active {
		message = "已阻止系统因空闲休眠；显示器仍可关闭"
	}
	return Status{
		Available: true,
		Active:    active,
		Platform:  "windows",
		Message:   message,
	}
}

func (m *windowsManager) Set(enabled bool) (Status, error) {
	if !enabled {
		return m.disable()
	}
	return m.enable()
}

func (m *windowsManager) enable() (Status, error) {
	m.mu.Lock()
	if m.active {
		status := m.statusLocked()
		m.mu.Unlock()
		return status, nil
	}
	stop := make(chan struct{})
	done := make(chan error, 1)
	ready := make(chan error, 1)
	m.active = true
	m.stop = stop
	m.done = done
	m.mu.Unlock()

	go func() {
		// SetThreadExecutionState is a per-thread flag. Keep this goroutine on
		// its OS thread for the full assertion lifetime so disable is called on
		// the same thread that enabled it.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := setThreadExecutionState(true); err != nil {
			ready <- err
			done <- err
			return
		}
		ready <- nil
		<-stop
		done <- setThreadExecutionState(false)
	}()

	if err := <-ready; err != nil {
		m.mu.Lock()
		if m.stop == stop {
			m.active = false
			m.stop = nil
			m.done = nil
		}
		m.mu.Unlock()
		return m.Status(), err
	}
	return m.Status(), nil
}

func (m *windowsManager) disable() (Status, error) {
	m.mu.Lock()
	if !m.active || m.stop == nil || m.done == nil {
		status := m.statusLocked()
		m.mu.Unlock()
		return status, nil
	}
	stop := m.stop
	done := m.done
	m.active = false
	m.stop = nil
	m.done = nil
	m.mu.Unlock()

	close(stop)
	if err := <-done; err != nil {
		return m.Status(), err
	}
	return m.Status(), nil
}

func (m *windowsManager) statusLocked() Status {
	message := "未开启：系统按原有设置休眠"
	if m.active {
		message = "已阻止系统因空闲休眠；显示器仍可关闭"
	}
	return Status{
		Available: true,
		Active:    m.active,
		Platform:  "windows",
		Message:   message,
	}
}

func (m *windowsManager) Close() error {
	_, err := m.disable()
	return err
}

func setThreadExecutionState(enabled bool) error {
	flags := uintptr(esContinuous)
	if enabled {
		flags |= esSystemRequired
	}
	r1, _, err := procSetThreadExecutionState.Call(flags)
	if r1 == 0 {
		if err != nil && !errors.Is(err, windows.ERROR_SUCCESS) {
			return fmt.Errorf("SetThreadExecutionState: %w", err)
		}
		return fmt.Errorf("SetThreadExecutionState failed")
	}
	return nil
}
