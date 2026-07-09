package app_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/app"
)

func primaryLANIPv4() string {
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
				continue
			}
			return ipnet.IP.String()
		}
	}
	return ""
}

func canDial(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 400*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func TestSetWebExposedRebind(t *testing.T) {
	port := 19093
	if app.PortInUse(port) {
		t.Skipf("port %d in use", port)
	}
	tmp := t.TempDir()
	t.Setenv("GATEWAY_DB", tmp+"/gateway-test.db")
	t.Setenv("GATEWAY_ADDR", "") // ensure PreferEnvAddr path is unused
	rt := app.New()
	if err := rt.Start(app.Config{Port: port, WebExposed: false, WebExposedSet: true}); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})
	if err := rt.WaitHealthy(5 * time.Second); err != nil {
		t.Fatalf("health: %v", err)
	}

	loop := fmt.Sprintf("127.0.0.1:%d", port)
	if !canDial(loop) {
		t.Fatal("loopback should be reachable when webExposed=false")
	}

	lanIP := primaryLANIPv4()
	if lanIP != "" {
		lan := net.JoinHostPort(lanIP, fmt.Sprintf("%d", port))
		if canDial(lan) {
			t.Fatalf("LAN %s should NOT be reachable when webExposed=false", lan)
		}
	}

	if err := rt.SetWebExposed(true); err != nil {
		t.Fatalf("SetWebExposed(true): %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if !canDial(loop) {
		t.Fatal("loopback should stay reachable after open")
	}
	if lanIP != "" {
		lan := net.JoinHostPort(lanIP, fmt.Sprintf("%d", port))
		if !canDial(lan) {
			t.Fatalf("LAN %s should be reachable when webExposed=true", lan)
		}
	}

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/__health", port))
	if err != nil {
		t.Fatalf("health http: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status %d", resp.StatusCode)
	}

	if err := rt.SetWebExposed(false); err != nil {
		t.Fatalf("SetWebExposed(false): %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if lanIP != "" {
		lan := net.JoinHostPort(lanIP, fmt.Sprintf("%d", port))
		if canDial(lan) {
			t.Fatalf("LAN %s should be blocked again after close", lan)
		}
	}
}
