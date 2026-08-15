package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"autoto/internal/codexauth"
	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
)

func authenticatedCodexRequest(app *Server, method, target string, body io.Reader) *http.Request {
	request := newTestRequest(method, target, body)
	request.Header.Set(localTokenHeader, app.localToken)
	return request
}

func TestNativeCodexCredentialRoutesRequireCanonicalToken(t *testing.T) {
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: t.TempDir()}}, nil, nil, nil, providers.NewRegistry())
	tests := []struct {
		name       string
		method     string
		target     string
		body       string
		validCodes []int
	}{
		{name: "accounts list", method: http.MethodGet, target: "/api/providers/oauth/codex/accounts", validCodes: []int{http.StatusOK}},
		{name: "accounts batch", method: http.MethodPost, target: "/api/providers/oauth/codex/accounts/batch", body: `{"ids":[],"operation":"enable"}`, validCodes: []int{http.StatusBadRequest}},
		{name: "import batch", method: http.MethodPost, target: "/api/providers/oauth/codex/import/batch", body: `{"files":[]}`, validCodes: []int{http.StatusBadRequest}},
		{name: "account export", method: http.MethodGet, target: "/api/providers/oauth/codex/accounts/codex_fixture/export", validCodes: []int{http.StatusBadRequest}},
		{name: "account patch", method: http.MethodPatch, target: "/api/providers/oauth/codex/accounts/codex_fixture", body: `{}`, validCodes: []int{http.StatusBadRequest}},
		{name: "account refresh", method: http.MethodPost, target: "/api/providers/oauth/codex/accounts/codex_fixture/refresh", validCodes: []int{http.StatusNotFound}},
		{name: "account delete", method: http.MethodDelete, target: "/api/providers/oauth/codex/accounts/codex_fixture", validCodes: []int{http.StatusOK}},
		{name: "native import", method: http.MethodPost, target: "/api/providers/oauth/codex/import", body: `{}`, validCodes: []int{http.StatusBadRequest}},
		{name: "oauth login status", method: http.MethodGet, target: "/api/providers/oauth/codex/login/codex_login_fixture", validCodes: []int{http.StatusNotFound}},
		{name: "oauth login cancel", method: http.MethodDelete, target: "/api/providers/oauth/codex/login/codex_login_fixture", validCodes: []int{http.StatusNotFound}},
		{name: "generic codex list", method: http.MethodGet, target: "/api/providers/codex/auth-files", validCodes: []int{http.StatusOK}},
		{name: "generic codex import", method: http.MethodPost, target: "/api/providers/codex/auth-files/import", body: `{}`, validCodes: []int{http.StatusBadRequest}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			newRequest := func() *http.Request {
				request := newTestRequest(test.method, test.target, strings.NewReader(test.body))
				if test.body != "" {
					request.Header.Set("Content-Type", "application/json")
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
				t.Fatalf("expected legacy header rejection, got %d: %s", legacy.Code, legacy.Body.String())
			}

			validRequest := newRequest()
			validRequest.Header.Set(localTokenHeader, app.localToken)
			valid := httptest.NewRecorder()
			app.Routes().ServeHTTP(valid, validRequest)
			matched := false
			for _, code := range test.validCodes {
				matched = matched || valid.Code == code
			}
			if !matched {
				t.Fatalf("canonical token did not reach handler, got %d: %s", valid.Code, valid.Body.String())
			}
		})
	}

	crossOriginRequest := authenticatedCodexRequest(app, http.MethodGet, "/api/providers/oauth/codex/accounts", nil)
	crossOriginRequest.Host = "localhost:7788"
	crossOriginRequest.Header.Set("Sec-Fetch-Site", "cross-site")
	crossOrigin := httptest.NewRecorder()
	app.Routes().ServeHTTP(crossOrigin, crossOriginRequest)
	if crossOrigin.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-origin Codex credential request, got %d: %s", crossOrigin.Code, crossOrigin.Body.String())
	}
	crossOriginExport := authenticatedCodexRequest(app, http.MethodGet, "/api/providers/oauth/codex/accounts/codex_fixture/export", nil)
	crossOriginExport.Header.Set("X-Autoto-Confirm", "export-codex-account")
	crossOriginExport.Host = "localhost:7788"
	crossOriginExport.Header.Set("Sec-Fetch-Site", "cross-site")
	crossOriginExportRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(crossOriginExportRecorder, crossOriginExport)
	if crossOriginExportRecorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-origin Codex export, got %d: %s", crossOriginExportRecorder.Code, crossOriginExportRecorder.Body.String())
	}
}

