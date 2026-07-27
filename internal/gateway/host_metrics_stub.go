//go:build !darwin && !linux

package gateway

import (
	"runtime"
	"time"
)

func collectHostMetrics(prev any) hostMetricsCollectResult {
	_ = prev
	m := HostMetrics{
		CPUCount:    runtime.NumCPU(),
		CollectedAt: time.Now(),
	}
	m.applyDiskRoot()
	m.applyProcess()
	return hostMetricsCollectResult{metrics: m, prev: nil}
}

// statfsRoot is unsupported outside darwin/linux.
func statfsRoot() (total, used uint64, ok bool) { return 0, 0, false }
