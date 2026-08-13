package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	oauthAppIssuerMaxBytes      = 2048
	oauthAppSubjectMaxBytes     = 1024
	oauthAppUserIDMaxBytes      = 128
	oauthAppEmailMaxBytes       = 320
	oauthAppDisplayNameMaxBytes = 512
	oauthAppScopeMaxBytes       = 256
	oauthAppScopesJSONMaxBytes  = 32768
)

// OAuthAppIdentity binds an issuer-local subject to one local user. Email and
// display name are mutable metadata and are never used as identity keys.
type OAuthAppIdentity struct {
	Issuer      string `json:"issuer"`
	Subject     string `json:"subject"`
	UserID      string `json:"userId"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// OAuthAppSession is the persisted session issued specifically for OAuth app
// clients. TokenHash is deliberately omitted from JSON; Store methods accept no
// plaintext token and require a lowercase SHA-256 hash.
type OAuthAppSession struct {
	ID         string   `json:"id"`
	TokenHash  string   `json:"-"`
	UserID     string   `json:"userId"`
	Scopes     []string `json:"scopes"`
	ExpiresAt  string   `json:"expiresAt"`
	RevokedAt  string   `json:"revokedAt,omitempty"`
	CreatedAt  string   `json:"createdAt"`
	LastSeenAt string   `json:"lastSeenAt"`
}

const oauthAppIdentityColumns = `issuer, subject, user_id, COALESCE(email,''), COALESCE(display_name,''), created_at, updated_at`
const oauthAppSessionColumns = `id, token_hash, user_id, scopes_json, expires_at, COALESCE(revoked_at,''), created_at, COALESCE(last_seen_at,'')`

// GetOAuthAppIdentity resolves an identity only by the exact issuer and subject
// pair. Email is intentionally not accepted as a lookup key.
func (s *oauthAppStore) GetOAuthAppIdentity(ctx context.Context, issuer, subject string) (OAuthAppIdentity, error) {
	if err := validateOAuthAppIdentityKey("issuer", issuer, oauthAppIssuerMaxBytes); err != nil {
		return OAuthAppIdentity{}, err
	}
	if err := validateOAuthAppIdentityKey("subject", subject, oauthAppSubjectMaxBytes); err != nil {
		return OAuthAppIdentity{}, err
	}
	return scanOAuthAppIdentity(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT `+oauthAppIdentityColumns+` FROM oauth_app_identities WHERE issuer = ? AND subject = ?`, issuer, subject).Scan(dest...)
	})
}

