package db

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAccountPreferencesFreshSchemaDefaultsAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "preferences.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if readUserVersion(t, ctx, store.DB()) != CurrentDBVersion {
		t.Fatalf("fresh database version = %d, want %d", readUserVersion(t, ctx, store.DB()), CurrentDBVersion)
	}
	for _, table := range []string{"account_preferences", "account_preference_claims"} {
		if !testTableExists(t, ctx, store.DB(), table) {
			t.Fatalf("fresh database is missing %s", table)
		}
	}

	preferences, err := store.GetAccountPreferences(ctx, AccountPreferenceScopeInstance, "default")
	if err != nil {
		t.Fatal(err)
	}
	if preferences.Revision != 0 || preferences.LocalStorageImportVersion != 0 || string(preferences.ProfileJSON) != "{}" || string(preferences.ModelVisibilityJSON) != "{}" || preferences.CreatedAt != "" || preferences.UpdatedAt != "" {
		t.Fatalf("unexpected safe defaults: %+v", preferences)
	}
	var rows int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM account_preferences`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("default read persisted %d rows", rows)
	}

	model := "provider:model"
	profile := json.RawMessage(`{"theme":"dark"}`)
	visibility := json.RawMessage(`{"hidden":["legacy"]}`)
	created, err := store.PatchAccountPreferences(ctx, AccountPreferenceScopeInstance, "default", AccountPreferencesPatch{ExpectedRevision: 0, ProfileJSON: &profile, PreferredModel: &model, ModelVisibilityJSON: &visibility})
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 || created.CreatedAt == "" || created.UpdatedAt == "" {
		t.Fatalf("unexpected created preferences: %+v", created)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.GetAccountPreferences(ctx, AccountPreferenceScopeInstance, "default")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Revision != 1 || persisted.PreferredModel != model || string(persisted.ProfileJSON) != string(profile) || string(persisted.ModelVisibilityJSON) != string(visibility) {
		t.Fatalf("preferences did not survive reopen: %+v", persisted)
	}
}

func TestAccountPreferencesV43Migration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "preferences-v43.db")
	seed, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.DB().ExecContext(ctx, `DROP TABLE account_preference_claims; DROP TABLE account_preferences; PRAGMA user_version = 43`); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if version := readUserVersion(t, ctx, migrated.DB()); version != CurrentDBVersion {
		t.Fatalf("migrated version = %d, want %d", version, CurrentDBVersion)
	}
	for _, table := range []string{"account_preferences", "account_preference_claims"} {
		if !testTableExists(t, ctx, migrated.DB(), table) {
			t.Fatalf("migration is missing %s", table)
		}
	}
	if _, err := migrated.DB().ExecContext(ctx, `INSERT INTO account_preferences (scope_kind, scope_id, profile_json, preferred_model, model_visibility_json, revision, local_storage_import_version, created_at, updated_at) VALUES ('instance','default','not-json','','{}',1,0,'now','now')`); err == nil {
		t.Fatal("migration schema accepted invalid JSON")
	}
	if _, err := migrated.DB().ExecContext(ctx, `INSERT INTO account_preferences (scope_kind, scope_id, profile_json, preferred_model, model_visibility_json, revision, local_storage_import_version, created_at, updated_at) VALUES ('instance','other','{}','','{}',1,0,'now','now')`); err == nil {
		t.Fatal("migration schema accepted an invalid instance scope")
	}
}

func TestPatchAccountPreferencesCASAndValidation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "preferences-cas.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	model := "model-a"
	created, err := store.PatchAccountPreferences(ctx, AccountPreferenceScopeUser, "user-a", AccountPreferencesPatch{ExpectedRevision: 0, PreferredModel: &model})
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 || created.PreferredModel != model {
		t.Fatalf("unexpected create result: %+v", created)
	}
	if _, err := store.PatchAccountPreferences(ctx, AccountPreferenceScopeUser, "user-a", AccountPreferencesPatch{ExpectedRevision: 0, PreferredModel: &model}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate expected-revision zero patch = %v, want conflict", err)
	}

	profile := json.RawMessage(`{"locale":"en"}`)
	updated, err := store.PatchAccountPreferences(ctx, AccountPreferenceScopeUser, "user-a", AccountPreferencesPatch{ExpectedRevision: 1, ProfileJSON: &profile})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.PreferredModel != model || string(updated.ProfileJSON) != string(profile) {
		t.Fatalf("unexpected CAS update: %+v", updated)
	}
	staleModel := "model-stale"
	if _, err := store.PatchAccountPreferences(ctx, AccountPreferenceScopeUser, "user-a", AccountPreferencesPatch{ExpectedRevision: 1, PreferredModel: &staleModel}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale patch = %v, want conflict", err)
	}
	missingModel := "model-missing"
	if _, err := store.PatchAccountPreferences(ctx, AccountPreferenceScopeUser, "missing", AccountPreferencesPatch{ExpectedRevision: 1, PreferredModel: &missingModel}); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing positive-revision patch = %v, want conflict", err)
	}

	invalid := json.RawMessage(`{"broken":`)
	if _, err := store.PatchAccountPreferences(ctx, AccountPreferenceScopeUser, "invalid", AccountPreferencesPatch{ExpectedRevision: 0, ProfileJSON: &invalid}); err == nil {
		t.Fatal("invalid JSON was accepted")
	}
	oversized := json.RawMessage(`"` + strings.Repeat("x", accountPreferenceMaxPayloadBytes) + `"`)
	if _, err := store.PatchAccountPreferences(ctx, AccountPreferenceScopeUser, "oversized", AccountPreferencesPatch{ExpectedRevision: 0, ProfileJSON: &oversized}); err == nil {
		t.Fatal("oversized preferences were accepted")
	}
	if _, err := store.GetAccountPreferences(ctx, AccountPreferenceScopeInstance, "other"); err == nil {
		t.Fatal("invalid instance scope id was accepted")
	}
}

func TestImportAccountPreferencesIsOneTimeAndNonDestructive(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "preferences-import.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	legacy := AccountPreferencesImport{Version: 1, ProfileJSON: json.RawMessage(`{"source":"legacy"}`), PreferredModel: "legacy-model", ModelVisibilityJSON: json.RawMessage(`{"legacy":true}`)}
	imported, applied, err := store.ImportAccountPreferences(ctx, AccountPreferenceScopeUser, "import-only", legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !applied || imported.Revision != 1 || imported.LocalStorageImportVersion != 1 || imported.PreferredModel != "legacy-model" {
		t.Fatalf("unexpected first import: applied=%v preferences=%+v", applied, imported)
	}
	repeated, applied, err := store.ImportAccountPreferences(ctx, AccountPreferenceScopeUser, "import-only", AccountPreferencesImport{Version: 1, ProfileJSON: json.RawMessage(`{"source":"newer-local"}`), PreferredModel: "newer-local", ModelVisibilityJSON: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if applied || repeated.Revision != imported.Revision || repeated.PreferredModel != imported.PreferredModel || string(repeated.ProfileJSON) != string(imported.ProfileJSON) {
		t.Fatalf("repeated import overwrote data: applied=%v preferences=%+v", applied, repeated)
	}

	officialModel := "official-model"
	officialProfile := json.RawMessage(`{"source":"server"}`)
	official, err := store.PatchAccountPreferences(ctx, AccountPreferenceScopeUser, "official", AccountPreferencesPatch{ExpectedRevision: 0, ProfileJSON: &officialProfile, PreferredModel: &officialModel})
	if err != nil {
		t.Fatal(err)
	}
	marked, applied, err := store.ImportAccountPreferences(ctx, AccountPreferenceScopeUser, "official", legacy)
	if err != nil {
		t.Fatal(err)
	}
	if applied || marked.LocalStorageImportVersion != 1 || marked.Revision != official.Revision+1 || marked.PreferredModel != officialModel || string(marked.ProfileJSON) != string(officialProfile) {
		t.Fatalf("import did not preserve official values: applied=%v preferences=%+v", applied, marked)
	}
	again, applied, err := store.ImportAccountPreferences(ctx, AccountPreferenceScopeUser, "official", legacy)
	if err != nil {
		t.Fatal(err)
	}
	if applied || again.Revision != marked.Revision || again.PreferredModel != officialModel {
		t.Fatalf("repeated marker import changed official values: applied=%v preferences=%+v", applied, again)
	}
	if _, _, err := store.ImportAccountPreferences(ctx, AccountPreferenceScopeUser, "bad-version", AccountPreferencesImport{Version: 2}); err == nil {
		t.Fatal("unsupported import version was accepted")
	}
}

func TestClaimInstanceAccountPreferencesOnlyForFirstUser(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "preferences-claim.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	instanceModel := "instance-model"
	instanceProfile := json.RawMessage(`{"theme":"instance"}`)
	instanceVisibility := json.RawMessage(`{"hidden":["x"]}`)
	if _, err := store.PatchAccountPreferences(ctx, AccountPreferenceScopeInstance, "default", AccountPreferencesPatch{ExpectedRevision: 0, ProfileJSON: &instanceProfile, PreferredModel: &instanceModel, ModelVisibilityJSON: &instanceVisibility}); err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateUser(ctx, "first", "hash")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateUser(ctx, "second", "hash")
	if err != nil {
		t.Fatal(err)
	}

	secondPreferences, claimed, err := store.ClaimInstanceAccountPreferencesForFirstUser(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed || secondPreferences.Revision != 0 || secondPreferences.PreferredModel != "" {
		t.Fatalf("second user inherited instance preferences: claimed=%v preferences=%+v", claimed, secondPreferences)
	}
	firstPreferences, claimed, err := store.ClaimInstanceAccountPreferencesForFirstUser(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !claimed || firstPreferences.Revision != 1 || firstPreferences.PreferredModel != instanceModel || string(firstPreferences.ProfileJSON) != string(instanceProfile) || string(firstPreferences.ModelVisibilityJSON) != string(instanceVisibility) {
		t.Fatalf("first user did not inherit instance preferences: claimed=%v preferences=%+v", claimed, firstPreferences)
	}
	if _, claimed, err := store.ClaimInstanceAccountPreferencesForFirstUser(ctx, first.ID); err != nil || claimed {
		t.Fatalf("repeat claim: claimed=%v err=%v", claimed, err)
	}

	firstModel := "first-only"
	if _, err := store.PatchAccountPreferences(ctx, AccountPreferenceScopeUser, first.ID, AccountPreferencesPatch{ExpectedRevision: firstPreferences.Revision, PreferredModel: &firstModel}); err != nil {
		t.Fatal(err)
	}
	isolated, err := store.GetAccountPreferences(ctx, AccountPreferenceScopeUser, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if isolated.Revision != 0 || isolated.PreferredModel != "" {
		t.Fatalf("second user observed first user's values: %+v", isolated)
	}

	if _, err := store.DB().ExecContext(ctx, `DELETE FROM users WHERE id = ?`, first.ID); err != nil {
		t.Fatal(err)
	}
	postDelete, claimed, err := store.ClaimInstanceAccountPreferencesForFirstUser(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed || postDelete.Revision != 0 || postDelete.PreferredModel != "" {
		t.Fatalf("claim was repeated after first-user deletion: claimed=%v preferences=%+v", claimed, postDelete)
	}
	var claims int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM account_preference_claims WHERE claim_kind = ?`, accountPreferenceClaimKind).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if claims != 1 {
		t.Fatalf("claim marker count = %d, want 1", claims)
	}
}

