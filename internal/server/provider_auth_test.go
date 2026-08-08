package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"autoto/internal/config"
)

func authenticatedProviderRequest(app *Server, method, target string, body io.Reader) *http.Request {
	request := newTestRequest(method, target, body)
	request.Header.Set(localTokenHeader, app.localToken)
	return request
}

func TestSensitiveProviderAPIsRequireCanonicalTokenWithoutBrowserHeaders(t *testing.T) {
	t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", "fixture-management-key")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"files":[]}`))
	}))
	defer upstream.Close()

	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{
		{
			Name:           "cliproxyapi",
			Type:           "openai-compatible",
			Profile:        config.ProviderProfileCLIProxyAPI,
			BaseURL:        upstream.URL + "/v1",
			Model:          "fixture-model",
			APIKeyOptional: true,
		},
		{
			Name:           "local-codex",
			Type:           "openai-compatible",
			Profile:        config.ProviderProfileCLIProxyAPI,
			BaseURL:        upstream.URL + "/v1",
			Model:          "fixture-model",
			APIKeyOptional: true,
		},
	}}}, nil, nil, nil)

	tests := []struct {
		name        string
		method      string
		target      string
		body        string
		contentType string
	}{
		{name: "CLIProxyAPI auth files list", method: http.MethodGet, target: "/api/providers/cliproxyapi/auth-files"},
		{name: "CLIProxyAPI auth files import", method: http.MethodPost, target: "/api/providers/cliproxyapi/auth-files/import", body: `{"content":"{\"refresh_token\":\"rt_fixture\"}"}`, contentType: "application/json"},
		{name: "generic provider auth files list", method: http.MethodGet, target: "/api/providers/local-codex/auth-files"},
		{name: "generic provider auth files import", method: http.MethodPost, target: "/api/providers/local-codex/auth-files/import", body: `{"content":"{\"refresh_token\":\"rt_fixture_generic\"}"}`, contentType: "application/json"},
		{name: "provider config write", method: http.MethodPut, target: "/api/providers/cliproxyapi/config", body: `{"name":"cliproxyapi","type":"openai-compatible","profile":"cliproxyapi","baseUrl":"` + upstream.URL + `/v1","model":"fixture-model","apiKeyOptional":true}`, contentType: "application/json"},
		{name: "provider draft test", method: http.MethodPost, target: "/api/providers/test", body: `{"name":"cliproxyapi","type":"openai-compatible","profile":"cliproxyapi","baseUrl":"` + upstream.URL + `/v1","model":"fixture-model"}`, contentType: "application/json"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			newRequest := func() *http.Request {
				request := newTestRequest(test.method, test.target, strings.NewReader(test.body))
				if test.contentType != "" {
					request.Header.Set("Content-Type", test.contentType)
				}
				return request
			}

			missing := httptest.NewRecorder()
			app.Routes().ServeHTTP(missing, newRequest())
			if missing.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 without token, got %d: %s", missing.Code, missing.Body.String())
			}

			wrongRequest := newRequest()
			wrongRequest.Header.Set(localTokenHeader, "wrong-fixture-token")
			wrong := httptest.NewRecorder()
			app.Routes().ServeHTTP(wrong, wrongRequest)
			if wrong.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 with wrong token, got %d: %s", wrong.Code, wrong.Body.String())
			}

			legacyRequest := newRequest()
			legacyRequest.Header.Set(nonCanonicalLocalTokenHeader, app.localToken)
			legacy := httptest.NewRecorder()
			app.Routes().ServeHTTP(legacy, legacyRequest)
			if legacy.Code != http.StatusUnauthorized {
				t.Fatalf("expected canonical-header enforcement, got %d: %s", legacy.Code, legacy.Body.String())
			}

			validRequest := newRequest()
			validRequest.Header.Set(localTokenHeader, app.localToken)
			valid := httptest.NewRecorder()
			app.Routes().ServeHTTP(valid, validRequest)
			if valid.Code != http.StatusOK {
				t.Fatalf("expected canonical token success, got %d: %s", valid.Code, valid.Body.String())
			}
		})
	}

	crossOriginRequest := authenticatedProviderRequest(app, http.MethodGet, "/api/providers/cliproxyapi/auth-files", nil)
	crossOriginRequest.Host = "localhost:7788"
	crossOriginRequest.Header.Set("Origin", "https://evil.example")
	crossOrigin := httptest.NewRecorder()
	app.Routes().ServeHTTP(crossOrigin, crossOriginRequest)
	if crossOrigin.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-origin sensitive request, got %d: %s", crossOrigin.Code, crossOrigin.Body.String())
	}
}

