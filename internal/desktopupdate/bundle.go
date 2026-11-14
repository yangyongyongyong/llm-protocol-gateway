package desktopupdate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// findBundle locates the .app that contains this process on macOS. A terminal
// binary has no bundle and therefore exposes no self-install capability.
func findBundle() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, resolveErr := filepath.EvalSymlinks(exe); resolveErr == nil {
		exe = resolved
	}
	exe = filepath.Clean(exe)
	for dir := filepath.Dir(exe); dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		if strings.HasSuffix(dir, ".app") {
			return dir
		}
	}
	return ""
}
