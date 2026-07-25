package subscriptionauth

import (
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

func oauthRequest(provider, access, subject string, priority int) CreateRequest {
	return CreateRequest{
		Provider:      provider,
		AccessToken:   access,
		RefreshToken:  "refresh-" + access,
		IDToken:       "id-" + access,
		TokenType:     "Bearer",
		ExpiresAt:     time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
		Email:         subject + "@example.test",
		Subject:       subject,
		Scope:         "openid profile email",
		ProjectID:     "project-1",
		DeviceID:      "device-1",
		TokenEndpoint: "https://auth.example.test/oauth/token",
		Priority:      priority,
	}
}

func TestDefaultStoreDir(t *testing.T) {
	got := DefaultStoreDir(" /home/example ", " GROK ")
	if want := filepath.Join("/home/example", "credentials", ProviderGrok); got != want {
		t.Fatalf("DefaultStoreDir() = %q, want %q", got, want)
	}
	if DefaultStoreDir("/home/example", "unknown") != "" || DefaultStoreDir("", ProviderKimi) != "" {
		t.Fatal("invalid home/provider should return an empty directory")
	}
}

func TestStorePermissionsSummaryAndPersistence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials", "grok")
	store := NewStore(dir)
	secret := "access-super-secret"
	request := oauthRequest(ProviderGrok, secret, "subject-1", 20)
	request.Alias = "Primary"
	created, err := store.CreateOAuth(request)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Filename != created.ID+credentialFileSuffix {
		t.Fatalf("unexpected stored credential identity: %+v", created)
	}
	if !store.Configured() {
		t.Fatal("new store should be configured")
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("directory mode = %o, want 700", info.Mode().Perm())
		}
		info, err = os.Stat(filepath.Join(dir, created.Filename))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("file mode = %o, want 600", info.Mode().Perm())
		}
	}

	summaryBytes, err := json.Marshal(Summary(created))
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{request.AccessToken, request.RefreshToken, request.IDToken} {
		if strings.Contains(string(summaryBytes), token) {
			t.Fatalf("summary leaked token %q: %s", token, summaryBytes)
		}
	}
	for _, forbidden := range []string{"access_token", "refresh_token", "id_token"} {
		if strings.Contains(string(summaryBytes), forbidden) {
			t.Fatalf("summary contains private field %q: %s", forbidden, summaryBytes)
		}
	}

	reloaded, err := NewStore(dir).GetByID(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.AccessToken != request.AccessToken || reloaded.RefreshToken != request.RefreshToken || reloaded.DeviceID != request.DeviceID {
		t.Fatalf("credential did not persist: %+v", reloaded.Credential)
	}
}

func TestStoreDeduplicatesProviderIdentityAndSorts(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "subscriptions"))
	first, err := store.CreateOAuth(oauthRequest(ProviderGrok, "access-first", "same-subject", 40))
	if err != nil {
		t.Fatal(err)
	}
	duplicateRequest := oauthRequest(ProviderGrok, "access-rotated", "same-subject", 1)
	duplicate, err := store.CreateOAuth(duplicateRequest)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.ID != first.ID || duplicate.AccessToken != first.AccessToken {
		t.Fatalf("duplicate identity should return original record: first=%+v duplicate=%+v", first, duplicate)
	}
	otherProvider, err := store.CreateOAuth(oauthRequest(ProviderKimi, "access-kimi", "same-subject", 10))
	if err != nil {
		t.Fatal(err)
	}
	if otherProvider.ID == first.ID {
		t.Fatal("identity deduplication crossed provider boundary")
	}
	tieA, err := store.CreateOAuth(oauthRequest(ProviderGemini, "access-a", "subject-a", 5))
	if err != nil {
		t.Fatal(err)
	}
	tieB, err := store.CreateOAuth(oauthRequest(ProviderGemini, "access-b", "subject-b", 5))
	if err != nil {
		t.Fatal(err)
	}

	items, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("Load() returned %d items, want 4", len(items))
	}
	ties := []string{tieA.ID, tieB.ID}
	sort.Strings(ties)
	if items[0].ID != ties[0] || items[1].ID != ties[1] || items[2].ID != otherProvider.ID || items[3].ID != first.ID {
		t.Fatalf("credentials not sorted by priority then ID: %+v", items)
	}
}

