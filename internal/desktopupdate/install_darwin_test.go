//go:build darwin

package desktopupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateAppRejectsReleaseMetadataMismatch(t *testing.T) {
	app := filepath.Join(t.TempDir(), "LLM Protocol Gateway.app")
	contents := filepath.Join(app, "Contents")
	if err := os.MkdirAll(filepath.Join(contents, "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contents, "MacOS", "LLM Protocol Gateway"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	info := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>com.wails.llm-protocol-gateway</string>
<key>CFBundleShortVersionString</key><string>0.1.3</string>
<key>CFBundleExecutable</key><string>LLM Protocol Gateway</string>
</dict></plist>`
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(info), 0o644); err != nil {
		t.Fatal(err)
	}

	err := validateApp(app, "1.0.20")
	if err == nil {
		t.Fatal("version mismatch must reject the update")
	}
	msg := err.Error()
	if !strings.Contains(msg, "发布清单为 1.0.20") || !strings.Contains(msg, "Info.plist 为 0.1.3") || !strings.Contains(msg, "发布修复版") {
		t.Fatalf("error should tell the operator which metadata is broken: %q", msg)
	}
}

func TestRollbackWatchdogScriptIsValidShell(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-n")
	cmd.Stdin = strings.NewReader(rollbackWatchdogScript)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rollback watchdog shell syntax: %v: %s", err, out)
	}
}

func TestRollbackWatchdogRestoresPreviousBundle(t *testing.T) {
	root := t.TempDir()
	newBundle := filepath.Join(root, "LLM Protocol Gateway.app")
	oldBundle := filepath.Join(root, ".previous.app")
	if err := os.MkdirAll(filepath.Join(newBundle, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(oldBundle, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldBundle, "Contents", "previous-marker"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifacts := rollbackArtifactsFor(filepath.Join(root, "settings.json"))
	journal := rollbackJournal{
		BundlePath:      newBundle,
		BackupPath:      oldBundle,
		PreviousVersion: "1.0.0",
		NewVersion:      "1.1.0",
		StartedAt:       time.Now().UTC().Format(time.RFC3339),
		Deadline:        time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
	}
	if err := saveRollbackJournal(artifacts.Journal, journal); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	openedPath := filepath.Join(root, "opened.txt")
	openScript := "#!/bin/sh\nprintf '%s\\n' \"$2\" >\"$OPEN_PATH\"\n"
	pgrepScript := "#!/bin/sh\nexit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "open"), []byte(openScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "pgrep"), []byte(pgrepScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("OPEN_PATH", openedPath)

	cmd := exec.Command(
		"/bin/sh", "-c", rollbackWatchdogScript, "wt-rollback",
		newBundle, oldBundle, "1.0.0", "1.1.0",
		artifacts.Commit, artifacts.Request, artifacts.PID, artifacts.History, artifacts.Journal, artifacts.WatchdogLog,
		"0", "2",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("watchdog failed: %v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(newBundle, "Contents", "previous-marker")); err != nil {
		t.Fatalf("previous bundle was not restored: %v", err)
	}
	history, err := os.ReadFile(artifacts.History)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(history), "1.1.0\t1.0.0\t") {
		t.Fatalf("history = %q", history)
	}
	opened, err := os.ReadFile(openedPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(opened)) != newBundle {
		t.Fatalf("relaunched %q, want %q", opened, newBundle)
	}
}
