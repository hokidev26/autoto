package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
)

func authenticatedAnthropicRequest(app *Server, method, target string, body *bytes.Reader) *http.Request {
	var request *http.Request
	if body == nil {
		request = newTestRequest(method, target, nil)
	} else {
		request = newTestRequest(method, target, body)
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set(localTokenHeader, app.localToken)
	return request
}

func TestAnthropicAccountRoutesRequireSensitiveTokenAndSameOrigin(t *testing.T) {
	home := t.TempDir()
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: home}}, nil, nil, nil, providers.NewRegistry())

	missing := httptest.NewRecorder()
	app.Routes().ServeHTTP(missing, newTestRequest(http.MethodGet, "/api/providers/auth/anthropic/accounts", nil))
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without local token, got %d: %s", missing.Code, missing.Body.String())
	}

	crossRequest := authenticatedAnthropicRequest(app, http.MethodGet, "/api/providers/auth/anthropic/accounts", nil)
	crossRequest.Host = "localhost:7788"
	crossRequest.Header.Set("Origin", "https://evil.example")
	cross := httptest.NewRecorder()
	app.Routes().ServeHTTP(cross, crossRequest)
	if cross.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-origin credential request, got %d: %s", cross.Code, cross.Body.String())
	}
}

func TestAnthropicAccountCRUDSyncAndSecretRedaction(t *testing.T) {
	secret := "sk-ant-api03-server-fixture-secret"
	var modelRequests int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected Anthropic path %s", r.URL.Path)
		}
		modelRequests++
		if got := r.Header.Get("X-Api-Key"); got != secret {
			t.Fatalf("unexpected Anthropic API key header %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("anthropic-ratelimit-requests-limit", "100")
		w.Header().Set("anthropic-ratelimit-requests-remaining", "73")
		w.Header().Set("anthropic-ratelimit-requests-reset", "2026-07-16T12:30:00Z")
		w.Header().Set("anthropic-ratelimit-input-tokens-limit", "10000")
		w.Header().Set("anthropic-ratelimit-input-tokens-remaining", "9000")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-test","type":"model","display_name":"Claude Test","created_at":"2026-07-01T00:00:00Z","max_input_tokens":200000,"max_tokens":8192,"capabilities":{}}],"has_more":false,"first_id":"claude-test","last_id":"claude-test"}`))
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
			Name: "anthropic", Type: "anthropic", BaseURL: upstream.URL, Model: "claude-test", MaxTokens: 128,
		}}},
	}, database, nil, nil, registry)

	createBody := bytes.NewReader([]byte(`{"authType":"api_key","apiKey":"` + secret + `","alias":"Primary","priority":7}`))
	create := httptest.NewRecorder()
	app.Routes().ServeHTTP(create, authenticatedAnthropicRequest(app, http.MethodPost, "/api/providers/auth/anthropic/accounts", createBody))
	if create.Code != http.StatusCreated {
		t.Fatalf("create failed: %d %s", create.Code, create.Body.String())
	}
	assertAnthropicResponseHasNoSecret(t, create.Body.Bytes(), secret)
	var created map[string]any
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id, _ := created["id"].(string)
	if id == "" || created["auth_type"] != "api_key" || created["managed"] != true {
		t.Fatalf("unexpected create response: %+v", created)
	}

	patchBody := bytes.NewReader([]byte(`{"alias":"Edited","priority":3,"disabled":false}`))
	patch := httptest.NewRecorder()
	app.Routes().ServeHTTP(patch, authenticatedAnthropicRequest(app, http.MethodPatch, "/api/providers/auth/anthropic/accounts/"+id, patchBody))
	if patch.Code != http.StatusOK || !strings.Contains(patch.Body.String(), `"priority":3`) {
		t.Fatalf("patch failed: %d %s", patch.Code, patch.Body.String())
	}
	assertAnthropicResponseHasNoSecret(t, patch.Body.Bytes(), secret)

	syncResult := httptest.NewRecorder()
	app.Routes().ServeHTTP(syncResult, authenticatedAnthropicRequest(app, http.MethodPost, "/api/providers/auth/anthropic/accounts/"+id+"/sync", nil))
	if syncResult.Code != http.StatusOK {
		t.Fatalf("sync failed: %d %s", syncResult.Code, syncResult.Body.String())
	}
	assertAnthropicResponseHasNoSecret(t, syncResult.Body.Bytes(), secret)
	if !strings.Contains(syncResult.Body.String(), "claude-test") || !strings.Contains(syncResult.Body.String(), `"remaining":"73"`) {
		t.Fatalf("sync response missing models/rate limits: %s", syncResult.Body.String())
	}
	if modelRequests != 1 {
		t.Fatalf("expected one model sync request, got %d", modelRequests)
	}

	list := httptest.NewRecorder()
	app.Routes().ServeHTTP(list, authenticatedAnthropicRequest(app, http.MethodGet, "/api/providers/auth/anthropic/accounts", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "Edited") || !strings.Contains(list.Body.String(), "claude-test") {
		t.Fatalf("list failed: %d %s", list.Code, list.Body.String())
	}
	assertAnthropicResponseHasNoSecret(t, list.Body.Bytes(), secret)

	stats, err := database.GetProviderAccountStats(context.Background(), "anthropic", id)
	if err != nil || len(stats.QuotaSnapshotJSON) == 0 {
		t.Fatalf("rate-limit snapshot was not persisted: %+v err=%v", stats, err)
	}

	if err := database.SetGatewayAccountGrant(context.Background(), "anthropic", id, true); err != nil {
		t.Fatal(err)
	}
	remove := httptest.NewRecorder()
	app.Routes().ServeHTTP(remove, authenticatedAnthropicRequest(app, http.MethodDelete, "/api/providers/auth/anthropic/accounts/"+id, nil))
	if remove.Code != http.StatusOK || !strings.Contains(remove.Body.String(), `"credential_deleted":true`) {
		t.Fatalf("delete failed: %d %s", remove.Code, remove.Body.String())
	}
	if remaining, err := database.ListProviderAccountStats(context.Background(), "anthropic"); err != nil || len(remaining) != 0 {
		t.Fatalf("Anthropic account stats were not cleaned: %+v err=%v", remaining, err)
	}
	if grants, err := database.ListGatewayAccountGrants(context.Background(), "anthropic"); err != nil || len(grants) != 0 {
		t.Fatalf("Anthropic Gateway grant was not cleaned with the credential: %+v err=%v", grants, err)
	}
}

