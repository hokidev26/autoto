package anthropicauth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestDefaultStoreDir(t *testing.T) {
	if got := DefaultStoreDir(" /home/example "); got != filepath.Join("/home/example", "credentials", "anthropic") {
		t.Fatalf("unexpected default store dir: %q", got)
	}
	if got := DefaultStoreDir("  "); got != "" {
		t.Fatalf("empty home should produce empty path, got %q", got)
	}
}

func TestStoreCreatesProfileAndAPIKeyWithPrivatePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials", "anthropic")
	store := NewStore(dir)
	profile, err := store.Create(CreateRequest{AuthType: AuthTypeProfile, Profile: "work-profile", Alias: "Work", Priority: 20})
	if err != nil {
		t.Fatal(err)
	}
	secret := "sk-ant-api03-fixture-secret-value"
	apiKey, err := store.Create(Credential{AuthType: AuthTypeAPIKey, APIKey: secret})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Credential.ID == "" || apiKey.Credential.ID == "" || profile.Credential.ID == apiKey.Credential.ID {
		t.Fatalf("credentials did not receive distinct IDs: profile=%q api=%q", profile.Credential.ID, apiKey.Credential.ID)
	}
	if strings.Contains(apiKey.Credential.ID, secret) || strings.Contains(apiKey.Credential.ID, base64.RawURLEncoding.EncodeToString([]byte(secret))) {
		t.Fatalf("API key was used as a reversible ID: %q", apiKey.Credential.ID)
	}
	if profile.Credential.APIKey != "" || profile.Credential.Profile != "work-profile" {
		t.Fatalf("profile credential stored unexpected authentication material: %+v", profile.Credential)
	}
	if apiKey.Credential.Profile != "" || apiKey.Credential.APIKey != secret {
		t.Fatalf("API key credential was not stored correctly: %+v", apiKey.Credential)
	}
	if profile.Credential.CreatedAt == "" || profile.Credential.UpdatedAt == "" {
		t.Fatalf("profile timestamps missing: %+v", profile.Credential)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("expected directory mode 0700, got %o", info.Mode().Perm())
		}
		for _, item := range []StoredCredential{profile, apiKey} {
			info, err := os.Stat(filepath.Join(dir, item.Filename))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("expected file mode 0600, got %o", info.Mode().Perm())
			}
		}
	}

	reloaded, err := NewStore(dir).GetByID(apiKey.Credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Credential.APIKey != secret || reloaded.Credential.AuthType != AuthTypeAPIKey {
		t.Fatalf("persisted API key did not reload: %+v", reloaded.Credential)
	}
}

func TestListSortsCandidatesAndExcludesDisabled(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "anthropic"))
	first, err := store.Create(CreateRequest{AuthType: AuthTypeProfile, Profile: "first", Priority: 30})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(CreateRequest{AuthType: AuthTypeProfile, Profile: "second", Priority: 5})
	if err != nil {
		t.Fatal(err)
	}
	third, err := store.Create(CreateRequest{AuthType: AuthTypeProfile, Profile: "third", Priority: 5})
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := store.Create(CreateRequest{AuthType: AuthTypeAPIKey, APIKey: "disabled-secret", Priority: 1, Disabled: true})
	if err != nil {
		t.Fatal(err)
	}

	candidates, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 3 {
		t.Fatalf("expected three enabled candidates, got %+v", candidates)
	}
	expectedTie := []string{second.Credential.ID, third.Credential.ID}
	sort.Strings(expectedTie)
	if candidates[0].Credential.ID != expectedTie[0] || candidates[1].Credential.ID != expectedTie[1] || candidates[2].Credential.ID != first.Credential.ID {
		t.Fatalf("candidates not sorted by priority and ID: %+v", candidates)
	}
	for _, candidate := range candidates {
		if candidate.Credential.ID == disabled.Credential.ID || candidate.Credential.Disabled {
			t.Fatalf("disabled credential became a candidate: %+v", candidate)
		}
	}
	accounts, err := store.ListAccounts()
	if err != nil || len(accounts) != 4 {
		t.Fatalf("account listing should include disabled records: accounts=%+v err=%v", accounts, err)
	}
	if !store.Configured() {
		t.Fatal("store with enabled candidates should be configured")
	}
	for _, item := range candidates {
		value := true
		if _, err := store.UpdateMetadata(item.Credential.ID, MetadataUpdate{Disabled: &value}); err != nil {
			t.Fatal(err)
		}
	}
	if store.Configured() {
		t.Fatal("store with only disabled accounts should not be configured")
	}
}

