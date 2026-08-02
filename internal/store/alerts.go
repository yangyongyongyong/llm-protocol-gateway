package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

const (
	defaultAlertPage = 50
	maxAlertPageSize = 200
)

func ensureAlertsTable(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS alerts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		time TEXT NOT NULL,
		rule TEXT NOT NULL DEFAULT '',
		severity TEXT NOT NULL DEFAULT 'warn',
		api_key_id TEXT NOT NULL DEFAULT '',
		api_key_name TEXT NOT NULL DEFAULT '',
		ips TEXT NOT NULL DEFAULT '',
		ip_count INTEGER NOT NULL DEFAULT 0,
		window_minutes INTEGER NOT NULL DEFAULT 0,
		request_count INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'unread',
		push_status TEXT NOT NULL DEFAULT '',
		push_error TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_alerts_time ON alerts(time DESC, id DESC)`)
	return err
}

// DetectMultiIPKeys returns API keys seen from at least threshold distinct
// client IPs since the given time — the key-leak signal.
//
// Rows with an empty api_key_id are excluded on purpose: unauthenticated
// traffic and console route tests carry no key id (see recordRequestLogEx), and
// folding them together would synthesize a bogus shared "key". Empty client_ip
// rows are excluded for the same reason — they would inflate nothing but add a
// phantom member to the IP set.
func (s *Store) DetectMultiIPKeys(since time.Time, threshold int) ([]monitor.MultiIPHit, error) {
	if threshold < 2 {
		threshold = 2
	}
	rows, err := s.reader().Query(`SELECT api_key_id,
			MAX(api_key_name) AS key_name,
			COUNT(DISTINCT client_ip) AS ip_count,
			COUNT(*) AS request_count,
			GROUP_CONCAT(DISTINCT client_ip) AS ips
		FROM request_logs
		WHERE time >= ? AND api_key_id != '' AND client_ip != ''
		GROUP BY api_key_id
		HAVING COUNT(DISTINCT client_ip) >= ?
		ORDER BY ip_count DESC`,
		since.UTC().Format(time.RFC3339Nano), threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	hits := make([]monitor.MultiIPHit, 0, 4)
	for rows.Next() {
		var (
			hit     monitor.MultiIPHit
			keyName sql.NullString
			ips     sql.NullString
		)
		if err := rows.Scan(&hit.APIKeyID, &keyName, &hit.IPCount, &hit.RequestCount, &ips); err != nil {
			return nil, err
		}
		hit.APIKeyName = keyName.String
		// GROUP_CONCAT with DISTINCT always uses "," and ignores a custom
		// separator, so splitting on comma is safe here.
		for _, ip := range strings.Split(ips.String, ",") {
			if trimmed := strings.TrimSpace(ip); trimmed != "" {
				hit.IPs = append(hit.IPs, trimmed)
			}
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

func (s *Store) InsertAlert(alert monitor.Alert) (int64, error) {
	ips, err := json.Marshal(alert.IPs)
	if err != nil {
		return 0, err
	}
	when := alert.Time
	if when.IsZero() {
		when = time.Now()
	}
	result, err := s.db.Exec(`INSERT INTO alerts (
			time, rule, severity, api_key_id, api_key_name, ips, ip_count,
			window_minutes, request_count, status, push_status, push_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		when.UTC().Format(time.RFC3339Nano), alert.Rule, alert.Severity,
		alert.APIKeyID, alert.APIKeyName, string(ips), alert.IPCount,
		alert.WindowMinutes, alert.RequestCount, alert.Status,
		alert.PushStatus, alert.PushError)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// AlertPage is one page of persisted alerts plus the unfiltered status counts
// the console needs for its filter chips.
type AlertPage struct {
	Items    []monitor.Alert `json:"items"`
	Total    int             `json:"total"`
	Page     int             `json:"page"`
	PageSize int             `json:"pageSize"`
	Counts   AlertCounts     `json:"counts"`
}

type AlertCounts struct {
	All     int `json:"all"`
	Unread  int `json:"unread"`
	Read    int `json:"read"`
	Ignored int `json:"ignored"`
}

func (s *Store) QueryAlerts(query monitor.AlertQuery) (AlertPage, error) {
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = defaultAlertPage
	}
	if pageSize > maxAlertPageSize {
		pageSize = maxAlertPageSize
	}
	page := query.Page
	if page <= 0 {
		page = 1
	}

	where := "1=1"
	args := make([]any, 0, 3)
	switch strings.ToLower(strings.TrimSpace(query.Status)) {
	case "unread", "read", "ignored":
		where = "status = ?"
		args = append(args, strings.ToLower(strings.TrimSpace(query.Status)))
	}

	result := AlertPage{Page: page, PageSize: pageSize, Items: []monitor.Alert{}}
	if err := s.reader().QueryRow(`SELECT COUNT(*) FROM alerts WHERE `+where, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	counts, err := s.alertCounts()
	if err != nil {
		return result, err
	}
	result.Counts = counts

	listArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := s.reader().Query(`SELECT id, time, rule, severity, api_key_id, api_key_name,
			ips, ip_count, window_minutes, request_count, status, push_status, push_error
		FROM alerts WHERE `+where+`
		ORDER BY time DESC, id DESC LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		alert, err := scanAlert(rows)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, alert)
	}
	return result, rows.Err()
}

func (s *Store) alertCounts() (AlertCounts, error) {
	var counts AlertCounts
	rows, err := s.reader().Query(`SELECT status, COUNT(*) FROM alerts GROUP BY status`)
	if err != nil {
		return counts, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			status string
			total  int
		)
		if err := rows.Scan(&status, &total); err != nil {
			return counts, err
		}
		counts.All += total
		switch status {
		case "unread":
			counts.Unread = total
		case "read":
			counts.Read = total
		case "ignored":
			counts.Ignored = total
		}
	}
	return counts, rows.Err()
}

// AlertByID returns one alert; ok is false when the id does not exist.
func (s *Store) AlertByID(id int64) (monitor.Alert, bool, error) {
	row := s.reader().QueryRow(`SELECT id, time, rule, severity, api_key_id, api_key_name,
			ips, ip_count, window_minutes, request_count, status, push_status, push_error
		FROM alerts WHERE id = ?`, id)
	alert, err := scanAlert(row)
	if err == sql.ErrNoRows {
		return monitor.Alert{}, false, nil
	}
	if err != nil {
		return monitor.Alert{}, false, err
	}
	return alert, true, nil
}

// LatestAlertForKey returns the most recent alert for a rule+key pair, used for
// cooldown suppression. ok is false when the key has never alerted.
func (s *Store) LatestAlertForKey(rule, apiKeyID string) (monitor.Alert, bool, error) {
	row := s.reader().QueryRow(`SELECT id, time, rule, severity, api_key_id, api_key_name,
			ips, ip_count, window_minutes, request_count, status, push_status, push_error
		FROM alerts WHERE rule = ? AND api_key_id = ?
		ORDER BY time DESC, id DESC LIMIT 1`, rule, apiKeyID)
	alert, err := scanAlert(row)
	if err == sql.ErrNoRows {
		return monitor.Alert{}, false, nil
	}
	if err != nil {
		return monitor.Alert{}, false, err
	}
	return alert, true, nil
}

func (s *Store) UpdateAlertStatus(id int64, status string) error {
	switch status {
	case "unread", "read", "ignored":
	default:
		return fmt.Errorf("invalid alert status %q", status)
	}
	_, err := s.db.Exec(`UPDATE alerts SET status = ? WHERE id = ?`, status, id)
	return err
}

func (s *Store) UpdateAlertPush(id int64, pushStatus, pushError string) error {
	_, err := s.db.Exec(`UPDATE alerts SET push_status = ?, push_error = ? WHERE id = ?`,
		pushStatus, pushError, id)
	return err
}

// rowScanner covers both *sql.Row and *sql.Rows so scanAlert serves both.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanAlert(row rowScanner) (monitor.Alert, error) {
	var (
		alert      monitor.Alert
		when       string
		ips        sql.NullString
		pushStatus sql.NullString
		pushError  sql.NullString
	)
	if err := row.Scan(&alert.ID, &when, &alert.Rule, &alert.Severity,
		&alert.APIKeyID, &alert.APIKeyName, &ips, &alert.IPCount,
		&alert.WindowMinutes, &alert.RequestCount, &alert.Status,
		&pushStatus, &pushError); err != nil {
		return monitor.Alert{}, err
	}
	if parsed, err := time.Parse(time.RFC3339Nano, when); err == nil {
		alert.Time = parsed
	}
	alert.PushStatus = pushStatus.String
	alert.PushError = pushError.String
	if raw := strings.TrimSpace(ips.String); raw != "" {
		_ = json.Unmarshal([]byte(raw), &alert.IPs)
	}
	if alert.IPs == nil {
		alert.IPs = []string{}
	}
	return alert, nil
}
