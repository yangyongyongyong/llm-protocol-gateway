package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// 父子透传的三列必须在三条路径上都写得进、读得出：列表查询（摘要）、
// 列表查询（带正文）、单条详情。它们各有独立的 SELECT 列清单和独立的
// Scan 序列，漏改任何一处，Scan 的参数个数就会和列数对不上——那是运行时
// 才炸的错误，不会在编译期暴露。
func TestRequestLogPeerColumnsRoundTrip(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	peerLog := monitor.RequestLog{
		Time:          now.Add(-time.Minute),
		Model:         "claude-3",
		Status:        200,
		Ingress:       "gw_aaaaaaaaaaaaaaaa",
		Egress:        "gw_bbbbbbbbbbbbbbbb",
		PeerGatewayID: "gw_aaaaaaaaaaaaaaaa",
		PeerDirection: "ingress",
		PeerOrigin:    "gw_aaaaaaaaaaaaaaaa",
		PeerPath:      "gw_aaaaaaaaaaaaaaaa,gw_mmmmmmmmmmmmmmmm",
		Analysis:      monitor.RequestAnalysis{SessionID: "sess-peer"},
		RequestBody:   `{"model":"claude-3"}`,
		ResponseBody:  `{"ok":true}`,
	}
	localLog := monitor.RequestLog{Time: now, Model: "claude-3", Status: 200}
	for _, log := range []monitor.RequestLog{peerLog, localLog} {
		if err := store.AppendRequestLog(log); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	// 摘要路径（默认不取正文）。
	page, err := store.QueryRequestLogs(monitor.RequestLogQuery{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("query summary: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("got %d rows, want 2", len(page.Items))
	}
	// 按时间倒序：第一条是本地请求，第二条是透传请求。
	local, peer := page.Items[0], page.Items[1]
	if local.Ingress != "" || local.Egress != "" || local.PeerGatewayID != "" || local.PeerPath != "" {
		t.Fatalf("a local request must leave the peer columns empty, got %+v", local)
	}
	if peer.Ingress != peerLog.Ingress || peer.Egress != peerLog.Egress ||
		peer.PeerGatewayID != peerLog.PeerGatewayID || peer.PeerDirection != peerLog.PeerDirection ||
		peer.PeerOrigin != peerLog.PeerOrigin || peer.PeerPath != peerLog.PeerPath {
		t.Fatalf("summary scan lost peer columns: %+v", peer)
	}

	// 带正文的列表路径。
	withBodies, err := store.QueryRequestLogs(monitor.RequestLogQuery{Page: 1, PageSize: 10, IncludeBodies: true})
	if err != nil {
		t.Fatalf("query with bodies: %v", err)
	}
	if got := withBodies.Items[1]; got.Ingress != peerLog.Ingress || got.PeerGatewayID != peerLog.PeerGatewayID ||
		got.PeerDirection != peerLog.PeerDirection || got.PeerOrigin != peerLog.PeerOrigin ||
		got.PeerPath != peerLog.PeerPath {
		t.Fatalf("body scan lost peer columns: %+v", got)
	}
	if withBodies.Items[1].RequestBody == "" {
		t.Fatal("IncludeBodies must still return the request body")
	}

	// 单条详情路径。
	detail, ok, err := store.RequestLogByID(peer.ID)
	if err != nil {
		t.Fatalf("by id: %v", err)
	}
	if !ok {
		t.Fatal("by id must find the row it just wrote")
	}
	if detail.Ingress != peerLog.Ingress || detail.Egress != peerLog.Egress ||
		detail.PeerGatewayID != peerLog.PeerGatewayID || detail.PeerDirection != peerLog.PeerDirection ||
		detail.PeerOrigin != peerLog.PeerOrigin || detail.PeerPath != peerLog.PeerPath {
		t.Fatalf("detail scan lost peer columns: %+v", detail)
	}
	if detail.ResponseBody != peerLog.ResponseBody {
		t.Fatalf("detail lost the response body: %q", detail.ResponseBody)
	}
}

// 老库升级：建表时没有这三列，ensureRequestLogsTable 必须补上，且既有行读到
// 的是空串而不是报错。
func TestRequestLogPeerColumnsAddedToExistingDatabase(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "legacy.db")

	// 先造一个"老版本"的表：只有当时就有的列。
	legacy, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.db.Exec(`DROP TABLE request_logs`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.db.Exec(`CREATE TABLE request_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		time TEXT NOT NULL,
		api_key_id TEXT NOT NULL DEFAULT '',
		api_key_name TEXT NOT NULL DEFAULT '',
		route_id TEXT NOT NULL DEFAULT '',
		provider_id TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		action TEXT NOT NULL DEFAULT '',
		protocol_flow TEXT NOT NULL DEFAULT '',
		path TEXT NOT NULL DEFAULT '',
		status INTEGER NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		cache_tokens INTEGER NOT NULL DEFAULT 0,
		cost_micros INTEGER NOT NULL DEFAULT 0,
		priced INTEGER NOT NULL DEFAULT 0,
		latency_ms INTEGER NOT NULL DEFAULT 0,
		client_host TEXT NOT NULL DEFAULT '',
		client_ip TEXT NOT NULL DEFAULT '',
		access_source TEXT NOT NULL DEFAULT '',
		error_description TEXT NOT NULL DEFAULT '',
		request_body TEXT NOT NULL DEFAULT '',
		response_body TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.db.Exec(`INSERT INTO request_logs (time, model, status) VALUES (?, 'old-model', 200)`,
		time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	// 重新打开会跑 ensureRequestLogsTable 的补列流程。
	upgraded, err := Open(path)
	if err != nil {
		t.Fatalf("reopen legacy db: %v", err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })

	page, err := upgraded.QueryRequestLogs(monitor.RequestLogQuery{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("query upgraded db: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("got %d rows, want the 1 legacy row", len(page.Items))
	}
	if item := page.Items[0]; item.Ingress != "" || item.Egress != "" || item.PeerGatewayID != "" ||
		item.PeerDirection != "" || item.PeerOrigin != "" || item.PeerPath != "" {
		t.Fatalf("a legacy row must read back as empty, got %+v", item)
	}

	// 补列之后新写入也要正常。
	if err := upgraded.AppendRequestLog(monitor.RequestLog{
		Time:          time.Now().UTC(),
		Model:         "new-model",
		Status:        200,
		PeerGatewayID: "gw_cccccccccccccccc",
	}); err != nil {
		t.Fatalf("append after upgrade: %v", err)
	}
	after, err := upgraded.QueryRequestLogs(monitor.RequestLogQuery{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("query after upgrade: %v", err)
	}
	if len(after.Items) != 2 || after.Items[0].PeerGatewayID != "gw_cccccccccccccccc" {
		t.Fatalf("new row after upgrade = %+v", after.Items)
	}
}