func TestClaimDoesNotOverwriteExistingFirstUserPreferences(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "preferences-existing-claim.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	instanceModel := "instance-model"
	if _, err := store.PatchAccountPreferences(ctx, AccountPreferenceScopeInstance, "default", AccountPreferencesPatch{ExpectedRevision: 0, PreferredModel: &instanceModel}); err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateUser(ctx, "existing-first", "hash")
	if err != nil {
		t.Fatal(err)
	}
	userModel := "user-model"
	existing, err := store.PatchAccountPreferences(ctx, AccountPreferenceScopeUser, first.ID, AccountPreferencesPatch{ExpectedRevision: 0, PreferredModel: &userModel})
	if err != nil {
		t.Fatal(err)
	}
	claimedPreferences, claimed, err := store.ClaimInstanceAccountPreferencesForFirstUser(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !claimed || claimedPreferences.Revision != existing.Revision || claimedPreferences.PreferredModel != userModel {
		t.Fatalf("claim overwrote existing first-user preferences: claimed=%v preferences=%+v", claimed, claimedPreferences)
	}
}

func TestOpenSecuresSQLiteFilesWithoutChangingExistingParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}
	ctx := context.Background()
	root := t.TempDir()
	existingParent := filepath.Join(root, "custom")
	if err := os.Mkdir(existingParent, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existingParent, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(existingParent, "preferences.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	assertPermission(t, existingParent, 0o750)
	assertPermission(t, path, 0o600)

	for _, sidecar := range []string{path + "-wal", path + "-shm", path + "-journal"} {
		if err := os.WriteFile(sidecar, nil, 0o666); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(sidecar, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	if err := secureSQLiteFiles(path); err != nil {
		t.Fatal(err)
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm", path + "-journal"} {
		assertPermission(t, sidecar, 0o600)
	}

	newParent := filepath.Join(root, "new", "nested")
	newStore, err := Open(ctx, filepath.Join(newParent, "new.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := newStore.Close(); err != nil {
		t.Fatal(err)
	}
	assertPermission(t, newParent, 0o700)
}

func TestOpenRejectsUnsafeSQLiteFileTypes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows symlink creation may require elevated privileges")
	}
	ctx := context.Background()

	t.Run("main database symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target.db")
		if err := os.WriteFile(target, []byte("not-a-database"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(target, 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "linked.db")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if store, err := Open(ctx, link); err == nil {
			_ = store.Close()
			t.Fatal("database symlink was accepted")
		}
		assertPermission(t, target, 0o644)
	})

	t.Run("main database directory", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "directory.db")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if store, err := Open(ctx, path); err == nil {
			_ = store.Close()
			t.Fatal("database directory was accepted")
		}
	})

	t.Run("sidecar symlink", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "safe.db")
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, "sidecar-target")
		if err := os.WriteFile(target, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(target, 0o644); err != nil {
			t.Fatal(err)
		}
		sidecar := path + "-wal"
		if err := os.Remove(sidecar); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if err := os.Symlink(target, sidecar); err != nil {
			t.Fatal(err)
		}
		if err := secureSQLiteFiles(path); err == nil {
			t.Fatal("SQLite sidecar symlink was accepted")
		}
		assertPermission(t, target, 0o644)
	})
}

func assertPermission(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s permissions = %04o, want %04o", path, got, want)
	}
}