func TestCLIProxyAPIAuthFilesRouteProxiesManagementAPI(t *testing.T) {
	t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", "secret")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/auth-files" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("X-Management-Key") != "secret" {
			t.Fatalf("missing management key")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"files":[{"name":"codex.json","provider":"codex"}]}`))
	}))
	defer upstream.Close()

	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name:           "cliproxyapi",
		Type:           "openai-compatible",
		Profile:        config.ProviderProfileCLIProxyAPI,
		BaseURL:        upstream.URL + "/v1",
		Model:          "gpt-5.5",
		APIKeyOptional: true,
	}}}}, nil, nil, nil)

	recorder := httptest.NewRecorder()
	request := authenticatedProviderRequest(app, http.MethodGet, "/api/providers/cliproxyapi/auth-files", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Files []struct {
			Name string `json:"name"`
		} `json:"files"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Files) != 1 || body.Files[0].Name != "codex.json" {
		t.Fatalf("unexpected auth files response: %+v", body)
	}
}

func TestCLIProxyAPIImportAuthFileUploadsMultipart(t *testing.T) {
	t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", "secret")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/auth-files" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Fatalf("expected multipart upload, got %s", r.Header.Get("Content-Type"))
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		files := r.MultipartForm.File["file"]
		if len(files) != 1 {
			t.Fatalf("expected one uploaded file, got %d", len(files))
		}
		if !strings.HasPrefix(files[0].Filename, "autoto-codex-") || !strings.HasSuffix(files[0].Filename, ".json") {
			t.Fatalf("expected Autoto default filename, got %q", files[0].Filename)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","uploaded":1}`))
	}))
	defer upstream.Close()

	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name:           "cliproxyapi",
		Type:           "openai-compatible",
		Profile:        config.ProviderProfileCLIProxyAPI,
		BaseURL:        upstream.URL + "/v1",
		Model:          "gpt-5.5",
		APIKeyOptional: true,
	}}}}, nil, nil, nil)

	payload := []byte(`{"content":"{\"refresh_token\":\"rt\"}"}`)
	recorder := httptest.NewRecorder()
	request := authenticatedProviderRequest(app, http.MethodPost, "/api/providers/cliproxyapi/auth-files/import", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestBuildProviderAuthImportPlanNormalizesSub2APIExport(t *testing.T) {
	now := time.Date(2026, time.July, 14, 6, 30, 0, 0, time.UTC)
	accountID := "11111111-2222-4333-8444-555555555555"
	token := testCodexJWT(t, map[string]any{
		"exp": float64(1784871033),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
			"chatgpt_plan_type":  "team",
		},
		"https://api.openai.com/profile": map[string]any{
			"email": "fixture@example.com",
		},
	})
	source := map[string]any{
		"format":       "sub2api",
		"exported_at":  "2026-07-14T06:26:14Z",
		"workspace_id": "fallback-workspace",
		"accounts": []any{map[string]any{
			"name":     "BATCH-EXAMPLE-01",
			"platform": "openai",
			"type":     "oauth",
			"credentials": map[string]any{
				"access_token":       token,
				"refresh_token":      "",
				"chatgpt_account_id": "",
				"email":              "",
				"expires_at":         float64(1784874374),
			},
			"extra": map[string]any{
				"batch_code":   "fixture-batch",
				"last_refresh": "2026-07-14T06:26:14Z",
				"openai_oauth_responses_websockets_v2_enabled": true,
			},
		}},
	}
	data, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := buildProviderAuthImportPlan("autoto-codex-auth.json", string(data), now)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Format != "sub2api" || len(plan.Files) != 1 || plan.Skipped != 0 {
		t.Fatalf("unexpected import plan: %+v", plan)
	}
	if plan.Files[0].Filename != "autoto-codex-auth-batch-example-01.json" {
		t.Fatalf("unexpected normalized filename %q", plan.Files[0].Filename)
	}
	var auth map[string]any
	if err := json.Unmarshal(plan.Files[0].Content, &auth); err != nil {
		t.Fatal(err)
	}
	if auth["type"] != "codex" || auth["access_token"] != token || auth["account_id"] != accountID || auth["email"] != "fixture@example.com" {
		t.Fatalf("unexpected normalized auth metadata: %+v", auth)
	}
	if auth["plan_type"] != "team" || auth["websockets"] != true || auth["disabled"] != false {
		t.Fatalf("unexpected normalized auth flags: %+v", auth)
	}
	if auth["expired"] != time.Unix(1784874374, 0).UTC().Format(time.RFC3339) || auth["last_refresh"] != "2026-07-14T06:26:14Z" {
		t.Fatalf("unexpected normalized timestamps: %+v", auth)
	}
	for _, forbidden := range []string{"credentials", "extra", "batch_code", "password"} {
		if _, exists := auth[forbidden]; exists {
			t.Fatalf("sub2api wrapper field %q leaked into auth file: %+v", forbidden, auth)
		}
	}
}

