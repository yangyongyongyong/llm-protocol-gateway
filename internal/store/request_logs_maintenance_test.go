package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// 新库应默认启用 INCREMENTAL auto_vacuum，MaintainStorage 走增量回收分支。
func TestMaintainStorageNewDBIsIncremental(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "gateway.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var autoVacuum int
	if err := s.db.QueryRow(`PRAGMA auto_vacuum`).Scan(&autoVacuum); err != nil {
		t.Fatal(err)
	}
	if autoVacuum != 2 {
		t.Fatalf("new DB auto_vacuum = %d, want 2 (INCREMENTAL)", autoVacuum)
	}
	// 干净库调用 MaintainStorage 不应报错。
	if err := s.MaintainStorage(); err != nil {
		t.Fatalf("MaintainStorage on clean incremental DB: %v", err)
	}
}

// 删行 + 维护后，磁盘应回收（文件体积明显下降）。
func TestMaintainStorageReclaimsSpaceAfterPrune(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "gateway.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// 写入足够多的大请求体（每条 ~200 KiB），把库撑大。
	bigBody := strings.Repeat("x", 200*1024)
	base := time.Now().UTC()
	for i := 0; i < 200; i++ {
		log := monitor.RequestLog{
			Time:        base.Add(time.Duration(i) * time.Second),
			Status:      502,
			RequestBody: bigBody,
		}
		if err := s.AppendRequestLogWithRetention(log, 7); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	// 触发 WAL checkpoint 让主库文件反映真实大小。
	_, _ = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	sizeBefore := dbFileSize(t, path)

	// 删除所有行（保留 0 天 → 全部过期）。
	if _, err := s.db.Exec(`DELETE FROM request_logs`); err != nil {
		t.Fatal(err)
	}
	if err := s.MaintainStorage(); err != nil {
		t.Fatalf("MaintainStorage: %v", err)
	}
	_, _ = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	sizeAfter := dbFileSize(t, path)

	if sizeAfter >= sizeBefore {
		t.Fatalf("expected DB to shrink after maintenance: before=%d after=%d", sizeBefore, sizeAfter)
	}
	// 回收后应远小于撑大时的体积（至少缩掉一半）。
	if sizeAfter > sizeBefore/2 {
		t.Fatalf("reclaim insufficient: before=%d after=%d", sizeBefore, sizeAfter)
	}
}

// 老库（auto_vacuum=NONE）首次维护应转换为 INCREMENTAL 并回收空间。
func TestMaintainStorageConvertsLegacyDB(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "legacy.db")
	// 用 NONE 模式打开，模拟历史遗留库。注意 auto_vacuum 必须在建表前设定。
	legacy, err := openWithAutoVacuum(path, "NONE")
	if err != nil {
		t.Fatal(err)
	}
	var av int
	if err := legacy.db.QueryRow(`PRAGMA auto_vacuum`).Scan(&av); err != nil {
		t.Fatal(err)
	}
	if av != 0 {
		t.Fatalf("legacy DB auto_vacuum = %d, want 0 (NONE)", av)
	}

	bigBody := strings.Repeat("y", 200*1024)
	for i := 0; i < 100; i++ {
		log := monitor.RequestLog{Time: time.Now().UTC().Add(time.Duration(i) * time.Second), Status: 502, RequestBody: bigBody}
		if err := legacy.AppendRequestLogWithRetention(log, 7); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if _, err := legacy.db.Exec(`DELETE FROM request_logs`); err != nil {
		t.Fatal(err)
	}
	_, _ = legacy.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	sizeBefore := dbFileSize(t, path)

	// 首次维护：应执行 VACUUM 转换模式并回收。
	if err := legacy.MaintainStorage(); err != nil {
		t.Fatalf("MaintainStorage (legacy convert): %v", err)
	}
	var avAfter int
	if err := legacy.db.QueryRow(`PRAGMA auto_vacuum`).Scan(&avAfter); err != nil {
		t.Fatal(err)
	}
	if avAfter != 2 {
		t.Fatalf("after convert auto_vacuum = %d, want 2 (INCREMENTAL)", avAfter)
	}
	_, _ = legacy.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	sizeAfter := dbFileSize(t, path)
	if sizeAfter >= sizeBefore {
		t.Fatalf("expected legacy DB to shrink after VACUUM: before=%d after=%d", sizeBefore, sizeAfter)
	}
	_ = legacy.Close()
}

func dbFileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}

// openWithAutoVacuum opens a Store forcing a specific auto_vacuum mode, so tests
// can simulate a legacy (NONE) database before the incremental default landed.
// auto_vacuum must be set before any table is created, hence the raw open here.
func openWithAutoVacuum(path, mode string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=auto_vacuum("+mode+")")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{path: path, db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}
