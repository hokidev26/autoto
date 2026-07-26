package peercontrol

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"sync"
	"time"

	"autoto/internal/db"
	"autoto/internal/runtime"
)

const (
	defaultSessionChallengeTTL = time.Minute
	defaultSessionTTL          = 15 * time.Minute
	defaultMaxChallenges       = 256
	defaultMaxSessions         = 512
	maxManagerEntries          = 10000
)

var (
	// ErrDisabled means peer sharing is not currently enabled.
	ErrDisabled = errors.New("peer sharing is disabled")
	// ErrUnauthorized means a peer credential or pairing is not authorized.
	ErrUnauthorized = errors.New("peer session is unauthorized")
	// ErrConflict means a one-time value was consumed, replayed, or capacity was reached.
	ErrConflict = errors.New("peer session conflict")
	// ErrClosed means the manager or client has been closed.
	ErrClosed = errors.New("peer control is closed")
)

// RemotePairingStore is the authoritative pairing lookup used on every
// authentication. *db.Store implements this interface.
type RemotePairingStore interface {
	GetRemotePeerPairing(context.Context, string) (db.RemotePeerPairing, error)
}

// ManagerOptions controls bounded in-memory peer session state.
type ManagerOptions struct {
	HomeDir        string
	Store          RemotePairingStore
	EndpointOrigin string
	Clock          func() time.Time
	ChallengeTTL   time.Duration
	SessionTTL     time.Duration
	MaxChallenges  int
	MaxSessions    int
}

// SessionChallenge is a short-lived, one-time challenge issued by the host.
type SessionChallenge struct {
	ProtocolVersion int       `json:"protocolVersion"`
	PairingID       string    `json:"pairingId"`
	Challenge       string    `json:"challenge"`
	ExpiresAt       time.Time `json:"expiresAt"`
	EndpointOrigin  string    `json:"endpointOrigin"`
	HostPublicKey   string    `json:"hostPublicKey"`
	HostFingerprint string    `json:"hostFingerprint"`
	HostSignature   string    `json:"hostSignature"`
}

