package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

func (s *userStore) HasUsers(ctx context.Context) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// CanonicalHandle makes handle comparisons stable across Unicode compatibility
// forms and case variants. This is account identity for the local MVP, not a
// project membership or OS-level tenancy boundary.
func CanonicalHandle(handle string) (string, string, error) {
	handle = norm.NFKC.String(handle)
	if handle == "" || len([]rune(handle)) > 64 || !utf8.ValidString(handle) {
		return "", "", errors.New("invalid handle")
	}
	for _, r := range handle {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.IsSpace(r) || r == '@' || r == '/' || r == '\\' {
			return "", "", errors.New("invalid handle")
		}
	}
	return handle, norm.NFKC.String(cases.Fold().String(handle)), nil
}

func (s *userStore) CreateUser(ctx context.Context, handle, passwordHash string) (User, error) {
	handle, handleKey, err := CanonicalHandle(handle)
	if err != nil {
		return User{}, err
	}
	if strings.TrimSpace(passwordHash) == "" {
		return User{}, errors.New("password hash is required")
	}
	user := User{ID: NewID(), Username: handle, Handle: handle, Role: "user", CreatedAt: Now()}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()

	var existingUsers int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&existingUsers); err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, username, handle, handle_key, password_hash, role, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, user.ID, user.Username, user.Handle, handleKey, passwordHash, user.Role, user.CreatedAt); err != nil {
		if isUniqueConstraint(err) {
			return User{}, fmt.Errorf("%w: handle already exists", ErrConflict)
		}
		return User{}, err
	}
	if existingUsers == 0 {
		if err := assignUnownedProjectsTx(ctx, tx, user.ID, user.CreatedAt); err != nil {
			return User{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return user, nil
}

// CreateProjectMember records a member role. Repeated assignments preserve the
// existing role so concurrent setup or migration retries are harmless.
func (s *userStore) CreateProjectMember(ctx context.Context, member ProjectMember) (ProjectMember, error) {
	member.ProjectID = strings.TrimSpace(member.ProjectID)
	member.UserID = strings.TrimSpace(member.UserID)
	member.Role = strings.TrimSpace(member.Role)
	if member.ProjectID == "" || member.UserID == "" {
		return ProjectMember{}, errors.New("project and user are required")
	}
	if member.Role == "" {
		member.Role = "member"
	}
	if member.Role != "owner" && member.Role != "member" {
		return ProjectMember{}, errors.New("invalid project member role")
	}
	if member.CreatedAt == "" {
		member.CreatedAt = Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO project_members (project_id, user_id, role, created_at) VALUES (?, ?, ?, ?) ON CONFLICT(project_id, user_id) DO UPDATE SET role = excluded.role`, member.ProjectID, member.UserID, member.Role, member.CreatedAt)
	if err != nil {
		return ProjectMember{}, err
	}
	return member, nil
}

func (s *userStore) DeleteProjectMember(ctx context.Context, projectID, userID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM project_members WHERE project_id = ? AND user_id = ?`, strings.TrimSpace(projectID), strings.TrimSpace(userID))
	return err
}

func (s *userStore) ListProjectMembers(ctx context.Context, projectID string) ([]ProjectMember, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_id, user_id, role, created_at FROM project_members WHERE project_id = ? ORDER BY created_at ASC, user_id ASC`, strings.TrimSpace(projectID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := make([]ProjectMember, 0)
	for rows.Next() {
		var member ProjectMember
		if err := rows.Scan(&member.ProjectID, &member.UserID, &member.Role, &member.CreatedAt); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

func (s *userStore) IsProjectMember(ctx context.Context, userID, projectID string) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_members WHERE user_id = ? AND project_id = ?`, strings.TrimSpace(userID), strings.TrimSpace(projectID)).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// AssignUnownedProjectsToUser gives a user ownership only of projects that have
// no members. It is used for first-user bootstrap and is safe to retry.
func (s *userStore) AssignUnownedProjectsToUser(ctx context.Context, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return errors.New("user is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := assignUnownedProjectsTx(ctx, tx, userID, Now()); err != nil {
		return err
	}
	return tx.Commit()
}

func assignUnownedProjectsTx(ctx context.Context, tx *sql.Tx, userID, createdAt string) error {
	_, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO project_members (project_id, user_id, role, created_at)
SELECT p.id, ?, 'owner', ?
FROM projects p
WHERE EXISTS (SELECT 1 FROM users WHERE id = ?)
  AND NOT EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = p.id)`, userID, createdAt, userID)
	return err
}

// CanAccessProject, CanAccessWorkline, and CanAccessAgent are the canonical
// membership checks for all project-scoped server resources.
func (s *userStore) CanAccessProject(ctx context.Context, userID, projectID string) (bool, error) {
	return s.IsProjectMember(ctx, userID, projectID)
}

func (s *userStore) CanAccessWorkline(ctx context.Context, userID, worklineID string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM worklines w JOIN project_members pm ON pm.project_id = w.project_id WHERE w.id = ? AND pm.user_id = ?`, strings.TrimSpace(worklineID), strings.TrimSpace(userID)).Scan(&count)
	return count > 0, err
}

