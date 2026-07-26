package peercontrol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"autoto/internal/db"
)

type peerManagerTestStore struct {
	mu       sync.Mutex
	pairings map[string]db.RemotePeerPairing
	reads    int
}

func (s *peerManagerTestStore) GetRemotePeerPairing(_ context.Context, id string) (db.RemotePeerPairing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	pairing, ok := s.pairings[id]
	if !ok {
		return db.RemotePeerPairing{}, sql.ErrNoRows
	}
	pairing.Scopes = append([]string(nil), pairing.Scopes...)
	return pairing, nil
}

func (s *peerManagerTestStore) update(id string, update func(*db.RemotePeerPairing)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pairing := s.pairings[id]
	update(&pairing)
	s.pairings[id] = pairing
}

type peerManagerTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *peerManagerTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *peerManagerTestClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func TestManagerDefaultsDisabledAndDoesNotPersistSharing(t *testing.T) {
	controller := testIdentity(t)
	store := newPeerManagerTestStore(controller, "pair-1")
	home := t.TempDir()
	clock := &peerManagerTestClock{now: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)}
	manager := newPeerManagerForTest(t, home, store, clock)
	if manager.SharingEnabled() {
		t.Fatal("new manager unexpectedly enabled sharing")
	}
	if _, err := manager.IssueSessionChallenge(context.Background(), "pair-1"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled challenge error = %v", err)
	}
	if err := manager.SetSharingEnabled(true); err != nil {
		t.Fatal(err)
	}
	if !manager.SharingEnabled() {
		t.Fatal("sharing did not enable")
	}
	identityPath := filepath.Join(home, "secrets", "peer-identity.json")
	if _, err := os.Stat(identityPath); err != nil {
		t.Fatalf("identity path was not created: %v", err)
	}

	restarted := newPeerManagerForTest(t, home, store, clock)
	if restarted.SharingEnabled() {
		t.Fatal("sharing state survived manager restart")
	}
	if restarted.Identity() != manager.Identity() {
		t.Fatal("durable identity changed across manager restart")
	}
}

