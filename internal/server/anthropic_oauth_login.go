package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"autoto/internal/anthropicauth"
)

const (
	anthropicOAuthLoginTTL      = 10 * time.Minute
	anthropicOAuthLoginIDPrefix = "anthropic_login_"
	anthropicOAuthLoginMaxCode  = 4096
)

type anthropicOAuthLoginStatus string

const (
	anthropicOAuthLoginPending   anthropicOAuthLoginStatus = "pending"
	anthropicOAuthLoginCompleted anthropicOAuthLoginStatus = "completed"
	anthropicOAuthLoginFailed    anthropicOAuthLoginStatus = "failed"
	anthropicOAuthLoginCancelled anthropicOAuthLoginStatus = "cancelled"
	anthropicOAuthLoginExpired   anthropicOAuthLoginStatus = "expired"
)

type anthropicOAuthLoginSession struct {
	loginID      string
	state        string
	verifier     string
	authURL      string
	status       anthropicOAuthLoginStatus
	expiresAt    time.Time
	errorMessage string
	account      *anthropicauth.AccountSummary
}

type anthropicOAuthLoginResponse struct {
	LoginID   string                        `json:"loginId"`
	AuthURL   string                        `json:"authUrl,omitempty"`
	ExpiresAt string                        `json:"expiresAt"`
	Status    anthropicOAuthLoginStatus     `json:"status"`
	Error     string                        `json:"error,omitempty"`
	Account   *anthropicauth.AccountSummary `json:"account,omitempty"`
}

type anthropicOAuthSubmitRequest struct {
	Code string `json:"code"`
}

// rejectRemoteAnthropicOAuthLogin blocks OAuth login from remote sessions; it is
// a local-only browser flow, mirroring the Codex restriction.
func (s *Server) rejectRemoteAnthropicOAuthLogin(w http.ResponseWriter, r *http.Request) bool {
	if s.remoteAccessAuthentication(r).Remote {
		writeError(w, http.StatusForbidden, "Anthropic 帳號登入只能在本機發起和管理")
		return true
	}
	return false
}

func (s *Server) startAnthropicOAuthLogin(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if s.rejectRemoteAnthropicOAuthLogin(w, r) {
		return
	}

	state, err := anthropicauth.NewOAuthState()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "無法安全啟動 Anthropic 帳號登入")
		return
	}
	pkce, err := anthropicauth.NewPKCE()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "無法安全啟動 Anthropic 帳號登入")
		return
	}
	authURL, err := anthropicauth.BuildAuthorizeURL(state, pkce.Challenge)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "無法建立 Anthropic 授權連結")
		return
	}
	loginID, err := anthropicauth.NewOAuthState()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "無法安全啟動 Anthropic 帳號登入")
		return
	}

	session := &anthropicOAuthLoginSession{
		loginID:   anthropicOAuthLoginIDPrefix + loginID,
		state:     state,
		verifier:  pkce.Verifier,
		authURL:   authURL,
		status:    anthropicOAuthLoginPending,
		expiresAt: s.now().Add(anthropicOAuthLoginTTL),
	}

	s.anthropicOAuthMu.Lock()
	s.expireAnthropicOAuthLoginsLocked(s.now())
	if s.anthropicOAuthLogins == nil {
		s.anthropicOAuthLogins = map[string]*anthropicOAuthLoginSession{}
	}
	s.anthropicOAuthLogins[session.loginID] = session
	response := anthropicOAuthLoginPublicResponse(session, true)
	s.anthropicOAuthMu.Unlock()

	writeJSON(w, http.StatusOK, response)
}

func (s *Server) getAnthropicOAuthLogin(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if s.rejectRemoteAnthropicOAuthLogin(w, r) {
		return
	}
	loginID := strings.TrimSpace(chi.URLParam(r, "loginId"))

	s.anthropicOAuthMu.Lock()
	s.expireAnthropicOAuthLoginsLocked(s.now())
	session := s.anthropicOAuthLogins[loginID]
	if session == nil {
		s.anthropicOAuthMu.Unlock()
		writeError(w, http.StatusNotFound, "Anthropic 帳號登入不存在或已過期")
		return
	}
	response := anthropicOAuthLoginPublicResponse(session, false)
	s.anthropicOAuthMu.Unlock()

	writeJSON(w, http.StatusOK, response)
}

