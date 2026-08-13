package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"autoto/internal/db"
)

const (
	authSessionCookieName = "autoto_session"
	authSessionLifetime   = 30 * 24 * time.Hour

	userPasswordHashV1 = "sha256-bcrypt-v1$"
	// This is bcrypt(SHA-256("autoto-dummy-user-password")); it keeps unknown
	// handles on the same password-verification path without granting access.
	dummyUserPasswordHash = userPasswordHashV1 + "$2a$10$bIFgXcQB.dJA6YIcyZrK5OVy1Xjgtk.uVk03o9D0wtWT37h6yAB86"

	authLoginMaxFailures    = 10
	authLoginFailureWindow  = 15 * time.Minute
	authLoginLockDuration   = 15 * time.Minute
	authLoginFailureEntries = 2048
)

type authLoginFailure struct {
	Count       int
	FirstFailed time.Time
	LockedUntil time.Time
}

type authCredentialsRequest struct {
	Handle   string `json:"handle"`
	Password string `json:"password"`
}

func cancelAuthSessionConnections(cancels []context.CancelFunc) {
	for _, cancel := range cancels {
		if cancel != nil {
			cancel()
		}
	}
}

func (s *Server) removeAuthSessionConnectionsLocked(tokenHash string) []context.CancelFunc {
	connections := s.authSessionConnections[tokenHash]
	delete(s.authSessionConnections, tokenHash)
	cancels := make([]context.CancelFunc, 0, len(connections))
	for _, cancel := range connections {
		cancels = append(cancels, cancel)
	}
	return cancels
}

func (s *Server) revokeAuthSessionToken(ctx context.Context, token string) error {
	token = strings.TrimSpace(token)
	if token == "" || s.store == nil {
		return nil
	}
	tokenHash := db.HashSessionToken(token)
	s.authSessionMu.Lock()
	if err := s.store.RevokeAuthSessionToken(ctx, token); err != nil {
		s.authSessionMu.Unlock()
		return err
	}
	cancels := s.removeAuthSessionConnectionsLocked(tokenHash)
	s.authSessionMu.Unlock()
	cancelAuthSessionConnections(cancels)
	return nil
}

// authSessionWebSocketContext binds a WebSocket to the browser login session
// that authorized its project access. Logout or expiry therefore terminates an
// established connection instead of only rejecting the next HTTP request.
func (s *Server) authSessionWebSocketContext(parent context.Context, r *http.Request) (context.Context, context.CancelFunc, bool) {
	if s.store == nil {
		ctx, cancel := context.WithCancel(parent)
		return ctx, cancel, true
	}

	token := ""
	if cookie, err := r.Cookie(authSessionCookieName); err == nil {
		token = strings.TrimSpace(cookie.Value)
	}

	// Serialize validation/registration with logout so revocation either wins
	// before this lookup or observes and cancels the newly registered socket.
	s.authSessionMu.Lock()
	hasUsers, err := s.store.HasUsers(parent)
	if err != nil {
		s.authSessionMu.Unlock()
		return nil, func() {}, false
	}
	if !hasUsers {
		s.authSessionMu.Unlock()
		ctx, cancel := context.WithCancel(parent)
		return ctx, cancel, true
	}
	if token == "" {
		s.authSessionMu.Unlock()
		return nil, func() {}, false
	}
	_, session, err := s.store.GetUserBySessionToken(parent, token, s.clock())
	if err != nil {
		s.authSessionMu.Unlock()
		return nil, func() {}, false
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, session.ExpiresAt)
	if err != nil || !expiresAt.After(s.clock()) {
		s.authSessionMu.Unlock()
		return nil, func() {}, false
	}
	ctx, cancel := context.WithDeadline(parent, expiresAt)
	if ctx.Err() != nil {
		s.authSessionMu.Unlock()
		cancel()
		return nil, func() {}, false
	}
	if s.authSessionConnections == nil {
		s.authSessionConnections = make(map[string]map[uint64]context.CancelFunc)
	}
	if s.authSessionConnections[session.TokenHash] == nil {
		s.authSessionConnections[session.TokenHash] = make(map[uint64]context.CancelFunc)
	}
	s.authSessionConnectionSeq++
	connectionID := s.authSessionConnectionSeq
	s.authSessionConnections[session.TokenHash][connectionID] = cancel
	s.authSessionMu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			cancel()
			s.authSessionMu.Lock()
			if connections := s.authSessionConnections[session.TokenHash]; connections != nil {
				delete(connections, connectionID)
				if len(connections) == 0 {
					delete(s.authSessionConnections, session.TokenHash)
				}
			}
			s.authSessionMu.Unlock()
		})
	}
	return ctx, release, true
}

