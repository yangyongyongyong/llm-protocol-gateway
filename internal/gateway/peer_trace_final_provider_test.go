package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// 回归：一次请求先试上游客服网关、429 后切到本地 Provider 成功时，请求日志
// 的 ProviderID 与父子链路口径必须一致。候选上游只能出现在 analysis.attempts，
// 不能继续占着 Egress，否则控制台会显示“OpenCode + 上游网关链路”这种矛盾组合。
func TestPeerEgressTraceFollowsFinalProviderAfterFailover(t *testing.T) {
	var peerHits atomic.Int64
	peerUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerHits.Add(1)
		w.Header().Set(peerIdentityHeader, otherID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
	}))
	defer peerUpstream.Close()

	var localHits atomic.Int64
	localUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-local", "object": "chat.completion",
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": "local provider"},
			}},
			"usage": map[string]any{"prompt_tokens": 4, "completion_tokens": 1, "total_tokens": 5},
		})
	}))
	defer localUpstream.Close()

	peerProvider := domain.Provider{
		ID: "via-parent", Name: "via-parent", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypePeer, BaseURL: peerUpstream.URL,
		APIKeySource: "literal:peer-token", HealthStatus: "healthy",
		PeerGrants: &domain.PeerUpstreamGrants{ParentGatewayID: otherID},
		Models: []domain.Model{{
			ID: "gpt-test", ProviderID: "via-parent", Protocol: domain.ProtocolOpenAIChat, InMenu: true,
		}},
	}
	localProvider := domain.Provider{
		ID: "local-provider", Name: "local-provider", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeAPIKey, BaseURL: localUpstream.URL + "/v1",
		APIKeySource: "literal:sk-local", HealthStatus: "healthy",
		Models: []domain.Model{{
			ID: "gpt-test", ProviderID: "local-provider", Protocol: domain.ProtocolOpenAIChat, InMenu: true,
		}},
	}
	route := domain.Route{
		ID: "r-final-provider", Name: "r-final-provider", ProviderID: peerProvider.ID,
		OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true,
	}
	key := domain.APIKey{
		ID: "k-final-provider", Name: "k-final-provider", Key: "sk-gw-final-provider",
		RouteID: route.ID, Enabled: true, StreamEnabled: true,
		FallbackProviderIDs: []string{localProvider.ID},
	}
	server := NewServer(NewRouter(domain.GatewayState{
		Providers: []domain.Provider{peerProvider, localProvider},
		Routes:    []domain.Route{route},
		APIKeys:   []domain.APIKey{key},
	}), monitor.NewStore())

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+key.Key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if peerHits.Load() != 1 || localHits.Load() != 1 {
		t.Fatalf("expected peer then local attempt, peer=%d local=%d", peerHits.Load(), localHits.Load())
	}

	page := server.logs.Query(monitor.RequestLogQuery{Page: 1, PageSize: 10, Status: "all"})
	if len(page.Items) != 1 {
		t.Fatalf("request logs = %+v", page.Items)
	}
	entry := page.Items[0]
	if entry.ProviderID != localProvider.ID {
		t.Fatalf("ProviderID = %q, want final local provider %q", entry.ProviderID, localProvider.ID)
	}
	if entry.Egress != "" || entry.PeerGatewayID != "" || entry.PeerDirection != "" || entry.PeerOrigin != "" || entry.PeerPath != "" {
		t.Fatalf("final local provider must not keep failed peer egress fields: %+v", entry)
	}
	if len(entry.Analysis.Attempts) != 2 {
		t.Fatalf("candidate history must keep both attempts: %+v", entry.Analysis.Attempts)
	}
	if entry.Analysis.Attempts[0].ProviderID != peerProvider.ID || entry.Analysis.Attempts[1].ProviderID != localProvider.ID {
		t.Fatalf("attempt order = %+v", entry.Analysis.Attempts)
	}
}
