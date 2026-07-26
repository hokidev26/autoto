package db

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	RemotePairingInvitationStatusOpen     = "open"
	RemotePairingInvitationStatusClaimed  = "claimed"
	RemotePairingInvitationStatusApproved = "approved"
	RemotePairingInvitationStatusRejected = "rejected"
	RemotePairingInvitationStatusRevoked  = "revoked"
	RemotePairingInvitationStatusExpired  = "expired"

	RemotePeerLocalRoleHost       = "host"
	RemotePeerLocalRoleController = "controller"

	RemotePeerPairingStatusActive  = "active"
	RemotePeerPairingStatusRevoked = "revoked"
	RemotePeerPairingStatusExpired = "expired"

	RemotePeerScopeObserve      = "observe"
	RemotePeerScopeSendTask     = "send_task"
	RemotePeerScopeApproveOnce  = "approve_once"
	RemotePeerScopeExecuteTools = "execute_tools"

	RemotePeerPermissionModeReadOnly    = "readOnly"
	RemotePeerPermissionModeAcceptEdits = "acceptEdits"

	remoteCollaborationMaxListLimit = 200
	remoteScopesJSONMaxBytes        = 1024
)

const remoteCollaborationSchemaSQL = `

CREATE TABLE IF NOT EXISTS remote_pairing_invitations (
  id TEXT PRIMARY KEY,
  code_hash TEXT NOT NULL UNIQUE,
  protocol_version INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'open',
  requester_display_name TEXT,
  requester_installation_id TEXT,
  requester_public_key TEXT,
  requester_fingerprint TEXT,
  failed_attempts INTEGER NOT NULL DEFAULT 0,
  locked_until TEXT,
  expires_at TEXT NOT NULL,
  revision INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  completed_at TEXT,
  CHECK (length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
  CHECK (length(code_hash) = 64 AND code_hash NOT GLOB '*[^0-9a-f]*'),
  CHECK (protocol_version BETWEEN 1 AND 1000),
  CHECK (status IN ('open', 'claimed', 'approved', 'rejected', 'revoked', 'expired')),
  CHECK (requester_display_name IS NULL OR length(CAST(requester_display_name AS BLOB)) BETWEEN 1 AND 256),
  CHECK (requester_installation_id IS NULL OR length(CAST(requester_installation_id AS BLOB)) BETWEEN 1 AND 128),
  CHECK (requester_public_key IS NULL OR length(CAST(requester_public_key AS BLOB)) = 44),
  CHECK (requester_fingerprint IS NULL OR (length(requester_fingerprint) = 64 AND requester_fingerprint NOT GLOB '*[^0-9a-f]*')),
  CHECK (failed_attempts BETWEEN 0 AND 100000),
  CHECK (revision >= 1),
  CHECK (created_at <= updated_at),
  CHECK (created_at < expires_at),
  CHECK (locked_until IS NULL OR (created_at <= locked_until AND locked_until <= expires_at)),
  CHECK (completed_at IS NULL OR (created_at <= completed_at AND completed_at <= updated_at)),
  CHECK (
    (requester_display_name IS NULL AND requester_installation_id IS NULL AND requester_public_key IS NULL AND requester_fingerprint IS NULL)
    OR
    (requester_display_name IS NOT NULL AND requester_installation_id IS NOT NULL AND requester_public_key IS NOT NULL AND requester_fingerprint IS NOT NULL)
  ),
  CHECK (
    (status = 'open' AND requester_display_name IS NULL AND completed_at IS NULL)
    OR (status = 'claimed' AND requester_display_name IS NOT NULL AND completed_at IS NULL)
    OR (status = 'approved' AND requester_display_name IS NOT NULL AND completed_at IS NOT NULL)
    OR (status IN ('rejected', 'revoked', 'expired') AND completed_at IS NOT NULL)
  )
);
CREATE INDEX IF NOT EXISTS idx_remote_pairing_invitations_status_expiry ON remote_pairing_invitations(status, expires_at, updated_at, id);
CREATE INDEX IF NOT EXISTS idx_remote_pairing_invitations_requester ON remote_pairing_invitations(requester_fingerprint, status, updated_at, id);

CREATE TABLE IF NOT EXISTS remote_peer_pairings (
  id TEXT PRIMARY KEY,
  local_role TEXT NOT NULL,
  display_name TEXT NOT NULL,
  peer_installation_id TEXT NOT NULL,
  peer_public_key TEXT NOT NULL,
  peer_fingerprint TEXT NOT NULL,
  endpoint_origin TEXT,
  status TEXT NOT NULL DEFAULT 'active',
  scopes_json TEXT NOT NULL DEFAULT '[]',
  credential_revision INTEGER NOT NULL DEFAULT 1,
  grant_revision INTEGER NOT NULL DEFAULT 1,
  expires_at TEXT,
  last_seen_at TEXT,
  paired_at TEXT NOT NULL,
  revoked_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
  CHECK (local_role IN ('host', 'controller')),
  CHECK (length(CAST(display_name AS BLOB)) BETWEEN 1 AND 256),
  CHECK (length(CAST(peer_installation_id AS BLOB)) BETWEEN 1 AND 128),
  CHECK (length(CAST(peer_public_key AS BLOB)) = 44),
  CHECK (length(peer_fingerprint) = 64 AND peer_fingerprint NOT GLOB '*[^0-9a-f]*'),
  CHECK ((local_role = 'host' AND endpoint_origin IS NULL) OR (local_role = 'controller' AND length(CAST(endpoint_origin AS BLOB)) BETWEEN 8 AND 2048)),
  CHECK (status IN ('active', 'revoked', 'expired')),
  CHECK (length(CAST(scopes_json AS BLOB)) <= 1024 AND json_valid(scopes_json) AND json_type(scopes_json) = 'array'),
  CHECK (credential_revision >= 1),
  CHECK (grant_revision >= 1),
  CHECK (created_at <= paired_at AND paired_at <= updated_at),
  CHECK (expires_at IS NULL OR paired_at < expires_at),
  CHECK (last_seen_at IS NULL OR (paired_at <= last_seen_at AND last_seen_at <= updated_at)),
  CHECK (revoked_at IS NULL OR (paired_at <= revoked_at AND revoked_at <= updated_at)),
  CHECK (
    (status = 'active' AND revoked_at IS NULL)
    OR (status = 'revoked' AND revoked_at IS NOT NULL)
    OR (status = 'expired' AND revoked_at IS NULL AND expires_at IS NOT NULL AND expires_at <= updated_at)
  )
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_remote_peer_pairings_active_fingerprint ON remote_peer_pairings(local_role, peer_fingerprint) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_remote_peer_pairings_role_status ON remote_peer_pairings(local_role, status, updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_remote_peer_pairings_expiry ON remote_peer_pairings(status, expires_at, id);
CREATE INDEX IF NOT EXISTS idx_remote_peer_pairings_installation ON remote_peer_pairings(peer_installation_id, local_role, status, id);

CREATE TABLE IF NOT EXISTS remote_peer_grants (
  id TEXT PRIMARY KEY,
  pairing_id TEXT NOT NULL REFERENCES remote_peer_pairings(id) ON DELETE CASCADE,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  scopes_json TEXT NOT NULL DEFAULT '[]',
  permission_mode_cap TEXT NOT NULL,
  revision INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(pairing_id, agent_id),
  CHECK (length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
  CHECK (length(CAST(pairing_id AS BLOB)) BETWEEN 1 AND 128),
  CHECK (length(CAST(project_id AS BLOB)) BETWEEN 1 AND 128),
  CHECK (length(CAST(agent_id AS BLOB)) BETWEEN 1 AND 128),
  CHECK (length(CAST(scopes_json AS BLOB)) <= 1024 AND json_valid(scopes_json) AND json_type(scopes_json) = 'array'),
  CHECK (permission_mode_cap IN ('readOnly', 'acceptEdits')),
  CHECK (revision >= 1),
  CHECK (created_at <= updated_at)
);
CREATE INDEX IF NOT EXISTS idx_remote_peer_grants_pairing_project ON remote_peer_grants(pairing_id, project_id, agent_id);
CREATE INDEX IF NOT EXISTS idx_remote_peer_grants_project_agent ON remote_peer_grants(project_id, agent_id, pairing_id);
`

