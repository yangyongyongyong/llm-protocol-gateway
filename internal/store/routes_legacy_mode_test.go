package store

import (
	"database/sql"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 老数据库的 routes 表还带着 mode 列。新代码不再读写它，必须仍能从旧表
// 正常加载路由，否则升级后整个控制台都会因为多一列而读不到路由。
func TestLoadRoutesIgnoresLegacyModeColumn(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE routes (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		output_endpoint_id TEXT NOT NULL DEFAULT '',
		provider_id TEXT NOT NULL,
		output_protocol TEXT NOT NULL,
		mode TEXT NOT NULL DEFAULT 'auto',
		enabled INTEGER NOT NULL DEFAULT 1,
		position INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO routes
		(id, name, output_endpoint_id, provider_id, output_protocol, mode, enabled, position)
		VALUES ('r1', 'legacy', 'e1', 'p1', 'openai_chat', 'pass_through', 1, 0)`); err != nil {
		t.Fatal(err)
	}

	routes, err := (&Store{db: db}).loadRoutes()
	if err != nil {
		t.Fatalf("loadRoutes: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %d, want 1", len(routes))
	}
	got := routes[0]
	if got.ID != "r1" || got.ProviderID != "p1" || got.OutputEndpointID != "e1" {
		t.Fatalf("route = %+v", got)
	}
	if got.OutputProtocol != domain.ProtocolOpenAIChat || !got.Enabled {
		t.Fatalf("route = %+v", got)
	}
}
