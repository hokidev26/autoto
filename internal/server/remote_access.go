package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"autoto/internal/config"
	"autoto/internal/db"
)

const (
	remoteAccessModeRestricted  = "restricted"
	remoteAccessModeFull        = "full"
	remoteAccessSessionTTL      = 24 * time.Hour
	remoteAccessSessionMaxCount = 2048
)

type remoteAccessSession struct {
	TokenHash          string
	Mode               string
	ExpiresAt          time.Time
	CredentialRevision int64
}

type remoteAccessAuth struct {
	Remote        bool
	Authenticated bool
	Mode          string
	ExpiresAt     time.Time
	Session       bool
}

type remoteCapabilities struct {
	MaxPermissionMode    string `json:"maxPermissionMode"`
	TerminalAllowed      bool   `json:"terminalAllowed"`
	FilesystemScope      string `json:"filesystemScope"`
	NativePickerAllowed  bool   `json:"nativePickerAllowed"`
	SecurityAdminAllowed bool   `json:"securityAdminAllowed"`
}

func normalizedCredentialRevision(cfg config.Config) int64 {
	if cfg.Security.CredentialRevision < 1 {
		return 1
	}
	return cfg.Security.CredentialRevision
}

func remoteSessionTokenHash(token string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(digest[:])
}

func newRemoteAccessSessionToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

const remoteAccessCredentialConnectionKey = "credential"

func remoteAccessSessionConnectionKey(hash string) string {
	return "session:" + hash
}

func cancelRemoteAccessConnections(cancels []context.CancelFunc) {
	for _, cancel := range cancels {
		if cancel != nil {
			cancel()
		}
	}
}

func (s *Server) removeRemoteAccessSessionLocked(hash string) []context.CancelFunc {
	delete(s.remoteAccessSessions, hash)
	key := remoteAccessSessionConnectionKey(hash)
	connections := s.remoteAccessConnections[key]
	delete(s.remoteAccessConnections, key)
	cancels := make([]context.CancelFunc, 0, len(connections))
	for _, cancel := range connections {
		cancels = append(cancels, cancel)
	}
	return cancels
}

func (s *Server) newRemoteAccessSession(mode string) (string, remoteAccessSession, error) {
	token, err := newRemoteAccessSessionToken()
	if err != nil {
		return "", remoteAccessSession{}, err
	}
	cfg := s.configSnapshot()
	session := remoteAccessSession{
		TokenHash:          remoteSessionTokenHash(token),
		Mode:               mode,
		ExpiresAt:          s.now().Add(remoteAccessSessionTTL).UTC(),
		CredentialRevision: normalizedCredentialRevision(cfg),
	}
	s.remoteAccessMu.Lock()
	if s.remoteAccessSessions == nil {
		s.remoteAccessSessions = make(map[string]remoteAccessSession)
	}
	s.remoteAccessSessions[session.TokenHash] = session
	cancels := append(s.pruneRemoteAccessSessionsLocked(s.now()), s.trimRemoteAccessSessionsLocked()...)
	s.remoteAccessMu.Unlock()
	cancelRemoteAccessConnections(cancels)
	return token, session, nil
}

func (s *Server) revokeRemoteAccessSession(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	s.remoteAccessMu.Lock()
	cancels := s.removeRemoteAccessSessionLocked(remoteSessionTokenHash(token))
	s.remoteAccessMu.Unlock()
	cancelRemoteAccessConnections(cancels)
}

func (s *Server) revokeAllRemoteAccessSessions() {
	s.remoteAccessMu.Lock()
	cancels := make([]context.CancelFunc, 0)
	for _, connections := range s.remoteAccessConnections {
		for _, cancel := range connections {
			cancels = append(cancels, cancel)
		}
	}
	s.remoteAccessSessions = make(map[string]remoteAccessSession)
	s.remoteAccessConnections = make(map[string]map[uint64]context.CancelFunc)
	s.remoteAccessMu.Unlock()
	cancelRemoteAccessConnections(cancels)
}

func (s *Server) pruneRemoteAccessSessionsLocked(now time.Time) []context.CancelFunc {
	cancels := make([]context.CancelFunc, 0)
	for hash, session := range s.remoteAccessSessions {
		if session.ExpiresAt.IsZero() || !session.ExpiresAt.After(now) {
			cancels = append(cancels, s.removeRemoteAccessSessionLocked(hash)...)
		}
	}
	return cancels
}

