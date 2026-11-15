package desktopupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// rollbackJournal survives the process handoff between the old and new App.
// The detached watchdog uses the paths recorded here even if the new build
// cannot start far enough to run Go code.
type rollbackJournal struct {
	BundlePath      string `json:"bundlePath"`
	BackupPath      string `json:"backupPath"`
	PreviousVersion string `json:"previousVersion"`
	NewVersion      string `json:"newVersion"`
	StartedAt       string `json:"startedAt"`
	Deadline        string `json:"deadline"`
}

type rollbackEvent struct {
	FromVersion string
	ToVersion   string
	At          string
	Reason      string
}

type rollbackArtifacts struct {
	Journal     string
	Commit      string
	Request     string
	PID         string
	History     string
	WatchdogLog string
}

func rollbackArtifactsFor(settingsPath string) rollbackArtifacts {
	dir := filepath.Dir(settingsPath)
	base := strings.TrimSuffix(filepath.Base(settingsPath), filepath.Ext(settingsPath))
	if strings.TrimSpace(base) == "" {
		base = "desktop-update"
	}
	journal := filepath.Join(dir, base+"-rollback.json")
	return rollbackArtifacts{
		Journal:     journal,
		Commit:      journal + ".healthy",
		Request:     journal + ".request",
		PID:         journal + ".pid",
		History:     filepath.Join(dir, base+"-rollback-history.tsv"),
		WatchdogLog: filepath.Join(dir, base+"-rollback-watchdog.log"),
	}
}

func (j rollbackJournal) deadline() (time.Time, error) {
	if strings.TrimSpace(j.Deadline) == "" {
		return time.Time{}, errors.New("回退日志缺少截止时间")
	}
	t, err := time.Parse(time.RFC3339, j.Deadline)
	if err != nil {
		return time.Time{}, fmt.Errorf("回退截止时间无效：%w", err)
	}
	return t, nil
}

func saveRollbackJournal(path string, journal rollbackJournal) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	f, err := os.CreateTemp(dir, ".desktop-update-rollback-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func loadRollbackJournal(path string) (*rollbackJournal, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var j rollbackJournal
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, err
	}
	if strings.TrimSpace(j.BundlePath) == "" || strings.TrimSpace(j.BackupPath) == "" {
		return nil, errors.New("回退日志缺少 App 路径")
	}
	if _, err := j.deadline(); err != nil {
		return nil, err
	}
	return &j, nil
}

func loadLatestRollbackEvent(path string) (*rollbackEvent, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		fields := strings.Split(lines[i], "\t")
		if len(fields) < 4 {
			continue
		}
		return &rollbackEvent{
			FromVersion: strings.TrimSpace(fields[0]),
			ToVersion:   strings.TrimSpace(fields[1]),
			At:          strings.TrimSpace(fields[2]),
			Reason:      strings.TrimSpace(strings.Join(fields[3:], "\t")),
		}, nil
	}
	return nil, nil
}

func writeFileAtomic(path string, body []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".desktop-update-marker-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (m *Manager) launchWatchdog(ctx context.Context, journal *rollbackJournal, oldPID int) error {
	if journal == nil {
		return errors.New("缺少回退日志")
	}
	deadline, err := journal.deadline()
	if err != nil {
		return err
	}
	remaining := time.Until(deadline)
	if remaining < 5*time.Second {
		remaining = 5 * time.Second
	}
	return launchRollbackWatchdogFn(ctx, m.rollbacks, *journal, oldPID, remaining)
}

var (
	rollbackMonitorStartDelay = 2 * time.Second
	rollbackMonitorInterval   = 5 * time.Second
	rollbackFailureThreshold  = 3
)

func (m *Manager) monitorRollback(ctx context.Context) {
	time.Sleep(rollbackMonitorStartDelay)
	ticker := time.NewTicker(rollbackMonitorInterval)
	defer ticker.Stop()
	failures := 0
	healthyChecks := 0
	for {
		m.mu.Lock()
		journal := m.rollback
		healthCheck := m.healthCheck
		m.mu.Unlock()
		if journal == nil {
			return
		}
		if _, err := os.Stat(m.rollbacks.Commit); err == nil {
			return
		}
		if _, err := os.Stat(m.rollbacks.Journal); errors.Is(err, os.ErrNotExist) {
			return
		}
		var healthErr error
		if healthCheck != nil {
			healthErr = healthCheck()
		} else {
			healthErr = errors.New("当前版本未提供健康检查")
		}
		if healthErr == nil {
			failures = 0
			healthyChecks++
		} else {
			failures++
		}
		if failures >= rollbackFailureThreshold {
			m.requestRollback("更新后健康检查连续失败：" + healthErr.Error())
			return
		}
		deadline, err := journal.deadline()
		if err != nil {
			m.requestRollback("更新回退截止时间无效：" + err.Error())
			return
		}
		if !time.Now().Before(deadline) {
			if healthyChecks > 0 && failures == 0 {
				m.commitRollback()
			} else {
				m.requestRollback("更新后健康观察期结束，但网关未通过健康检查")
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) commitRollback() {
	m.mu.Lock()
	journal := m.rollback
	m.mu.Unlock()
	if journal == nil {
		return
	}
	_ = writeFileAtomic(m.rollbacks.Commit, []byte("healthy\n"), 0o600)
	_ = os.RemoveAll(journal.BackupPath)
	_ = os.Remove(m.rollbacks.Journal)
	_ = os.Remove(m.rollbacks.Request)
	m.mu.Lock()
	m.rollback = nil
	if m.state == StateRollingBack {
		m.state = StateIdle
	}
	m.err = ""
	m.message = fmt.Sprintf("新版本 %s 已通过 %.0f 分钟健康观察，更新已确认", journal.NewVersion, time.Until(mustDeadline(journal)).Minutes())
	m.mu.Unlock()
}

func mustDeadline(j *rollbackJournal) time.Time {
	if j == nil {
		return time.Now()
	}
	t, _ := j.deadline()
	return t
}

func (m *Manager) requestRollback(reason string) {
	m.mu.Lock()
	journal := m.rollback
	if journal == nil || m.state == StateRollingBack {
		m.mu.Unlock()
		return
	}
	m.state = StateRollingBack
	m.message = "检测到更新异常，正在恢复上一版本"
	m.err = reason
	m.mu.Unlock()
	ctx := m.contextLocked()
	// The install path already started this process; the PID file makes this a
	// no-op when it is alive, and re-arms it if the watchdog died.
	_ = m.launchWatchdog(ctx, journal, 0)
	_ = writeFileAtomic(m.rollbacks.Request, []byte(reason+"\n"), 0o600)
}

// RollbackNow asks the detached watchdog to restore the previous bundle. It is
// intentionally local-only at the HTTP layer, just like install/update.
func (m *Manager) RollbackNow() bool {
	m.mu.Lock()
	ok := m.rollback != nil && m.state != StateRollingBack && m.state != StateRestarting
	m.mu.Unlock()
	if !ok {
		return false
	}
	m.requestRollback("用户手动回退")
	return true
}
