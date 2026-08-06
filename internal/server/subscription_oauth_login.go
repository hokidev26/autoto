package server

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"autoto/internal/config"
	"autoto/internal/geminiauth"
	"autoto/internal/grokauth"
	"autoto/internal/kimiauth"
	"autoto/internal/kiroauth"
	"autoto/internal/subscriptionauth"
)

const (
	subscriptionOAuthLoginTTL        = 10 * time.Minute
	subscriptionOAuthTerminalTTL     = 10 * time.Minute
	geminiOAuthLoginTTL              = 5 * time.Minute
	geminiOAuthCallbackPort          = 51121
	geminiOAuthCallbackPath          = "/oauth-callback"
	subscriptionOAuthCallbackMaxWait = 5 * time.Second
	subscriptionOAuthCallbackCSP     = "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'"
)

type geminiOAuthLoginClient interface {
	BuildAuthURL(state, redirectURI string) (string, error)
	ExchangeCode(ctx context.Context, code, redirectURI string) (*geminiauth.TokenData, error)
	FetchUserInfo(ctx context.Context, accessToken string) (geminiauth.UserInfo, error)
	FetchProjectID(ctx context.Context, accessToken string) (string, error)
}

type grokOAuthLoginClient interface {
	StartDeviceFlow(ctx context.Context) (*grokauth.DeviceCodeResponse, error)
	Wait(ctx context.Context, device *grokauth.DeviceCodeResponse) (*grokauth.TokenData, error)
}

type kimiOAuthLoginClient interface {
	StartDeviceFlow(ctx context.Context) (*kimiauth.DeviceCodeResponse, error)
	Wait(ctx context.Context, device *kimiauth.DeviceCodeResponse) (*kimiauth.AuthBundle, error)
}

type kiroOAuthLoginClient interface {
	RefreshToken(ctx context.Context, refreshToken, region string) (*kiroauth.TokenData, error)
}

type subscriptionOAuthLoginTestConfig struct {
	GeminiListenAddress string
	SessionTTL          time.Duration
}

type subscriptionOAuthLoginStatus string

const (
	subscriptionOAuthLoginPending    subscriptionOAuthLoginStatus = "pending"
	subscriptionOAuthLoginExchanging subscriptionOAuthLoginStatus = "exchanging"
	subscriptionOAuthLoginCompleted  subscriptionOAuthLoginStatus = "completed"
	subscriptionOAuthLoginFailed     subscriptionOAuthLoginStatus = "failed"
	subscriptionOAuthLoginCancelled  subscriptionOAuthLoginStatus = "cancelled"
	subscriptionOAuthLoginExpired    subscriptionOAuthLoginStatus = "expired"
)

type subscriptionOAuthLoginSession struct {
	loginID         string
	provider        string
	state           string
	authURL         string
	userCode        string
	verificationURI string
	redirectURI     string
	status          subscriptionOAuthLoginStatus
	// locale is captured when the login starts so the server-rendered callback
	// page matches the language selected in Autoto, not the browser's.
	locale       remoteLoginLocale
	expiresAt    time.Time
	errorMessage string
	account      *subscriptionauth.AccountSummary
	listeners    []net.Listener
	callbackPort int
	server       *http.Server
	ctx          context.Context
	cancel       context.CancelFunc
}

type subscriptionOAuthLoginResponse struct {
	LoginID         string                           `json:"loginId"`
	Provider        string                           `json:"provider"`
	AuthURL         string                           `json:"authUrl,omitempty"`
	UserCode        string                           `json:"userCode,omitempty"`
	VerificationURI string                           `json:"verificationUri,omitempty"`
	ExpiresAt       string                           `json:"expiresAt"`
	Status          subscriptionOAuthLoginStatus     `json:"status"`
	Error           string                           `json:"error,omitempty"`
	Account         *subscriptionauth.AccountSummary `json:"account,omitempty"`
}

type subscriptionOAuthTokens struct {
	AccessToken   string
	RefreshToken  string
	IDToken       string
	TokenType     string
	ExpiresAt     string
	Email         string
	Subject       string
	Scope         string
	ProjectID     string
	DeviceID      string
	TokenEndpoint string
}