type RemotePairingInvitation struct {
	ID                      string `json:"id"`
	CodeHash                string `json:"-"`
	ProtocolVersion         int    `json:"protocolVersion"`
	Status                  string `json:"status"`
	RequesterDisplayName    string `json:"requesterDisplayName,omitempty"`
	RequesterInstallationID string `json:"requesterInstallationId,omitempty"`
	RequesterPublicKey      string `json:"requesterPublicKey,omitempty"`
	RequesterFingerprint    string `json:"requesterFingerprint,omitempty"`
	FailedAttempts          int    `json:"failedAttempts"`
	LockedUntil             string `json:"lockedUntil,omitempty"`
	ExpiresAt               string `json:"expiresAt"`
	Revision                int64  `json:"revision"`
	CreatedAt               string `json:"createdAt"`
	UpdatedAt               string `json:"updatedAt"`
	CompletedAt             string `json:"completedAt,omitempty"`
}

type RemotePairingRequester struct {
	DisplayName    string `json:"displayName"`
	InstallationID string `json:"installationId"`
	PublicKey      string `json:"publicKey"`
	Fingerprint    string `json:"fingerprint"`
}

type RemotePairingInvitationListOptions struct {
	Status               string `json:"status,omitempty"`
	RequesterFingerprint string `json:"requesterFingerprint,omitempty"`
	Limit                int    `json:"limit,omitempty"`
}

type RemotePeerPairing struct {
	ID                 string   `json:"id"`
	LocalRole          string   `json:"localRole"`
	DisplayName        string   `json:"displayName"`
	PeerInstallationID string   `json:"peerInstallationId"`
	PeerPublicKey      string   `json:"peerPublicKey"`
	PeerFingerprint    string   `json:"peerFingerprint"`
	EndpointOrigin     string   `json:"endpointOrigin,omitempty"`
	Status             string   `json:"status"`
	Scopes             []string `json:"scopes"`
	CredentialRevision int64    `json:"credentialRevision"`
	GrantRevision      int64    `json:"grantRevision"`
	ExpiresAt          string   `json:"expiresAt,omitempty"`
	LastSeenAt         string   `json:"lastSeenAt,omitempty"`
	PairedAt           string   `json:"pairedAt"`
	RevokedAt          string   `json:"revokedAt,omitempty"`
	CreatedAt          string   `json:"createdAt"`
	UpdatedAt          string   `json:"updatedAt"`
}

type RemotePeerPairingListOptions struct {
	LocalRole string `json:"localRole,omitempty"`
	Status    string `json:"status,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type RemotePeerGrant struct {
	ID                string   `json:"id"`
	PairingID         string   `json:"pairingId"`
	ProjectID         string   `json:"projectId"`
	AgentID           string   `json:"agentId"`
	Scopes            []string `json:"scopes"`
	PermissionModeCap string   `json:"permissionModeCap"`
	Revision          int64    `json:"revision"`
	CreatedAt         string   `json:"createdAt"`
	UpdatedAt         string   `json:"updatedAt"`
}

const remotePairingInvitationColumns = `id, code_hash, protocol_version, status, COALESCE(requester_display_name,''), COALESCE(requester_installation_id,''), COALESCE(requester_public_key,''), COALESCE(requester_fingerprint,''), failed_attempts, COALESCE(locked_until,''), expires_at, revision, created_at, updated_at, COALESCE(completed_at,'')`
const remotePeerPairingColumns = `id, local_role, display_name, peer_installation_id, peer_public_key, peer_fingerprint, COALESCE(endpoint_origin,''), status, scopes_json, credential_revision, grant_revision, COALESCE(expires_at,''), COALESCE(last_seen_at,''), paired_at, COALESCE(revoked_at,''), created_at, updated_at`
const remotePeerGrantColumns = `id, pairing_id, project_id, agent_id, scopes_json, permission_mode_cap, revision, created_at, updated_at`

type remoteCollaborationQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) CreateRemotePairingInvitation(ctx context.Context, invitation RemotePairingInvitation) (RemotePairingInvitation, error) {
	canonical, err := canonicalRemotePairingInvitationForCreate(invitation)
	if err != nil {
		return RemotePairingInvitation{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO remote_pairing_invitations (id, code_hash, protocol_version, status, requester_display_name, requester_installation_id, requester_public_key, requester_fingerprint, failed_attempts, locked_until, expires_at, revision, created_at, updated_at, completed_at) VALUES (?, ?, ?, 'open', NULL, NULL, NULL, NULL, 0, NULL, ?, 1, ?, ?, NULL)`, canonical.ID, canonical.CodeHash, canonical.ProtocolVersion, canonical.ExpiresAt, canonical.CreatedAt, canonical.UpdatedAt)
	if err != nil {
		if isUniqueConstraint(err) {
			return RemotePairingInvitation{}, fmt.Errorf("%w: remote pairing invitation already exists", ErrConflict)
		}
		return RemotePairingInvitation{}, fmt.Errorf("create remote pairing invitation: %w", err)
	}
	return canonical, nil
}

func (s *Store) GetRemotePairingInvitation(ctx context.Context, id string) (RemotePairingInvitation, error) {
	id, err := normalizeRemoteID("invitation id", id)
	if err != nil {
		return RemotePairingInvitation{}, err
	}
	return getRemotePairingInvitation(ctx, s.db, id)
}

