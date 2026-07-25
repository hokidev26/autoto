package codexauth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStoreImportPersistsAndListsWithoutSecrets(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials", "codex")
	store := NewStore(dir)
	document := ImportDocument{
		Filename: "account-one.json",
		Content:  []byte(`{"type":"codex","access_token":"fixture-access","refresh_token":"rt_fixture","email":"user@example.test","account_id":"account-1","plan_type":"plus"}`),
	}
	result, err := store.Import([]ImportDocument{document})
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 1 || result.Skipped != 0 || len(result.Files) != 1 {
		t.Fatalf("unexpected import result: %+v", result)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("expected credential directory mode 0700, got %o", info.Mode().Perm())
		}
		fileInfo, err := os.Stat(filepath.Join(dir, result.Files[0]))
		if err != nil {
			t.Fatal(err)
		}
		if fileInfo.Mode().Perm() != 0o600 {
			t.Fatalf("expected credential file mode 0600, got %o", fileInfo.Mode().Perm())
		}
	}

	accounts, err := store.ListAccounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].AccountID != "account-1" || accounts[0].Email != "user@example.test" || !accounts[0].Refreshable || accounts[0].AuthKind != "oauth" {
		t.Fatalf("unexpected account summary: %+v", accounts)
	}
	publicJSON, err := json.Marshal(accounts)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fixture-access", "rt_fixture"} {
		if strings.Contains(string(publicJSON), secret) {
			t.Fatalf("secret leaked through public account summary: %s", publicJSON)
		}
	}
}