func TestStoreUpdatesFiltersAndDeletes(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "subscriptions"))
	created, err := store.CreateOAuth(oauthRequest(ProviderKimi, "old-access", "subject-update", 20))
	if err != nil {
		t.Fatal(err)
	}
	alias, priority, disabled := "Disabled", 3, true
	updated, err := store.UpdateMetadata(created.ID, MetadataUpdate{Alias: &alias, Priority: &priority, Disabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Alias != alias || updated.Priority != priority || !updated.Disabled {
		t.Fatalf("metadata update failed: %+v", updated.Credential)
	}
	if candidates, err := store.List(); err != nil || len(candidates) != 0 {
		t.Fatalf("disabled credential should not be listed: items=%+v err=%v", candidates, err)
	}
	if accounts, err := store.ListAccounts(); err != nil || len(accounts) != 1 || !accounts[0].Disabled {
		t.Fatalf("account summaries should include disabled records: accounts=%+v err=%v", accounts, err)
	}
	if store.Configured() {
		t.Fatal("store with only disabled credentials should not be configured")
	}

	rotated, err := store.UpdateTokens(created.ID, TokenUpdate{
		AccessToken:   "new-access",
		RefreshToken:  "",
		IDToken:       "new-id",
		TokenType:     "Bearer",
		ExpiresAt:     time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339Nano),
		Email:         "updated@example.test",
		Subject:       "subject-update",
		Scope:         "openid",
		DeviceID:      "device-2",
		TokenEndpoint: "https://auth.kimi.com/api/oauth/token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rotated.AccessToken != "new-access" || rotated.RefreshToken != created.RefreshToken || rotated.DeviceID != "device-2" {
		t.Fatalf("token update/refresh preservation failed: %+v", rotated.Credential)
	}
	if err := store.Delete(created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetByID(created.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted credential still exists: %v", err)
	}
	if err := store.Delete(created.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second delete error = %v, want os.ErrNotExist", err)
	}
}

func TestStoreDoesNotLeakTokensInErrorsOrAlias(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "subscriptions"))
	secret := "token-fixture-secret"
	request := oauthRequest(ProviderGrok, secret, "leak-subject", 10)
	request.Alias = "alias-" + secret
	item, err := store.CreateOAuth(request)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(Summary(item))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("summary alias leaked token: %s", encoded)
	}

	tooLong := secret + strings.Repeat("x", maxTokenBytes)
	bad := oauthRequest(ProviderGrok, tooLong, "oversized", 10)
	_, err = store.CreateOAuth(bad)
	if err == nil {
		t.Fatal("expected oversized token to fail")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), tooLong) {
		t.Fatalf("validation error leaked token: %v", err)
	}

	badPath := filepath.Join(store.Dir(), "corrupt.json")
	if err := os.WriteFile(badPath, []byte(`{"id":"bad","provider":"grok","access_token":"`+secret+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = store.Load()
	if err == nil {
		t.Fatal("expected corrupt credential to fail")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("load error leaked token: %v", err)
	}
}

func TestStoreRejectsTraversalAndSymlinkStore(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "subscriptions"))
	if _, err := store.GetByID("../outside"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("traversal ID error = %v, want os.ErrNotExist", err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is permission-dependent on Windows")
	}
	realDir := filepath.Join(t.TempDir(), "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	linkedStore := NewStore(link)
	_, err := linkedStore.CreateOAuth(oauthRequest(ProviderGrok, "secret", "subject", 10))
	if err == nil || !strings.Contains(err.Error(), "不安全") {
		t.Fatalf("symlink store should be rejected, got %v", err)
	}

	parentTarget := filepath.Join(t.TempDir(), "parent-target")
	if err := os.Mkdir(parentTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	parentLink := filepath.Join(t.TempDir(), "parent-link")
	if err := os.Symlink(parentTarget, parentLink); err != nil {
		t.Fatal(err)
	}
	childStore := NewStore(filepath.Join(parentLink, "grok"))
	_, err = childStore.CreateOAuth(oauthRequest(ProviderGrok, "secret-child", "subject-child", 10))
	if err == nil || !strings.Contains(err.Error(), "不安全") {
		t.Fatalf("symlink parent should be rejected, got %v", err)
	}
}
