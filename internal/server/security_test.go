package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5/middleware"

	agentpkg "autoto/internal/agent"
	"autoto/internal/compat"
	"autoto/internal/config"
	"autoto/internal/db"
)

type requestLogCapture struct {
	messages []string
}

func (c *requestLogCapture) Print(values ...interface{}) {
	c.messages = append(c.messages, fmt.Sprint(values...))
}

type legacyWarningCapture struct {
	mu     sync.Mutex
	usages []compat.Usage
}

func (c *legacyWarningCapture) add(usage compat.Usage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.usages = append(c.usages, usage)
}

func (c *legacyWarningCapture) snapshot() []compat.Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]compat.Usage(nil), c.usages...)
}

func captureLegacyWarnings(app *Server) *legacyWarningCapture {
	capture := &legacyWarningCapture{}
	app.legacyWarnings = compat.NewRegistry(capture.add)
	return capture
}

func TestSensitiveProviderRoutesAlwaysRequireCanonicalLocalToken(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/providers/oauth/codex/accounts"},
		{http.MethodPost, "/api/providers/oauth/codex/accounts/batch"},
		{http.MethodPost, "/api/providers/oauth/codex/import/batch"},
		{http.MethodGet, "/api/providers/oauth/codex/accounts/codex_fixture/export"},
		{http.MethodPatch, "/api/providers/oauth/codex/accounts/codex_fixture"},
		{http.MethodPost, "/api/providers/oauth/codex/accounts/codex_fixture/refresh"},
		{http.MethodDelete, "/api/providers/oauth/codex/accounts/codex_fixture"},
		{http.MethodPost, "/api/providers/oauth/codex/import"},
		{http.MethodPut, "/api/providers/openai-compatible/config"},
		{http.MethodPatch, "/api/providers/openai-compatible"},
		{http.MethodDelete, "/api/providers/openai-compatible"},
		{http.MethodPost, "/api/providers/openai-compatible/test"},
		{http.MethodGet, "/api/providers/codex/auth-files"},
		{http.MethodPost, "/api/providers/codex/auth-files/import"},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			missing := httptest.NewRecorder()
			app.Routes().ServeHTTP(missing, newTestRequest(route.method, route.path, strings.NewReader(`{}`)))
			if missing.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 without canonical token, got %d: %s", missing.Code, missing.Body.String())
			}
			legacy := newTestRequest(route.method, route.path, strings.NewReader(`{}`))
			legacy.Header.Set(nonCanonicalLocalTokenHeader, app.localToken)
			legacyRecorder := httptest.NewRecorder()
			app.Routes().ServeHTTP(legacyRecorder, legacy)
			if legacyRecorder.Code != http.StatusUnauthorized {
				t.Fatalf("expected legacy token rejection, got %d: %s", legacyRecorder.Code, legacyRecorder.Body.String())
			}
		})
	}
	health := httptest.NewRecorder()
	app.Routes().ServeHTTP(health, newTestRequest(http.MethodGet, "/api/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health route should remain available without token, got %d", health.Code)
	}
}

func TestRequestLoggerRedactsLocalTokenQuery(t *testing.T) {
	capture := &requestLogCapture{}
	formatter := &redactingLogFormatter{delegate: &middleware.DefaultLogFormatter{Logger: capture, NoColor: true}}
	request := newTestRequest(http.MethodGet, "/ws/agent?id=agent-1&token=local-secret-token", nil)
	entry := formatter.NewLogEntry(request)
	entry.Write(http.StatusUnauthorized, 0, http.Header{}, time.Millisecond, nil)
	output := strings.Join(capture.messages, "\n")
	if strings.Contains(output, "local-secret-token") {
		t.Fatalf("request log leaked the local token: %s", output)
	}
	if !strings.Contains(output, "token=%5BREDACTED%5D") || !strings.Contains(output, "id=agent-1") {
		t.Fatalf("request log did not preserve a redacted URL: %s", output)
	}
}

func TestLocalRequestGuardRejectsCrossOriginAPI(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/health", nil)
	request.Host = "localhost:7788"
	request.Header.Set("Origin", "http://evil.test")

	app.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestLocalRequestGuardRejectsFetchSiteCrossSiteWithoutOrigin(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/health", nil)
	request.Host = "localhost:7788"
	request.Header.Set("Sec-Fetch-Site", "cross-site")

	app.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for Sec-Fetch-Site cross-site, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestLocalRequestGuardRequiresTokenForFetchSiteBrowserAPI(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)

	missing := httptest.NewRecorder()
	missingReq := newTestRequest(http.MethodGet, "/api/health", nil)
	missingReq.Host = "localhost:7788"
	missingReq.Header.Set("Sec-Fetch-Site", "same-origin")
	app.Routes().ServeHTTP(missing, missingReq)
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token for Sec-Fetch-Site browser request, got %d: %s", missing.Code, missing.Body.String())
	}

	ok := httptest.NewRecorder()
	okReq := newTestRequest(http.MethodGet, "/api/health", nil)
	okReq.Host = "localhost:7788"
	okReq.Header.Set("Sec-Fetch-Site", "same-origin")
	okReq.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(ok, okReq)
	if ok.Code != http.StatusOK {
		t.Fatalf("expected 200 with token for Sec-Fetch-Site browser request, got %d: %s", ok.Code, ok.Body.String())
	}
}

func TestLocalRequestGuardRequiresTokenForBrowserAPI(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)

	missing := httptest.NewRecorder()
	missingReq := newTestRequest(http.MethodGet, "/api/health", nil)
	missingReq.Header.Set("Origin", "http://localhost:7788")
	missingReq.Host = "localhost:7788"
	app.Routes().ServeHTTP(missing, missingReq)
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d: %s", missing.Code, missing.Body.String())
	}

	ok := httptest.NewRecorder()
	okReq := newTestRequest(http.MethodGet, "/api/health", nil)
	okReq.Header.Set("Origin", "http://localhost:7788")
	okReq.Header.Set(localTokenHeader, app.localToken)
	okReq.Host = "localhost:7788"
	app.Routes().ServeHTTP(ok, okReq)
	if ok.Code != http.StatusOK {
		t.Fatalf("expected 200 with token, got %d: %s", ok.Code, ok.Body.String())
	}
}