func TestSummaryClearsTokenBearingMetadataAndDerivesAuthKind(t *testing.T) {
	credential := Credential{
		ID:           "codex_MDEyMzQ1Njc4OWFiY2RlZg",
		Type:         DefaultProviderName,
		AccessToken:  "access-secret-value",
		RefreshToken: "refresh-secret-value",
		IDToken:      "id-secret-value",
		Alias:        "primary access-secret-value",
		Email:        "refresh-secret-value@example.test",
		AccountID:    "workspace-id-secret-value",
		PlanType:     "plus-access-secret-value",
	}
	summary := Summary(StoredCredential{Filename: "account.json", Credential: credential})
	if summary.Alias != "" || summary.Email != "" || summary.AccountID != "" || summary.PlanType != "" {
		t.Fatalf("token-bearing public metadata was retained: %+v", summary)
	}
	if summary.AuthKind != "oauth" || !summary.Refreshable || credential.Type != DefaultProviderName {
		t.Fatalf("unexpected derived OAuth summary or disk type mutation: summary=%+v credential=%+v", summary, credential)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"authKind":"oauth"`) {
		t.Fatalf("summary JSON omitted derived authKind: %s", encoded)
	}
	for _, secret := range []string{credential.AccessToken, credential.RefreshToken, credential.IDToken} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("summary JSON leaked %q: %s", secret, encoded)
		}
	}

	credential.RefreshToken = ""
	credential.Alias = "safe alias"
	credential.Email = "safe@example.test"
	credential.AccountID = "safe-account"
	credential.PlanType = "plus"
	summary = Summary(StoredCredential{Filename: "access-only.json", Credential: credential})
	if summary.AuthKind != "access_only" || summary.Refreshable || summary.Alias != credential.Alias || summary.Email != credential.Email || summary.AccountID != credential.AccountID || summary.PlanType != credential.PlanType {
		t.Fatalf("unexpected access-only summary: %+v", summary)
	}
}

func TestStoreLifecycleSurvivesFreshStoreInstances(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials", "codex")
	created, err := NewStore(dir).Import([]ImportDocument{{
		Filename: "restart.json",
		Content:  []byte(`{"type":"codex","access_token":"initial-access","refresh_token":"initial-refresh","account_id":"restart-account"}`),
	}})
	if err != nil || created.Imported != 1 || len(created.Files) != 1 {
		t.Fatalf("create failed: result=%+v err=%v", created, err)
	}

	afterCreate := NewStore(dir)
	items, err := afterCreate.Load()
	if err != nil || len(items) != 1 {
		t.Fatalf("fresh store did not recover create: items=%+v err=%v", items, err)
	}
	stableID := items[0].Credential.ID
	if stableID == "" || items[0].Credential.AccessToken != "initial-access" || items[0].Credential.RefreshToken != "initial-refresh" {
		t.Fatalf("fresh store recovered unexpected credential: %+v", items[0])
	}

	rotated := items[0]
	rotated.Credential.AccessToken = "rotated-access"
	rotated.Credential.RefreshToken = "rotated-refresh"
	if err := afterCreate.Update(rotated); err != nil {
		t.Fatal(err)
	}
	afterUpdate, err := NewStore(dir).GetByID(stableID)
	if err != nil {
		t.Fatal(err)
	}
	if afterUpdate.Credential.AccessToken != "rotated-access" || afterUpdate.Credential.RefreshToken != "rotated-refresh" || afterUpdate.Credential.ID != stableID {
		t.Fatalf("fresh store did not recover update: %+v", afterUpdate)
	}

	if err := NewStore(dir).Delete(stableID); err != nil {
		t.Fatal(err)
	}
	deletedStore := NewStore(dir)
	if _, err := deletedStore.GetByID(stableID); !os.IsNotExist(err) {
		t.Fatalf("fresh store recovered deleted credential: %v", err)
	}
	remaining, err := deletedStore.Load()
	if err != nil || len(remaining) != 0 {
		t.Fatalf("fresh store retained deleted credential: items=%+v err=%v", remaining, err)
	}
}

func TestStoreExportByIDRoundTripsCompleteCredential(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "source"))
	result, err := store.Import([]ImportDocument{{
		Filename: "backup.json",
		Content:  []byte(`{"type":"codex","access_token":"export-access","refresh_token":"rt_export","email":"export@example.test","account_id":"export-account","alias":"Primary","priority":7,"disabled":true}`),
	}})
	if err != nil || len(result.Files) != 1 {
		t.Fatalf("import failed: result=%+v err=%v", result, err)
	}
	accounts, err := store.ListAccounts()
	if err != nil || len(accounts) != 1 {
		t.Fatalf("list failed: accounts=%+v err=%v", accounts, err)
	}

	document, err := store.ExportByID(accounts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if document.Filename != result.Files[0] || !json.Valid(document.Content) || !strings.HasSuffix(string(document.Content), "\n") {
		t.Fatalf("unexpected export document: filename=%q content=%q", document.Filename, document.Content)
	}
	for _, secret := range []string{"export-access", "rt_export"} {
		if !strings.Contains(string(document.Content), secret) {
			t.Fatalf("export omitted credential material %q: %s", secret, document.Content)
		}
	}
	var exported Credential
	if err := json.Unmarshal(document.Content, &exported); err != nil {
		t.Fatal(err)
	}
	if exported.ID != accounts[0].ID || exported.Alias != "Primary" || exported.Priority != 7 || !exported.Disabled {
		t.Fatalf("export lost account metadata: %+v", exported)
	}

	restored := NewStore(filepath.Join(t.TempDir(), "restored"))
	if restoredResult, err := restored.Import([]ImportDocument{document}); err != nil || restoredResult.Imported != 1 {
		t.Fatalf("export did not round-trip: result=%+v err=%v", restoredResult, err)
	}
	if _, err := store.ExportByID("codex_missing"); !os.IsNotExist(err) {
		t.Fatalf("missing export error = %v, want os.ErrNotExist", err)
	}
}

func TestStoreMigratesStableIDAndSupportsMetadataLifecycle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "legacy.json")
	if err := os.WriteFile(path, []byte(`{"id":"account-legacy","type":"codex","access_token":"legacy-access","account_id":"account-legacy"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewStore(dir)
	first, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Credential.ID == "" || first[0].Credential.ID == "account-legacy" || first[0].Credential.Priority != DefaultPriority {
		t.Fatalf("legacy credential was not migrated: %+v", first)
	}
	stableID := first[0].Credential.ID
	alias := "Primary <account>"
	priority := 12
	disabled := true
	updated, err := store.UpdateMetadata(stableID, MetadataUpdate{Alias: &alias, Priority: &priority, Disabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Credential.Alias != alias || updated.Credential.Priority != priority || !updated.Credential.Disabled {
		t.Fatalf("metadata update did not persist: %+v", updated)
	}
	result, err := store.Import([]ImportDocument{{Filename: "renamed.json", Content: []byte(`{"type":"codex","access_token":"rotated-access","refresh_token":"rt_rotated","account_id":"account-legacy"}`)}})
	if err != nil || result.Imported != 1 {
		t.Fatalf("reimport failed: result=%+v err=%v", result, err)
	}
	reimported, err := store.GetByID(stableID)
	if err != nil {
		t.Fatal(err)
	}
	if reimported.Credential.AccessToken != "rotated-access" || reimported.Credential.ID != stableID || reimported.Credential.Alias != alias || reimported.Credential.Priority != priority || !reimported.Credential.Disabled {
		t.Fatalf("reimport did not preserve management identity: %+v", reimported)
	}
	if err := store.Delete(stableID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetByID(stableID); !os.IsNotExist(err) {
		t.Fatalf("deleted credential remains addressable: %v", err)
	}
}

func TestStoreCanonicalizesSymlinkedParentBeforeWriting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	realParent := filepath.Join(root, "real")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedParent := filepath.Join(root, "linked")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(linkedParent, "codex"))
	if strings.Contains(store.Dir(), linkedParent) {
		t.Fatalf("store retained a symlinked parent: %s", store.Dir())
	}
	result, err := store.Import([]ImportDocument{{Filename: "account.json", Content: []byte(`{"type":"codex","access_token":"fixture","account_id":"account"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("unexpected import result: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(realParent, "codex", result.Files[0])); err != nil {
		t.Fatalf("credential was not written through the canonical path: %v", err)
	}
}

func TestStoreFilenameCollisionNeverOverwritesDifferentIdentity(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "codex"))
	first, err := store.Import([]ImportDocument{{Filename: "shared.json", Content: []byte(`{"type":"codex","access_token":"first-access","account_id":"first-account","alias":"First","priority":7,"disabled":true}`)}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Import([]ImportDocument{{Filename: "shared.json", Content: []byte(`{"type":"codex","access_token":"second-access","account_id":"second-account"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Files) != 1 || len(second.Files) != 1 || first.Files[0] == second.Files[0] {
		t.Fatalf("filename collision did not allocate a unique file: first=%+v second=%+v", first, second)
	}
	items, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected two distinct credentials, got %+v", items)
	}
	byAccount := map[string]Credential{}
	for _, item := range items {
		byAccount[item.Credential.AccountID] = item.Credential
	}
	if byAccount["first-account"].AccessToken != "first-access" || byAccount["first-account"].Alias != "First" || byAccount["first-account"].Priority != 7 || !byAccount["first-account"].Disabled {
		t.Fatalf("first account was overwritten: %+v", byAccount["first-account"])
	}
	if byAccount["second-account"].AccessToken != "second-access" || byAccount["second-account"].ID == byAccount["first-account"].ID || byAccount["second-account"].Alias != "" || byAccount["second-account"].Priority != DefaultPriority || byAccount["second-account"].Disabled {
		t.Fatalf("second account inherited metadata: %+v", byAccount["second-account"])
	}
}

func TestStoreUpdateUsesIDAndPreservesManagementMetadata(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "codex"))
	result, err := store.Import([]ImportDocument{{
		Filename: "account.json",
		Content:  []byte(`{"type":"codex","access_token":"initial-access","account_id":"account-1","alias":"Imported alias","priority":9,"disabled":true}`),
	}})
	if err != nil || len(result.Files) != 1 {
		t.Fatalf("initial import failed: result=%+v err=%v", result, err)
	}
	items, err := store.Load()
	if err != nil || len(items) != 1 {
		t.Fatalf("load failed: items=%+v err=%v", items, err)
	}
	item := items[0]
	item.Filename = filepath.Join("..", "outside.json")
	item.Credential.AccessToken = "rotated-access"
	item.Credential.Alias = "attacker alias"
	item.Credential.Priority = 1
	item.Credential.Disabled = false
	if err := store.Update(item); err != nil {
		t.Fatal(err)
	}
	updated, err := store.GetByID(items[0].Credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Filename != "account.json" || updated.Credential.AccessToken != "rotated-access" || updated.Credential.Alias != "Imported alias" || updated.Credential.Priority != 9 || !updated.Credential.Disabled {
		t.Fatalf("ID update changed path or management metadata: %+v", updated)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(store.Dir()), "outside.json")); !os.IsNotExist(err) {
		t.Fatalf("update trusted caller-controlled filename: %v", err)
	}
}

func TestStoreMigratesDuplicateLegacyIDsWithoutChangingFirst(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	duplicateID := "codex_MDEyMzQ1Njc4OWFiY2RlZg"
	for name, accountID := range map[string]string{"a.json": "account-a", "b.json": "account-b"} {
		content := []byte(`{"id":"` + duplicateID + `","type":"codex","access_token":"fixture-` + accountID + `","account_id":"` + accountID + `"}`)
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store := NewStore(dir)
	first, err := store.Load()
	if err != nil || len(first) != 2 {
		t.Fatalf("migration failed: items=%+v err=%v", first, err)
	}
	if first[0].Credential.ID != duplicateID || first[1].Credential.ID == duplicateID || first[0].Credential.ID == first[1].Credential.ID {
		t.Fatalf("duplicate IDs were not migrated deterministically: %+v", first)
	}
	second, err := store.Load()
	if err != nil || second[0].Credential.ID != first[0].Credential.ID || second[1].Credential.ID != first[1].Credential.ID {
		t.Fatalf("migrated IDs were not stable: first=%+v second=%+v err=%v", first, second, err)
	}
}

func TestStoreRejectsSymlinkCredentialTargets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	dir := filepath.Join(t.TempDir(), "codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.json")
	original := []byte(`{"type":"codex","access_token":"outside","account_id":"outside"}`)
	if err := os.WriteFile(target, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "linked.json")); err != nil {
		t.Fatal(err)
	}
	store := NewStore(dir)
	items, err := store.Load()
	if err != nil || len(items) != 0 {
		t.Fatalf("symlink credential was loaded: items=%+v err=%v", items, err)
	}
	if _, err := store.Import([]ImportDocument{{Filename: "linked.json", Content: []byte(`{"type":"codex","access_token":"inside","account_id":"inside"}`)}}); err == nil {
		t.Fatal("expected import over a symlink credential target to fail")
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != string(original) {
		t.Fatalf("external symlink target changed: %q err=%v", contents, err)
	}
}

func TestStoreImportDeduplicatesAndUpdatesSameAccount(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "codex"))
	first := ImportDocument{Filename: "one.json", Content: []byte(`{"type":"codex","access_token":"access-one","refresh_token":"rt_one","account_id":"account-1"}`)}
	result, err := store.Import([]ImportDocument{first})
	if err != nil || result.Imported != 1 {
		t.Fatalf("first import failed: result=%+v err=%v", result, err)
	}
	result, err = store.Import([]ImportDocument{first})
	if err != nil || result.Imported != 0 || result.Skipped != 1 {
		t.Fatalf("duplicate import mismatch: result=%+v err=%v", result, err)
	}

	updated := ImportDocument{Filename: "renamed.json", Content: []byte(`{"type":"codex","access_token":"access-two","refresh_token":"rt_two","account_id":"account-1"}`)}
	result, err = store.Import([]ImportDocument{updated})
	if err != nil || result.Imported != 1 || result.Skipped != 0 || len(result.Files) != 1 || result.Files[0] != "one.json" {
		t.Fatalf("updated import mismatch: result=%+v err=%v", result, err)
	}
	items, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Credential.AccessToken != "access-two" || items[0].Credential.RefreshToken != "rt_two" {
		t.Fatalf("credential was not updated in place: %+v", items)
	}
}

func TestStoreBatchMetadataAndDeleteReturnPerIDResults(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "codex"))
	_, err := store.Import([]ImportDocument{
		{Filename: "one.json", Content: []byte(`{"type":"codex","access_token":"batch-secret-one","account_id":"batch-account-one"}`)},
		{Filename: "two.json", Content: []byte(`{"type":"codex","access_token":"batch-secret-two","account_id":"batch-account-two"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.Load()
	if err != nil || len(items) != 2 {
		t.Fatalf("load failed: items=%+v err=%v", items, err)
	}
	missingID := "codex_MDEyMzQ1Njc4OWFiY2RlZg"
	priority := 23
	disabled := true
	updates, err := store.BatchUpdateMetadata(
		[]string{items[0].Credential.ID, missingID, items[1].Credential.ID},
		MetadataUpdate{Priority: &priority, Disabled: &disabled},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 3 || updates[0].Err != nil || !os.IsNotExist(updates[1].Err) || updates[2].Err != nil {
		t.Fatalf("unexpected metadata results: %+v", updates)
	}
	for _, index := range []int{0, 2} {
		if updates[index].Item.Credential.Priority != priority || !updates[index].Item.Credential.Disabled {
			t.Fatalf("metadata not returned for result %d: %+v", index, updates[index])
		}
		stored, err := store.GetByID(updates[index].ID)
		if err != nil || stored.Credential.Priority != priority || !stored.Credential.Disabled {
			t.Fatalf("metadata not persisted for %s: %+v err=%v", updates[index].ID, stored, err)
		}
	}

	deletes, err := store.BatchDelete([]string{items[0].Credential.ID, missingID, items[1].Credential.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(deletes) != 3 || !deletes[0].Deleted || deletes[0].Err != nil || !os.IsNotExist(deletes[1].Err) || !deletes[2].Deleted || deletes[2].Err != nil {
		t.Fatalf("unexpected delete results: %+v", deletes)
	}
	remaining, err := store.Load()
	if err != nil || len(remaining) != 0 {
		t.Fatalf("batch delete retained credentials: %+v err=%v", remaining, err)
	}
}