func (s *Server) trimRemoteAccessSessionsLocked() []context.CancelFunc {
	cancels := make([]context.CancelFunc, 0)
	for len(s.remoteAccessSessions) > remoteAccessSessionMaxCount {
		candidate := ""
		var earliest time.Time
		for hash, session := range s.remoteAccessSessions {
			if candidate == "" || session.ExpiresAt.Before(earliest) {
				candidate = hash
				earliest = session.ExpiresAt
			}
		}
		if candidate == "" {
			return cancels
		}
		cancels = append(cancels, s.removeRemoteAccessSessionLocked(candidate)...)
	}
	return cancels
}

func (s *Server) remoteSessionForToken(token string) (remoteAccessSession, bool) {
	hash := remoteSessionTokenHash(token)
	cfg := s.configSnapshot()
	now := s.now()
	s.remoteAccessMu.Lock()
	cancels := s.pruneRemoteAccessSessionsLocked(now)
	session, ok := s.remoteAccessSessions[hash]
	if !ok || session.CredentialRevision != normalizedCredentialRevision(cfg) || !session.ExpiresAt.After(now) {
		if ok {
			cancels = append(cancels, s.removeRemoteAccessSessionLocked(hash)...)
		}
		s.remoteAccessMu.Unlock()
		cancelRemoteAccessConnections(cancels)
		return remoteAccessSession{}, false
	}
	s.remoteAccessMu.Unlock()
	cancelRemoteAccessConnections(cancels)
	return session, true
}

func (s *Server) credentialConfigured() (bool, string) {
	security := s.configSnapshot().Security
	if strings.TrimSpace(security.AccessPassword) != "" {
		return true, "environment"
	}
	if strings.TrimSpace(security.AccessPasswordHash) != "" {
		return true, "config"
	}
	return false, "none"
}

func (s *Server) verifyRemoteAccessPassword(password string) bool {
	security := s.configSnapshot().Security
	if envPassword := strings.TrimSpace(security.AccessPassword); envPassword != "" {
		return constantTimeEqualToken(password, envPassword)
	}
	return config.VerifyAccessPassword(security.AccessPasswordHash, password)
}

func configuredRemoteAccessMode(cfg config.Config) string {
	if cfg.Security.AllowRemoteFullAccess {
		return remoteAccessModeFull
	}
	return remoteAccessModeRestricted
}

// remoteAccessAuthentication checks a request credential without emitting
// legacy warnings. Call validRemoteAccessReporting when warning behavior is
// required by older callers.
func (s *Server) remoteAccessAuthentication(r *http.Request) remoteAccessAuth {
	auth := remoteAccessAuth{Remote: s.remoteAccessGateRequired(r) || requestHasRemoteAccessCredential(r)}
	if cookie, err := r.Cookie(remoteAccessCookieName); err == nil {
		if session, ok := s.remoteSessionForToken(cookie.Value); ok {
			auth.Authenticated, auth.Mode, auth.ExpiresAt, auth.Session = true, session.Mode, session.ExpiresAt, true
			return auth
		}
		return auth // The canonical cookie deliberately takes precedence.
	}
	if cookie, err := r.Cookie(legacyRemoteAccessCookieName); err == nil {
		if session, ok := s.remoteSessionForToken(cookie.Value); ok {
			auth.Authenticated, auth.Mode, auth.ExpiresAt, auth.Session = true, session.Mode, session.ExpiresAt, true
			return auth
		}
	}
	if value := strings.TrimSpace(r.Header.Get(remoteAccessHeader)); value != "" {
		if s.verifyRemoteAccessPassword(value) {
			auth.Authenticated, auth.Mode = true, remoteAccessModeRestricted
		}
		return auth
	}
	if value := strings.TrimSpace(r.Header.Get(legacyRemoteAccessHeader)); value != "" {
		if s.verifyRemoteAccessPassword(value) {
			auth.Authenticated, auth.Mode = true, remoteAccessModeRestricted
		}
		return auth
	}
	bearer := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(bearer), "bearer ") && s.verifyRemoteAccessPassword(strings.TrimSpace(bearer[len("bearer "):])) {
		auth.Authenticated, auth.Mode = true, remoteAccessModeRestricted
		return auth
	}
	// Local requests do not need a remote credential. If a credential was
	// explicitly supplied, however, preserve the compatibility validator's
	// success/failure result instead of treating a bad credential as local auth.
	if !auth.Remote && !requestHasRemoteAccessCredential(r) {
		auth.Authenticated, auth.Mode = true, remoteAccessModeFull
	}
	return auth
}

