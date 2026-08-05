package gateway

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Each selfcheck job dir holds a full per-case client home (100+ MiB in
// practice); without pruning the cache grew to ~800 MiB. Keep the newest few,
// never touch the running job's dir.
func TestPruneSelfcheckJobDirs(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "selfcheck")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}

	// Oldest → newest, plus one unrelated dir that must survive.
	names := []string{"sc-oldest", "sc-older", "sc-newer", "sc-newest", "sc-running"}
	base := time.Now().Add(-24 * time.Hour)
	for i, name := range names {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "payload"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := base.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(dir, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	unrelated := filepath.Join(root, "not-a-job")
	if err := os.MkdirAll(unrelated, 0o700); err != nil {
		t.Fatal(err)
	}

	removed, freed := pruneSelfcheckJobDirsIn(root, "sc-running")
	if removed != 2 {
		t.Fatalf("removed %d dirs, want 2 (oldest two)", removed)
	}
	if freed <= 0 {
		t.Fatalf("freed bytes = %d, want > 0", freed)
	}

	mustExist := []string{"sc-newest", "sc-newer", "sc-running", "not-a-job"}
	for _, name := range mustExist {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("%s should have been kept: %v", name, err)
		}
	}
	for _, name := range []string{"sc-oldest", "sc-older"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("%s should have been pruned (err=%v)", name, err)
		}
	}

	// Idempotent: a second sweep has nothing left to do.
	if removed, _ := pruneSelfcheckJobDirsIn(root, "sc-running"); removed != 0 {
		t.Fatalf("second prune removed %d dirs, want 0", removed)
	}
}

// A missing cache root must be a silent no-op, not an error path.
func TestPruneSelfcheckJobDirsMissingRoot(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if removed, freed := pruneSelfcheckJobDirsIn(missing, ""); removed != 0 || freed != 0 {
		t.Fatalf("got (%d, %d), want (0, 0)", removed, freed)
	}
}