func TestMetadataUpdateAndDeleteLifecycle(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "anthropic"))
	created, err := store.Create(CreateRequest{AuthType: AuthTypeProfile, Profile: "default"})
	if err != nil {
		t.Fatal(err)
	}
	alias := "Primary"
	priority := 7
	disabled := true
	updated, err := store.UpdateMetadata(created.Credential.ID, MetadataUpdate{Alias: &alias, Priority: &priority, Disabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Credential.Alias != alias || updated.Credential.Priority != priority || !updated.Credential.Disabled {
		t.Fatalf("metadata update did not persist: %+v", updated.Credential)
	}
	if updated.Credential.CreatedAt != created.Credential.CreatedAt || updated.Credential.UpdatedAt < created.Credential.UpdatedAt {
		t.Fatalf("metadata update changed creation time or regressed update time: before=%+v after=%+v", created.Credential, updated.Credential)
	}
	if err := store.Delete(created.Credential.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetByID(created.Credential.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted credential is still addressable: %v", err)
	}
	if err := store.Delete(created.Credential.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second delete should report not found: %v", err)
	}
}

func TestDuplicateImportsDeduplicateProfilesAndAPIKeys(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "anthropic"))
	profile := CreateRequest{AuthType: AuthTypeProfile, Profile: "team-profile", Alias: "First alias"}
	firstProfile, err := store.Create(profile)
	if err != nil {
		t.Fatal(err)
	}
	duplicateProfile, err := store.Create(CreateRequest{AuthType: AuthTypeProfile, Profile: "team-profile", Alias: "Ignored alias", Priority: 1})
	if err != nil {
		t.Fatal(err)
	}
	if duplicateProfile.Credential.ID != firstProfile.Credential.ID || duplicateProfile.Credential.Alias != "First alias" {
		t.Fatalf("duplicate profile did not preserve the original record: first=%+v duplicate=%+v", firstProfile, duplicateProfile)
	}

	secret := "sk-ant-duplicate-fixture"
	result, err := store.Import([]ImportDocument{{Filename: "key.json", Content: []byte(`{"auth_type":"api_key","api_key":"` + secret + `"}`)}})
	if err != nil || result.Imported != 1 || result.Skipped != 0 || len(result.Files) != 1 || result.Files[0] != "key.json" {
		t.Fatalf("initial API key import mismatch: result=%+v err=%v", result, err)
	}
	result, err = store.Import([]Credential{{AuthType: AuthTypeAPIKey, APIKey: secret}})
	if err != nil || result.Imported != 0 || result.Skipped != 1 {
		t.Fatalf("duplicate API key import mismatch: result=%+v err=%v", result, err)
	}
	items, err := store.Load()
	if err != nil || len(items) != 2 {
		t.Fatalf("duplicates created extra records: items=%+v err=%v", items, err)
	}
}

