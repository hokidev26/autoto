package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (s *userStore) CreateUserAccessKey(ctx context.Context, key UserAccessKey) (UserAccessKey, error) {
	key.UserID = strings.TrimSpace(key.UserID)
	key.TokenHash = strings.TrimSpace(key.TokenHash)
	key.Label = strings.TrimSpace(key.Label)
	if key.UserID == "" || key.TokenHash == "" {
		return UserAccessKey{}, errors.New("access key user and token hash are required")
	}
	if key.ID == "" {
		key.ID = NewID()
	}
	if key.CreatedAt == "" {
		key.CreatedAt = Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_access_keys (id, user_id, token_hash, label, created_at) VALUES (?, ?, ?, ?, ?)`, key.ID, key.UserID, key.TokenHash, key.Label, key.CreatedAt)
	if isUniqueConstraint(err) {
		return UserAccessKey{}, fmt.Errorf("%w: access key already exists", ErrConflict)
	}
	if err != nil {
		return UserAccessKey{}, err
	}
	return key, nil
}

func (s *userStore) GetUserByAccessKeyToken(ctx context.Context, token string) (User, UserAccessKey, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return User{}, UserAccessKey{}, sql.ErrNoRows
	}
	var user User
	var key UserAccessKey
	err := s.db.QueryRowContext(ctx, `SELECT u.id, u.username, u.handle, u.role, u.created_at, k.id, k.user_id, k.token_hash, k.label, k.created_at, COALESCE(k.last_used_at, ''), COALESCE(k.revoked_at, '') FROM user_access_keys k JOIN users u ON u.id = k.user_id WHERE k.token_hash = ? AND k.revoked_at IS NULL`, HashSessionToken(token)).Scan(&user.ID, &user.Username, &user.Handle, &user.Role, &user.CreatedAt, &key.ID, &key.UserID, &key.TokenHash, &key.Label, &key.CreatedAt, &key.LastUsedAt, &key.RevokedAt)
	return user, key, err
}

func (s *userStore) TouchUserAccessKey(ctx context.Context, keyID string) error {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return errors.New("access key is required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE user_access_keys SET last_used_at = ? WHERE id = ? AND revoked_at IS NULL`, Now(), keyID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *userStore) ListUserAccessKeys(ctx context.Context, userID string) ([]UserAccessKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, label, created_at, COALESCE(last_used_at, ''), COALESCE(revoked_at, '') FROM user_access_keys WHERE user_id = ? AND revoked_at IS NULL ORDER BY created_at ASC, id ASC`, strings.TrimSpace(userID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make([]UserAccessKey, 0)
	for rows.Next() {
		var key UserAccessKey
		if err := rows.Scan(&key.ID, &key.UserID, &key.Label, &key.CreatedAt, &key.LastUsedAt, &key.RevokedAt); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *userStore) CountUserAccessKeys(ctx context.Context, userID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_access_keys WHERE user_id = ? AND revoked_at IS NULL`, strings.TrimSpace(userID)).Scan(&count)
	return count, err
}

func (s *userStore) RevokeUserAccessKey(ctx context.Context, userID, keyID string) error {
	userID = strings.TrimSpace(userID)
	keyID = strings.TrimSpace(keyID)
	if userID == "" || keyID == "" {
		return errors.New("user and access key are required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE user_access_keys SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL`, Now(), keyID, userID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