func (s *Server) startSubscriptionOAuthLogin(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	provider, ok := subscriptionOAuthProvider(chi.URLParam(r, "provider"))
	if !ok {
		writeError(w, http.StatusNotFound, "订阅 Provider 不支持 OAuth 登录")
		return
	}
	if s.rejectRemoteSubscriptionOAuthLogin(w, r) {
		return
	}

	s.subscriptionOAuthMu.Lock()
	s.expireSubscriptionOAuthLoginsLocked(s.now())
	if active := s.activeSubscriptionOAuthLoginLocked(provider); active != nil {
		response := subscriptionOAuthLoginPublicResponse(active, true)
		s.subscriptionOAuthMu.Unlock()
		writeJSON(w, http.StatusOK, response)
		return
	}
	s.subscriptionOAuthMu.Unlock()

	var (
		session  *subscriptionOAuthLoginSession
		activate func()
		err      error
	)
	switch provider {
	case subscriptionauth.ProviderGemini:
		session, activate, err = s.prepareGeminiOAuthLogin()
	case subscriptionauth.ProviderGrok:
		session, activate, err = s.prepareGrokOAuthLogin(r.Context())
	case subscriptionauth.ProviderKimi:
		session, activate, err = s.prepareKimiOAuthLogin(r.Context())
	case subscriptionauth.ProviderKiro:
		session, activate, err = s.prepareKiroOAuthLogin()
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, subscriptionOAuthStartError(provider))
		return
	}
	// Recorded before activate() starts the callback listener, so the callback
	// page always renders in the language the Autoto UI is using rather than in
	// whatever the redirected browser happens to advertise.
	session.locale = subscriptionCallbackLocale(r.URL.Query().Get("locale"), r.Header.Get("Accept-Language"))

	s.subscriptionOAuthMu.Lock()
	s.expireSubscriptionOAuthLoginsLocked(s.now())
	if active := s.activeSubscriptionOAuthLoginLocked(provider); active != nil {
		response := subscriptionOAuthLoginPublicResponse(active, true)
		s.subscriptionOAuthMu.Unlock()
		discardSubscriptionOAuthLogin(session)
		writeJSON(w, http.StatusOK, response)
		return
	}
	if s.subscriptionOAuthLogins == nil {
		s.subscriptionOAuthLogins = make(map[string]*subscriptionOAuthLoginSession)
	}
	s.subscriptionOAuthLogins[session.loginID] = session
	response := subscriptionOAuthLoginPublicResponse(session, true)
	s.subscriptionOAuthMu.Unlock()

	activate()
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) getSubscriptionOAuthLogin(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	provider, ok := subscriptionOAuthProvider(chi.URLParam(r, "provider"))
	if !ok {
		writeError(w, http.StatusNotFound, "订阅 Provider 不支持 OAuth 登录")
		return
	}
	if s.rejectRemoteSubscriptionOAuthLogin(w, r) {
		return
	}
	loginID := strings.TrimSpace(chi.URLParam(r, "loginId"))

	s.subscriptionOAuthMu.Lock()
	defer s.subscriptionOAuthMu.Unlock()
	s.expireSubscriptionOAuthLoginsLocked(s.now())
	session := s.subscriptionOAuthLogins[loginID]
	if session == nil || session.provider != provider {
		writeError(w, http.StatusNotFound, "订阅 OAuth 登录会话不存在或已过期")
		return
	}
	writeJSON(w, http.StatusOK, subscriptionOAuthLoginPublicResponse(session, session.status == subscriptionOAuthLoginPending))
}

func (s *Server) cancelSubscriptionOAuthLogin(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	provider, ok := subscriptionOAuthProvider(chi.URLParam(r, "provider"))
	if !ok {
		writeError(w, http.StatusNotFound, "订阅 Provider 不支持 OAuth 登录")
		return
	}
	if s.rejectRemoteSubscriptionOAuthLogin(w, r) {
		return
	}
	loginID := strings.TrimSpace(chi.URLParam(r, "loginId"))

	s.subscriptionOAuthMu.Lock()
	defer s.subscriptionOAuthMu.Unlock()
	s.expireSubscriptionOAuthLoginsLocked(s.now())
	session := s.subscriptionOAuthLogins[loginID]
	if session == nil || session.provider != provider {
		writeError(w, http.StatusNotFound, "订阅 OAuth 登录会话不存在或已过期")
		return
	}
	if subscriptionOAuthLoginActive(session.status) {
		s.finishSubscriptionOAuthLoginLocked(session, subscriptionOAuthLoginCancelled, "", nil)
	}
	writeJSON(w, http.StatusOK, subscriptionOAuthLoginPublicResponse(session, false))
}

func (s *Server) rejectRemoteSubscriptionOAuthLogin(w http.ResponseWriter, r *http.Request) bool {
	if s.remoteAccessAuthentication(r).Remote {
		writeError(w, http.StatusForbidden, "订阅 OAuth 登录只能在本机发起和管理")
		return true
	}
	return false
}

