package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestUserHandleUnicodeCaseConflictAndValidation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	user, err := store.CreateUser(ctx, "Ａlice", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if user.Handle != "Alice" {
		t.Fatalf("expected NFKC handle, got %q", user.Handle)
	}
	if user.Role != "admin" {
		t.Fatalf("expected first user to be admin, got %q", user.Role)
	}
	if _, err := store.CreateUser(ctx, "alice", "hash"); !IsConflict(err) {
		t.Fatalf("expected Unicode/case handle conflict, got %v", err)
	}
	for _, handle := range []string{"a b", "a@b", "a/b", "a\\b", "a\u200db", "a\nb"} {
		if _, err := store.CreateUser(ctx, handle, "hash"); err == nil {
			t.Fatalf("expected invalid handle %q to be rejected", handle)
		}
	}
}

func TestMigrationV18BackfillsHandlesFromV17Users(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	raw := openRawDB(t, path)
	if _, err := raw.ExecContext(ctx, schemaSQL); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `
PRAGMA foreign_keys = OFF;
DROP TABLE auth_sessions;
DROP TABLE message_drafts;
CREATE TABLE users_v17 (
  id TEXT PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT,
  role TEXT NOT NULL DEFAULT 'user',
  avatar_color TEXT,
  avatar_image_id TEXT,
  git_username TEXT,
  git_email TEXT,
  created_at TEXT NOT NULL
);
DROP TABLE users;
ALTER TABLE users_v17 RENAME TO users;
PRAGMA foreign_keys = ON;
`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	now := Now()
	if _, err := raw.ExecContext(ctx, `INSERT INTO users (id, username, password_hash, role, created_at) VALUES ('legacy-user', 'Ａlice', 'hash', 'user', ?)`, now); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `PRAGMA user_version = 17`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var handle, handleKey string
	if err := store.DB().QueryRowContext(ctx, `SELECT handle, handle_key FROM users WHERE id = 'legacy-user'`).Scan(&handle, &handleKey); err != nil {
		t.Fatal(err)
	}
	if handle != "Alice" || handleKey != "alice" {
		t.Fatalf("unexpected v18 handle backfill: handle=%q key=%q", handle, handleKey)
	}
	if !testColumnExists(t, ctx, store.DB(), "agent_messages", "correction_of_message_id") {
		t.Fatal("expected v20 correction column")
	}
}

