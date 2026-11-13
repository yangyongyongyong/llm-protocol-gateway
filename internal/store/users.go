package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// scanUser reads a users row in SELECT column order.
func scanUser(rows *sql.Rows) (domain.User, error) {
	var user domain.User
	var role, roleID, kind string
	var enabled int
	if err := rows.Scan(&user.ID, &user.Username, &user.PasswordHash, &role, &roleID,
		&user.DisplayName, &user.AvatarURL, &enabled, &user.CreatedAt, &user.LastLoginAt, &user.LastActiveAt,
		&kind, &user.GatewayID, &user.OwnerUserID); err != nil {
		return domain.User{}, err
	}
	user.Role = domain.UserRole(strings.TrimSpace(role))
	if user.Role == "" {
		user.Role = domain.UserRoleUser
	}
	user.RoleID = strings.TrimSpace(roleID)
	if user.RoleID == "" {
		user.RoleID = string(user.Role)
	}
	// 空 kind 是升级前的行：那个年代只存在本地控制台账号。
	user.Kind = domain.UserKind(strings.TrimSpace(kind))
	if user.Kind == "" {
		user.Kind = domain.UserKindLocal
	}
	user.Enabled = enabled != 0
	return user, nil
}

const userSelectColumns = `id, username, password_hash, role, role_id, display_name, avatar_url, enabled, created_at, last_login_at, last_active_at, kind, gateway_id, owner_user_id`