// UpsertOAuthAppIdentity creates a binding or refreshes its mutable profile
// metadata. The atomic conflict clause updates only when user_id already
// matches, so an issuer/subject pair can never be rebound by an upsert race.
func (s *oauthAppStore) UpsertOAuthAppIdentity(ctx context.Context, identity OAuthAppIdentity) (OAuthAppIdentity, error) {
	canonical, err := canonicalOAuthAppIdentity(identity)
	if err != nil {
		return OAuthAppIdentity{}, err
	}
	now := Now()
	result, err := s.db.ExecContext(ctx, `
INSERT INTO oauth_app_identities (issuer, subject, user_id, email, display_name, created_at, updated_at)
VALUES (?, ?, ?, NULLIF(?,''), NULLIF(?,''), ?, ?)
ON CONFLICT(issuer, subject) DO UPDATE SET
  email = excluded.email,
  display_name = excluded.display_name,
  updated_at = excluded.updated_at
WHERE oauth_app_identities.user_id = excluded.user_id`,
		canonical.Issuer, canonical.Subject, canonical.UserID, canonical.Email, canonical.DisplayName, now, now)
	if err != nil {
		return OAuthAppIdentity{}, fmt.Errorf("upsert OAuth app identity: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return OAuthAppIdentity{}, err
	}
	stored, err := s.GetOAuthAppIdentity(ctx, canonical.Issuer, canonical.Subject)
	if err != nil {
		return OAuthAppIdentity{}, err
	}
	if affected == 0 || stored.UserID != canonical.UserID {
		return OAuthAppIdentity{}, fmt.Errorf("%w: OAuth app identity is already bound to another user", ErrConflict)
	}
	return stored, nil
}

// CreateOAuthAppSession persists only a validated token hash. CreatedAt,
// LastSeenAt, and RevokedAt are controlled by the Store rather than callers.
func (s *oauthAppStore) CreateOAuthAppSession(ctx context.Context, session OAuthAppSession) (OAuthAppSession, error) {
	canonical, scopesJSON, err := canonicalOAuthAppSessionForCreate(session)
	if err != nil {
		return OAuthAppSession{}, err
	}
	if canonical.ID == "" {
		canonical.ID = NewID()
	}
	if err := validateOAuthAppText("session id", canonical.ID, oauthAppUserIDMaxBytes, true); err != nil {
		return OAuthAppSession{}, err
	}
	now := Now()
	canonical.CreatedAt = now
	canonical.LastSeenAt = now
	canonical.RevokedAt = ""
	_, err = s.db.ExecContext(ctx, `INSERT INTO oauth_app_sessions (id, token_hash, user_id, scopes_json, expires_at, revoked_at, created_at, last_seen_at) VALUES (?, ?, ?, ?, ?, NULL, ?, ?)`,
		canonical.ID, canonical.TokenHash, canonical.UserID, scopesJSON, canonical.ExpiresAt, canonical.CreatedAt, canonical.LastSeenAt)
	if err != nil {
		if isUniqueConstraint(err) {
			return OAuthAppSession{}, fmt.Errorf("%w: OAuth app session already exists", ErrConflict)
		}
		return OAuthAppSession{}, errors.New("create OAuth app session failed")
	}
	return canonical, nil
}

// GetOAuthAppSessionByTokenHash returns only a currently effective session.
// Revoked, expired, missing, and malformed-expiry rows all fail closed.
func (s *oauthAppStore) GetOAuthAppSessionByTokenHash(ctx context.Context, tokenHash string) (OAuthAppSession, error) {
	if !validOAuthAppTokenHash(tokenHash) {
		return OAuthAppSession{}, errors.New("invalid OAuth app token hash")
	}
	now := time.Now().UTC()
	session, err := scanOAuthAppSession(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT `+oauthAppSessionColumns+` FROM oauth_app_sessions WHERE token_hash = ? AND revoked_at IS NULL AND julianday(expires_at) IS NOT NULL AND julianday(expires_at) > julianday(?)`, tokenHash, now.Format(time.RFC3339Nano)).Scan(dest...)
	})
	if err != nil {
		return OAuthAppSession{}, err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, session.ExpiresAt)
	if err != nil || !expiresAt.After(time.Now().UTC()) || session.RevokedAt != "" {
		return OAuthAppSession{}, sql.ErrNoRows
	}
	return session, nil
}

// TouchOAuthAppSession advances last_seen_at monotonically for an effective
// session. It cannot revive or mutate an expired/revoked session.
func (s *oauthAppStore) TouchOAuthAppSession(ctx context.Context, id, seenAt string) (OAuthAppSession, error) {
	id = strings.TrimSpace(id)
	if err := validateOAuthAppText("session id", id, oauthAppUserIDMaxBytes, true); err != nil {
		return OAuthAppSession{}, err
	}
	var err error
	if seenAt == "" {
		seenAt = Now()
	} else if seenAt, err = canonicalOAuthAppTime("session last seen time", seenAt); err != nil {
		return OAuthAppSession{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `UPDATE oauth_app_sessions SET last_seen_at = CASE WHEN julianday(last_seen_at) IS NULL OR julianday(last_seen_at) < julianday(?) THEN ? ELSE last_seen_at END WHERE id = ? AND revoked_at IS NULL AND julianday(expires_at) IS NOT NULL AND julianday(expires_at) > julianday(?)`, seenAt, seenAt, id, now)
	if err != nil {
		return OAuthAppSession{}, errors.New("touch OAuth app session failed")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return OAuthAppSession{}, err
	}
	if affected == 0 {
		return OAuthAppSession{}, sql.ErrNoRows
	}
	return s.getOAuthAppSessionByID(ctx, id)
}

// RevokeOAuthAppSession atomically and idempotently revokes one session.
func (s *oauthAppStore) RevokeOAuthAppSession(ctx context.Context, id string) (OAuthAppSession, error) {
	id = strings.TrimSpace(id)
	if err := validateOAuthAppText("session id", id, oauthAppUserIDMaxBytes, true); err != nil {
		return OAuthAppSession{}, err
	}
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE oauth_app_sessions SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?`, now, id)
	if err != nil {
		return OAuthAppSession{}, errors.New("revoke OAuth app session failed")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return OAuthAppSession{}, err
	}
	if affected == 0 {
		return OAuthAppSession{}, sql.ErrNoRows
	}
	return s.getOAuthAppSessionByID(ctx, id)
}

// RevokeOAuthAppSessionsForUser revokes every active OAuth app session for one
// local user in a single atomic statement and returns the number newly revoked.
func (s *oauthAppStore) RevokeOAuthAppSessionsForUser(ctx context.Context, userID string) (int64, error) {
	userID = strings.TrimSpace(userID)
	if err := validateOAuthAppText("user id", userID, oauthAppUserIDMaxBytes, true); err != nil {
		return 0, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE oauth_app_sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, Now(), userID)
	if err != nil {
		return 0, errors.New("revoke OAuth app user sessions failed")
	}
	return result.RowsAffected()
}

// CleanupExpiredOAuthAppSessions removes sessions expired at or before before.
// An empty cutoff means now. Rows with malformed/null expiration values are
// invalid sessions and are also removed.
func (s *oauthAppStore) CleanupExpiredOAuthAppSessions(ctx context.Context, before string) (int64, error) {
	var err error
	if before == "" {
		before = Now()
	} else if before, err = canonicalOAuthAppTime("session cleanup cutoff", before); err != nil {
		return 0, err
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM oauth_app_sessions WHERE julianday(expires_at) IS NULL OR julianday(expires_at) <= julianday(?)`, before)
	if err != nil {
		return 0, errors.New("cleanup expired OAuth app sessions failed")
	}
	return result.RowsAffected()
}

func (s *oauthAppStore) getOAuthAppSessionByID(ctx context.Context, id string) (OAuthAppSession, error) {
	return scanOAuthAppSession(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT `+oauthAppSessionColumns+` FROM oauth_app_sessions WHERE id = ?`, id).Scan(dest...)
	})
}

