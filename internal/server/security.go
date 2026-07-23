package server

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"autoto/internal/config"
)

const localTokenHeader = "X-Autoto-Token"
const legacyLocalTokenHeader = "X-CodeHarbor-Token"
const localTokenCookieName = "autoto_local_token"
const localTokenQuery = "token"

const remoteAccessCookieName = "autoto_remote_access"
const legacyRemoteAccessCookieName = "codeharbor_remote_access"
const remoteAccessHeader = "X-Autoto-Access"
const legacyRemoteAccessHeader = "X-CodeHarbor-Access"

const remoteAccessPath = "/auth/remote-access"
const remoteAccessLogoutPath = "/auth/remote-access/logout"

const remoteAccessMaxFailures = 10
const remoteAccessFailureWindow = 15 * time.Minute
const remoteAccessLockDuration = 15 * time.Minute
const remoteAccessFailureMaxEntries = 2048

type remoteAccessFailure struct {
	Count       int
	FirstFailed time.Time
	LockedUntil time.Time
}

func newLocalToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("crypto/rand failed while generating local token: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// localAPITokenFile is the durable loopback API token under homeDir/secrets.
// Regenerating it on every process start invalidates open browser tabs and can
// turn any auto-loading settings panel into a 401 toast storm after restarts.
const localAPITokenFileName = "local-api.token"

func localAPITokenPath(homeDir string) string {
	homeDir = strings.TrimSpace(homeDir)
	if homeDir == "" {
		return ""
	}
	return filepath.Join(homeDir, "secrets", localAPITokenFileName)
}

func resolveLocalToken(homeDir string) string {
	if token := strings.TrimSpace(os.Getenv("AUTOTO_LOCAL_TOKEN")); token != "" {
		return token
	}
	if token := strings.TrimSpace(os.Getenv("CODEHARBOR_LOCAL_TOKEN")); token != "" {
		return token
	}
	path := localAPITokenPath(homeDir)
	if path != "" {
		if token, err := loadLocalTokenFile(path); err == nil && token != "" {
			return token
		}
	}
	token := newLocalToken()
	if path != "" {
		if err := writeLocalTokenFile(path, token); err != nil {
			slog.Warn("persist local API token", "path", path, "error", err)
		}
	}
	return token
}

func loadLocalTokenFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("empty local API token file")
	}
	return token, nil
}

