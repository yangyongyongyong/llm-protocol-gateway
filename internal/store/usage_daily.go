package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

const usageLastRequestSettingKey = "usage_last_request"

func ensureUsageDailyTables(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS usage_daily_buckets (
		day TEXT NOT NULL,
		bucket_type TEXT NOT NULL,
		bucket_id TEXT NOT NULL DEFAULT '',
		bucket_name TEXT NOT NULL DEFAULT '',
		request_count INTEGER NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		cache_tokens INTEGER NOT NULL DEFAULT 0,
		status_2xx INTEGER NOT NULL DEFAULT 0,
		status_4xx INTEGER NOT NULL DEFAULT 0,
		status_5xx INTEGER NOT NULL DEFAULT 0,
		status_other INTEGER NOT NULL DEFAULT 0,
		latency_sum INTEGER NOT NULL DEFAULT 0,
		ttft_sum INTEGER NOT NULL DEFAULT 0,
		ttft_count INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (day, bucket_type, bucket_id)
	)`)
	if err != nil {
		return err
	}
	// rx_bytes / tx_bytes were added with the per-day traffic chart; existing
	// installs get them via ALTER TABLE rather than a table rebuild.
	// cache_write_tokens / cost_micros / priced_count join them: a day aggregated
	// before they existed reads back as 0, which the console reports as 未计价
	// instead of a free day.
	for _, col := range []string{"rx_bytes", "tx_bytes", "cache_write_tokens", "cost_micros", "priced_count"} {
		if err := addColumnIfMissing(tx, "usage_daily_buckets", col, "INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("usage_daily_buckets.%s: %w", col, err)
		}
	}
	_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_usage_daily_buckets_day ON usage_daily_buckets(day)`)
	return err
}

func (s *Store) ApplyUsageDelta(delta monitor.UsagePersistDelta) error {
	return s.ApplyUsageDeltas([]monitor.UsagePersistDelta{delta})
}