func TestWebSocketTokenUsesCookieAndRejectsQueryToken(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)

	cookieRequest := newTestRequest(http.MethodGet, "/ws/agent?id=agent-1", nil)
	cookieRequest.AddCookie(&http.Cookie{Name: localTokenCookieName, Value: app.localToken})
	if !app.validWebSocketToken(cookieRequest) {
		t.Fatal("expected local-token cookie to authorize websocket")
	}

	headerRequest := newTestRequest(http.MethodGet, "/ws/agent?id=agent-1", nil)
	headerRequest.Header.Set(localTokenHeader, app.localToken)
	if !app.validWebSocketToken(headerRequest) {
		t.Fatal("expected local-token header to authorize websocket")
	}

	queryRequest := newTestRequest(http.MethodGet, "/ws/agent?id=agent-1&token="+app.localToken, nil)
	if app.validWebSocketToken(queryRequest) {
		t.Fatal("websocket query token must not authorize")
	}
}

func TestLocalRequestGuardAllowsNonBrowserLocalAPI(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/health", nil)
	request.Host = "localhost:7788"

	app.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200 for non-browser local request, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestIndexInjectsLocalToken(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/", nil)
	request.Host = "localhost:7788"

	app.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "window.AUTOTO_LOCAL_TOKEN=") || !strings.Contains(body, app.localToken) {
		t.Fatalf("expected canonical and legacy local token globals in index")
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != localTokenCookieName {
		t.Fatalf("expected canonical local token cookie, got %+v", cookies)
	}
	for header, want := range map[string]string{
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
		"Permissions-Policy":     "camera=(), geolocation=(), microphone=()",
	} {
		if got := recorder.Header().Get(header); got != want {
			t.Fatalf("expected index security header %s=%q, got %q", header, want, got)
		}
	}
	csp := recorder.Header().Get("Content-Security-Policy")
	nonce := uiDocumentCSPNonceForTest(t, csp)
	if want := fmt.Sprintf(uiDocumentCSPTemplate, nonce); csp != want {
		t.Fatalf("expected index CSP %q, got %q", want, csp)
	}
	if !strings.Contains(body, `<script nonce="`+nonce+`">window.AUTOTO_LOCAL_TOKEN=`) {
		t.Fatal("expected the injected local-token script to carry the CSP nonce")
	}
}

func TestExposedLoopbackRemainsLocalButRemotePeerCannotSpoofHost(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{Exposed: true, AccessPassword: "secret"}}, nil, nil, nil)

	local := newTestRequest(http.MethodGet, "/", nil)
	local.Host = "localhost:7788"
	local.RemoteAddr = "127.0.0.1:4567"
	if app.remoteAccessGateRequired(local) {
		t.Fatal("a true loopback request must retain the local administrator boundary")
	}
	localRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(localRecorder, local)
	if localRecorder.Code != http.StatusOK || !strings.Contains(localRecorder.Body.String(), app.localToken) {
		t.Fatalf("loopback administrator page did not receive the canonical local token: %d", localRecorder.Code)
	}

	spoofed := newTestRequest(http.MethodGet, "/", nil)
	spoofed.Host = "localhost:7788"
	spoofed.RemoteAddr = "203.0.113.90:4567"
	if !app.remoteAccessGateRequired(spoofed) {
		t.Fatal("a remote peer must not bypass authentication with Host: localhost")
	}

	forwarded := newTestRequest(http.MethodGet, "/", nil)
	forwarded.Host = "localhost:7788"
	forwarded.RemoteAddr = "127.0.0.1:4567"
	forwarded.Header.Set("CF-Connecting-IP", "203.0.113.91")
	if !app.remoteAccessGateRequired(forwarded) {
		t.Fatal("a tunneled request with a remote forwarding identity must stay remote")
	}
}

