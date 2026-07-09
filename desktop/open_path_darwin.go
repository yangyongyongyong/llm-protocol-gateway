//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func openPathInFinder(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("empty path")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	out, err := exec.Command("open", path).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}