// ApplyUsageDeltas persists several deltas in a single transaction/commit.
// The async usage worker (internal/monitor/usage_accumulator.go) batches the
// deltas it accumulates within a short window onto one call here instead of
// calling ApplyUsageDelta per request, so sustained traffic produces one WAL
// commit (and fsync) per batch rather than one per request.
func (s *Store) ApplyUsageDeltas(deltas []monitor.UsagePersistDelta) error {
	if len(deltas) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var lastRequest *monitor.RequestLog
	for _, delta := range deltas {
		if err := applyUsageDeltaTx(tx, delta); err != nil {
			return err
		}
		if delta.LastRequest != nil {
			lastRequest = delta.LastRequest
		}
	}

	if lastRequest != nil {
		payload, err := json.Marshal(lastRequest)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, usageLastRequestSettingKey, string(payload)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// BackfillUsageKeyDimensions preserves permanent totals and byte counters while
// replacing only dimensions recoverable from retained request logs. Downstream
// gateway dimensions joined that set in schema v4, so a one-time replay also
// rebuilds them without touching the authoritative day totals.
func (s *Store) BackfillUsageKeyDimensions(since time.Time, deltas []monitor.UsagePersistDelta) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	sinceDay := since.Local().Format("2006-01-02")
	rows, err := tx.Query(`SELECT day, request_count FROM usage_daily_buckets WHERE bucket_type = 'total'`)
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	counts := map[string]int64{}
	for _, delta := range deltas {
		counts[delta.Day]++
	}
	for rows.Next() {
		var day string
		var count int64
		if err := rows.Scan(&day, &count); err != nil {
			rows.Close()
			return err
		}
		existing[day] = true
		if day >= sinceDay && counts[day] != count {
			// A partial log day cannot safely replace existing key dimensions.
			delete(counts, day)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for day := range counts {
		if day < sinceDay {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM usage_daily_buckets WHERE day = ? AND bucket_type IN ('key_provider', 'key_model', 'key_protocol', 'key_downstream_gateway', 'downstream_gateway')`, day); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE usage_daily_buckets SET status_2xx=0, status_4xx=0, status_5xx=0, status_other=0, latency_sum=0, ttft_sum=0, ttft_count=0 WHERE day = ? AND bucket_type='api_key'`, day); err != nil {
			return err
		}
	}
	for _, delta := range deltas {
		if _, complete := counts[delta.Day]; !complete || delta.Day < sinceDay {
			continue
		}
		if !existing[delta.Day] {
			if err := applyUsageDeltaTx(tx, delta); err != nil {
				return err
			}
			continue
		}
		if err := upsertUsageKeyDimensionsTx(tx, delta); err != nil {
			return err
		}
		if err := upsertUsageDownstreamTx(tx, delta); err != nil {
			return err
		}
		var s2, s4, s5, other, ttftCount int64
		switch delta.StatusClass {
		case "2xx":
			s2 = 1
		case "4xx":
			s4 = 1
		case "5xx":
			s5 = 1
		default:
			other = 1
		}
		if delta.TTFTMs > 0 {
			ttftCount = 1
		}
		if _, err := tx.Exec(`UPDATE usage_daily_buckets SET status_2xx=status_2xx+?, status_4xx=status_4xx+?, status_5xx=status_5xx+?, status_other=status_other+?, latency_sum=latency_sum+?, ttft_sum=ttft_sum+?, ttft_count=ttft_count+? WHERE day=? AND bucket_type='api_key' AND bucket_id=?`, s2, s4, s5, other, delta.LatencyMs, delta.TTFTMs, ttftCount, delta.Day, delta.KeyID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func applyUsageDeltaTx(tx *sql.Tx, delta monitor.UsagePersistDelta) error {
	if err := upsertUsageBucket(tx, delta.Day, "total", "", "", delta, true); err != nil {
		return err
	}
	if err := upsertUsageBucket(tx, delta.Day, "api_key", delta.KeyID, delta.KeyName, delta, true); err != nil {
		return err
	}
	if err := upsertUsageBucket(tx, delta.Day, "provider", delta.ProviderID, "", delta, false); err != nil {
		return err
	}
	if err := upsertUsageBucket(tx, delta.Day, "user", delta.UserID, "", delta, false); err != nil {
		return err
	}
	if delta.Model != "" {
		if err := upsertUsageBucket(tx, delta.Day, "model", delta.Model, "", delta, false); err != nil {
			return err
		}
	}
	if delta.OutputProtocol != "" {
		if err := upsertUsageBucket(tx, delta.Day, "output_protocol", delta.OutputProtocol, "", delta, false); err != nil {
			return err
		}
	}
	if err := upsertUsageDownstreamTx(tx, delta); err != nil {
		return err
	}
	return upsertUsageKeyDimensionsTx(tx, delta)
}

// upsertUsageDownstreamTx 写全量下游网关桶，供管理员查看所有下游；Key
// 维度桶由 upsertUsageKeyDimensionsTx 一并写入，供普通用户按名下 Key 隔离。
// 只有 ingress 事件带 PeerGatewayID，因此本机转发到父网关的 egress 流量
// 永远不会混进“下游用量”。
func upsertUsageDownstreamTx(tx *sql.Tx, delta monitor.UsagePersistDelta) error {
	gatewayID := delta.PeerGatewayID
	if gatewayID == "" {
		return nil
	}
	return upsertUsageBucket(tx, delta.Day, "downstream_gateway", gatewayID, "", delta, false)
}

func upsertUsageKeyDimensionsTx(tx *sql.Tx, delta monitor.UsagePersistDelta) error {
	for _, dimension := range []struct{ kind, id string }{{"key_provider", delta.ProviderID}, {"key_model", delta.Model}, {"key_protocol", delta.OutputProtocol}, {"key_downstream_gateway", delta.PeerGatewayID}} {
		if dimension.id == "" {
			continue
		}
		identity, err := json.Marshal([]string{delta.KeyID, dimension.id})
		if err != nil {
			return err
		}
		if err := upsertUsageBucket(tx, delta.Day, dimension.kind, string(identity), "", delta, false); err != nil {
			return err
		}
	}
	return nil
}

func upsertUsageBucket(tx *sql.Tx, day, bucketType, bucketID, bucketName string, delta monitor.UsagePersistDelta, withStatus bool) error {
	status2xx, status4xx, status5xx, statusOther := int64(0), int64(0), int64(0), int64(0)
	latencySum, ttftSum, ttftCount := int64(0), int64(0), int64(0)
	if withStatus {
		switch delta.StatusClass {
		case "2xx":
			status2xx = 1
		case "4xx":
			status4xx = 1
		case "5xx":
			status5xx = 1
		default:
			statusOther = 1
		}
		latencySum = delta.LatencyMs
		if delta.TTFTMs > 0 {
			ttftSum = delta.TTFTMs
			ttftCount = 1
		}
	}
	_, err := tx.Exec(`INSERT INTO usage_daily_buckets (
		day, bucket_type, bucket_id, bucket_name,
		request_count, input_tokens, output_tokens, cache_tokens, cache_write_tokens, cost_micros, priced_count,
		status_2xx, status_4xx, status_5xx, status_other,
		latency_sum, ttft_sum, ttft_count, rx_bytes, tx_bytes
	) VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(day, bucket_type, bucket_id) DO UPDATE SET
		bucket_name = CASE WHEN excluded.bucket_name != '' THEN excluded.bucket_name ELSE usage_daily_buckets.bucket_name END,
		request_count = usage_daily_buckets.request_count + 1,
		input_tokens = usage_daily_buckets.input_tokens + excluded.input_tokens,
		output_tokens = usage_daily_buckets.output_tokens + excluded.output_tokens,
		cache_tokens = usage_daily_buckets.cache_tokens + excluded.cache_tokens,
		cache_write_tokens = usage_daily_buckets.cache_write_tokens + excluded.cache_write_tokens,
		cost_micros = usage_daily_buckets.cost_micros + excluded.cost_micros,
		priced_count = usage_daily_buckets.priced_count + excluded.priced_count,
		status_2xx = usage_daily_buckets.status_2xx + excluded.status_2xx,
		status_4xx = usage_daily_buckets.status_4xx + excluded.status_4xx,
		status_5xx = usage_daily_buckets.status_5xx + excluded.status_5xx,
		status_other = usage_daily_buckets.status_other + excluded.status_other,
		latency_sum = usage_daily_buckets.latency_sum + excluded.latency_sum,
		ttft_sum = usage_daily_buckets.ttft_sum + excluded.ttft_sum,
		ttft_count = usage_daily_buckets.ttft_count + excluded.ttft_count,
		rx_bytes = usage_daily_buckets.rx_bytes + excluded.rx_bytes,
		tx_bytes = usage_daily_buckets.tx_bytes + excluded.tx_bytes`,
		day, bucketType, bucketID, bucketName,
		delta.InputTokens, delta.OutputTokens, delta.CacheTokens, delta.CacheWriteTokens, delta.CostMicros, delta.PricedCount,
		status2xx, status4xx, status5xx, statusOther,
		latencySum, ttftSum, ttftCount, delta.RxBytes, delta.TxBytes,
	)
	return err
}

func (s *Store) LoadUsageSince(since time.Time) (map[string]monitor.UsageDayBuckets, *monitor.RequestLog, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if since.IsZero() {
		rows, err = s.reader().Query(`SELECT day, bucket_type, bucket_id, bucket_name,
			request_count, input_tokens, output_tokens, cache_tokens, cache_write_tokens, cost_micros, priced_count,
			status_2xx, status_4xx, status_5xx, status_other,
			latency_sum, ttft_sum, ttft_count, rx_bytes, tx_bytes
			FROM usage_daily_buckets ORDER BY day`)
	} else {
		sinceDay := since.Local().Format("2006-01-02")
		rows, err = s.reader().Query(`SELECT day, bucket_type, bucket_id, bucket_name,
			request_count, input_tokens, output_tokens, cache_tokens, cache_write_tokens, cost_micros, priced_count,
			status_2xx, status_4xx, status_5xx, status_other,
			latency_sum, ttft_sum, ttft_count, rx_bytes, tx_bytes
			FROM usage_daily_buckets WHERE day >= ? ORDER BY day`, sinceDay)
	}
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	out := make(map[string]monitor.UsageDayBuckets)
	for rows.Next() {
		var (
			day, bucketType, bucketID, bucketName string
			reqCount, inTok, outTok, cacheTok     int64
			cacheWriteTok, costMicros             int64
			pricedCount                           int64
			s2xx, s4xx, s5xx, sOther              int64
			latSum, ttftSum, ttftCount            int64
			rxBytes, txBytes                      int64
		)
		if err := rows.Scan(&day, &bucketType, &bucketID, &bucketName, &reqCount, &inTok, &outTok, &cacheTok,
			&cacheWriteTok, &costMicros, &pricedCount,
			&s2xx, &s4xx, &s5xx, &sOther, &latSum, &ttftSum, &ttftCount, &rxBytes, &txBytes); err != nil {
			return nil, nil, err
		}
		dayBuckets, ok := out[day]
		if !ok {
			dayBuckets = monitor.UsageDayBuckets{
				ByKey:               make(map[string]monitor.UsageDayBuckets),
				ByAPIKey:            make(map[string]monitor.APIKeyDayStats),
				ByProvider:          make(map[string]monitor.ProviderDayStats),
				ByModel:             make(map[string]monitor.ModelDayStats),
				ByUser:              make(map[string]monitor.UserDayStats),
				ByProtocol:          make(map[string]monitor.ProtocolDayStats),
				ByDownstreamGateway: make(map[string]monitor.DownstreamGatewayDayStats),
			}
		}
		stats := monitor.APIKeyDayStats{
			RequestCount:     reqCount,
			InputTokens:      inTok,
			OutputTokens:     outTok,
			CacheTokens:      cacheTok,
			CacheWriteTokens: cacheWriteTok,
			CostMicros:       costMicros,
			PricedCount:      pricedCount,
			RxBytes:          rxBytes,
			TxBytes:          txBytes,
		}
		switch bucketType {
		case "total":
			dayBuckets.Total = stats
			dayBuckets.Status2xx = s2xx
			dayBuckets.Status4xx = s4xx
			dayBuckets.Status5xx = s5xx
			dayBuckets.StatusOther = sOther
			dayBuckets.LatencySum = latSum
			dayBuckets.TTFTSum = ttftSum
			dayBuckets.TTFTCount = ttftCount
		case "api_key":
			stats.APIKeyID = bucketID
			stats.APIKeyName = bucketName
			dayBuckets.ByAPIKey[bucketID] = stats
			child := dayBuckets.ByKey[bucketID]
			child.Total = stats
			child.Status2xx = s2xx
			child.Status4xx = s4xx
			child.Status5xx = s5xx
			child.StatusOther = sOther
			child.LatencySum = latSum
			child.TTFTSum = ttftSum
			child.TTFTCount = ttftCount
			dayBuckets.ByKey[bucketID] = child
		case "key_provider", "key_model", "key_protocol", "key_downstream_gateway":
			var identity []string
			if err := json.Unmarshal([]byte(bucketID), &identity); err != nil || len(identity) != 2 {
				return nil, nil, fmt.Errorf("invalid usage key dimension %q", bucketID)
			}
			child := dayBuckets.ByKey[identity[0]]
			id := identity[1]
			switch bucketType {
			case "key_provider":
				if child.ByProvider == nil {
					child.ByProvider = make(map[string]monitor.ProviderDayStats)
				}
				child.ByProvider[id] = monitor.ProviderDayStats{
					ProviderID: id, RequestCount: reqCount, InputTokens: inTok, OutputTokens: outTok,
					CacheTokens: cacheTok, CacheWriteTokens: cacheWriteTok, CostMicros: costMicros, PricedCount: pricedCount,
				}
			case "key_model":
				if child.ByModel == nil {
					child.ByModel = make(map[string]monitor.ModelDayStats)
				}
				child.ByModel[id] = monitor.ModelDayStats{
					Model: id, RequestCount: reqCount, InputTokens: inTok, OutputTokens: outTok,
					CacheTokens: cacheTok, CacheWriteTokens: cacheWriteTok, CostMicros: costMicros, PricedCount: pricedCount,
				}
			case "key_protocol":
				if child.ByProtocol == nil {
					child.ByProtocol = make(map[string]monitor.ProtocolDayStats)
				}
				child.ByProtocol[id] = monitor.ProtocolDayStats{
					Protocol: id, RequestCount: reqCount, InputTokens: inTok, OutputTokens: outTok,
					CacheTokens: cacheTok, CacheWriteTokens: cacheWriteTok, CostMicros: costMicros, PricedCount: pricedCount,
				}
			case "key_downstream_gateway":
				if child.ByDownstreamGateway == nil {
					child.ByDownstreamGateway = make(map[string]monitor.DownstreamGatewayDayStats)
				}
				child.ByDownstreamGateway[id] = monitor.DownstreamGatewayDayStats{
					GatewayID: id, RequestCount: reqCount, InputTokens: inTok, OutputTokens: outTok,
					CacheTokens: cacheTok, CacheWriteTokens: cacheWriteTok, CostMicros: costMicros, PricedCount: pricedCount,
				}
			}
			dayBuckets.ByKey[identity[0]] = child
		case "downstream_gateway":
			dayBuckets.ByDownstreamGateway[bucketID] = monitor.DownstreamGatewayDayStats{
				GatewayID:        bucketID,
				RequestCount:     reqCount,
				InputTokens:      inTok,
				OutputTokens:     outTok,
				CacheTokens:      cacheTok,
				CacheWriteTokens: cacheWriteTok,
				CostMicros:       costMicros,
				PricedCount:      pricedCount,
			}
		case "provider":
			dayBuckets.ByProvider[bucketID] = monitor.ProviderDayStats{
				ProviderID:       bucketID,
				RequestCount:     reqCount,
				InputTokens:      inTok,
				OutputTokens:     outTok,
				CacheTokens:      cacheTok,
				CacheWriteTokens: cacheWriteTok,
				CostMicros:       costMicros,
				PricedCount:      pricedCount,
			}
		case "model":
			dayBuckets.ByModel[bucketID] = monitor.ModelDayStats{
				Model:            bucketID,
				RequestCount:     reqCount,
				InputTokens:      inTok,
				OutputTokens:     outTok,
				CacheTokens:      cacheTok,
				CacheWriteTokens: cacheWriteTok,
				CostMicros:       costMicros,
				PricedCount:      pricedCount,
			}
		case "user":
			dayBuckets.ByUser[bucketID] = monitor.UserDayStats{
				UserID:           bucketID,
				RequestCount:     reqCount,
				InputTokens:      inTok,
				OutputTokens:     outTok,
				CacheTokens:      cacheTok,
				CacheWriteTokens: cacheWriteTok,
				CostMicros:       costMicros,
				PricedCount:      pricedCount,
			}
		case "output_protocol":
			dayBuckets.ByProtocol[bucketID] = monitor.ProtocolDayStats{
				Protocol:         bucketID,
				RequestCount:     reqCount,
				InputTokens:      inTok,
				OutputTokens:     outTok,
				CacheTokens:      cacheTok,
				CacheWriteTokens: cacheWriteTok,
				CostMicros:       costMicros,
				PricedCount:      pricedCount,
			}
		}
		out[day] = dayBuckets
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	var last *monitor.RequestLog
	var raw string
	if err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, usageLastRequestSettingKey).Scan(&raw); err == nil && raw != "" {
		var parsed monitor.RequestLog
		if json.Unmarshal([]byte(raw), &parsed) == nil {
			last = &parsed
		}
	}
	return out, last, nil
}

func (s *Store) PruneUsageBefore(cutoffDay time.Time) error {
	cutoff := cutoffDay.Local().Format("2006-01-02")
	if _, err := s.db.Exec(`DELETE FROM usage_daily_buckets WHERE day < ?`, cutoff); err != nil {
		return fmt.Errorf("prune usage_daily_buckets: %w", err)
	}
	return nil
}

// ClearUsageSince removes aggregates for day >= since so that window can be
// rebuilt from request logs, while older permanent daily rows stay intact.
func (s *Store) ClearUsageSince(since time.Time) error {
	if since.IsZero() {
		return s.ClearUsageDaily()
	}
	sinceDay := since.Local().Format("2006-01-02")
	if _, err := s.db.Exec(`DELETE FROM usage_daily_buckets WHERE day >= ?`, sinceDay); err != nil {
		return fmt.Errorf("clear usage_daily_buckets since %s: %w", sinceDay, err)
	}
	return nil
}

// ClearUsageDaily removes all persisted usage aggregates (before a full rebuild).
func (s *Store) ClearUsageDaily() error {
	if _, err := s.db.Exec(`DELETE FROM usage_daily_buckets`); err != nil {
		return err
	}
	// RTK savings are not reconstructible from request logs, but a full usage
	// reset should still clear them so the dashboard starts from a clean slate.
	if _, err := s.db.Exec(`DELETE FROM rtk_daily_buckets`); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM settings WHERE key = ?`, usageLastRequestSettingKey)
	return err
}

// CountRequestLogs returns total persisted request log rows (for rebuild validation).
func (s *Store) CountRequestLogs() (int64, error) {
	var count int64
	err := s.db.QueryRow(`SELECT COUNT(1) FROM request_logs`).Scan(&count)
	return count, err
}