func (s *Server) webSocketAuthorizationContext(parent context.Context, r *http.Request) (context.Context, context.CancelFunc, bool) {
	remoteCtx, releaseRemote, ok := s.remoteWebSocketContext(parent, r)
	if !ok {
		return nil, func() {}, false
	}
	authCtx, releaseAuth, ok := s.authSessionWebSocketContext(remoteCtx, r)
	if !ok {
		releaseRemote()
		return nil, func() {}, false
	}
	return authCtx, func() {
		releaseAuth()
		releaseRemote()
	}, true
}

func validUserPasswordLength(password string) bool {
	return len(password) >= 8 && len(password) <= 1024
}

func hashUserPassword(password string) (string, error) {
	digest := sha256.Sum256([]byte(password))
	hash, err := bcrypt.GenerateFromPassword(digest[:], bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return userPasswordHashV1 + string(hash), nil
}

func verifyUserPassword(encoded, password string) bool {
	encoded = strings.TrimSpace(encoded)
	if strings.HasPrefix(encoded, userPasswordHashV1) {
		digest := sha256.Sum256([]byte(password))
		return bcrypt.CompareHashAndPassword([]byte(strings.TrimPrefix(encoded, userPasswordHashV1)), digest[:]) == nil
	}
	// Preserve compatibility with users created before the versioned pre-hash
	// format was introduced.
	return bcrypt.CompareHashAndPassword([]byte(encoded), []byte(password)) == nil
}

func authLoginFailureKey(r *http.Request, handle string) string {
	_, canonical, err := db.CanonicalHandle(handle)
	if err != nil {
		digest := sha256.Sum256([]byte(strings.TrimSpace(handle)))
		canonical = base64.RawURLEncoding.EncodeToString(digest[:])
	}
	return remoteAccessClientKey(r) + "\x00" + canonical
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var req authCredentialsRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if !validUserPasswordLength(req.Password) {
		writeError(w, http.StatusBadRequest, "password must be between 8 and 1024 bytes")
		return
	}
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	if hasUsers && !s.configSnapshot().Auth.RegistrationOpen {
		writeError(w, http.StatusForbidden, "registration is closed")
		return
	}
	hash, err := hashUserPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash password: "+err.Error())
		return
	}
	user, err := s.store.CreateUser(r.Context(), req.Handle, string(hash))
	if err != nil {
		s.writeRequestError(w, r, statusFromError(err), err)
		return
	}
	if err := s.startSession(w, r, user); err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req authCredentialsRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if !validUserPasswordLength(req.Password) {
		writeError(w, http.StatusBadRequest, "password must be between 8 and 1024 bytes")
		return
	}
	failureKey := authLoginFailureKey(r, req.Handle)
	if locked, until := s.authLoginLocked(failureKey); locked {
		s.writeAuthLoginLocked(w, until)
		return
	}

	var user db.User
	passwordHash := dummyUserPasswordHash
	found := false
	if _, _, err := db.CanonicalHandle(req.Handle); err == nil {
		storedUser, storedHash, loadErr := s.store.GetUserByHandle(r.Context(), req.Handle)
		switch {
		case loadErr == nil:
			user = storedUser
			passwordHash = storedHash
			found = true
		case errors.Is(loadErr, sql.ErrNoRows):
			// Continue through the same password verification path so account
			// existence is not exposed by the response or a cheap fast path.
		default:
			writeError(w, http.StatusInternalServerError, "login is temporarily unavailable")
			return
		}
	}
	validPassword := verifyUserPassword(passwordHash, req.Password)
	if !found || !validPassword {
		if until := s.recordAuthLoginFailure(failureKey); !until.IsZero() {
			s.writeAuthLoginLocked(w, until)
			return
		}
		writeError(w, http.StatusUnauthorized, "invalid handle or password")
		return
	}
	s.clearAuthLoginFailures(failureKey)
	if err := s.startSession(w, r, user); err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(authSessionCookieName); err == nil && cookie.Value != "" {
		if err := s.revokeAuthSessionToken(r.Context(), cookie.Value); err != nil {
			s.writeRequestError(w, r, http.StatusInternalServerError, err)
			return
		}
	}
	s.clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	user, ok, err := s.currentUser(r)
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "login required")
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok, err := s.currentUser(r); err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	} else if !ok {
		writeError(w, http.StatusUnauthorized, "login required")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	users, err := s.store.ListUsersByHandlePrefix(r.Context(), r.URL.Query().Get("handlePrefix"), limit)
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) currentUser(r *http.Request) (db.User, bool, error) {
	if s.store == nil {
		return db.User{}, false, nil
	}
	cookie, err := r.Cookie(authSessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return db.User{}, false, nil
	}
	user, _, err := s.store.GetUserBySessionToken(r.Context(), cookie.Value, s.clock())
	if errors.Is(err, sql.ErrNoRows) {
		return db.User{}, false, nil
	}
	if err != nil {
		return db.User{}, false, err
	}
	return user, true, nil
}