func TestAnthropicAccountListShowsLegacyConfigAsReadOnlyFallback(t *testing.T) {
	secret := "sk-ant-legacy-secret"
	home := t.TempDir()
	app := New(config.Config{
		Paths: config.PathsConfig{HomeDir: home},
		Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
			Name: "anthropic", Type: "anthropic", APIKey: secret, Model: "claude-test",
		}}},
	}, nil, nil, nil, providers.NewRegistry())

	result := httptest.NewRecorder()
	app.Routes().ServeHTTP(result, authenticatedAnthropicRequest(app, http.MethodGet, "/api/providers/auth/anthropic/accounts", nil))
	if result.Code != http.StatusOK {
		t.Fatalf("list failed: %d %s", result.Code, result.Body.String())
	}
	assertAnthropicResponseHasNoSecret(t, result.Body.Bytes(), secret)
	if !strings.Contains(result.Body.String(), `"id":"configured"`) || !strings.Contains(result.Body.String(), `"managed":false`) || !strings.Contains(result.Body.String(), `"source":"configured"`) {
		t.Fatalf("legacy fallback was not exposed as a read-only account: %s", result.Body.String())
	}
}

func TestAnthropicAccountCreateDoesNotReenableDisabledProvider(t *testing.T) {
	home := t.TempDir()
	registry := providers.NewRegistry()
	app := New(config.Config{
		Paths: config.PathsConfig{HomeDir: home},
		Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
			Name: "anthropic", Type: "anthropic", Disabled: true, Model: "claude-test",
		}}},
	}, nil, nil, nil, registry)
	body := bytes.NewReader([]byte(`{"authType":"profile","profile":"work-profile"}`))
	result := httptest.NewRecorder()
	app.Routes().ServeHTTP(result, authenticatedAnthropicRequest(app, http.MethodPost, "/api/providers/auth/anthropic/accounts", body))
	if result.Code != http.StatusCreated {
		t.Fatalf("disabled provider should still store account: %d %s", result.Code, result.Body.String())
	}
	if _, ok := registry.Get("anthropic"); ok {
		t.Fatal("adding an Anthropic account re-enabled a disabled provider")
	}
}

func assertAnthropicResponseHasNoSecret(t *testing.T, body []byte, secrets ...string) {
	t.Helper()
	text := string(body)
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			t.Fatalf("Anthropic secret leaked in response: %s", text)
		}
	}
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
				for _, forbidden := range []string{"apikey", "accesstoken", "refreshtoken", "authorization", "cookie", "clientsecret"} {
					if strings.Contains(normalized, forbidden) {
						t.Fatalf("sensitive key %q leaked in response: %s", key, text)
					}
				}
				visit(child)
			}
		case []any:
			for _, child := range typed {
				visit(child)
			}
		}
	}
	visit(value)
}
