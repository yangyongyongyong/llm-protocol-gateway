package store

import (
	"path/filepath"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestPreventSleepRoundTripAndDefaultsOff(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "gateway.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	loaded, err := s.Load(domain.GatewayState{})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PreventSleep {
		t.Fatal("new stores must default sleep prevention off")
	}

	if err := s.Save(domain.GatewayState{PreventSleep: true}); err != nil {
		t.Fatal(err)
	}
	loaded, err = s.Load(domain.GatewayState{})
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.PreventSleep {
		t.Fatal("preventSleep did not survive a restart")
	}

	if err := s.Save(domain.GatewayState{PreventSleep: false}); err != nil {
		t.Fatal(err)
	}
	loaded, err = s.Load(domain.GatewayState{})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PreventSleep {
		t.Fatal("preventSleep stayed on after being saved off")
	}
}