func (s *Server) prepareGeminiOAuthLogin() (*subscriptionOAuthLoginSession, func(), error) {
	loginRandom, err := geminiauth.GenerateState()
	if err != nil {
		return nil, nil, err
	}
	state, err := geminiauth.GenerateState()
	if err != nil {
		return nil, nil, err
	}
	addresses := []string{
		net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", geminiOAuthCallbackPort)),
		net.JoinHostPort("127.0.0.1", "0"),
	}
	if testConfig := s.subscriptionOAuthTestConfig; testConfig != nil && strings.TrimSpace(testConfig.GeminiListenAddress) != "" {
		if err := validateCodexOAuthListenAddress(testConfig.GeminiListenAddress); err != nil {
			return nil, nil, err
		}
		addresses = []string{strings.TrimSpace(testConfig.GeminiListenAddress)}
	}
	listeners, port, err := listenCodexOAuthCallback(addresses)
	if err != nil {
		return nil, nil, err
	}
	redirectURI := fmt.Sprintf("http://localhost:%d%s", port, geminiOAuthCallbackPath)
	client := s.newGeminiOAuthLoginClient()
	authURL, err := client.BuildAuthURL(state, redirectURI)
	if err != nil {
		closeCodexOAuthCallbackListeners(listeners)
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	now := s.now().UTC()
	session := &subscriptionOAuthLoginSession{
		loginID:      subscriptionauth.ProviderGemini + "_login_" + loginRandom,
		provider:     subscriptionauth.ProviderGemini,
		state:        state,
		authURL:      authURL,
		redirectURI:  redirectURI,
		status:       subscriptionOAuthLoginPending,
		expiresAt:    now.Add(s.subscriptionOAuthTTL(subscriptionauth.ProviderGemini, 0)),
		listeners:    listeners,
		callbackPort: port,
		ctx:          ctx,
		cancel:       cancel,
	}
	mux := http.NewServeMux()
	mux.HandleFunc(geminiOAuthCallbackPath, func(callbackWriter http.ResponseWriter, callbackRequest *http.Request) {
		s.handleGeminiOAuthCallback(session, client, callbackWriter, callbackRequest)
	})
	session.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 15 * time.Second}
	activate := func() {
		for _, listener := range session.listeners {
			go s.serveSubscriptionOAuthCallback(session, listener)
		}
		go s.expireSubscriptionOAuthLoginAfter(session)
	}
	return session, activate, nil
}

func (s *Server) prepareGrokOAuthLogin(ctx context.Context) (*subscriptionOAuthLoginSession, func(), error) {
	client := s.newGrokOAuthLoginClient()
	device, err := client.StartDeviceFlow(ctx)
	if err != nil {
		return nil, nil, err
	}
	loginRandom, err := geminiauth.GenerateState()
	if err != nil {
		return nil, nil, err
	}
	sessionCtx, cancel := context.WithCancel(context.Background())
	now := s.now().UTC()
	authURL := strings.TrimSpace(device.VerificationURIComplete)
	if authURL == "" {
		authURL = strings.TrimSpace(device.VerificationURI)
	}
	session := &subscriptionOAuthLoginSession{
		loginID:         subscriptionauth.ProviderGrok + "_login_" + loginRandom,
		provider:        subscriptionauth.ProviderGrok,
		authURL:         authURL,
		userCode:        strings.TrimSpace(device.UserCode),
		verificationURI: strings.TrimSpace(device.VerificationURI),
		status:          subscriptionOAuthLoginPending,
		expiresAt:       now.Add(s.subscriptionOAuthTTL(subscriptionauth.ProviderGrok, device.ExpiresIn)),
		ctx:             sessionCtx,
		cancel:          cancel,
	}
	activate := func() {
		go s.completeGrokOAuthLogin(session, client, device)
		go s.expireSubscriptionOAuthLoginAfter(session)
	}
	return session, activate, nil
}

func (s *Server) prepareKimiOAuthLogin(ctx context.Context) (*subscriptionOAuthLoginSession, func(), error) {
	client := s.newKimiOAuthLoginClient()
	device, err := client.StartDeviceFlow(ctx)
	if err != nil {
		return nil, nil, err
	}
	loginRandom, err := geminiauth.GenerateState()
	if err != nil {
		return nil, nil, err
	}
	sessionCtx, cancel := context.WithCancel(context.Background())
	now := s.now().UTC()
	authURL := strings.TrimSpace(device.VerificationURIComplete)
	if authURL == "" {
		authURL = strings.TrimSpace(device.VerificationURI)
	}
	session := &subscriptionOAuthLoginSession{
		loginID:         subscriptionauth.ProviderKimi + "_login_" + loginRandom,
		provider:        subscriptionauth.ProviderKimi,
		authURL:         authURL,
		userCode:        strings.TrimSpace(device.UserCode),
		verificationURI: strings.TrimSpace(device.VerificationURI),
		status:          subscriptionOAuthLoginPending,
		expiresAt:       now.Add(s.subscriptionOAuthTTL(subscriptionauth.ProviderKimi, device.ExpiresIn)),
		ctx:             sessionCtx,
		cancel:          cancel,
	}
	activate := func() {
		go s.completeKimiOAuthLogin(session, client, device)
		go s.expireSubscriptionOAuthLoginAfter(session)
	}
	return session, activate, nil
}

func (session *subscriptionOAuthLoginSession) callbackLocale() remoteLoginLocale {
	if session == nil || session.locale == "" {
		return remoteLoginLocaleChineseSimplified
	}
	return session.locale
}

