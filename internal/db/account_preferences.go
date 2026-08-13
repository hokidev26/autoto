package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	AccountPreferenceScopeInstance = "instance"
	AccountPreferenceScopeUser     = "user"

	accountPreferenceInstanceID      = "default"
	accountPreferenceClaimKind       = "instance_to_first_user"
	accountPreferenceImportVersion   = 1
	accountPreferenceMaxPayloadBytes = 256 * 1024
	accountPreferenceMaxModelBytes   = 1024
	accountPreferenceMaxScopeIDBytes = 128

	AccountPreferencesCurrentSetupVersion = 1
	AccountPreferencesMaxSetupVersion     = 1000
)

type AccountPreferences struct {
	ScopeKind                 string          `json:"scopeKind"`
	ScopeID                   string          `json:"scopeId"`
	ProfileJSON               json.RawMessage `json:"profileJson"`
	PreferredModel            string          `json:"preferredModel"`
	ModelVisibilityJSON       json.RawMessage `json:"modelVisibilityJson"`
	SetupVersion              int             `json:"setupVersion"`
	Revision                  int64           `json:"revision"`
	LocalStorageImportVersion int             `json:"localStorageImportVersion"`
	CreatedAt                 string          `json:"createdAt"`
	UpdatedAt                 string          `json:"updatedAt"`
}

type AccountPreferencesPatch struct {
	ExpectedRevision    int64            `json:"expectedRevision"`
	ProfileJSON         *json.RawMessage `json:"profileJson,omitempty"`
	PreferredModel      *string          `json:"preferredModel,omitempty"`
	ModelVisibilityJSON *json.RawMessage `json:"modelVisibilityJson,omitempty"`
	SetupVersion        *int             `json:"setupVersion,omitempty"`
}

type AccountPreferencesImport struct {
	Version             int             `json:"version"`
	ProfileJSON         json.RawMessage `json:"profileJson"`
	PreferredModel      string          `json:"preferredModel"`
	ModelVisibilityJSON json.RawMessage `json:"modelVisibilityJson"`
}

type accountPreferenceRowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *accountPreferenceStore) GetAccountPreferences(ctx context.Context, scopeKind, scopeID string) (AccountPreferences, error) {
	scopeKind, scopeID, err := normalizeAccountPreferenceScope(scopeKind, scopeID)
	if err != nil {
		return AccountPreferences{}, err
	}
	preferences, found, err := getAccountPreferencesRow(ctx, s.db, scopeKind, scopeID)
	if err != nil {
		return AccountPreferences{}, err
	}
	if !found {
		return defaultAccountPreferences(scopeKind, scopeID), nil
	}
	return preferences, nil
}