func canonicalOAuthAppIdentity(identity OAuthAppIdentity) (OAuthAppIdentity, error) {
	if err := validateOAuthAppIdentityKey("issuer", identity.Issuer, oauthAppIssuerMaxBytes); err != nil {
		return OAuthAppIdentity{}, err
	}
	if err := validateOAuthAppIdentityKey("subject", identity.Subject, oauthAppSubjectMaxBytes); err != nil {
		return OAuthAppIdentity{}, err
	}
	identity.UserID = strings.TrimSpace(identity.UserID)
	identity.Email = strings.TrimSpace(identity.Email)
	identity.DisplayName = strings.TrimSpace(identity.DisplayName)
	if err := validateOAuthAppText("user id", identity.UserID, oauthAppUserIDMaxBytes, true); err != nil {
		return OAuthAppIdentity{}, err
	}
	if err := validateOAuthAppText("email", identity.Email, oauthAppEmailMaxBytes, false); err != nil {
		return OAuthAppIdentity{}, err
	}
	if err := validateOAuthAppText("display name", identity.DisplayName, oauthAppDisplayNameMaxBytes, false); err != nil {
		return OAuthAppIdentity{}, err
	}
	identity.CreatedAt = ""
	identity.UpdatedAt = ""
	return identity, nil
}

func canonicalOAuthAppSessionForCreate(session OAuthAppSession) (OAuthAppSession, string, error) {
	session.ID = strings.TrimSpace(session.ID)
	session.UserID = strings.TrimSpace(session.UserID)
	if err := validateOAuthAppText("user id", session.UserID, oauthAppUserIDMaxBytes, true); err != nil {
		return OAuthAppSession{}, "", err
	}
	if !validOAuthAppTokenHash(session.TokenHash) {
		return OAuthAppSession{}, "", errors.New("invalid OAuth app token hash")
	}
	expiresAt, err := canonicalOAuthAppTime("session expiration time", session.ExpiresAt)
	if err != nil {
		return OAuthAppSession{}, "", err
	}
	scopes, scopesJSON, err := normalizeOAuthAppScopes(session.Scopes)
	if err != nil {
		return OAuthAppSession{}, "", err
	}
	session.Scopes = scopes
	session.ExpiresAt = expiresAt
	session.CreatedAt = ""
	session.LastSeenAt = ""
	session.RevokedAt = ""
	return session, scopesJSON, nil
}

func normalizeOAuthAppScopes(scopes []string) ([]string, string, error) {
	unique := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" || len(scope) > oauthAppScopeMaxBytes || !utf8.ValidString(scope) || strings.ContainsRune(scope, 0) {
			return nil, "", errors.New("invalid OAuth app scope")
		}
		for _, char := range scope {
			if unicode.IsControl(char) || unicode.IsSpace(char) {
				return nil, "", errors.New("invalid OAuth app scope")
			}
		}
		unique[scope] = struct{}{}
	}
	normalized := make([]string, 0, len(unique))
	for scope := range unique {
		normalized = append(normalized, scope)
	}
	sort.Strings(normalized)
	encoded, err := json.Marshal(normalized)
	if err != nil || len(encoded) > oauthAppScopesJSONMaxBytes {
		return nil, "", errors.New("OAuth app scopes are too large")
	}
	return normalized, string(encoded), nil
}

