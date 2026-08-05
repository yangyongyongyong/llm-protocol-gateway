package gateway

import (
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// The 30-minute Cursor refresh calls saveState() (a full DELETE+re-INSERT of
// providers/models/routes) only when the catalog actually changed, so this
// comparison is what keeps an unchanged refresh off the disk.
func TestSameModelCatalog(t *testing.T) {
	base := []domain.Model{
		{ID: "m1", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, ContextLength: 200000, InMenu: true},
		{ID: "m2", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, ContextLength: 128000, InMenu: true},
	}
	sameContent := []domain.Model{
		{ID: "m1", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, ContextLength: 200000, InMenu: true},
		{ID: "m2", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, ContextLength: 128000, InMenu: true},
	}

	if !sameModelCatalog(base, sameContent) {
		t.Fatal("identical catalogs must compare equal (otherwise every refresh rewrites the DB)")
	}
	if !sameModelCatalog(nil, nil) {
		t.Fatal("two empty catalogs must compare equal")
	}

	cases := map[string][]domain.Model{
		"extra model":    append(append([]domain.Model{}, base...), domain.Model{ID: "m3", ProviderID: "p1"}),
		"fewer models":   base[:1],
		"renamed model":  {base[0], {ID: "m2-renamed", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, ContextLength: 128000, InMenu: true}},
		"context change": {base[0], {ID: "m2", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, ContextLength: 64000, InMenu: true}},
		"reordered":      {base[1], base[0]},
		"empty vs full":  nil,
	}
	for name, other := range cases {
		if sameModelCatalog(base, other) {
			t.Fatalf("%s must not compare equal", name)
		}
	}
}
