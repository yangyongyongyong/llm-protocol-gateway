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
	_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_usage_daily_buckets_day ON usage_daily_buckets(day)`)
	return err
}

func (s *Store) ApplyUsageDelta(delta monitor.UsagePersistDelta) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := upsertUsageBucket(tx, delta.Day, "total", "", "", delta, true); err != nil {
		return err
	}
	if err := upsertUsageBucket(tx, delta.Day, "api_key", delta.KeyID, delta.KeyName, delta, false); err != nil {
		return err
	}
	if err := upsertUsageBucket(tx, delta.Day, "provider", delta.ProviderID, "", delta, false); err != nil {
		return err
	}
	if delta.Model != "" {
		if err := upsertUsageBucket(tx, delta.Day, "model", delta.Model, "", delta, false); err != nil {
			return err
		}
	}

	if delta.LastRequest != nil {
		payload, err := json.Marshal(delta.LastRequest)
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
		request_count, input_tokens, output_tokens, cache_tokens,
		status_2xx, status_4xx, status_5xx, status_other,
		latency_sum, ttft_sum, ttft_count
	) VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(day, bucket_type, bucket_id) DO UPDATE SET
		bucket_name = CASE WHEN excluded.bucket_name != '' THEN excluded.bucket_name ELSE usage_daily_buckets.bucket_name END,
		request_count = usage_daily_buckets.request_count + 1,
		input_tokens = usage_daily_buckets.input_tokens + excluded.input_tokens,
		output_tokens = usage_daily_buckets.output_tokens + excluded.output_tokens,
		cache_tokens = usage_daily_buckets.cache_tokens + excluded.cache_tokens,
		status_2xx = usage_daily_buckets.status_2xx + excluded.status_2xx,
		status_4xx = usage_daily_buckets.status_4xx + excluded.status_4xx,
		status_5xx = usage_daily_buckets.status_5xx + excluded.status_5xx,
		status_other = usage_daily_buckets.status_other + excluded.status_other,
		latency_sum = usage_daily_buckets.latency_sum + excluded.latency_sum,
		ttft_sum = usage_daily_buckets.ttft_sum + excluded.ttft_sum,
		ttft_count = usage_daily_buckets.ttft_count + excluded.ttft_count`,
		day, bucketType, bucketID, bucketName,
		delta.InputTokens, delta.OutputTokens, delta.CacheTokens,
		status2xx, status4xx, status5xx, statusOther,
		latencySum, ttftSum, ttftCount,
	)
	return err
}

func (s *Store) LoadUsageSince(since time.Time) (map[string]monitor.UsageDayBuckets, *monitor.RequestLog, error) {
	sinceDay := since.Local().Format("2006-01-02")
	rows, err := s.reader().Query(`SELECT day, bucket_type, bucket_id, bucket_name,
		request_count, input_tokens, output_tokens, cache_tokens,
		status_2xx, status_4xx, status_5xx, status_other,
		latency_sum, ttft_sum, ttft_count
		FROM usage_daily_buckets WHERE day >= ? ORDER BY day`, sinceDay)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	out := make(map[string]monitor.UsageDayBuckets)
	for rows.Next() {
		var (
			day, bucketType, bucketID, bucketName string
			reqCount, inTok, outTok, cacheTok      int64
			s2xx, s4xx, s5xx, sOther               int64
			latSum, ttftSum, ttftCount             int64
		)
		if err := rows.Scan(&day, &bucketType, &bucketID, &bucketName, &reqCount, &inTok, &outTok, &cacheTok,
			&s2xx, &s4xx, &s5xx, &sOther, &latSum, &ttftSum, &ttftCount); err != nil {
			return nil, nil, err
		}
		dayBuckets, ok := out[day]
		if !ok {
			dayBuckets = monitor.UsageDayBuckets{
				ByAPIKey:   make(map[string]monitor.APIKeyDayStats),
				ByProvider: make(map[string]monitor.ProviderDayStats),
				ByModel:    make(map[string]monitor.ModelDayStats),
			}
		}
		stats := monitor.APIKeyDayStats{
			RequestCount: reqCount,
			InputTokens:  inTok,
			OutputTokens: outTok,
			CacheTokens:  cacheTok,
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
		case "provider":
			dayBuckets.ByProvider[bucketID] = monitor.ProviderDayStats{
				ProviderID:   bucketID,
				RequestCount: reqCount,
				InputTokens:  inTok,
				OutputTokens: outTok,
				CacheTokens:  cacheTok,
			}
		case "model":
			dayBuckets.ByModel[bucketID] = monitor.ModelDayStats{
				Model:        bucketID,
				RequestCount: reqCount,
				InputTokens:  inTok,
				OutputTokens: outTok,
				CacheTokens:  cacheTok,
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

// ClearUsageDaily removes all persisted usage aggregates (before a full rebuild).
func (s *Store) ClearUsageDaily() error {
	if _, err := s.db.Exec(`DELETE FROM usage_daily_buckets`); err != nil {
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
