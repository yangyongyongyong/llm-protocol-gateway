//go:build darwin || linux

package gateway

import "testing"

func TestParseSmartctlDiskTempNVMe(t *testing.T) {
	in := []byte(`smartctl 7.3
=== START OF INFORMATION SECTION ===
Model Number:                       Samsung SSD 990 PRO 1TB
Serial Number:                      ABC

=== START OF SMART DATA SECTION ===
Temperature:                        47 Celsius
Temperature Sensor 1:               47 Celsius
Temperature Sensor 2:               50 Celsius
`)
	model, temp, source, ok := parseSmartctlDiskTemp(in)
	if !ok {
		t.Fatal("expected ok")
	}
	if model != "Samsung SSD 990 PRO 1TB" {
		t.Fatalf("model=%q", model)
	}
	if temp != 47 {
		t.Fatalf("temp=%v", temp)
	}
	if source != "smartctl/nvme" {
		t.Fatalf("source=%q", source)
	}
}

func TestParseSmartctlDiskTempATA(t *testing.T) {
	in := []byte(`Device Model:     ST1000DM003
194 Temperature_Celsius     0x0022   110   094   000    Old_age   Always       -       35
`)
	model, temp, source, ok := parseSmartctlDiskTemp(in)
	if !ok {
		t.Fatal("expected ok")
	}
	if model != "ST1000DM003" {
		t.Fatalf("model=%q", model)
	}
	if temp != 35 {
		t.Fatalf("temp=%v", temp)
	}
	if source != "smartctl/ata" {
		t.Fatalf("source=%q", source)
	}
}

func TestParseDarwinPhysicalDiskList(t *testing.T) {
	in := `/dev/disk0 (internal, physical):
   #:                       TYPE NAME
/dev/disk4 (external, physical):
   #:                       TYPE NAME
`
	matches := reDarwinPhysDisk.FindAllStringSubmatch(in, -1)
	if len(matches) != 2 {
		t.Fatalf("matches=%d", len(matches))
	}
	if matches[0][1] != "/dev/disk0" || matches[0][2] != "internal" {
		t.Fatalf("disk0=%v", matches[0])
	}
	if matches[1][1] != "/dev/disk4" || matches[1][2] != "external" {
		t.Fatalf("disk4=%v", matches[1])
	}
}

func TestIsLinuxPartitionName(t *testing.T) {
	cases := map[string]bool{
		"sda":       false,
		"sda1":      true,
		"nvme0n1":   false,
		"nvme0n1p1": true,
		"vda":       false,
		"vda2":      true,
	}
	for name, want := range cases {
		if got := isLinuxPartitionName(name); got != want {
			t.Fatalf("%s: got %v want %v", name, got, want)
		}
	}
}

func TestCollectDiskTempsLive(t *testing.T) {
	temps := collectDiskTemps()
	if len(temps) == 0 {
		t.Skip("no disk temps (smartctl missing or denied)")
	}
	for _, d := range temps {
		t.Logf("%s model=%q temp=%.1fC internal=%v source=%s", d.Device, d.Model, d.TempC, d.Internal, d.Source)
		if d.TempC <= 0 || d.TempC >= 120 {
			t.Fatalf("implausible temp: %+v", d)
		}
	}
}