func requestHasRemoteAccessCredential(r *http.Request) bool {
	if _, err := r.Cookie(remoteAccessCookieName); err == nil {
		return true
	}
	if _, err := r.Cookie(legacyRemoteAccessCookieName); err == nil {
		return true
	}
	return requestHasRemotePasswordCredential(r)
}

func requestHasRemotePasswordCredential(r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get(remoteAccessHeader)) != "" || strings.TrimSpace(r.Header.Get(legacyRemoteAccessHeader)) != "" {
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Authorization"))), "bearer ")
}

func remoteAccessSessionTokenFromRequest(r *http.Request) string {
	if cookie, err := r.Cookie(remoteAccessCookieName); err == nil {
		return strings.TrimSpace(cookie.Value)
	}
	if cookie, err := r.Cookie(legacyRemoteAccessCookieName); err == nil {
		return strings.TrimSpace(cookie.Value)
	}
	return ""
}

// remoteWebSocketContext binds an upgraded remote connection to the authority
// that admitted it. Session logout, password/policy changes, bounded-session
// eviction, and expiry therefore cancel established Agent and terminal sockets
// instead of only rejecting the next HTTP request.
func (s *Server) remoteWebSocketContext(parent context.Context, r *http.Request) (context.Context, context.CancelFunc, bool) {
	if !s.remoteAccessGateRequired(r) {
		ctx, cancel := context.WithCancel(parent)
		return ctx, cancel, true
	}

	// Match the global config mutation order so a policy/password update either
	// completes before this validation or cancels the connection after registry
	// insertion; it cannot slip between validation and registration.
	s.configMutationMu.Lock()
	defer s.configMutationMu.Unlock()
	auth := s.remoteAccessAuthentication(r)
	if !auth.Authenticated {
		return nil, func() {}, false
	}

	var ctx context.Context
	var cancel context.CancelFunc
	if auth.Session && !auth.ExpiresAt.IsZero() {
		ctx, cancel = context.WithDeadline(parent, auth.ExpiresAt)
	} else {
		ctx, cancel = context.WithCancel(parent)
	}
	if ctx.Err() != nil {
		cancel()
		return nil, func() {}, false
	}

	key := remoteAccessCredentialConnectionKey
	credentialRevision := normalizedCredentialRevision(s.configSnapshot())
	s.remoteAccessMu.Lock()
	if auth.Session {
		token := remoteAccessSessionTokenFromRequest(r)
		hash := remoteSessionTokenHash(token)
		session, ok := s.remoteAccessSessions[hash]
		now := s.now()
		if token == "" || !ok || session.CredentialRevision != credentialRevision || !session.ExpiresAt.After(now) {
			s.remoteAccessMu.Unlock()
			cancel()
			return nil, func() {}, false
		}
		key = remoteAccessSessionConnectionKey(hash)
	}
	if s.remoteAccessConnections == nil {
		s.remoteAccessConnections = make(map[string]map[uint64]context.CancelFunc)
	}
	if s.remoteAccessConnections[key] == nil {
		s.remoteAccessConnections[key] = make(map[uint64]context.CancelFunc)
	}
	s.remoteAccessConnectionSeq++
	connectionID := s.remoteAccessConnectionSeq
	s.remoteAccessConnections[key][connectionID] = cancel
	s.remoteAccessMu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			cancel()
			s.remoteAccessMu.Lock()
			if connections := s.remoteAccessConnections[key]; connections != nil {
				delete(connections, connectionID)
				if len(connections) == 0 {
					delete(s.remoteAccessConnections, key)
				}
			}
			s.remoteAccessMu.Unlock()
		})
	}
	return ctx, release, true
}

func (s *Server) resolveCWDForRequest(r *http.Request, input string) (string, error) {
	auth := s.remoteAccessAuthentication(r)
	if auth.Remote && auth.Mode != remoteAccessModeFull {
		return s.resolveFSPath(input)
	}
	return s.resolveHostFSPath(input)
}