func (s *Server) submitAnthropicOAuthLogin(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if s.rejectRemoteAnthropicOAuthLogin(w, r) {
		return
	}
	defer r.Body.Close()
	loginID := strings.TrimSpace(chi.URLParam(r, "loginId"))

	var request anthropicOAuthSubmitRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, anthropicOAuthLoginMaxCode))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "登入碼格式無效")
		return
	}
	code, pastedState := anthropicauth.ParseAuthorizationCode(request.Code)
	if code == "" {
		writeError(w, http.StatusBadRequest, "請貼上授權碼")
		return
	}

	s.anthropicOAuthMu.Lock()
	s.expireAnthropicOAuthLoginsLocked(s.now())
	session := s.anthropicOAuthLogins[loginID]
	if session == nil {
		s.anthropicOAuthMu.Unlock()
		writeError(w, http.StatusNotFound, "Anthropic 帳號登入不存在或已過期")
		return
	}
	if session.status != anthropicOAuthLoginPending {
		response := anthropicOAuthLoginPublicResponse(session, false)
		s.anthropicOAuthMu.Unlock()
		writeJSON(w, http.StatusOK, response)
		return
	}
	verifier := session.verifier
	expectedState := session.state
	s.anthropicOAuthMu.Unlock()

	if pastedState != "" && pastedState != expectedState {
		s.failAnthropicOAuthLogin(loginID, "授權碼與本次登入不符，請重新登入")
		writeError(w, http.StatusBadRequest, "授權碼與本次登入不符，請重新登入")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	tokens, err := anthropicauth.ExchangeAuthorizationCode(ctx, code, verifier, expectedState, nil)
	if err != nil {
		s.failAnthropicOAuthLogin(loginID, "授權碼交換失敗，請重新登入")
		writeError(w, http.StatusBadGateway, "授權碼交換失敗，請重新登入")
		return
	}

	store, err := s.nativeAnthropicCredentialStore()
	if err != nil {
		s.failAnthropicOAuthLogin(loginID, err.Error())
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	expiresAt := ""
	if tokens.ExpiresIn > 0 {
		expiresAt = s.now().Add(time.Duration(tokens.ExpiresIn) * time.Second).UTC().Format(time.RFC3339Nano)
	}
	item, err := store.Create(anthropicauth.CreateRequest{
		AuthType:       anthropicauth.AuthTypeOAuth,
		OAuthAccess:    tokens.AccessToken,
		OAuthRefresh:   tokens.RefreshToken,
		OAuthExpiresAt: expiresAt,
	})
	if err != nil {
		s.failAnthropicOAuthLogin(loginID, "儲存 Anthropic 帳號失敗")
		writeError(w, http.StatusInternalServerError, "儲存 Anthropic 帳號失敗")
		return
	}
	if err := s.ensureNativeAnthropicProvider(); err != nil {
		s.failAnthropicOAuthLogin(loginID, err.Error())
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	summary := anthropicauth.Summary(item)

	s.anthropicOAuthMu.Lock()
	if current := s.anthropicOAuthLogins[loginID]; current != nil {
		current.status = anthropicOAuthLoginCompleted
		current.account = &summary
		current.verifier = ""
	}
	response := anthropicOAuthLoginPublicResponse(s.anthropicOAuthLogins[loginID], false)
	s.anthropicOAuthMu.Unlock()

	writeJSON(w, http.StatusOK, response)
}

func (s *Server) cancelAnthropicOAuthLogin(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if s.rejectRemoteAnthropicOAuthLogin(w, r) {
		return
	}
	loginID := strings.TrimSpace(chi.URLParam(r, "loginId"))

	s.anthropicOAuthMu.Lock()
	if session := s.anthropicOAuthLogins[loginID]; session != nil && session.status == anthropicOAuthLoginPending {
		session.status = anthropicOAuthLoginCancelled
		session.verifier = ""
	}
	delete(s.anthropicOAuthLogins, loginID)
	s.anthropicOAuthMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"status": anthropicOAuthLoginCancelled})
}

func (s *Server) failAnthropicOAuthLogin(loginID, message string) {
	s.anthropicOAuthMu.Lock()
	defer s.anthropicOAuthMu.Unlock()
	if session := s.anthropicOAuthLogins[loginID]; session != nil {
		session.status = anthropicOAuthLoginFailed
		session.errorMessage = message
		session.verifier = ""
	}
}

func (s *Server) expireAnthropicOAuthLoginsLocked(now time.Time) {
	for id, session := range s.anthropicOAuthLogins {
		if session == nil {
			delete(s.anthropicOAuthLogins, id)
			continue
		}
		if session.status == anthropicOAuthLoginPending && now.After(session.expiresAt) {
			session.status = anthropicOAuthLoginExpired
			session.verifier = ""
		}
		// Drop terminal sessions that clients no longer need to poll.
		if session.status == anthropicOAuthLoginExpired && now.After(session.expiresAt.Add(anthropicOAuthLoginTTL)) {
			delete(s.anthropicOAuthLogins, id)
		}
	}
}

func anthropicOAuthLoginPublicResponse(session *anthropicOAuthLoginSession, includeAuthURL bool) anthropicOAuthLoginResponse {
	if session == nil {
		return anthropicOAuthLoginResponse{Status: anthropicOAuthLoginExpired}
	}
	response := anthropicOAuthLoginResponse{
		LoginID:   session.loginID,
		ExpiresAt: session.expiresAt.UTC().Format(time.RFC3339Nano),
		Status:    session.status,
		Error:     session.errorMessage,
		Account:   session.account,
	}
	if includeAuthURL || session.status == anthropicOAuthLoginPending {
		response.AuthURL = session.authURL
	}
	return response
}
