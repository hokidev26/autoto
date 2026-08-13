package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var remoteCollaborationTables = []string{
	"remote_pairing_invitations",
	"remote_peer_pairings",
	"remote_peer_grants",
}

var remoteCollaborationIndexes = []string{
	"idx_remote_pairing_invitations_status_expiry",
	"idx_remote_pairing_invitations_requester",
	"idx_remote_peer_pairings_active_fingerprint",
	"idx_remote_peer_pairings_role_status",
	"idx_remote_peer_pairings_expiry",
	"idx_remote_peer_pairings_installation",
	"idx_remote_peer_grants_pairing_project",
	"idx_remote_peer_grants_project_agent",
}

func TestFreshDatabaseIncludesV53RemoteCollaborationSchema(t *testing.T) {
	ctx := context.Background()
	store := openRemoteCollaborationTestStore(t, ctx)
	defer store.Close()
	assertRemoteCollaborationSchema(t, ctx, store.DB())
	if version := readUserVersion(t, ctx, store.DB()); version != CurrentDBVersion {
		t.Fatalf("user_version=%d, want %d", version, CurrentDBVersion)
	}
}

func TestV53RemoteCollaborationMigrationFromV52(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v52.db")
	raw := openRawDB(t, path)
	v52Schema := strings.TrimSuffix(schemaSQL, remoteCollaborationSchemaSQL)
	if v52Schema == schemaSQL {
		t.Fatal("v53 schema suffix was not present")
	}
	if _, err := raw.ExecContext(ctx, v52Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `PRAGMA user_version = 52`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	assertRemoteCollaborationSchema(t, ctx, migrated.DB())
	if version := readUserVersion(t, ctx, migrated.DB()); version != CurrentDBVersion {
		t.Fatalf("user_version=%d, want %d", version, CurrentDBVersion)
	}

	fresh := openRemoteCollaborationTestStore(t, ctx)
	defer fresh.Close()
	if got, want := remoteCollaborationSchemaObjects(t, ctx, migrated.DB()), remoteCollaborationSchemaObjects(t, ctx, fresh.DB()); !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated v53 schema differs from fresh schema\nmigrated: %#v\nfresh: %#v", got, want)
	}
}

