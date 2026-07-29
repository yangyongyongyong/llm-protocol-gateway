//go:build darwin && !cgo

package gateway

// darwinReadTempSensors needs cgo (IOKit); without it we fall back to
// powermetrics / thermal pressure.
func darwinReadTempSensors() []tempSensorReading { return nil }

func darwinReadMemory() (total, used uint64, ok bool) { return 0, 0, false }

func darwinReadInterfaceCounters() []interfaceCounter { return nil }

func darwinReadFans() []HostFanSpeed { return nil }
