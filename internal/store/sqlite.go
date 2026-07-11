package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/luca/llm-protocol-gateway/internal/config"
	"github.com/luca/llm-protocol-gateway/internal/domain"
)

const schemaVersion = 5

type Store struct {
	path string
	db   *sql.DB
}

func Open(path string) (*Store, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{path: path, db: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func OpenDefault() (*Store, error) {
	path, err := DefaultDBPath()
	if err != nil {
		return nil, err
	}
	return Open(path)
}

// DefaultDBPath resolves the SQLite database path. It reuses the config
// directory resolution strategy from internal/config: an explicit GATEWAY_DB
// override wins, then GATEWAY_CONFIG when it points at a .db file, then the
// per-user config dir, then a home-directory fallback. The database file is
// always named gateway.db within the resolved directory.
func DefaultDBPath() (string, error) {
	if path := strings.TrimSpace(os.Getenv("GATEWAY_DB")); path != "" {
		return filepath.Abs(path)
	}
	if path := strings.TrimSpace(os.Getenv("GATEWAY_CONFIG")); strings.HasSuffix(strings.ToLower(path), ".db") {
		return filepath.Abs(path)
	}
	configPath, err := config.DefaultConfigPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(configPath), "gateway.db"), nil
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS providers (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			protocol TEXT NOT NULL,
			base_url TEXT NOT NULL,
			api_key_source TEXT NOT NULL DEFAULT '',
			default_model TEXT NOT NULL DEFAULT '',
			default_thinking_depth TEXT NOT NULL DEFAULT '',
			health_status TEXT NOT NULL DEFAULT '',
			auth_header TEXT NOT NULL DEFAULT '',
			extra_endpoint TEXT NOT NULL DEFAULT '',
			position INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS models (
			provider_id TEXT NOT NULL,
			id TEXT NOT NULL,
			protocol TEXT NOT NULL,
			context_length INTEGER NOT NULL DEFAULT 0,
			in_menu INTEGER NOT NULL DEFAULT 1,
			position INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (provider_id, id)
		)`,
		`CREATE TABLE IF NOT EXISTS routes (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			output_endpoint_id TEXT NOT NULL DEFAULT '',
			provider_id TEXT NOT NULL,
			output_protocol TEXT NOT NULL,
			mode TEXT NOT NULL DEFAULT 'auto',
			enabled INTEGER NOT NULL DEFAULT 1,
			position INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS api_keys (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			key TEXT NOT NULL UNIQUE,
			route_id TEXT NOT NULL DEFAULT '',
			model_override TEXT NOT NULL DEFAULT '',
			thinking_depth_override TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL DEFAULT '',
			last_used_at TEXT NOT NULL DEFAULT '',
			position INTEGER NOT NULL DEFAULT 0
		)`,
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	// Providers gained Claude OAuth columns in schema version 2. SQLite has no
	// "ADD COLUMN IF NOT EXISTS", so check PRAGMA table_info first; this keeps
	// startup idempotent for both fresh and pre-existing databases, mirroring
	// the CREATE TABLE IF NOT EXISTS style used above.
	oauthColumns := []struct{ name, definition string }{
		{"auth_type", "TEXT NOT NULL DEFAULT ''"},
		{"oauth_access_token", "TEXT NOT NULL DEFAULT ''"},
		{"oauth_refresh_token", "TEXT NOT NULL DEFAULT ''"},
		{"oauth_expires_at", "TEXT NOT NULL DEFAULT ''"},
		{"oauth_scope", "TEXT NOT NULL DEFAULT ''"},
		{"oauth_account_label", "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range oauthColumns {
		if err := addColumnIfMissing(tx, "providers", column.name, column.definition); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	if err := ensureRequestLogsTable(tx); err != nil {
		return fmt.Errorf("migrate request_logs: %w", err)
	}
	if err := ensureUsageDailyTables(tx); err != nil {
		return fmt.Errorf("migrate usage_daily: %w", err)
	}
	if err := addColumnIfMissing(tx, "api_keys", "model_aliases", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// stream_enabled defaults to 1 so existing keys keep streaming on.
	if err := addColumnIfMissing(tx, "api_keys", "stream_enabled", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := addColumnIfMissing(tx, "api_keys", "fallback_provider_ids", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := addColumnIfMissing(tx, "api_keys", "active_provider_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := addColumnIfMissing(tx, "api_keys", "fallback_model_overrides", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES ('version', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, fmt.Sprintf("%d", schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// addColumnIfMissing runs ALTER TABLE ... ADD COLUMN for the given column only
// if it is not already present, since SQLite lacks ADD COLUMN IF NOT EXISTS.
func addColumnIfMissing(tx *sql.Tx, table, column, definition string) error {
	rows, err := tx.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return err
	}
	exists := false
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		if name == column {
			exists = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = tx.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, definition))
	return err
}

// Initialized reports whether the database already holds gateway state. It is
// used to decide whether a one-time JSON import should run.
func (s *Store) Initialized() (bool, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = 'initialized'`).Scan(&value)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

func (s *Store) markInitialized(tx *sql.Tx) error {
	_, err := tx.Exec(`INSERT INTO settings (key, value) VALUES ('initialized', 'true')
		ON CONFLICT(key) DO UPDATE SET value = 'true'`)
	return err
}

func (s *Store) Load(defaultState domain.GatewayState) (domain.GatewayState, error) {
	state := defaultState

	providers, err := s.loadProviders()
	if err != nil {
		return domain.GatewayState{}, err
	}
	state.Providers = providers

	routes, err := s.loadRoutes()
	if err != nil {
		return domain.GatewayState{}, err
	}
	state.Routes = routes

	keys, err := s.loadAPIKeys()
	if err != nil {
		return domain.GatewayState{}, err
	}
	state.APIKeys = keys

	state.Models = []domain.Model{}
	state.Metrics = domain.MetricsSnapshot{}

	if raw := s.setting("publicAccess"); raw != "" {
		var settings domain.PublicAccessSettings
		if err := json.Unmarshal([]byte(raw), &settings); err == nil {
			// Legacy configs predate exposeApi/exposeUi. Default each surface on
			// when its hostname is present (or both on for older single-domain setups).
			var rawMap map[string]json.RawMessage
			if err := json.Unmarshal([]byte(raw), &rawMap); err == nil {
				if _, ok := rawMap["exposeApi"]; !ok {
					settings.ExposeAPI = strings.TrimSpace(settings.CustomDomain) != ""
				}
				if _, ok := rawMap["exposeUi"]; !ok {
					settings.ExposeUI = strings.TrimSpace(settings.UIDomain) != ""
					if !settings.ExposeAPI && !settings.ExposeUI && strings.TrimSpace(settings.CustomDomain) != "" {
						settings.ExposeAPI = true
						settings.ExposeUI = strings.TrimSpace(settings.UIDomain) != ""
					}
				}
			}
			state.PublicAccess = settings
		}
	}
	if raw := s.setting("endpointStreamEnabled"); raw != "" {
		var overrides map[string]bool
		if err := json.Unmarshal([]byte(raw), &overrides); err == nil && len(overrides) > 0 {
			// Apply onto default endpoints so NewRouter's mergeDefaultEndpoints
			// preserves the persisted streamEnabled flags.
			for index := range state.Endpoints {
				if enabled, ok := overrides[state.Endpoints[index].ID]; ok {
					state.Endpoints[index].StreamEnabled = enabled
				}
			}
		}
	}
	state.LogLevel = strings.TrimSpace(s.setting("logLevel"))
	if raw := strings.TrimSpace(s.setting("requestLogRetentionDays")); raw != "" {
		if days, err := strconv.Atoi(raw); err == nil && days > 0 {
			state.RequestLogRetentionDays = days
		}
	}
	if state.RequestLogRetentionDays <= 0 {
		state.RequestLogRetentionDays = 7
	}
	if raw := strings.TrimSpace(s.setting("webExposed")); raw != "" {
		state.WebExposed = raw == "1" || strings.EqualFold(raw, "true")
	}
	if raw := s.setting("providerRequestAdapters"); raw != "" {
		var adapters map[string]*domain.RequestAdapter
		if err := json.Unmarshal([]byte(raw), &adapters); err == nil {
			for index := range state.Providers {
				if adapter, ok := adapters[state.Providers[index].ID]; ok {
					state.Providers[index].RequestAdapter = adapter
				}
			}
		}
	}
	return state, nil
}

func (s *Store) Save(state domain.GatewayState) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM providers`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM models`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM routes`); err != nil {
		return err
	}
	for pIndex, provider := range state.Providers {
		var accessToken, refreshToken, expiresAt, scope, accountLabel string
		switch provider.AuthType {
		case domain.AuthTypeClaudeOAuth:
			if provider.ClaudeOAuth != nil {
				accessToken = provider.ClaudeOAuth.AccessToken
				refreshToken = provider.ClaudeOAuth.RefreshToken
				expiresAt = provider.ClaudeOAuth.ExpiresAt
				scope = provider.ClaudeOAuth.Scope
				accountLabel = provider.ClaudeOAuth.AccountLabel
			}
		case domain.AuthTypeCursorOAuth:
			if provider.CursorOAuth != nil {
				accessToken = provider.CursorOAuth.AccessToken
				refreshToken = provider.CursorOAuth.RefreshToken
				expiresAt = provider.CursorOAuth.ExpiresAt
				accountLabel = provider.CursorOAuth.AccountLabel
			}
		}
		if _, err := tx.Exec(`INSERT INTO providers
			(id, name, protocol, base_url, api_key_source, default_model, default_thinking_depth, health_status, auth_header, extra_endpoint, position,
			 auth_type, oauth_access_token, oauth_refresh_token, oauth_expires_at, oauth_scope, oauth_account_label)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			provider.ID, provider.Name, string(provider.Protocol), provider.BaseURL, provider.APIKeySource,
			provider.DefaultModel, provider.DefaultThinkingDepth, provider.HealthStatus, provider.AuthHeader,
			provider.ExtraEndpoint, pIndex,
			provider.AuthType, accessToken, refreshToken, expiresAt, scope, accountLabel); err != nil {
			return err
		}
		for mIndex, model := range provider.Models {
			inMenu := 0
			if model.InMenu {
				inMenu = 1
			}
			if _, err := tx.Exec(`INSERT INTO models
				(provider_id, id, protocol, context_length, in_menu, position)
				VALUES (?, ?, ?, ?, ?, ?)`,
				provider.ID, model.ID, string(model.Protocol), model.ContextLength, inMenu, mIndex); err != nil {
				return err
			}
		}
	}
	for index, route := range state.Routes {
		enabled := 0
		if route.Enabled {
			enabled = 1
		}
		if _, err := tx.Exec(`INSERT INTO routes
			(id, name, output_endpoint_id, provider_id, output_protocol, mode, enabled, position)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			route.ID, route.Name, route.OutputEndpointID, route.ProviderID, string(route.OutputProtocol),
			string(route.Mode), enabled, index); err != nil {
			return err
		}
	}

	publicAccess := state.PublicAccess
	publicAccess.Tunnel = nil
	publicRaw, err := json.Marshal(publicAccess)
	if err != nil {
		return err
	}
	if err := setSetting(tx, "publicAccess", string(publicRaw)); err != nil {
		return err
	}
	// Persist per-endpoint streamEnabled flags (fixed endpoint list itself is code-defined).
	streamOverrides := make(map[string]bool, len(state.Endpoints))
	for _, endpoint := range state.Endpoints {
		streamOverrides[endpoint.ID] = endpoint.StreamEnabled
	}
	streamRaw, err := json.Marshal(streamOverrides)
	if err != nil {
		return err
	}
	if err := setSetting(tx, "endpointStreamEnabled", string(streamRaw)); err != nil {
		return err
	}
	if err := setSetting(tx, "logLevel", strings.TrimSpace(state.LogLevel)); err != nil {
		return err
	}
	retentionDays := state.RequestLogRetentionDays
	if retentionDays <= 0 {
		retentionDays = 7
	}
	if err := setSetting(tx, "requestLogRetentionDays", strconv.Itoa(retentionDays)); err != nil {
		return err
	}
	webExposedValue := "false"
	if state.WebExposed {
		webExposedValue = "true"
	}
	if err := setSetting(tx, "webExposed", webExposedValue); err != nil {
		return err
	}
	adapters := make(map[string]*domain.RequestAdapter, len(state.Providers))
	for _, provider := range state.Providers {
		if provider.RequestAdapter != nil {
			adapters[provider.ID] = provider.RequestAdapter
		}
	}
	adapterRaw, err := json.Marshal(adapters)
	if err != nil {
		return err
	}
	if err := setSetting(tx, "providerRequestAdapters", string(adapterRaw)); err != nil {
		return err
	}
	if err := s.markInitialized(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) loadProviders() ([]domain.Provider, error) {
	rows, err := s.db.Query(`SELECT id, name, protocol, base_url, api_key_source, default_model, default_thinking_depth, health_status, auth_header, extra_endpoint,
		auth_type, oauth_access_token, oauth_refresh_token, oauth_expires_at, oauth_scope, oauth_account_label
		FROM providers ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	providers := []domain.Provider{}
	for rows.Next() {
		var p domain.Provider
		var protocol string
		var accessToken, refreshToken, expiresAt, scope, accountLabel string
		if err := rows.Scan(&p.ID, &p.Name, &protocol, &p.BaseURL, &p.APIKeySource, &p.DefaultModel, &p.DefaultThinkingDepth, &p.HealthStatus, &p.AuthHeader, &p.ExtraEndpoint,
			&p.AuthType, &accessToken, &refreshToken, &expiresAt, &scope, &accountLabel); err != nil {
			_ = rows.Close()
			return nil, err
		}
		p.Protocol = domain.Protocol(protocol)
		switch p.AuthType {
		case domain.AuthTypeClaudeOAuth:
			if accessToken != "" || refreshToken != "" {
				p.ClaudeOAuth = &domain.ClaudeOAuthCredential{
					AccessToken:  accessToken,
					RefreshToken: refreshToken,
					ExpiresAt:    expiresAt,
					Scope:        scope,
					AccountLabel: accountLabel,
				}
			}
		case domain.AuthTypeCursorOAuth:
			if accessToken != "" || refreshToken != "" {
				p.CursorOAuth = &domain.CursorOAuthCredential{
					AccessToken:  accessToken,
					RefreshToken: refreshToken,
					ExpiresAt:    expiresAt,
					AccountLabel: accountLabel,
				}
			}
		default:
			if accessToken != "" || refreshToken != "" {
				p.ClaudeOAuth = &domain.ClaudeOAuthCredential{
					AccessToken:  accessToken,
					RefreshToken: refreshToken,
					ExpiresAt:    expiresAt,
					Scope:        scope,
					AccountLabel: accountLabel,
				}
			}
		}
		providers = append(providers, p)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range providers {
		models, err := s.loadModels(providers[index].ID)
		if err != nil {
			return nil, err
		}
		providers[index].Models = models
	}
	return providers, nil
}

func (s *Store) loadModels(providerID string) ([]domain.Model, error) {
	rows, err := s.db.Query(`SELECT id, protocol, context_length, in_menu FROM models WHERE provider_id = ? ORDER BY position, id`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	models := []domain.Model{}
	for rows.Next() {
		var m domain.Model
		var protocol string
		var inMenu int
		if err := rows.Scan(&m.ID, &protocol, &m.ContextLength, &inMenu); err != nil {
			return nil, err
		}
		m.ProviderID = providerID
		m.Protocol = domain.Protocol(protocol)
		m.InMenu = inMenu != 0
		models = append(models, m)
	}
	return models, rows.Err()
}

func (s *Store) loadRoutes() ([]domain.Route, error) {
	rows, err := s.db.Query(`SELECT id, name, output_endpoint_id, provider_id, output_protocol, mode, enabled FROM routes ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	routes := []domain.Route{}
	for rows.Next() {
		var route domain.Route
		var protocol, mode string
		var enabled int
		if err := rows.Scan(&route.ID, &route.Name, &route.OutputEndpointID, &route.ProviderID, &protocol, &mode, &enabled); err != nil {
			return nil, err
		}
		route.OutputProtocol = domain.Protocol(protocol)
		route.Mode = domain.RouteMode(mode)
		route.Enabled = enabled != 0
		routes = append(routes, route)
	}
	return routes, rows.Err()
}

func encodeModelAliases(aliases map[string]string) string {
	if len(aliases) == 0 {
		return ""
	}
	raw, err := json.Marshal(aliases)
	if err != nil {
		return ""
	}
	return string(raw)
}

func decodeModelAliases(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var aliases map[string]string
	if err := json.Unmarshal([]byte(raw), &aliases); err != nil {
		return nil
	}
	if len(aliases) == 0 {
		return nil
	}
	return aliases
}

func encodeProviderIDList(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return ""
	}
	return string(raw)
}

func decodeProviderIDList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	out := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func encodeFallbackModelOverrides(overrides map[string]string) string {
	if len(overrides) == 0 {
		return ""
	}
	raw, err := json.Marshal(overrides)
	if err != nil {
		return ""
	}
	return string(raw)
}

func decodeFallbackModelOverrides(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var overrides map[string]string
	if err := json.Unmarshal([]byte(raw), &overrides); err != nil {
		return nil
	}
	if len(overrides) == 0 {
		return nil
	}
	out := make(map[string]string, len(overrides))
	for id, model := range overrides {
		id = strings.TrimSpace(id)
		model = strings.TrimSpace(model)
		if id == "" || model == "" {
			continue
		}
		out[id] = model
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *Store) loadAPIKeys() ([]domain.APIKey, error) {
	rows, err := s.db.Query(`SELECT id, name, key, route_id, model_override, model_aliases, thinking_depth_override, stream_enabled, fallback_provider_ids, fallback_model_overrides, active_provider_id, enabled, created_at, last_used_at
		FROM api_keys ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []domain.APIKey{}
	for rows.Next() {
		var k domain.APIKey
		var enabled int
		var streamEnabled int
		var modelAliases string
		var fallbackProviderIDs string
		var fallbackModelOverrides string
		if err := rows.Scan(&k.ID, &k.Name, &k.Key, &k.RouteID, &k.ModelOverride, &modelAliases, &k.ThinkingDepthOverride, &streamEnabled, &fallbackProviderIDs, &fallbackModelOverrides, &k.ActiveProviderID, &enabled, &k.CreatedAt, &k.LastUsedAt); err != nil {
			return nil, err
		}
		k.Enabled = enabled != 0
		k.StreamEnabled = streamEnabled != 0
		k.ModelAliases = decodeModelAliases(modelAliases)
		k.FallbackProviderIDs = decodeProviderIDList(fallbackProviderIDs)
		k.FallbackModelOverrides = decodeFallbackModelOverrides(fallbackModelOverrides)
		k.ActiveProviderID = strings.TrimSpace(k.ActiveProviderID)
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// APIKeys returns the persisted API keys directly from the database.
func (s *Store) APIKeys() ([]domain.APIKey, error) {
	return s.loadAPIKeys()
}

func (s *Store) CreateAPIKey(key domain.APIKey) error {
	var maxPosition sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(position) FROM api_keys`).Scan(&maxPosition); err != nil {
		return err
	}
	position := int64(0)
	if maxPosition.Valid {
		position = maxPosition.Int64 + 1
	}
	enabled := 0
	if key.Enabled {
		enabled = 1
	}
	streamEnabled := 0
	if key.StreamEnabled {
		streamEnabled = 1
	}
	_, err := s.db.Exec(`INSERT INTO api_keys
		(id, name, key, route_id, model_override, model_aliases, thinking_depth_override, stream_enabled, fallback_provider_ids, fallback_model_overrides, active_provider_id, enabled, created_at, last_used_at, position)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key.ID, key.Name, key.Key, key.RouteID, key.ModelOverride, encodeModelAliases(key.ModelAliases), key.ThinkingDepthOverride, streamEnabled, encodeProviderIDList(key.FallbackProviderIDs), encodeFallbackModelOverrides(key.FallbackModelOverrides), strings.TrimSpace(key.ActiveProviderID), enabled, key.CreatedAt, key.LastUsedAt, position)
	return err
}

func (s *Store) UpdateAPIKey(key domain.APIKey) error {
	enabled := 0
	if key.Enabled {
		enabled = 1
	}
	streamEnabled := 0
	if key.StreamEnabled {
		streamEnabled = 1
	}
	_, err := s.db.Exec(`UPDATE api_keys
		SET name = ?, route_id = ?, model_override = ?, model_aliases = ?, thinking_depth_override = ?, stream_enabled = ?, fallback_provider_ids = ?, fallback_model_overrides = ?, active_provider_id = ?, enabled = ?
		WHERE id = ?`,
		key.Name, key.RouteID, key.ModelOverride, encodeModelAliases(key.ModelAliases), key.ThinkingDepthOverride, streamEnabled, encodeProviderIDList(key.FallbackProviderIDs), encodeFallbackModelOverrides(key.FallbackModelOverrides), strings.TrimSpace(key.ActiveProviderID), enabled, key.ID)
	return err
}

func (s *Store) DeleteAPIKey(id string) error {
	_, err := s.db.Exec(`DELETE FROM api_keys WHERE id = ?`, id)
	return err
}

func (s *Store) TouchAPIKey(id string, lastUsedAt string) error {
	_, err := s.db.Exec(`UPDATE api_keys SET last_used_at = ? WHERE id = ?`, lastUsedAt, id)
	return err
}

func (s *Store) setting(key string) string {
	var value string
	if err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value); err != nil {
		return ""
	}
	return value
}

// Setting returns a settings value (empty string when missing).
func (s *Store) Setting(key string) string {
	return s.setting(key)
}

// SetSetting upserts a settings key/value pair.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// HasSetting reports whether a settings key exists (even if the value is empty).
func (s *Store) HasSetting(key string) bool {
	var value string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	return err == nil
}

func setSetting(tx *sql.Tx, key, value string) error {
	_, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// MigrateFromJSON performs the one-time import from a legacy config.json into
// the SQLite database when the database has not been initialized yet. The JSON
// file is left in place as a backup. It returns whether an import happened.
func (s *Store) MigrateFromJSON(jsonPath string, defaultState domain.GatewayState) (bool, error) {
	initialized, err := s.Initialized()
	if err != nil {
		return false, err
	}
	if initialized {
		return false, nil
	}
	if _, err := os.Stat(jsonPath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	legacy := config.NewStore(jsonPath)
	state, err := legacy.Load(defaultState)
	if err != nil {
		return false, err
	}
	state.APIKeys = nil
	if err := s.Save(state); err != nil {
		return false, err
	}
	slog.Info("migrated legacy config into sqlite",
		"json", jsonPath,
		"providers", len(state.Providers),
		"routes", len(state.Routes),
		"models", len(state.Models))
	return true, nil
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}
