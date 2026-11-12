package gateway

import (
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 探测结论要能单独写回 provider，且顺手去重、记时间戳。
func TestSetProviderProtocolSupport(t *testing.T) {
	t.Parallel()
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{ID: "p1", Name: "p1", Protocol: domain.ProtocolClaude}},
	})

	saved, err := router.SetProviderProtocolSupport("p1",
		[]string{"claude", " openai_chat ", "openai_chat", ""})
	if err != nil {
		t.Fatalf("SetProviderProtocolSupport: %v", err)
	}
	if len(saved.ProtocolSupport) != 2 || saved.ProtocolSupport[0] != "claude" || saved.ProtocolSupport[1] != "openai_chat" {
		t.Fatalf("protocols = %v, want deduped [claude openai_chat]", saved.ProtocolSupport)
	}
	if saved.ProtocolProbedAt == "" {
		t.Fatal("探测时间应当被记下来")
	}
	if saved.Protocol != domain.ProtocolClaude {
		t.Fatalf("写探测结论不该动母语协议，got %q", saved.Protocol)
	}
}

// supportedProtocols 只留探测成功的那些。
func TestSupportedProtocolsFiltersUnsupported(t *testing.T) {
	t.Parallel()
	results := []protocolDetection{
		{Protocol: domain.ProtocolClaude, Supported: true},
		{Protocol: domain.ProtocolOpenAIChat, Supported: false},
		{Protocol: domain.ProtocolOpenAIResponses, Supported: true},
	}
	got := supportedProtocols(results)
	if len(got) != 2 || got[0] != "claude" || got[1] != "openai_responses" {
		t.Fatalf("supportedProtocols = %v", got)
	}
}