func (s *Server) handleGeminiOAuthCallback(session *subscriptionOAuthLoginSession, client geminiOAuthLoginClient, w http.ResponseWriter, r *http.Request) {
	locale := session.callbackLocale()
	providerLabel := subscriptionProviderLabel(subscriptionauth.ProviderGemini)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeSubscriptionOAuthCallbackHTML(w, http.StatusMethodNotAllowed, locale, subscriptionCallbackMethodNotAllowed, providerLabel, "")
		return
	}
	if !validCodexOAuthCallbackHost(r.Host, session.callbackPort) {
		writeSubscriptionOAuthCallbackHTML(w, http.StatusBadRequest, locale, subscriptionCallbackInvalidHost, providerLabel, "")
		return
	}

	state := strings.TrimSpace(r.URL.Query().Get("state"))
	s.subscriptionOAuthMu.Lock()
	s.expireSubscriptionOAuthLoginsLocked(s.now())
	if s.subscriptionOAuthLogins[session.loginID] != session {
		s.subscriptionOAuthMu.Unlock()
		writeSubscriptionOAuthCallbackHTML(w, http.StatusGone, locale, subscriptionCallbackSessionEnded, providerLabel, "")
		return
	}
	if session.status != subscriptionOAuthLoginPending {
		status := session.status
		s.subscriptionOAuthMu.Unlock()
		writeSubscriptionOAuthCallbackStatusHTML(w, locale, subscriptionauth.ProviderGemini, status)
		return
	}
	if !constantTimeEqualToken(state, session.state) {
		s.subscriptionOAuthMu.Unlock()
		writeSubscriptionOAuthCallbackHTML(w, http.StatusBadRequest, locale, subscriptionCallbackStateInvalid, providerLabel, "")
		return
	}
	if oauthError := safeCodexOAuthErrorCode(r.URL.Query().Get("error")); oauthError != "" {
		message := "Gemini 授权被拒绝"
		s.finishSubscriptionOAuthLoginLocked(session, subscriptionOAuthLoginFailed, message, nil)
		s.subscriptionOAuthMu.Unlock()
		writeSubscriptionOAuthCallbackHTML(w, http.StatusBadRequest, locale, subscriptionCallbackProviderDenied, providerLabel, message+"。")
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" || len(code) > 16<<10 {
		s.finishSubscriptionOAuthLoginLocked(session, subscriptionOAuthLoginFailed, "Gemini OAuth 登录失败，请重新开始", nil)
		s.subscriptionOAuthMu.Unlock()
		writeSubscriptionOAuthCallbackHTML(w, http.StatusBadRequest, locale, subscriptionCallbackMissingCode, providerLabel, "")
		return
	}
	session.status = subscriptionOAuthLoginExchanging
	redirectURI := session.redirectURI
	s.subscriptionOAuthMu.Unlock()

	tokens, err := client.ExchangeCode(session.ctx, code, redirectURI)
	if err != nil {
		s.failSubscriptionOAuthLogin(session, "Gemini OAuth token 交换失败，请重新开始")
		writeSubscriptionOAuthCallbackStatusHTML(w, locale, subscriptionauth.ProviderGemini, subscriptionOAuthLoginFailed)
		return
	}
	info, err := client.FetchUserInfo(session.ctx, tokens.AccessToken)
	if err != nil {
		s.failSubscriptionOAuthLogin(session, "Gemini 账号信息读取失败，请重新开始")
		writeSubscriptionOAuthCallbackStatusHTML(w, locale, subscriptionauth.ProviderGemini, subscriptionOAuthLoginFailed)
		return
	}
	projectID, err := client.FetchProjectID(session.ctx, tokens.AccessToken)
	if err != nil || strings.TrimSpace(projectID) == "" {
		s.failSubscriptionOAuthLogin(session, "Gemini Cloud Code 项目初始化失败，请重新开始")
		writeSubscriptionOAuthCallbackStatusHTML(w, locale, subscriptionauth.ProviderGemini, subscriptionOAuthLoginFailed)
		return
	}
	account, err := s.saveSubscriptionOAuthCredential(subscriptionauth.ProviderGemini, subscriptionOAuthTokens{
		AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, IDToken: tokens.IDToken,
		TokenType: tokens.TokenType, ExpiresAt: tokens.ExpiresAt, Email: info.Email,
		Subject: info.Subject, ProjectID: projectID,
	})
	if err != nil {
		s.failSubscriptionOAuthLogin(session, "Gemini 凭据无法安全保存，请重试")
		writeSubscriptionOAuthCallbackHTML(w, http.StatusInternalServerError, locale, subscriptionCallbackStoreFailed, providerLabel, "")
		return
	}

	s.subscriptionOAuthMu.Lock()
	if s.subscriptionOAuthLogins[session.loginID] == session && session.status == subscriptionOAuthLoginExchanging {
		s.finishSubscriptionOAuthLoginLocked(session, subscriptionOAuthLoginCompleted, "", account)
	}
	status := session.status
	s.subscriptionOAuthMu.Unlock()
	writeSubscriptionOAuthCallbackStatusHTML(w, locale, subscriptionauth.ProviderGemini, status)
}