func (s *Store) ListRemotePairingInvitations(ctx context.Context, optionArgs ...RemotePairingInvitationListOptions) ([]RemotePairingInvitation, error) {
	options, err := normalizeRemoteInvitationListOptions(optionArgs)
	if err != nil {
		return nil, err
	}
	query := `SELECT ` + remotePairingInvitationColumns + ` FROM remote_pairing_invitations WHERE 1 = 1`
	args := make([]any, 0, 3)
	if options.Status != "" {
		query += ` AND status = ?`
		args = append(args, options.Status)
	}
	if options.RequesterFingerprint != "" {
		query += ` AND requester_fingerprint = ?`
		args = append(args, options.RequesterFingerprint)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, normalizedRemoteListLimit(options.Limit))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]RemotePairingInvitation, 0)
	for rows.Next() {
		item, err := scanRemotePairingInvitation(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ClaimRemotePairingInvitation(ctx context.Context, id, codeHash string, expectedRevision int64, requester RemotePairingRequester) (RemotePairingInvitation, error) {
	id, err := normalizeRemoteID("invitation id", id)
	if err != nil {
		return RemotePairingInvitation{}, err
	}
	codeHash = strings.TrimSpace(codeHash)
	if !validRemoteSHA256(codeHash) {
		return RemotePairingInvitation{}, errors.New("remote pairing invitation code hash must be a lowercase SHA-256 hash")
	}
	if expectedRevision < 1 {
		return RemotePairingInvitation{}, errors.New("remote pairing invitation expected revision must be positive")
	}
	requester, err = canonicalRemotePairingRequester(requester)
	if err != nil {
		return RemotePairingInvitation{}, err
	}
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE remote_pairing_invitations SET status = 'claimed', requester_display_name = ?, requester_installation_id = ?, requester_public_key = ?, requester_fingerprint = ?, failed_attempts = 0, locked_until = NULL, revision = revision + 1, updated_at = ? WHERE id = ? AND code_hash = ? AND status = 'open' AND revision = ? AND expires_at > ? AND (locked_until IS NULL OR locked_until <= ?)`, requester.DisplayName, requester.InstallationID, requester.PublicKey, requester.Fingerprint, now, id, codeHash, expectedRevision, now, now)
	if err != nil {
		return RemotePairingInvitation{}, fmt.Errorf("claim remote pairing invitation: %w", err)
	}
	if err := requireRemoteTransition(result, "claim remote pairing invitation"); err != nil {
		return RemotePairingInvitation{}, err
	}
	return getRemotePairingInvitation(ctx, s.db, id)
}

func (s *Store) RecordRemotePairingInvitationFailure(ctx context.Context, id string, expectedRevision int64, maxAttempts int, lockedUntil string) (RemotePairingInvitation, error) {
	id, err := normalizeRemoteID("invitation id", id)
	if err != nil {
		return RemotePairingInvitation{}, err
	}
	if expectedRevision < 1 || maxAttempts < 1 || maxAttempts > 100000 {
		return RemotePairingInvitation{}, errors.New("invalid remote pairing invitation failure policy")
	}
	lockedUntil, err = canonicalRemoteTime("invitation locked until", lockedUntil, true)
	if err != nil {
		return RemotePairingInvitation{}, err
	}
	now := Now()
	if lockedUntil <= now {
		return RemotePairingInvitation{}, errors.New("remote pairing invitation lock must be in the future")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE remote_pairing_invitations SET failed_attempts = failed_attempts + 1, locked_until = CASE WHEN failed_attempts + 1 >= ? THEN ? ELSE NULL END, revision = revision + 1, updated_at = ? WHERE id = ? AND status = 'open' AND revision = ? AND expires_at > ? AND ? <= expires_at AND (locked_until IS NULL OR locked_until <= ?)`, maxAttempts, lockedUntil, now, id, expectedRevision, now, lockedUntil, now)
	if err != nil {
		return RemotePairingInvitation{}, fmt.Errorf("record remote pairing invitation failure: %w", err)
	}
	if err := requireRemoteTransition(result, "record remote pairing invitation failure"); err != nil {
		return RemotePairingInvitation{}, err
	}
	return getRemotePairingInvitation(ctx, s.db, id)
}

func (s *Store) ApproveRemotePairingInvitation(ctx context.Context, id string, expectedRevision int64, pairing RemotePeerPairing, grants []RemotePeerGrant) (RemotePeerPairing, []RemotePeerGrant, error) {
	id, err := normalizeRemoteID("invitation id", id)
	if err != nil {
		return RemotePeerPairing{}, nil, err
	}
	if expectedRevision < 1 {
		return RemotePeerPairing{}, nil, errors.New("remote pairing invitation expected revision must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RemotePeerPairing{}, nil, err
	}
	defer tx.Rollback()
	invitation, err := getRemotePairingInvitation(ctx, tx, id)
	if err != nil {
		return RemotePeerPairing{}, nil, err
	}
	now := Now()
	if invitation.Status != RemotePairingInvitationStatusClaimed || invitation.Revision != expectedRevision || invitation.ExpiresAt <= now || !remoteInvitationRequesterComplete(invitation) {
		return RemotePeerPairing{}, nil, fmt.Errorf("%w: remote pairing invitation cannot be approved", ErrConflict)
	}
	pairing.LocalRole = RemotePeerLocalRoleHost
	pairing.DisplayName = invitation.RequesterDisplayName
	pairing.PeerInstallationID = invitation.RequesterInstallationID
	pairing.PeerPublicKey = invitation.RequesterPublicKey
	pairing.PeerFingerprint = invitation.RequesterFingerprint
	pairing.EndpointOrigin = ""
	pairing.Status = RemotePeerPairingStatusActive
	pairing, scopesJSON, err := canonicalRemotePeerPairingForCreate(pairing, RemotePeerLocalRoleHost, now)
	if err != nil {
		return RemotePeerPairing{}, nil, err
	}
	canonicalGrants, grantJSON, err := canonicalRemotePeerGrants(ctx, tx, pairing.ID, pairing.GrantRevision, grants, now)
	if err != nil {
		return RemotePeerPairing{}, nil, err
	}
	if err := validateRemoteGrantScopes(pairing.Scopes, canonicalGrants); err != nil {
		return RemotePeerPairing{}, nil, err
	}
	if err := insertRemotePeerPairing(ctx, tx, pairing, scopesJSON); err != nil {
		return RemotePeerPairing{}, nil, err
	}
	if err := insertRemotePeerGrants(ctx, tx, canonicalGrants, grantJSON); err != nil {
		return RemotePeerPairing{}, nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE remote_pairing_invitations SET status = 'approved', locked_until = NULL, revision = revision + 1, updated_at = ?, completed_at = ? WHERE id = ? AND status = 'claimed' AND revision = ? AND expires_at > ? AND requester_display_name IS NOT NULL AND requester_installation_id IS NOT NULL AND requester_public_key IS NOT NULL AND requester_fingerprint IS NOT NULL`, now, now, id, expectedRevision, now)
	if err != nil {
		return RemotePeerPairing{}, nil, fmt.Errorf("approve remote pairing invitation: %w", err)
	}
	if err := requireRemoteTransition(result, "approve remote pairing invitation"); err != nil {
		return RemotePeerPairing{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return RemotePeerPairing{}, nil, err
	}
	return pairing, canonicalGrants, nil
}

func (s *Store) RejectRemotePairingInvitation(ctx context.Context, id, expectedStatus string, expectedRevision int64) (RemotePairingInvitation, error) {
	return s.transitionRemotePairingInvitation(ctx, id, expectedStatus, RemotePairingInvitationStatusRejected, expectedRevision, false)
}

func (s *Store) RevokeRemotePairingInvitation(ctx context.Context, id, expectedStatus string, expectedRevision int64) (RemotePairingInvitation, error) {
	return s.transitionRemotePairingInvitation(ctx, id, expectedStatus, RemotePairingInvitationStatusRevoked, expectedRevision, false)
}

func (s *Store) ExpireRemotePairingInvitation(ctx context.Context, id, expectedStatus string, expectedRevision int64) (RemotePairingInvitation, error) {
	return s.transitionRemotePairingInvitation(ctx, id, expectedStatus, RemotePairingInvitationStatusExpired, expectedRevision, true)
}

func (s *Store) transitionRemotePairingInvitation(ctx context.Context, id, expectedStatus, targetStatus string, expectedRevision int64, requireExpired bool) (RemotePairingInvitation, error) {
	id, err := normalizeRemoteID("invitation id", id)
	if err != nil {
		return RemotePairingInvitation{}, err
	}
	expectedStatus = strings.TrimSpace(expectedStatus)
	if expectedStatus != RemotePairingInvitationStatusOpen && expectedStatus != RemotePairingInvitationStatusClaimed {
		return RemotePairingInvitation{}, errors.New("invalid expected remote pairing invitation status")
	}
	if expectedRevision < 1 {
		return RemotePairingInvitation{}, errors.New("remote pairing invitation expected revision must be positive")
	}
	now := Now()
	query := `UPDATE remote_pairing_invitations SET status = ?, locked_until = NULL, revision = revision + 1, updated_at = ?, completed_at = ? WHERE id = ? AND status = ? AND revision = ?`
	args := []any{targetStatus, now, now, id, expectedStatus, expectedRevision}
	if requireExpired {
		query += ` AND expires_at <= ?`
		args = append(args, now)
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return RemotePairingInvitation{}, fmt.Errorf("transition remote pairing invitation: %w", err)
	}
	if err := requireRemoteTransition(result, "transition remote pairing invitation"); err != nil {
		return RemotePairingInvitation{}, err
	}
	return getRemotePairingInvitation(ctx, s.db, id)
}

// CreateRemotePeerPairing creates the controller-side record for a pairing
// established by the remote host. Host-side records are created only by
// ApproveRemotePairingInvitation so their identity stays bound to the claim.
func (s *Store) CreateRemotePeerPairing(ctx context.Context, pairing RemotePeerPairing) (RemotePeerPairing, error) {
	now := Now()
	canonical, scopesJSON, err := canonicalRemotePeerPairingForCreate(pairing, RemotePeerLocalRoleController, now)
	if err != nil {
		return RemotePeerPairing{}, err
	}
	if err := insertRemotePeerPairing(ctx, s.db, canonical, scopesJSON); err != nil {
		return RemotePeerPairing{}, err
	}
	return canonical, nil
}

func (s *Store) GetRemotePeerPairing(ctx context.Context, id string) (RemotePeerPairing, error) {
	id, err := normalizeRemoteID("pairing id", id)
	if err != nil {
		return RemotePeerPairing{}, err
	}
	return getRemotePeerPairing(ctx, s.db, id)
}

func (s *Store) ListRemotePeerPairings(ctx context.Context, optionArgs ...RemotePeerPairingListOptions) ([]RemotePeerPairing, error) {
	options, err := normalizeRemotePeerPairingListOptions(optionArgs)
	if err != nil {
		return nil, err
	}
	query := `SELECT ` + remotePeerPairingColumns + ` FROM remote_peer_pairings WHERE 1 = 1`
	args := make([]any, 0, 3)
	if options.LocalRole != "" {
		query += ` AND local_role = ?`
		args = append(args, options.LocalRole)
	}
	if options.Status != "" {
		query += ` AND status = ?`
		args = append(args, options.Status)
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, normalizedRemoteListLimit(options.Limit))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]RemotePeerPairing, 0)
	for rows.Next() {
		item, err := scanRemotePeerPairing(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ReplaceRemotePeerGrants(ctx context.Context, pairingID string, expectedGrantRevision int64, grants []RemotePeerGrant) (RemotePeerPairing, []RemotePeerGrant, error) {
	pairingID, err := normalizeRemoteID("pairing id", pairingID)
	if err != nil {
		return RemotePeerPairing{}, nil, err
	}
	if expectedGrantRevision < 1 {
		return RemotePeerPairing{}, nil, errors.New("remote peer expected grant revision must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RemotePeerPairing{}, nil, err
	}
	defer tx.Rollback()
	now := Now()
	pairing, err := getRemotePeerPairing(ctx, tx, pairingID)
	if err != nil {
		return RemotePeerPairing{}, nil, err
	}
	if pairing.Status != RemotePeerPairingStatusActive || pairing.GrantRevision != expectedGrantRevision || pairing.ExpiresAt != "" && pairing.ExpiresAt <= now {
		return RemotePeerPairing{}, nil, fmt.Errorf("%w: remote peer grants cannot be replaced", ErrConflict)
	}
	newRevision := expectedGrantRevision + 1
	canonicalGrants, grantJSON, err := canonicalRemotePeerGrants(ctx, tx, pairingID, newRevision, grants, now)
	if err != nil {
		return RemotePeerPairing{}, nil, err
	}
	if err := validateRemoteGrantScopes(pairing.Scopes, canonicalGrants); err != nil {
		return RemotePeerPairing{}, nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE remote_peer_pairings SET grant_revision = grant_revision + 1, updated_at = ? WHERE id = ? AND status = 'active' AND grant_revision = ? AND (expires_at IS NULL OR expires_at > ?)`, now, pairingID, expectedGrantRevision, now)
	if err != nil {
		return RemotePeerPairing{}, nil, err
	}
	if err := requireRemoteTransition(result, "replace remote peer grants"); err != nil {
		return RemotePeerPairing{}, nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM remote_peer_grants WHERE pairing_id = ?`, pairingID); err != nil {
		return RemotePeerPairing{}, nil, err
	}
	if err := insertRemotePeerGrants(ctx, tx, canonicalGrants, grantJSON); err != nil {
		return RemotePeerPairing{}, nil, err
	}
	pairing, err = getRemotePeerPairing(ctx, tx, pairingID)
	if err != nil {
		return RemotePeerPairing{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return RemotePeerPairing{}, nil, err
	}
	return pairing, canonicalGrants, nil
}

// ReplaceRemotePeerAuthorization atomically updates host-wide scopes, optional
// expiration, and the complete per-agent grant set under one grant revision.
func (s *Store) ReplaceRemotePeerAuthorization(ctx context.Context, pairingID string, expectedGrantRevision int64, scopes []string, expiresAt string, grants []RemotePeerGrant) (RemotePeerPairing, []RemotePeerGrant, error) {
	pairingID, err := normalizeRemoteID("pairing id", pairingID)
	if err != nil {
		return RemotePeerPairing{}, nil, err
	}
	if expectedGrantRevision < 1 {
		return RemotePeerPairing{}, nil, errors.New("remote peer expected grant revision must be positive")
	}
	normalizedScopes, scopesJSON, err := normalizeRemoteScopes(scopes)
	if err != nil {
		return RemotePeerPairing{}, nil, err
	}
	expiresAt, err = canonicalRemoteTime("pairing expiration time", expiresAt, false)
	if err != nil {
		return RemotePeerPairing{}, nil, err
	}
	now := Now()
	if expiresAt != "" && expiresAt <= now {
		return RemotePeerPairing{}, nil, errors.New("remote peer pairing expiration must be in the future")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RemotePeerPairing{}, nil, err
	}
	defer tx.Rollback()
	pairing, err := getRemotePeerPairing(ctx, tx, pairingID)
	if err != nil {
		return RemotePeerPairing{}, nil, err
	}
	if pairing.LocalRole != RemotePeerLocalRoleHost || pairing.Status != RemotePeerPairingStatusActive || pairing.GrantRevision != expectedGrantRevision || pairing.ExpiresAt != "" && pairing.ExpiresAt <= now {
		return RemotePeerPairing{}, nil, fmt.Errorf("%w: remote peer authorization cannot be replaced", ErrConflict)
	}
	newRevision := expectedGrantRevision + 1
	canonicalGrants, grantJSON, err := canonicalRemotePeerGrants(ctx, tx, pairingID, newRevision, grants, now)
	if err != nil {
		return RemotePeerPairing{}, nil, err
	}
	if err := validateRemoteGrantScopes(normalizedScopes, canonicalGrants); err != nil {
		return RemotePeerPairing{}, nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE remote_peer_pairings SET scopes_json = ?, expires_at = NULLIF(?, ''), grant_revision = grant_revision + 1, updated_at = ? WHERE id = ? AND local_role = 'host' AND status = 'active' AND grant_revision = ? AND (expires_at IS NULL OR expires_at > ?)`, scopesJSON, expiresAt, now, pairingID, expectedGrantRevision, now)
	if err != nil {
		return RemotePeerPairing{}, nil, err
	}
	if err := requireRemoteTransition(result, "replace remote peer authorization"); err != nil {
		return RemotePeerPairing{}, nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM remote_peer_grants WHERE pairing_id = ?`, pairingID); err != nil {
		return RemotePeerPairing{}, nil, err
	}
	if err := insertRemotePeerGrants(ctx, tx, canonicalGrants, grantJSON); err != nil {
		return RemotePeerPairing{}, nil, err
	}
	pairing, err = getRemotePeerPairing(ctx, tx, pairingID)
	if err != nil {
		return RemotePeerPairing{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return RemotePeerPairing{}, nil, err
	}
	return pairing, canonicalGrants, nil
}

func (s *Store) GetRemotePeerGrant(ctx context.Context, pairingID, agentID string) (RemotePeerGrant, error) {
	pairingID, err := normalizeRemoteID("pairing id", pairingID)
	if err != nil {
		return RemotePeerGrant{}, err
	}
	agentID, err = normalizeRemoteID("grant agent id", agentID)
	if err != nil {
		return RemotePeerGrant{}, err
	}
	return scanRemotePeerGrant(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT `+remotePeerGrantColumns+` FROM remote_peer_grants WHERE pairing_id = ? AND agent_id = ?`, pairingID, agentID).Scan(dest...)
	})
}