func validateOAuthAppIdentityKey(name, value string, maxBytes int) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maxBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("invalid OAuth app identity %s", name)
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return fmt.Errorf("invalid OAuth app identity %s", name)
		}
	}
	return nil
}

func validateOAuthAppText(name, value string, maxBytes int, required bool) error {
	if required && value == "" {
		return fmt.Errorf("OAuth app %s is required", name)
	}
	if value == "" {
		return nil
	}
	if len(value) > maxBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("invalid OAuth app %s", name)
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return fmt.Errorf("invalid OAuth app %s", name)
		}
	}
	return nil
}

func validOAuthAppTokenHash(tokenHash string) bool {
	if len(tokenHash) != 64 {
		return false
	}
	for _, char := range tokenHash {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func canonicalOAuthAppTime(name, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("OAuth app %s is required", name)
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", fmt.Errorf("invalid OAuth app %s", name)
	}
	return parsed.UTC().Format(time.RFC3339Nano), nil
}

func scanOAuthAppIdentity(scan func(...any) error) (OAuthAppIdentity, error) {
	var identity OAuthAppIdentity
	if err := scan(&identity.Issuer, &identity.Subject, &identity.UserID, &identity.Email, &identity.DisplayName, &identity.CreatedAt, &identity.UpdatedAt); err != nil {
		return OAuthAppIdentity{}, err
	}
	if _, err := canonicalOAuthAppIdentity(identity); err != nil {
		return OAuthAppIdentity{}, errors.New("invalid stored OAuth app identity")
	}
	if _, err := canonicalOAuthAppTime("identity created time", identity.CreatedAt); err != nil {
		return OAuthAppIdentity{}, errors.New("invalid stored OAuth app identity")
	}
	if _, err := canonicalOAuthAppTime("identity updated time", identity.UpdatedAt); err != nil {
		return OAuthAppIdentity{}, errors.New("invalid stored OAuth app identity")
	}
	return identity, nil
}

func scanOAuthAppSession(scan func(...any) error) (OAuthAppSession, error) {
	var session OAuthAppSession
	var scopesJSON string
	if err := scan(&session.ID, &session.TokenHash, &session.UserID, &scopesJSON, &session.ExpiresAt, &session.RevokedAt, &session.CreatedAt, &session.LastSeenAt); err != nil {
		return OAuthAppSession{}, err
	}
	if err := validateOAuthAppText("session id", session.ID, oauthAppUserIDMaxBytes, true); err != nil || validateOAuthAppText("user id", session.UserID, oauthAppUserIDMaxBytes, true) != nil || !validOAuthAppTokenHash(session.TokenHash) {
		return OAuthAppSession{}, errors.New("invalid stored OAuth app session")
	}
	var scopes []string
	if err := json.Unmarshal([]byte(scopesJSON), &scopes); err != nil || scopes == nil {
		return OAuthAppSession{}, errors.New("invalid stored OAuth app session scopes")
	}
	normalized, _, err := normalizeOAuthAppScopes(scopes)
	if err != nil {
		return OAuthAppSession{}, errors.New("invalid stored OAuth app session scopes")
	}
	session.Scopes = normalized
	if session.ExpiresAt, err = canonicalOAuthAppTime("session expiration time", session.ExpiresAt); err != nil {
		return OAuthAppSession{}, errors.New("invalid stored OAuth app session")
	}
	if session.CreatedAt, err = canonicalOAuthAppTime("session created time", session.CreatedAt); err != nil {
		return OAuthAppSession{}, errors.New("invalid stored OAuth app session")
	}
	if session.LastSeenAt != "" {
		if session.LastSeenAt, err = canonicalOAuthAppTime("session last seen time", session.LastSeenAt); err != nil {
			return OAuthAppSession{}, errors.New("invalid stored OAuth app session")
		}
	}
	if session.RevokedAt != "" {
		if session.RevokedAt, err = canonicalOAuthAppTime("session revoked time", session.RevokedAt); err != nil {
			return OAuthAppSession{}, errors.New("invalid stored OAuth app session")
		}
	}
	return session, nil
}