// ListUsers returns all console users ordered by creation time.
func (s *Store) ListUsers() ([]domain.User, error) {
	rows, err := s.reader().Query(`SELECT ` + userSelectColumns + ` FROM users ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []domain.User{}
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

// UserByID returns one user; sql.ErrNoRows is mapped to a not-found error.
func (s *Store) UserByID(id string) (domain.User, error) {
	return s.queryOneUser(`SELECT `+userSelectColumns+` FROM users WHERE id = ?`, id)
}

// UserByUsername matches the login name case-insensitively.
func (s *Store) UserByUsername(username string) (domain.User, error) {
	return s.queryOneUser(`SELECT `+userSelectColumns+` FROM users WHERE LOWER(username) = LOWER(?)`, strings.TrimSpace(username))
}

// UserByGatewayID returns the peer account registered for a gateway identity.
//
// 注册以网关身份为准而不是用户名：下游改了网关名字（或者两个下游起了同名）
// 时，靠用户名会认错人——要么给同一个网关建出第二个账号，要么把新网关塞进
// 别人的授权里。网关身份是从机器标识派生的，重装也不变。
func (s *Store) UserByGatewayID(gatewayID string) (domain.User, error) {
	gatewayID = strings.TrimSpace(gatewayID)
	if gatewayID == "" {
		return domain.User{}, fmt.Errorf("user not found")
	}
	return s.queryOneUser(`SELECT `+userSelectColumns+` FROM users WHERE gateway_id = ? AND kind = ?`,
		gatewayID, string(domain.UserKindPeer))
}

func (s *Store) queryOneUser(query string, args ...any) (domain.User, error) {
	rows, err := s.reader().Query(query, args...)
	if err != nil {
		return domain.User{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return domain.User{}, err
		}
		return domain.User{}, fmt.Errorf("user not found")
	}
	return scanUser(rows)
}

func (s *Store) CreateUser(user domain.User) error {
	enabled := 0
	if user.Enabled {
		enabled = 1
	}
	if user.RoleID == "" {
		user.RoleID = string(user.Role)
	}
	kind := strings.TrimSpace(string(user.Kind))
	if kind == "" {
		kind = string(domain.UserKindLocal)
	}
	_, err := s.db.Exec(`INSERT INTO users
		(id, username, password_hash, role, role_id, display_name, avatar_url, enabled, created_at, last_login_at, last_active_at, kind, gateway_id, owner_user_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		user.ID, strings.TrimSpace(user.Username), user.PasswordHash, string(user.Role), user.RoleID,
		strings.TrimSpace(user.DisplayName), strings.TrimSpace(user.AvatarURL), enabled,
		user.CreatedAt, user.LastLoginAt, user.LastActiveAt, kind, strings.TrimSpace(user.GatewayID), strings.TrimSpace(user.OwnerUserID))
	return err
}

func (s *Store) UpdateUser(user domain.User) error {
	enabled := 0
	if user.Enabled {
		enabled = 1
	}
	if user.RoleID == "" {
		user.RoleID = string(user.Role)
	}
	kind := strings.TrimSpace(string(user.Kind))
	if kind == "" {
		kind = string(domain.UserKindLocal)
	}
	_, err := s.db.Exec(`UPDATE users
		SET username = ?, password_hash = ?, role = ?, role_id = ?, display_name = ?, avatar_url = ?,
		    enabled = ?, last_login_at = ?, last_active_at = ?, kind = ?, gateway_id = ?, owner_user_id = ?
		WHERE id = ?`,
		strings.TrimSpace(user.Username), user.PasswordHash, string(user.Role), user.RoleID,
		strings.TrimSpace(user.DisplayName), strings.TrimSpace(user.AvatarURL), enabled,
		user.LastLoginAt, user.LastActiveAt, kind, strings.TrimSpace(user.GatewayID), strings.TrimSpace(user.OwnerUserID), user.ID)
	return err
}

// DeleteUser removes the account and its resource grants in one transaction.
func (s *Store) DeleteUser(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM user_resource_grants WHERE user_id = ?`, id); err != nil {
		return err
	}
	// Deleting a human owner must not leave downstream gateways pointing at a
	// nonexistent user. They become unassigned, while their gateway account and
	// traffic history stay intact.
	if _, err := tx.Exec(`UPDATE users SET owner_user_id = '' WHERE owner_user_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM users WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// TouchUserLogin records the latest successful login time.
func (s *Store) TouchUserLogin(id string, lastLoginAt string) error {
	_, err := s.db.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, lastLoginAt, id)
	return err
}

// TouchUserActive records the latest console API activity time. Callers
// throttle this (>=5min between writes per user); the accurate value lives in
// the gateway's in-memory tracker.
func (s *Store) TouchUserActive(id string, lastActiveAt string) error {
	_, err := s.db.Exec(`UPDATE users SET last_active_at = ? WHERE id = ?`, lastActiveAt, id)
	return err
}

// ListResourceGrants returns userID -> granted provider IDs.
func (s *Store) ListResourceGrants() (map[string][]string, error) {
	rows, err := s.reader().Query(`SELECT user_id, resource_id FROM user_resource_grants ORDER BY user_id, resource_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var userID, resourceID string
		if err := rows.Scan(&userID, &resourceID); err != nil {
			return nil, err
		}
		out[userID] = append(out[userID], resourceID)
	}
	return out, rows.Err()
}

// SetResourceGrants replaces one user's provider grants.
func (s *Store) SetResourceGrants(userID string, resourceIDs []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM user_resource_grants WHERE user_id = ?`, userID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, resourceID := range resourceIDs {
		resourceID = strings.TrimSpace(resourceID)
		if resourceID == "" || seen[resourceID] {
			continue
		}
		seen[resourceID] = true
		if _, err := tx.Exec(`INSERT INTO user_resource_grants (user_id, resource_id) VALUES (?, ?)`, userID, resourceID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AddPeerGrantAudit appends one manual downstream grant change.
func (s *Store) AddPeerGrantAudit(audit domain.PeerGrantAudit) error {
	before, err := json.Marshal(audit.BeforeProviderIDs)
	if err != nil {
		return err
	}
	after, err := json.Marshal(audit.AfterProviderIDs)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO peer_grant_audits
		(peer_user_id, actor_user_id, actor_username, actor_display_name, grant_mode, before_provider_ids, after_provider_ids, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(audit.PeerUserID), strings.TrimSpace(audit.ActorUserID),
		strings.TrimSpace(audit.ActorUsername), strings.TrimSpace(audit.ActorDisplayName),
		strings.TrimSpace(audit.GrantMode), string(before), string(after), audit.CreatedAt)
	return err
}

// LatestPeerGrantAudits returns the newest manual grant change per peer.
func (s *Store) LatestPeerGrantAudits() (map[string]domain.PeerGrantAudit, error) {
	rows, err := s.reader().Query(`SELECT id, peer_user_id, actor_user_id, actor_username, actor_display_name,
		grant_mode, before_provider_ids, after_provider_ids, created_at
		FROM peer_grant_audits ORDER BY peer_user_id, created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]domain.PeerGrantAudit{}
	for rows.Next() {
		var audit domain.PeerGrantAudit
		var before, after string
		if err := rows.Scan(&audit.ID, &audit.PeerUserID, &audit.ActorUserID, &audit.ActorUsername,
			&audit.ActorDisplayName, &audit.GrantMode, &before, &after, &audit.CreatedAt); err != nil {
			return nil, err
		}
		if _, seen := out[audit.PeerUserID]; seen {
			continue
		}
		_ = json.Unmarshal([]byte(before), &audit.BeforeProviderIDs)
		_ = json.Unmarshal([]byte(after), &audit.AfterProviderIDs)
		out[audit.PeerUserID] = audit
	}
	return out, rows.Err()
}
