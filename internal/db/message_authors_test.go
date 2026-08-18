package db

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestListMessageAuthorsUsesHandleAndCurrentProfile(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "authors.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ray, err := store.CreateUser(ctx, "ray", "hash")
	if err != nil {
		t.Fatal(err)
	}
	feng, err := store.CreateCollaboratorUser(ctx, "feng", "hash")
	if err != nil {
		t.Fatal(err)
	}
	profile := json.RawMessage(`{"displayName":"Ray Display","avatarInitials":"rd","avatarDataUrl":"data:image/jpeg;base64,AAAA"}`)
	if _, err := store.PatchAccountPreferences(ctx, AccountPreferenceScopeUser, ray.ID, AccountPreferencesPatch{ExpectedRevision: 0, ProfileJSON: &profile}); err != nil {
		t.Fatal(err)
	}

	authors, err := store.ListMessageAuthors(ctx, []string{ray.ID, feng.ID, ray.ID, "", "missing", "api"})
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 2 {
		t.Fatalf("authors = %#v", authors)
	}
	if authors[ray.ID].Handle != "ray" || authors[ray.ID].DisplayName != "Ray Display" || authors[ray.ID].AvatarInitials != "RD" {
		t.Fatalf("ray author = %+v", authors[ray.ID])
	}
	if strings.Contains(authors[ray.ID].DisplayName, "data:image") || authors[ray.ID].ID != ray.ID {
		t.Fatalf("ray author leaked non-public fields: %+v", authors[ray.ID])
	}
	if authors[feng.ID].Handle != "feng" || authors[feng.ID].DisplayName != "feng" || authors[feng.ID].AvatarInitials != "FE" {
		t.Fatalf("feng author without profile = %+v", authors[feng.ID])
	}
	if _, ok := authors["missing"]; ok {
		t.Fatal("missing users must be omitted")
	}
}

func TestListMessageAuthorsClipsDisplayName(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "authors-clip.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	user, err := store.CreateUser(ctx, "host", "hash")
	if err != nil {
		t.Fatal(err)
	}
	longName := strings.Repeat("名", messageAuthorDisplayNameRunes+4)
	profile, err := json.Marshal(map[string]string{"displayName": longName})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO account_preferences (scope_kind, scope_id, profile_json, preferred_model, model_visibility_json, setup_version, revision, local_storage_import_version, created_at, updated_at) VALUES (?, ?, ?, '', '{}', 0, 1, 0, ?, ?)`, AccountPreferenceScopeUser, user.ID, string(profile), Now(), Now()); err != nil {
		t.Fatal(err)
	}
	authors, err := store.ListMessageAuthors(ctx, []string{user.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := utf8.RuneCountInString(authors[user.ID].DisplayName); got != messageAuthorDisplayNameRunes {
		t.Fatalf("displayName runes = %d, want %d (%q)", got, messageAuthorDisplayNameRunes, authors[user.ID].DisplayName)
	}
}

func TestListMessageAuthorsEmpty(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "authors-empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authors, err := store.ListMessageAuthors(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 0 {
		t.Fatalf("empty lookup = %#v", authors)
	}
}
