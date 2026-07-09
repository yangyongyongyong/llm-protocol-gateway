//go:build !darwin

package main

import "fmt"

func stopLaunchAgent(label string) (string, error) {
	return "", fmt.Errorf("LaunchAgent 仅支持 macOS（label=%s）", label)
}