func (s *Server) completeGrokOAuthLogin(session *subscriptionOAuthLoginSession, client grokOAuthLoginClient, device *grokauth.DeviceCodeResponse) {
	tokens, err := client.Wait(session.ctx, device)
	if err != nil {
		s.failSubscriptionOAuthLogin(session, "Grok 设备授权失败，请重新开始")
		return
	}
	if !s.beginSubscriptionOAuthExchange(session) {
		return
	}
	account, err := s.saveSubscriptionOAuthCredential(subscriptionauth.ProviderGrok, subscriptionOAuthTokens{
		AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, IDToken: tokens.IDToken,
		TokenType: tokens.TokenType, ExpiresAt: tokens.ExpiresAt, Email: tokens.Email,
		Subject: tokens.Subject, TokenEndpoint: device.TokenEndpoint,
	})
	if err != nil {
		s.failSubscriptionOAuthLogin(session, "Grok 凭据无法安全保存，请重试")
		return
	}
	s.completeSubscriptionOAuthLogin(session, account)
}

func (s *Server) completeKimiOAuthLogin(session *subscriptionOAuthLoginSession, client kimiOAuthLoginClient, device *kimiauth.DeviceCodeResponse) {
	bundle, err := client.Wait(session.ctx, device)
	if err != nil {
		s.failSubscriptionOAuthLogin(session, "Kimi 设备授权失败，请重新开始")
		return
	}
	if !s.beginSubscriptionOAuthExchange(session) {
		return
	}
	tokens := bundle.TokenData
	deviceID := strings.TrimSpace(bundle.DeviceID)
	if deviceID == "" {
		deviceID = strings.TrimSpace(tokens.DeviceID)
	}
	if deviceID == "" {
		deviceID = strings.TrimSpace(device.DeviceID)
	}
	account, err := s.saveSubscriptionOAuthCredential(subscriptionauth.ProviderKimi, subscriptionOAuthTokens{
		AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, TokenType: tokens.TokenType,
		ExpiresAt: tokens.ExpiresAt, Scope: tokens.Scope, DeviceID: deviceID,
	})
	if err != nil {
		s.failSubscriptionOAuthLogin(session, "Kimi 凭据无法安全保存，请重试")
		return
	}
	s.completeSubscriptionOAuthLogin(session, account)
}

func (s *Server) beginSubscriptionOAuthExchange(session *subscriptionOAuthLoginSession) bool {
	s.subscriptionOAuthMu.Lock()
	defer s.subscriptionOAuthMu.Unlock()
	if session == nil || s.subscriptionOAuthLogins[session.loginID] != session || session.status != subscriptionOAuthLoginPending {
		return false
	}
	session.status = subscriptionOAuthLoginExchanging
	return true
}

func (s *Server) completeSubscriptionOAuthLogin(session *subscriptionOAuthLoginSession, account *subscriptionauth.AccountSummary) {
	s.subscriptionOAuthMu.Lock()
	defer s.subscriptionOAuthMu.Unlock()
	if session != nil && s.subscriptionOAuthLogins[session.loginID] == session && session.status == subscriptionOAuthLoginExchanging {
		s.finishSubscriptionOAuthLoginLocked(session, subscriptionOAuthLoginCompleted, "", account)
	}
}

func (s *Server) failSubscriptionOAuthLogin(session *subscriptionOAuthLoginSession, message string) {
	s.subscriptionOAuthMu.Lock()
	defer s.subscriptionOAuthMu.Unlock()
	if session != nil && s.subscriptionOAuthLogins[session.loginID] == session && subscriptionOAuthLoginActive(session.status) {
		s.finishSubscriptionOAuthLoginLocked(session, subscriptionOAuthLoginFailed, message, nil)
	}
}

func (s *Server) saveSubscriptionOAuthCredential(provider string, tokens subscriptionOAuthTokens) (*subscriptionauth.AccountSummary, error) {
	// Reserve and validate the native provider name before persisting credentials.
	// This prevents a failed login from leaving orphaned secrets when the built-in
	// name is occupied by a different protocol.
	if err := s.ensureNativeSubscriptionProvider(provider); err != nil {
		return nil, err
	}
	store, err := s.nativeSubscriptionCredentialStore(provider)
	if err != nil {
		return nil, err
	}
	item, err := store.CreateOAuth(subscriptionauth.CreateRequest{
		Provider: provider, Priority: subscriptionauth.DefaultPriority,
		AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, IDToken: tokens.IDToken,
		TokenType: tokens.TokenType, ExpiresAt: tokens.ExpiresAt, Email: tokens.Email,
		Subject: tokens.Subject, Scope: tokens.Scope, ProjectID: tokens.ProjectID,
		DeviceID: tokens.DeviceID, TokenEndpoint: tokens.TokenEndpoint,
	})
	if err != nil {
		return nil, err
	}
	item, err = store.UpdateTokens(item.ID, subscriptionauth.TokenUpdate{
		AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, IDToken: tokens.IDToken,
		TokenType: tokens.TokenType, ExpiresAt: tokens.ExpiresAt, Email: tokens.Email,
		Subject: tokens.Subject, Scope: tokens.Scope, ProjectID: tokens.ProjectID,
		DeviceID: tokens.DeviceID, TokenEndpoint: tokens.TokenEndpoint,
	})
	if err != nil {
		return nil, err
	}
	summary := subscriptionauth.Summary(item)
	return &summary, nil
}