func TestManagerSupportsRequestBoundDynamicOrigins(t *testing.T) {
	controller := testIdentity(t)
	store := newPeerManagerTestStore(controller, "pair-1")
	clock := &peerManagerTestClock{now: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)}
	manager, err := NewManager(ManagerOptions{HomeDir: t.TempDir(), Store: store, Clock: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	enableManagerForTest(t, manager)
	if _, err := manager.IssueSessionChallenge(context.Background(), "pair-1"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("missing default origin error = %v", err)
	}
	challenge, err := manager.IssueSessionChallengeForOrigin(context.Background(), "pair-1", "https://dynamic.example")
	if err != nil {
		t.Fatal(err)
	}
	if challenge.EndpointOrigin != "https://dynamic.example" {
		t.Fatalf("challenge origin = %q", challenge.EndpointOrigin)
	}
	signed, err := SignChallenge(controller, challenge.PairingID, challenge.Challenge, challenge.EndpointOrigin, challenge.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.EstablishSession(context.Background(), "pair-1", signed); err != nil {
		t.Fatal(err)
	}
}

func TestManagerEstablishAuthenticateReplayAndHashedStorage(t *testing.T) {
	controller := testIdentity(t)
	store := newPeerManagerTestStore(controller, "pair-1")
	clock := &peerManagerTestClock{now: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)}
	manager := newPeerManagerForTest(t, t.TempDir(), store, clock)
	enableManagerForTest(t, manager)

	challenge, signed := issueAndSignManagerChallenge(t, manager, controller, "pair-1")
	credential, err := manager.EstablishSession(context.Background(), "pair-1", signed)
	if err != nil {
		t.Fatal(err)
	}
	if credential.BearerToken == "" || !clock.Now().Before(credential.ExpiresAt) {
		t.Fatalf("invalid session credential: %+v", credential)
	}
	if _, err := manager.EstablishSession(context.Background(), "pair-1", signed); !errors.Is(err, ErrConflict) {
		t.Fatalf("challenge replay error = %v", err)
	}
	authenticated, err := manager.Authenticate(context.Background(), credential.BearerToken)
	if err != nil {
		t.Fatal(err)
	}
	if authenticated.PairingID != challenge.PairingID || authenticated.CredentialRevision != 1 || authenticated.GrantRevision != 1 {
		t.Fatalf("unexpected authenticated session: %+v", authenticated)
	}

	expectedHash := sha256.Sum256([]byte(credential.BearerToken))
	manager.mu.Lock()
	if len(manager.sessions) != 1 {
		manager.mu.Unlock()
		t.Fatalf("stored sessions = %d", len(manager.sessions))
	}
	for _, session := range manager.sessions {
		if session.tokenHash != expectedHash {
			manager.mu.Unlock()
			t.Fatal("manager did not store the bearer SHA-256")
		}
		if bytes.Contains(session.tokenHash[:], []byte(credential.BearerToken)) {
			manager.mu.Unlock()
			t.Fatal("stored token hash contained plaintext bearer")
		}
	}
	formatted := fmt.Sprintf("%#v", manager.sessions)
	manager.mu.Unlock()
	if strings.Contains(formatted, credential.BearerToken) {
		t.Fatal("manager session formatting exposed plaintext bearer")
	}
	if _, err := manager.Authenticate(context.Background(), "not-a-token"); !errors.Is(err, ErrUnauthorized) || strings.Contains(err.Error(), credential.BearerToken) {
		t.Fatalf("invalid bearer error = %v", err)
	}
}

func TestManagerRevisionRevocationAndExpirationInvalidateSessions(t *testing.T) {
	controller := testIdentity(t)
	store := newPeerManagerTestStore(controller, "pair-1")
	clock := &peerManagerTestClock{now: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)}
	manager := newPeerManagerForTest(t, t.TempDir(), store, clock)
	enableManagerForTest(t, manager)

	grantCredential := establishManagerSession(t, manager, controller, "pair-1")
	store.update("pair-1", func(pairing *db.RemotePeerPairing) { pairing.GrantRevision++ })
	if _, err := manager.Authenticate(context.Background(), grantCredential.BearerToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("grant revision mismatch error = %v", err)
	}
	if _, err := manager.Authenticate(context.Background(), grantCredential.BearerToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("invalidated grant session error = %v", err)
	}

	credentialCredential := establishManagerSession(t, manager, controller, "pair-1")
	store.update("pair-1", func(pairing *db.RemotePeerPairing) { pairing.CredentialRevision++ })
	if _, err := manager.Authenticate(context.Background(), credentialCredential.BearerToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("credential revision mismatch error = %v", err)
	}

	revokedCredential := establishManagerSession(t, manager, controller, "pair-1")
	store.update("pair-1", func(pairing *db.RemotePeerPairing) { pairing.Status = db.RemotePeerPairingStatusRevoked })
	if _, err := manager.Authenticate(context.Background(), revokedCredential.BearerToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked pairing error = %v", err)
	}

	store.update("pair-1", func(pairing *db.RemotePeerPairing) {
		pairing.Status = db.RemotePeerPairingStatusActive
		pairing.ExpiresAt = clock.Now().Add(time.Minute).Format(time.RFC3339Nano)
	})
	expiringCredential := establishManagerSession(t, manager, controller, "pair-1")
	clock.Advance(2 * time.Minute)
	if _, err := manager.Authenticate(context.Background(), expiringCredential.BearerToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired pairing error = %v", err)
	}
	if store.reads < 8 {
		t.Fatalf("pairing store reads = %d, expected authoritative rereads", store.reads)
	}
}

func TestManagerStopSharingInvalidateAndCloseCancelConnections(t *testing.T) {
	controller := testIdentity(t)
	store := newPeerManagerTestStore(controller, "pair-1")
	clock := &peerManagerTestClock{now: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)}
	manager := newPeerManagerForTest(t, t.TempDir(), store, clock)
	enableManagerForTest(t, manager)
	credential := establishManagerSession(t, manager, controller, "pair-1")

	cancelled := make(chan struct{})
	unregister, err := manager.RegisterConnection("pair-1", func() { close(cancelled) })
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetSharingEnabled(false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("stop-sharing did not cancel registered connection")
	}
	unregister()
	unregister()
	if _, err := manager.Authenticate(context.Background(), credential.BearerToken); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled authenticate error = %v", err)
	}

	if err := manager.SetSharingEnabled(true); err != nil {
		t.Fatal(err)
	}
	invalidated := make(chan struct{})
	if _, err := manager.RegisterConnection("pair-1", func() { close(invalidated) }); err != nil {
		t.Fatal(err)
	}
	if err := manager.InvalidatePairing("pair-1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-invalidated:
	default:
		t.Fatal("pairing invalidation did not cancel connection")
	}

	closed := make(chan struct{})
	if _, err := manager.RegisterConnection("pair-1", func() { close(closed) }); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	default:
		t.Fatal("close did not cancel connection")
	}
	if manager.SharingEnabled() {
		t.Fatal("closed manager still reports sharing enabled")
	}
	if err := manager.SetSharingEnabled(true); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed manager enable error = %v", err)
	}
}

