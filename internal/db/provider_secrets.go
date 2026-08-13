package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"autoto/internal/secrets"
)

const (
	ProviderSecretPendingNone   = secrets.ProviderSecretPendingNone
	ProviderSecretPendingSet    = secrets.ProviderSecretPendingSet
	ProviderSecretPendingClear  = secrets.ProviderSecretPendingClear
	ProviderSecretPendingDelete = secrets.ProviderSecretPendingDelete
)

var (
	ErrProviderSecretNotFound       = sql.ErrNoRows
	ErrProviderSecretPendingMissing = errors.New("provider secret pending change not found")
	ErrProviderSecretPendingNone    = errors.New("provider secret has no pending change")
)

type ProviderSecretRecord = secrets.ProviderSecretRecord
type ProviderSecretPending = secrets.ProviderSecretPending

func (s *Store) GetProviderSecret(ctx context.Context, name, kind string) (ProviderSecretRecord, error) {
	if err := ensureProviderSecretStore(s); err != nil {
		return ProviderSecretRecord{}, err
	}
	name, kind, err := validateProviderSecretKey(name, kind)
	if err != nil {
		return ProviderSecretRecord{}, err
	}
	return scanProviderSecret(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, providerSecretSelectSQL+` WHERE provider_name = ? AND secret_kind = ?`, name, kind).Scan(dest...)
	})
}

func (s *Store) ListProviderSecrets(ctx context.Context) ([]ProviderSecretRecord, error) {
	if err := ensureProviderSecretStore(s); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, providerSecretSelectSQL+` ORDER BY provider_name, secret_kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ProviderSecretRecord, 0)
	for rows.Next() {
		record, err := scanProviderSecret(func(dest ...any) error { return rows.Scan(dest...) })
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

// CountProviderSecrets reports how many records hold encrypted material that
// depends on the vault key (an active ciphertext or a pending set). The vault
// uses this as its key-regeneration guard, so rows that only carry a pending
// clear/delete action must not be counted: they contain no ciphertext, and
// counting them made the guard refuse to create the very first key when a
// clear was prepared before the first set in the same request (e.g. saving
// request headers on a fresh install before any API key ever existed).
func (s *Store) CountProviderSecrets(ctx context.Context) (int, error) {
	if err := ensureProviderSecretStore(s); err != nil {
		return 0, err
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_secrets WHERE active_ciphertext IS NOT NULL OR pending_ciphertext IS NOT NULL`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) PutProviderSecretPending(ctx context.Context, pending ProviderSecretPending) error {
	if err := ensureProviderSecretStore(s); err != nil {
		return err
	}
	name, kind, err := validateProviderSecretKey(pending.ProviderName, pending.SecretKind)
	if err != nil {
		return err
	}
	pending.Action = strings.TrimSpace(pending.Action)
	if !validProviderSecretPendingAction(pending.Action) {
		return errors.New("invalid provider secret pending action")
	}
	if !utf8.ValidString(pending.LastFive) || utf8.RuneCountInString(pending.LastFive) > 5 {
		return errors.New("provider secret last_five is too long")
	}
	switch pending.Action {
	case ProviderSecretPendingSet:
		if len(pending.Ciphertext) == 0 || len(pending.Nonce) == 0 || len(pending.BindingFingerprint) == 0 {
			return errors.New("provider secret set requires encrypted material and binding")
		}
		if pending.KeyVersion <= 0 || pending.SecretRevision <= 0 {
			return errors.New("provider secret set requires positive key version and revision")
		}
	case ProviderSecretPendingClear:
		if len(pending.BindingFingerprint) == 0 || pending.SecretRevision <= 0 {
			return errors.New("provider secret clear requires binding and revision")
		}
		pending.Ciphertext = nil
		pending.Nonce = nil
		pending.KeyVersion = 0
		pending.LastFive = ""
	case ProviderSecretPendingDelete:
		pending.Ciphertext = nil
		pending.Nonce = nil
		pending.BindingFingerprint = nil
		pending.KeyVersion = 0
		pending.LastFive = ""
		pending.SecretRevision = 0
	case ProviderSecretPendingNone:
		pending.Ciphertext = nil
		pending.Nonce = nil
		pending.BindingFingerprint = nil
		pending.KeyVersion = 0
		pending.LastFive = ""
		pending.SecretRevision = 0
	}
	now := Now()
	_, err = s.db.ExecContext(ctx, `
INSERT INTO provider_secrets (
  provider_name, secret_kind, pending_action, pending_ciphertext, pending_nonce,
  pending_binding_fingerprint, pending_key_version, pending_last_five,
  pending_secret_revision, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(provider_name, secret_kind) DO UPDATE SET
  pending_action = excluded.pending_action,
  pending_ciphertext = excluded.pending_ciphertext,
  pending_nonce = excluded.pending_nonce,
  pending_binding_fingerprint = excluded.pending_binding_fingerprint,
  pending_key_version = excluded.pending_key_version,
  pending_last_five = excluded.pending_last_five,
  pending_secret_revision = excluded.pending_secret_revision,
  updated_at = excluded.updated_at
`, name, kind, pending.Action, pending.Ciphertext, pending.Nonce, pending.BindingFingerprint, pending.KeyVersion, pending.LastFive, pending.SecretRevision, now, now)
	return err
}

