package gateway

import (
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestClassifyAccessSource(t *testing.T) {
	cases := []struct {
		host   string
		public string
		want   string
	}{
		{"127.0.0.1", "", monitor.AccessSourceLocal},
		{"localhost", "", monitor.AccessSourceLocal},
		{"192.168.1.20", "", monitor.AccessSourceLAN},
		{"gateway.lucadesign.uk", "https://gateway.lucadesign.uk", monitor.AccessSourcePublic},
		{"abc.trycloudflare.com", "", monitor.AccessSourcePublic},
	}
	for _, tc := range cases {
		if got := classifyAccessSource(tc.host, tc.public); got != tc.want {
			t.Fatalf("host=%q public=%q got=%q want=%q", tc.host, tc.public, got, tc.want)
		}
	}
}
