package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// UnpricedRequestRow 表示一条尚未计价（priced = 0）的请求日志记录，供补算成本时读取。
type UnpricedRequestRow struct {
	ID               int64
	Time             time.Time
	APIKeyID         string
	APIKeyName       string
	RouteID          string
	ProviderID       string
	Model            string
	ProtocolFlow     string
	InputTokens      int64
	OutputTokens     int64
	CacheTokens      int64
	CacheWriteTokens int64
	PeerGatewayID    string
	PeerDirection    string
}

// PricedRequestResolution 是上层对一条未计价请求解析模型/用户归属并按当前价格表计价后的结果。
type PricedRequestResolution struct {
	Day            string
	KeyID          string
	UserID         string
	ProviderID     string
	ModelBucket    string
	OutputProtocol string
	PeerGatewayID  string
	CostMicros     int64
	Priced         bool
}

// CostBackfillResult 汇总一次历史未计价请求补算的更新条数与总金额。
type CostBackfillResult struct {
	Updated    int64 `json:"updated"`
	CostMicros int64 `json:"costMicros"`
}

type bucketCostDelta struct {
	day        string
	bucketType string
	bucketID   string
	costMicros int64
	pricedCnt  int64
}

// BackfillUnpricedCosts 在单个 SQLite 事务内按当前价格表补算 [from, to) 区间内
// priced = 0 的历史请求日志，并同步把新增的 cost_micros 与 priced_count 精确累加进
// usage_daily_buckets 对应各维度桶；已计价（priced = 1）的历史记录绝不覆盖。
func (s *Store) BackfillUnpricedCosts(from, to time.Time, resolve func(UnpricedRequestRow) PricedRequestResolution) (CostBackfillResult, error) {
	if resolve == nil {
		return CostBackfillResult{}, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return CostBackfillResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	// 用 ±1 秒宽松边界先做索引筛选，再在 Go 侧按精确时间点 [from, to) 过滤，
	// 避免 RFC3339Nano 整秒结尾 'Z' 与带毫秒 '.' 在 ASCII 字典序上的边界偏差。
	where := []string{"priced = 0"}
	args := make([]any, 0, 2)
	if !from.IsZero() {
		where = append(where, "time >= ?")
		args = append(args, from.UTC().Add(-time.Second).Format(time.RFC3339Nano))
	}
	if !to.IsZero() {
		where = append(where, "time < ?")
		args = append(args, to.UTC().Add(time.Second).Format(time.RFC3339Nano))
	}

	query := `SELECT id, time, api_key_id, api_key_name, route_id, provider_id, model, protocol_flow,
		input_tokens, output_tokens, cache_tokens, cache_write_tokens, peer_gateway_id, peer_direction
		FROM request_logs WHERE ` + strings.Join(where, " AND ") + ` ORDER BY id ASC`

	rows, err := tx.Query(query, args...)
	if err != nil {
		return CostBackfillResult{}, fmt.Errorf("query unpriced request_logs: %w", err)
	}
	candidates := make([]UnpricedRequestRow, 0, 128)
	for rows.Next() {
		var (
			row     UnpricedRequestRow
			rawTime string
		)
		if err := rows.Scan(
			&row.ID, &rawTime, &row.APIKeyID, &row.APIKeyName, &row.RouteID, &row.ProviderID,
			&row.Model, &row.ProtocolFlow, &row.InputTokens, &row.OutputTokens,
			&row.CacheTokens, &row.CacheWriteTokens, &row.PeerGatewayID, &row.PeerDirection,
		); err != nil {
			_ = rows.Close()
			return CostBackfillResult{}, err
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, rawTime)
		if parseErr != nil {
			parsed, parseErr = time.Parse(time.RFC3339, rawTime)
		}
		if parseErr != nil {
			continue
		}
		if !from.IsZero() && parsed.Before(from) {
			continue
		}
		if !to.IsZero() && !parsed.Before(to) {
			continue
		}
		row.Time = parsed
		candidates = append(candidates, row)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return CostBackfillResult{}, err
	}
	if err := rows.Close(); err != nil {
		return CostBackfillResult{}, err
	}

	if len(candidates) == 0 {
		return CostBackfillResult{}, nil
	}

	updateStmt, err := tx.Prepare(`UPDATE request_logs SET cost_micros = ?, priced = 1 WHERE id = ? AND priced = 0`)
	if err != nil {
		return CostBackfillResult{}, err
	}
	defer func() { _ = updateStmt.Close() }()

	deltas := make(map[string]*bucketCostDelta)
	addBucket := func(day, bucketType, bucketID string, costMicros int64) {
		key := day + "\x00" + bucketType + "\x00" + bucketID
		entry, ok := deltas[key]
		if !ok {
			entry = &bucketCostDelta{day: day, bucketType: bucketType, bucketID: bucketID}
			deltas[key] = entry
		}
		entry.costMicros += costMicros
		entry.pricedCnt++
	}

	var result CostBackfillResult
	for _, row := range candidates {
		res := resolve(row)
		if !res.Priced {
			continue
		}
		execRes, err := updateStmt.Exec(res.CostMicros, row.ID)
		if err != nil {
			return CostBackfillResult{}, err
		}
		affected, err := execRes.RowsAffected()
		if err != nil {
			return CostBackfillResult{}, err
		}
		if affected == 0 {
			continue
		}
		result.Updated++
		result.CostMicros += res.CostMicros

		day := res.Day
		if day == "" {
			day = row.Time.Local().Format("2006-01-02")
		}
		addBucket(day, "total", "", res.CostMicros)
		addBucket(day, "api_key", res.KeyID, res.CostMicros)
		addBucket(day, "provider", res.ProviderID, res.CostMicros)
		addBucket(day, "user", res.UserID, res.CostMicros)
		if res.ModelBucket != "" {
			addBucket(day, "model", res.ModelBucket, res.CostMicros)
		}
		if res.ProviderID != "" && res.ModelBucket != "" {
			if idBytes, err := json.Marshal([]string{res.ProviderID, res.ModelBucket}); err == nil {
				addBucket(day, "provider_model", string(idBytes), res.CostMicros)
			}
		}
		if res.OutputProtocol != "" {
			addBucket(day, "output_protocol", res.OutputProtocol, res.CostMicros)
		}
		if res.PeerGatewayID != "" {
			addBucket(day, "downstream_gateway", res.PeerGatewayID, res.CostMicros)
		}
		for _, dim := range []struct{ kind, id string }{
			{"key_provider", res.ProviderID},
			{"key_model", res.ModelBucket},
			{"key_protocol", res.OutputProtocol},
			{"key_downstream_gateway", res.PeerGatewayID},
		} {
			if dim.id == "" {
				continue
			}
			if idBytes, err := json.Marshal([]string{res.KeyID, dim.id}); err == nil {
				addBucket(day, dim.kind, string(idBytes), res.CostMicros)
			}
		}
		if res.ProviderID != "" && res.ModelBucket != "" {
			if idBytes, err := json.Marshal([]string{res.KeyID, res.ProviderID, res.ModelBucket}); err == nil {
				addBucket(day, "key_provider_model", string(idBytes), res.CostMicros)
			}
		}
	}

	if len(deltas) > 0 {
		bucketStmt, err := tx.Prepare(`UPDATE usage_daily_buckets
			SET cost_micros = cost_micros + ?, priced_count = priced_count + ?
			WHERE day = ? AND bucket_type = ? AND bucket_id = ?`)
		if err != nil {
			return CostBackfillResult{}, err
		}
		defer func() { _ = bucketStmt.Close() }()
		for _, d := range deltas {
			if _, err := bucketStmt.Exec(d.costMicros, d.pricedCnt, d.day, d.bucketType, d.bucketID); err != nil {
				return CostBackfillResult{}, err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return CostBackfillResult{}, err
	}
	return result, nil
}