func TestBuildProviderAuthImportPlanDeduplicatesAccountsAndPreservesStandardFields(t *testing.T) {
	now := time.Date(2026, time.July, 14, 6, 30, 0, 0, time.UTC)
	duplicate := map[string]any{
		"platform": "openai",
		"credentials": map[string]any{
			"access_token":       "fixture-access-token",
			"chatgpt_account_id": "account-duplicate",
		},
	}
	plan, err := buildProviderAuthJSONImportPlan("batch.json", map[string]any{
		"format":   "sub2api",
		"accounts": []any{duplicate, duplicate},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 1 || plan.Skipped != 1 {
		t.Fatalf("expected one unique account and one duplicate, got %+v", plan)
	}

	standard := map[string]any{
		"type":          "codex",
		"refresh_token": "rt_fixture_only",
		"email":         "standard@example.com",
		"model-aliases": []any{map[string]any{"name": "gpt-fixture", "alias": "fixture-alias"}},
	}
	standardPlan, err := buildProviderAuthJSONImportPlan("standard.json", standard, now)
	if err != nil {
		t.Fatal(err)
	}
	var normalized map[string]any
	if err := json.Unmarshal(standardPlan.Files[0].Content, &normalized); err != nil {
		t.Fatal(err)
	}
	if _, ok := normalized["model-aliases"]; !ok {
		t.Fatalf("expected standard Codex extension fields to be preserved: %+v", normalized)
	}
}

func TestCLIProxyAPIImportAuthFileSplitsSub2APIAccountsWithoutLeakingTokens(t *testing.T) {
	t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", "secret")
	uploaded := make([]map[string]any, 0, 2)
	filenames := make([]string, 0, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		var auth map[string]any
		if err := json.Unmarshal(data, &auth); err != nil {
			t.Fatalf("uploaded auth file is not JSON: %v", err)
		}
		filenames = append(filenames, header.Filename)
		uploaded = append(uploaded, auth)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name:           "cliproxyapi",
		Type:           "openai-compatible",
		Profile:        config.ProviderProfileCLIProxyAPI,
		BaseURL:        upstream.URL + "/v1",
		Model:          "gpt-5.5",
		APIKeyOptional: true,
	}}}}, nil, nil, nil)
	source := map[string]any{
		"format": "sub2api",
		"accounts": []any{
			map[string]any{"name": "account-one", "platform": "openai", "credentials": map[string]any{"access_token": "fixture-access-one", "chatgpt_account_id": "account-one-id"}},
			map[string]any{"name": "account-two", "platform": "openai", "credentials": map[string]any{"refresh_token": "rt_fixture_two", "chatgpt_account_id": "account-two-id"}},
		},
	}
	content, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	requestBody, err := json.Marshal(importAuthFileRequest{Filename: "autoto-codex-auth.json", Content: string(content)})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := authenticatedProviderRequest(app, http.MethodPost, "/api/providers/cliproxyapi/auth-files/import", bytes.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(uploaded) != 2 || len(filenames) != 2 {
		t.Fatalf("expected two uploads, filenames=%v auth=%+v", filenames, uploaded)
	}
	if uploaded[0]["type"] != "codex" || uploaded[1]["type"] != "codex" {
		t.Fatalf("expected normalized Codex auth files: %+v", uploaded)
	}
	responseText := recorder.Body.String()
	for _, secret := range []string{"fixture-access-one", "rt_fixture_two"} {
		if strings.Contains(responseText, secret) {
			t.Fatalf("credential leaked in import response: %s", responseText)
		}
	}
	var response providerAuthImportResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Imported != 2 || response.Format != "sub2api" || response.Status != "ok" {
		t.Fatalf("unexpected import response: %+v", response)
	}
}