func TestLoopbackForwardingMetadataCannotFallBackToLocalAuthority(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	tests := []struct {
		name    string
		headers http.Header
	}{
		{
			name:    "forwarded scheme alone marks a proxy boundary",
			headers: http.Header{"X-Forwarded-Proto": []string{"https"}},
		},
		{
			name: "remote host on a later header line remains remote",
			headers: http.Header{
				"X-Forwarded-Proto": []string{"https"},
				"X-Forwarded-Host":  []string{"localhost:7788", "demo.trycloudflare.com"},
			},
		},
		{
			name: "remote forwarded hop on a later header line remains remote",
			headers: http.Header{
				"Forwarded": []string{
					`for=127.0.0.1;host="localhost:7788";proto=https`,
					`for=203.0.113.88;host="demo.trycloudflare.com";proto=https`,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := newTestRequest(http.MethodGet, "/api/health", nil)
			request.Host = "localhost:7788"
			request.RemoteAddr = "127.0.0.1:4321"
			request.Header = tt.headers.Clone()
			if !app.remoteAccessGateRequired(request) {
				t.Fatal("loopback proxy metadata must not retain local administrator authority")
			}
			recorder := httptest.NewRecorder()
			app.Routes().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("uncredentialed loopback proxy request got %d: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestDirectRemoteAbsoluteHTTPSURLDoesNotSpoofTLS(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	request := newTestRequest(http.MethodGet, "/api/health", nil)
	request.Host = "remote.example.test"
	request.RemoteAddr = "203.0.113.89:4321"
	request.URL.Scheme = "https"
	request.URL.Host = request.Host
	request.TLS = nil

	if requestIsHTTPS(request) {
		t.Fatal("an absolute-form https URL over plaintext must not count as transport TLS")
	}
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("plaintext absolute-form remote request got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestForwardedProxyHeadersRequireLoopbackPeerAndPreserveCloudflareHTTPS(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)

	for _, header := range []string{"X-Real-IP", "True-Client-IP"} {
		t.Run("loopback proxy "+header, func(t *testing.T) {
			request := newTestRequest(http.MethodGet, "/", nil)
			request.Host = "localhost:7788"
			request.RemoteAddr = "127.0.0.1:4321"
			request.Header.Set(header, "203.0.113.91")
			if !app.remoteAccessGateRequired(request) {
				t.Fatalf("%s from a loopback proxy must classify request as remote", header)
			}
		})
	}

	for _, header := range []string{"X-Forwarded-Host", "Forwarded", "X-Real-IP", "True-Client-IP", "X-Forwarded-Proto"} {
		t.Run("direct remote ignores "+header, func(t *testing.T) {
			request := newTestRequest(http.MethodGet, "/api/health", nil)
			request.Host = "localhost:7788"
			request.RemoteAddr = "203.0.113.92:4321"
			switch header {
			case "Forwarded":
				request.Header.Set(header, `for=127.0.0.1;host=localhost:7788;proto=https`)
			case "X-Forwarded-Host":
				request.Header.Set(header, "localhost:7788")
			case "X-Forwarded-Proto":
				request.Header.Set(header, "https")
			default:
				request.Header.Set(header, "127.0.0.1")
			}
			if !app.remoteAccessGateRequired(request) {
				t.Fatalf("direct remote peer must remain remote despite forged %s", header)
			}
			recorder := httptest.NewRecorder()
			app.Routes().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("direct plaintext remote request with forged %s got %d: %s", header, recorder.Code, recorder.Body.String())
			}
		})
	}

	cloudflare := newTestRequest(http.MethodGet, "/api/health", nil)
	cloudflare.Host = "localhost:7788"
	cloudflare.RemoteAddr = "127.0.0.1:4321"
	cloudflare.Header.Set("CF-Connecting-IP", "203.0.113.93")
	cloudflare.Header.Set("X-Forwarded-Host", "demo.trycloudflare.com")
	cloudflare.Header.Set("X-Forwarded-Proto", "https")
	cloudflare.Header.Set("Origin", "https://demo.trycloudflare.com")
	cloudflare.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, cloudflare)
	if recorder.Code != http.StatusOK {
		t.Fatalf("trusted loopback Cloudflare HTTPS request got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestRemoteLoginAndLogoutRejectCrossSiteForms(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	login := newTestRequest(http.MethodPost, remoteAccessPath, strings.NewReader("password=secret"))
	login.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(login)
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	login.Header.Set("Origin", "https://evil.example")
	loginRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(loginRecorder, login)
	if loginRecorder.Code != http.StatusForbidden {
		t.Fatalf("cross-site login form got %d: %s", loginRecorder.Code, loginRecorder.Body.String())
	}

	logout := newTestRequest(http.MethodPost, remoteAccessLogoutPath, nil)
	logout.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(logout)
	logout.Header.Set("Origin", "https://evil.example")
	logoutRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(logoutRecorder, logout)
	if logoutRecorder.Code != http.StatusForbidden {
		t.Fatalf("cross-site logout form got %d: %s", logoutRecorder.Code, logoutRecorder.Body.String())
	}
}

func TestSameOriginAcceptsBrowserSignalAndRootPath(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	rootPath := newTestRequest(http.MethodPost, remoteAccessPath, nil)
	rootPath.Header.Set("Origin", "http://localhost:7788/")
	if !app.sameOriginRequest(rootPath) {
		t.Fatal("an Origin root path must be treated as the same serialized origin")
	}

	browserSignal := newTestRequest(http.MethodPost, remoteAccessPath, nil)
	browserSignal.Header.Set("Origin", "null")
	browserSignal.Header.Set("Sec-Fetch-Site", "same-origin")
	if !app.sameOriginRequest(browserSignal) {
		t.Fatal("a browser-controlled same-origin signal must permit privacy-preserving Origin serialization")
	}

	crossSite := newTestRequest(http.MethodPost, remoteAccessPath, nil)
	crossSite.Header.Set("Origin", "http://localhost:7788")
	crossSite.Header.Set("Sec-Fetch-Site", "cross-site")
	if app.sameOriginRequest(crossSite) {
		t.Fatal("cross-site fetch metadata must override an otherwise matching Origin")
	}
}

func TestSameOriginRequiresExactSchemeHostAndPort(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	for _, origin := range []string{"https://localhost:7788", "http://localhost", "http://localhost:7789", "http://localhost:7788/not-an-origin"} {
		request := newTestRequest(http.MethodGet, "/api/health", nil)
		request.Host = "localhost:7788"
		request.Header.Set("Origin", origin)
		if app.sameOriginRequest(request) {
			t.Fatalf("origin %q must not match http://localhost:7788", origin)
		}
	}
}

func TestWebSocketRejectsBadOriginAndMissingToken(t *testing.T) {
	app := New(config.Config{}, nil, nil, agentpkg.NewHub())
	server := httptest.NewServer(app.Routes())
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/agent?id=n1"

	ctx := t.Context()
	_, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"http://evil.test"}, localTokenHeader: []string{app.localToken}}})
	if err == nil {
		t.Fatal("expected bad origin websocket dial to fail")
	}

	_, _, err = websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{server.URL}}})
	if err == nil {
		t.Fatal("expected missing token websocket dial to fail")
	}
}

