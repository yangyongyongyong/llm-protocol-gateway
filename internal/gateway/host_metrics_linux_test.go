//go:build linux

package gateway

import "testing"

func TestParseProcMeminfo(t *testing.T) {
	in := "MemTotal:       16384000 kB\nMemFree:         1000000 kB\nMemAvailable:    8192000 kB\nSwapTotal:       2048000 kB\nSwapFree:        1024000 kB\n"
	total, used, ok := parseProcMeminfo(in)
	if !ok || total != 16384000*1024 || used != (16384000-8192000)*1024 {
		t.Fatalf("total=%d used=%d ok=%v", total, used, ok)
	}
	st, su := parseProcSwap(in)
	if st != 2048000*1024 || su != 1024000*1024 {
		t.Fatalf("swap total=%d used=%d", st, su)
	}
}

func TestParseProcNetDevAndLoadavg(t *testing.T) {
	in := "Inter-|   Receive                                                |  Transmit\n" +
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n" +
		"    lo:  100 1 0 0 0 0 0 0  100 1 0 0 0 0 0 0\n" +
		"  eth0: 2000 5 0 0 0 0 0 0 1000 4 0 0 0 0 0 0\n"
	list := parseProcNetDev(in)
	if len(list) != 2 {
		t.Fatalf("got %+v", list)
	}
	sum := sumInterfaceCounters(list)
	if !sum.ok || sum.rx != 2000 || sum.tx != 1000 || sum.interfaces != 1 {
		t.Fatalf("sum=%+v", sum)
	}
	l1, l5, l15 := parseProcLoadavg("0.52 0.31 0.24 1/512 4242\n")
	if l1 != 0.52 || l5 != 0.31 || l15 != 0.24 {
		t.Fatalf("load=%v %v %v", l1, l5, l15)
	}
}