func TestNativeCodexImportStoresLocallyAndRegistersProvider(t *testing.T) {
	home := t.TempDir()
	registry := providers.NewRegistry()
	app := New(config.Config{
		Paths: config.PathsConfig{HomeDir: home},
		Agent: config.AgentConfig{DefaultModel: "codex:gpt-test"},
	}, nil, nil, nil, registry)
	configPath := filepath.Join(home, "config.json")
	app.SetConfigPath(configPath)

	source := map[string]any{
		"format": "sub2api",
		"accounts": []any{
			map[string]any{"name": "account-one", "platform": "openai", "credentials": map[string]any{"access_token": "fixture-access-one", "refresh_token": "rt_fixture_one", "chatgpt_account_id": "account-one-id", "email": "one@example.test"}},
			map[string]any{"name": "account-two", "platform": "openai", "credentials": map[string]any{"access_token": "fixture-access-two", "chatgpt_account_id": "account-two-id"}},
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
	request := authenticatedCodexRequest(app, http.MethodPost, "/api/providers/oauth/codex/import", bytes.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	for _, secret := range []string{"fixture-access-one", "fixture-access-two", "rt_fixture_one"} {
		if strings.Contains(recorder.Body.String(), secret) {
			t.Fatalf("credential leaked in import response: %s", recorder.Body.String())
		}
	}
	var result providerAuthImportResponse
	if err := json.NewDecoder(recorder.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "ok" || result.Format != "sub2api" || result.Imported != 2 || result.Skipped != 0 {
		t.Fatalf("unexpected import result: %+v", result)
	}
	if _, ok := registry.Get("codex"); !ok {
		t.Fatal("native Codex provider was not registered")
	}
	settingsRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(settingsRecorder, newTestRequest(http.MethodGet, "/api/settings", nil))
	if settingsRecorder.Code != http.StatusOK {
		t.Fatalf("expected settings 200, got %d: %s", settingsRecorder.Code, settingsRecorder.Body.String())
	}
	var settingsBody struct {
		Providers []settingsProviderResponse `json:"providers"`
	}
	if err := json.NewDecoder(settingsRecorder.Body).Decode(&settingsBody); err != nil {
		t.Fatal(err)
	}
	if len(settingsBody.Providers) != 1 || settingsBody.Providers[0].Name != "codex" || !settingsBody.Providers[0].Configured {
		t.Fatalf("native Codex readiness missing from settings: %+v", settingsBody.Providers)
	}
	providerConfig, ok := app.providerConfig("codex")
	if !ok || providerConfig.Type != config.ProviderTypeCodex || providerConfig.BaseURL != codexauth.DefaultBaseURL {
		t.Fatalf("unexpected registered Codex config: %+v", providerConfig)
	}

	accountsRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(accountsRecorder, authenticatedCodexRequest(app, http.MethodGet, "/api/providers/oauth/codex/accounts", nil))
	if accountsRecorder.Code != http.StatusOK {
		t.Fatalf("expected accounts 200, got %d: %s", accountsRecorder.Code, accountsRecorder.Body.String())
	}
	for _, secret := range []string{"fixture-access-one", "fixture-access-two", "rt_fixture_one"} {
		if strings.Contains(accountsRecorder.Body.String(), secret) {
			t.Fatalf("credential leaked in accounts response: %s", accountsRecorder.Body.String())
		}
	}
	var accounts codexOAuthAccountsResponse
	if err := json.NewDecoder(accountsRecorder.Body).Decode(&accounts); err != nil {
		t.Fatal(err)
	}
	if accounts.Count != 2 || len(accounts.Accounts) != 2 {
		t.Fatalf("unexpected accounts response: %+v", accounts)
	}

	entries, err := os.ReadDir(codexauth.DefaultStoreDir(home))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected two local credential files, got %d", len(entries))
	}
	persistedConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fixture-access-one", "fixture-access-two", "rt_fixture_one"} {
		if strings.Contains(string(persistedConfig), secret) {
			t.Fatalf("credential leaked in config.json: %s", persistedConfig)
		}
	}
}

func TestNativeCodexAccountManagementEndpointsAndSecretSafety(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fixture-management-access" || r.Header.Get("ChatGPT-Account-ID") != "management-account" {
			t.Fatalf("unexpected quota headers: auth=%q account=%q", r.Header.Get("Authorization"), r.Header.Get("ChatGPT-Account-ID"))
		}
		_, _ = w.Write([]byte(`{"plan_type":"team","rate_limit":{"primary_window":{"used_percent":40,"limit_window_seconds":18000},"secondary_window":{"used_percent":70,"limit_window_seconds":604800}}}`))
	}))
	defer upstream.Close()

	home := t.TempDir()
	database, err := db.Open(context.Background(), filepath.Join(home, "autoto.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	registry := providers.NewRegistry()
	app := New(config.Config{
		Paths: config.PathsConfig{HomeDir: home},
		Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
			Name: "codex", Type: config.ProviderTypeCodex, BaseURL: upstream.URL + "/backend-api/codex", Model: "gpt-test", CodexAllowInsecureTestEndpoint: true,
		}}},
	}, database, nil, nil, registry)

	payload, _ := json.Marshal(importAuthFileRequest{Filename: "management.json", Content: `{"access_token":"fixture-management-access","refresh_token":"rt_management_fixture","account_id":"management-account","email":"manage@example.test"}`})
	importRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(importRecorder, authenticatedCodexRequest(app, http.MethodPost, "/api/providers/oauth/codex/import", bytes.NewReader(payload)))
	if importRecorder.Code != http.StatusOK {
		t.Fatalf("import failed: %d %s", importRecorder.Code, importRecorder.Body.String())
	}

	accountsRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(accountsRecorder, authenticatedCodexRequest(app, http.MethodGet, "/api/providers/oauth/codex/accounts", nil))
	if accountsRecorder.Code != http.StatusOK {
		t.Fatalf("list failed: %d %s", accountsRecorder.Code, accountsRecorder.Body.String())
	}
	assertCodexResponseHasNoSecrets(t, accountsRecorder.Body.Bytes())
	var listed codexOAuthAccountsResponse
	if err := json.Unmarshal(accountsRecorder.Body.Bytes(), &listed); err != nil || len(listed.Accounts) != 1 {
		t.Fatalf("unexpected list response: %+v err=%v", listed, err)
	}
	id := listed.Accounts[0].ID
	if !strings.HasPrefix(id, "codex_") || listed.Accounts[0].Priority != codexauth.DefaultPriority {
		t.Fatalf("missing stable account metadata: %+v", listed.Accounts[0])
	}
	if _, err := database.DB().Exec(`INSERT INTO api_requests (id, provider, credential_id, input_tokens, output_tokens, cost_usd, created_at) VALUES ('codex-usage-fixture', 'codex', ?, 120, 30, 1.25, ?)`, id, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	accountsRecorder = httptest.NewRecorder()
	app.Routes().ServeHTTP(accountsRecorder, authenticatedCodexRequest(app, http.MethodGet, "/api/providers/oauth/codex/accounts", nil))
	if accountsRecorder.Code != http.StatusOK {
		t.Fatalf("usage list failed: %d %s", accountsRecorder.Code, accountsRecorder.Body.String())
	}
	if err := json.NewDecoder(accountsRecorder.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	usage := listed.Accounts[0].Usage
	if usage.Total.RequestCount != 1 || usage.Total.InputTokens != 120 || usage.Total.OutputTokens != 30 || usage.Total.TotalTokens != 150 || usage.Total.CostUSD != 1.25 || usage.Last5Hours.RequestCount != 1 || usage.Last7Days.RequestCount != 1 {
		t.Fatalf("unexpected account usage: %+v", usage)
	}

	missingConfirmation := httptest.NewRecorder()
	app.Routes().ServeHTTP(missingConfirmation, authenticatedCodexRequest(app, http.MethodGet, "/api/providers/oauth/codex/accounts/"+id+"/export", nil))
	if missingConfirmation.Code != http.StatusBadRequest {
		t.Fatalf("export without explicit confirmation should be rejected, got %d: %s", missingConfirmation.Code, missingConfirmation.Body.String())
	}

	exportRequest := authenticatedCodexRequest(app, http.MethodGet, "/api/providers/oauth/codex/accounts/"+id+"/export", nil)
	exportRequest.Header.Set("X-Autoto-Confirm", "export-codex-account")
	exportRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(exportRecorder, exportRequest)
	if exportRecorder.Code != http.StatusOK {
		t.Fatalf("export failed: %d %s", exportRecorder.Code, exportRecorder.Body.String())
	}
	if got := exportRecorder.Header().Get("Content-Disposition"); !strings.Contains(got, `attachment`) || !strings.Contains(got, listed.Accounts[0].Name) {
		t.Fatalf("unexpected export disposition: %q", got)
	}
	if exportRecorder.Header().Get("Cache-Control") == "" || !strings.Contains(exportRecorder.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("export response is cacheable: %q", exportRecorder.Header().Get("Cache-Control"))
	}
	if !json.Valid(exportRecorder.Body.Bytes()) || !strings.Contains(exportRecorder.Body.String(), "fixture-management-access") || !strings.Contains(exportRecorder.Body.String(), "rt_management_fixture") {
		t.Fatalf("export did not contain a valid complete credential: %s", exportRecorder.Body.String())
	}

	remoteExport := authenticatedCodexRequest(app, http.MethodGet, "/api/providers/oauth/codex/accounts/"+id+"/export", nil)
	remoteExport.Header.Set("X-Autoto-Confirm", "export-codex-account")
	remoteExport.Host = "remote.example.test"
	markRemoteHTTPS(remoteExport)
	remoteRecorder := httptest.NewRecorder()
	app.exportCodexOAuthAccount(remoteRecorder, remoteExport)
	if remoteRecorder.Code != http.StatusForbidden {
		t.Fatalf("remote export should be denied, got %d: %s", remoteRecorder.Code, remoteRecorder.Body.String())
	}

	patchBody := bytes.NewBufferString(`{"alias":"Primary <script>","priority":7,"disabled":false}`)
	patchRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(patchRecorder, authenticatedCodexRequest(app, http.MethodPatch, "/api/providers/oauth/codex/accounts/"+id, patchBody))
	if patchRecorder.Code != http.StatusOK || !strings.Contains(patchRecorder.Body.String(), `"priority":7`) {
		t.Fatalf("patch failed: %d %s", patchRecorder.Code, patchRecorder.Body.String())
	}
	assertCodexResponseHasNoSecrets(t, patchRecorder.Body.Bytes())

	refreshRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(refreshRecorder, authenticatedCodexRequest(app, http.MethodPost, "/api/providers/oauth/codex/accounts/"+id+"/refresh", nil))
	if refreshRecorder.Code != http.StatusOK || !strings.Contains(refreshRecorder.Body.String(), `"plan_type":"team"`) {
		t.Fatalf("refresh failed: %d %s", refreshRecorder.Code, refreshRecorder.Body.String())
	}
	assertCodexResponseHasNoSecrets(t, refreshRecorder.Body.Bytes())
	var refreshed codexOAuthAccountResponse
	if err := json.Unmarshal(refreshRecorder.Body.Bytes(), &refreshed); err != nil {
		t.Fatal(err)
	}
	if refreshed.Usage.Total.RequestCount != 1 || refreshed.Usage.Total.TotalTokens != 150 || refreshed.Usage.Total.CostUSD != 1.25 {
		t.Fatalf("refresh response omitted account usage: %+v", refreshed.Usage)
	}

	if err := database.SetGatewayAccountGrant(context.Background(), "codex", id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(`CREATE TRIGGER fail_codex_stats_delete BEFORE DELETE ON provider_account_stats BEGIN SELECT RAISE(ABORT, 'fixture cleanup failure'); END;`); err != nil {
		t.Fatal(err)
	}
	deleteRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(deleteRecorder, authenticatedCodexRequest(app, http.MethodDelete, "/api/providers/oauth/codex/accounts/"+id, nil))
	if deleteRecorder.Code != http.StatusMultiStatus ||
		!strings.Contains(deleteRecorder.Body.String(), `"status":"partial"`) ||
		!strings.Contains(deleteRecorder.Body.String(), `"credential_deleted":true`) ||
		!strings.Contains(deleteRecorder.Body.String(), `"cleanup_pending":true`) ||
		!strings.Contains(deleteRecorder.Body.String(), `"retryable":true`) {
		t.Fatalf("expected retryable partial delete response, got %d %s", deleteRecorder.Code, deleteRecorder.Body.String())
	}
	if grants, err := database.ListGatewayAccountGrants(context.Background(), "codex"); err != nil || len(grants) != 0 {
		t.Fatalf("Codex Gateway grant was not cleaned with the credential: %+v err=%v", grants, err)
	}
	if _, err := database.DB().Exec(`DROP TRIGGER fail_codex_stats_delete`); err != nil {
		t.Fatal(err)
	}
	retryRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(retryRecorder, authenticatedCodexRequest(app, http.MethodDelete, "/api/providers/oauth/codex/accounts/"+id, nil))
	if retryRecorder.Code != http.StatusOK ||
		!strings.Contains(retryRecorder.Body.String(), `"already_missing":true`) ||
		!strings.Contains(retryRecorder.Body.String(), `"stats_deleted":true`) ||
		!strings.Contains(retryRecorder.Body.String(), `"cleanup_pending":false`) ||
		!strings.Contains(retryRecorder.Body.String(), `"retryable":false`) {
		t.Fatalf("delete cleanup retry failed: %d %s", retryRecorder.Code, retryRecorder.Body.String())
	}
	if stats, err := database.ListProviderAccountStats(context.Background(), "codex"); err != nil || len(stats) != 0 {
		t.Fatalf("account stats were not cleaned: %+v err=%v", stats, err)
	}
}

func TestNativeCodexBatchImportReturnsPerFileResults(t *testing.T) {
	home := t.TempDir()
	registry := providers.NewRegistry()
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: home}}, nil, nil, nil, registry)
	credential := `{"type":"codex","access_token":"batch-import-secret","refresh_token":"batch-import-refresh","account_id":"batch-import-account"}`
	body, err := json.Marshal(codexOAuthImportBatchRequest{Files: []importAuthFileRequest{
		{Filename: "one.json", Content: credential},
		{Filename: "duplicate.json", Content: credential},
		{Filename: "broken.json", Content: `{"access_token":`},
		{Filename: "wrong.txt", Content: `{}`},
		{Filename: "platform.json", Content: `{"access_token":"batch-import-platform-token","platform":"batch-import-platform-secret"}`},
	}})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := authenticatedCodexRequest(app, http.MethodPost, "/api/providers/oauth/codex/import/batch", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMultiStatus {
		t.Fatalf("expected 207, got %d: %s", recorder.Code, recorder.Body.String())
	}
	assertCodexResponseHasNoSecrets(t, recorder.Body.Bytes())
	var response codexOAuthImportBatchResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "partial" || response.Total != 5 || response.Success != 1 || response.Skipped != 1 || response.Failed != 3 || len(response.Results) != 5 {
		t.Fatalf("unexpected batch import response: %+v", response)
	}
	if first := response.Results[0]; first.Filename != "one.json" || first.Status != "success" || first.Imported != 1 || first.Format != "codex" || len(first.Files) != 1 || first.Error != "" {
		t.Fatalf("unexpected successful file result: %+v", first)
	}
	if duplicate := response.Results[1]; duplicate.Filename != "duplicate.json" || duplicate.Status != "skipped" || duplicate.Imported != 0 || duplicate.Skipped != 1 || duplicate.Error != "" {
		t.Fatalf("unexpected skipped file result: %+v", duplicate)
	}
	if broken := response.Results[2]; broken.Status != "failed" || broken.Error == "" {
		t.Fatalf("unexpected malformed file result: %+v", broken)
	}
	if wrong := response.Results[3]; wrong.Status != "failed" || wrong.Error != "僅支援 .json 檔案" {
		t.Fatalf("unexpected wrong-type result: %+v", wrong)
	}
	if platform := response.Results[4]; platform.Status != "failed" || platform.Error != "帳號 platform 不是 OpenAI/Codex" {
		t.Fatalf("unexpected platform result: %+v", platform)
	}
	accounts, err := codexauth.NewStore(codexauth.DefaultStoreDir(home)).ListAccounts()
	if err != nil || len(accounts) != 1 || accounts[0].AccountID != "batch-import-account" {
		t.Fatalf("unexpected imported accounts: %+v err=%v", accounts, err)
	}
	if _, ok := registry.Get(codexauth.DefaultProviderName); !ok {
		t.Fatal("batch import did not register the native Codex provider")
	}

	invalidRecorder := httptest.NewRecorder()
	invalidRequest := authenticatedCodexRequest(app, http.MethodPost, "/api/providers/oauth/codex/import/batch", strings.NewReader(`{"files":[{"filename":"one.json","content":"{}","unknown":true}]}`))
	invalidRequest.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(invalidRecorder, invalidRequest)
	if invalidRecorder.Code != http.StatusBadRequest {
		t.Fatalf("expected strict batch import validation, got %d: %s", invalidRecorder.Code, invalidRecorder.Body.String())
	}
}