func (s *Store) CommitProviderSecretPending(ctx context.Context, name, kind string) error {
	if err := ensureProviderSecretStore(s); err != nil {
		return err
	}
	name, kind, err := validateProviderSecretKey(name, kind)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin provider secret commit: %w", err)
	}
	defer tx.Rollback()
	var action string
	var ciphertext, nonce, bindingFingerprint []byte
	var keyVersion, secretRevision int64
	var lastFive string
	err = tx.QueryRowContext(ctx, `SELECT pending_action, pending_ciphertext, pending_nonce, pending_binding_fingerprint, pending_key_version, pending_last_five, pending_secret_revision FROM provider_secrets WHERE provider_name = ? AND secret_kind = ?`, name, kind).Scan(&action, &ciphertext, &nonce, &bindingFingerprint, &keyVersion, &lastFive, &secretRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrProviderSecretPendingMissing
	}
	if err != nil {
		return fmt.Errorf("read provider secret pending change: %w", err)
	}
	switch action {
	case ProviderSecretPendingSet:
		if len(ciphertext) == 0 || len(nonce) == 0 || keyVersion <= 0 || secretRevision <= 0 {
			return errors.New("stored provider secret pending set is invalid")
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE provider_secrets
SET active_ciphertext = ?, active_nonce = ?, active_binding_fingerprint = ?, active_key_version = ?, active_last_five = ?, active_secret_revision = ?,
    pending_action = 'none', pending_ciphertext = NULL, pending_nonce = NULL, pending_binding_fingerprint = NULL,
    pending_key_version = 0, pending_last_five = '', pending_secret_revision = 0, updated_at = ?
WHERE provider_name = ? AND secret_kind = ?
`, ciphertext, nonce, bindingFingerprint, keyVersion, lastFive, secretRevision, Now(), name, kind); err != nil {
			return fmt.Errorf("commit provider secret pending set: %w", err)
		}
	case ProviderSecretPendingClear, ProviderSecretPendingDelete:
		if _, err := tx.ExecContext(ctx, `DELETE FROM provider_secrets WHERE provider_name = ? AND secret_kind = ?`, name, kind); err != nil {
			return fmt.Errorf("commit provider secret pending deletion: %w", err)
		}
	case ProviderSecretPendingNone:
		return ErrProviderSecretPendingNone
	default:
		return errors.New("stored provider secret pending action is invalid")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit provider secret pending change: %w", err)
	}
	return nil
}

func (s *Store) RollbackProviderSecretPending(ctx context.Context, name, kind string) error {
	if err := ensureProviderSecretStore(s); err != nil {
		return err
	}
	name, kind, err := validateProviderSecretKey(name, kind)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin provider secret rollback: %w", err)
	}
	defer tx.Rollback()
	var action string
	var activeCiphertext []byte
	err = tx.QueryRowContext(ctx, `SELECT pending_action, active_ciphertext FROM provider_secrets WHERE provider_name = ? AND secret_kind = ?`, name, kind).Scan(&action, &activeCiphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrProviderSecretPendingMissing
	}
	if err != nil {
		return fmt.Errorf("read provider secret for rollback: %w", err)
	}
	if action == ProviderSecretPendingNone {
		return ErrProviderSecretPendingNone
	}
	if activeCiphertext == nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM provider_secrets WHERE provider_name = ? AND secret_kind = ?`, name, kind); err != nil {
			return fmt.Errorf("delete pending-only provider secret: %w", err)
		}
	} else if _, err := tx.ExecContext(ctx, `
UPDATE provider_secrets
SET pending_action = 'none', pending_ciphertext = NULL, pending_nonce = NULL, pending_binding_fingerprint = NULL,
    pending_key_version = 0, pending_last_five = '', pending_secret_revision = 0, updated_at = ?
WHERE provider_name = ? AND secret_kind = ?
`, Now(), name, kind); err != nil {
		return fmt.Errorf("clear provider secret pending change: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit provider secret rollback: %w", err)
	}
	return nil
}