func TestRemoteAccessGateRendersLoginPageForRemoteIndex(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/", nil)
	request.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(request)
	request.Header.Set("Accept", "text/html")

	app.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 login page, got %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "AUTOTO_ACCESS_PASSWORD") {
		t.Fatalf("expected canonical password configuration guidance, got %s", body)
	}
	if !strings.Contains(body, "Autoto 远程访问保护") || strings.Contains(body, "NarraFork") {
		t.Fatalf("expected Autoto remote access branding, got %s", body)
	}
	for header, want := range map[string]string{
		"Content-Security-Policy": "frame-ancestors 'none'",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"X-Frame-Options":         "DENY",
	} {
		if got := recorder.Header().Get(header); !strings.Contains(got, want) {
			t.Fatalf("expected %s to contain %q, got %q", header, want, got)
		}
	}
	for header, want := range map[string]string{
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'",
		"X-Frame-Options":         "DENY",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
	} {
		if recorder.Header().Get(header) != want {
			t.Fatalf("expected login security header %s=%q, got %q", header, want, recorder.Header().Get(header))
		}
	}
	for _, fragment := range []string{
		`color-scheme: light`,
		`<label class="password-label" for="remoteAccessPassword">访问密码</label>`,
		`.password-label {`,
		`--page:#f4f6fb`,
		`--radius:8px`,
		`--radius-xl:calc(var(--radius) * 1.4)`,
		`border-radius: var(--radius-xl)`,
		`border-radius: var(--radius-lg)`,
		`class="remote-access-shell remote-access-card"`,
		`class="card-content"`,
		`.remote-access-card { position: relative; z-index: 1; width: min(100%, 488px)`,
		`background: linear-gradient(150deg, rgba(255,255,255,.99)`,
		`form method="post" action="/auth/remote-access"`,
		`id="remoteAccessPassword" name="password"`,
		`autocomplete="current-password"`,
		`aria-label="访问密码"`,
		`<label class="password-label" for="remoteAccessPassword">访问密码</label>`,
		`<circle cx="16" cy="16" r="12.5"></circle>`,
		`@keyframes connection-bounce`,
		`animation: connection-bounce 1.15s`,
		`gap: 5px`,
		`@media (prefers-reduced-motion: reduce)`,
		`@media (max-width: 520px)`,
		`body { justify-content: center; padding: max(16px, env(safe-area-inset-top))`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("expected redesigned responsive login page fragment %q, got %s", fragment, body)
		}
	}
	for _, removed := range []string{
		`当前请求来自非可信 localhost`,
		`密码只用于验证当前浏览器的远程会话`,
		`class="remote-policy"`,
		`运行主机已设为受限权限`,
		`最高 acceptEdits`,
		`通过 localhost 打开设置`,
		`class="page-footer"`,
		`本机 localhost 访问不受影响`,
		`aria-describedby="passwordHint"`,
		`class="field-hint"`,
		`<label for="remoteAccessPassword">`,
		`class="protection-pill"`,
		`>远程访问保护</span>`,
		`name="mode"`,
		`class="access-mode"`,
		`选择本次会话权限`,
		`border-radius: 26px`,
		`border-radius: 22px`,
		`border-radius: 15px`,
	} {
		if strings.Contains(body, removed) {
			t.Fatalf("expected compact login page to remove %q, got %s", removed, body)
		}
	}
	if strings.Contains(body, "<script") || strings.Contains(body, "window.AUTOTO_LOCAL_TOKEN=") {
		t.Fatal("remote login page must remain script-free and must not leak local token")
	}
}

func TestRemoteAccessGateLocalizesLoginPageFromAcceptLanguage(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/", nil)
	request.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(request)
	request.Header.Set("Accept", "text/html")
	request.Header.Set("Accept-Language", "zh-TW,zh-Hant;q=0.9,en;q=0.8")

	app.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 login page, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Language"); got != "zh-TW" {
		t.Fatalf("expected Traditional Chinese content language, got %q", got)
	}
	if got := recorder.Header().Get("Vary"); !strings.Contains(got, "Accept-Language") {
		t.Fatalf("expected Accept-Language variance, got %q", got)
	}
	body := recorder.Body.String()
	for _, fragment := range []string{
		`<html lang="zh-TW">`,
		`<title>Autoto 遠端存取保護</title>`,
		`<span class="connection-state">等待驗證</span>`,
		`<h1 id="remoteAccessTitle">安全解鎖 Autoto</h1>`,
		`<label class="password-label" for="remoteAccessPassword">存取密碼</label>`,
		`placeholder="請輸入存取密碼" aria-label="存取密碼"`,
		`<span>解鎖 Autoto</span>`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("expected Traditional Chinese login fragment %q, got %s", fragment, body)
		}
	}
	for _, simplified := range []string{"远程访问保护", "等待验证", "安全解锁", "访问密码", "请输入访问密码"} {
		if strings.Contains(body, simplified) {
			t.Fatalf("Traditional Chinese login page leaked Simplified Chinese %q: %s", simplified, body)
		}
	}
}

func TestRemoteAccessLoginKeepsTraditionalLocaleAfterPasswordFailure(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, remoteAccessPath, strings.NewReader("password=wrong"))
	request.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(request)
	request.Header.Set("Accept-Language", "zh-Hant-TW,zh;q=0.9")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	app.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected password rejection, got %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if recorder.Header().Get("Content-Language") != "zh-TW" || !strings.Contains(body, "密碼不正確，請重試。") || strings.Contains(body, "密码不正确") {
		t.Fatalf("expected Traditional Chinese password failure page, got headers=%v body=%s", recorder.Header(), body)
	}
}

func TestRemoteAccessLoginPageOmitsHostConfiguredModeAndSelector(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{
		AccessPassword:          "secret",
		AllowRemoteFullAccess:   true,
		DefaultRemoteAccessMode: remoteAccessModeFull,
	}}, nil, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, remoteAccessPath, nil)
	request.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(request)
	request.Header.Set("Accept", "text/html")
	app.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected login form, got %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{
		`运行主机已设为完整权限`,
		`登录后可访问主机目录、终端与 bypassPermissions`,
		`只能在运行 Autoto 的主机本地更改`,
		`class="remote-policy"`,
		`class="page-footer"`,
		`name="mode"`,
		`<fieldset class="access-mode"`,
		`选择本次会话权限`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("remote login form must omit %q", forbidden)
		}
	}
	if strings.Contains(body, app.localToken) || strings.Contains(body, "window.AUTOTO_LOCAL_TOKEN") {
		t.Fatal("remote login form must not expose the canonical local token")
	}
}

func TestRemoteAccessLoginPageRejectsPlainRemoteHTTP(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	request := newTestRequest(http.MethodGet, remoteAccessPath, nil)
	request.Host = "demo.trycloudflare.com"
	markRemotePlainHTTP(request)
	request.Header.Set("Accept", "text/html")
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "HTTPS") {
		t.Fatalf("expected remote login page HTTP rejection, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestRemoteAccessLoginIgnoresClientModeAndUsesHostPolicy(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{
		AccessPassword:          "secret",
		AllowRemoteFullAccess:   true,
		DefaultRemoteAccessMode: remoteAccessModeFull,
	}}, nil, nil, nil)
	login := httptest.NewRecorder()
	loginRequest := newTestRequest(http.MethodPost, remoteAccessPath, strings.NewReader("password=secret&mode=restricted"))
	loginRequest.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(loginRequest)
	loginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	app.Routes().ServeHTTP(login, loginRequest)
	if login.Code != http.StatusSeeOther {
		t.Fatalf("expected login redirect, got %d: %s", login.Code, login.Body.String())
	}

	settingsRequest := newTestRequest(http.MethodGet, "/api/security/remote-access", nil)
	settingsRequest.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(settingsRequest)
	for _, cookie := range login.Result().Cookies() {
		settingsRequest.AddCookie(cookie)
	}
	settings := httptest.NewRecorder()
	app.Routes().ServeHTTP(settings, settingsRequest)
	if settings.Code != http.StatusOK || !strings.Contains(settings.Body.String(), `"mode":"full"`) {
		t.Fatalf("host-configured full mode must ignore the client request, got %d: %s", settings.Code, settings.Body.String())
	}
}

