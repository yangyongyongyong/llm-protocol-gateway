package netutil

import (
	"net"
	"testing"
)

func TestIsPrivateIPv4(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"10.0.0.1", true},
		{"172.16.5.1", true},
		{"172.31.255.255", true},
		{"172.32.0.1", false},
		{"192.168.1.10", true},
		{"8.8.8.8", false},
		{"127.0.0.1", false},
	}
	for _, tc := range cases {
		got := isPrivateIPv4(net.ParseIP(tc.ip))
		if got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.ip, got, tc.want)
		}
	}
}
