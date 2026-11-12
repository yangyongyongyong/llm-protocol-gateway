package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// disconnectedPeerServer builds a child gateway holding two upstreams: "up-1" with
// two member rows (one per protocol, as a registration leaves them) and "up-2"
// with one. The members share an UpstreamKey, which is what makes them one
// upstream on the 网关互联 page.
func disconnectedPeerServer(t *testing.T) *Server {
	t.Helper()
	server := newPeerTestServer(t,
		domain.Provider{
			ID: "peer-up1-a", Name: "vps1", Protocol: domain.ProtocolOpenAIChat,
			AuthType: domain.AuthTypePeer, APIKeySource: "literal:" + peerTokenPrefix + "one",
			PeerGrants: &domain.PeerUpstreamGrants{UpstreamKey: "up-1", UpstreamName: "vps1"},
		},
		domain.Provider{
			ID: "peer-up1-b", Name: "vps1", Protocol: domain.ProtocolClaude,
			AuthType: domain.AuthTypePeer, APIKeySource: "literal:" + peerTokenPrefix + "one",
			PeerGrants: &domain.PeerUpstreamGrants{UpstreamKey: "up-1", UpstreamName: "vps1"},
		},
		domain.Provider{
			ID: "peer-up2-a", Name: "vps2", Protocol: domain.ProtocolOpenAIChat,
			AuthType: domain.AuthTypePeer, APIKeySource: "literal:" + peerTokenPrefix + "two",
			PeerGrants: &domain.PeerUpstreamGrants{UpstreamKey: "up-2", UpstreamName: "vps2"},
		},
	)
	return server
}

func serveLocalOperator(t *testing.T, server *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(""))
	request.Header.Set("Content-Type", "application/json")
	// 本机操作员：回环源地址 + 回环 Host，与其它控制台接口一致。
	request.Host = "127.0.0.1"
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

// Disconnecting one upstream must take its whole group with it. A registration
// leaves one provider per protocol under a single UpstreamKey; deleting only the
// row that was clicked would leave a half chain — the page still says connected,
// but half the models are gone, and the members would no longer share the one
// credential they were issued.
func TestDisconnectPeerUpstreamRemovesTheWholeGroup(t *testing.T) {
	server := disconnectedPeerServer(t)

	// 点的是组里任意一条成员行，删的必须是整组。
	recorder := serveLocalOperator(t, server, http.MethodDelete, "/__peer-upstreams/peer-up1-a")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	remaining := map[string]bool{}
	for _, provider := range server.router.State().Providers {
		if provider.Deleted {
			continue
		}
		remaining[provider.ID] = true
	}
	if remaining["peer-up1-a"] || remaining["peer-up1-b"] {
		t.Fatalf("up-1 的成员行应当整组删除，实际还在：%v", remaining)
	}
	if !remaining["peer-up2-a"] {
		t.Fatalf("另一条上游不该被牵连，实际：%v", remaining)
	}
}

// The disconnect is a soft delete like every other provider, so the rows can be
// restored from the trash instead of being lost to a stray click.
func TestDisconnectPeerUpstreamIsRecoverable(t *testing.T) {
	server := disconnectedPeerServer(t)

	if recorder := serveLocalOperator(t, server, http.MethodDelete, "/__peer-upstreams/peer-up2-a"); recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	deleted := server.router.DeletedProviders()
	found := false
	for _, provider := range deleted {
		if provider.ID == "peer-up2-a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("断开应当是软删除（回收站可见），实际回收站里没有：%+v", deleted)
	}
}

// An id that is not a peer provider is refused rather than silently deleting a
// local model provider that happens to be named in the URL.
func TestDisconnectPeerUpstreamRejectsNonPeerProvider(t *testing.T) {
	server := newPeerTestServer(t, domain.Provider{
		ID: "local-model", Name: "local", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeAPIKey, APIKeySource: "literal:sk-test",
	})

	recorder := serveLocalOperator(t, server, http.MethodDelete, "/__peer-upstreams/local-model")
	if recorder.Code == http.StatusOK {
		t.Fatalf("非 peer provider 不该被这个接口删掉，body=%s", recorder.Body.String())
	}
	for _, provider := range server.router.State().Providers {
		if provider.ID == "local-model" && provider.Deleted {
			t.Fatal("local-model 被误删了")
		}
	}
}