func TestProviderAuthImportRejectsAccountWithoutTokensWithoutEchoingInput(t *testing.T) {
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name: "cliproxyapi", Type: "openai-compatible", Profile: config.ProviderProfileCLIProxyAPI, Model: "gpt-5.5",
	}}}}, nil, nil, nil)
	content := `{"format":"sub2api","accounts":[{"name":"private-fixture","credentials":{"email":"private@example.com"}}]}`
	payload, err := json.Marshal(importAuthFileRequest{Content: content})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := authenticatedProviderRequest(app, http.MethodPost, "/api/providers/cliproxyapi/auth-files/import", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "private@example.com") || strings.Contains(recorder.Body.String(), "private-fixture") {
		t.Fatalf("input metadata leaked in validation response: %s", recorder.Body.String())
	}
}

func testCodexJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".fixture-signature"
}

func TestAuthFileProviderUsesProfileRatherThanLegacyName(t *testing.T) {
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name:    "local-codex",
		Type:    "openai-compatible",
		Profile: config.ProviderProfileCLIProxyAPI,
		Model:   "gpt-5.5",
	}}}}, nil, nil, nil)
	provider, err := app.authFileProvider("local-codex")
	if err != nil || provider.Name != "local-codex" || provider.Profile != config.ProviderProfileCLIProxyAPI {
		t.Fatalf("expected profile-selected provider, provider=%+v err=%v", provider, err)
	}
}

func TestGenericProviderAuthHandlerUsesURLProviderName(t *testing.T) {
	t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", "secret")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/auth-files" || r.Header.Get("X-Management-Key") != "secret" {
			t.Fatalf("unexpected generic auth request: %s key=%q", r.URL.Path, r.Header.Get("X-Management-Key"))
		}
		_, _ = w.Write([]byte(`{"files":[]}`))
	}))
	defer upstream.Close()

	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name:    "local-codex",
		Type:    "openai-compatible",
		Profile: config.ProviderProfileCLIProxyAPI,
		BaseURL: upstream.URL + "/v1",
		Model:   "gpt-5.5",
	}}}}, nil, nil, nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("name", "local-codex")
	request := newTestRequest(http.MethodGet, "/api/providers/local-codex/auth-files", nil).WithContext(context.WithValue(context.Background(), chi.RouteCtxKey, routeContext))
	recorder := httptest.NewRecorder()
	app.listProviderAuthFiles(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestCLIProxyAPIManagementUsesAutotoDefaultKey(t *testing.T) {
	t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", "")
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.Header.Get("X-Management-Key"); got != defaultCLIProxyAPIManagementKey {
			t.Fatalf("expected Autoto default management key, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"files":[]}`))
	}))
	defer upstream.Close()

	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name: "cliproxyapi", Type: "openai-compatible", BaseURL: upstream.URL + "/v1", Model: "gpt-5.5", APIKeyOptional: true,
	}}}}, nil, nil, nil)
	capture := captureLegacyWarnings(app)
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, authenticatedProviderRequest(app, http.MethodGet, "/api/providers/cliproxyapi/auth-files", nil))
	if recorder.Code != http.StatusOK || requests != 1 {
		t.Fatalf("expected one successful request, got status=%d requests=%d body=%s", recorder.Code, requests, recorder.Body.String())
	}
	if usages := capture.snapshot(); len(usages) != 0 {
		t.Fatalf("canonical default success must not warn: %+v", usages)
	}
}

func TestCLIProxyAPIManagementDoesNotRetryExplicitKeyOrExposeIt(t *testing.T) {
	const explicitKey = "custom-management-key"
	t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", explicitKey)
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.Header.Get("X-Management-Key"); got != explicitKey {
			t.Fatalf("expected explicit management key, got %q", got)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()

	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name: "cliproxyapi", Type: "openai-compatible", BaseURL: upstream.URL + "/v1", Model: "gpt-5.5", APIKeyOptional: true,
	}}}}, nil, nil, nil)
	capture := captureLegacyWarnings(app)
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, authenticatedProviderRequest(app, http.MethodGet, "/api/providers/cliproxyapi/auth-files", nil))
	if recorder.Code != http.StatusBadGateway || requests != 1 {
		t.Fatalf("expected one failed explicit-key request, got status=%d requests=%d body=%s", recorder.Code, requests, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), explicitKey) {
		t.Fatalf("management key leaked in response: %s", recorder.Body.String())
	}
	if usages := capture.snapshot(); len(usages) != 0 {
		t.Fatalf("explicit key must not trigger legacy warning: %+v", usages)
	}
}

func TestCLIProxyAPIManagementRejectsUnsafeBaseURLs(t *testing.T) {
	for _, baseURL := range []string{
		"https://example.test/v1",
		"ftp://127.0.0.1:8317/v1",
		"http://user@127.0.0.1:8317/v1",
		"http://127.0.0.1:8317/v1?next=external",
		"http://127.0.0.1:8317/v1#external",
	} {
		t.Run(baseURL, func(t *testing.T) {
			_, err := providerManagementBaseURL(config.ProviderSummary{Profile: config.ProviderProfileCLIProxyAPI, BaseURL: baseURL})
			if err == nil {
				t.Fatalf("unsafe management Base URL was accepted: %s", baseURL)
			}
		})
	}
}

