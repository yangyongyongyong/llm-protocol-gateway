//go:build darwin

package autostart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/launchagent"
)

func withTestApp(t *testing.T) (home, exe, appPath string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, "home")
	appPath = filepath.Join(root, "Applications", "Autostart Package Test.app")
	exe = filepath.Join(appPath, "Contents", "MacOS", "Autostart Package Test")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldHome, oldConfig, oldExe, oldApps, oldRun, oldStatus := userHomeDir, userConfigDir, appExecutable, applicationsDir, runCommand, legacyLaunchAgentStatus
	userHomeDir = func() (string, error) { return home, nil }
	userConfigDir = func() (string, error) { return filepath.Join(root, "Library", "Application Support"), nil }
	appExecutable = func() (string, error) { return exe, nil }
	applicationsDir = filepath.Join(root, "Applications")
	runCommand = func(string, ...string) ([]byte, error) { return nil, nil }
	legacyLaunchAgentStatus = func(string) int { return smUnknown }
	t.Cleanup(func() {
		userHomeDir, userConfigDir, appExecutable, applicationsDir, runCommand, legacyLaunchAgentStatus = oldHome, oldConfig, oldExe, oldApps, oldRun, oldStatus
	})
	return home, exe, appPath
}

func TestDarwinStatusReadsTheLaunchAgentRecord(t *testing.T) {
	home, exe, appPath := withTestApp(t)
	m := newManager()

	st := m.Status()
	if !st.Available || st.Enabled {
		t.Fatalf("fresh status = %+v, want available + off", st)
	}
	wantApp, _ := filepath.EvalSymlinks(appPath)
	if st.AppPath != wantApp {
		t.Fatalf("AppPath = %q, want %q", st.AppPath, wantApp)
	}

	st, err := m.Set(true)
	if err != nil {
		t.Fatalf("Set(true): %v", err)
	}
	if !st.Enabled || st.NeedsApproval || st.Mechanism != "launchAgent" {
		t.Fatalf("enabled status = %+v", st)
	}
	path := filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	plist, err := launchagent.Parse(raw)
	if err != nil {
		t.Fatalf("parse plist: %v", err)
	}
	wantExe, _ := filepath.EvalSymlinks(exe)
	if plist.Label != launchAgentLabel || len(plist.ProgramArguments) != 1 || plist.ProgramArguments[0] != wantExe {
		t.Fatalf("plist = %+v", plist)
	}
	if !strings.Contains(string(raw), "<key>RunAtLoad</key><true/>") {
		t.Fatalf("plist is missing RunAtLoad:\n%s", raw)
	}
	if !strings.Contains(string(raw), "<key>AbandonProcessGroup</key><true/>") {
		t.Fatalf("plist is missing AbandonProcessGroup:\n%s", raw)
	}

	legacyLaunchAgentStatus = func(string) int { return smRequiresApproval }
	if st = m.Status(); !st.Enabled || !st.NeedsApproval {
		t.Fatalf("held-back status = %+v, want enabled + needsApproval", st)
	}

	st, err = m.Set(false)
	if err != nil {
		t.Fatalf("Set(false): %v", err)
	}
	if st.Enabled {
		t.Fatalf("disabled status = %+v", st)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("plist still exists after disable: %v", err)
	}
}

func TestDarwinRefusesACommandLineBinary(t *testing.T) {
	oldExe := appExecutable
	appExecutable = func() (string, error) { return "/tmp/llm-protocol-gateway", nil }
	t.Cleanup(func() { appExecutable = oldExe })

	st := newManager().Status()
	if st.Available {
		t.Fatalf("command-line binary must not expose app autostart: %+v", st)
	}
	if !strings.Contains(st.Message, ".app") {
		t.Fatalf("message should explain the .app requirement: %q", st.Message)
	}
}

func TestRecordTargetPrefersTheInstalledCopy(t *testing.T) {
	_, devExe, devApp := withTestApp(t)
	installedApp := filepath.Join(applicationsDir, filepath.Base(devApp))
	installedExe := filepath.Join(installedApp, "Contents", "MacOS", filepath.Base(devExe))
	if err := os.MkdirAll(filepath.Dir(installedExe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installedExe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	info := []byte(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.wails.llm-protocol-gateway</string></dict></plist>`)
	if err := os.WriteFile(filepath.Join(installedApp, "Contents", "Info.plist"), info, 0o644); err != nil {
		t.Fatal(err)
	}
	appExecutable = func() (string, error) { return devExe, nil }

	exe, app, err := recordTarget()
	if err != nil {
		t.Fatalf("recordTarget: %v", err)
	}
	wantExe, _ := filepath.EvalSymlinks(installedExe)
	wantApp, _ := filepath.EvalSymlinks(installedApp)
	if exe != wantExe || app != wantApp {
		t.Fatalf("recordTarget = (%q, %q), want installed copy (%q, %q)", exe, app, wantExe, wantApp)
	}
}

func TestEnsureDefaultTurnsOnOnceAndRespectsLaterOff(t *testing.T) {
	home, _, _ := withTestApp(t)
	m := newManager()

	if err := m.EnsureDefault(); err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	if st := m.Status(); !st.Enabled {
		t.Fatalf("first install default = %+v, want enabled", st)
	}
	marker := defaultMarkerPath()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("default marker missing: %v", err)
	}
	agent := filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
	if _, err := os.Stat(agent); err != nil {
		t.Fatalf("default did not create LaunchAgent: %v", err)
	}

	if _, err := m.Set(false); err != nil {
		t.Fatalf("turn off: %v", err)
	}
	if err := m.EnsureDefault(); err != nil {
		t.Fatalf("EnsureDefault after off: %v", err)
	}
	if st := m.Status(); st.Enabled {
		t.Fatalf("a later user choice was overridden: %+v", st)
	}
	if _, err := os.Stat(agent); !os.IsNotExist(err) {
		t.Fatalf("LaunchAgent came back after user turned it off: %v", err)
	}
}

func TestEnsureDefaultWaitsUntilTheAppIsInstalled(t *testing.T) {
	root := t.TempDir()
	appPath := filepath.Join(root, "Downloads", "Autostart Package Test.app")
	exe := filepath.Join(appPath, "Contents", "MacOS", "Autostart Package Test")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldHome, oldConfig, oldExe, oldApps := userHomeDir, userConfigDir, appExecutable, applicationsDir
	userHomeDir = func() (string, error) { return filepath.Join(root, "home"), nil }
	userConfigDir = func() (string, error) { return filepath.Join(root, "config"), nil }
	appExecutable = func() (string, error) { return exe, nil }
	applicationsDir = filepath.Join(root, "Applications")
	t.Cleanup(func() {
		userHomeDir, userConfigDir, appExecutable, applicationsDir = oldHome, oldConfig, oldExe, oldApps
	})

	if err := newManager().EnsureDefault(); err != nil {
		t.Fatalf("EnsureDefault from Downloads: %v", err)
	}
	if _, err := os.Stat(defaultMarkerPath()); !os.IsNotExist(err) {
		t.Fatalf("default marker should wait for an installed copy: %v", err)
	}
}