func (s *accountPreferenceStore) PatchAccountPreferences(ctx context.Context, scopeKind, scopeID string, patch AccountPreferencesPatch) (AccountPreferences, error) {
	scopeKind, scopeID, err := normalizeAccountPreferenceScope(scopeKind, scopeID)
	if err != nil {
		return AccountPreferences{}, err
	}
	if patch.ExpectedRevision < 0 {
		return AccountPreferences{}, errors.New("account preferences expected revision must not be negative")
	}
	if patch.SetupVersion != nil && (*patch.SetupVersion < 0 || *patch.SetupVersion > AccountPreferencesCurrentSetupVersion) {
		return AccountPreferences{}, fmt.Errorf("account preferences setup version must be between 0 and %d", AccountPreferencesCurrentSetupVersion)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AccountPreferences{}, err
	}
	defer tx.Rollback()

	if patch.ExpectedRevision == 0 {
		candidate := defaultAccountPreferences(scopeKind, scopeID)
		if err := applyAccountPreferencesPatch(&candidate, patch); err != nil {
			return AccountPreferences{}, err
		}
		candidate.Revision = 1
		candidate.CreatedAt = Now()
		candidate.UpdatedAt = candidate.CreatedAt
		if _, err := tx.ExecContext(ctx, `INSERT INTO account_preferences (scope_kind, scope_id, profile_json, preferred_model, model_visibility_json, setup_version, revision, local_storage_import_version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, candidate.ScopeKind, candidate.ScopeID, string(candidate.ProfileJSON), candidate.PreferredModel, string(candidate.ModelVisibilityJSON), candidate.SetupVersion, candidate.Revision, candidate.LocalStorageImportVersion, candidate.CreatedAt, candidate.UpdatedAt); err != nil {
			if isUniqueConstraint(err) {
				return AccountPreferences{}, fmt.Errorf("%w: account preferences changed", ErrConflict)
			}
			return AccountPreferences{}, err
		}
		if err := tx.Commit(); err != nil {
			return AccountPreferences{}, err
		}
		return candidate, nil
	}

	current, found, err := getAccountPreferencesRow(ctx, tx, scopeKind, scopeID)
	if err != nil {
		return AccountPreferences{}, err
	}
	if !found || current.Revision != patch.ExpectedRevision {
		return AccountPreferences{}, fmt.Errorf("%w: account preferences changed", ErrConflict)
	}
	candidate := current
	if err := applyAccountPreferencesPatch(&candidate, patch); err != nil {
		return AccountPreferences{}, err
	}
	candidate.UpdatedAt = Now()
	result, err := tx.ExecContext(ctx, `UPDATE account_preferences SET profile_json = ?, preferred_model = ?, model_visibility_json = ?, setup_version = ?, revision = revision + 1, updated_at = ? WHERE scope_kind = ? AND scope_id = ? AND revision = ?`, string(candidate.ProfileJSON), candidate.PreferredModel, string(candidate.ModelVisibilityJSON), candidate.SetupVersion, candidate.UpdatedAt, scopeKind, scopeID, patch.ExpectedRevision)
	if err != nil {
		return AccountPreferences{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return AccountPreferences{}, err
	}
	if affected != 1 {
		return AccountPreferences{}, fmt.Errorf("%w: account preferences changed", ErrConflict)
	}
	candidate.Revision = patch.ExpectedRevision + 1
	if err := tx.Commit(); err != nil {
		return AccountPreferences{}, err
	}
	return candidate, nil
}

func (s *accountPreferenceStore) ImportAccountPreferences(ctx context.Context, scopeKind, scopeID string, input AccountPreferencesImport) (AccountPreferences, bool, error) {
	scopeKind, scopeID, err := normalizeAccountPreferenceScope(scopeKind, scopeID)
	if err != nil {
		return AccountPreferences{}, false, err
	}
	if input.Version != accountPreferenceImportVersion {
		return AccountPreferences{}, false, fmt.Errorf("unsupported account preferences import version %d", input.Version)
	}
	profile, visibility, err := normalizeAccountPreferencePayload(input.ProfileJSON, input.PreferredModel, input.ModelVisibilityJSON)
	if err != nil {
		return AccountPreferences{}, false, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AccountPreferences{}, false, err
	}
	defer tx.Rollback()

	current, found, err := getAccountPreferencesRow(ctx, tx, scopeKind, scopeID)
	if err != nil {
		return AccountPreferences{}, false, err
	}
	if !found {
		now := Now()
		preferences := AccountPreferences{
			ScopeKind:                 scopeKind,
			ScopeID:                   scopeID,
			ProfileJSON:               profile,
			PreferredModel:            input.PreferredModel,
			ModelVisibilityJSON:       visibility,
			Revision:                  1,
			LocalStorageImportVersion: accountPreferenceImportVersion,
			CreatedAt:                 now,
			UpdatedAt:                 now,
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO account_preferences (scope_kind, scope_id, profile_json, preferred_model, model_visibility_json, revision, local_storage_import_version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?)`, scopeKind, scopeID, string(profile), input.PreferredModel, string(visibility), accountPreferenceImportVersion, now, now); err != nil {
			if isUniqueConstraint(err) {
				return AccountPreferences{}, false, fmt.Errorf("%w: account preferences changed", ErrConflict)
			}
			return AccountPreferences{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return AccountPreferences{}, false, err
		}
		return preferences, true, nil
	}
	if current.LocalStorageImportVersion >= accountPreferenceImportVersion {
		if err := tx.Commit(); err != nil {
			return AccountPreferences{}, false, err
		}
		return current, false, nil
	}

	current.UpdatedAt = Now()
	result, err := tx.ExecContext(ctx, `UPDATE account_preferences SET local_storage_import_version = ?, revision = revision + 1, updated_at = ? WHERE scope_kind = ? AND scope_id = ? AND revision = ? AND local_storage_import_version = 0`, accountPreferenceImportVersion, current.UpdatedAt, scopeKind, scopeID, current.Revision)
	if err != nil {
		return AccountPreferences{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return AccountPreferences{}, false, err
	}
	if affected != 1 {
		return AccountPreferences{}, false, fmt.Errorf("%w: account preferences changed", ErrConflict)
	}
	current.Revision++
	current.LocalStorageImportVersion = accountPreferenceImportVersion
	if err := tx.Commit(); err != nil {
		return AccountPreferences{}, false, err
	}
	return current, false, nil
}

func (s *accountPreferenceStore) ClaimInstanceAccountPreferencesForFirstUser(ctx context.Context, userID string) (AccountPreferences, bool, error) {
	userID = strings.TrimSpace(userID)
	if len(userID) == 0 || len(userID) > accountPreferenceMaxScopeIDBytes || !utf8.ValidString(userID) || strings.ContainsRune(userID, 0) {
		return AccountPreferences{}, false, errors.New("invalid account preference user id")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AccountPreferences{}, false, err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = ?`, userID).Scan(&exists); err != nil {
		return AccountPreferences{}, false, err
	}
	var firstUserID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users ORDER BY created_at ASC, id ASC LIMIT 1`).Scan(&firstUserID); err != nil {
		return AccountPreferences{}, false, err
	}
	if firstUserID != userID {
		preferences, found, err := getAccountPreferencesRow(ctx, tx, AccountPreferenceScopeUser, userID)
		if err != nil {
			return AccountPreferences{}, false, err
		}
		if !found {
			preferences = defaultAccountPreferences(AccountPreferenceScopeUser, userID)
		}
		if err := tx.Commit(); err != nil {
			return AccountPreferences{}, false, err
		}
		return preferences, false, nil
	}

	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO account_preference_claims (claim_kind, claimed_user_id, created_at) VALUES (?, ?, ?)`, accountPreferenceClaimKind, userID, Now())
	if err != nil {
		return AccountPreferences{}, false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return AccountPreferences{}, false, err
	}
	if inserted == 0 {
		preferences, found, err := getAccountPreferencesRow(ctx, tx, AccountPreferenceScopeUser, userID)
		if err != nil {
			return AccountPreferences{}, false, err
		}
		if !found {
			preferences = defaultAccountPreferences(AccountPreferenceScopeUser, userID)
		}
		if err := tx.Commit(); err != nil {
			return AccountPreferences{}, false, err
		}
		return preferences, false, nil
	}

	preferences, found, err := getAccountPreferencesRow(ctx, tx, AccountPreferenceScopeUser, userID)
	if err != nil {
		return AccountPreferences{}, false, err
	}
	if !found {
		instance, instanceFound, err := getAccountPreferencesRow(ctx, tx, AccountPreferenceScopeInstance, accountPreferenceInstanceID)
		if err != nil {
			return AccountPreferences{}, false, err
		}
		if instanceFound {
			now := Now()
			preferences = AccountPreferences{
				ScopeKind:                 AccountPreferenceScopeUser,
				ScopeID:                   userID,
				ProfileJSON:               append(json.RawMessage(nil), instance.ProfileJSON...),
				PreferredModel:            instance.PreferredModel,
				ModelVisibilityJSON:       append(json.RawMessage(nil), instance.ModelVisibilityJSON...),
				SetupVersion:              instance.SetupVersion,
				Revision:                  1,
				LocalStorageImportVersion: instance.LocalStorageImportVersion,
				CreatedAt:                 now,
				UpdatedAt:                 now,
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO account_preferences (scope_kind, scope_id, profile_json, preferred_model, model_visibility_json, setup_version, revision, local_storage_import_version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`, preferences.ScopeKind, preferences.ScopeID, string(preferences.ProfileJSON), preferences.PreferredModel, string(preferences.ModelVisibilityJSON), preferences.SetupVersion, preferences.LocalStorageImportVersion, now, now); err != nil {
				return AccountPreferences{}, false, err
			}
		} else {
			preferences = defaultAccountPreferences(AccountPreferenceScopeUser, userID)
		}
	}
	if err := tx.Commit(); err != nil {
		return AccountPreferences{}, false, err
	}
	return preferences, true, nil
}