func TestAuthenticatedRemoteIndexDoesNotExposeCanonicalLocalToken(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{
		AccessPassword:          "secret",
		AllowRemoteFullAccess:   true,
		DefaultRemoteAccessMode: remoteAccessModeRestricted,
	}}, nil, nil, nil)
	login := httptest.NewRecorder()
	loginRequest := newTestRequest(http.MethodPost, remoteAccessPath, strings.NewReader("password=secret&mode=restricted"))
	loginRequest.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(loginRequest)
	loginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	app.Routes().ServeHTTP(login, loginRequest)
	if login.Code != http.StatusSeeOther {
		t.Fatalf("expected login redirect, got %d: %s", login.Code, login.Body.String())
	}

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/", nil)
	request.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(request)
	for _, cookie := range login.Result().Cookies() {
		request.AddCookie(cookie)
	}
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected authenticated remote index, got %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, app.localToken) || strings.Contains(body, "window.AUTOTO_LOCAL_TOKEN=") {
		t.Fatal("authenticated remote index must not expose the canonical local token")
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == localTokenCookieName {
			t.Fatal("authenticated remote index must not set the canonical local token cookie")
		}
	}
}

func TestRemoteAccessLoginRejectsCrossOriginAndPlainHTTP(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)

	crossOrigin := newTestRequest(http.MethodPost, remoteAccessPath, strings.NewReader("password=secret"))
	crossOrigin.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(crossOrigin)
	crossOrigin.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	crossOrigin.Header.Set("Origin", "https://evil.test")
	crossOrigin.Header.Set("Sec-Fetch-Site", "cross-site")
	crossOriginRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(crossOriginRecorder, crossOrigin)
	if crossOriginRecorder.Code != http.StatusForbidden {
		t.Fatalf("expected cross-origin login rejection, got %d: %s", crossOriginRecorder.Code, crossOriginRecorder.Body.String())
	}

	plainHTTP := newTestRequest(http.MethodPost, remoteAccessPath, strings.NewReader("password=secret"))
	plainHTTP.Host = "demo.trycloudflare.com"
	markRemotePlainHTTP(plainHTTP)
	plainHTTP.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	plainHTTP.Header.Set("Origin", "http://demo.trycloudflare.com")
	plainRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(plainRecorder, plainHTTP)
	if plainRecorder.Code != http.StatusForbidden || !strings.Contains(plainRecorder.Body.String(), "HTTPS") {
		t.Fatalf("expected explicit remote HTTP rejection, got %d: %s", plainRecorder.Code, plainRecorder.Body.String())
	}

	https := newTestRequest(http.MethodPost, remoteAccessPath, strings.NewReader("password=secret"))
	https.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(https)
	https.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	https.Header.Set("Origin", "https://demo.trycloudflare.com")
	httpsRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(httpsRecorder, https)
	if httpsRecorder.Code != http.StatusSeeOther {
		t.Fatalf("expected HTTPS login to pass, got %d: %s", httpsRecorder.Code, httpsRecorder.Body.String())
	}
	for _, cookie := range httpsRecorder.Result().Cookies() {
		if cookie.Name == remoteAccessCookieName && !cookie.Secure {
			t.Fatal("HTTPS remote login must issue a Secure cookie")
		}
	}
}

func TestRemoteAccessLogoutRejectsCrossOrigin(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	request := newTestRequest(http.MethodPost, remoteAccessLogoutPath, nil)
	request.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(request)
	request.Header.Set("Origin", "https://evil.test")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected cross-origin logout rejection, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestRemoteAccessGateAllowsRemoteRequestAfterPasswordLogin(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)

	login := httptest.NewRecorder()
	loginReq := newTestRequest(http.MethodPost, "/auth/remote-access", strings.NewReader("password=secret"))
	loginReq.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(loginReq)
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	app.Routes().ServeHTTP(login, loginReq)
	if login.Code != http.StatusSeeOther {
		t.Fatalf("expected login redirect, got %d: %s", login.Code, login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected remote access cookie")
	}

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/health", nil)
	request.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(request)
	request.Header.Set("Origin", "https://demo.trycloudflare.com")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200 after remote login, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestRemoteAccessGateAcceptsOnlyCanonicalHeaderAndCookie(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	sessionToken, _, err := app.newRemoteAccessSession(remoteAccessModeRestricted)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		header string
		cookie string
		value  string
		want   int
	}{
		{name: "canonical header", header: remoteAccessHeader, value: "secret", want: http.StatusOK},
		{name: "canonical cookie", cookie: remoteAccessCookieName, value: sessionToken, want: http.StatusOK},
		{name: "non-canonical header", header: nonCanonicalRemoteAccessHeader, value: "secret", want: http.StatusUnauthorized},
		{name: "non-canonical cookie", cookie: nonCanonicalRemoteAccessCookieName, value: sessionToken, want: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := newTestRequest(http.MethodGet, "/api/health", nil)
			request.Host = "demo.trycloudflare.com"
			markRemoteHTTPS(request)
			if tt.header != "" {
				request.Header.Set(tt.header, tt.value)
			}
			if tt.cookie != "" {
				request.AddCookie(&http.Cookie{Name: tt.cookie, Value: tt.value})
			}
			app.Routes().ServeHTTP(recorder, request)
			if recorder.Code != tt.want {
				t.Fatalf("expected status %d, got %d: %s", tt.want, recorder.Code, recorder.Body.String())
			}
		})
	}
}