func TestRemotePairingClaimApproveAndRevoke(t *testing.T) {
	ctx := context.Background()
	store := openRemoteCollaborationTestStore(t, ctx)
	defer store.Close()
	project, _, agent, err := store.CreateProject(ctx, "Remote", "", t.TempDir(), "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	rawCode := "pairing-code-must-never-be-stored"
	invitation := createRemoteInvitation(t, ctx, store, rawCode, time.Now().UTC().Add(time.Hour))
	requester := remoteTestRequester("a")
	claimed, err := store.ClaimRemotePairingInvitation(ctx, invitation.ID, invitation.CodeHash, invitation.Revision, requester)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Status != RemotePairingInvitationStatusClaimed || claimed.Revision != 2 || claimed.RequesterFingerprint != requester.Fingerprint {
		t.Fatalf("unexpected claimed invitation: %+v", claimed)
	}

	pairing, grants, err := store.ApproveRemotePairingInvitation(ctx, claimed.ID, claimed.Revision, RemotePeerPairing{
		Scopes: []string{RemotePeerScopeExecuteTools, RemotePeerScopeSendTask, RemotePeerScopeObserve, RemotePeerScopeSendTask},
	}, []RemotePeerGrant{{
		ProjectID:         project.ID,
		AgentID:           agent.ID,
		Scopes:            []string{RemotePeerScopeExecuteTools, RemotePeerScopeObserve, RemotePeerScopeExecuteTools},
		PermissionModeCap: RemotePeerPermissionModeReadOnly,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if pairing.LocalRole != RemotePeerLocalRoleHost || pairing.Status != RemotePeerPairingStatusActive || pairing.PeerFingerprint != requester.Fingerprint || pairing.EndpointOrigin != "" {
		t.Fatalf("unexpected approved pairing: %+v", pairing)
	}
	if got, want := pairing.Scopes, []string{RemotePeerScopeObserve, RemotePeerScopeSendTask, RemotePeerScopeExecuteTools}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pairing scopes=%v, want %v", got, want)
	}
	if len(grants) != 1 || !reflect.DeepEqual(grants[0].Scopes, []string{RemotePeerScopeObserve, RemotePeerScopeExecuteTools}) {
		t.Fatalf("unexpected grants: %+v", grants)
	}
	storedGrant, err := store.GetRemotePeerGrant(ctx, pairing.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(storedGrant.Scopes, grants[0].Scopes) || storedGrant.ProjectID != project.ID {
		t.Fatalf("unexpected stored grant: %+v", storedGrant)
	}
	approved, err := store.GetRemotePairingInvitation(ctx, invitation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != RemotePairingInvitationStatusApproved || approved.Revision != 3 || approved.CompletedAt == "" {
		t.Fatalf("unexpected approved invitation: %+v", approved)
	}

	pairing, grants, err = store.ReplaceRemotePeerAuthorization(ctx, pairing.ID, pairing.GrantRevision, []string{RemotePeerScopeObserve, RemotePeerScopeSendTask}, time.Now().UTC().Add(2*time.Hour).Format(time.RFC3339Nano), []RemotePeerGrant{{
		ProjectID:         project.ID,
		AgentID:           agent.ID,
		Scopes:            []string{RemotePeerScopeObserve},
		PermissionModeCap: RemotePeerPermissionModeReadOnly,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if pairing.GrantRevision != 2 || len(grants) != 1 || !reflect.DeepEqual(pairing.Scopes, []string{RemotePeerScopeObserve, RemotePeerScopeSendTask}) || pairing.ExpiresAt == "" {
		t.Fatalf("unexpected replaced authorization: pairing=%+v grants=%+v", pairing, grants)
	}
	if _, _, err := store.ReplaceRemotePeerAuthorization(ctx, pairing.ID, pairing.GrantRevision, []string{RemotePeerScopeObserve}, pairing.ExpiresAt, []RemotePeerGrant{{
		ProjectID: project.ID, AgentID: agent.ID, Scopes: []string{RemotePeerScopeSendTask}, PermissionModeCap: RemotePeerPermissionModeReadOnly,
	}}); err == nil || !strings.Contains(err.Error(), "exceeds pairing scopes") {
		t.Fatalf("grant scope wider than pairing was accepted: %v", err)
	}

	revoked, err := store.RevokeRemotePeerPairing(ctx, pairing.ID, pairing.Status, pairing.CredentialRevision)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.Status != RemotePeerPairingStatusRevoked || revoked.CredentialRevision != pairing.CredentialRevision+1 || revoked.RevokedAt == "" {
		t.Fatalf("unexpected revoked pairing: %+v", revoked)
	}
}

func TestRemotePairingRevisionConflicts(t *testing.T) {
	ctx := context.Background()
	store := openRemoteCollaborationTestStore(t, ctx)
	defer store.Close()
	invitation := createRemoteInvitation(t, ctx, store, "revision-code", time.Now().UTC().Add(time.Hour))
	if _, err := store.ClaimRemotePairingInvitation(ctx, invitation.ID, invitation.CodeHash, invitation.Revision+1, remoteTestRequester("b")); !IsConflict(err) {
		t.Fatalf("expected claim revision conflict, got %v", err)
	}
	if _, err := store.ClaimRemotePairingInvitation(ctx, invitation.ID, strings.Repeat("0", 64), invitation.Revision, remoteTestRequester("b")); !IsConflict(err) {
		t.Fatalf("expected claim code hash conflict, got %v", err)
	}

	pairing, err := store.CreateRemotePeerPairing(ctx, RemotePeerPairing{
		LocalRole:          RemotePeerLocalRoleController,
		DisplayName:        "Host",
		PeerInstallationID: "host-installation",
		PeerPublicKey:      remoteTestPublicKey(),
		PeerFingerprint:    remoteTestFingerprint(),
		EndpointOrigin:     "HTTPS://Example.COM:8443/",
		Scopes:             []string{RemotePeerScopeObserve},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pairing.EndpointOrigin != "https://example.com:8443" {
		t.Fatalf("endpoint origin=%q", pairing.EndpointOrigin)
	}
	if _, _, err := store.ReplaceRemotePeerGrants(ctx, pairing.ID, pairing.GrantRevision+1, nil); !IsConflict(err) {
		t.Fatalf("expected grant revision conflict, got %v", err)
	}
	if _, err := store.RevokeRemotePeerPairing(ctx, pairing.ID, pairing.Status, pairing.CredentialRevision+1); !IsConflict(err) {
		t.Fatalf("expected pairing revision conflict, got %v", err)
	}
}

func TestRemotePairingInvitationExpirationAndLocking(t *testing.T) {
	ctx := context.Background()
	store := openRemoteCollaborationTestStore(t, ctx)
	defer store.Close()

	expired := createRemoteInvitation(t, ctx, store, "expired-code", time.Now().UTC().Add(time.Hour))
	createdAt := time.Now().UTC().Add(-2 * time.Hour).Format(timestampLayout)
	expiresAt := time.Now().UTC().Add(-time.Hour).Format(timestampLayout)
	updatedAt := Now()
	if _, err := store.DB().ExecContext(ctx, `UPDATE remote_pairing_invitations SET created_at = ?, expires_at = ?, updated_at = ? WHERE id = ?`, createdAt, expiresAt, updatedAt, expired.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRemotePairingInvitation(ctx, expired.ID, expired.CodeHash, expired.Revision, remoteTestRequester("d")); !IsConflict(err) {
		t.Fatalf("expected expired invitation conflict, got %v", err)
	}
	expiredState, err := store.ExpireRemotePairingInvitation(ctx, expired.ID, RemotePairingInvitationStatusOpen, expired.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if expiredState.Status != RemotePairingInvitationStatusExpired || expiredState.CompletedAt == "" {
		t.Fatalf("unexpected expired invitation: %+v", expiredState)
	}

	locked := createRemoteInvitation(t, ctx, store, "locked-code", time.Now().UTC().Add(2*time.Hour))
	lockUntil := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	locked, err = store.RecordRemotePairingInvitationFailure(ctx, locked.ID, locked.Revision, 1, lockUntil)
	if err != nil {
		t.Fatal(err)
	}
	if locked.FailedAttempts != 1 || locked.LockedUntil == "" || locked.Revision != 2 {
		t.Fatalf("unexpected locked invitation: %+v", locked)
	}
	if _, err := store.ClaimRemotePairingInvitation(ctx, locked.ID, locked.CodeHash, locked.Revision, remoteTestRequester("e")); !IsConflict(err) {
		t.Fatalf("expected locked invitation conflict, got %v", err)
	}
}

func TestRemotePeerGrantRejectsProjectAgentMismatch(t *testing.T) {
	ctx := context.Background()
	store := openRemoteCollaborationTestStore(t, ctx)
	defer store.Close()
	firstProject, _, _, err := store.CreateProject(ctx, "First", "", t.TempDir(), "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	_, _, secondAgent, err := store.CreateProject(ctx, "Second", "", t.TempDir(), "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	invitation := createRemoteInvitation(t, ctx, store, "mismatch-code", time.Now().UTC().Add(time.Hour))
	claimed, err := store.ClaimRemotePairingInvitation(ctx, invitation.ID, invitation.CodeHash, invitation.Revision, remoteTestRequester("f"))
	if err != nil {
		t.Fatal(err)
	}
	pairingID := NewID()
	if _, _, err := store.ApproveRemotePairingInvitation(ctx, claimed.ID, claimed.Revision, RemotePeerPairing{ID: pairingID, Scopes: []string{RemotePeerScopeObserve}}, []RemotePeerGrant{{
		ProjectID:         firstProject.ID,
		AgentID:           secondAgent.ID,
		Scopes:            []string{RemotePeerScopeObserve},
		PermissionModeCap: RemotePeerPermissionModeReadOnly,
	}}); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("expected project-agent mismatch, got %v", err)
	}
	stillClaimed, err := store.GetRemotePairingInvitation(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillClaimed.Status != RemotePairingInvitationStatusClaimed || stillClaimed.Revision != claimed.Revision {
		t.Fatalf("failed approval changed invitation: %+v", stillClaimed)
	}
	if _, err := store.GetRemotePeerPairing(ctx, pairingID); !IsNotFound(err) {
		t.Fatalf("failed approval persisted pairing: %v", err)
	}
}

func TestRemoteScopesNormalizeAndRawPairingCodeIsNotStored(t *testing.T) {
	ctx := context.Background()
	store := openRemoteCollaborationTestStore(t, ctx)
	defer store.Close()
	rawCode := "raw-secret-pairing-code"
	invitation := createRemoteInvitation(t, ctx, store, rawCode, time.Now().UTC().Add(time.Hour))
	if invitation.CodeHash == rawCode {
		t.Fatal("returned invitation exposed raw pairing code")
	}
	var rawMatches int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM remote_pairing_invitations WHERE code_hash = ? OR instr(code_hash, ?) > 0`, rawCode, rawCode).Scan(&rawMatches); err != nil {
		t.Fatal(err)
	}
	if rawMatches != 0 {
		t.Fatal("raw pairing code was stored")
	}

	pairing, err := store.CreateRemotePeerPairing(ctx, RemotePeerPairing{
		DisplayName:        "Remote host",
		PeerInstallationID: "remote-host",
		PeerPublicKey:      remoteTestPublicKey(),
		PeerFingerprint:    remoteTestFingerprint(),
		EndpointOrigin:     "http://127.0.0.1:8080",
		Scopes: []string{
			RemotePeerScopeSendTask,
			RemotePeerScopeApproveSession,
			RemotePeerScopeApproveOnce,
			RemotePeerScopeObserve,
			RemotePeerScopeSendTask,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantScopes := []string{RemotePeerScopeObserve, RemotePeerScopeSendTask, RemotePeerScopeApproveOnce, RemotePeerScopeApproveSession}
	if !reflect.DeepEqual(pairing.Scopes, wantScopes) {
		t.Fatalf("normalized scopes=%v, want %v", pairing.Scopes, wantScopes)
	}
	var storedScopes string
	if err := store.DB().QueryRowContext(ctx, `SELECT scopes_json FROM remote_peer_pairings WHERE id = ?`, pairing.ID).Scan(&storedScopes); err != nil {
		t.Fatal(err)
	}
	if storedScopes != `["observe","send_task","approve_once","approve_session"]` {
		t.Fatalf("stored scopes=%s", storedScopes)
	}
	if _, err := store.CreateRemotePeerPairing(ctx, RemotePeerPairing{
		DisplayName:        "Bad scopes",
		PeerInstallationID: "bad-scopes",
		PeerPublicKey:      remoteTestPublicKey(),
		PeerFingerprint:    remoteTestFingerprint(),
		EndpointOrigin:     "https://example.test",
		Scopes:             []string{"admin"},
	}); err == nil {
		t.Fatal("invalid scope was accepted")
	}
	if _, err := store.CreateRemotePeerPairing(ctx, RemotePeerPairing{
		DisplayName:        "Mismatched identity",
		PeerInstallationID: "mismatched-identity",
		PeerPublicKey:      remoteTestPublicKey(),
		PeerFingerprint:    strings.Repeat("f", 64),
		EndpointOrigin:     "https://example.test",
		Scopes:             []string{RemotePeerScopeObserve},
	}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched public key fingerprint was accepted: %v", err)
	}
}

func openRemoteCollaborationTestStore(t *testing.T, ctx context.Context) *Store {
	t.Helper()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "remote-collaboration.db"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func createRemoteInvitation(t *testing.T, ctx context.Context, store *Store, rawCode string, expiresAt time.Time) RemotePairingInvitation {
	t.Helper()
	hash := sha256.Sum256([]byte(rawCode))
	invitation, err := store.CreateRemotePairingInvitation(ctx, RemotePairingInvitation{
		CodeHash:        fmt.Sprintf("%x", hash[:]),
		ProtocolVersion: 1,
		ExpiresAt:       expiresAt.Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	return invitation
}

func remoteTestRequester(hashChar string) RemotePairingRequester {
	return RemotePairingRequester{
		DisplayName:    "Remote controller",
		InstallationID: "controller-" + hashChar,
		PublicKey:      remoteTestPublicKey(),
		Fingerprint:    remoteTestFingerprint(),
	}
}

func remoteTestPublicKey() string {
	return base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
}

func remoteTestFingerprint() string {
	publicKey, err := base64.StdEncoding.DecodeString(remoteTestPublicKey())
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(publicKey)
	return fmt.Sprintf("%x", digest[:])
}

func assertRemoteCollaborationSchema(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	for _, table := range remoteCollaborationTables {
		if !testTableExists(t, ctx, database, table) {
			t.Errorf("missing table %s", table)
		}
	}
	for _, index := range remoteCollaborationIndexes {
		var count int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("missing index %s", index)
		}
	}
	for _, forbidden := range []string{"remote_tool_executions", "remote_tool_execution_requests", "remote_tool_execution_results"} {
		if testTableExists(t, ctx, database, forbidden) {
			t.Errorf("phase one unexpectedly created %s", forbidden)
		}
	}
}

func remoteCollaborationSchemaObjects(t *testing.T, ctx context.Context, database *sql.DB) map[string]string {
	t.Helper()
	names := append(append([]string{}, remoteCollaborationTables...), remoteCollaborationIndexes...)
	objects := make(map[string]string, len(names))
	for _, name := range names {
		var objectType, sqlText string
		if err := database.QueryRowContext(ctx, `SELECT type, COALESCE(sql, '') FROM sqlite_master WHERE name = ?`, name).Scan(&objectType, &sqlText); err != nil {
			t.Fatalf("read schema object %s: %v", name, err)
		}
		objects[objectType+":"+name] = strings.Join(strings.Fields(sqlText), " ")
	}
	return objects
}