func (s *Server) activeSubscriptionOAuthLoginLocked(provider string) *subscriptionOAuthLoginSession {
	for _, session := range s.subscriptionOAuthLogins {
		if session != nil && session.provider == provider && subscriptionOAuthLoginActive(session.status) {
			return session
		}
	}
	return nil
}

func subscriptionOAuthLoginActive(status subscriptionOAuthLoginStatus) bool {
	return status == subscriptionOAuthLoginPending || status == subscriptionOAuthLoginExchanging
}

func (s *Server) expireSubscriptionOAuthLoginAfter(session *subscriptionOAuthLoginSession) {
	if session == nil {
		return
	}
	delay := time.Until(session.expiresAt)
	if delay < 0 {
		delay = 0
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		s.subscriptionOAuthMu.Lock()
		if s.subscriptionOAuthLogins[session.loginID] == session && subscriptionOAuthLoginActive(session.status) {
			s.finishSubscriptionOAuthLoginLocked(session, subscriptionOAuthLoginExpired, "", nil)
		}
		s.subscriptionOAuthMu.Unlock()
	case <-session.ctx.Done():
	}
}

func (s *Server) expireSubscriptionOAuthLoginsLocked(now time.Time) {
	for loginID, session := range s.subscriptionOAuthLogins {
		if session == nil {
			delete(s.subscriptionOAuthLogins, loginID)
			continue
		}
		if subscriptionOAuthLoginActive(session.status) && !now.Before(session.expiresAt) {
			s.finishSubscriptionOAuthLoginLocked(session, subscriptionOAuthLoginExpired, "", nil)
		}
		if !subscriptionOAuthLoginActive(session.status) && now.After(session.expiresAt.Add(subscriptionOAuthTerminalTTL)) {
			delete(s.subscriptionOAuthLogins, loginID)
		}
	}
}

func (s *Server) finishSubscriptionOAuthLoginLocked(session *subscriptionOAuthLoginSession, status subscriptionOAuthLoginStatus, message string, account *subscriptionauth.AccountSummary) {
	if session == nil {
		return
	}
	session.status = status
	session.errorMessage = message
	session.account = account
	session.state = ""
	session.authURL = ""
	session.userCode = ""
	session.verificationURI = ""
	session.redirectURI = ""
	if session.cancel != nil {
		session.cancel()
	}
	listeners := session.listeners
	session.listeners = nil
	if session.server != nil {
		go shutdownSubscriptionOAuthCallback(session.server, listeners)
	} else {
		closeCodexOAuthCallbackListeners(listeners)
	}
}

func discardSubscriptionOAuthLogin(session *subscriptionOAuthLoginSession) {
	if session == nil {
		return
	}
	if session.cancel != nil {
		session.cancel()
	}
	closeCodexOAuthCallbackListeners(session.listeners)
	session.listeners = nil
}

func shutdownSubscriptionOAuthCallback(server *http.Server, listeners []net.Listener) {
	ctx, cancel := context.WithTimeout(context.Background(), subscriptionOAuthCallbackMaxWait)
	defer cancel()
	_ = server.Shutdown(ctx)
	closeCodexOAuthCallbackListeners(listeners)
}

func (s *Server) serveSubscriptionOAuthCallback(session *subscriptionOAuthLoginSession, listener net.Listener) {
	err := session.server.Serve(listener)
	if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return
	}
	s.failSubscriptionOAuthLogin(session, "Gemini 本地 OAuth 回调服务异常，请重新开始")
}

func (s *Server) subscriptionOAuthTTL(provider string, expiresIn int) time.Duration {
	if testConfig := s.subscriptionOAuthTestConfig; testConfig != nil && testConfig.SessionTTL > 0 {
		return testConfig.SessionTTL
	}
	maximum := subscriptionOAuthLoginTTL
	switch provider {
	case subscriptionauth.ProviderGemini:
		maximum = geminiOAuthLoginTTL
	case subscriptionauth.ProviderGrok:
		maximum = grokauth.MaxWait
	case subscriptionauth.ProviderKimi:
		maximum = kimiauth.MaxWait
	}
	if expiresIn > 0 {
		fromServer := time.Duration(expiresIn) * time.Second
		if fromServer < maximum {
			return fromServer
		}
	}
	return maximum
}

func subscriptionOAuthLoginPublicResponse(session *subscriptionOAuthLoginSession, includeAuthorization bool) subscriptionOAuthLoginResponse {
	if session == nil {
		return subscriptionOAuthLoginResponse{Status: subscriptionOAuthLoginExpired}
	}
	response := subscriptionOAuthLoginResponse{
		LoginID: session.loginID, Provider: session.provider,
		ExpiresAt: session.expiresAt.UTC().Format(time.RFC3339Nano), Status: session.status,
		Error: session.errorMessage, Account: session.account,
	}
	if includeAuthorization && session.status == subscriptionOAuthLoginPending {
		response.AuthURL = session.authURL
		response.UserCode = session.userCode
		response.VerificationURI = session.verificationURI
	}
	return response
}

