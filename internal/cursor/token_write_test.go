package cursor

import (
	"os"
	"path/filepath"
	"testing"
)

// writeToken runs on every Cursor-routed request; an unchanged token must not
// rewrite the file (that was one disk write per request).
func TestWriteTokenSkipsUnchangedValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access-token")
	b := &Bridge{tokenFile: path}

	if err := b.writeToken("tok-abc"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	first := info.ModTime()

	// Same value (and a whitespace-padded variant) must be a no-op.
	for _, same := range []string{"tok-abc", "  tok-abc\n"} {
		if err := b.writeToken(same); err != nil {
			t.Fatal(err)
		}
		info, err = os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(first) {
			t.Fatalf("unchanged token rewrote the file (mtime moved) for %q", same)
		}
	}

	// A real change must still be written.
	if err := b.writeToken("tok-xyz"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "tok-xyz" {
		t.Fatalf("changed token not persisted, got %q", string(data))
	}
}
