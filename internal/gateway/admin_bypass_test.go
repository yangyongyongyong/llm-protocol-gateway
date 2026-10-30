package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// VPS 上网关由 Caddy 反代，对端永远是 127.0.0.1。只认对端地址的免登录判断会把
// 每个域名访问者都当成本机操作员，控制台（含全部 Provider 凭证）就此公开。
func TestLocalAdminBypassRejectsProxiedDomains(t *testing.T) {
	for _, tc := range []struct {
		name string
		host string
		want bool
	}{
		{name: "loopback host directly", host: "127.0.0.1:18093", want: true},
		{name: "localhost directly", host: "localhost:18093", want: true},
		{name: "empty host is the desktop app case", host: "", want: true},
		{name: "public domain through the reverse proxy", host: "llm.lucadesign.uk"},
		{name: "public IP through the reverse proxy", host: "198.51.100.10:18093"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/__auth/status", nil)
			request.Host = tc.host
			request.RemoteAddr = "127.0.0.1:51234"
			if got := isLocalAdminBypass(request); got != tc.want {
				t.Fatalf("isLocalAdminBypass(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}
