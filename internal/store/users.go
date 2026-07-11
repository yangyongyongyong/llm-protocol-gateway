package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// scanUser reads a users row in SELECT column order.
func scanUser(rows *sql.Rows) (domain.User, error) {
	var user domain.User
	var role string
	var allowedProviderIDs string
	var enabled int
	if err := rows.Scan(&user.ID, &user.Username, &user.PasswordHash, &role, &allowedProviderIDs, &enabled, &user.CreatedAt, &user.LastLoginAt); err != nil {
		return domain.User{}, err
	}
	user.Role = domain.UserRole(strings.TrimSpace(role))
	if user.Role == "" {
		user.Role = domain.UserRoleUser
	}
	user.AllowedProviderIDs = decodeProviderIDList(allowedProviderIDs)
	user.Enabled = enabled != 0
	return user, nil
}

const userSelectColumns = `id, username, password_hash, role, allowed_provider_ids, enabled, created_at, last_login_at`

// ListUsers returns all console users ordered by creation time.
func (s *Store) ListUsers() ([]domain.User, error) {
	rows, err := s.db.Query(`SELECT ` + userSelectColumns + ` FROM users ORDER BY created_at, id`)
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

func (s *Store) queryOneUser(query string, arg any) (domain.User, error) {
	rows, err := s.db.Query(query, arg)
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
	_, err := s.db.Exec(`INSERT INTO users
		(id, username, password_hash, role, allowed_provider_ids, enabled, created_at, last_login_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		user.ID, strings.TrimSpace(user.Username), user.PasswordHash, string(user.Role), encodeProviderIDList(user.AllowedProviderIDs), enabled, user.CreatedAt, user.LastLoginAt)
	return err
}

func (s *Store) UpdateUser(user domain.User) error {
	enabled := 0
	if user.Enabled {
		enabled = 1
	}
	_, err := s.db.Exec(`UPDATE users
		SET username = ?, password_hash = ?, role = ?, allowed_provider_ids = ?, enabled = ?, last_login_at = ?
		WHERE id = ?`,
		strings.TrimSpace(user.Username), user.PasswordHash, string(user.Role), encodeProviderIDList(user.AllowedProviderIDs), enabled, user.LastLoginAt, user.ID)
	return err
}

func (s *Store) DeleteUser(id string) error {
	_, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

// TouchUserLogin records the latest successful login time.
func (s *Store) TouchUserLogin(id string, lastLoginAt string) error {
	_, err := s.db.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, lastLoginAt, id)
	return err
}