func writeLocalTokenFile(path, token string) error {
	token = strings.TrimSpace(token)
	if path == "" || token == "" {
		return errors.New("missing local API token path or value")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(dir, 0o700)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(tmp, 0o600)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (s *Server) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}

func (s *Server) localRequestGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remote := s.remoteAccessGateRequired(r)
		if remote && remotePlainHTTP(r) && !isRemoteAccessLoginPath(r.URL.Path) {
			writeError(w, http.StatusForbidden, "remote access requires HTTPS")
			return
		}
		if remote && s.handleRemoteAccessGate(w, r) {
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api" {
			if !s.sameOriginRequest(r) {
				writeError(w, http.StatusForbidden, "cross-origin API request denied")
				return
			}
			if isBrowserInitiated(r) {
				if remote {
					if !s.remoteAccessAuthentication(r).Authenticated {
						writeError(w, http.StatusUnauthorized, "missing or invalid remote session")
						return
					}
				} else if !s.validHeaderToken(r) {
					writeError(w, http.StatusUnauthorized, "missing or invalid local API token")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) sensitiveLocalTokenGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.requireSensitiveLocalToken(w, r) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) fullRemoteAccessGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := s.remoteAccessAuthentication(r)
		if auth.Remote && (!auth.Authenticated || !auth.Session || auth.Mode != remoteAccessModeFull) {
			writeError(w, http.StatusForbidden, "security administration requires a full remote session")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireSensitiveLocalToken(w http.ResponseWriter, r *http.Request) bool {
	auth := s.remoteAccessAuthentication(r)
	if auth.Remote {
		// A restricted remote session must never be able to promote itself by
		// replaying a leaked or stale canonical local token.
		if !auth.Authenticated || !auth.Session || auth.Mode != remoteAccessModeFull {
			writeError(w, http.StatusForbidden, "sensitive API access requires a full remote session")
			return false
		}
		return true
	}
	if !constantTimeEqualToken(r.Header.Get(localTokenHeader), s.localToken) {
		writeError(w, http.StatusUnauthorized, "missing or invalid local API token")
		return false
	}
	return true
}

func (s *Server) validateWebSocketRequest(w http.ResponseWriter, r *http.Request) bool {
	if !s.sameOriginRequest(r) {
		writeError(w, http.StatusForbidden, "cross-origin websocket request denied")
		return false
	}
	if s.remoteAccessGateRequired(r) {
		if !s.remoteAccessAuthentication(r).Authenticated {
			writeError(w, http.StatusUnauthorized, "missing or invalid remote session")
			return false
		}
		return true
	}
	if !s.validWebSocketToken(r) {
		writeError(w, http.StatusUnauthorized, "missing or invalid websocket token")
		return false
	}
	return true
}

func (s *Server) validHeaderToken(r *http.Request) bool {
	valid, legacy := validTokenFromHeadersWithSource(r, s.localToken, localTokenHeader, legacyLocalTokenHeader)
	if valid && legacy {
		s.warnLegacy("credential:"+legacyLocalTokenHeader, legacyLocalTokenHeader, localTokenHeader, "request-header")
	}
	return valid
}

func (s *Server) validWebSocketToken(r *http.Request) bool {
	if cookie, err := r.Cookie(localTokenCookieName); err == nil && constantTimeEqualToken(cookie.Value, s.localToken) {
		return true
	}
	if s.validHeaderToken(r) {
		return true
	}
	if constantTimeEqualToken(r.URL.Query().Get(localTokenQuery), s.localToken) {
		s.warnLegacy("credential:websocket-query-token", "WebSocket ?token= query parameter", localTokenCookieName+" cookie or "+localTokenHeader+" header", "query-parameter")
		return true
	}
	return false
}

func validTokenFromHeaders(r *http.Request, want string, canonicalName, legacyName string) bool {
	valid, _ := validTokenFromHeadersWithSource(r, want, canonicalName, legacyName)
	return valid
}

func validTokenFromHeadersWithSource(r *http.Request, want string, canonicalName, legacyName string) (bool, bool) {
	if canonicalValue := strings.TrimSpace(r.Header.Get(canonicalName)); canonicalValue != "" {
		return constantTimeEqualToken(canonicalValue, want), false
	}
	valid := constantTimeEqualToken(r.Header.Get(legacyName), want)
	return valid, valid
}

func constantTimeEqualToken(got, want string) bool {
	got = strings.TrimSpace(got)
	want = strings.TrimSpace(want)
	if got == "" || want == "" || len(got) != len(want) {
		return false
	}
	var diff byte
	for i := 0; i < len(got); i++ {
		diff |= got[i] ^ want[i]
	}
	return diff == 0
}

func verifyRemoteAccessPasswordForConfig(cfg config.Config, password string) bool {
	if envPassword := strings.TrimSpace(cfg.Security.AccessPassword); envPassword != "" {
		return constantTimeEqualToken(password, envPassword)
	}
	return config.VerifyAccessPassword(cfg.Security.AccessPasswordHash, password)
}

// newRemoteAccessSessionForConfig mints a session from the configuration snapshot
// already protected by configMutationMu. Keeping the revision and mode coupled to
// that snapshot prevents a concurrent credential/policy update from issuing stale
// authority after the update completes.
func (s *Server) newRemoteAccessSessionForConfig(mode string, cfg config.Config) (string, error) {
	token, err := newRemoteAccessSessionToken()
	if err != nil {
		return "", err
	}
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
	return token, nil
}

func isBrowserInitiated(r *http.Request) bool {
	return strings.TrimSpace(r.Header.Get("Origin")) != "" || strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")) != ""
}

func (s *Server) sameOriginRequest(r *http.Request) bool {
	fetchSite := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
	if fetchSite == "cross-site" {
		return false
	}
	// Sec-Fetch-Site is a browser-controlled forbidden header. Accept its
	// explicit same-origin verdict before parsing Origin so privacy-focused
	// browsers and embedded engines that serialize Origin as null or with a root
	// slash can still submit the form. Non-browser clients could already omit
	// Origin entirely, so this does not widen the existing client boundary.
	if fetchSite == "same-origin" {
		return true
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	for _, target := range requestOriginTargets(r) {
		if sameOrigin(parsed, target) {
			return true
		}
	}
	return false
}

type requestOriginTarget struct {
	scheme string
	host   string
}

func sameOrigin(origin *url.URL, target requestOriginTarget) bool {
	return strings.EqualFold(origin.Scheme, target.scheme) && sameHostForScheme(origin.Host, target.host, origin.Scheme)
}

func sameHostForScheme(a, b, scheme string) bool {
	return strings.EqualFold(normalizeHostPortForScheme(a, scheme), normalizeHostPortForScheme(b, scheme))
}

func normalizeHostPortForScheme(value, scheme string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		host, port = strings.Trim(value, "[]"), ""
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "" {
		return ""
	}
	if port == "" || (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		return host
	}
	return net.JoinHostPort(host, port)
}

func requestOriginTargets(r *http.Request) []requestOriginTarget {
	if trustedLoopbackPeer(r) && (requestHasRemoteForwardingHeaders(r) || hasForwardedSchemeHeader(r)) {
		if target, ok := trustedForwardedOriginTarget(r); ok {
			return []requestOriginTarget{target}
		}
		return nil
	}
	return []requestOriginTarget{{scheme: requestScheme(r), host: r.Host}}
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

type forwardedHop struct {
	host     string
	proto    string
	hostSet  bool
	protoSet bool
}

func trustedForwardedOriginTarget(r *http.Request) (requestOriginTarget, bool) {
	if !trustedLoopbackPeer(r) {
		return requestOriginTarget{}, false
	}
	scheme, ok := trustedForwardedScheme(r)
	if !ok {
		return requestOriginTarget{}, false
	}
	xForwardedHost := lastHeaderListValue(r.Header.Values("X-Forwarded-Host"))
	hop, _ := lastForwardedHop(r.Header.Values("Forwarded"))
	if xForwardedHost != "" && hop.hostSet && !sameHostForScheme(xForwardedHost, hop.host, scheme) {
		return requestOriginTarget{}, false
	}
	host := xForwardedHost
	if host == "" && hop.hostSet {
		host = hop.host
	}
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	if normalizeHostPortForScheme(host, scheme) == "" {
		return requestOriginTarget{}, false
	}
	return requestOriginTarget{scheme: scheme, host: host}, true
}

func trustedForwardedScheme(r *http.Request) (string, bool) {
	if !trustedLoopbackPeer(r) {
		return "", false
	}
	xForwardedProtoRaw := lastHeaderListValue(r.Header.Values("X-Forwarded-Proto"))
	xForwardedProto, xForwardedProtoOK := normalizeForwardedScheme(xForwardedProtoRaw)
	if xForwardedProtoRaw != "" && !xForwardedProtoOK {
		return "", false
	}
	hop, forwardedPresent := lastForwardedHop(r.Header.Values("Forwarded"))
	if forwardedPresent && hop.protoSet && hop.proto == "" {
		return "", false
	}
	if xForwardedProtoOK && hop.proto != "" && xForwardedProto != hop.proto {
		return "", false
	}
	if xForwardedProtoOK {
		return xForwardedProto, true
	}
	if hop.proto != "" {
		return hop.proto, true
	}
	return "", false
}

func hasForwardedSchemeHeader(r *http.Request) bool {
	if lastHeaderListValue(r.Header.Values("X-Forwarded-Proto")) != "" {
		return true
	}
	hop, present := lastForwardedHop(r.Header.Values("Forwarded"))
	return present && hop.protoSet
}

func normalizeForwardedScheme(value string) (string, bool) {
	scheme := strings.ToLower(strings.Trim(strings.TrimSpace(value), "\""))
	return scheme, scheme == "http" || scheme == "https"
}

func lastForwardedHop(values []string) (forwardedHop, bool) {
	part := lastHeaderListValue(values)
	if part == "" {
		return forwardedHop{}, false
	}
	hop := forwardedHop{}
	for _, param := range strings.Split(part, ";") {
		key, raw, ok := strings.Cut(strings.TrimSpace(param), "=")
		if !ok {
			continue
		}
		raw = strings.Trim(strings.TrimSpace(raw), "\"")
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "host":
			hop.host, hop.hostSet = raw, raw != ""
		case "proto":
			hop.protoSet = true
			if scheme, valid := normalizeForwardedScheme(raw); valid {
				hop.proto = scheme
			}
		}
	}
	return hop, true
}

func lastHeaderListValue(values []string) string {
	last := ""
	for _, value := range values {
		for _, part := range splitCommaList(value) {
			if part != "" {
				last = part
			}
		}
	}
	return last
}

func (s *Server) remoteAccessGateRequired(r *http.Request) bool {
	if !trustedLoopbackPeer(r) {
		return true
	}
	if requestHasRemoteForwardingHeaders(r) {
		return true
	}
	// Local administrator authority requires both a loopback transport peer and
	// a loopback Host. This prevents DNS-rebinding or forged Host requests from
	// receiving the process-local token even when they reach a loopback listener.
	return !isLoopbackHost(r.Host)
}

func (s *Server) remoteHardeningActive(r *http.Request) bool {
	auth := s.remoteAccessAuthentication(r)
	// Hardening is a capability property of an authenticated restricted remote
	// session, not merely a property of the listener or remote network path.
	return auth.Remote && auth.Authenticated && auth.Mode == remoteAccessModeRestricted
}

func (s *Server) handleRemoteAccessGate(w http.ResponseWriter, r *http.Request) bool {
	if isRemoteAccessLoginPath(r.URL.Path) {
		s.handleRemoteAccessLogin(w, r)
		return true
	}
	passwordCredential := requestHasRemotePasswordCredential(r)
	if passwordCredential {
		if locked, until := s.remoteAccessLocked(r); locked {
			writeError(w, http.StatusTooManyRequests, s.remoteAccessLockMessage(until))
			return true
		}
	}
	if s.validRemoteAccess(r) {
		if passwordCredential {
			s.clearRemoteAccessFailures(r)
		}
		return false
	}
	if passwordCredential {
		if lockedUntil := s.recordRemoteAccessFailure(r); !lockedUntil.IsZero() {
			writeError(w, http.StatusTooManyRequests, s.remoteAccessLockMessage(lockedUntil))
			return true
		}
	}
	if shouldRenderRemoteAccessPage(r) {
		s.writeRemoteAccessLoginPage(w, r, http.StatusUnauthorized, "")
		return true
	}
	message := "remote access requires a configured credential"
	if configured, _ := s.credentialConfigured(); configured {
		message = "remote access authentication required"
	}
	writeError(w, http.StatusUnauthorized, message)
	return true
}

func isRemoteAccessLoginPath(path string) bool {
	return path == remoteAccessPath || path == remoteAccessLogoutPath
}

func shouldRenderRemoteAccessPage(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/ws/") {
		return false
	}
	return acceptsHTML(r) || r.URL.Path == "/"
}

func acceptsHTML(r *http.Request) bool {
	accept := strings.ToLower(r.Header.Get("Accept"))
	return accept == "" || strings.Contains(accept, "text/html")
}

func (s *Server) handleRemoteAccessLogin(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == remoteAccessLogoutPath {
		s.handleRemoteAccessLogout(w, r)
		return
	}
	loginCopy := remoteLoginCopyForAcceptLanguage(r.Header.Get("Accept-Language"))
	if r.Method != http.MethodPost {
		if r.Method == http.MethodGet {
			if s.remoteAccessGateRequired(r) && remotePlainHTTP(r) {
				s.writeRemoteAccessLoginPage(w, r, http.StatusForbidden, loginCopy.HTTPSRequiredMessage)
				return
			}
			s.writeRemoteAccessLoginPage(w, r, http.StatusOK, "")
			return
		}
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.sameOriginRequest(r) {
		s.writeRemoteAccessLoginPage(w, r, http.StatusForbidden, loginCopy.CrossSiteDeniedMessage)
		return
	}
	if s.remoteAccessGateRequired(r) && remotePlainHTTP(r) {
		s.writeRemoteAccessLoginPage(w, r, http.StatusForbidden, loginCopy.HTTPSRequiredMessage)
		return
	}
	if locked, until := s.remoteAccessLocked(r); locked {
		s.writeRemoteAccessLoginPage(w, r, http.StatusTooManyRequests, s.remoteAccessLockMessageForCopy(until, loginCopy))
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeRemoteAccessLoginPage(w, r, http.StatusBadRequest, loginCopy.FormUnreadableMessage)
		return
	}

	// Keep credential verification, policy selection, credential revision capture,
	// and session issuance in one security mutation critical section. A concurrent
	// local password/policy update therefore cannot produce a session for stale
	// authority after the update has completed.
	s.configMutationMu.Lock()
	cfg := s.configSnapshot()
	if !verifyRemoteAccessPasswordForConfig(cfg, r.FormValue("password")) {
		s.configMutationMu.Unlock()
		lockedUntil := s.recordRemoteAccessFailure(r)
		if !lockedUntil.IsZero() {
			s.writeRemoteAccessLoginPage(w, r, http.StatusTooManyRequests, s.remoteAccessLockMessageForCopy(lockedUntil, loginCopy))
			return
		}
		s.writeRemoteAccessLoginPage(w, r, http.StatusUnauthorized, loginCopy.IncorrectPasswordMessage)
		return
	}
	// The remote client only proves possession of the password. Session authority
	// is selected by the operator on the host running Autoto and cannot be
	// upgraded by posting a forged form value.
	token, err := s.newRemoteAccessSessionForConfig(configuredRemoteAccessMode(cfg), cfg)
	s.configMutationMu.Unlock()
	if err != nil {
		s.writeRemoteAccessLoginPage(w, r, http.StatusInternalServerError, loginCopy.SessionFailedMessage)
		return
	}
	s.clearRemoteAccessFailures(r)
	s.setRemoteAccessCookie(w, r, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)

}

func (s *Server) handleRemoteAccessLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.sameOriginRequest(r) {
		writeError(w, http.StatusForbidden, "cross-site logout request denied")
		return
	}
	if s.remoteAccessGateRequired(r) && remotePlainHTTP(r) {
		writeError(w, http.StatusForbidden, "remote access requires HTTPS")
		return
	}
	if s.validRemoteAccessWithoutWarning(r) {
		s.clearRemoteAccessFailures(r)
		if cookie, err := r.Cookie(remoteAccessCookieName); err == nil {
			s.revokeRemoteAccessSession(cookie.Value)
		} else if legacyCookie, legacyErr := r.Cookie(legacyRemoteAccessCookieName); legacyErr == nil {
			s.revokeRemoteAccessSession(legacyCookie.Value)
		}
	}
	s.clearRemoteAccessCookie(w, r)
	if acceptsHTML(r) {
		http.Redirect(w, r, remoteAccessPath, http.StatusSeeOther)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) remoteAccessLocked(r *http.Request) (bool, time.Time) {
	key := remoteAccessClientKey(r)
	now := s.now()
	s.remoteAccessMu.Lock()
	defer s.remoteAccessMu.Unlock()
	failure, ok := s.remoteAccessFailure[key]
	if !ok {
		return false, time.Time{}
	}
	if remoteAccessFailureExpired(failure, now) {
		delete(s.remoteAccessFailure, key)
		return false, time.Time{}
	}
	if !failure.LockedUntil.IsZero() {
		return true, failure.LockedUntil
	}
	return false, time.Time{}
}

func (s *Server) recordRemoteAccessFailure(r *http.Request) time.Time {
	key := remoteAccessClientKey(r)
	now := s.now()
	s.remoteAccessMu.Lock()
	defer s.remoteAccessMu.Unlock()
	if s.remoteAccessFailure == nil {
		s.remoteAccessFailure = make(map[string]remoteAccessFailure)
	}
	s.pruneRemoteAccessFailuresLocked(now)
	failure := s.remoteAccessFailure[key]
	if failure.FirstFailed.IsZero() || now.Sub(failure.FirstFailed) > remoteAccessFailureWindow {
		failure = remoteAccessFailure{FirstFailed: now}
	}
	failure.Count++
	if failure.Count >= remoteAccessMaxFailures {
		failure.LockedUntil = now.Add(remoteAccessLockDuration)
	}
	s.remoteAccessFailure[key] = failure
	s.trimRemoteAccessFailuresLocked()
	return failure.LockedUntil
}

func (s *Server) clearRemoteAccessFailures(r *http.Request) {
	key := remoteAccessClientKey(r)
	s.remoteAccessMu.Lock()
	defer s.remoteAccessMu.Unlock()
	delete(s.remoteAccessFailure, key)
}

func (s *Server) remoteAccessLockMessage(until time.Time) string {
	return s.remoteAccessLockMessageForCopy(until, remoteLoginCopyForLocale(remoteLoginLocaleChineseSimplified))
}

func (s *Server) remoteAccessLockMessageForCopy(until time.Time, loginCopy remoteLoginCopy) string {
	remaining := until.Sub(s.now())
	if remaining <= time.Minute {
		return loginCopy.LockRetrySoonMessage
	}
	minutes := int((remaining + time.Minute - 1) / time.Minute)
	return fmt.Sprintf(loginCopy.LockRetryMinutesMessage, minutes)
}

func (s *Server) pruneRemoteAccessFailuresLocked(now time.Time) {
	for key, failure := range s.remoteAccessFailure {
		if remoteAccessFailureExpired(failure, now) {
			delete(s.remoteAccessFailure, key)
		}
	}
}

func (s *Server) trimRemoteAccessFailuresLocked() {
	for len(s.remoteAccessFailure) > remoteAccessFailureMaxEntries {
		candidate := remoteAccessFailureTrimCandidate(s.remoteAccessFailure)
		if candidate == "" {
			return
		}
		delete(s.remoteAccessFailure, candidate)
	}
}

func remoteAccessFailureTrimCandidate(failures map[string]remoteAccessFailure) string {
	oldestUnlockedKey := ""
	oldestUnlocked := time.Time{}
	oldestLockedKey := ""
	oldestLocked := time.Time{}
	for key, failure := range failures {
		if failure.FirstFailed.IsZero() {
			return key
		}
		if failure.LockedUntil.IsZero() {
			if oldestUnlockedKey == "" || failure.FirstFailed.Before(oldestUnlocked) {
				oldestUnlockedKey = key
				oldestUnlocked = failure.FirstFailed
			}
			continue
		}
		if oldestLockedKey == "" || failure.FirstFailed.Before(oldestLocked) {
			oldestLockedKey = key
			oldestLocked = failure.FirstFailed
		}
	}
	if oldestUnlockedKey != "" {
		return oldestUnlockedKey
	}
	return oldestLockedKey
}

func remoteAccessFailureExpired(failure remoteAccessFailure, now time.Time) bool {
	if !failure.LockedUntil.IsZero() {
		return !now.Before(failure.LockedUntil)
	}
	if failure.FirstFailed.IsZero() {
		return true
	}
	return now.Sub(failure.FirstFailed) > remoteAccessFailureWindow
}

func remoteAccessClientKey(r *http.Request) string {
	if isLoopbackHost(r.RemoteAddr) {
		if ip := trustedForwardedClientIP(r); ip != "" {
			return "ip:" + ip
		}
	}
	if ip := headerClientIP(r.RemoteAddr); ip != "" {
		return "ip:" + ip
	}
	if host := hostOnly(r.RemoteAddr); host != "" {
		return "host:" + host
	}
	return "unknown"
}

func trustedForwardedClientIP(r *http.Request) string {
	// Cloudflare overwrites CF-Connecting-IP and supplies Cf-Ray. Requiring both
	// avoids treating a client-injected vendor header as the lockout identity
	// behind an unrelated loopback proxy.
	if strings.TrimSpace(strings.Join(r.Header.Values("Cf-Ray"), ",")) != "" {
		if ip := lastHeaderClientIP(r.Header.Values("CF-Connecting-IP")); ip != "" {
			return ip
		}
	}
	// Generic proxies commonly append their authoritative hop. Use the final
	// valid value rather than a client-controlled prefix.
	if ip := lastForwardedForClientIP(r.Header.Values("Forwarded")); ip != "" {
		return ip
	}
	return lastHeaderClientIP(r.Header.Values("X-Forwarded-For"))
}

func lastForwardedForClientIP(values []string) string {
	part := lastHeaderListValue(values)
	if part == "" {
		return ""
	}
	for _, param := range strings.Split(part, ";") {
		key, raw, ok := strings.Cut(strings.TrimSpace(param), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "for") {
			continue
		}
		return headerClientIP(raw)
	}
	return ""
}

func lastHeaderClientIP(values []string) string {
	last := ""
	for _, value := range values {
		for _, item := range splitCommaList(value) {
			if ip := headerClientIP(item); ip != "" {
				last = ip
			}
		}
	}
	return last
}

func firstHeaderClientIP(values []string) string {
	for _, value := range values {
		for _, item := range splitCommaList(value) {
			if ip := headerClientIP(item); ip != "" {
				return ip
			}
		}
	}
	return ""
}

func headerClientIP(value string) string {
	value = strings.Trim(strings.TrimSpace(value), "\"")
	if value == "" || strings.EqualFold(value, "unknown") || strings.HasPrefix(value, "_") {
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.Trim(value, "[]")
	ip := net.ParseIP(value)
	if ip == nil {
		return ""
	}
	return ip.String()
}

func (s *Server) validRemoteAccess(r *http.Request) bool {
	return s.validRemoteAccessReporting(r, true)
}

func (s *Server) validRemoteAccessWithoutWarning(r *http.Request) bool {
	return s.validRemoteAccessReporting(r, false)
}

func (s *Server) validRemoteAccessReporting(r *http.Request, warn bool) bool {
	auth := s.remoteAccessAuthentication(r)
	if !auth.Authenticated {
		return false
	}
	if warn {
		if _, err := r.Cookie(remoteAccessCookieName); err != nil {
			if _, legacyErr := r.Cookie(legacyRemoteAccessCookieName); legacyErr == nil {
				s.warnLegacy("credential:"+legacyRemoteAccessCookieName, legacyRemoteAccessCookieName, remoteAccessCookieName, "cookie")
			}
		}
		if strings.TrimSpace(r.Header.Get(remoteAccessHeader)) == "" && strings.TrimSpace(r.Header.Get(legacyRemoteAccessHeader)) != "" {
			s.warnLegacy("credential:"+legacyRemoteAccessHeader, legacyRemoteAccessHeader, remoteAccessHeader, "request-header")
		}
	}
	return true
}

func (s *Server) setRemoteAccessCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     remoteAccessCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int((24 * time.Hour).Seconds()),
		HttpOnly: true,
		Secure:   requestIsHTTPS(r) || s.remoteAccessGateRequired(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearRemoteAccessCookie(w http.ResponseWriter, r *http.Request) {
	for _, name := range []string{remoteAccessCookieName, legacyRemoteAccessCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   requestIsHTTPS(r) || s.remoteAccessGateRequired(r),
			SameSite: http.SameSiteLaxMode,
		})
	}
}

func requestIsHTTPS(r *http.Request) bool {
	if trustedLoopbackPeer(r) && (requestHasRemoteForwardingHeaders(r) || hasForwardedSchemeHeader(r)) {
		scheme, ok := trustedForwardedScheme(r)
		return ok && scheme == "https"
	}
	// For direct requests, only the transport TLS state is authoritative. An
	// absolute-form plaintext request can carry an https URL scheme and must not
	// be able to impersonate a TLS connection.
	return r.TLS != nil
}

func remotePlainHTTP(r *http.Request) bool {
	return !requestIsHTTPS(r)
}

func (s *Server) writeRemoteAccessLoginPage(w http.ResponseWriter, r *http.Request, status int, message string) {
	loginCopy := remoteLoginCopyForAcceptLanguage(r.Header.Get("Accept-Language"))
	passwordConfigured, _ := s.credentialConfigured()
	messageHTML := ""
	if message != "" {
		messageHTML = fmt.Sprintf(`<div class="alert" role="alert">%s</div>`, html.EscapeString(message))
	} else if !passwordConfigured {
		messageHTML = fmt.Sprintf(`<div class="alert" role="alert">%s<code>AUTOTO_ACCESS_PASSWORD</code>%s</div>`, html.EscapeString(loginCopy.UnconfiguredPasswordNoticeBefore), html.EscapeString(loginCopy.UnconfiguredPasswordNoticeAfter))
	}
	formHTML := fmt.Sprintf(`<button class="submit" type="submit"><span>%s</span><svg viewBox="0 0 24 24" aria-hidden="true"><path d="m9 18 6-6-6-6"></path></svg></button>`, html.EscapeString(loginCopy.SubmitLabel))
	if !passwordConfigured {
		formHTML = fmt.Sprintf(`<button class="submit" type="submit" disabled><span>%s</span></button>`, html.EscapeString(loginCopy.DisabledSubmitLabel))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Language", loginCopy.LanguageTag)
	w.Header().Set("Vary", "Accept-Language")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="%s">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover" />
  <title>%s</title>
  <style>
    :root { color-scheme: light; --page:#f4f6fb; --card:#ffffff; --line:#e2e6ef; --line-strong:#d4dae6; --text:#202634; --muted:#747e90; --accent:#5369f3; --accent-light:#6678f5; --radius:8px; --radius-sm:calc(var(--radius) * .6); --radius-md:calc(var(--radius) * .8); --radius-lg:var(--radius); --radius-xl:calc(var(--radius) * 1.4); }
    * { box-sizing: border-box; }
    html { min-height: 100%%; background: var(--page); }
    body { position: relative; min-height: 100dvh; margin: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 5px; overflow-x: hidden; padding: 28px 18px; background: radial-gradient(circle at 18%% 10%%, rgba(92,108,255,.13), transparent 34%%), radial-gradient(circle at 88%% 88%%, rgba(62,190,255,.07), transparent 28%%), #f4f6fb; color: var(--text); font: 15px/1.55 ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    body::before { content: ""; position: fixed; inset: 0; pointer-events: none; opacity: .55; background-image: linear-gradient(rgba(77,88,116,.035) 1px, transparent 1px), linear-gradient(90deg, rgba(77,88,116,.035) 1px, transparent 1px); background-size: 32px 32px; mask-image: linear-gradient(to bottom, black, transparent 88%%); }
    svg { display: block; fill: none; stroke: currentColor; stroke-width: 1.8; stroke-linecap: round; stroke-linejoin: round; }
    .remote-access-card { position: relative; z-index: 1; width: min(100%%, 488px); overflow: hidden; border: 1px solid var(--line); border-radius: var(--radius-xl); background: linear-gradient(150deg, rgba(255,255,255,.99), rgba(250,251,254,.99)); padding: 32px; box-shadow: 0 1px 2px rgba(49,61,96,.06), 0 10px 30px rgba(49,61,96,.08); }
    .remote-access-card::before { content: ""; position: absolute; width: 290px; height: 290px; top: -190px; right: -150px; border-radius: 999px; background: radial-gradient(circle, rgba(109,124,255,.18), transparent 68%%); pointer-events: none; }
    .card-content { position: relative; z-index: 1; }
    .brand-row { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 28px; }
    .brand-identity { display: inline-flex; align-items: center; gap: 10px; min-width: 0; }
    .brand-mark { width: 36px; height: 36px; display: inline-flex; flex: 0 0 36px; align-items: center; justify-content: center; border: 1px solid #d2d9ff; border-radius: var(--radius-lg); background: linear-gradient(145deg, #eef1ff, #f7f8ff); color: #5369f3; box-shadow: 0 1px 2px rgba(77,93,224,.10); }
    .brand-mark svg { width: 24px; height: 24px; }
    .brand-name { color: #252b38; font-size: 13px; font-weight: 850; letter-spacing: .17em; }
    .connection-state { display: inline-flex; align-items: center; gap: 7px; color: #7f899a; font-size: 11px; white-space: nowrap; }
    .connection-state::before { content: ""; width: 7px; height: 7px; flex: 0 0 7px; border-radius: 999px; background: #e7a83f; box-shadow: 0 0 0 4px rgba(231,168,63,.12); transform-origin: center; animation: connection-bounce 1.15s cubic-bezier(.45,.05,.55,.95) infinite; }
    @keyframes connection-bounce {
      0%%, 100%% { transform: translateY(0) scale(.94); box-shadow: 0 0 0 4px rgba(231,168,63,.10); }
      48%% { transform: translateY(-4px) scale(1.12); box-shadow: 0 5px 0 -2px rgba(231,168,63,.18), 0 0 0 7px rgba(231,168,63,.08); }
    }
    h1 { margin: 18px 0 24px; color: #202634; font-size: 34px; letter-spacing: -.045em; line-height: 1.08; }
    form { display: grid; gap: 10px; }
    .password-label { color: #5d6676; font-size: 13px; font-weight: 700; }
    .password-field { position: relative; display: flex; align-items: center; min-height: 56px; border: 1px solid var(--line-strong); border-radius: var(--radius-lg); background: #f7f8fb; transition: border-color .16s ease, box-shadow .16s ease, background .16s ease; }
    .password-field:focus-within { border-color: #9aa8ff; background: #ffffff; box-shadow: 0 0 0 3px rgba(83,105,243,.12); }
    .password-icon { width: 19px; height: 19px; display: inline-flex; flex: 0 0 19px; margin-left: 16px; color: #8791a3; }
    .password-icon svg { width: 19px; height: 19px; }
    .password-field input { width: 100%%; min-width: 0; height: 54px; border: 0; outline: 0; background: transparent; color: var(--text); padding: 0 16px 0 12px; font: inherit; font-size: 16px; }
    .password-field input::placeholder { color: #9aa3b2; }
    .password-field input:disabled { cursor: not-allowed; opacity: .58; }
    .submit { width: 100%%; min-height: 54px; display: inline-flex; align-items: center; justify-content: center; gap: 9px; margin-top: 8px; border: 0; border-radius: var(--radius-lg); background: linear-gradient(135deg, #6576ff, #5264ee); color: #fff; box-shadow: 0 1px 2px rgba(69,87,224,.24), 0 4px 12px rgba(69,87,224,.18); font: inherit; font-weight: 820; cursor: pointer; transition: transform .15s ease, box-shadow .15s ease, filter .15s ease; }
    .submit svg { width: 18px; height: 18px; transition: transform .15s ease; }
    .submit:hover:not(:disabled) { transform: translateY(-1px); filter: brightness(1.04); box-shadow: 0 2px 4px rgba(69,87,224,.22), 0 7px 18px rgba(69,87,224,.22); }
    .submit:hover:not(:disabled) svg { transform: translateX(3px); }
    .submit:active:not(:disabled) { transform: translateY(0); }
    .submit:focus-visible { outline: 3px solid rgba(153,167,255,.38); outline-offset: 3px; }
    .submit:disabled { opacity: .48; cursor: not-allowed; box-shadow: none; }
    .alert { margin: 0 0 18px; border: 1px solid #fecdd3; border-radius: var(--radius-lg); padding: 11px 13px; background: #fff4f5; color: #b42336; font-size: 13px; }
    code { border-radius: var(--radius-sm); background: #edf0ff; color: #4054c7; padding: 1px 5px; font: 12px/1.5 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
    @media (prefers-reduced-motion: reduce) {
      .connection-state::before { animation: none; }
    }
    @media (max-width: 520px) {
      body { justify-content: center; padding: max(16px, env(safe-area-inset-top)) 14px max(18px, env(safe-area-inset-bottom)); }
      body::before { opacity: .18; }
      .remote-access-card { width: 100%%; border-radius: var(--radius-xl); padding: 25px 20px; }
      .brand-row { margin-bottom: 23px; }
      .brand-mark { width: 34px; height: 34px; flex-basis: 34px; }
      .connection-state { font-size: 10px; }
      h1 { font-size: 30px; }
    }
  </style>
</head>
<body>
  <main class="remote-access-shell remote-access-card" aria-labelledby="remoteAccessTitle">
    <div class="card-content">
      <div class="brand-row">
        <span class="brand-identity"><span class="brand-mark" aria-hidden="true"><svg viewBox="0 0 32 32"><circle cx="16" cy="16" r="12.5"></circle><path d="M10.5 17.5c1.6 2 3.4 3 5.5 3s3.9-1 5.5-3"></path><path d="M11.5 12.5h.01M20.5 12.5h.01"></path></svg></span><span class="brand-name">AUTOTO</span></span>
        <span class="connection-state">%s</span>
      </div>
      <header>
        <h1 id="remoteAccessTitle">%s</h1>
      </header>
      %s
      <form method="post" action="/auth/remote-access" autocomplete="off">
        <label class="password-label" for="remoteAccessPassword">%s</label>
        <div class="password-field">

          <span class="password-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><rect x="5" y="10" width="14" height="10" rx="2"></rect><path d="M8 10V7a4 4 0 0 1 8 0v3"></path></svg></span>
          <input id="remoteAccessPassword" name="password" type="password" inputmode="text" autocomplete="current-password" placeholder="%s" aria-label="%s" autofocus %s />
        </div>
        %s
      </form>
    </div>
  </main>
</body>
</html>`, html.EscapeString(loginCopy.LanguageTag), html.EscapeString(loginCopy.PageTitle), html.EscapeString(loginCopy.ConnectionState), html.EscapeString(loginCopy.Heading), messageHTML, html.EscapeString(loginCopy.PasswordLabel), html.EscapeString(loginCopy.PasswordPlaceholder), html.EscapeString(loginCopy.PasswordLabel), disabledAttr(!passwordConfigured), formHTML)
}

func disabledAttr(disabled bool) string {
	if disabled {
		return "disabled"
	}
	return ""
}

func trustedLoopbackPeer(r *http.Request) bool {
	return isLoopbackHost(r.RemoteAddr)
}

func requestHasRemoteForwardingHeaders(r *http.Request) bool {
	if firstHeaderClientIP(r.Header.Values("CF-Connecting-IP")) != "" || firstHeaderClientIP(r.Header.Values("True-Client-IP")) != "" || firstHeaderClientIP(r.Header.Values("X-Real-IP")) != "" || strings.TrimSpace(strings.Join(r.Header.Values("Cf-Ray"), ",")) != "" {
		return true
	}
	if forwarded := strings.TrimSpace(strings.Join(r.Header.Values("Forwarded"), ",")); forwarded != "" && forwardedHeaderLooksRemote(forwarded) {
		return true
	}
	if host := strings.TrimSpace(strings.Join(r.Header.Values("X-Forwarded-Host"), ",")); host != "" && anyForwardedHostRemote(host) {
		return true
	}
	if forwardedFor := strings.TrimSpace(strings.Join(r.Header.Values("X-Forwarded-For"), ",")); forwardedFor != "" && anyForwardedForRemote(forwardedFor) {
		return true
	}
	// A loopback peer that supplies an explicit forwarded scheme is acting as a
	// proxy boundary even when it omits client-IP metadata. Treat it as remote so
	// missing or partially appended forwarding metadata cannot regain local admin
	// authority.
	return hasForwardedSchemeHeader(r)
}

func anyForwardedHostRemote(value string) bool {
	for _, host := range splitCommaList(value) {
		if host != "" && !isLoopbackHost(host) {
			return true
		}
	}
	return false
}

func anyForwardedForRemote(value string) bool {
	for _, item := range splitCommaList(value) {
		item = strings.Trim(strings.TrimSpace(item), "[]\"")
		if item == "" || strings.EqualFold(item, "unknown") {
			continue
		}
		if host, _, err := net.SplitHostPort(item); err == nil {
			item = host
		}
		ip := net.ParseIP(strings.Trim(item, "[]"))
		if ip == nil || !ip.IsLoopback() {
			return true
		}
	}
	return false
}

func forwardedHeaderLooksRemote(value string) bool {
	for _, part := range splitCommaList(value) {
		params := strings.Split(part, ";")
		for _, param := range params {
			key, raw, ok := strings.Cut(strings.TrimSpace(param), "=")
			if !ok {
				continue
			}
			key = strings.ToLower(strings.TrimSpace(key))
			raw = strings.Trim(strings.TrimSpace(raw), "\"")
			if key == "host" && raw != "" && !isLoopbackHost(raw) {
				return true
			}
			if key == "for" && raw != "" && anyForwardedForRemote(raw) {
				return true
			}
		}
	}
	return false
}

func splitCommaList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, strings.TrimSpace(part))
	}
	return out
}

func isLoopbackHost(value string) bool {
	host := hostOnly(value)
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func hostOnly(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		return strings.Trim(strings.ToLower(host), "[]")
	}
	if strings.Count(value, ":") > 1 {
		return strings.Trim(strings.ToLower(value), "[]")
	}
	if host, _, ok := strings.Cut(value, ":"); ok {
		return strings.Trim(strings.ToLower(host), "[]")
	}
	return strings.Trim(strings.ToLower(value), "[]")
}

func (s *Server) safeDefaultPermissionModeForRequest(r *http.Request, mode string) string {
	mode = strings.TrimSpace(mode)
	if !validPermissionMode(mode) {
		mode = "acceptEdits"
	}
	if s.capabilitiesForRequest(r).MaxPermissionMode != "bypassPermissions" && mode == "bypassPermissions" {
		return "acceptEdits"
	}
	return mode
}

func (s *Server) permissionModeAllowedForRequest(r *http.Request, mode string) (string, bool, string) {
	mode = strings.TrimSpace(mode)
	if !validPermissionMode(mode) {
		return "", false, "invalid permissionMode"
	}
	if s.capabilitiesForRequest(r).MaxPermissionMode != "bypassPermissions" && mode == "bypassPermissions" {
		return "", false, "bypassPermissions is disabled for restricted remote sessions; use a local request or full remote session"
	}
	return mode, true, ""
}

// enforceRemotePermissionCap protects legacy direct-execution paths that cannot
// carry a Run permission cap. New message submissions use a durable per-Run cap
// instead and therefore leave the Agent's configured mode untouched.
func (s *Server) enforceRemotePermissionCap(r *http.Request, agentID string) error {
	if s.capabilitiesForRequest(r).MaxPermissionMode == "bypassPermissions" || s.store == nil {
		return nil
	}
	agent, err := s.store.GetAgent(r.Context(), agentID)
	if err != nil {
		return err
	}
	if agent.PermissionMode == "bypassPermissions" {
		return errors.New("restricted remote request cannot directly execute an agent configured for bypassPermissions")
	}
	return nil
}