func (s *Server) newGeminiOAuthLoginClient() geminiOAuthLoginClient {
	if s.geminiOAuthClientFactory != nil {
		return s.geminiOAuthClientFactory()
	}
	return geminiauth.New(nil)
}

func (s *Server) newGrokOAuthLoginClient() grokOAuthLoginClient {
	if s.grokOAuthClientFactory != nil {
		return s.grokOAuthClientFactory()
	}
	return grokauth.New(nil)
}

func (s *Server) newKimiOAuthLoginClient() kimiOAuthLoginClient {
	if s.kimiOAuthClientFactory != nil {
		return s.kimiOAuthClientFactory()
	}
	return kimiauth.New(nil, config.Version, "")
}

func (s *Server) newKiroOAuthLoginClient() kiroOAuthLoginClient {
	if s.kiroOAuthClientFactory != nil {
		return s.kiroOAuthClientFactory()
	}
	return kiroauth.New(nil)
}

// prepareKiroOAuthLogin creates a login session for Kiro. Unlike Grok/Kimi
// which use OIDC device flow, Kiro requires the user to paste a refresh token
// from ~/.kiro/credentials.json. The session starts in pending state and
// waits for a POST to /login/{loginId}/submit with the token.
func (s *Server) prepareKiroOAuthLogin() (*subscriptionOAuthLoginSession, func(), error) {
	loginRandom, err := geminiauth.GenerateState()
	if err != nil {
		return nil, nil, err
	}
	sessionCtx, cancel := context.WithCancel(context.Background())
	now := s.now().UTC()
	session := &subscriptionOAuthLoginSession{
		loginID:   subscriptionauth.ProviderKiro + "_login_" + loginRandom,
		provider:  subscriptionauth.ProviderKiro,
		status:    subscriptionOAuthLoginPending,
		expiresAt: now.Add(s.subscriptionOAuthTTL(subscriptionauth.ProviderKiro, 0)),
		ctx:       sessionCtx,
		cancel:    cancel,
	}
	activate := func() {
		go s.expireSubscriptionOAuthLoginAfter(session)
	}
	return session, activate, nil
}

