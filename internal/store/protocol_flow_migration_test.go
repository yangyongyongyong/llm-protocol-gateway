package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// 协议流方向订正：改版前写的是 "<上游> -> <客户端>"，现在是 "<客户端> -> <上游>"。
//
// 真实升级顺序是：库里已经躺着旧方向的历史行 → 新版本启动时 Open() 跑迁移，
// 一次性翻正 → 之后写入的行都是新方向，靠 settings 标记不再翻第二次。
//
// 这里按这个顺序造场景：先插旧行（模拟升级前），再跑迁移；然后插入新行，在
// 保留标记的前提下再跑一次，确认新行不会被翻回去。
func TestReverseLegacyProtocolFlowFlipsLegacyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// 升级前遗留：方向写反的历史行。
	for _, flow := range []string{
		"OpenAI Chat -> OpenAI Responses",
		"Claude -> OpenAI Chat",
	} {
		if err := s.AppendRequestLog(monitor.RequestLog{
			Time:         time.Now(),
			ProtocolFlow: flow,
			Action:       "pass_through",
			Path:         "/openai/v1/responses",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// 等价于升级后首次启动：标记还没写，迁移真正执行。
	if err := s.runProtocolFlowMigration(t, true); err != nil {
		t.Fatal(err)
	}
	flows, err := s.protocolFlowValuesForTest()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"OpenAI Responses -> OpenAI Chat", "OpenAI Chat -> Claude"} {
		if !flows[want] {
			t.Fatalf("旧行没被翻正，期望含 %q，现有：%v", want, flows)
		}
	}
	for _, gone := range []string{"OpenAI Chat -> OpenAI Responses", "Claude -> OpenAI Chat"} {
		if flows[gone] {
			t.Fatalf("旧方向仍然存在 %q：%v", gone, flows)
		}
	}
	if got := s.setting(protocolFlowDirectionSettingKey); got != "done" {
		t.Fatalf("幂等标记 = %q, want done", got)
	}

	// 升级后新写入的行，方向已经是新的。
	fresh := "OpenAI Responses -> Claude"
	if err := s.AppendRequestLog(monitor.RequestLog{
		Time:         time.Now(),
		ProtocolFlow: fresh,
		Action:       "convert",
		Path:         "/openai/v1/responses",
	}); err != nil {
		t.Fatal(err)
	}

	// 再次启动（保留标记）：新行必须原样留着，不能又被翻回去。
	if err := s.runProtocolFlowMigration(t, false); err != nil {
		t.Fatal(err)
	}
	flows2, err := s.protocolFlowValuesForTest()
	if err != nil {
		t.Fatal(err)
	}
	if !flows2[fresh] {
		t.Fatalf("第二次迁移把新方向的行翻掉了，期望含 %q，现有：%v", fresh, flows2)
	}
	if !flows2["OpenAI Responses -> OpenAI Chat"] {
		t.Fatalf("第二次迁移把已翻正的行又翻回去了：%v", flows2)
	}
}

// 旧日志里同协议直通可能只写一个协议名；新格式是两侧都写。旧单名行要原样保留，
// 不能被当成带箭头记录误翻。
func TestReverseLegacyProtocolFlowLeavesSingleNameAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.AppendRequestLog(monitor.RequestLog{
		Time:         time.Now(),
		ProtocolFlow: "OpenAI Chat",
		Action:       "pass_through",
		Path:         "/v1/chat/completions",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.runProtocolFlowMigration(t, true); err != nil {
		t.Fatal(err)
	}
	flows, err := s.protocolFlowValuesForTest()
	if err != nil {
		t.Fatal(err)
	}
	if !flows["OpenAI Chat"] {
		t.Fatalf("直通协议名被改动了：%v", flows)
	}
}

// runProtocolFlowMigration 在测试里单独跑一次方向订正迁移。
//
// resetMarker=true 模拟"升级后首次启动"（标记还没写过）；false 则保留标记，
// 验证幂等：迁移在真实路径上每次 Open() 都会跑。
func (s *Store) runProtocolFlowMigration(t *testing.T, resetMarker bool) error {
	t.Helper()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if resetMarker {
		if _, err := tx.Exec(`DELETE FROM settings WHERE key = ?`, protocolFlowDirectionSettingKey); err != nil {
			return err
		}
	}
	if err := reverseLegacyProtocolFlow(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// protocolFlowValuesForTest 返回当前库里出现过的 protocol_flow 集合。
func (s *Store) protocolFlowValuesForTest() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT DISTINCT protocol_flow FROM request_logs WHERE protocol_flow <> ''`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var flow string
		if err := rows.Scan(&flow); err != nil {
			return nil, err
		}
		out[flow] = true
	}
	return out, rows.Err()
}