func TestNativeCodexBatchMetadataDeleteValidationAndSecretSafety(t *testing.T) {
	home := t.TempDir()
	database, err := db.Open(context.Background(), filepath.Join(home, "autoto.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: home}}, database, nil, nil, providers.NewRegistry())
	payload, _ := json.Marshal(importAuthFileRequest{Filename: "batch.json", Content: `{"format":"sub2api","accounts":[{"name":"one","credentials":{"access_token":"batch-secret-one","refresh_token":"batch-refresh-one","chatgpt_account_id":"batch-account-one"}},{"name":"two","credentials":{"access_token":"batch-secret-two","refresh_token":"batch-refresh-two","chatgpt_account_id":"batch-account-two"}}]}`})
	importRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(importRecorder, authenticatedCodexRequest(app, http.MethodPost, "/api/providers/oauth/codex/import", bytes.NewReader(payload)))
	if importRecorder.Code != http.StatusOK {
		t.Fatalf("import failed: %d %s", importRecorder.Code, importRecorder.Body.String())
	}
	accountsRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(accountsRecorder, authenticatedCodexRequest(app, http.MethodGet, "/api/providers/oauth/codex/accounts", nil))
	var accounts codexOAuthAccountsResponse
	if err := json.Unmarshal(accountsRecorder.Body.Bytes(), &accounts); err != nil || len(accounts.Accounts) != 2 {
		t.Fatalf("unexpected accounts: %+v err=%v", accounts, err)
	}
	idOne := accounts.Accounts[0].ID
	idTwo := accounts.Accounts[1].ID

	tooManyIDs := make([]string, maxCodexOAuthBatchAccounts+1)
	for index := range tooManyIDs {
		tooManyIDs[index] = idOne
	}
	tooManyBody, _ := json.Marshal(codexOAuthAccountsBatchRequest{IDs: tooManyIDs, Operation: "enable"})
	invalidRequests := []string{
		`{"ids":["` + idOne + `"],"operation":"enable","unknown":true}`,
		`{"ids":["` + idOne + `"],"operation":"enable"} {}`,
		`{"ids":["codex_invalid"],"operation":"enable"}`,
		`{"ids":["` + idOne + `"],"operation":"unknown"}`,
		`{"ids":["` + idOne + `"],"operation":"set_priority"}`,
		`{"ids":["` + idOne + `"],"operation":"enable","priority":7}`,
		string(tooManyBody),
		strings.Repeat(" ", maxCodexOAuthBatchBytes+1),
	}
	for index, body := range invalidRequests {
		recorder := httptest.NewRecorder()
		request := authenticatedCodexRequest(app, http.MethodPost, "/api/providers/oauth/codex/accounts/batch", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		app.Routes().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid request %d returned %d: %s", index, recorder.Code, recorder.Body.String())
		}
		assertCodexResponseHasNoSecrets(t, recorder.Body.Bytes())
	}

	priorityBody := `{"ids":["` + idOne + `","` + idOne + `","` + idTwo + `"],"operation":"set_priority","priority":17}`
	priorityRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(priorityRecorder, authenticatedCodexRequest(app, http.MethodPost, "/api/providers/oauth/codex/accounts/batch", strings.NewReader(priorityBody)))
	if priorityRecorder.Code != http.StatusOK {
		t.Fatalf("batch priority failed: %d %s", priorityRecorder.Code, priorityRecorder.Body.String())
	}
	assertCodexResponseHasNoSecrets(t, priorityRecorder.Body.Bytes())
	var priorityResponse codexOAuthAccountsBatchResponse
	if err := json.Unmarshal(priorityRecorder.Body.Bytes(), &priorityResponse); err != nil || priorityResponse.Status != "ok" || priorityResponse.Total != 2 || priorityResponse.Success != 2 || priorityResponse.Failed != 0 {
		t.Fatalf("unexpected priority response: %+v err=%v", priorityResponse, err)
	}
	for _, result := range priorityResponse.Results {
		if !result.Success || result.Error != "" || result.Warning != "" || result.Retryable {
			t.Fatalf("unexpected priority item: %+v", result)
		}
	}
	credentialStore := codexauth.NewStore(codexauth.DefaultStoreDir(home))
	for _, id := range []string{idOne, idTwo} {
		item, err := credentialStore.GetByID(id)
		if err != nil || item.Credential.Priority != 17 {
			t.Fatalf("priority not persisted for %s: %+v err=%v", id, item, err)
		}
	}

	missingID := "codex_MDEyMzQ1Njc4OWFiY2RlZg"
	partialRecorder := httptest.NewRecorder()
	partialBody := `{"ids":["` + idOne + `","` + missingID + `"],"operation":"disable"}`
	app.Routes().ServeHTTP(partialRecorder, authenticatedCodexRequest(app, http.MethodPost, "/api/providers/oauth/codex/accounts/batch", strings.NewReader(partialBody)))
	if partialRecorder.Code != http.StatusMultiStatus {
		t.Fatalf("expected metadata 207, got %d: %s", partialRecorder.Code, partialRecorder.Body.String())
	}
	var partialResponse codexOAuthAccountsBatchResponse
	if err := json.Unmarshal(partialRecorder.Body.Bytes(), &partialResponse); err != nil || partialResponse.Status != "partial" || partialResponse.Success != 1 || partialResponse.Failed != 1 || partialResponse.Results[1].Retryable {
		t.Fatalf("unexpected partial metadata response: %+v err=%v", partialResponse, err)
	}
	if item, err := credentialStore.GetByID(idOne); err != nil || !item.Credential.Disabled {
		t.Fatalf("disable not persisted: %+v err=%v", item, err)
	}
	enableRecorder := httptest.NewRecorder()
	enableBody := `{"ids":["` + idOne + `","` + idTwo + `"],"operation":"enable"}`
	app.Routes().ServeHTTP(enableRecorder, authenticatedCodexRequest(app, http.MethodPost, "/api/providers/oauth/codex/accounts/batch", strings.NewReader(enableBody)))
	if enableRecorder.Code != http.StatusOK {
		t.Fatalf("batch enable failed: %d %s", enableRecorder.Code, enableRecorder.Body.String())
	}
	for _, id := range []string{idOne, idTwo} {
		item, err := credentialStore.GetByID(id)
		if err != nil || item.Credential.Disabled {
			t.Fatalf("enable not persisted for %s: %+v err=%v", id, item, err)
		}
	}

	for _, id := range []string{idOne, idTwo} {
		if _, err := database.DB().Exec(`INSERT INTO provider_account_stats (provider, account_id, success_count) VALUES ('codex', ?, 1)`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.DB().Exec(`INSERT INTO api_requests (id, provider, credential_id, created_at) VALUES ('batch-history', 'codex', ?, ?)`, idOne, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(`CREATE TRIGGER fail_second_codex_stats_delete BEFORE DELETE ON provider_account_stats WHEN OLD.account_id = '` + idTwo + `' BEGIN SELECT RAISE(ABORT, 'fixture cleanup failure'); END;`); err != nil {
		t.Fatal(err)
	}
	deleteRecorder := httptest.NewRecorder()
	deleteBody := `{"ids":["` + idOne + `","` + idTwo + `"],"operation":"delete"}`
	app.Routes().ServeHTTP(deleteRecorder, authenticatedCodexRequest(app, http.MethodPost, "/api/providers/oauth/codex/accounts/batch", strings.NewReader(deleteBody)))
	if deleteRecorder.Code != http.StatusMultiStatus {
		t.Fatalf("expected delete 207, got %d: %s", deleteRecorder.Code, deleteRecorder.Body.String())
	}
	assertCodexResponseHasNoSecrets(t, deleteRecorder.Body.Bytes())
	var deleteResponse codexOAuthAccountsBatchResponse
	if err := json.Unmarshal(deleteRecorder.Body.Bytes(), &deleteResponse); err != nil || deleteResponse.Status != "partial" || deleteResponse.Total != 2 || deleteResponse.Success != 1 || deleteResponse.Failed != 1 {
		t.Fatalf("unexpected delete response: %+v err=%v", deleteResponse, err)
	}
	byID := map[string]codexOAuthAccountsBatchResult{}
	for _, result := range deleteResponse.Results {
		byID[result.ID] = result
	}
	if !byID[idOne].Success || byID[idOne].Retryable || byID[idOne].Warning != "" {
		t.Fatalf("unexpected successful delete result: %+v", byID[idOne])
	}
	if byID[idTwo].Success || !byID[idTwo].Retryable || byID[idTwo].Warning == "" || byID[idTwo].Error != "" {
		t.Fatalf("unexpected cleanup warning result: %+v", byID[idTwo])
	}
	stored, err := codexauth.NewStore(codexauth.DefaultStoreDir(home)).Load()
	if err != nil || len(stored) != 0 {
		t.Fatalf("batch delete retained credentials: %+v err=%v", stored, err)
	}
	stats, err := database.ListProviderAccountStats(context.Background(), codexauth.DefaultProviderName)
	if err != nil || len(stats) != 1 {
		t.Fatalf("unexpected remaining stats: %+v err=%v", stats, err)
	}
	var historyCount int
	if err := database.DB().QueryRow(`SELECT COUNT(*) FROM api_requests WHERE id = 'batch-history'`).Scan(&historyCount); err != nil || historyCount != 1 {
		t.Fatalf("api_requests history was removed: count=%d err=%v", historyCount, err)
	}
}

func TestNativeCodexBatchSyncIsSequentialAndPerAccountBounded(t *testing.T) {
	var mu sync.Mutex
	active := 0
	maxActive := 0
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		active++
		calls++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()
		time.Sleep(15 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		_, _ = w.Write([]byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":5}}}`))
	}))
	defer upstream.Close()

	home := t.TempDir()
	app := New(config.Config{
		Paths: config.PathsConfig{HomeDir: home},
		Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
			Name: "codex", Type: config.ProviderTypeCodex, BaseURL: upstream.URL + "/backend-api/codex", Model: "gpt-test", CodexAllowInsecureTestEndpoint: true,
		}}},
	}, nil, nil, nil, providers.NewRegistry())
	payload, _ := json.Marshal(importAuthFileRequest{Filename: "sync.json", Content: `{"format":"sub2api","accounts":[{"name":"one","credentials":{"access_token":"sync-secret-one","chatgpt_account_id":"sync-account-one"}},{"name":"two","credentials":{"access_token":"sync-secret-two","chatgpt_account_id":"sync-account-two"}}]}`})
	importRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(importRecorder, authenticatedCodexRequest(app, http.MethodPost, "/api/providers/oauth/codex/import", bytes.NewReader(payload)))
	if importRecorder.Code != http.StatusOK {
		t.Fatalf("import failed: %d %s", importRecorder.Code, importRecorder.Body.String())
	}
	accounts, err := codexauth.NewStore(codexauth.DefaultStoreDir(home)).ListAccounts()
	if err != nil || len(accounts) != 2 {
		t.Fatalf("list failed: %+v err=%v", accounts, err)
	}
	body, _ := json.Marshal(codexOAuthAccountsBatchRequest{IDs: []string{accounts[0].ID, accounts[1].ID}, Operation: "sync"})
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, authenticatedCodexRequest(app, http.MethodPost, "/api/providers/oauth/codex/accounts/batch", bytes.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("batch sync failed: %d %s", recorder.Code, recorder.Body.String())
	}
	assertCodexResponseHasNoSecrets(t, recorder.Body.Bytes())
	var response codexOAuthAccountsBatchResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Status != "ok" || response.Success != 2 || response.Failed != 0 {
		t.Fatalf("unexpected sync response: %+v err=%v", response, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 || maxActive != 1 {
		t.Fatalf("batch sync was not sequential: calls=%d maxActive=%d", calls, maxActive)
	}
}

func TestNativeCodexSyncPersistsQuotaUnauthorizedStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			w.WriteHeader(http.StatusUnauthorized)
		case "/oauth/token":
			_, _ = w.Write([]byte(`{"access_token":"server-quota-refreshed","expires_in":3600}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	home := t.TempDir()
	database, err := db.Open(context.Background(), filepath.Join(home, "autoto.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	app := New(config.Config{
		Paths: config.PathsConfig{HomeDir: home},
		Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
			Name: "codex", Type: config.ProviderTypeCodex, BaseURL: upstream.URL + "/backend-api/codex", Model: "gpt-test",
			CodexAllowInsecureTestEndpoint: true, CodexRefreshURLForTest: upstream.URL + "/oauth/token",
		}}},
	}, database, nil, nil, providers.NewRegistry())

	payload, _ := json.Marshal(importAuthFileRequest{
		Filename: "quota-status.json",
		Content:  `{"type":"codex","access_token":"server-quota-old","refresh_token":"server-quota-refresh","account_id":"server-quota-account"}`,
	})
	importRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(importRecorder, authenticatedCodexRequest(app, http.MethodPost, "/api/providers/oauth/codex/import", bytes.NewReader(payload)))
	if importRecorder.Code != http.StatusOK {
		t.Fatalf("import failed: %d %s", importRecorder.Code, importRecorder.Body.String())
	}

	listRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(listRecorder, authenticatedCodexRequest(app, http.MethodGet, "/api/providers/oauth/codex/accounts", nil))
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("initial list failed: %d %s", listRecorder.Code, listRecorder.Body.String())
	}
	var listed codexOAuthAccountsResponse
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &listed); err != nil || len(listed.Accounts) != 1 {
		t.Fatalf("unexpected initial account list: %+v err=%v", listed, err)
	}
	id := listed.Accounts[0].ID

	refreshRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(refreshRecorder, authenticatedCodexRequest(app, http.MethodPost, "/api/providers/oauth/codex/accounts/"+id+"/refresh", nil))
	if refreshRecorder.Code != http.StatusBadGateway {
		t.Fatalf("expected quota refresh failure, got %d: %s", refreshRecorder.Code, refreshRecorder.Body.String())
	}
	assertCodexResponseHasNoSecrets(t, refreshRecorder.Body.Bytes())

	listRecorder = httptest.NewRecorder()
	app.Routes().ServeHTTP(listRecorder, authenticatedCodexRequest(app, http.MethodGet, "/api/providers/oauth/codex/accounts", nil))
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("post-sync list failed: %d %s", listRecorder.Code, listRecorder.Body.String())
	}
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &listed); err != nil || len(listed.Accounts) != 1 || listed.Accounts[0].Stats == nil {
		t.Fatalf("quota status was not returned with account: %+v err=%v", listed, err)
	}
	if listed.Accounts[0].Stats.LastErrorCode != "quota_unauthorized" || listed.Accounts[0].Stats.LastStatusCode != "quota_unauthorized" || listed.Accounts[0].Stats.LastHTTPStatus != http.StatusUnauthorized {
		t.Fatalf("unexpected persisted quota status: %+v", listed.Accounts[0].Stats)
	}
}

func assertCodexResponseHasNoSecrets(t *testing.T, body []byte) {
	t.Helper()
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	var visit func(any)
	visit = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
				for _, forbidden := range []string{"accesstoken", "refreshtoken", "idtoken", "authorization", "cookie", "jwt"} {
					if strings.Contains(normalized, forbidden) {
						t.Fatalf("sensitive key %q leaked in response: %s", key, body)
					}
				}
				visit(child)
			}
		case []any:
			for _, child := range typed {
				visit(child)
			}
		case string:
			for _, secret := range []string{
				"fixture-management-access", "rt_management_fixture",
				"batch-secret-one", "batch-refresh-one", "batch-secret-two", "batch-refresh-two",
				"batch-import-secret", "batch-import-refresh", "batch-import-platform-token", "batch-import-platform-secret",
				"sync-secret-one", "sync-secret-two",
			} {
				if strings.Contains(typed, secret) {
					t.Fatalf("secret value leaked in response: %s", body)
				}
			}
		}
	}
	visit(value)
}

func TestNativeCodexImportFeedsModelCatalogWithoutProxy(t *testing.T) {
	var modelRequests int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		modelRequests++
		if r.Header.Get("Authorization") != "Bearer fixture-access" || r.Header.Get("ChatGPT-Account-ID") != "account-1" {
			t.Fatalf("unexpected direct Codex headers: authorization=%q account=%q", r.Header.Get("Authorization"), r.Header.Get("ChatGPT-Account-ID"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-native"}]}`))
	}))
	defer upstream.Close()

	home := t.TempDir()
	cfg := config.Config{
		Paths: config.PathsConfig{HomeDir: home},
		Agent: config.AgentConfig{DefaultModel: "codex:gpt-native"},
		Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
			Name: "codex", Type: config.ProviderTypeCodex, BaseURL: upstream.URL, Model: "gpt-native", CodexAllowInsecureTestEndpoint: true,
		}}},
	}
	registry := providers.NewRegistry()
	app := New(cfg, nil, nil, nil, registry)
	payload, err := json.Marshal(importAuthFileRequest{Content: `{"access_token":"fixture-access","account_id":"account-1"}`})
	if err != nil {
		t.Fatal(err)
	}
	importRecorder := httptest.NewRecorder()
	importRequest := authenticatedCodexRequest(app, http.MethodPost, "/api/providers/oauth/codex/import", bytes.NewReader(payload))
	importRequest.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(importRecorder, importRequest)
	if importRecorder.Code != http.StatusOK {
		t.Fatalf("expected import 200, got %d: %s", importRecorder.Code, importRecorder.Body.String())
	}

	modelsRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(modelsRecorder, newTestRequest(http.MethodGet, "/api/models", nil))
	if modelsRecorder.Code != http.StatusOK {
		t.Fatalf("expected models 200, got %d: %s", modelsRecorder.Code, modelsRecorder.Body.String())
	}
	if modelRequests != 1 {
		t.Fatalf("expected one direct model request, got %d", modelRequests)
	}
	var response modelsResponse
	if err := json.NewDecoder(modelsRecorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Providers) != 1 || response.Providers[0].Name != "codex" || !response.Providers[0].Configured || len(response.Providers[0].Models) != 1 || response.Providers[0].Models[0] != "gpt-native" {
		t.Fatalf("unexpected model catalog response: %+v", response)
	}

	registered, ok := registry.Get("codex")
	if !ok {
		t.Fatal("Codex provider not registered")
	}
	models, err := registered.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0] != "gpt-native" {
		t.Fatalf("registered provider cannot reuse local credentials: models=%v err=%v", models, err)
	}
}