func (s *userStore) CanAccessAgent(ctx context.Context, userID, agentID string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents a JOIN worklines w ON w.id = a.workline_id JOIN project_members pm ON pm.project_id = w.project_id WHERE a.id = ? AND pm.user_id = ?`, strings.TrimSpace(agentID), strings.TrimSpace(userID)).Scan(&count)
	return count > 0, err
}

func (s *userStore) GetUserByHandle(ctx context.Context, handle string) (User, string, error) {
	_, handleKey, err := CanonicalHandle(handle)
	if err != nil {
		return User{}, "", err
	}
	var user User
	var passwordHash string
	err = s.db.QueryRowContext(ctx, `SELECT id, username, handle, role, created_at, COALESCE(password_hash, '') FROM users WHERE handle_key = ?`, handleKey).Scan(&user.ID, &user.Username, &user.Handle, &user.Role, &user.CreatedAt, &passwordHash)
	return user, passwordHash, err
}

func (s *userStore) ListUsersByHandlePrefix(ctx context.Context, prefix string, limit int) ([]User, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	key := ""
	if prefix != "" {
		_, canonical, err := CanonicalHandle(prefix)
		if err != nil {
			return nil, err
		}
		key = canonical
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, username, handle, role, created_at FROM users WHERE handle_key LIKE ? ESCAPE '\' ORDER BY handle_key ASC LIMIT ?`, escapeLike(key)+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]User, 0)
	for rows.Next() {
		var user User
		if err := rows.Scan(&user.ID, &user.Username, &user.Handle, &user.Role, &user.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "%", `\%`)
	return strings.ReplaceAll(value, "_", `\_`)
}

func HashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *userStore) CreateAuthSession(ctx context.Context, session AuthSession) (AuthSession, error) {
	if session.ID == "" {
		session.ID = NewID()
	}
	if session.UserID == "" || session.TokenHash == "" || session.ExpiresAt == "" {
		return AuthSession{}, errors.New("invalid auth session")
	}
	if session.CreatedAt == "" {
		session.CreatedAt = Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO auth_sessions (id, user_id, token_hash, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`, session.ID, session.UserID, session.TokenHash, session.CreatedAt, session.ExpiresAt)
	if isUniqueConstraint(err) {
		return AuthSession{}, fmt.Errorf("%w: session already exists", ErrConflict)
	}
	if err != nil {
		return AuthSession{}, err
	}
	return session, nil
}

func (s *userStore) GetUserBySessionToken(ctx context.Context, token string, now time.Time) (User, AuthSession, error) {
	var user User
	var session AuthSession
	err := s.db.QueryRowContext(ctx, `SELECT u.id, u.username, u.handle, u.role, u.created_at, s.id, s.user_id, s.token_hash, s.created_at, s.expires_at, COALESCE(s.revoked_at, '') FROM auth_sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = ? AND s.revoked_at IS NULL AND s.expires_at > ?`, HashSessionToken(token), now.UTC().Format(time.RFC3339Nano)).Scan(&user.ID, &user.Username, &user.Handle, &user.Role, &user.CreatedAt, &session.ID, &session.UserID, &session.TokenHash, &session.CreatedAt, &session.ExpiresAt, &session.RevokedAt)
	return user, session, err
}

func (s *userStore) RevokeAuthSessionToken(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at = ? WHERE token_hash = ? AND revoked_at IS NULL`, Now(), HashSessionToken(token))
	return err
}