// A wrong canonical credential must fail outright: the gate must not fall back
// to some other header or cookie that happens to carry the right secret.
func TestRemoteAccessGateDoesNotFallBackToOtherCredentialNames(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	request := newTestRequest(http.MethodGet, "/api/health", nil)
	request.Header.Set(remoteAccessHeader, "wrong-secret")
	request.Header.Set(nonCanonicalRemoteAccessHeader, "secret")
	if app.validRemoteAccess(request) {
		t.Fatal("expected a wrong canonical remote access header to fail without fallback")
	}

	sessionToken, _, err := app.newRemoteAccessSession(remoteAccessModeRestricted)
	if err != nil {
		t.Fatal(err)
	}
	cookieRequest := newTestRequest(http.MethodGet, "/api/health", nil)
	cookieRequest.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(cookieRequest)
	cookieRequest.AddCookie(&http.Cookie{Name: remoteAccessCookieName, Value: "invalid-secret"})
	cookieRequest.AddCookie(&http.Cookie{Name: nonCanonicalRemoteAccessCookieName, Value: sessionToken})
	if app.validRemoteAccess(cookieRequest) {
		t.Fatal("expected a wrong canonical remote access cookie to fail without fallback")
	}
}

func TestRemoteAccessGateAllowsBearerPasswordForAPI(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/health", nil)
	request.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(request)
	request.Header.Set("Authorization", "Bearer secret")

	app.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200 with bearer password, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestRemoteAccessGateTrustsForwardedRemoteIdentityOnlyFromLoopbackProxy(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)

	trusted := newTestRequest(http.MethodGet, "/", nil)
	trusted.Host = "localhost:7788"
	trusted.RemoteAddr = "127.0.0.1:4567"
	trusted.Header.Set("X-Real-IP", "203.0.113.9")
	if !app.remoteAccessGateRequired(trusted) {
		t.Fatal("loopback proxy with X-Real-IP must be treated as remote")
	}

	for _, header := range []string{"X-Real-IP", "True-Client-IP"} {
		spoofed := newTestRequest(http.MethodGet, "/", nil)
		spoofed.Host = "localhost:7788"
		spoofed.RemoteAddr = "203.0.113.20:4567"
		spoofed.Header.Set(header, "127.0.0.1")
		if !app.remoteAccessGateRequired(spoofed) {
			t.Fatalf("direct remote client must not bypass the gate with forged %s", header)
		}
	}
}

func TestSameOriginTrustsForwardedHostOnlyFromLoopbackProxy(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)

	trusted := newTestRequest(http.MethodGet, "/api/health", nil)
	trusted.Host = "localhost:7788"
	trusted.RemoteAddr = "127.0.0.1:4567"
	trusted.Header.Set("X-Forwarded-Host", "demo.trycloudflare.com")
	trusted.Header.Set("X-Forwarded-Proto", "https")
	trusted.Header.Set("Origin", "https://demo.trycloudflare.com")
	trusted.Header.Set("Authorization", "Bearer secret")
	app.Routes().ServeHTTP(httptest.NewRecorder(), trusted)
	if !app.sameOriginRequest(trusted) {
		t.Fatal("trusted proxy forwarded origin should pass")
	}

	forged := newTestRequest(http.MethodGet, "/api/health", nil)
	forged.Host = "localhost:7788"
	forged.RemoteAddr = "203.0.113.21:4567"
	forged.Header.Set("X-Forwarded-Host", "demo.trycloudflare.com")
	forged.Header.Set("X-Forwarded-Proto", "https")
	forged.Header.Set("Origin", "https://demo.trycloudflare.com")
	if app.sameOriginRequest(forged) {
		t.Fatal("direct remote client must not pass same-origin with forged forwarding headers")
	}
}

func TestSameOriginComparesSchemeAndEffectivePort(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	request := newTestRequest(http.MethodGet, "/api/health", nil)
	request.Host = "localhost:7788"
	request.Header.Set("Origin", "https://localhost:7788")
	if app.sameOriginRequest(request) {
		t.Fatal("same host with a different scheme must be denied")
	}
	request.Header.Set("Origin", "http://localhost:7789")
	if app.sameOriginRequest(request) {
		t.Fatal("same host with a different effective port must be denied")
	}
}

func postRemoteAccessPassword(app *Server, remoteAddr string, password string, headers http.Header) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/auth/remote-access", strings.NewReader("password="+password))
	request.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(request)
	if remoteAddr != "" {
		request.RemoteAddr = remoteAddr
	}
	if trustedLoopbackPeer(request) {
		request.TLS = nil
		request.Header.Set("X-Forwarded-Proto", "https")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	app.Routes().ServeHTTP(recorder, request)
	return recorder
}

func postRemoteAccessLogout(app *Server, remoteAddr string, headers http.Header) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/auth/remote-access/logout", nil)
	request.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(request)
	if remoteAddr != "" {
		request.RemoteAddr = remoteAddr
	}
	if trustedLoopbackPeer(request) {
		request.TLS = nil
		request.Header.Set("X-Forwarded-Proto", "https")
	}
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	app.Routes().ServeHTTP(recorder, request)
	return recorder
}

func TestRemoteAccessLoginLocksAfterTenBadPasswords(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	for i := 0; i < remoteAccessMaxFailures; i++ {
		recorder := httptest.NewRecorder()
		request := newTestRequest(http.MethodPost, "/auth/remote-access", strings.NewReader("password=wrong"))
		request.Host = "demo.trycloudflare.com"
		markRemoteHTTPS(request)
		request.RemoteAddr = "203.0.113.10:5555"
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		app.Routes().ServeHTTP(recorder, request)
		if i < remoteAccessMaxFailures-1 && recorder.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d expected 401, got %d: %s", i+1, recorder.Code, recorder.Body.String())
		}
		if i == remoteAccessMaxFailures-1 && recorder.Code != http.StatusTooManyRequests {
			t.Fatalf("attempt %d expected 429, got %d: %s", i+1, recorder.Code, recorder.Body.String())
		}
	}

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/auth/remote-access", strings.NewReader("password=secret"))
	request.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(request)
	request.RemoteAddr = "203.0.113.10:5555"
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("expected locked correct password to remain 429, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestRemoteAccessLockoutUsesForwardedClientIPFromLocalProxy(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	a := http.Header{"CF-Connecting-IP": []string{"203.0.113.21"}, "Cf-Ray": []string{"ray-a"}}
	b := http.Header{"CF-Connecting-IP": []string{"203.0.113.22"}, "Cf-Ray": []string{"ray-b"}}

	for i := 0; i < remoteAccessMaxFailures; i++ {
		recorder := postRemoteAccessPassword(app, "127.0.0.1:5555", "wrong", a)
		if i < remoteAccessMaxFailures-1 && recorder.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d expected 401, got %d: %s", i+1, recorder.Code, recorder.Body.String())
		}
		if i == remoteAccessMaxFailures-1 && recorder.Code != http.StatusTooManyRequests {
			t.Fatalf("attempt %d expected 429, got %d: %s", i+1, recorder.Code, recorder.Body.String())
		}
	}

	recorder := postRemoteAccessPassword(app, "127.0.0.1:5555", "secret", b)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("expected different forwarded client to avoid shared tunnel lock, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestRemoteAccessLockoutUsesProxyAppendedForwardedForValue(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	for i := 0; i < remoteAccessMaxFailures; i++ {
		headers := http.Header{"X-Forwarded-For": []string{fmt.Sprintf("198.51.100.%d, 203.0.113.77", i+1)}}
		recorder := postRemoteAccessPassword(app, "127.0.0.1:5555", "wrong", headers)
		if i < remoteAccessMaxFailures-1 && recorder.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d expected 401, got %d: %s", i+1, recorder.Code, recorder.Body.String())
		}
		if i == remoteAccessMaxFailures-1 && recorder.Code != http.StatusTooManyRequests {
			t.Fatalf("attempt %d expected 429 despite changing client-controlled XFF prefix, got %d: %s", i+1, recorder.Code, recorder.Body.String())
		}
	}
}

