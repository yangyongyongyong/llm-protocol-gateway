package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// A peer request cancelled before the parent sends X-Gateway-Id must still leave
// an egress chain in the log. The registered parent ID is a safe fallback: it is
// the identity the child authenticated against, not a value supplied by the
// current request.
func TestSendPeerDoorRecordsEgressWhenCancelledBeforeResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer upstream.Close()

	ctx, cancel := context.WithCancel(context.Background())
	timing := newRequestTiming(time.Now())
	ctx = withRequestTiming(ctx, timing)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	provider := domain.Provider{
		ID:           "peer-member",
		Name:         "peer-member",
		Protocol:     domain.ProtocolOpenAIResponses,
		AuthType:     domain.AuthTypePeer,
		BaseURL:      upstream.URL,
		APIKeySource: "literal:peer-token",
		PeerGrants:   &domain.PeerUpstreamGrants{ParentGatewayID: otherID},
	}

	cancel()
	server := NewServer(NewRouter(domain.GatewayState{}), nil)
	if _, err := server.sendPeerDoor(ctx, request, provider, "m", []byte(`{"model":"m","input":"hi"}`), "application/json"); err == nil {
		t.Fatal("a cancelled peer request must return an error")
	}

	trace := peerTraceFor(request, timing)
	if trace.Egress != otherID {
		t.Fatalf("Egress = %q, want the registered parent %q", trace.Egress, otherID)
	}
	if trace.peerDirection() != "egress" {
		t.Fatalf("peer direction = %q, want egress", trace.peerDirection())
	}
}