func TestManagerBoundsAndExpiredChallengeCleanup(t *testing.T) {
	first := testIdentity(t)
	second := testIdentity(t)
	store := newPeerManagerTestStore(first, "pair-1")
	store.pairings["pair-2"] = managerTestPairing(second, "pair-2")
	clock := &peerManagerTestClock{now: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)}
	manager, err := NewManager(ManagerOptions{
		HomeDir:        t.TempDir(),
		Store:          store,
		EndpointOrigin: "https://host.example",
		Clock:          clock.Now,
		ChallengeTTL:   5 * time.Second,
		SessionTTL:     time.Minute,
		MaxChallenges:  1,
		MaxSessions:    1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetSharingEnabled(true); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.IssueSessionChallenge(context.Background(), "pair-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.IssueSessionChallenge(context.Background(), "pair-2"); !errors.Is(err, ErrConflict) {
		t.Fatalf("challenge capacity error = %v", err)
	}
	clock.Advance(6 * time.Second)
	challenge, err := manager.IssueSessionChallenge(context.Background(), "pair-2")
	if err != nil {
		t.Fatalf("expired challenge was not cleaned: %v", err)
	}
	signed, err := SignChallenge(second, challenge.PairingID, challenge.Challenge, challenge.EndpointOrigin, challenge.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.EstablishSession(context.Background(), "pair-2", signed); err != nil {
		t.Fatal(err)
	}
	_, signed = issueAndSignManagerChallenge(t, manager, first, "pair-1")
	if _, err := manager.EstablishSession(context.Background(), "pair-1", signed); !errors.Is(err, ErrConflict) {
		t.Fatalf("session capacity error = %v", err)
	}
}

func newPeerManagerTestStore(identity *Identity, pairingID string) *peerManagerTestStore {
	return &peerManagerTestStore{pairings: map[string]db.RemotePeerPairing{
		pairingID: managerTestPairing(identity, pairingID),
	}}
}

func managerTestPairing(identity *Identity, pairingID string) db.RemotePeerPairing {
	public := identity.Public()
	return db.RemotePeerPairing{
		ID:                 pairingID,
		LocalRole:          db.RemotePeerLocalRoleHost,
		DisplayName:        "Controller",
		PeerInstallationID: "controller-installation",
		PeerPublicKey:      public.PublicKey,
		PeerFingerprint:    public.Fingerprint,
		Status:             db.RemotePeerPairingStatusActive,
		Scopes:             []string{db.RemotePeerScopeObserve, db.RemotePeerScopeSendTask},
		CredentialRevision: 1,
		GrantRevision:      1,
	}
}

func newPeerManagerForTest(t *testing.T, home string, store RemotePairingStore, clock *peerManagerTestClock) *Manager {
	t.Helper()
	manager, err := NewManager(ManagerOptions{
		HomeDir:        home,
		Store:          store,
		EndpointOrigin: "https://host.example",
		Clock:          clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return manager
}

func enableManagerForTest(t *testing.T, manager *Manager) {
	t.Helper()
	if err := manager.SetSharingEnabled(true); err != nil {
		t.Fatal(err)
	}
}

func issueAndSignManagerChallenge(t *testing.T, manager *Manager, identity *Identity, pairingID string) (SessionChallenge, SignedChallenge) {
	t.Helper()
	challenge, err := manager.IssueSessionChallenge(context.Background(), pairingID)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := SignChallenge(identity, challenge.PairingID, challenge.Challenge, challenge.EndpointOrigin, challenge.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	return challenge, signed
}

func establishManagerSession(t *testing.T, manager *Manager, identity *Identity, pairingID string) SessionCredential {
	t.Helper()
	_, signed := issueAndSignManagerChallenge(t, manager, identity, pairingID)
	credential, err := manager.EstablishSession(context.Background(), pairingID, signed)
	if err != nil {
		t.Fatal(err)
	}
	return credential
}