func TestCreateUserRolesAndGuestAccessKeys(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "guest-roles.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := store.CreateCollaboratorUser(ctx, "too-soon", "hash"); err == nil {
		t.Fatal("the first account must not be a collaborator")
	}
	admin, err := store.CreateUser(ctx, "host", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if admin.Role != "admin" {
		t.Fatalf("first user role = %q, want admin", admin.Role)
	}
	collaborator, err := store.CreateUser(ctx, "teammate", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if collaborator.Role != RoleOperator {
		t.Fatalf("second public user role = %q, want operator", collaborator.Role)
	}
	operator, err := store.CreateOperatorUser(ctx, "named-operator", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if operator.Role != RoleOperator {
		t.Fatalf("admin-created operator role = %q, want user", operator.Role)
	}
	named, err := store.CreateCollaboratorUser(ctx, "named-teammate", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if named.Role != RoleCollaborator {
		t.Fatalf("admin-created collaborator role = %q, want collaborator", named.Role)
	}
	if _, err := store.CreateGuestUser(ctx, "viewer", ""); err != nil {
		t.Fatal(err)
	}
	guest, err := store.CreateGuestUser(ctx, "key-only", "")
	if err != nil {
		t.Fatal(err)
	}
	if guest.Role != "guest" {
		t.Fatalf("guest role = %q", guest.Role)
	}
	set, err := store.UserPasswordSet(ctx, guest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if set {
		t.Fatal("key-only guest must store an empty password hash")
	}

	project, _, _, err := store.CreateProject(ctx, "Shared", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceUserProjectMemberships(ctx, guest.ID, []string{project.ID}); err != nil {
		t.Fatal(err)
	}
	ids, err := store.ListProjectIDsForUser(ctx, guest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != project.ID {
		t.Fatalf("guest memberships = %v, want [%s]", ids, project.ID)
	}
	if err := store.ReplaceUserProjectMemberships(ctx, guest.ID, []string{"missing-project"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected missing project to be not found, got %v", err)
	}

	key, err := store.CreateUserAccessKey(ctx, UserAccessKey{UserID: guest.ID, TokenHash: HashSessionToken("atk_testtoken"), Label: "phone"})
	if err != nil {
		t.Fatal(err)
	}
	found, stored, err := store.GetUserByAccessKeyToken(ctx, "atk_testtoken")
	if err != nil {
		t.Fatal(err)
	}
	if found.ID != guest.ID || stored.ID != key.ID {
		t.Fatalf("access key lookup mismatch: user=%s key=%s", found.ID, stored.ID)
	}
	if err := store.TouchUserAccessKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeUserAccessKey(ctx, guest.ID, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetUserByAccessKeyToken(ctx, "atk_testtoken"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoked key must not authenticate, got %v", err)
	}

	count, err := store.CountUsersByRole(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("admin count = %d, want 1", count)
	}
	if err := store.DeleteUser(ctx, guest.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetUser(ctx, guest.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted guest still present: %v", err)
	}
}

func TestAdminCanAccessProjectsWithoutMembership(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "admin-access.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	admin, err := store.CreateUser(ctx, "host", "hash")
	if err != nil {
		t.Fatal(err)
	}
	operator, err := store.CreateOperatorUser(ctx, "operator", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, workline, agent, err := store.CreateProjectForUser(ctx, operator.ID, "Owned", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	member, err := store.IsProjectMember(ctx, admin.ID, project.ID)
	if err != nil || member {
		t.Fatalf("admin should not be stored as a member, member=%v err=%v", member, err)
	}
	allowed, err := store.CanAccessProject(ctx, admin.ID, project.ID)
	if err != nil || !allowed {
		t.Fatalf("admin CanAccessProject = %v, %v", allowed, err)
	}
	allowed, err = store.CanAccessWorkline(ctx, admin.ID, workline.ID)
	if err != nil || !allowed {
		t.Fatalf("admin CanAccessWorkline = %v, %v", allowed, err)
	}
	allowed, err = store.CanAccessAgent(ctx, admin.ID, agent.ID)
	if err != nil || !allowed {
		t.Fatalf("admin CanAccessAgent = %v, %v", allowed, err)
	}
	outsider, err := store.CreateCollaboratorUser(ctx, "outsider", "hash")
	if err != nil {
		t.Fatal(err)
	}
	allowed, err = store.CanAccessProject(ctx, outsider.ID, project.ID)
	if err != nil || allowed {
		t.Fatalf("collaborator without membership CanAccessProject = %v, %v", allowed, err)
	}
}

func TestUpdateUserRoleRejectsGuestsAndInvalidRoles(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "role-update.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateUser(ctx, "host", "hash"); err != nil {
		t.Fatal(err)
	}
	operator, err := store.CreateOperatorUser(ctx, "operator", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateUserRole(ctx, operator.ID, RoleCollaborator); err != nil {
		t.Fatal(err)
	}
	updated, err := store.GetUser(ctx, operator.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Role != RoleCollaborator {
		t.Fatalf("role = %q, want collaborator", updated.Role)
	}
	guest, err := store.CreateGuestUser(ctx, "viewer", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateUserRole(ctx, guest.ID, RoleOperator); err == nil {
		t.Fatal("expected guest role change to fail")
	}
	if err := store.UpdateUserRole(ctx, operator.ID, RoleGuest); err == nil {
		t.Fatal("expected conversion to guest to fail")
	}
	if err := store.UpdateUserRole(ctx, operator.ID, "root"); err == nil {
		t.Fatal("expected invalid role to fail")
	}
}

func TestRevokeAuthSessionsExceptToken(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "revoke-except.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	admin, err := store.CreateUser(ctx, "host", "hash")
	if err != nil {
		t.Fatal(err)
	}
	collaborator, err := store.CreateCollaboratorUser(ctx, "teammate", "hash")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	expires := now.Add(time.Hour).Format(time.RFC3339Nano)
	created := Now()
	if _, err := store.CreateAuthSession(ctx, AuthSession{UserID: admin.ID, TokenHash: HashSessionToken("keep-token"), CreatedAt: created, ExpiresAt: expires}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAuthSession(ctx, AuthSession{UserID: collaborator.ID, TokenHash: HashSessionToken("drop-token"), CreatedAt: created, ExpiresAt: expires}); err != nil {
		t.Fatal(err)
	}

	n, err := store.RevokeAuthSessionsExceptToken(ctx, "keep-token")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("revoked %d sessions, want 1", n)
	}
	if _, _, err := store.GetUserBySessionToken(ctx, "keep-token", now); err != nil {
		t.Fatalf("kept session should stay valid: %v", err)
	}
	if _, _, err := store.GetUserBySessionToken(ctx, "drop-token", now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("other session should be revoked, got %v", err)
	}

	n, err = store.RevokeAuthSessionsExceptToken(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("empty keep token revoked %d sessions, want 1", n)
	}
	if _, _, err := store.GetUserBySessionToken(ctx, "keep-token", now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("empty keep token should revoke remaining sessions, got %v", err)
	}
}