// submitKiroOAuthLogin receives the refresh token pasted by the user,
// calls the Kiro refresh endpoint to validate it and retrieve an access token,
// then saves the credential and completes the login session.
func (s *Server) submitKiroOAuthLogin(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if s.rejectRemoteSubscriptionOAuthLogin(w, r) {
		return
	}
	loginID := strings.TrimSpace(chi.URLParam(r, "loginId"))

	s.subscriptionOAuthMu.Lock()
	s.expireSubscriptionOAuthLoginsLocked(s.now())
	session := s.subscriptionOAuthLogins[loginID]
	if session == nil || session.provider != subscriptionauth.ProviderKiro {
		s.subscriptionOAuthMu.Unlock()
		writeError(w, http.StatusNotFound, "Kiro 登录会话不存在或已过期")
		return
	}
	if session.status != subscriptionOAuthLoginPending {
		response := subscriptionOAuthLoginPublicResponse(session, false)
		s.subscriptionOAuthMu.Unlock()
		writeJSON(w, http.StatusOK, response)
		return
	}
	s.subscriptionOAuthMu.Unlock()

	var req struct {
		RefreshToken string `json:"refreshToken"`
		Region       string `json:"region"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	refreshToken := strings.TrimSpace(req.RefreshToken)
	if refreshToken == "" {
		writeError(w, http.StatusBadRequest, "refreshToken 不能为空")
		return
	}
	region := strings.TrimSpace(req.Region)
	if region == "" {
		region = kiroauth.DefaultRegion
	}
	if err := kiroauth.ValidateRegion(region); err != nil {
		writeError(w, http.StatusBadRequest, "region 无效："+err.Error())
		return
	}

	if !s.beginSubscriptionOAuthExchange(session) {
		writeError(w, http.StatusConflict, "Kiro 登录会话已过期或已完成")
		return
	}

	client := s.newKiroOAuthLoginClient()
	tokens, err := client.RefreshToken(session.ctx, refreshToken, region)
	if err != nil {
		s.failSubscriptionOAuthLogin(session, "Kiro refresh token 验证失败，请检查 token 是否有效")
		writeError(w, http.StatusBadGateway, "Kiro refresh token 验证失败")
		return
	}
	account, err := s.saveSubscriptionOAuthCredential(subscriptionauth.ProviderKiro, subscriptionOAuthTokens{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresAt:    tokens.ExpiresAt,
		Subject:      tokens.ProfileArn, // store ProfileArn in Subject for region derivation
	})
	if err != nil {
		s.failSubscriptionOAuthLogin(session, "Kiro 凭据无法安全保存，请重试")
		writeError(w, http.StatusInternalServerError, "Kiro 凭据保存失败")
		return
	}
	s.completeSubscriptionOAuthLogin(session, account)
	writeJSON(w, http.StatusOK, subscriptionOAuthLoginPublicResponse(session, false))
}

// submitKiroAPIKeyLogin accepts a Kiro API key (ksk_xxx format) directly,
// stores it as the account's access token, and completes the login session.
// No refresh endpoint is called — API keys are long-lived and do not expire.
func (s *Server) submitKiroAPIKeyLogin(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if s.rejectRemoteSubscriptionOAuthLogin(w, r) {
		return
	}
	loginID := strings.TrimSpace(chi.URLParam(r, "loginId"))

	s.subscriptionOAuthMu.Lock()
	s.expireSubscriptionOAuthLoginsLocked(s.now())
	session := s.subscriptionOAuthLogins[loginID]
	if session == nil || session.provider != subscriptionauth.ProviderKiro {
		s.subscriptionOAuthMu.Unlock()
		writeError(w, http.StatusNotFound, "Kiro 登录会话不存在或已过期")
		return
	}
	if session.status != subscriptionOAuthLoginPending {
		response := subscriptionOAuthLoginPublicResponse(session, false)
		s.subscriptionOAuthMu.Unlock()
		writeJSON(w, http.StatusOK, response)
		return
	}
	s.subscriptionOAuthMu.Unlock()

	var req struct {
		KiroAPIKey string `json:"kiroApiKey"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	apiKey := strings.TrimSpace(req.KiroAPIKey)
	if !strings.HasPrefix(apiKey, "ksk_") || len(apiKey) < 8 {
		writeError(w, http.StatusBadRequest, "kiroApiKey 格式无效，应以 ksk_ 开头")
		return
	}

	if !s.beginSubscriptionOAuthExchange(session) {
		writeError(w, http.StatusConflict, "Kiro 登录会话已过期或已完成")
		return
	}

	// API keys are stored directly as AccessToken with no expiry.
	account, err := s.saveSubscriptionOAuthCredential(subscriptionauth.ProviderKiro, subscriptionOAuthTokens{
		AccessToken: apiKey,
	})
	if err != nil {
		s.failSubscriptionOAuthLogin(session, "Kiro API key 无法安全保存，请重试")
		writeError(w, http.StatusInternalServerError, "Kiro API key 保存失败")
		return
	}
	s.completeSubscriptionOAuthLogin(session, account)
	writeJSON(w, http.StatusOK, subscriptionOAuthLoginPublicResponse(session, false))
}

func subscriptionOAuthProvider(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case subscriptionauth.ProviderGemini:
		return subscriptionauth.ProviderGemini, true
	case subscriptionauth.ProviderGrok:
		return subscriptionauth.ProviderGrok, true
	case subscriptionauth.ProviderKimi:
		return subscriptionauth.ProviderKimi, true
	case subscriptionauth.ProviderKiro:
		return subscriptionauth.ProviderKiro, true
	default:
		return "", false
	}
}

func subscriptionOAuthStartError(provider string) string {
	switch provider {
	case subscriptionauth.ProviderGemini:
		return "无法启动 Gemini OAuth 登录"
	case subscriptionauth.ProviderGrok:
		return "无法启动 Grok 设备授权"
	case subscriptionauth.ProviderKimi:
		return "无法启动 Kimi 设备授权"
	case subscriptionauth.ProviderKiro:
		return "无法启动 Kiro 登录会话"
	default:
		return "无法启动订阅 OAuth 登录"
	}
}

func writeSubscriptionOAuthCallbackStatusHTML(w http.ResponseWriter, locale remoteLoginLocale, provider string, status subscriptionOAuthLoginStatus) {
	name := subscriptionProviderLabel(provider)
	switch status {
	case subscriptionOAuthLoginCompleted:
		writeSubscriptionOAuthCallbackHTML(w, http.StatusOK, locale, subscriptionCallbackSuccess, name, "")
	case subscriptionOAuthLoginCancelled:
		writeSubscriptionOAuthCallbackHTML(w, http.StatusGone, locale, subscriptionCallbackCancelled, name, "")
	case subscriptionOAuthLoginExpired:
		writeSubscriptionOAuthCallbackHTML(w, http.StatusGone, locale, subscriptionCallbackExpired, name, "")
	default:
		writeSubscriptionOAuthCallbackHTML(w, http.StatusBadRequest, locale, subscriptionCallbackFailed, name, "")
	}
}

func writeSubscriptionOAuthCallbackHTML(w http.ResponseWriter, status int, locale remoteLoginLocale, key subscriptionCallbackKey, providerLabel, extra string) {
	title, message := subscriptionCallbackText(locale, key, providerLabel)
	if extra != "" {
		message = extra
	}
	languageTag := string(locale)
	if languageTag == "" {
		languageTag = string(remoteLoginLocaleChineseSimplified)
	}
	setNoStore(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", subscriptionOAuthCallbackCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "<!doctype html><html lang=\"%s\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>%s</title></head><body><main><h1>%s</h1><p>%s</p></main></body></html>", html.EscapeString(languageTag), html.EscapeString(title), html.EscapeString(title), html.EscapeString(message))
}
