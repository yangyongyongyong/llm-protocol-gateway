package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

const (
	requestLogSafetyCap   = 50000
	defaultRequestLogDays = 7
	defaultRequestLogPage = 100
	maxRequestLogPageSize = 500
	// defaultPageSizeBytes is only a fallback for reporting when PRAGMA page_size
	// answers something unusable.
	defaultPageSizeBytes = 4096
	// incrementalVacuumThresholdPages 是触发增量回收的空闲页阈值。默认页大小
	// 4 KiB，2560 页约 10 MiB：低于此不回收，避免频繁小额 IO；高于此才把空闲
	// 空间还给操作系统。
	incrementalVacuumThresholdPages = 2560
)

func (s *Store) AppendRequestLog(log monitor.RequestLog) error {
	return s.AppendRequestLogWithRetention(log, defaultRequestLogDays)
}

// AppendRequestLogs inserts many rows in a single transaction/commit.
//
// A bodiless success row is only a few hundred bytes while every commit dirties
// at least one page, so committing per request wastes most of each page. The
// gateway's request-log batcher funnels successful requests here; failures still
// go through AppendRequestLogWithRetention one at a time so they land
// immediately.
func (s *Store) AppendRequestLogs(logs []monitor.RequestLog) error {
	if len(logs) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(requestLogInsertSQL)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()

	for _, log := range logs {
		if log.Time.IsZero() {
			log.Time = time.Now()
		}
		if _, err := stmt.Exec(requestLogInsertArgs(log)...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// requestLogInsertSQL / requestLogInsertArgs are shared by the single-row and
// batched insert paths so the column list can only ever drift in one place.
const requestLogInsertSQL = `INSERT INTO request_logs (
	time, api_key_id, api_key_name, route_id, provider_id, model, action, protocol_flow, path,
	status, input_tokens, output_tokens, cache_tokens, cache_write_tokens, cost_micros, priced, latency_ms, ttft_ms,
	prep_ms, pre_upstream_ms, upstream_ttfb_ms, gateway_overhead_ms, convert_out_ms, post_ms, timing_flags,
	client_host, client_ip, access_source, reasoning_effort,
	ingress, egress, peer_gateway_id, peer_direction, peer_origin, peer_path,
	error_description, request_body, response_body, analysis_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func requestLogInsertArgs(log monitor.RequestLog) []any {
	analysis, _ := json.Marshal(log.Analysis)
	return []any{
		log.Time.UTC().Format(time.RFC3339Nano),
		log.APIKeyID,
		log.APIKeyName,
		log.RouteID,
		log.ProviderID,
		log.Model,
		log.Action,
		log.ProtocolFlow,
		log.Path,
		log.Status,
		log.InputTokens,
		log.OutputTokens,
		log.CacheTokens,
		log.CacheWriteTokens,
		log.CostMicros,
		boolToInt(log.Priced),
		log.LatencyMillis,
		log.TTFTMillis,
		log.PrepMillis,
		log.PreUpstreamMillis,
		log.UpstreamTTFBMillis,
		log.GatewayOverheadMillis,
		log.ConvertOutMillis,
		log.PostMillis,
		log.TimingFlags,
		log.ClientHost,
		log.ClientIP,
		log.AccessSource,
		log.ReasoningEffort,
		log.Ingress,
		log.Egress,
		log.PeerGatewayID,
		log.PeerDirection,
		log.PeerOrigin,
		log.PeerPath,
		log.ErrorDescription,
		log.RequestBody,
		log.ResponseBody,
		string(analysis),
	}
}

func (s *Store) AppendRequestLogWithRetention(log monitor.RequestLog, retentionDays int) error {
	if log.Time.IsZero() {
		log.Time = time.Now()
	}
	_, err := s.db.Exec(requestLogInsertSQL, requestLogInsertArgs(log)...)
	// retentionDays 不在这里立即生效——保留窗口按天计，靠网关启动时和每 6 小时
	// 的维护 ticker（internal/app/runtime.go）跑 PruneRequestLogs 就足够及时，
	// 没必要每条请求都额外跑 2 条 DELETE（稳态下几乎总是 0 行命中，纯粹的
	// 写锁占用和语句开销，见磨损排查记录）。retentionDays 参数保留只是为了不
	// 破坏调用方签名；真正生效的地方是上面提到的两处周期性调用。
	return err
}

func (s *Store) PruneRequestLogs(retentionDays int) error {
	return s.PruneRequestLogsContext(context.Background(), retentionDays)
}

// pruneBatchRows bounds how many rows one prune statement deletes and
// pruneBatchLimit how many such statements a single pass runs.
//
// Both exist because the prune shares the *single* SQLite writer connection with
// request-log writes and with the shutdown flush. One statement deleting hundreds
// of thousands of rows holds that connection for minutes: the gateway then cannot
// exit inside systemd's TimeoutStopSec, gets SIGKILLed mid-transaction, and the
// rollback leaves the table exactly as it was — so the next start repeats the same
// unfinished work. A deploy into that state stalls for 90s and looks hung.
const (
	pruneBatchRows  = 2000
	pruneBatchLimit = 500
	// storageReclaimBatchPages bounds how many free pages one
	// PRAGMA incremental_vacuum releases, for the same reason: a single statement
	// returning gigabytes of pages to the OS would own the writer connection while
	// shutdown was trying to use it.
	storageReclaimBatchPages = 8192
	storageReclaimBatchLimit = 512
)

// fullVacuumAutoConvertMaxPages caps the file size the gateway will convert
// NONE/FULL → INCREMENTAL on its own. Converting rewrites the entire file in one
// statement: seconds on a small database, minutes on a multi-gigabyte one.
// A var so tests can exercise both sides of the guard.
var fullVacuumAutoConvertMaxPages = 65536 // 256 MiB with the default 4 KiB pages

// PruneRequestLogsContext drops expired rows and enforces the row cap in bounded
// batches. A cancelled ctx stops the pass without failing it: the scheduled
// maintenance picks up where it left off.
func (s *Store) PruneRequestLogsContext(ctx context.Context, retentionDays int) error {
	if retentionDays <= 0 {
		retentionDays = defaultRequestLogDays
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339Nano)
	if _, err := s.deleteInBatches(ctx,
		`DELETE FROM request_logs WHERE id IN (
			SELECT id FROM request_logs WHERE time < ? ORDER BY time LIMIT ?)`,
		cutoff, pruneBatchRows); err != nil {
		return err
	}
	// Safety cap: the inner select lists everything beyond the newest
	// requestLogSafetyCap rows, the outer LIMIT takes one batch of them.
	_, err := s.deleteInBatches(ctx,
		`DELETE FROM request_logs WHERE id IN (
			SELECT id FROM (
				SELECT id FROM request_logs ORDER BY time DESC, id DESC LIMIT -1 OFFSET ?
			) LIMIT ?)`,
		requestLogSafetyCap, pruneBatchRows)
	return err
}

// deleteInBatches repeats statement until it matches no rows or the pass budget
// runs out, committing each batch separately so the writer connection is released
// between them.
func (s *Store) deleteInBatches(ctx context.Context, statement string, args ...any) (int, error) {
	deleted := 0
	for batch := 0; batch < pruneBatchLimit; batch++ {
		if err := ctx.Err(); err != nil {
			slog.Info("prune stopped between batches", "deleted", deleted, "reason", err)
			return deleted, nil
		}
		result, err := s.db.ExecContext(ctx, statement, args...)
		if err != nil {
			return deleted, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return deleted, err
		}
		deleted += int(rows)
		if rows == 0 {
			return deleted, nil
		}
	}
	if deleted > 0 {
		slog.Info("prune pass hit its batch budget; remaining rows go to the next one", "deleted", deleted)
	}
	return deleted, nil
}

// MaintainStorage reclaims disk space that pruning frees inside the SQLite file.
//
// 背景：request_logs 里错误请求体最大留 256 KiB，坏 Provider 一次故障风暴可能
// 把库撑大数 GB。删行（PruneRequestLogs）只把页放回 freelist，不还给操作系统，
// 文件只增不减。
//
// 处理策略：
//   - 增量库（auto_vacuum=INCREMENTAL）：分批 PRAGMA incremental_vacuum，每批只放还
//     有限页数，批与批之间写连接是空的。
//   - 老库（NONE/FULL）：只有空闲页够多、且文件小于 fullVacuumAutoConvertMaxPages
//     才自动做一次全量 VACUUM 重建；大库留给维护窗口显式放行（GATEWAY_ALLOW_VACUUM）。
//     这条语句重写整个文件：生产上那个 4.28 GB 的库跑了 30 分钟，期间任何重启都会被
//     SIGKILL 打断、整段回滚，下次启动再从头发一遍——发布因此看起来卡死，库也永远收不拢。
func (s *Store) MaintainStorage() error {
	return s.MaintainStorageContext(context.Background())
}

func (s *Store) MaintainStorageContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return nil
	}
	autoVacuum, err := s.autoVacuumMode(ctx)
	if err != nil {
		return err
	}
	free, pages, pageSize, err := s.storagePages(ctx)
	if err != nil {
		return err
	}
	if free < incrementalVacuumThresholdPages {
		// Nothing worth handing back yet, in any mode. This is the steady state on
		// a healthy database, so it stays quiet.
		return nil
	}

	// auto_vacuum: 0=NONE, 1=FULL, 2=INCREMENTAL。
	if autoVacuum != 2 {
		// 切换 auto_vacuum 模式必须配合一次全量 VACUUM 才能生效（重写整个文件）。
		if pages > fullVacuumAutoConvertMaxPages && !fullVacuumAllowed() {
			slog.Warn("storage reclaim deferred: this database is not in incremental mode and needs a full VACUUM",
				"fileMiB", mib(pages, pageSize), "freeMiB", mib(free, pageSize),
				"hint", "run VACUUM in a maintenance window, or restart once with GATEWAY_ALLOW_VACUUM=1")
			return nil
		}
		if _, err := s.db.Exec(`PRAGMA auto_vacuum = INCREMENTAL`); err != nil {
			return fmt.Errorf("set auto_vacuum incremental: %w", err)
		}
		if _, err := s.db.Exec(`VACUUM`); err != nil {
			return fmt.Errorf("vacuum: %w", err)
		}
		slog.Info("storage reclaim rewrote the database", "fileMiB", mib(pages, pageSize))
		return nil
	}

	// 增量模式：分批把空闲页还给操作系统。不带参数的 PRAGMA incremental_vacuum
	// 会把 freelist 一次全部放还，大库上同样是分钟级的单语句；分批执行，每批之间
	// 写连接是空的。
	statement := fmt.Sprintf(`PRAGMA incremental_vacuum(%d)`, storageReclaimBatchPages)
	released := 0
	for batch := 0; batch < storageReclaimBatchLimit; batch++ {
		if err := ctx.Err(); err != nil {
			slog.Info("storage reclaim stopped between batches", "releasedMiB", mib(released, pageSize), "reason", err)
			return nil
		}
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("incremental_vacuum: %w", err)
		}
		released += storageReclaimBatchPages
		remaining, _, _, err := s.storagePages(ctx)
		if err != nil {
			return err
		}
		if remaining < storageReclaimBatchPages {
			break
		}
	}
	if released >= storageReclaimBatchPages*storageReclaimBatchLimit {
		slog.Info("storage reclaim hit its batch budget; the rest waits for the next pass",
			"releasedMiB", mib(released, pageSize))
	} else {
		slog.Info("storage reclaim returned free pages to the OS", "releasedMiB", mib(released, pageSize))
	}
	return nil
}

// autoVacuumMode reports how the open database will treat freed pages. Measured
// against a legacy file, the writer connection answers the file's own mode even
// though the DSN asks for INCREMENTAL, so this is the value the branches above
// need.
func (s *Store) autoVacuumMode(ctx context.Context) (int, error) {
	var mode int
	if err := s.db.QueryRowContext(ctx, `PRAGMA auto_vacuum`).Scan(&mode); err != nil {
		return 0, fmt.Errorf("read auto_vacuum: %w", err)
	}
	return mode, nil
}

// storagePages reports the freelist depth, how many pages the file holds, and the
// page size.
func (s *Store) storagePages(ctx context.Context) (free int, pages int, pageSize int, err error) {
	if err := s.db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&free); err != nil {
		return 0, 0, 0, fmt.Errorf("read freelist_count: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages); err != nil {
		return 0, 0, 0, fmt.Errorf("read page_count: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return 0, 0, 0, fmt.Errorf("read page_size: %w", err)
	}
	if pageSize <= 0 {
		pageSize = defaultPageSizeBytes
	}
	return free, pages, pageSize, nil
}

// mib converts a page count to megabytes so the log says something an operator can
// act on.
func mib(pages int, pageSize int) int64 {
	return int64(pages) * int64(pageSize) / (1 << 20)
}

// fullVacuumAllowed opts a large database into the one-time conversion VACUUM.
// Opt-in by design: that statement rewrites the whole file on the single writer
// connection, which is a maintenance-window cost rather than a startup one.
func fullVacuumAllowed() bool {
	value := strings.TrimSpace(os.Getenv("GATEWAY_ALLOW_VACUUM"))
	return value == "1" || strings.EqualFold(value, "true")
}

func (s *Store) ListRequestLogs(limit int) ([]monitor.RequestLog, error) {
	if limit <= 0 {
		limit = defaultRequestLogPage
	}
	page, err := s.QueryRequestLogs(monitor.RequestLogQuery{Page: 1, PageSize: limit, Status: "all"})
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

func (s *Store) QueryRequestLogs(query monitor.RequestLogQuery) (monitor.RequestLogPage, error) {
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = defaultRequestLogPage
	}
	if pageSize > maxRequestLogPageSize {
		pageSize = maxRequestLogPageSize
	}
	page := query.Page
	if page <= 0 {
		page = 1
	}

	where := []string{"1=1"}
	args := make([]any, 0, 6)
	if !query.From.IsZero() {
		where = append(where, "time >= ?")
		args = append(args, query.From.UTC().Format(time.RFC3339Nano))
	}
	if !query.To.IsZero() {
		where = append(where, "time < ?")
		args = append(args, query.To.UTC().Format(time.RFC3339Nano))
	}
	switch strings.ToLower(strings.TrimSpace(query.Status)) {
	case "2xx":
		where = append(where, "status >= 200 AND status < 300")
	case "499":
		where = append(where, "status = 499")
	case "4xx":
		where = append(where, "status >= 400 AND status < 500 AND status <> 499")
	case "5xx":
		where = append(where, "status >= 500 AND status < 600")
	}
	if keyName := strings.TrimSpace(query.APIKeyName); keyName != "" {
		where = append(where, "LOWER(api_key_name) LIKE ?")
		args = append(args, "%"+strings.ToLower(keyName)+"%")
	}
	if providerID := strings.TrimSpace(query.ProviderID); providerID != "" {
		where = append(where, "provider_id = ?")
		args = append(args, providerID)
	}
	if model := strings.TrimSpace(query.Model); model != "" {
		where = append(where, "model = ?")
		args = append(args, model)
	}
	if session := strings.TrimSpace(query.SessionID); session != "" {
		// session_id lives inside analysis_json; json_extract keeps this a
		// database-side filter instead of loading every row to compare.
		where = append(where, "json_extract(analysis_json, '$.sessionId') = ?")
		args = append(args, session)
	}
	if query.APIKeyIDs != nil {
		// Per-user isolation: an empty (non-nil) set must match nothing.
		if len(query.APIKeyIDs) == 0 {
			where = append(where, "1=0")
		} else {
			placeholders := make([]string, 0, len(query.APIKeyIDs))
			for _, id := range query.APIKeyIDs {
				placeholders = append(placeholders, "?")
				args = append(args, id)
			}
			where = append(where, "api_key_id IN ("+strings.Join(placeholders, ",")+")")
		}
	}

	if !query.BeforeTime.IsZero() && query.BeforeID > 0 {
		// Stable freeze matching ORDER BY time DESC, id DESC: keep the snapshot
		// frontier (newest page-1 row) and everything older.
		// 游标是"已读到的最旧一条"：它本身已经返回过，下一页必须从它之后
		// 继续，用严格小于，否则每翻一页都会重复那一行。
		where = append(where, "(time < ? OR (time = ? AND id < ?))")
		ts := query.BeforeTime.UTC().Format(time.RFC3339Nano)
		args = append(args, ts, ts, query.BeforeID)
	} else if !query.BeforeTime.IsZero() {
		where = append(where, "time <= ?")
		args = append(args, query.BeforeTime.UTC().Format(time.RFC3339Nano))
	} else if query.BeforeID > 0 {
		where = append(where, "id <= ?")
		args = append(args, query.BeforeID)
	}

	whereSQL := strings.Join(where, " AND ")
	var total int
	countArgs := append([]any{}, args...)
	if err := s.reader().QueryRow(`SELECT COUNT(1) FROM request_logs WHERE `+whereSQL, countArgs...).Scan(&total); err != nil {
		return monitor.RequestLogPage{}, err
	}

	offset := (page - 1) * pageSize
	args = append(args, pageSize, offset)
	selectCols := `id, time, api_key_id, api_key_name, route_id, provider_id, model, action, protocol_flow, path,
		status, input_tokens, output_tokens, cache_tokens, cache_write_tokens, cost_micros, priced, latency_ms, ttft_ms,
		prep_ms, pre_upstream_ms, upstream_ttfb_ms, gateway_overhead_ms, convert_out_ms, post_ms, timing_flags,
		client_host, client_ip, access_source, reasoning_effort,
		ingress, egress, peer_gateway_id, peer_direction, peer_origin, peer_path,
		error_description, analysis_json`
	if query.IncludeBodies {
		selectCols += `, request_body, response_body`
	}
	rows, err := s.reader().Query(`SELECT `+selectCols+`
		FROM request_logs WHERE `+whereSQL+` ORDER BY time DESC, id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return monitor.RequestLogPage{}, err
	}
	defer rows.Close()

	logs := make([]monitor.RequestLog, 0, pageSize)
	for rows.Next() {
		var item monitor.RequestLog
		var scanErr error
		if query.IncludeBodies {
			item, scanErr = scanRequestLog(rows)
		} else {
			item, scanErr = scanRequestLogSummary(rows)
		}
		if scanErr != nil {
			return monitor.RequestLogPage{}, scanErr
		}
		logs = append(logs, item)
	}
	if err := rows.Err(); err != nil {
		return monitor.RequestLogPage{}, err
	}
	return monitor.RequestLogPage{Items: logs, Total: total, Page: page}, nil
}

// RequestLogByID returns a single request log with full request and response bodies.
// ok is false if the id does not exist.
func (s *Store) RequestLogByID(id int64) (monitor.RequestLog, bool, error) {
	row := s.reader().QueryRow(`SELECT id, time, api_key_id, api_key_name, route_id, provider_id, model, action, protocol_flow, path,
		status, input_tokens, output_tokens, cache_tokens, cache_write_tokens, cost_micros, priced, latency_ms, ttft_ms,
		prep_ms, pre_upstream_ms, upstream_ttfb_ms, gateway_overhead_ms, convert_out_ms, post_ms, timing_flags,
		client_host, client_ip, access_source, reasoning_effort,
		ingress, egress, peer_gateway_id, peer_direction, peer_origin, peer_path,
		error_description, analysis_json, request_body, response_body
		FROM request_logs WHERE id = ?`, id)
	item, err := scanRequestLog(row)
	if err == sql.ErrNoRows {
		return monitor.RequestLog{}, false, nil
	}
	if err != nil {
		return monitor.RequestLog{}, false, err
	}
	return item, true, nil
}

func scanRequestLog(rows rowScanner) (monitor.RequestLog, error) {
	var item monitor.RequestLog
	var timeValue, analysis string
	var ttft, prep, preUp, upTTFB, overhead, convertOut, post sql.NullInt64
	var timingFlags, clientHost, clientIP, accessSource, reasoning, ingress, egress, peerGateway, peerDirection, peerOrigin, peerPath sql.NullString
	var pricedValue int
	if err := rows.Scan(
		&item.ID,
		&timeValue,
		&item.APIKeyID,
		&item.APIKeyName,
		&item.RouteID,
		&item.ProviderID,
		&item.Model,
		&item.Action,
		&item.ProtocolFlow,
		&item.Path,
		&item.Status,
		&item.InputTokens,
		&item.OutputTokens,
		&item.CacheTokens,
		&item.CacheWriteTokens,
		&item.CostMicros,
		&pricedValue,
		&item.LatencyMillis,
		&ttft,
		&prep,
		&preUp,
		&upTTFB,
		&overhead,
		&convertOut,
		&post,
		&timingFlags,
		&clientHost,
		&clientIP,
		&accessSource,
		&reasoning,
		&ingress,
		&egress,
		&peerGateway,
		&peerDirection,
		&peerOrigin,
		&peerPath,
		&item.ErrorDescription,
		&analysis,
		&item.RequestBody,
		&item.ResponseBody,
	); err != nil {
		return monitor.RequestLog{}, err
	}
	if err := json.Unmarshal([]byte(analysis), &item.Analysis); err != nil {
		return monitor.RequestLog{}, fmt.Errorf("decode request analysis: %w", err)
	}
	item.Priced = pricedValue != 0
	applyRequestLogScan(&item, timeValue, ttft, prep, preUp, upTTFB, overhead, convertOut, post, timingFlags, clientHost, clientIP, accessSource, reasoning, ingress, egress, peerGateway, peerDirection, peerOrigin, peerPath)
	return item, nil
}

func scanRequestLogSummary(rows rowScanner) (monitor.RequestLog, error) {
	var item monitor.RequestLog
	var timeValue, analysis string
	var ttft, prep, preUp, upTTFB, overhead, convertOut, post sql.NullInt64
	var timingFlags, clientHost, clientIP, accessSource, reasoning, ingress, egress, peerGateway, peerDirection, peerOrigin, peerPath sql.NullString
	var pricedValue int
	if err := rows.Scan(
		&item.ID,
		&timeValue,
		&item.APIKeyID,
		&item.APIKeyName,
		&item.RouteID,
		&item.ProviderID,
		&item.Model,
		&item.Action,
		&item.ProtocolFlow,
		&item.Path,
		&item.Status,
		&item.InputTokens,
		&item.OutputTokens,
		&item.CacheTokens,
		&item.CacheWriteTokens,
		&item.CostMicros,
		&pricedValue,
		&item.LatencyMillis,
		&ttft,
		&prep,
		&preUp,
		&upTTFB,
		&overhead,
		&convertOut,
		&post,
		&timingFlags,
		&clientHost,
		&clientIP,
		&accessSource,
		&reasoning,
		&ingress,
		&egress,
		&peerGateway,
		&peerDirection,
		&peerOrigin,
		&peerPath,
		&item.ErrorDescription,
		&analysis,
	); err != nil {
		return monitor.RequestLog{}, err
	}
	if err := json.Unmarshal([]byte(analysis), &item.Analysis); err != nil {
		return monitor.RequestLog{}, fmt.Errorf("decode request analysis: %w", err)
	}
	item.Priced = pricedValue != 0
	applyRequestLogScan(&item, timeValue, ttft, prep, preUp, upTTFB, overhead, convertOut, post, timingFlags, clientHost, clientIP, accessSource, reasoning, ingress, egress, peerGateway, peerDirection, peerOrigin, peerPath)
	return item, nil
}

func applyRequestLogScan(
	item *monitor.RequestLog,
	timeValue string,
	ttft, prep, preUp, upTTFB, overhead, convertOut, post sql.NullInt64,
	timingFlags, clientHost, clientIP, accessSource, reasoning, ingress, egress, peerGateway, peerDirection, peerOrigin, peerPath sql.NullString,
) {
	parsed, parseErr := time.Parse(time.RFC3339Nano, timeValue)
	if parseErr != nil {
		parsed, parseErr = time.Parse(time.RFC3339, timeValue)
	}
	if parseErr == nil {
		item.Time = parsed
	}
	if ttft.Valid {
		item.TTFTMillis = ttft.Int64
	}
	if prep.Valid {
		item.PrepMillis = prep.Int64
	}
	if preUp.Valid {
		item.PreUpstreamMillis = preUp.Int64
	}
	if upTTFB.Valid {
		item.UpstreamTTFBMillis = upTTFB.Int64
	}
	if overhead.Valid {
		item.GatewayOverheadMillis = overhead.Int64
	}
	if convertOut.Valid {
		item.ConvertOutMillis = convertOut.Int64
	}
	if post.Valid {
		item.PostMillis = post.Int64
	}
	if timingFlags.Valid {
		item.TimingFlags = timingFlags.String
	}
	if clientHost.Valid {
		item.ClientHost = clientHost.String
	}
	if clientIP.Valid {
		item.ClientIP = clientIP.String
	}
	if accessSource.Valid {
		item.AccessSource = accessSource.String
	}
	if reasoning.Valid {
		item.ReasoningEffort = reasoning.String
	}
	if ingress.Valid {
		item.Ingress = ingress.String
	}
	if egress.Valid {
		item.Egress = egress.String
	}
	if peerGateway.Valid {
		item.PeerGatewayID = peerGateway.String
	}
	if peerDirection.Valid {
		item.PeerDirection = peerDirection.String
	}
	if peerOrigin.Valid {
		item.PeerOrigin = peerOrigin.String
	}
	if peerPath.Valid {
		item.PeerPath = peerPath.String
	}
}

func ensureRequestLogsTable(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS request_logs (
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
		cache_write_tokens INTEGER NOT NULL DEFAULT 0,
		cost_micros INTEGER NOT NULL DEFAULT 0,
		priced INTEGER NOT NULL DEFAULT 0,
		latency_ms INTEGER NOT NULL DEFAULT 0,
		ttft_ms INTEGER NOT NULL DEFAULT 0,
		client_host TEXT NOT NULL DEFAULT '',
		client_ip TEXT NOT NULL DEFAULT '',
		access_source TEXT NOT NULL DEFAULT '',
		ingress TEXT NOT NULL DEFAULT '',
		egress TEXT NOT NULL DEFAULT '',
		peer_gateway_id TEXT NOT NULL DEFAULT '',
		peer_direction TEXT NOT NULL DEFAULT '',
		peer_origin TEXT NOT NULL DEFAULT '',
		peer_path TEXT NOT NULL DEFAULT '',
		error_description TEXT NOT NULL DEFAULT '',
		request_body TEXT NOT NULL DEFAULT '',
		response_body TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		return err
	}
	for _, col := range []struct {
		name string
		def  string
	}{
		{"ttft_ms", "INTEGER NOT NULL DEFAULT 0"},
		{"prep_ms", "INTEGER NOT NULL DEFAULT 0"},
		{"pre_upstream_ms", "INTEGER NOT NULL DEFAULT 0"},
		{"upstream_ttfb_ms", "INTEGER NOT NULL DEFAULT 0"},
		{"gateway_overhead_ms", "INTEGER NOT NULL DEFAULT 0"},
		{"convert_out_ms", "INTEGER NOT NULL DEFAULT 0"},
		{"post_ms", "INTEGER NOT NULL DEFAULT 0"},
		{"timing_flags", "TEXT NOT NULL DEFAULT ''"},
		{"client_host", "TEXT NOT NULL DEFAULT ''"},
		{"client_ip", "TEXT NOT NULL DEFAULT ''"},
		{"access_source", "TEXT NOT NULL DEFAULT ''"},
		{"reasoning_effort", "TEXT NOT NULL DEFAULT ''"},
		{"cache_write_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"cost_micros", "INTEGER NOT NULL DEFAULT 0"},
		{"priced", "INTEGER NOT NULL DEFAULT 0"},
		{"analysis_json", "TEXT NOT NULL DEFAULT '{}'"},
		{"ingress", "TEXT NOT NULL DEFAULT ''"},
		{"egress", "TEXT NOT NULL DEFAULT ''"},
		{"peer_gateway_id", "TEXT NOT NULL DEFAULT ''"},
		{"peer_direction", "TEXT NOT NULL DEFAULT ''"},
		{"peer_origin", "TEXT NOT NULL DEFAULT ''"},
		{"peer_path", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := addColumnIfMissing(tx, "request_logs", col.name, col.def); err != nil {
			return fmt.Errorf("request_logs.%s: %w", col.name, err)
		}
	}
	_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_request_logs_time ON request_logs(time DESC, id DESC)`)
	if err != nil {
		return err
	}
	// 父子网关的对端用量是"按 peer_gateway_id 在一片时间窗里 GROUP BY"。
	// 纯本地流量的这一列是空串，索引条目很少，不会拖慢平时的写入；不建索引
	// 的话控制台每次查子网关用量都要全表扫，日志上百万行时直接不可用。
	_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_request_logs_peer_time ON request_logs(peer_gateway_id, time DESC, id DESC) WHERE peer_gateway_id <> ''`)
	return err
}
