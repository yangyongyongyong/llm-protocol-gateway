package netutil

import (
	"net"
	"os"
	"strings"
)

// PrimaryLANIPv4 returns the best-effort private IPv4 address for LAN clients.
// Order: GATEWAY_LAN_HOST env override, then the first non-loopback private IPv4
// on an up interface. Empty string means no usable address was found.
func PrimaryLANIPv4() string {
	if override := strings.TrimSpace(os.Getenv("GATEWAY_LAN_HOST")); override != "" {
		return override
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var fallback string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip := ipFromAddr(addr)
			if ip == nil || ip.IsLoopback() || ip.To4() == nil {
				continue
			}
			v4 := ip.To4().String()
			if isPrivateIPv4(ip) {
				return v4
			}
			if fallback == "" {
				fallback = v4
			}
		}
	}
	return fallback
}

func ipFromAddr(addr net.Addr) net.IP {
	switch v := addr.(type) {
	case *net.IPNet:
		return v.IP
	case *net.IPAddr:
		return v.IP
	default:
		return nil
	}
}

func isPrivateIPv4(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	// 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16
	if v4[0] == 10 {
		return true
	}
	if v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31 {
		return true
	}
	if v4[0] == 192 && v4[1] == 168 {
		return true
	}
	return false
}