func TestRemoteAccessLockoutIgnoresForgedForwardedForFromDirectRemote(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	forgedForwardedFor := []string{
		"198.51.100.1", "198.51.100.2", "198.51.100.3", "198.51.100.4", "198.51.100.5",
		"198.51.100.6", "198.51.100.7", "198.51.100.8", "198.51.100.9", "198.51.100.10",
	}
	if len(forgedForwardedFor) < remoteAccessMaxFailures {
		t.Fatalf("test needs at least %d forged client IPs", remoteAccessMaxFailures)
	}
	for i := 0; i < remoteAccessMaxFailures; i++ {
		recorder := postRemoteAccessPassword(app, "203.0.113.23:5555", "wrong", http.Header{"X-Forwarded-For": []string{forgedForwardedFor[i]}})
		if i < remoteAccessMaxFailures-1 && recorder.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d expected 401, got %d: %s", i+1, recorder.Code, recorder.Body.String())
		}
		if i == remoteAccessMaxFailures-1 && recorder.Code != http.StatusTooManyRequests {
			t.Fatalf("attempt %d expected 429 despite changing forged XFF, got %d: %s", i+1, recorder.Code, recorder.Body.String())
		}
	}
}

func TestRemoteAccessLockoutExpires(t *testing.T) {
	current := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	app.clock = func() time.Time { return current }

	for i := 0; i < remoteAccessMaxFailures; i++ {
		postRemoteAccessPassword(app, "203.0.113.24:5555", "wrong", nil)
	}
	locked := postRemoteAccessPassword(app, "203.0.113.24:5555", "secret", nil)
	if locked.Code != http.StatusTooManyRequests {
		t.Fatalf("expected correct password to be locked before expiry, got %d: %s", locked.Code, locked.Body.String())
	}

	current = current.Add(remoteAccessLockDuration + time.Second)
	unlocked := postRemoteAccessPassword(app, "203.0.113.24:5555", "secret", nil)
	if unlocked.Code != http.StatusSeeOther {
		t.Fatalf("expected lockout to expire, got %d: %s", unlocked.Code, unlocked.Body.String())
	}
}

func TestRemoteAccessFailureWindowResetsCount(t *testing.T) {
	current := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	app.clock = func() time.Time { return current }

	for i := 0; i < remoteAccessMaxFailures-1; i++ {
		recorder := postRemoteAccessPassword(app, "203.0.113.25:5555", "wrong", nil)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("initial attempt %d expected 401, got %d: %s", i+1, recorder.Code, recorder.Body.String())
		}
	}
	current = current.Add(remoteAccessFailureWindow + time.Second)
	recorder := postRemoteAccessPassword(app, "203.0.113.25:5555", "wrong", nil)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected first attempt after window reset to stay 401, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestRemoteAccessFailureTrimPreservesLockedEntries(t *testing.T) {
	current := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	app.clock = func() time.Time { return current }

	lockedReq := newTestRequest(http.MethodPost, "/auth/remote-access", nil)
	lockedReq.RemoteAddr = "203.0.113.30:5555"
	for i := 0; i < remoteAccessMaxFailures; i++ {
		app.recordRemoteAccessFailure(lockedReq)
	}
	if locked, _ := app.remoteAccessLocked(lockedReq); !locked {
		t.Fatal("expected seeded victim entry to be locked")
	}

	for i := 1; i <= remoteAccessFailureMaxEntries+25; i++ {
		req := newTestRequest(http.MethodPost, "/auth/remote-access", nil)
		req.RemoteAddr = fmt.Sprintf("10.%d.%d.%d:5555", (i/65536)%256, (i/256)%256, i%256)
		app.recordRemoteAccessFailure(req)
	}

	app.remoteAccessMu.Lock()
	entryCount := len(app.remoteAccessFailure)
	_, retained := app.remoteAccessFailure[remoteAccessClientKey(lockedReq)]
	app.remoteAccessMu.Unlock()
	if entryCount > remoteAccessFailureMaxEntries {
		t.Fatalf("expected at most %d failure entries, got %d", remoteAccessFailureMaxEntries, entryCount)
	}
	if !retained {
		t.Fatal("expected locked entry to be retained during capacity trim")
	}
	if locked, _ := app.remoteAccessLocked(lockedReq); !locked {
		t.Fatal("expected locked entry to remain active after capacity trim")
	}
}

func TestRemoteAccessFailurePrunesExpiredEntries(t *testing.T) {
	current := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	app.clock = func() time.Time { return current }

	expiredReq := newTestRequest(http.MethodPost, "/auth/remote-access", nil)
	expiredReq.RemoteAddr = "203.0.113.31:5555"
	app.recordRemoteAccessFailure(expiredReq)
	expiredKey := remoteAccessClientKey(expiredReq)

	current = current.Add(remoteAccessFailureWindow + time.Second)
	freshReq := newTestRequest(http.MethodPost, "/auth/remote-access", nil)
	freshReq.RemoteAddr = "203.0.113.32:5555"
	app.recordRemoteAccessFailure(freshReq)

	app.remoteAccessMu.Lock()
	_, expiredRetained := app.remoteAccessFailure[expiredKey]
	_, freshRetained := app.remoteAccessFailure[remoteAccessClientKey(freshReq)]
	app.remoteAccessMu.Unlock()
	if expiredRetained {
		t.Fatal("expected expired failure entry to be pruned")
	}
	if !freshRetained {
		t.Fatal("expected fresh failure entry to be retained")
	}
}

