package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

const (
	requestLogSafetyCap     = 50000
	defaultRequestLogDays   = 7
	defaultRequestLogPage   = 100
	maxRequestLogPageSize   = 500
)

func (s *Store) AppendRequestLog(log monitor.RequestLog) error {
	return s.AppendRequestLogWithRetention(log, defaultRequestLogDays)
}

func (s *Store) AppendRequestLogWithRetention(log monitor.RequestLog, retentionDays int) error {
	if log.Time.IsZero() {
		log.Time = time.Now()
	}
	_, err := s.db.Exec(`INSERT INTO request_logs (
		time, api_key_id, api_key_name, route_id, provider_id, model, action, protocol_flow, path,
		status, input_tokens, output_tokens, cache_tokens, latency_ms, ttft_ms, client_host, client_ip, access_source,
		error_description, request_body, response_body
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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
		log.LatencyMillis,
		log.TTFTMillis,
		log.ClientHost,
		log.ClientIP,
		log.AccessSource,
		log.ErrorDescription,
		log.RequestBody,
		log.ResponseBody,
	)
	if err != nil {
		return err
	}
	return s.PruneRequestLogs(retentionDays)
}

func (s *Store) PruneRequestLogs(retentionDays int) error {
	if retentionDays <= 0 {
		retentionDays = defaultRequestLogDays
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339Nano)
	if _, err := s.db.Exec(`DELETE FROM request_logs WHERE time < ?`, cutoff); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM request_logs WHERE id NOT IN (
		SELECT id FROM request_logs ORDER BY time DESC, id DESC LIMIT ?
	)`, requestLogSafetyCap)
	return err
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
	case "4xx":
		where = append(where, "status >= 400 AND status < 500")
	case "5xx":
		where = append(where, "status >= 500 AND status < 600")
	}
	if keyName := strings.TrimSpace(query.APIKeyName); keyName != "" {
		where = append(where, "LOWER(api_key_name) LIKE ?")
		args = append(args, "%"+strings.ToLower(keyName)+"%")
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

	whereSQL := strings.Join(where, " AND ")
	var total int
	countArgs := append([]any{}, args...)
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM request_logs WHERE `+whereSQL, countArgs...).Scan(&total); err != nil {
		return monitor.RequestLogPage{}, err
	}

	offset := (page - 1) * pageSize
	args = append(args, pageSize, offset)
	selectCols := `time, api_key_id, api_key_name, route_id, provider_id, model, action, protocol_flow, path,
		status, input_tokens, output_tokens, cache_tokens, latency_ms, ttft_ms, client_host, client_ip, access_source,
		error_description`
	if query.IncludeBodies {
		selectCols += `, request_body, response_body`
	}
	rows, err := s.db.Query(`SELECT `+selectCols+`
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

func scanRequestLog(rows *sql.Rows) (monitor.RequestLog, error) {
	var item monitor.RequestLog
	var timeValue string
	var ttft sql.NullInt64
	var clientHost, clientIP, accessSource sql.NullString
	if err := rows.Scan(
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
		&item.LatencyMillis,
		&ttft,
		&clientHost,
		&clientIP,
		&accessSource,
		&item.ErrorDescription,
		&item.RequestBody,
		&item.ResponseBody,
	); err != nil {
		return monitor.RequestLog{}, err
	}
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
	if clientHost.Valid {
		item.ClientHost = clientHost.String
	}
	if clientIP.Valid {
		item.ClientIP = clientIP.String
	}
	if accessSource.Valid {
		item.AccessSource = accessSource.String
	}
	return item, nil
}

func scanRequestLogSummary(rows *sql.Rows) (monitor.RequestLog, error) {
	var item monitor.RequestLog
	var timeValue string
	var ttft sql.NullInt64
	var clientHost, clientIP, accessSource sql.NullString
	if err := rows.Scan(
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
		&item.LatencyMillis,
		&ttft,
		&clientHost,
		&clientIP,
		&accessSource,
		&item.ErrorDescription,
	); err != nil {
		return monitor.RequestLog{}, err
	}
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
	if clientHost.Valid {
		item.ClientHost = clientHost.String
	}
	if clientIP.Valid {
		item.ClientIP = clientIP.String
	}
	if accessSource.Valid {
		item.AccessSource = accessSource.String
	}
	return item, nil
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
		latency_ms INTEGER NOT NULL DEFAULT 0,
		ttft_ms INTEGER NOT NULL DEFAULT 0,
		client_host TEXT NOT NULL DEFAULT '',
		client_ip TEXT NOT NULL DEFAULT '',
		access_source TEXT NOT NULL DEFAULT '',
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
		{"client_host", "TEXT NOT NULL DEFAULT ''"},
		{"client_ip", "TEXT NOT NULL DEFAULT ''"},
		{"access_source", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := addColumnIfMissing(tx, "request_logs", col.name, col.def); err != nil {
			return fmt.Errorf("request_logs.%s: %w", col.name, err)
		}
	}
	_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_request_logs_time ON request_logs(time DESC, id DESC)`)
	return err
}
