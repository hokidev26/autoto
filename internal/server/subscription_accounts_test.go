package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/subscriptionauth"
)

func TestSubscriptionAccountListDoesNotLeakTokensAndIncludesStats(t *testing.T) {
	home := t.TempDir()
	database, err := db.Open(context.Background(), filepath.Join(home, "subscription-list.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: home}}, database, nil, nil, providers.NewRegistry())
	store, err := app.nativeSubscriptionCredentialStore(subscriptionauth.ProviderGemini)
	if err != nil {
		t.Fatal(err)
	}
	const accessToken = "access-list-secret"
	const refreshToken = "refresh-list-secret"
	const idToken = "id-list-secret"
	created, err := store.CreateOAuth(subscriptionauth.CreateRequest{
		Provider:     subscriptionauth.ProviderGemini,
		Alias:        "主账号",
		Priority:     7,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		IDToken:      idToken,
		ExpiresAt:    time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
		Email:        "gemini@example.test",
		Subject:      "subject-1",
		ProjectID:    "project-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.RecordProviderAccountAttempt(context.Background(), providers.ProviderAccountAttempt{
		Provider: subscriptionauth.ProviderGemini, AccountID: created.ID, Success: true, HTTPStatus: http.StatusOK, AttemptedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	app.listSubscriptionAccounts(response, subscriptionRequest(http.MethodGet, subscriptionauth.ProviderGemini, "", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("列表失败：%d %s", response.Code, response.Body.String())
	}
	assertSubscriptionResponseHasNoSecrets(t, response.Body.Bytes(), accessToken, refreshToken, idToken)
	var payload struct {
		Accounts []struct {
			subscriptionauth.AccountSummary
			Stats *db.ProviderAccountStats `json:"stats"`
		} `json:"accounts"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Count != 1 || len(payload.Accounts) != 1 {
		t.Fatalf("账号数量异常：%+v", payload)
	}
	account := payload.Accounts[0]
	if account.ID != created.ID || account.Alias != "主账号" || account.Email != "gemini@example.test" {
		t.Fatalf("账号摘要异常：%+v", account)
	}
	if account.Stats == nil || account.Stats.SuccessCount != 1 {
		t.Fatalf("账号统计未附加：%+v", account.Stats)
	}
}

func TestSubscriptionAccountPatchUpdatesOnlyMetadataAndUsesStrictJSON(t *testing.T) {
	home := t.TempDir()
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: home}}, nil, nil, nil, providers.NewRegistry())
	store, err := app.nativeSubscriptionCredentialStore(subscriptionauth.ProviderKimi)
	if err != nil {
		t.Fatal(err)
	}
	const accessToken = "access-patch-secret"
	const refreshToken = "refresh-patch-secret"
	created, err := store.CreateOAuth(subscriptionauth.CreateRequest{
		Provider: subscriptionauth.ProviderKimi, Alias: "原账号", Priority: 100,
		AccessToken: accessToken, RefreshToken: refreshToken, DeviceID: "device-patch",
	})
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"alias":"编辑后","priority":3,"disabled":true}`)
	app.patchSubscriptionAccount(response, subscriptionRequest(http.MethodPatch, subscriptionauth.ProviderKimi, created.ID, body))
	if response.Code != http.StatusOK {
		t.Fatalf("Patch 失败：%d %s", response.Code, response.Body.String())
	}
	assertSubscriptionResponseHasNoSecrets(t, response.Body.Bytes(), accessToken, refreshToken)
	updated, err := store.GetByID(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Alias != "编辑后" || updated.Priority != 3 || !updated.Disabled {
		t.Fatalf("元数据未更新：%+v", updated.Credential)
	}
	if updated.AccessToken != accessToken || updated.RefreshToken != refreshToken || updated.DeviceID != "device-patch" {
		t.Fatal("Patch 修改了 OAuth 凭据字段")
	}

	unknown := httptest.NewRecorder()
	app.patchSubscriptionAccount(unknown, subscriptionRequest(http.MethodPatch, subscriptionauth.ProviderKimi, created.ID, strings.NewReader(`{"alias":"x","access_token":"forbidden"}`)))
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("未知字段应返回 400，实际 %d：%s", unknown.Code, unknown.Body.String())
	}
	trailing := httptest.NewRecorder()
	app.patchSubscriptionAccount(trailing, subscriptionRequest(http.MethodPatch, subscriptionauth.ProviderKimi, created.ID, strings.NewReader(`{"alias":"x"}{"disabled":false}`)))
	if trailing.Code != http.StatusBadRequest {
		t.Fatalf("多个 JSON 对象应返回 400，实际 %d：%s", trailing.Code, trailing.Body.String())
	}
}

func TestSubscriptionAccountDeleteCleansStatsAndGatewayGrantIdempotently(t *testing.T) {
	home := t.TempDir()
	database, err := db.Open(context.Background(), filepath.Join(home, "subscription-delete.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: home}}, database, nil, nil, providers.NewRegistry())
	store, err := app.nativeSubscriptionCredentialStore(subscriptionauth.ProviderGrok)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateOAuth(subscriptionauth.CreateRequest{
		Provider: subscriptionauth.ProviderGrok, Priority: 10,
		AccessToken: "access-delete-secret", RefreshToken: "refresh-delete-secret", TokenEndpoint: "https://auth.x.ai/oauth/token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.RecordProviderAccountAttempt(context.Background(), providers.ProviderAccountAttempt{
		Provider: subscriptionauth.ProviderGrok, AccountID: created.ID, Success: false, HTTPStatus: http.StatusUnauthorized, AttemptedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetGatewayAccountGrant(context.Background(), subscriptionauth.ProviderGrok, created.ID, true); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	app.deleteSubscriptionAccount(response, subscriptionRequest(http.MethodDelete, subscriptionauth.ProviderGrok, created.ID, nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"credential_deleted":true`) {
		t.Fatalf("删除失败：%d %s", response.Code, response.Body.String())
	}
	if _, err := store.GetByID(created.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("凭据仍存在：%v", err)
	}
	if stats, err := database.ListProviderAccountStats(context.Background(), subscriptionauth.ProviderGrok); err != nil || len(stats) != 0 {
		t.Fatalf("统计未清理：%+v err=%v", stats, err)
	}
	if grants, err := database.ListGatewayAccountGrants(context.Background(), subscriptionauth.ProviderGrok); err != nil || len(grants) != 0 {
		t.Fatalf("Gateway 授权未清理：%+v err=%v", grants, err)
	}

	retry := httptest.NewRecorder()
	app.deleteSubscriptionAccount(retry, subscriptionRequest(http.MethodDelete, subscriptionauth.ProviderGrok, created.ID, nil))
	if retry.Code != http.StatusOK || !strings.Contains(retry.Body.String(), `"already_missing":true`) {
		t.Fatalf("幂等删除失败：%d %s", retry.Code, retry.Body.String())
	}
}

func TestSubscriptionAccountSyncRequiresRefreshTokenWithoutNetwork(t *testing.T) {
	home := t.TempDir()
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: home}}, nil, nil, nil, providers.NewRegistry())
	store, err := app.nativeSubscriptionCredentialStore(subscriptionauth.ProviderKimi)
	if err != nil {
		t.Fatal(err)
	}
	const accessToken = "access-sync-secret"
	created, err := store.CreateOAuth(subscriptionauth.CreateRequest{
		Provider: subscriptionauth.ProviderKimi, AccessToken: accessToken, DeviceID: "device-sync",
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	app.syncSubscriptionAccount(response, subscriptionRequest(http.MethodPost, subscriptionauth.ProviderKimi, created.ID, nil))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "缺少 refresh token") {
		t.Fatalf("缺少 refresh token 未返回明确 400：%d %s", response.Code, response.Body.String())
	}
	assertSubscriptionResponseHasNoSecrets(t, response.Body.Bytes(), accessToken)
}

func TestSubscriptionAccountRejectsInvalidProvider(t *testing.T) {
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: t.TempDir()}}, nil, nil, nil, providers.NewRegistry())
	response := httptest.NewRecorder()
	app.listSubscriptionAccounts(response, subscriptionRequest(http.MethodGet, "codex", "", nil))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "僅支援") {
		t.Fatalf("非法 Provider 未返回中文 400：%d %s", response.Code, response.Body.String())
	}
	if _, err := app.nativeSubscriptionCredentialStore("anthropic"); err == nil {
		t.Fatal("非法 Provider 不应创建凭据 Store")
	}
	if err := app.ensureNativeSubscriptionProvider("other"); err == nil {
		t.Fatal("非法 Provider 不应注册")
	}
}

func TestSubscriptionEnsureNativeProviderDoesNotRestartDisabledProvider(t *testing.T) {
	home := t.TempDir()
	registry := providers.NewRegistry()
	activeConfig := config.NormalizeProviderConfig(config.ProviderConfig{
		Name: subscriptionauth.ProviderGemini, Type: config.ProviderTypeGemini,
		CredentialStorePath: subscriptionauth.DefaultStoreDir(home, subscriptionauth.ProviderGemini),
	})
	registry.Register(providers.NewGeminiProvider(activeConfig))
	app := New(config.Config{
		Paths: config.PathsConfig{HomeDir: home},
		Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
			Name: subscriptionauth.ProviderGemini, Type: config.ProviderTypeGemini, Disabled: true,
		}}},
	}, nil, nil, nil, registry)

	if err := app.ensureNativeSubscriptionProvider(subscriptionauth.ProviderGemini); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get(subscriptionauth.ProviderGemini); ok {
		t.Fatal("ensure 重新启动了已禁用的 Gemini Provider")
	}
	provider, ok := app.providerConfig(subscriptionauth.ProviderGemini)
	if !ok || !provider.Disabled {
		t.Fatalf("禁用配置被改写：%+v", provider)
	}
}

func TestSubscriptionEnsureNativeProviderUsesFallbackForOccupiedName(t *testing.T) {
	home := t.TempDir()
	registry := providers.NewRegistry()
	app := New(config.Config{
		Paths: config.PathsConfig{HomeDir: home},
		Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
			Name: subscriptionauth.ProviderKimi, Type: "openai-compatible", BaseURL: "https://example.test/v1", Model: "fixture",
		}}},
	}, nil, nil, nil, registry)
	if err := app.ensureNativeSubscriptionProvider(subscriptionauth.ProviderKimi); err != nil {
		t.Fatalf("名称占用时应使用备用名称注册：%v", err)
	}
	provider, ok := app.providerConfig("kimi-oauth")
	if !ok || provider.Type != config.ProviderTypeKimi {
		t.Fatalf("未创建原生 Kimi 备用 Provider：%+v", provider)
	}
	if _, ok := registry.Get("kimi-oauth"); !ok {
		t.Fatal("原生 Kimi 备用 Provider 未注册到运行时")
	}
	legacy, ok := app.providerConfig(subscriptionauth.ProviderKimi)
	if !ok || legacy.Type != "openai-compatible" {
		t.Fatalf("同名旧 Provider 被意外改写：%+v", legacy)
	}
}

func subscriptionRequest(method, provider, id string, body io.Reader) *http.Request {
	request := newTestRequest(method, "/", body)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("provider", provider)
	if id != "" {
		routeContext.URLParams.Add("id", id)
	}
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}

func assertSubscriptionResponseHasNoSecrets(t *testing.T, body []byte, secrets ...string) {
	t.Helper()
	text := string(body)
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			t.Fatalf("响应泄露订阅凭据：%s", text)
		}
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	var visit func(any)
	visit = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
				switch normalized {
				case "accesstoken", "refreshtoken", "idtoken", "devicecode", "authorization", "clientsecret":
					t.Fatalf("响应包含敏感字段 %q：%s", key, text)
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
