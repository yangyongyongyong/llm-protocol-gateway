package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// SQLite 日志查询必须和内存筛选保持同一口径：499 是独立的客户端取消类，
// 不能出现在 4xx 异常筛选里。
func TestQueryRequestLogsSeparatesClientCanceled(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	logs := []monitor.RequestLog{
		{ID: 1, Time: now.Add(-4 * time.Minute), Model: "m", Status: 200},
		{ID: 2, Time: now.Add(-3 * time.Minute), Model: "m", Status: 429},
		{ID: 3, Time: now.Add(-2 * time.Minute), Model: "m", Status: 499},
		{ID: 4, Time: now.Add(-time.Minute), Model: "m", Status: 500},
	}
	for _, log := range logs {
		if err := store.AppendRequestLog(log); err != nil {
			t.Fatalf("append %d: %v", log.ID, err)
		}
	}

	tests := []struct {
		status string
		wantID int64
	}{
		{status: "2xx", wantID: 1},
		{status: "4xx", wantID: 2},
		{status: "499", wantID: 3},
		{status: "5xx", wantID: 4},
	}
	for _, tc := range tests {
		page, err := store.QueryRequestLogs(monitor.RequestLogQuery{Status: tc.status, Page: 1, PageSize: 10})
		if err != nil {
			t.Fatalf("query %s: %v", tc.status, err)
		}
		if len(page.Items) != 1 || page.Items[0].ID != tc.wantID {
			t.Fatalf("status %s returned %+v, want only id %d", tc.status, page.Items, tc.wantID)
		}
	}
}