func TestNativeCodexImportDoesNotReactivateDisabledProvider(t *testing.T) {
	home := t.TempDir()
	codex := config.ProviderConfig{Name: "codex", Type: config.ProviderTypeCodex, BaseURL: codexauth.DefaultBaseURL, Model: "gpt-test", Disabled: true}
	registry := providers.NewRegistry()
	registry.Register(providers.NewCodexProvider(config.ProviderConfig{
		Name: "codex", Type: config.ProviderTypeCodex, BaseURL: codexauth.DefaultBaseURL, Model: "gpt-test", CredentialStorePath: codexauth.DefaultStoreDir(home),
	}))
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: home}, Agent: config.AgentConfig{DefaultModel: "codex:gpt-test"}, Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{codex}}}, nil, nil, nil, registry)
	payload, err := json.Marshal(importAuthFileRequest{Content: `{"access_token":"fixture-disabled-access","account_id":"disabled-account"}`})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := authenticatedCodexRequest(app, http.MethodPost, "/api/providers/oauth/codex/import", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("disabled Codex import should still store credentials: %d %s", recorder.Code, recorder.Body.String())
	}
	if _, ok := registry.Get("codex"); ok {
		t.Fatal("credential import re-registered disabled Codex provider")
	}
	stored, ok := app.providerConfig("codex")
	if !ok || !stored.Disabled {
		t.Fatalf("import changed disabled Codex config: %+v", stored)
	}
}