func normalizeAccountPreferenceScope(scopeKind, scopeID string) (string, string, error) {
	scopeKind = strings.TrimSpace(scopeKind)
	scopeID = strings.TrimSpace(scopeID)
	switch scopeKind {
	case AccountPreferenceScopeInstance:
		if scopeID != accountPreferenceInstanceID {
			return "", "", errors.New("instance account preference scope id must be default")
		}
	case AccountPreferenceScopeUser:
		if scopeID == "" || len(scopeID) > accountPreferenceMaxScopeIDBytes || !utf8.ValidString(scopeID) || strings.ContainsRune(scopeID, 0) {
			return "", "", errors.New("invalid account preference user scope id")
		}
	default:
		return "", "", errors.New("invalid account preference scope")
	}
	return scopeKind, scopeID, nil
}

func defaultAccountPreferences(scopeKind, scopeID string) AccountPreferences {
	return AccountPreferences{
		ScopeKind:           scopeKind,
		ScopeID:             scopeID,
		ProfileJSON:         json.RawMessage(`{}`),
		ModelVisibilityJSON: json.RawMessage(`{}`),
	}
}

func applyAccountPreferencesPatch(preferences *AccountPreferences, patch AccountPreferencesPatch) error {
	profile := preferences.ProfileJSON
	preferredModel := preferences.PreferredModel
	visibility := preferences.ModelVisibilityJSON
	if patch.ProfileJSON != nil {
		profile = *patch.ProfileJSON
	}
	if patch.PreferredModel != nil {
		preferredModel = *patch.PreferredModel
	}
	if patch.ModelVisibilityJSON != nil {
		visibility = *patch.ModelVisibilityJSON
	}
	if patch.SetupVersion != nil {
		preferences.SetupVersion = *patch.SetupVersion
	}
	normalizedProfile, normalizedVisibility, err := normalizeAccountPreferencePayload(profile, preferredModel, visibility)
	if err != nil {
		return err
	}
	preferences.ProfileJSON = normalizedProfile
	preferences.PreferredModel = preferredModel
	preferences.ModelVisibilityJSON = normalizedVisibility
	return nil
}

