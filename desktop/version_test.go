package main

import "testing"

func TestAppVersionStripsReleasePrefix(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })
	version = "v1.2.3"
	if got := appVersion(); got != "1.2.3" {
		t.Fatalf("appVersion() = %q, want 1.2.3", got)
	}
}