func TestCLIProxyAPIManagementRejectsNonLoopbackImportBeforeCredentialsLeave(t *testing.T) {
	const managementKey = "fixture-management-key"
	const importedRefreshToken = "rt_import_fixture_value"
	t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", managementKey)

	received := make(chan struct {
		authorization string
		managementKey string
		body          string
	}, 1)
	receiver := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		received <- struct {
			authorization string
			managementKey string
			body          string
		}{r.Header.Get("Authorization"), r.Header.Get("X-Management-Key"), string(data)}
		w.WriteHeader(http.StatusOK)
	}))
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	receiver.Listener = listener
	receiver.Start()
	defer receiver.Close()

	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name: "cliproxyapi", Type: "openai-compatible", Profile: config.ProviderProfileCLIProxyAPI, BaseURL: receiver.URL + "/v1", Model: "gpt-test", APIKeyOptional: true,
	}}}}, nil, nil, nil)
	payload := []byte(`{"content":"{\"refresh_token\":\"` + importedRefreshToken + `\"}"}`)
	recorder := httptest.NewRecorder()
	request := authenticatedProviderRequest(app, http.MethodPost, "/api/providers/cliproxyapi/auth-files/import", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("expected unsafe management URL failure, got %d: %s", recorder.Code, recorder.Body.String())
	}
	for _, secret := range []string{managementKey, importedRefreshToken} {
		if strings.Contains(recorder.Body.String(), secret) {
			t.Fatalf("secret leaked in unsafe URL error response: %s", recorder.Body.String())
		}
	}
	select {
	case <-received:
		t.Fatal("non-loopback receiver received a management request")
	default:
	}
}

func TestNativeCodexCredentialStoreConcurrentInitialization(t *testing.T) {
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: t.TempDir()}}, nil, nil, nil)
	app.codexCredentialsMu.Lock()
	app.codexCredentials = nil
	app.codexCredentialsMu.Unlock()

	const workers = 32
	stores := make(chan any, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store, err := app.nativeCodexCredentialStore()
			if err != nil {
				errs <- err
				return
			}
			stores <- store
		}()
	}
	wg.Wait()
	close(stores)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var first any
	for store := range stores {
		if first == nil {
			first = store
			continue
		}
		if store != first {
			t.Fatal("concurrent credential-store initialization returned different stores")
		}
	}
	if first == nil {
		t.Fatal("expected initialized Codex credential store")
	}
}

func TestCLIProxyAPIManagementNeverFollowsImportRedirects(t *testing.T) {
	const managementKey = "fixture-management-key"
	const importedRefreshToken = "rt_redirect_fixture_value"
	t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", managementKey)

	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			received := make(chan struct {
				authorization string
				managementKey string
				body          string
			}, 1)
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, _ := io.ReadAll(r.Body)
				received <- struct {
					authorization string
					managementKey string
					body          string
				}{r.Header.Get("Authorization"), r.Header.Get("X-Management-Key"), string(data)}
				w.WriteHeader(http.StatusOK)
			}))
			defer target.Close()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL+"/capture", status)
			}))
			defer source.Close()

			app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
				Name: "cliproxyapi", Type: "openai-compatible", Profile: config.ProviderProfileCLIProxyAPI, BaseURL: source.URL + "/v1", Model: "gpt-test", APIKeyOptional: true,
			}}}}, nil, nil, nil)
			payload := []byte(`{"content":"{\"refresh_token\":\"` + importedRefreshToken + `\"}"}`)
			recorder := httptest.NewRecorder()
			request := authenticatedProviderRequest(app, http.MethodPost, "/api/providers/cliproxyapi/auth-files/import", bytes.NewReader(payload))
			request.Header.Set("Content-Type", "application/json")
			app.Routes().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadGateway {
				t.Fatalf("expected redirect rejection, got %d: %s", recorder.Code, recorder.Body.String())
			}
			for _, secret := range []string{managementKey, importedRefreshToken} {
				if strings.Contains(recorder.Body.String(), secret) {
					t.Fatalf("secret leaked in redirect error response: %s", recorder.Body.String())
				}
			}
			select {
			case <-received:
				t.Fatal("redirect target received a management request")
			default:
			}
		})
	}
}