func (s *Store) ListRemotePeerGrants(ctx context.Context, pairingID string) ([]RemotePeerGrant, error) {
	pairingID, err := normalizeRemoteID("pairing id", pairingID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+remotePeerGrantColumns+` FROM remote_peer_grants WHERE pairing_id = ? ORDER BY project_id, agent_id, id`, pairingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]RemotePeerGrant, 0)
	for rows.Next() {
		item, err := scanRemotePeerGrant(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) RevokeRemotePeerPairing(ctx context.Context, id, expectedStatus string, expectedCredentialRevision int64) (RemotePeerPairing, error) {
	return s.transitionRemotePeerPairing(ctx, id, expectedStatus, RemotePeerPairingStatusRevoked, expectedCredentialRevision, false)
}

func (s *Store) ExpireRemotePeerPairing(ctx context.Context, id, expectedStatus string, expectedCredentialRevision int64) (RemotePeerPairing, error) {
	return s.transitionRemotePeerPairing(ctx, id, expectedStatus, RemotePeerPairingStatusExpired, expectedCredentialRevision, true)
}

func (s *Store) transitionRemotePeerPairing(ctx context.Context, id, expectedStatus, targetStatus string, expectedCredentialRevision int64, requireExpired bool) (RemotePeerPairing, error) {
	id, err := normalizeRemoteID("pairing id", id)
	if err != nil {
		return RemotePeerPairing{}, err
	}
	expectedStatus = strings.TrimSpace(expectedStatus)
	if expectedStatus != RemotePeerPairingStatusActive || expectedCredentialRevision < 1 {
		return RemotePeerPairing{}, errors.New("invalid expected remote peer pairing state")
	}
	now := Now()
	var query string
	var args []any
	if targetStatus == RemotePeerPairingStatusRevoked {
		query = `UPDATE remote_peer_pairings SET status = 'revoked', credential_revision = credential_revision + 1, revoked_at = ?, updated_at = ? WHERE id = ? AND status = ? AND credential_revision = ?`
		args = []any{now, now, id, expectedStatus, expectedCredentialRevision}
	} else {
		query = `UPDATE remote_peer_pairings SET status = 'expired', credential_revision = credential_revision + 1, revoked_at = NULL, updated_at = ? WHERE id = ? AND status = ? AND credential_revision = ?`
		args = []any{now, id, expectedStatus, expectedCredentialRevision}
		if requireExpired {
			query += ` AND expires_at IS NOT NULL AND expires_at <= ?`
			args = append(args, now)
		}
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return RemotePeerPairing{}, fmt.Errorf("transition remote peer pairing: %w", err)
	}
	if err := requireRemoteTransition(result, "transition remote peer pairing"); err != nil {
		return RemotePeerPairing{}, err
	}
	return getRemotePeerPairing(ctx, s.db, id)
}

func (s *Store) TouchRemotePeerPairingLastSeen(ctx context.Context, id, seenAt string) (RemotePeerPairing, error) {
	id, err := normalizeRemoteID("pairing id", id)
	if err != nil {
		return RemotePeerPairing{}, err
	}
	if strings.TrimSpace(seenAt) == "" {
		seenAt = Now()
	} else if seenAt, err = canonicalRemoteTime("pairing last seen time", seenAt, true); err != nil {
		return RemotePeerPairing{}, err
	}
	now := Now()
	if seenAt > now {
		return RemotePeerPairing{}, errors.New("remote peer last seen time cannot be in the future")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE remote_peer_pairings SET last_seen_at = CASE WHEN last_seen_at IS NULL OR last_seen_at < ? THEN ? ELSE last_seen_at END, updated_at = CASE WHEN updated_at < ? THEN ? ELSE updated_at END WHERE id = ? AND status = 'active' AND (expires_at IS NULL OR expires_at > ?) AND paired_at <= ?`, seenAt, seenAt, seenAt, seenAt, id, now, seenAt)
	if err != nil {
		return RemotePeerPairing{}, err
	}
	if err := requireRemoteTransition(result, "touch remote peer pairing last seen"); err != nil {
		return RemotePeerPairing{}, err
	}
	return getRemotePeerPairing(ctx, s.db, id)
}

type remoteCollaborationExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertRemotePeerPairing(ctx context.Context, execer remoteCollaborationExecer, pairing RemotePeerPairing, scopesJSON string) error {
	_, err := execer.ExecContext(ctx, `INSERT INTO remote_peer_pairings (id, local_role, display_name, peer_installation_id, peer_public_key, peer_fingerprint, endpoint_origin, status, scopes_json, credential_revision, grant_revision, expires_at, last_seen_at, paired_at, revoked_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, NULLIF(?,''), 'active', ?, ?, ?, NULLIF(?,''), NULL, ?, NULL, ?, ?)`, pairing.ID, pairing.LocalRole, pairing.DisplayName, pairing.PeerInstallationID, pairing.PeerPublicKey, pairing.PeerFingerprint, pairing.EndpointOrigin, scopesJSON, pairing.CredentialRevision, pairing.GrantRevision, pairing.ExpiresAt, pairing.PairedAt, pairing.CreatedAt, pairing.UpdatedAt)
	if err != nil {
		if isUniqueConstraint(err) {
			return fmt.Errorf("%w: remote peer pairing already exists", ErrConflict)
		}
		return fmt.Errorf("create remote peer pairing: %w", err)
	}
	return nil
}

func insertRemotePeerGrants(ctx context.Context, execer remoteCollaborationExecer, grants []RemotePeerGrant, scopesJSON []string) error {
	for index, grant := range grants {
		if _, err := execer.ExecContext(ctx, `INSERT INTO remote_peer_grants (id, pairing_id, project_id, agent_id, scopes_json, permission_mode_cap, revision, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, grant.ID, grant.PairingID, grant.ProjectID, grant.AgentID, scopesJSON[index], grant.PermissionModeCap, grant.Revision, grant.CreatedAt, grant.UpdatedAt); err != nil {
			if isUniqueConstraint(err) {
				return fmt.Errorf("%w: duplicate remote peer grant", ErrConflict)
			}
			return fmt.Errorf("create remote peer grant: %w", err)
		}
	}
	return nil
}

func canonicalRemotePairingInvitationForCreate(invitation RemotePairingInvitation) (RemotePairingInvitation, error) {
	var err error
	if invitation.ID == "" {
		invitation.ID = NewID()
	}
	if invitation.ID, err = normalizeRemoteID("invitation id", invitation.ID); err != nil {
		return RemotePairingInvitation{}, err
	}
	invitation.CodeHash = strings.TrimSpace(invitation.CodeHash)
	if !validRemoteSHA256(invitation.CodeHash) {
		return RemotePairingInvitation{}, errors.New("remote pairing invitation code hash must be a lowercase SHA-256 hash")
	}
	if invitation.ProtocolVersion < 1 || invitation.ProtocolVersion > 1000 {
		return RemotePairingInvitation{}, errors.New("invalid remote pairing protocol version")
	}
	if invitation.ExpiresAt, err = canonicalRemoteTime("invitation expiration time", invitation.ExpiresAt, true); err != nil {
		return RemotePairingInvitation{}, err
	}
	now := Now()
	if invitation.ExpiresAt <= now {
		return RemotePairingInvitation{}, errors.New("remote pairing invitation expiration must be in the future")
	}
	invitation.Status = RemotePairingInvitationStatusOpen
	invitation.RequesterDisplayName = ""
	invitation.RequesterInstallationID = ""
	invitation.RequesterPublicKey = ""
	invitation.RequesterFingerprint = ""
	invitation.FailedAttempts = 0
	invitation.LockedUntil = ""
	invitation.Revision = 1
	invitation.CreatedAt = now
	invitation.UpdatedAt = now
	invitation.CompletedAt = ""
	return invitation, nil
}

func canonicalRemotePairingRequester(requester RemotePairingRequester) (RemotePairingRequester, error) {
	requester.DisplayName = strings.TrimSpace(requester.DisplayName)
	requester.InstallationID = strings.TrimSpace(requester.InstallationID)
	requester.PublicKey = strings.TrimSpace(requester.PublicKey)
	requester.Fingerprint = strings.TrimSpace(requester.Fingerprint)
	if err := validateRemoteText("requester display name", requester.DisplayName, 256, true); err != nil {
		return RemotePairingRequester{}, err
	}
	if !validRemoteInstallationID(requester.InstallationID) {
		return RemotePairingRequester{}, errors.New("invalid remote requester installation id")
	}
	if err := validateRemotePublicKeyFingerprint(requester.PublicKey, requester.Fingerprint); err != nil {
		return RemotePairingRequester{}, fmt.Errorf("invalid remote requester identity: %w", err)
	}
	return requester, nil
}

func canonicalRemotePeerPairingForCreate(pairing RemotePeerPairing, requiredRole, now string) (RemotePeerPairing, string, error) {
	var err error
	if pairing.ID == "" {
		pairing.ID = NewID()
	}
	if pairing.ID, err = normalizeRemoteID("pairing id", pairing.ID); err != nil {
		return RemotePeerPairing{}, "", err
	}
	pairing.LocalRole = strings.TrimSpace(pairing.LocalRole)
	if pairing.LocalRole == "" {
		pairing.LocalRole = requiredRole
	}
	if pairing.LocalRole != requiredRole {
		return RemotePeerPairing{}, "", fmt.Errorf("remote peer pairing local role must be %s", requiredRole)
	}
	pairing.DisplayName = strings.TrimSpace(pairing.DisplayName)
	pairing.PeerInstallationID = strings.TrimSpace(pairing.PeerInstallationID)
	pairing.PeerPublicKey = strings.TrimSpace(pairing.PeerPublicKey)
	pairing.PeerFingerprint = strings.TrimSpace(pairing.PeerFingerprint)
	if err := validateRemoteText("pairing display name", pairing.DisplayName, 256, true); err != nil {
		return RemotePeerPairing{}, "", err
	}
	if !validRemoteInstallationID(pairing.PeerInstallationID) {
		return RemotePeerPairing{}, "", errors.New("invalid remote peer installation id")
	}
	if err := validateRemotePublicKeyFingerprint(pairing.PeerPublicKey, pairing.PeerFingerprint); err != nil {
		return RemotePeerPairing{}, "", fmt.Errorf("invalid remote peer identity: %w", err)
	}
	pairing.EndpointOrigin, err = canonicalRemoteEndpointOrigin(pairing.EndpointOrigin, pairing.LocalRole == RemotePeerLocalRoleController)
	if err != nil {
		return RemotePeerPairing{}, "", err
	}
	var scopesJSON string
	pairing.Scopes, scopesJSON, err = normalizeRemoteScopes(pairing.Scopes)
	if err != nil {
		return RemotePeerPairing{}, "", err
	}
	pairing.ExpiresAt, err = canonicalRemoteTime("pairing expiration time", pairing.ExpiresAt, false)
	if err != nil {
		return RemotePeerPairing{}, "", err
	}
	if pairing.ExpiresAt != "" && pairing.ExpiresAt <= now {
		return RemotePeerPairing{}, "", errors.New("remote peer pairing expiration must be in the future")
	}
	if pairing.CredentialRevision == 0 {
		pairing.CredentialRevision = 1
	}
	if pairing.GrantRevision == 0 {
		pairing.GrantRevision = 1
	}
	if pairing.CredentialRevision < 1 || pairing.GrantRevision < 1 {
		return RemotePeerPairing{}, "", errors.New("invalid remote peer pairing revision")
	}
	pairing.Status = RemotePeerPairingStatusActive
	pairing.LastSeenAt = ""
	pairing.PairedAt = now
	pairing.RevokedAt = ""
	pairing.CreatedAt = now
	pairing.UpdatedAt = now
	return pairing, scopesJSON, nil
}

func validateRemoteGrantScopes(pairingScopes []string, grants []RemotePeerGrant) error {
	allowed := make(map[string]struct{}, len(pairingScopes))
	for _, scope := range pairingScopes {
		allowed[scope] = struct{}{}
	}
	for _, grant := range grants {
		for _, scope := range grant.Scopes {
			if _, ok := allowed[scope]; !ok {
				return fmt.Errorf("remote peer grant scope %q exceeds pairing scopes", scope)
			}
		}
	}
	return nil
}

func canonicalRemotePeerGrants(ctx context.Context, queryer remoteCollaborationQueryer, pairingID string, revision int64, grants []RemotePeerGrant, now string) ([]RemotePeerGrant, []string, error) {
	if len(grants) > 10000 {
		return nil, nil, errors.New("too many remote peer grants")
	}
	canonical := make([]RemotePeerGrant, 0, len(grants))
	encoded := make([]string, 0, len(grants))
	seenAgents := make(map[string]struct{}, len(grants))
	for _, grant := range grants {
		grant.PairingID = pairingID
		var err error
		if grant.ID == "" {
			grant.ID = NewID()
		}
		if grant.ID, err = normalizeRemoteID("grant id", grant.ID); err != nil {
			return nil, nil, err
		}
		if grant.ProjectID, err = normalizeRemoteID("grant project id", grant.ProjectID); err != nil {
			return nil, nil, err
		}
		if grant.AgentID, err = normalizeRemoteID("grant agent id", grant.AgentID); err != nil {
			return nil, nil, err
		}
		if _, exists := seenAgents[grant.AgentID]; exists {
			return nil, nil, errors.New("duplicate remote peer grant agent")
		}
		seenAgents[grant.AgentID] = struct{}{}
		grant.PermissionModeCap = strings.TrimSpace(grant.PermissionModeCap)
		if grant.PermissionModeCap != RemotePeerPermissionModeReadOnly && grant.PermissionModeCap != RemotePeerPermissionModeAcceptEdits {
			return nil, nil, errors.New("invalid remote peer grant permission mode cap")
		}
		var scopesJSON string
		grant.Scopes, scopesJSON, err = normalizeRemoteScopes(grant.Scopes)
		if err != nil {
			return nil, nil, err
		}
		var count int
		if err := queryer.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents a JOIN worklines w ON w.id = a.workline_id WHERE a.id = ? AND w.project_id = ?`, grant.AgentID, grant.ProjectID).Scan(&count); err != nil {
			return nil, nil, err
		}
		if count != 1 {
			return nil, nil, errors.New("remote peer grant agent does not belong to project")
		}
		grant.Revision = revision
		grant.CreatedAt = now
		grant.UpdatedAt = now
		canonical = append(canonical, grant)
		encoded = append(encoded, scopesJSON)
	}
	return canonical, encoded, nil
}

func getRemotePairingInvitation(ctx context.Context, queryer remoteCollaborationQueryer, id string) (RemotePairingInvitation, error) {
	return scanRemotePairingInvitation(func(dest ...any) error {
		return queryer.QueryRowContext(ctx, `SELECT `+remotePairingInvitationColumns+` FROM remote_pairing_invitations WHERE id = ?`, id).Scan(dest...)
	})
}

func getRemotePeerPairing(ctx context.Context, queryer remoteCollaborationQueryer, id string) (RemotePeerPairing, error) {
	return scanRemotePeerPairing(func(dest ...any) error {
		return queryer.QueryRowContext(ctx, `SELECT `+remotePeerPairingColumns+` FROM remote_peer_pairings WHERE id = ?`, id).Scan(dest...)
	})
}

func scanRemotePairingInvitation(scan func(...any) error) (RemotePairingInvitation, error) {
	var invitation RemotePairingInvitation
	if err := scan(&invitation.ID, &invitation.CodeHash, &invitation.ProtocolVersion, &invitation.Status, &invitation.RequesterDisplayName, &invitation.RequesterInstallationID, &invitation.RequesterPublicKey, &invitation.RequesterFingerprint, &invitation.FailedAttempts, &invitation.LockedUntil, &invitation.ExpiresAt, &invitation.Revision, &invitation.CreatedAt, &invitation.UpdatedAt, &invitation.CompletedAt); err != nil {
		return RemotePairingInvitation{}, err
	}
	if err := validateStoredRemotePairingInvitation(invitation); err != nil {
		return RemotePairingInvitation{}, err
	}
	return invitation, nil
}

func scanRemotePeerPairing(scan func(...any) error) (RemotePeerPairing, error) {
	var pairing RemotePeerPairing
	var scopesJSON string
	if err := scan(&pairing.ID, &pairing.LocalRole, &pairing.DisplayName, &pairing.PeerInstallationID, &pairing.PeerPublicKey, &pairing.PeerFingerprint, &pairing.EndpointOrigin, &pairing.Status, &scopesJSON, &pairing.CredentialRevision, &pairing.GrantRevision, &pairing.ExpiresAt, &pairing.LastSeenAt, &pairing.PairedAt, &pairing.RevokedAt, &pairing.CreatedAt, &pairing.UpdatedAt); err != nil {
		return RemotePeerPairing{}, err
	}
	if err := json.Unmarshal([]byte(scopesJSON), &pairing.Scopes); err != nil {
		return RemotePeerPairing{}, errors.New("invalid stored remote peer pairing scopes")
	}
	normalized, normalizedJSON, err := normalizeRemoteScopes(pairing.Scopes)
	if err != nil || normalizedJSON != scopesJSON {
		return RemotePeerPairing{}, errors.New("invalid stored remote peer pairing scopes")
	}
	pairing.Scopes = normalized
	if _, err := normalizeRemoteID("pairing id", pairing.ID); err != nil || validateRemoteText("pairing display name", pairing.DisplayName, 256, true) != nil || !validRemoteInstallationID(pairing.PeerInstallationID) || !validRemotePublicKey(pairing.PeerPublicKey) || !validRemoteSHA256(pairing.PeerFingerprint) {
		return RemotePeerPairing{}, errors.New("invalid stored remote peer pairing")
	}
	if pairing.LocalRole != RemotePeerLocalRoleHost && pairing.LocalRole != RemotePeerLocalRoleController || !validRemotePeerPairingStatus(pairing.Status) || pairing.CredentialRevision < 1 || pairing.GrantRevision < 1 {
		return RemotePeerPairing{}, errors.New("invalid stored remote peer pairing")
	}
	if endpoint, err := canonicalRemoteEndpointOrigin(pairing.EndpointOrigin, pairing.LocalRole == RemotePeerLocalRoleController); err != nil || endpoint != pairing.EndpointOrigin {
		return RemotePeerPairing{}, errors.New("invalid stored remote peer pairing endpoint")
	}
	for name, value := range map[string]string{"paired": pairing.PairedAt, "created": pairing.CreatedAt, "updated": pairing.UpdatedAt} {
		if canonical, err := canonicalRemoteTime("pairing "+name+" time", value, true); err != nil || canonical != value {
			return RemotePeerPairing{}, errors.New("invalid stored remote peer pairing timestamp")
		}
	}
	for name, value := range map[string]string{"expiration": pairing.ExpiresAt, "last seen": pairing.LastSeenAt, "revoked": pairing.RevokedAt} {
		if canonical, err := canonicalRemoteTime("pairing "+name+" time", value, false); err != nil || canonical != value {
			return RemotePeerPairing{}, errors.New("invalid stored remote peer pairing timestamp")
		}
	}
	return pairing, nil
}

func scanRemotePeerGrant(scan func(...any) error) (RemotePeerGrant, error) {
	var grant RemotePeerGrant
	var scopesJSON string
	if err := scan(&grant.ID, &grant.PairingID, &grant.ProjectID, &grant.AgentID, &scopesJSON, &grant.PermissionModeCap, &grant.Revision, &grant.CreatedAt, &grant.UpdatedAt); err != nil {
		return RemotePeerGrant{}, err
	}
	if err := json.Unmarshal([]byte(scopesJSON), &grant.Scopes); err != nil {
		return RemotePeerGrant{}, errors.New("invalid stored remote peer grant scopes")
	}
	normalized, normalizedJSON, err := normalizeRemoteScopes(grant.Scopes)
	if err != nil || normalizedJSON != scopesJSON {
		return RemotePeerGrant{}, errors.New("invalid stored remote peer grant scopes")
	}
	grant.Scopes = normalized
	if grant.PermissionModeCap != RemotePeerPermissionModeReadOnly && grant.PermissionModeCap != RemotePeerPermissionModeAcceptEdits || grant.Revision < 1 {
		return RemotePeerGrant{}, errors.New("invalid stored remote peer grant")
	}
	return grant, nil
}

func validateStoredRemotePairingInvitation(invitation RemotePairingInvitation) error {
	if _, err := normalizeRemoteID("invitation id", invitation.ID); err != nil || !validRemoteSHA256(invitation.CodeHash) || invitation.ProtocolVersion < 1 || invitation.ProtocolVersion > 1000 || !validRemoteInvitationStatus(invitation.Status) || invitation.Revision < 1 || invitation.FailedAttempts < 0 {
		return errors.New("invalid stored remote pairing invitation")
	}
	for name, value := range map[string]string{"expiration": invitation.ExpiresAt, "created": invitation.CreatedAt, "updated": invitation.UpdatedAt} {
		if canonical, err := canonicalRemoteTime("invitation "+name+" time", value, true); err != nil || canonical != value {
			return errors.New("invalid stored remote pairing invitation timestamp")
		}
	}
	for name, value := range map[string]string{"locked": invitation.LockedUntil, "completed": invitation.CompletedAt} {
		if canonical, err := canonicalRemoteTime("invitation "+name+" time", value, false); err != nil || canonical != value {
			return errors.New("invalid stored remote pairing invitation timestamp")
		}
	}
	if remoteInvitationRequesterComplete(invitation) {
		if _, err := canonicalRemotePairingRequester(RemotePairingRequester{DisplayName: invitation.RequesterDisplayName, InstallationID: invitation.RequesterInstallationID, PublicKey: invitation.RequesterPublicKey, Fingerprint: invitation.RequesterFingerprint}); err != nil {
			return errors.New("invalid stored remote pairing invitation requester")
		}
	} else if invitation.RequesterDisplayName != "" || invitation.RequesterInstallationID != "" || invitation.RequesterPublicKey != "" || invitation.RequesterFingerprint != "" {
		return errors.New("incomplete stored remote pairing invitation requester")
	}
	return nil
}

func normalizeRemoteScopes(scopes []string) ([]string, string, error) {
	allowed := map[string]struct{}{RemotePeerScopeObserve: {}, RemotePeerScopeSendTask: {}, RemotePeerScopeApproveOnce: {}, RemotePeerScopeExecuteTools: {}}
	unique := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if _, ok := allowed[scope]; !ok {
			return nil, "", fmt.Errorf("invalid remote peer scope %q", scope)
		}
		unique[scope] = struct{}{}
	}
	order := []string{RemotePeerScopeObserve, RemotePeerScopeSendTask, RemotePeerScopeApproveOnce, RemotePeerScopeExecuteTools}
	normalized := make([]string, 0, len(unique))
	for _, scope := range order {
		if _, ok := unique[scope]; ok {
			normalized = append(normalized, scope)
		}
	}
	encoded, err := json.Marshal(normalized)
	if err != nil || len(encoded) > remoteScopesJSONMaxBytes {
		return nil, "", errors.New("remote peer scopes are too large")
	}
	return normalized, string(encoded), nil
}

func canonicalRemoteEndpointOrigin(value string, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			return "", errors.New("remote peer endpoint origin is required")
		}
		return "", nil
	}
	if len(value) > 2048 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return "", errors.New("invalid remote peer endpoint origin")
	}
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" || parsed.ForceQuery {
		return "", errors.New("invalid remote peer endpoint origin")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("remote peer endpoint origin must use http or https")
	}
	if parsed.Path != "" && parsed.Path != "/" || parsed.RawPath != "" {
		return "", errors.New("remote peer endpoint must be an origin without a path")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" {
		return "", errors.New("invalid remote peer endpoint origin")
	}
	port := parsed.Port()
	host := hostname
	if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	if port != "" {
		if _, err := net.LookupPort("tcp", port); err != nil {
			return "", errors.New("invalid remote peer endpoint port")
		}
		host = net.JoinHostPort(hostname, port)
	}
	return scheme + "://" + host, nil
}

func canonicalRemoteTime(name, value string, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			return "", fmt.Errorf("remote %s is required", name)
		}
		return "", nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", fmt.Errorf("invalid remote %s", name)
	}
	return parsed.UTC().Format(timestampLayout), nil
}

func normalizeRemoteID(name, value string) (string, error) {
	value = strings.TrimSpace(value)
	if err := validateRemoteText(name, value, 128, true); err != nil {
		return "", err
	}
	for _, char := range value {
		if unicode.IsSpace(char) {
			return "", fmt.Errorf("invalid remote %s", name)
		}
	}
	return value, nil
}

func validateRemoteText(name, value string, maxBytes int, required bool) error {
	if required && value == "" {
		return fmt.Errorf("remote %s is required", name)
	}
	if value == "" {
		return nil
	}
	if len(value) > maxBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("invalid remote %s", name)
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return fmt.Errorf("invalid remote %s", name)
		}
	}
	return nil
}

func validRemoteInstallationID(value string) bool {
	if len(value) < 1 || len(value) > 128 || value != strings.TrimSpace(value) {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && !strings.ContainsRune("._:-", char) {
			return false
		}
	}
	return true
}

func validRemotePublicKey(value string) bool {
	_, err := decodeRemotePublicKey(value)
	return err == nil
}

func decodeRemotePublicKey(value string) ([]byte, error) {
	if len(value) != 44 || !utf8.ValidString(value) || strings.ContainsAny(value, " \t\r\n\x00") {
		return nil, errors.New("public key must be canonical base64 Ed25519 key material")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != 32 || base64.StdEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("public key must be canonical base64 Ed25519 key material")
	}
	return decoded, nil
}

func validateRemotePublicKeyFingerprint(publicKey, fingerprint string) error {
	decoded, err := decodeRemotePublicKey(publicKey)
	if err != nil {
		return err
	}
	if !validRemoteSHA256(fingerprint) {
		return errors.New("fingerprint must be a lowercase SHA-256 hash")
	}
	digest := sha256.Sum256(decoded)
	expected, err := hex.DecodeString(fingerprint)
	if err != nil || subtle.ConstantTimeCompare(digest[:], expected) != 1 {
		return errors.New("fingerprint does not match public key")
	}
	return nil
}

func validRemoteSHA256(value string) bool {
	return len(value) == 64 && isLowerHex(value)
}

func validRemoteInvitationStatus(status string) bool {
	switch status {
	case RemotePairingInvitationStatusOpen, RemotePairingInvitationStatusClaimed, RemotePairingInvitationStatusApproved, RemotePairingInvitationStatusRejected, RemotePairingInvitationStatusRevoked, RemotePairingInvitationStatusExpired:
		return true
	default:
		return false
	}
}

func validRemotePeerPairingStatus(status string) bool {
	return status == RemotePeerPairingStatusActive || status == RemotePeerPairingStatusRevoked || status == RemotePeerPairingStatusExpired
}

func remoteInvitationRequesterComplete(invitation RemotePairingInvitation) bool {
	return invitation.RequesterDisplayName != "" && invitation.RequesterInstallationID != "" && invitation.RequesterPublicKey != "" && invitation.RequesterFingerprint != ""
}

func normalizeRemoteInvitationListOptions(args []RemotePairingInvitationListOptions) (RemotePairingInvitationListOptions, error) {
	if len(args) > 1 {
		return RemotePairingInvitationListOptions{}, errors.New("too many remote pairing invitation list options")
	}
	var options RemotePairingInvitationListOptions
	if len(args) == 1 {
		options = args[0]
	}
	options.Status = strings.TrimSpace(options.Status)
	options.RequesterFingerprint = strings.TrimSpace(options.RequesterFingerprint)
	if options.Status != "" && !validRemoteInvitationStatus(options.Status) {
		return RemotePairingInvitationListOptions{}, errors.New("invalid remote pairing invitation status filter")
	}
	if options.RequesterFingerprint != "" && !validRemoteSHA256(options.RequesterFingerprint) {
		return RemotePairingInvitationListOptions{}, errors.New("invalid remote pairing invitation fingerprint filter")
	}
	if options.Limit < 0 || options.Limit > remoteCollaborationMaxListLimit {
		return RemotePairingInvitationListOptions{}, errors.New("invalid remote pairing invitation list limit")
	}
	return options, nil
}

func normalizeRemotePeerPairingListOptions(args []RemotePeerPairingListOptions) (RemotePeerPairingListOptions, error) {
	if len(args) > 1 {
		return RemotePeerPairingListOptions{}, errors.New("too many remote peer pairing list options")
	}
	var options RemotePeerPairingListOptions
	if len(args) == 1 {
		options = args[0]
	}
	options.LocalRole = strings.TrimSpace(options.LocalRole)
	options.Status = strings.TrimSpace(options.Status)
	if options.LocalRole != "" && options.LocalRole != RemotePeerLocalRoleHost && options.LocalRole != RemotePeerLocalRoleController {
		return RemotePeerPairingListOptions{}, errors.New("invalid remote peer pairing role filter")
	}
	if options.Status != "" && !validRemotePeerPairingStatus(options.Status) {
		return RemotePeerPairingListOptions{}, errors.New("invalid remote peer pairing status filter")
	}
	if options.Limit < 0 || options.Limit > remoteCollaborationMaxListLimit {
		return RemotePeerPairingListOptions{}, errors.New("invalid remote peer pairing list limit")
	}
	return options, nil
}

func normalizedRemoteListLimit(limit int) int {
	if limit == 0 {
		return 100
	}
	return limit
}

func requireRemoteTransition(result sql.Result, action string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%w: %s lost expected state or revision", ErrConflict, action)
	}
	return nil
}
