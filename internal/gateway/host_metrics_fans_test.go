//go:build darwin || linux

package gateway

import "testing"

func TestFanSpeedPercent(t *testing.T) {
	if got := fanSpeedPercent(1700, 1700, 4500); got != 0 {
		t.Fatalf("idle percent=%v", got)
	}
	if got := fanSpeedPercent(4500, 1700, 4500); got != 100 {
		t.Fatalf("max percent=%v", got)
	}
	got := fanSpeedPercent(3100, 1700, 4500)
	if got < 49 || got > 51 {
		t.Fatalf("mid percent=%v", got)
	}
}

func TestReadHostFansLive(t *testing.T) {
	fans := readHostFans()
	if len(fans) == 0 {
		t.Skip("no fans on this host")
	}
	for _, f := range fans {
		t.Logf("fan#%d %s rpm=%.0f min=%.0f max=%.0f percent=%.1f", f.ID, f.Name, f.RPM, f.MinRPM, f.MaxRPM, f.Percent)
		if f.RPM < 0 || f.RPM > 20000 {
			t.Fatalf("implausible rpm: %+v", f)
		}
	}
}