func (s *Store) DeleteProviderSecret(ctx context.Context, name, kind string) error {
	if err := ensureProviderSecretStore(s); err != nil {
		return err
	}
	name, kind, err := validateProviderSecretKey(name, kind)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM provider_secrets WHERE provider_name = ? AND secret_kind = ?`, name, kind)
	return err
}

const providerSecretSelectSQL = `SELECT provider_name, secret_kind, active_ciphertext, active_nonce, active_binding_fingerprint, active_key_version, active_last_five, active_secret_revision, pending_action, pending_ciphertext, pending_nonce, pending_binding_fingerprint, pending_key_version, pending_last_five, pending_secret_revision, created_at, updated_at FROM provider_secrets`

type providerSecretScanner func(...any) error

func scanProviderSecret(scan providerSecretScanner) (ProviderSecretRecord, error) {
	var record ProviderSecretRecord
	if err := scan(
		&record.ProviderName, &record.SecretKind,
		&record.ActiveCiphertext, &record.ActiveNonce, &record.ActiveBindingFingerprint,
		&record.ActiveKeyVersion, &record.ActiveLastFive, &record.ActiveSecretRevision,
		&record.PendingAction, &record.PendingCiphertext, &record.PendingNonce, &record.PendingBindingFingerprint,
		&record.PendingKeyVersion, &record.PendingLastFive, &record.PendingSecretRevision,
		&record.CreatedAt, &record.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProviderSecretRecord{}, ErrProviderSecretNotFound
		}
		return ProviderSecretRecord{}, err
	}
	return record, nil
}

func ensureProviderSecretStore(s *Store) error {
	if s == nil || s.db == nil {
		return errors.New("database store is unavailable")
	}
	return nil
}

func validateProviderSecretKey(name, kind string) (string, string, error) {
	name = strings.TrimSpace(name)
	kind = strings.TrimSpace(kind)
	if name == "" || kind == "" || len([]byte(name)) > 128 || len([]byte(kind)) > 128 || strings.ContainsRune(name, 0) || strings.ContainsRune(kind, 0) {
		return "", "", errors.New("invalid provider secret key")
	}
	return name, kind, nil
}

func validProviderSecretPendingAction(action string) bool {
	switch action {
	case ProviderSecretPendingNone, ProviderSecretPendingSet, ProviderSecretPendingClear, ProviderSecretPendingDelete:
		return true
	default:
		return false
	}
}
