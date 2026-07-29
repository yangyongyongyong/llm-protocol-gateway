//go:build darwin

package gateway

func readHostFans() []HostFanSpeed {
	return darwinReadFans()
}
