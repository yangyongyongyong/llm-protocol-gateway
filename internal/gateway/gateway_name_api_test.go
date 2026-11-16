package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestGatewayNameIsPersistedAndSentToParent(t *testing.T) {
	server, store := newAuthTestServer(t)
	server.stateSaver = namingStateSaver{store: store}
	server.SetGatewayIdentity(domain.GatewayIdentity{ID: gwID, Name: "旧名称", Source: "machine"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/__gateway-name", strings.NewReader(`{"name":"本机 Mac mini"}`))
	req.Host = "127.0.0.1:18093"
	req.RemoteAddr = "127.0.0.1:4321"
	server.handleSetGatewayName(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var status gatewayNameStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Name != "本机 Mac mini" || server.GatewayName() != status.Name {
		t.Fatalf("name=%q server=%q", status.Name, server.GatewayName())
	}
	if got := store.Setting(settingGatewayName); got != status.Name {
		t.Fatalf("persisted name=%q", got)
	}
}

type namingStateSaver struct{ store AdminAuthStore }

func (n namingStateSaver) Save(domain.GatewayState) error { return nil }