func (s *Server) resolveFSPathForRequest(r *http.Request, input string) (string, error) {
	if s.fsHostScopeForRequest(r) {
		return s.resolveHostFSPath(input)
	}
	return s.resolveFSPath(input)
}

// fsHostScopeForRequest reports whether the request may browse the whole host.
// A remote session needs full access; a local session qualifies as long as the
// server is not exposed to the network (then it is the user's own machine and a
// loopback-looking caller cannot be an outsider enumerating the filesystem).
func (s *Server) fsHostScopeForRequest(r *http.Request) bool {
	auth := s.remoteAccessAuthentication(r)
	if auth.Remote {
		return auth.Mode == remoteAccessModeFull
	}
	return !s.configSnapshot().Security.Exposed
}

func (s *Server) resolveHostFSPath(input string) (string, error) {
	path := strings.TrimSpace(input)
	if path == "" {
		path = s.fsBasePath()
	} else if !filepath.IsAbs(path) {
		// Resolve relatives against the project base (not the server's working
		// directory) so host-scoped callers match project-scoped resolution.
		path = filepath.Join(s.fsBasePath(), path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return resolvePhysicalFSPath(abs)
}

func (s *Server) filesystemPathWithinProjectRoot(path string) bool {
	cfg := s.configSnapshot()
	if strings.TrimSpace(cfg.Paths.DefaultProjectDir) == "" && strings.TrimSpace(cfg.Paths.HomeDir) == "" {
		// Zero-value configs are used by isolated tests and embeddings. Real startup
		// always provides a configured project root before remote access is exposed.
		return true
	}
	_, err := s.resolveFSPath(path)
	return err == nil
}

func (s *Server) filesystemPathAllowedForRequest(r *http.Request, path string) bool {
	return s.capabilitiesForRequest(r).FilesystemScope != "project" || s.filesystemPathWithinProjectRoot(path)
}

func (s *Server) filterAgentsForRequest(r *http.Request, agents []db.Agent) []db.Agent {
	projectScoped := s.capabilitiesForRequest(r).FilesystemScope == "project"
	filtered := make([]db.Agent, 0, len(agents))
	for _, agent := range agents {
		if projectScoped && !s.filesystemPathWithinProjectRoot(agent.CWD) {
			continue
		}
		agent.SystemPrompt = ""
		agent.ContextSummary = ""
		filtered = append(filtered, agent)
	}
	return filtered
}

func (s *Server) filterProjectsForRequest(r *http.Request, projects []db.Project) []db.Project {
	if s.capabilitiesForRequest(r).FilesystemScope != "project" {
		return projects
	}
	filtered := make([]db.Project, 0, len(projects))
	for _, project := range projects {
		if s.filesystemPathWithinProjectRoot(project.GitPath) {
			filtered = append(filtered, project)
		}
	}
	return filtered
}

func (s *Server) filterWorklinesForRequest(r *http.Request, worklines []db.Workline) []db.Workline {
	if s.capabilitiesForRequest(r).FilesystemScope != "project" {
		return worklines
	}
	filtered := make([]db.Workline, 0, len(worklines))
	for _, workline := range worklines {
		if strings.TrimSpace(workline.WorktreePath) == "" || s.filesystemPathWithinProjectRoot(workline.WorktreePath) {
			filtered = append(filtered, workline)
		}
	}
	return filtered
}

func (s *Server) filterNavigationConversationsForRequest(r *http.Request, conversations []db.NavigationConversation) []db.NavigationConversation {
	return s.filterNavigationConversationsForRequestWithLegacy(r, conversations, false)
}

func (s *Server) filterNavigationConversationsForRequestWithLegacy(r *http.Request, conversations []db.NavigationConversation, allowLegacy bool) []db.NavigationConversation {
	projectScoped := s.capabilitiesForRequest(r).FilesystemScope == "project"
	filtered := make([]db.NavigationConversation, 0, len(conversations))
	for _, conversation := range conversations {
		if conversation.Context == db.ProjectFlowModeConversation {
			// Legacy standalone rows are available only to the explicit archive
			// projection so users can restore or permanently delete old data. The
			// ordinary navigation and overview paths always pass allowLegacy=false.
			if allowLegacy && (!projectScoped || strings.TrimSpace(conversation.CWD) == "") {
				filtered = append(filtered, conversation)
			}
			continue
		}
		if !projectScoped || s.filesystemPathWithinProjectRoot(conversation.CWD) {
			filtered = append(filtered, conversation)
		}
	}
	return filtered
}

func (s *Server) capabilitiesForRequest(r *http.Request) remoteCapabilities {
	auth := s.remoteAccessAuthentication(r)
	if !auth.Remote {
		return remoteCapabilities{
			MaxPermissionMode:    "bypassPermissions",
			TerminalAllowed:      true,
			FilesystemScope:      "host",
			NativePickerAllowed:  true,
			SecurityAdminAllowed: true,
		}
	}
	if auth.Mode == remoteAccessModeFull {
		return remoteCapabilities{
			MaxPermissionMode:    "bypassPermissions",
			TerminalAllowed:      true,
			FilesystemScope:      "host",
			NativePickerAllowed:  s.configSnapshot().Security.AllowRemoteNativePicker,
			SecurityAdminAllowed: false,
		}
	}
	return remoteCapabilities{
		MaxPermissionMode:    "acceptEdits",
		TerminalAllowed:      false,
		FilesystemScope:      "project",
		NativePickerAllowed:  false,
		SecurityAdminAllowed: false,
	}
}

func (s *Server) remotePermissionModeCapForRequest(r *http.Request) string {
	if s.capabilitiesForRequest(r).MaxPermissionMode == "acceptEdits" {
		return "acceptEdits"
	}
	return ""
}

func (s *Server) remoteSecurityMutationAllowed(r *http.Request, _ string) (bool, string) {
	if s.remoteAccessAuthentication(r).Remote {
		return false, "remote access settings can only be changed from localhost on the host running Autoto"
	}
	if constantTimeEqualToken(r.Header.Get(localTokenHeader), s.localToken) {
		return true, ""
	}
	return false, "security changes require the canonical local token"
}

type remoteAccessPolicyRequest struct {
	AllowFullAccess         *bool  `json:"allowFullAccess"`
	DefaultMode             string `json:"defaultMode"`
	AllowRemoteNativePicker *bool  `json:"allowRemoteNativePicker"`
	Revision                int64  `json:"revision"`
	CurrentPassword         string `json:"currentPassword"`
}

type remoteAccessPasswordRequest struct {
	Strategy        string `json:"strategy"`
	Password        string `json:"password"`
	CurrentPassword string `json:"currentPassword"`
}

func (s *Server) getRemoteAccessSettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.configSnapshot()
	configured, source := s.credentialConfigured()
	auth := s.remoteAccessAuthentication(r)
	session := map[string]any{"remote": auth.Remote, "authenticated": auth.Authenticated, "mode": "", "expiresAt": ""}
	if auth.Authenticated && auth.Remote {
		session["mode"] = auth.Mode
		if !auth.ExpiresAt.IsZero() {
			session["expiresAt"] = auth.ExpiresAt.UTC().Format(time.RFC3339Nano)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"credential":   map[string]any{"configured": configured, "source": source},
		"policy":       map[string]any{"allowFullAccess": cfg.Security.AllowRemoteFullAccess, "defaultMode": configuredRemoteAccessMode(cfg), "allowRemoteNativePicker": cfg.Security.AllowRemoteNativePicker, "revision": normalizedCredentialRevision(cfg)},
		"session":      session,
		"capabilities": s.capabilitiesForRequest(r),
		"tunnel":       s.temporaryTunnelSnapshot(),
	})
}

func (s *Server) updateRemoteAccessPolicy(w http.ResponseWriter, r *http.Request) {
	var req remoteAccessPolicyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.AllowFullAccess == nil || req.AllowRemoteNativePicker == nil {
		writeError(w, http.StatusBadRequest, "allowFullAccess and allowRemoteNativePicker are required")
		return
	}
	s.configMutationMu.Lock()
	defer s.configMutationMu.Unlock()
	if ok, message := s.remoteSecurityMutationAllowed(r, req.CurrentPassword); !ok {
		writeError(w, http.StatusForbidden, message)
		return
	}
	mode := remoteAccessModeRestricted
	if *req.AllowFullAccess {
		mode = remoteAccessModeFull
	}
	current := s.configSnapshot()
	if req.Revision != normalizedCredentialRevision(current) {
		writeError(w, http.StatusConflict, "remote access policy revision is stale")
		return
	}
	updated := current
	updated.Security.AllowRemoteFullAccess = *req.AllowFullAccess
	updated.Security.DefaultRemoteAccessMode = mode
	updated.Security.AllowRemoteNativePicker = *req.AllowRemoteNativePicker
	updated.Security.CredentialRevision = normalizedCredentialRevision(current) + 1
	path := s.configPathSnapshot()
	if strings.TrimSpace(path) == "" {
		writeError(w, http.StatusServiceUnavailable, "security configuration path is unavailable")
		return
	}
	if err := config.Save(path, updated); err != nil {
		writeError(w, http.StatusInternalServerError, "security policy was not saved: "+err.Error())
		return
	}
	s.cfgMu.Lock()
	s.cfg = updated
	s.cfgMu.Unlock()
	// Policy changes invalidate sessions so an existing full session cannot
	// outlive a newly restricted policy.
	s.revokeAllRemoteAccessSessions()
	writeJSON(w, http.StatusOK, map[string]any{"allowFullAccess": updated.Security.AllowRemoteFullAccess, "defaultMode": updated.Security.DefaultRemoteAccessMode, "allowRemoteNativePicker": updated.Security.AllowRemoteNativePicker, "revision": updated.Security.CredentialRevision})
}

func validateCustomRemoteAccessPassword(password string) error {
	if len(password) < 12 || len(password) > 256 {
		return errors.New("password must be between 12 and 256 characters")
	}
	classes := 0
	var lower, upper, digit, symbol bool
	for _, ch := range password {
		if unicode.IsSpace(ch) || unicode.IsControl(ch) {
			return errors.New("password must not contain whitespace or control characters")
		}
		switch {
		case unicode.IsLower(ch):
			lower = true
		case unicode.IsUpper(ch):
			upper = true
		case unicode.IsDigit(ch):
			digit = true
		default:
			symbol = true
		}
	}
	for _, present := range []bool{lower, upper, digit, symbol} {
		if present {
			classes++
		}
	}
	if classes < 3 {
		return errors.New("password must include at least three character classes")
	}
	return nil
}

func generateRemoteAccessPassword() (string, error) {
	// URL-safe base64 provides upper/lowercase letters, digits, and '-'/'_'.
	buf := make([]byte, 30)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "At-" + base64.RawURLEncoding.EncodeToString(buf), nil
}

func (s *Server) updateRemoteAccessPassword(w http.ResponseWriter, r *http.Request) {
	var req remoteAccessPasswordRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.configMutationMu.Lock()
	defer s.configMutationMu.Unlock()
	if ok, message := s.remoteSecurityMutationAllowed(r, req.CurrentPassword); !ok {
		writeError(w, http.StatusForbidden, message)
		return
	}
	strategy := strings.ToLower(strings.TrimSpace(req.Strategy))
	password := ""
	generated := ""
	switch strategy {
	case "generate":
		var err error
		password, err = generateRemoteAccessPassword()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not generate password")
			return
		}
		generated = password
	case "custom":
		password = req.Password
		if err := validateCustomRemoteAccessPassword(password); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "strategy must be generate or custom")
		return
	}
	hash, err := config.HashAccessPassword(password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	updated := s.configSnapshot()
	updated.Security.AccessPassword = ""
	updated.Security.AccessPasswordHash = hash
	updated.Security.CredentialRevision = normalizedCredentialRevision(updated) + 1
	path := s.configPathSnapshot()
	if strings.TrimSpace(path) == "" {
		writeError(w, http.StatusServiceUnavailable, "security configuration path is unavailable")
		return
	}
	if err := config.Save(path, updated); err != nil {
		writeError(w, http.StatusInternalServerError, "security password was not saved: "+err.Error())
		return
	}
	s.cfgMu.Lock()
	s.cfg = updated
	s.cfgMu.Unlock()
	s.revokeAllRemoteAccessSessions()
	response := map[string]any{"credential": map[string]any{"configured": true, "source": "config"}, "revision": updated.Security.CredentialRevision}
	if generated != "" {
		response["generatedPassword"] = generated
	}
	writeJSON(w, http.StatusOK, response)
}