func normalizeAccountPreferencePayload(profile json.RawMessage, preferredModel string, visibility json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	profile = normalizeAccountPreferenceJSONDefault(profile)
	visibility = normalizeAccountPreferenceJSONDefault(visibility)
	if !json.Valid(profile) {
		return nil, nil, errors.New("account preference profile must be valid JSON")
	}
	if !json.Valid(visibility) {
		return nil, nil, errors.New("account preference model visibility must be valid JSON")
	}
	if len(preferredModel) > accountPreferenceMaxModelBytes || !utf8.ValidString(preferredModel) || strings.ContainsRune(preferredModel, 0) {
		return nil, nil, errors.New("invalid account preference preferred model")
	}
	if len(profile)+len(preferredModel)+len(visibility) > accountPreferenceMaxPayloadBytes {
		return nil, nil, fmt.Errorf("account preferences exceed %d bytes", accountPreferenceMaxPayloadBytes)
	}
	return append(json.RawMessage(nil), profile...), append(json.RawMessage(nil), visibility...), nil
}

func normalizeAccountPreferenceJSONDefault(raw json.RawMessage) json.RawMessage {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		trimmed = `{}`
	}
	return json.RawMessage(trimmed)
}

func getAccountPreferencesRow(ctx context.Context, queryer accountPreferenceRowQueryer, scopeKind, scopeID string) (AccountPreferences, bool, error) {
	var preferences AccountPreferences
	var profile, visibility string
	err := queryer.QueryRowContext(ctx, `SELECT scope_kind, scope_id, profile_json, preferred_model, model_visibility_json, setup_version, revision, local_storage_import_version, created_at, updated_at FROM account_preferences WHERE scope_kind = ? AND scope_id = ?`, scopeKind, scopeID).Scan(&preferences.ScopeKind, &preferences.ScopeID, &profile, &preferences.PreferredModel, &visibility, &preferences.SetupVersion, &preferences.Revision, &preferences.LocalStorageImportVersion, &preferences.CreatedAt, &preferences.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AccountPreferences{}, false, nil
	}
	if err != nil {
		return AccountPreferences{}, false, err
	}
	if preferences.SetupVersion < 0 || preferences.SetupVersion > AccountPreferencesCurrentSetupVersion {
		return AccountPreferences{}, false, fmt.Errorf("stored account preferences for %s/%s have invalid setup version", scopeKind, scopeID)
	}
	normalizedProfile, normalizedVisibility, err := normalizeAccountPreferencePayload(json.RawMessage(profile), preferences.PreferredModel, json.RawMessage(visibility))
	if err != nil {
		return AccountPreferences{}, false, fmt.Errorf("stored account preferences for %s/%s are invalid: %w", scopeKind, scopeID, err)
	}
	preferences.ProfileJSON = normalizedProfile
	preferences.ModelVisibilityJSON = normalizedVisibility
	return preferences, true, nil
}