// SessionCredential is returned exactly once when a session is established.
// Manager stores only SHA-256 of BearerToken.
type SessionCredential struct {
	BearerToken string    `json:"bearerToken"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

// AuthenticatedSession is the revalidated authorization state for a bearer.
type AuthenticatedSession struct {
	PairingID          string    `json:"pairingId"`
	CredentialRevision int64     `json:"credentialRevision"`
	GrantRevision      int64     `json:"grantRevision"`
	ExpiresAt          time.Time `json:"expiresAt"`
	Scopes             []Scope   `json:"scopes"`
}

type pendingSessionChallenge struct {
	value SessionChallenge
}

type storedSession struct {
	id                 uint64
	tokenHash          [sha256.Size]byte
	pairingID          string
	credentialRevision int64
	grantRevision      int64
	expiresAt          time.Time
}

type registeredConnection struct {
	id        uint64
	pairingID string
	cancel    func()
}

// Manager owns the host identity and all process-local peer sharing state.
var _ runtime.Service = (*Manager)(nil)

type Manager struct {
	mu sync.Mutex

	store          RemotePairingStore
	identity       *Identity
	endpointOrigin string
	clock          func() time.Time
	challengeTTL   time.Duration
	sessionTTL     time.Duration
	maxChallenges  int
	maxSessions    int

	started        bool
	closed         bool
	sharingEnabled bool
	nextID         uint64
	challenges     map[string]pendingSessionChallenge
	sessions       map[uint64]storedSession
	connections    map[uint64]registeredConnection
	clients        map[*Client]struct{}
}

// NewManager loads or creates <home>/secrets/peer-identity.json and initializes
// sharing as disabled. Sharing state is deliberately never persisted.
func NewManager(options ManagerOptions) (*Manager, error) {
	if options.Store == nil {
		return nil, errors.New("peer manager store is required")
	}
	origin := ""
	var err error
	if options.EndpointOrigin != "" {
		origin, err = normalizeOrigin(options.EndpointOrigin)
		if err != nil {
			return nil, err
		}
	}
	identityStore, err := NewIdentityStore(options.HomeDir)
	if err != nil {
		return nil, err
	}
	identity, err := identityStore.LoadOrCreate()
	if err != nil {
		return nil, err
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	challengeTTL := options.ChallengeTTL
	if challengeTTL == 0 {
		challengeTTL = defaultSessionChallengeTTL
	}
	if challengeTTL < 5*time.Second || challengeTTL > 5*time.Minute {
		return nil, errors.New("peer session challenge TTL is out of bounds")
	}
	sessionTTL := options.SessionTTL
	if sessionTTL == 0 {
		sessionTTL = defaultSessionTTL
	}
	if sessionTTL < time.Minute || sessionTTL > 24*time.Hour {
		return nil, errors.New("peer session TTL is out of bounds")
	}
	maxChallenges := options.MaxChallenges
	if maxChallenges == 0 {
		maxChallenges = defaultMaxChallenges
	}
	maxSessions := options.MaxSessions
	if maxSessions == 0 {
		maxSessions = defaultMaxSessions
	}
	if maxChallenges < 1 || maxChallenges > maxManagerEntries || maxSessions < 1 || maxSessions > maxManagerEntries {
		return nil, errors.New("peer manager capacity is out of bounds")
	}
	return &Manager{
		store:          options.Store,
		identity:       identity,
		endpointOrigin: origin,
		clock:          clock,
		challengeTTL:   challengeTTL,
		sessionTTL:     sessionTTL,
		maxChallenges:  maxChallenges,
		maxSessions:    maxSessions,
		challenges:     make(map[string]pendingSessionChallenge),
		sessions:       make(map[uint64]storedSession),
		connections:    make(map[uint64]registeredConnection),
		clients:        make(map[*Client]struct{}),
	}, nil
}

// Identity returns the manager's public identity.
func (m *Manager) Identity() PublicIdentity {
	if m == nil || m.identity == nil {
		return PublicIdentity{}
	}
	return m.identity.Public()
}

// Start implements runtime.Service. It never enables peer sharing.
func (m *Manager) Start(ctx context.Context) error {
	if m == nil {
		return ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	if m.started {
		return fmt.Errorf("%w: peer manager already started", ErrConflict)
	}
	m.started = true
	m.sharingEnabled = false
	return nil
}

// Close implements runtime.Service. It immediately disables sharing, clears
// all challenges and sessions, and cancels every registered connection.
func (m *Manager) Close(context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.started = false
	m.sharingEnabled = false
	m.challenges = make(map[string]pendingSessionChallenge)
	m.sessions = make(map[uint64]storedSession)
	cancels := m.detachConnectionsLocked("")
	clients := make([]*Client, 0, len(m.clients))
	for client := range m.clients {
		clients = append(clients, client)
	}
	m.clients = make(map[*Client]struct{})
	m.mu.Unlock()
	cancelConnections(cancels)
	for _, client := range clients {
		_ = client.Close()
	}
	return nil
}

// SharingEnabled reports the process-local sharing switch.
func (m *Manager) SharingEnabled() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.closed && m.sharingEnabled
}

// SetSharingEnabled changes the process-local sharing switch. Disabling clears
// credentials and cancels all registered connections before returning.
func (m *Manager) SetSharingEnabled(enabled bool) error {
	if m == nil {
		return ErrClosed
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	m.cleanupExpiredLocked(m.now())
	m.sharingEnabled = enabled
	var cancels []func()
	if !enabled {
		m.challenges = make(map[string]pendingSessionChallenge)
		m.sessions = make(map[uint64]storedSession)
		cancels = m.detachConnectionsLocked("")
	}
	m.mu.Unlock()
	cancelConnections(cancels)
	return nil
}

// IssueSessionChallenge uses the manager's configured default origin. Servers
// with dynamic tunnel origins should call IssueSessionChallengeForOrigin.
func (m *Manager) IssueSessionChallenge(ctx context.Context, pairingID string) (SessionChallenge, error) {
	if m == nil {
		return SessionChallenge{}, ErrClosed
	}
	return m.IssueSessionChallengeForOrigin(ctx, pairingID, m.endpointOrigin)
}

// IssueSessionChallengeForOrigin verifies an active host-side pairing in the
// database, then creates a bounded, short-lived, one-time challenge bound to
// the exact HTTPS origin used by this request.
func (m *Manager) IssueSessionChallengeForOrigin(ctx context.Context, pairingID, endpointOrigin string) (SessionChallenge, error) {
	if err := validateIdentifier(pairingID); err != nil {
		return SessionChallenge{}, fmt.Errorf("%w: invalid pairing", ErrUnauthorized)
	}
	origin, err := normalizeOrigin(endpointOrigin)
	if err != nil {
		return SessionChallenge{}, fmt.Errorf("%w: invalid endpoint origin", ErrUnauthorized)
	}
	if err := m.requireSharing(); err != nil {
		return SessionChallenge{}, err
	}
	pairing, _, err := m.loadActiveHostPairing(ctx, pairingID)
	if err != nil {
		return SessionChallenge{}, err
	}
	challenge, err := GenerateChallenge()
	if err != nil {
		return SessionChallenge{}, err
	}
	now := m.now()
	issued := SessionChallenge{
		ProtocolVersion: ProtocolVersion,
		PairingID:       pairing.ID,
		Challenge:       challenge,
		ExpiresAt:       now.Add(m.challengeTTL).UTC(),
		EndpointOrigin:  origin,
	}
	issued, err = SignHostSessionChallenge(m.identity, issued)
	if err != nil {
		return SessionChallenge{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return SessionChallenge{}, ErrClosed
	}
	if !m.sharingEnabled {
		return SessionChallenge{}, ErrDisabled
	}
	m.cleanupExpiredLocked(now)
	for token, pending := range m.challenges {
		if pending.value.PairingID == pairingID {
			delete(m.challenges, token)
		}
	}
	if len(m.challenges) >= m.maxChallenges {
		return SessionChallenge{}, fmt.Errorf("%w: challenge capacity reached", ErrConflict)
	}
	m.challenges[challenge] = pendingSessionChallenge{value: issued}
	return issued, nil
}

// EstablishSession atomically consumes the referenced challenge before any
// signature verification, preventing both successful and failed replay.
func (m *Manager) EstablishSession(ctx context.Context, pairingID string, signed SignedChallenge) (SessionCredential, error) {
	if m == nil {
		return SessionCredential{}, ErrClosed
	}
	now := m.now()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return SessionCredential{}, ErrClosed
	}
	if !m.sharingEnabled {
		m.mu.Unlock()
		return SessionCredential{}, ErrDisabled
	}
	m.cleanupExpiredLocked(now)
	pending, exists := m.challenges[signed.Challenge]
	if exists {
		delete(m.challenges, signed.Challenge)
	}
	m.mu.Unlock()
	if !exists {
		return SessionCredential{}, fmt.Errorf("%w: challenge unavailable", ErrConflict)
	}
	challenge := pending.value
	if pairingID != challenge.PairingID || signed.ProtocolVersion != challenge.ProtocolVersion || signed.PairingID != challenge.PairingID || signed.Challenge != challenge.Challenge || !signed.ExpiresAt.Equal(challenge.ExpiresAt) || signed.EndpointOrigin != challenge.EndpointOrigin || !now.Before(challenge.ExpiresAt) {
		return SessionCredential{}, fmt.Errorf("%w: challenge response does not match", ErrUnauthorized)
	}
	pairing, pairingExpiry, err := m.loadActiveHostPairing(ctx, pairingID)
	if err != nil {
		return SessionCredential{}, err
	}
	if !constantStringEqual(signed.SignerPublicKey, pairing.PeerPublicKey) || !constantStringEqual(signed.SignerFingerprint, pairing.PeerFingerprint) {
		return SessionCredential{}, fmt.Errorf("%w: signer identity does not match pairing", ErrUnauthorized)
	}
	if err := VerifyChallenge(signed, pairing.PeerFingerprint, challenge.EndpointOrigin, now); err != nil {
		return SessionCredential{}, fmt.Errorf("%w: invalid signed challenge", ErrUnauthorized)
	}
	token, err := GenerateOpaqueToken()
	if err != nil {
		return SessionCredential{}, err
	}
	tokenHash := sha256.Sum256([]byte(token))
	expiresAt := now.Add(m.sessionTTL).UTC()
	if !pairingExpiry.IsZero() && pairingExpiry.Before(expiresAt) {
		expiresAt = pairingExpiry
	}
	if !now.Before(expiresAt) {
		return SessionCredential{}, fmt.Errorf("%w: pairing expired", ErrUnauthorized)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return SessionCredential{}, ErrClosed
	}
	if !m.sharingEnabled {
		return SessionCredential{}, ErrDisabled
	}
	m.cleanupExpiredLocked(now)
	if len(m.sessions) >= m.maxSessions {
		return SessionCredential{}, fmt.Errorf("%w: session capacity reached", ErrConflict)
	}
	m.nextID++
	m.sessions[m.nextID] = storedSession{
		id:                 m.nextID,
		tokenHash:          tokenHash,
		pairingID:          pairing.ID,
		credentialRevision: pairing.CredentialRevision,
		grantRevision:      pairing.GrantRevision,
		expiresAt:          expiresAt,
	}
	return SessionCredential{BearerToken: token, ExpiresAt: expiresAt}, nil
}

// Authenticate hashes the bearer and re-reads the authoritative database on
// every call. Any pairing or revision mismatch invalidates all state for that
// pairing and cancels its registered connections.
func (m *Manager) Authenticate(ctx context.Context, bearerToken string) (AuthenticatedSession, error) {
	if m == nil {
		return AuthenticatedSession{}, ErrClosed
	}
	if validateRandomToken(bearerToken) != nil {
		return AuthenticatedSession{}, ErrUnauthorized
	}
	tokenHash := sha256.Sum256([]byte(bearerToken))
	now := m.now()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return AuthenticatedSession{}, ErrClosed
	}
	if !m.sharingEnabled {
		m.mu.Unlock()
		return AuthenticatedSession{}, ErrDisabled
	}
	m.cleanupExpiredLocked(now)
	session, exists := m.findSessionLocked(tokenHash)
	m.mu.Unlock()
	if !exists {
		return AuthenticatedSession{}, ErrUnauthorized
	}

	pairing, _, err := m.loadActiveHostPairing(ctx, session.pairingID)
	if err != nil || pairing.CredentialRevision != session.credentialRevision || pairing.GrantRevision != session.grantRevision {
		m.invalidatePairingState(session.pairingID)
		if err != nil && ctx != nil && ctx.Err() != nil {
			return AuthenticatedSession{}, ctx.Err()
		}
		return AuthenticatedSession{}, ErrUnauthorized
	}
	scopes, err := NormalizeScopes(pairing.Scopes)
	if err != nil {
		m.invalidatePairingState(session.pairingID)
		return AuthenticatedSession{}, ErrUnauthorized
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return AuthenticatedSession{}, ErrClosed
	}
	if !m.sharingEnabled {
		m.mu.Unlock()
		return AuthenticatedSession{}, ErrDisabled
	}
	current, stillExists := m.sessions[session.id]
	if !stillExists || subtle.ConstantTimeCompare(current.tokenHash[:], tokenHash[:]) != 1 {
		m.mu.Unlock()
		return AuthenticatedSession{}, ErrUnauthorized
	}
	m.mu.Unlock()
	return AuthenticatedSession{
		PairingID:          session.pairingID,
		CredentialRevision: session.credentialRevision,
		GrantRevision:      session.grantRevision,
		ExpiresAt:          session.expiresAt,
		Scopes:             append([]Scope(nil), scopes...),
	}, nil
}

// InvalidatePairing removes all challenge/session state for pairingID and
// cancels all of its registered connections.
func (m *Manager) InvalidatePairing(pairingID string) error {
	if err := validateIdentifier(pairingID); err != nil {
		return fmt.Errorf("%w: invalid pairing", ErrUnauthorized)
	}
	if m == nil {
		return ErrClosed
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	cancels := m.invalidatePairingLocked(pairingID)
	m.mu.Unlock()
	cancelConnections(cancels)
	return nil
}

// RegisterConnection registers a cancellation callback for a live peer
// connection and returns an idempotent unregister function.
func (m *Manager) RegisterConnection(pairingID string, cancel func()) (func(), error) {
	if err := validateIdentifier(pairingID); err != nil || cancel == nil {
		return nil, fmt.Errorf("%w: invalid connection registration", ErrUnauthorized)
	}
	if m == nil {
		return nil, ErrClosed
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	if !m.sharingEnabled {
		m.mu.Unlock()
		return nil, ErrDisabled
	}
	m.nextID++
	id := m.nextID
	m.connections[id] = registeredConnection{id: id, pairingID: pairingID, cancel: cancel}
	m.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			delete(m.connections, id)
			m.mu.Unlock()
		})
	}, nil
}

// NewClient constructs a fixed-origin controller client without exposing the
// manager's private identity to callers.
func (m *Manager) NewClient(options ClientOptions) (*Client, error) {
	if m == nil || m.identity == nil {
		return nil, ErrClosed
	}
	if options.Identity != nil && options.Identity != m.identity {
		return nil, errors.New("peer client identity is managed internally")
	}
	options.Identity = m.identity
	client, err := NewClient(options)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = client.Close()
		return nil, ErrClosed
	}
	client.onClose = m.removeClient
	m.clients[client] = struct{}{}
	m.mu.Unlock()
	return client, nil
}

func (m *Manager) removeClient(client *Client) {
	if m == nil || client == nil {
		return
	}
	m.mu.Lock()
	delete(m.clients, client)
	m.mu.Unlock()
}

func (m *Manager) requireSharing() error {
	if m == nil {
		return ErrClosed
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	if !m.sharingEnabled {
		return ErrDisabled
	}
	return nil
}

func (m *Manager) loadActiveHostPairing(ctx context.Context, pairingID string) (db.RemotePeerPairing, time.Time, error) {
	if m == nil || m.store == nil {
		return db.RemotePeerPairing{}, time.Time{}, ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	pairing, err := m.store.GetRemotePeerPairing(ctx, pairingID)
	if err != nil {
		if ctx.Err() != nil {
			return db.RemotePeerPairing{}, time.Time{}, ctx.Err()
		}
		return db.RemotePeerPairing{}, time.Time{}, ErrUnauthorized
	}
	if pairing.ID != pairingID || pairing.LocalRole != db.RemotePeerLocalRoleHost || pairing.Status != db.RemotePeerPairingStatusActive || pairing.CredentialRevision < 1 || pairing.GrantRevision < 1 {
		return db.RemotePeerPairing{}, time.Time{}, ErrUnauthorized
	}
	publicKey, err := decodePublicKey(pairing.PeerPublicKey)
	if err != nil || !validFingerprint(pairing.PeerFingerprint) {
		return db.RemotePeerPairing{}, time.Time{}, ErrUnauthorized
	}
	fingerprint, _ := FingerprintPublicKey(publicKey)
	if !constantStringEqual(fingerprint, pairing.PeerFingerprint) {
		return db.RemotePeerPairing{}, time.Time{}, ErrUnauthorized
	}
	var expiresAt time.Time
	if pairing.ExpiresAt != "" {
		expiresAt, err = time.Parse(time.RFC3339Nano, pairing.ExpiresAt)
		if err != nil || !m.now().Before(expiresAt) {
			return db.RemotePeerPairing{}, time.Time{}, ErrUnauthorized
		}
		expiresAt = expiresAt.UTC()
	}
	return pairing, expiresAt, nil
}

func (m *Manager) now() time.Time {
	if m == nil || m.clock == nil {
		return time.Now().UTC()
	}
	return m.clock().UTC()
}

func (m *Manager) cleanupExpiredLocked(now time.Time) {
	for token, pending := range m.challenges {
		if !now.Before(pending.value.ExpiresAt) {
			delete(m.challenges, token)
		}
	}
	for id, session := range m.sessions {
		if !now.Before(session.expiresAt) {
			delete(m.sessions, id)
		}
	}
}

func (m *Manager) findSessionLocked(tokenHash [sha256.Size]byte) (storedSession, bool) {
	var match storedSession
	found := 0
	for _, session := range m.sessions {
		equal := subtle.ConstantTimeCompare(session.tokenHash[:], tokenHash[:])
		if equal == 1 {
			match = session
		}
		found |= equal
	}
	return match, found == 1
}

func (m *Manager) invalidatePairingState(pairingID string) {
	m.mu.Lock()
	cancels := m.invalidatePairingLocked(pairingID)
	m.mu.Unlock()
	cancelConnections(cancels)
}

func (m *Manager) invalidatePairingLocked(pairingID string) []func() {
	for token, pending := range m.challenges {
		if pending.value.PairingID == pairingID {
			delete(m.challenges, token)
		}
	}
	for id, session := range m.sessions {
		if session.pairingID == pairingID {
			delete(m.sessions, id)
		}
	}
	return m.detachConnectionsLocked(pairingID)
}

func (m *Manager) detachConnectionsLocked(pairingID string) []func() {
	cancels := make([]func(), 0)
	for id, connection := range m.connections {
		if pairingID == "" || connection.pairingID == pairingID {
			delete(m.connections, id)
			cancels = append(cancels, connection.cancel)
		}
	}
	return cancels
}

func cancelConnections(cancels []func()) {
	for _, cancel := range cancels {
		if cancel != nil {
			cancel()
		}
	}
}