func (s *Server) requireUser(w http.ResponseWriter, r *http.Request) (db.User, bool) {
	user, ok, err := s.currentUser(r)
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return db.User{}, false
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "login required")
		return db.User{}, false
	}
	return user, true
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, user db.User) error {
	token, err := newSessionToken()
	if err != nil {
		return err
	}
	now := s.clock().UTC()
	expiresAt := now.Add(authSessionLifetime)
	if _, err := s.store.CreateAuthSession(r.Context(), db.AuthSession{UserID: user.ID, TokenHash: db.HashSessionToken(token), CreatedAt: now.Format(time.RFC3339Nano), ExpiresAt: expiresAt.Format(time.RFC3339Nano)}); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: authSessionCookieName, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: requestIsHTTPS(r), Expires: expiresAt, MaxAge: int(authSessionLifetime.Seconds())})
	return nil
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: authSessionCookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: requestIsHTTPS(r), MaxAge: -1, Expires: time.Unix(1, 0)})
}

func newSessionToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func (s *Server) authLoginLocked(key string) (bool, time.Time) {
	now := s.now()
	s.authLoginMu.Lock()
	defer s.authLoginMu.Unlock()
	failure, ok := s.authLoginFailures[key]
	if !ok {
		return false, time.Time{}
	}
	if authLoginFailureExpired(failure, now) {
		delete(s.authLoginFailures, key)
		return false, time.Time{}
	}
	if !failure.LockedUntil.IsZero() {
		return true, failure.LockedUntil
	}
	return false, time.Time{}
}

func (s *Server) recordAuthLoginFailure(key string) time.Time {
	now := s.now()
	s.authLoginMu.Lock()
	defer s.authLoginMu.Unlock()
	if s.authLoginFailures == nil {
		s.authLoginFailures = make(map[string]authLoginFailure)
	}
	for existingKey, failure := range s.authLoginFailures {
		if authLoginFailureExpired(failure, now) {
			delete(s.authLoginFailures, existingKey)
		}
	}
	failure := s.authLoginFailures[key]
	if failure.FirstFailed.IsZero() || now.Sub(failure.FirstFailed) > authLoginFailureWindow {
		failure = authLoginFailure{FirstFailed: now}
	}
	failure.Count++
	if failure.Count >= authLoginMaxFailures {
		failure.LockedUntil = now.Add(authLoginLockDuration)
	}
	s.authLoginFailures[key] = failure
	for len(s.authLoginFailures) > authLoginFailureEntries {
		oldestKey := ""
		oldest := time.Time{}
		for candidateKey, candidate := range s.authLoginFailures {
			if oldestKey == "" || candidate.FirstFailed.Before(oldest) {
				oldestKey = candidateKey
				oldest = candidate.FirstFailed
			}
		}
		if oldestKey == "" {
			break
		}
		delete(s.authLoginFailures, oldestKey)
	}
	return failure.LockedUntil
}

func (s *Server) clearAuthLoginFailures(key string) {
	s.authLoginMu.Lock()
	defer s.authLoginMu.Unlock()
	delete(s.authLoginFailures, key)
}

func (s *Server) writeAuthLoginLocked(w http.ResponseWriter, until time.Time) {
	remaining := until.Sub(s.now())
	seconds := int64((remaining + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	writeError(w, http.StatusTooManyRequests, "too many login attempts; try again later")
}

func authLoginFailureExpired(failure authLoginFailure, now time.Time) bool {
	if !failure.LockedUntil.IsZero() {
		return !now.Before(failure.LockedUntil)
	}
	if failure.FirstFailed.IsZero() {
		return true
	}
	return now.Sub(failure.FirstFailed) > authLoginFailureWindow
}