func TestFilenameCollisionDoesNotOverwriteAnotherAccount(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "anthropic"))
	first, err := store.Import(ImportDocument{Filename: "shared.json", Content: []byte(`{"auth_type":"profile","profile":"first"}`)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Import(ImportDocument{Filename: "shared.json", Content: []byte(`{"auth_type":"profile","profile":"second"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Files) != 1 || len(second.Files) != 1 || first.Files[0] == second.Files[0] {
		t.Fatalf("filename collision overwrote an account: first=%+v second=%+v", first, second)
	}
	items, err := store.Load()
	if err != nil || len(items) != 2 {
		t.Fatalf("expected two records after collision: items=%+v err=%v", items, err)
	}
	profiles := []string{items[0].Credential.Profile, items[1].Credential.Profile}
	sort.Strings(profiles)
	if strings.Join(profiles, ",") != "first,second" {
		t.Fatalf("unexpected profiles after collision: %v", profiles)
	}
}

func TestSummaryAndErrorsNeverLeakAPIKey(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "anthropic"))
	secret := "sk-ant-super-sensitive-test-key"
	item, err := store.Create(CreateRequest{AuthType: AuthTypeAPIKey, APIKey: secret, Alias: "Safe alias"})
	if err != nil {
		t.Fatal(err)
	}
	summary := Summary(item)
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(strings.ToLower(string(encoded)), "api_key\":") {
		t.Fatalf("summary leaked API key material: %s", encoded)
	}
	accounts, err := store.ListAccounts()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(accounts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("account list leaked API key material: %s", encoded)
	}

	tooLongSecret := secret + strings.Repeat("x", maxAPIKeyBytes)
	_, err = store.Create(CreateRequest{AuthType: AuthTypeAPIKey, APIKey: tooLongSecret})
	if err == nil {
		t.Fatal("expected oversized API key to fail")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), tooLongSecret) {
		t.Fatalf("validation error leaked API key: %v", err)
	}

	badPath := filepath.Join(store.Dir(), "broken.json")
	if err := os.WriteFile(badPath, []byte(`{"auth_type":"api_key","api_key":"`+secret+`","profile":"also-set"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = store.Load()
	if err == nil {
		t.Fatal("expected malformed credential to fail loading")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("load error leaked API key: %v", err)
	}
}

func TestRejectsTraversalAndSymlinkCredentialTargets(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "anthropic"))
	secret := "sk-ant-path-fixture"
	for _, filename := range []string{"../outside.json", "subdir/account.json", `subdir\\account.json`, strings.Repeat("a", maxFilenameBytes+1)} {
		_, err := store.Import(ImportDocument{Filename: filename, Content: []byte(`{"auth_type":"api_key","api_key":"` + secret + `"}`)})
		if err == nil {
			t.Fatalf("expected malicious filename %q to fail", filename)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("filename error leaked API key for %q: %v", filename, err)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(store.Dir()), "outside.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path traversal wrote outside the store: %v", err)
	}

	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	if err := os.MkdirAll(store.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external.json")
	original := []byte("external contents")
	if err := os.WriteFile(external, original, 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(store.Dir(), "linked.json")
	if err := os.Symlink(external, linked); err != nil {
		t.Fatal(err)
	}
	_, err := store.Import(ImportDocument{Filename: "linked.json", Content: []byte(`{"auth_type":"profile","profile":"safe"}`)})
	if err == nil {
		t.Fatal("expected import over symlink target to fail")
	}
	contents, err := os.ReadFile(external)
	if err != nil || string(contents) != string(original) {
		t.Fatalf("external symlink target changed: contents=%q err=%v", contents, err)
	}
}

func TestRejectsSymlinkStoreAndCanonicalizesSymlinkedParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	realStore := filepath.Join(root, "real-store")
	if err := os.Mkdir(realStore, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedStore := filepath.Join(root, "linked-store")
	if err := os.Symlink(realStore, linkedStore); err != nil {
		t.Fatal(err)
	}
	store := NewStore(linkedStore)
	_, err := store.Create(CreateRequest{AuthType: AuthTypeProfile, Profile: "default"})
	if err == nil {
		t.Fatal("expected a symlink store directory to be rejected")
	}

	realParent := filepath.Join(root, "real-parent")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedParent := filepath.Join(root, "linked-parent")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Fatal(err)
	}
	canonical := NewStore(filepath.Join(linkedParent, "anthropic"))
	if strings.Contains(canonical.Dir(), linkedParent) {
		t.Fatalf("store retained a symlinked parent: %q", canonical.Dir())
	}
	if _, err := canonical.Create(CreateRequest{AuthType: AuthTypeProfile, Profile: "canonical"}); err != nil {
		t.Fatal(err)
	}
}

func TestInputLimitsAndAuthenticationShape(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "anthropic"))
	tests := []CreateRequest{
		{AuthType: AuthTypeProfile},
		{AuthType: AuthTypeProfile, Profile: "profile", APIKey: "secret"},
		{AuthType: AuthTypeAPIKey},
		{AuthType: AuthTypeAPIKey, APIKey: "secret", Profile: "profile"},
		{AuthType: "token", APIKey: "secret"},
		{AuthType: AuthTypeProfile, Profile: strings.Repeat("p", maxProfileBytes+1)},
		{AuthType: AuthTypeAPIKey, APIKey: strings.Repeat("k", maxAPIKeyBytes+1)},
		{AuthType: AuthTypeProfile, Profile: "bad\nprofile"},
		{AuthType: AuthTypeAPIKey, APIKey: "bad\nkey"},
		{AuthType: AuthTypeProfile, Profile: "profile", Alias: strings.Repeat("a", maxAliasBytes+1)},
		{AuthType: AuthTypeProfile, Profile: "profile", Priority: maxPriority + 1},
	}
	for index, request := range tests {
		if _, err := store.Create(request); err == nil {
			t.Fatalf("invalid request %d unexpectedly succeeded: %+v", index, request)
		}
	}
	oversized := ImportDocument{Filename: "large.json", Content: []byte(strings.Repeat("x", maxCredentialBytes+1))}
	if _, err := store.Import(oversized); err == nil {
		t.Fatal("oversized import unexpectedly succeeded")
	}
}

func TestLoadRepairsPermissionsAndMigratesMetadata(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "anthropic")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "legacy.json")
	if err := os.WriteFile(path, []byte(`{"auth_type":"profile","profile":"legacy"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	items, err := NewStore(dir).Load()
	if err != nil || len(items) != 1 {
		t.Fatalf("legacy load failed: items=%+v err=%v", items, err)
	}
	credential := items[0].Credential
	if !validCredentialID(credential.ID) || credential.Priority != DefaultPriority || credential.CreatedAt == "" || credential.UpdatedAt == "" {
		t.Fatalf("legacy record was not migrated: %+v", credential)
	}
	if _, err := time.Parse(time.RFC3339Nano, credential.CreatedAt); err != nil {
		t.Fatalf("invalid migrated creation timestamp: %q", credential.CreatedAt)
	}
	if runtime.GOOS != "windows" {
		dirInfo, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		fileInfo, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if dirInfo.Mode().Perm() != 0o700 || fileInfo.Mode().Perm() != 0o600 {
			t.Fatalf("permissions were not repaired: dir=%o file=%o", dirInfo.Mode().Perm(), fileInfo.Mode().Perm())
		}
	}
}

func TestStoreWritesEncryptedCredentialFilesAtRest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials", "anthropic")
	store := NewStore(dir)
	item, err := store.Create(CreateRequest{
		AuthType:     AuthTypeOAuth,
		OAuthAccess:  "oauth-at-secret-value",
		OAuthRefresh: "oauth-rt-secret-value",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, item.Filename))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"oauth-at-secret-value", "oauth-rt-secret-value"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("credential file stored token %q in plaintext: %s", secret, raw)
		}
	}
	if !strings.Contains(string(raw), "autoto_credential_envelope") {
		t.Fatalf("credential file is missing the encrypted envelope marker: %s", raw)
	}
	if _, err := os.Stat(dir + ".key"); err != nil {
		t.Fatalf("credential key file was not created: %v", err)
	}
	items, err := NewStore(dir).Load()
	if err != nil || len(items) != 1 || items[0].Credential.OAuthAccess != "oauth-at-secret-value" || items[0].Credential.OAuthRefresh != "oauth-rt-secret-value" {
		t.Fatalf("encrypted credential did not round-trip: items=%+v err=%v", items, err)
	}
}

func TestStoreMigratesPlaintextCredentialFileToEncrypted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "anthropic")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "legacy.json")
	if err := os.WriteFile(path, []byte(`{"auth_type":"oauth","oauth_access":"legacy-at-secret","oauth_refresh":"legacy-rt-secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := NewStore(dir).Load()
	if err != nil || len(items) != 1 || items[0].Credential.OAuthAccess != "legacy-at-secret" || items[0].Credential.OAuthRefresh != "legacy-rt-secret" {
		t.Fatalf("plaintext credential failed to load: items=%+v err=%v", items, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"legacy-at-secret", "legacy-rt-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("plaintext token %q survived migration: %s", secret, raw)
		}
	}
	if !strings.Contains(string(raw), "autoto_credential_envelope") {
		t.Fatalf("migrated file is not an encrypted envelope: %s", raw)
	}
	again, err := NewStore(dir).Load()
	if err != nil || len(again) != 1 || again[0].Credential.ID != items[0].Credential.ID || again[0].Credential.OAuthAccess != "legacy-at-secret" {
		t.Fatalf("migrated credential did not round-trip: items=%+v err=%v", again, err)
	}
}

func TestStoreMissingKeyFileTreatsEncryptedCredentialsAsAbsent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "anthropic")
	store := NewStore(dir)
	if _, err := store.Create(CreateRequest{AuthType: AuthTypeOAuth, OAuthAccess: "orphan-secret"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir + ".key"); err != nil {
		t.Fatal(err)
	}
	fresh := NewStore(dir)
	items, err := fresh.Load()
	if err != nil || len(items) != 0 {
		t.Fatalf("missing key must yield no credentials without failing: items=%+v err=%v", items, err)
	}
	if fresh.Configured() {
		t.Fatal("store must report not configured when the key file is missing")
	}
	relogin, err := fresh.Create(CreateRequest{AuthType: AuthTypeOAuth, OAuthAccess: "relogin-secret"})
	if err != nil {
		t.Fatal(err)
	}
	items, err = fresh.Load()
	if err != nil || len(items) != 1 || items[0].Credential.ID != relogin.Credential.ID || items[0].Credential.OAuthAccess != "relogin-secret" {
		t.Fatalf("re-login credential unavailable: items=%+v err=%v", items, err)
	}
}
