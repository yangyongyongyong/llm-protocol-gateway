//go:build darwin || linux

package gateway

func applyFanSpeeds(m *HostMetrics) {
	if m == nil {
		return
	}
	fans := readHostFans()
	if len(fans) > 0 {
		m.Fans = fans
	}
}