func TestRemoteAccessLogoutOnlyClearsFailuresWhenAuthenticated(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	for i := 0; i < remoteAccessMaxFailures; i++ {
		postRemoteAccessPassword(app, "203.0.113.26:5555", "wrong", nil)
	}

	unauthLogout := postRemoteAccessLogout(app, "203.0.113.26:5555", http.Header{"Accept": []string{"application/json"}})
	if unauthLogout.Code != http.StatusOK {
		t.Fatalf("expected unauthenticated logout to still clear cookie, got %d: %s", unauthLogout.Code, unauthLogout.Body.String())
	}
	stillLocked := postRemoteAccessPassword(app, "203.0.113.26:5555", "secret", nil)
	if stillLocked.Code != http.StatusTooManyRequests {
		t.Fatalf("expected unauthenticated logout not to clear failure lock, got %d: %s", stillLocked.Code, stillLocked.Body.String())
	}

	authLogout := postRemoteAccessLogout(app, "203.0.113.26:5555", http.Header{"Authorization": []string{"Bearer secret"}, "Accept": []string{"application/json"}})
	if authLogout.Code != http.StatusOK {
		t.Fatalf("expected authenticated logout, got %d: %s", authLogout.Code, authLogout.Body.String())
	}
	unlocked := postRemoteAccessPassword(app, "203.0.113.26:5555", "secret", nil)
	if unlocked.Code != http.StatusSeeOther {
		t.Fatalf("expected authenticated logout to clear failure lock, got %d: %s", unlocked.Code, unlocked.Body.String())
	}
}

func TestRemoteAccessLoginSuccessClearsBadPasswordCount(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	for i := 0; i < remoteAccessMaxFailures-1; i++ {
		recorder := httptest.NewRecorder()
		request := newTestRequest(http.MethodPost, "/auth/remote-access", strings.NewReader("password=wrong"))
		request.Host = "demo.trycloudflare.com"
		markRemoteHTTPS(request)
		request.RemoteAddr = "203.0.113.11:5555"
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		app.Routes().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d expected 401, got %d: %s", i+1, recorder.Code, recorder.Body.String())
		}
	}

	success := httptest.NewRecorder()
	successReq := newTestRequest(http.MethodPost, "/auth/remote-access", strings.NewReader("password=secret"))
	successReq.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(successReq)
	successReq.RemoteAddr = "203.0.113.11:5555"
	successReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	app.Routes().ServeHTTP(success, successReq)
	if success.Code != http.StatusSeeOther {
		t.Fatalf("expected successful login after bad attempts, got %d: %s", success.Code, success.Body.String())
	}

	badAgain := httptest.NewRecorder()
	badAgainReq := newTestRequest(http.MethodPost, "/auth/remote-access", strings.NewReader("password=wrong"))
	badAgainReq.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(badAgainReq)
	badAgainReq.RemoteAddr = "203.0.113.11:5555"
	badAgainReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	app.Routes().ServeHTTP(badAgain, badAgainReq)
	if badAgain.Code != http.StatusUnauthorized || !strings.Contains(badAgain.Body.String(), "密码不正确") {
		t.Fatalf("expected counter reset after success, got %d: %s", badAgain.Code, badAgain.Body.String())
	}
	if strings.Contains(badAgain.Body.String(), "剩余") {
		t.Fatalf("expected login failure page not to reveal remaining attempts, got %s", badAgain.Body.String())
	}
}

func TestRemoteAccessLogoutClearsCookie(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/auth/remote-access/logout", nil)
	request.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(request)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Accept", "application/json")

	app.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200 logout, got %d: %s", recorder.Code, recorder.Body.String())
	}
	found := map[string]bool{}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == remoteAccessCookieName {
			found[cookie.Name] = true
			if cookie.MaxAge >= 0 || cookie.Value != "" {
				t.Fatalf("expected clearing cookie, got %+v", cookie)
			}
		}
	}
	if !found[remoteAccessCookieName] {
		t.Fatalf("expected the canonical remote access cookie to clear, got %+v", found)
	}
}

func TestRemoteHardeningRejectsTerminalWebSocketByDefault(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{Security: config.SecurityConfig{Exposed: true, AccessPassword: "secret"}}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/ws/terminal?agentId="+agent.ID, nil)
	request.Host = "remote.example.test"
	markRemoteHTTPS(request)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected restricted remote terminal websocket to be rejected, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestRemoteHardeningRejectsBypassPermissionMode(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{Security: config.SecurityConfig{Exposed: true, AccessPassword: "secret"}}, store, nil, nil)

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPatch, "/api/agents/"+agent.ID+"/permission-mode", strings.NewReader(`{"permissionMode":"bypassPermissions"}`))
	request.Host = "localhost:7788"
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "bypassPermissions is disabled") {
		t.Fatalf("expected bypass disabled error, got %s", recorder.Body.String())
	}
}

func TestRemoteHardeningClampsDefaultBypassForNewProject(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projectRoot := t.TempDir()
	projectDir := filepath.Join(projectRoot, "project")
	cfg := config.Config{
		Paths:    config.PathsConfig{DefaultProjectDir: projectRoot},
		Agent:    config.AgentConfig{DefaultModel: "fake:test", DefaultPermissionMode: "bypassPermissions"},
		Security: config.SecurityConfig{Exposed: true, AccessPassword: "secret"},
	}
	app := New(cfg, store, nil, nil)

	payload, err := json.Marshal(map[string]string{"name": "Demo", "gitPath": projectDir})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/projects", strings.NewReader(string(payload)))
	request.Host = "localhost:7788"
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Agent db.Agent `json:"agent"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Agent.PermissionMode != "acceptEdits" {
		t.Fatalf("expected acceptEdits cap, got %q", body.Agent.PermissionMode)
	}
}
