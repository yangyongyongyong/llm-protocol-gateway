//go:build darwin

package gateway

import "testing"

func TestPickCPUTempFromSensorsPrefersCPUCluster(t *testing.T) {
	v, src, ok := pickCPUTempFromSensors([]tempSensorReading{
		{"NAND CH0 temp", 39},
		{"pACC MTR Temp Sensor0", 60},
		{"eACC MTR Temp Sensor0", 50},
		{"PMU tdie1", 90},
	})
	if !ok || src != "iokit/cpu-cluster" || v != 55 {
		t.Fatalf("got %v %q ok=%v", v, src, ok)
	}
}

func TestPickCPUTempFromSensorsFallsBackToDie(t *testing.T) {
	v, src, ok := pickCPUTempFromSensors([]tempSensorReading{
		{"NAND CH0 temp", 39},
		{"PMU tdie1", 50.0},
		{"PMU tdie2", 51.0},
		{"PMU2 tdie1", 0}, // unpowered sensor must be ignored
	})
	if !ok || src != "iokit/soc-die" || v != 50.5 {
		t.Fatalf("got %v %q ok=%v", v, src, ok)
	}
}

func TestPickCPUTempFromSensorsEmpty(t *testing.T) {
	if _, _, ok := pickCPUTempFromSensors(nil); ok {
		t.Fatal("expected no reading")
	}
}

func TestDarwinLiveSensorsReport(t *testing.T) {
	readings := darwinReadTempSensors()
	if len(readings) == 0 {
		t.Skip("no IOKit temperature sensors on this host")
	}
	v, src, ok := pickCPUTempFromSensors(readings)
	if !ok {
		t.Fatalf("sensors present (%d) but no CPU temp picked", len(readings))
	}
	t.Logf("live cpu temp = %.1f°C (%s, %d sensors)", v, src, len(readings))
}
