package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"autoto/internal/codexauth"
	"autoto/internal/config"
	"autoto/internal/db"
	gatewaypkg "autoto/internal/gateway"
	"autoto/internal/subscriptionauth"
)

func TestGatewayAdminRoutesRequireSensitiveToken(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	routes := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/gateway/status", ""},
		{http.MethodPatch, "/api/gateway/config", `{}`},
		{http.MethodGet, "/api/gateway/accounts", ""},
		{http.MethodPatch, "/api/gateway/accounts/codex/account-1", `{}`},
		{http.MethodGet, "/api/gateway/requests", ""},
		{http.MethodGet, "/api/gateway/keys", ""},
		{http.MethodPost, "/api/gateway/keys", `{}`},
		{http.MethodPatch, "/api/gateway/keys/key-1", `{}`},
		{http.MethodPost, "/api/gateway/keys/key-1/rotate", ""},
		{http.MethodPost, "/api/gateway/keys/key-1/revoke", ""},
		{http.MethodGet, "/api/gateway/models", ""},
		{http.MethodPost, "/api/gateway/models", `{}`},
		{http.MethodPatch, "/api/gateway/models?alias=model-1", `{}`},
		{http.MethodDelete, "/api/gateway/models?alias=model-1", ""},
		{http.MethodPatch, "/api/gateway/models/model-1", `{}`},
		{http.MethodDelete, "/api/gateway/models/model-1", ""},
		{http.MethodGet, "/api/gateway/usage", ""},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			request := newTestRequest(route.method, route.path, strings.NewReader(route.body))
			response := httptest.NewRecorder()
			app.Routes().ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 without sensitive token, got %d: %s", response.Code, response.Body.String())
			}

			legacyRequest := newTestRequest(route.method, route.path, strings.NewReader(route.body))
			legacyRequest.Header.Set(legacyLocalTokenHeader, app.localToken)
			legacyResponse := httptest.NewRecorder()
			app.Routes().ServeHTTP(legacyResponse, legacyRequest)
			if legacyResponse.Code != http.StatusUnauthorized {
				t.Fatalf("expected legacy token rejection, got %d: %s", legacyResponse.Code, legacyResponse.Body.String())
			}
		})
	}
}

func TestGatewayBearerTokenCannotAuthenticateAdminRoutes(t *testing.T) {
	app, store := newGatewayAdminTestServer(t, config.Config{})
	generated, err := gatewaypkg.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateGatewayKey(context.Background(), db.GatewayKey{Name: "client", KeyPrefix: generated.Prefix, TokenHash: generated.Hash, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	request := newTestRequest(http.MethodGet, "/api/gateway/keys", nil)
	request.Header.Set("Authorization", "Bearer "+generated.Token)
	response := httptest.NewRecorder()
	app.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized && response.Code != http.StatusForbidden {
		t.Fatalf("Gateway bearer token authenticated an admin route: %d %s", response.Code, response.Body.String())
	}
}

func TestGatewayKeyAdminLifecycleAndOneTimeTokens(t *testing.T) {
	app, store := newGatewayAdminTestServer(t, config.Config{})

	createdResponse := gatewayAdminJSONRequest(t, app, http.MethodPost, "/api/gateway/keys", map[string]any{
		"name": "Laptop",
	}, http.StatusCreated)
	var created struct {
		Key   db.GatewayKey `json:"key"`
		Token string        `json:"token"`
	}
	decodeGatewayAdminResponse(t, createdResponse, &created)
	if !strings.Contains(createdResponse.Header().Get("Cache-Control"), "no-store") || createdResponse.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("create response was cacheable: %+v", createdResponse.Header())
	}
	if created.Key.ID == "" || created.Token == "" || !created.Key.Enabled {
		t.Fatalf("unexpected create response: %+v", created)
	}
	if strings.Contains(createdResponse.Body.String(), "tokenHash") || strings.Contains(createdResponse.Body.String(), gatewaypkg.HashToken(created.Token)) {
		t.Fatalf("create response leaked token hash: %s", createdResponse.Body.String())
	}

	stored, err := store.GetGatewayKey(context.Background(), created.Key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TokenHash != gatewaypkg.HashToken(created.Token) || stored.TokenHash == created.Token {
		t.Fatalf("database did not store only the generated token hash: %+v", stored)
	}
	var plaintextCount int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM gateway_keys WHERE token_hash = ? OR name = ? OR key_prefix = ?`, created.Token, created.Token, created.Token).Scan(&plaintextCount); err != nil {
		t.Fatal(err)
	}
	if plaintextCount != 0 {
		t.Fatal("plaintext token was persisted in gateway_keys")
	}

	listResponse := gatewayAdminJSONRequest(t, app, http.MethodGet, "/api/gateway/keys", nil, http.StatusOK)
	if strings.Contains(listResponse.Body.String(), created.Token) || strings.Contains(listResponse.Body.String(), stored.TokenHash) {
		t.Fatalf("list response repeated secret material: %s", listResponse.Body.String())
	}
	var listed struct {
		Keys []struct {
			db.GatewayKey
			Usage struct {
				MonthlyTokens int64 `json:"monthlyTokens"`
			} `json:"usage"`
		} `json:"keys"`
	}
	decodeGatewayAdminResponse(t, listResponse, &listed)
	if len(listed.Keys) != 1 || listed.Keys[0].Usage.MonthlyTokens != 0 {
		t.Fatalf("unexpected key list: %+v", listed)
	}

	updatedResponse := gatewayAdminJSONRequest(t, app, http.MethodPatch, "/api/gateway/keys/"+created.Key.ID, map[string]any{
		"name":              "Team",
		"enabled":           false,
		"allowedModels":     []string{"code", "chat", "code"},
		"requestsPerMinute": 0,
		"monthlyTokenLimit": 0,
		"maxConcurrency":    0,
		"expectedUpdatedAt": created.Key.UpdatedAt,
	}, http.StatusOK)
	var updated struct {
		Key db.GatewayKey `json:"key"`
	}
	decodeGatewayAdminResponse(t, updatedResponse, &updated)
	if updated.Key.Name != "Team" || updated.Key.Enabled || strings.Join(updated.Key.AllowedModels, ",") != "chat,code" {
		t.Fatalf("unexpected key update: %+v", updated.Key)
	}

	rotatedResponse := gatewayAdminJSONRequest(t, app, http.MethodPost, "/api/gateway/keys/"+created.Key.ID+"/rotate", nil, http.StatusOK)
	var rotated struct {
		Key   db.GatewayKey `json:"key"`
		Token string        `json:"token"`
	}
	decodeGatewayAdminResponse(t, rotatedResponse, &rotated)
	if !strings.Contains(rotatedResponse.Header().Get("Cache-Control"), "no-store") || rotatedResponse.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("rotate response was cacheable: %+v", rotatedResponse.Header())
	}
	if rotated.Token == "" || rotated.Token == created.Token || rotated.Key.KeyPrefix == created.Key.KeyPrefix {
		t.Fatalf("unexpected rotation response: %+v", rotated)
	}
	stored, err = store.GetGatewayKey(context.Background(), created.Key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TokenHash != gatewaypkg.HashToken(rotated.Token) || stored.TokenHash == gatewaypkg.HashToken(created.Token) {
		t.Fatal("rotation did not replace the stored hash")
	}

	revokedResponse := gatewayAdminJSONRequest(t, app, http.MethodPost, "/api/gateway/keys/"+created.Key.ID+"/revoke", nil, http.StatusOK)
	var revoked struct {
		Key db.GatewayKey `json:"key"`
	}
	decodeGatewayAdminResponse(t, revokedResponse, &revoked)
	if revoked.Key.Enabled || revoked.Key.RevokedAt == "" {
		t.Fatalf("key was not revoked: %+v", revoked.Key)
	}
	gatewayAdminJSONRequest(t, app, http.MethodPost, "/api/gateway/keys/"+created.Key.ID+"/rotate", nil, http.StatusConflict)
	gatewayAdminJSONRequest(t, app, http.MethodPatch, "/api/gateway/keys/"+created.Key.ID, map[string]any{"enabled": true, "expectedUpdatedAt": revoked.Key.UpdatedAt}, http.StatusConflict)

	gatewayAdminJSONRequest(t, app, http.MethodPost, "/api/gateway/keys", map[string]any{"name": "bad", "unknown": true}, http.StatusBadRequest)
	gatewayAdminJSONRequest(t, app, http.MethodPost, "/api/gateway/keys", map[string]any{"name": "bad", "requestsPerMinute": -1}, http.StatusBadRequest)
}

func TestGatewayModelAdminCRUDAndSafeRename(t *testing.T) {
	app, store := newGatewayAdminTestServer(t, config.Config{})

	createdResponse := gatewayAdminJSONRequest(t, app, http.MethodPost, "/api/gateway/models", map[string]any{
		"alias": "fast", "targetModel": "relay:gpt-fast",
	}, http.StatusCreated)
	var created struct {
		Model db.GatewayModel `json:"model"`
	}
	decodeGatewayAdminResponse(t, createdResponse, &created)
	if created.Model.Alias != "fast" || !created.Model.Enabled || created.Model.CreatedAt == "" {
		t.Fatalf("unexpected model create: %+v", created.Model)
	}
	gatewayAdminJSONRequest(t, app, http.MethodPost, "/api/gateway/models", map[string]any{
		"alias": "fast", "targetModel": "other:model",
	}, http.StatusConflict)

	updatedResponse := gatewayAdminJSONRequest(t, app, http.MethodPatch, "/api/gateway/models?alias=fast", map[string]any{
		"targetModel": "relay:gpt-updated", "enabled": false, "expectedUpdatedAt": created.Model.UpdatedAt,
	}, http.StatusOK)
	var updated struct {
		Model db.GatewayModel `json:"model"`
	}
	decodeGatewayAdminResponse(t, updatedResponse, &updated)
	if updated.Model.TargetModel != "relay:gpt-updated" || updated.Model.Enabled {
		t.Fatalf("unexpected model update: %+v", updated.Model)
	}

	gatewayAdminJSONRequest(t, app, http.MethodPost, "/api/gateway/models", map[string]any{
		"alias": "occupied", "targetModel": "relay:occupied", "enabled": true,
	}, http.StatusCreated)
	policyKey := createGatewayAdminStoredKey(t, store, "Policy", "policy", true)
	if _, err := store.UpdateGatewayKeyPolicy(context.Background(), policyKey.ID, db.GatewayKeyPolicy{Name: policyKey.Name, Enabled: true, AllowedModels: []string{"fast", "occupied"}}); err != nil {
		t.Fatal(err)
	}
	gatewayAdminJSONRequest(t, app, http.MethodPatch, "/api/gateway/models?alias=fast", map[string]any{
		"alias": "occupied", "targetModel": "relay:must-not-overwrite", "enabled": true, "expectedUpdatedAt": updated.Model.UpdatedAt,
	}, http.StatusConflict)
	oldAfterConflict, err := store.GetGatewayModel(context.Background(), "fast")
	if err != nil || oldAfterConflict.TargetModel != "relay:gpt-updated" {
		t.Fatalf("rename conflict changed the old alias: %+v, %v", oldAfterConflict, err)
	}
	occupied, err := store.GetGatewayModel(context.Background(), "occupied")
	if err != nil || occupied.TargetModel != "relay:occupied" {
		t.Fatalf("rename conflict overwrote destination: %+v, %v", occupied, err)
	}

	renamedResponse := gatewayAdminJSONRequest(t, app, http.MethodPatch, "/api/gateway/models?alias=fast", map[string]any{
		"alias": "quick", "targetModel": "relay:gpt-quick", "enabled": true, "expectedUpdatedAt": updated.Model.UpdatedAt,
	}, http.StatusOK)
	var renamed struct {
		Model db.GatewayModel `json:"model"`
	}
	decodeGatewayAdminResponse(t, renamedResponse, &renamed)
	if renamed.Model.Alias != "quick" || renamed.Model.TargetModel != "relay:gpt-quick" || !renamed.Model.Enabled || renamed.Model.CreatedAt != created.Model.CreatedAt {
		t.Fatalf("unexpected renamed model: %+v", renamed.Model)
	}
	if _, err := store.GetGatewayModel(context.Background(), "fast"); err == nil {
		t.Fatal("old alias still exists after rename")
	}
	policyKey, err = store.GetGatewayKey(context.Background(), policyKey.ID)
	if err != nil || strings.Join(policyKey.AllowedModels, ",") != "occupied,quick" {
		t.Fatalf("model rename did not update key whitelist atomically: %+v, %v", policyKey, err)
	}

	listResponse := gatewayAdminJSONRequest(t, app, http.MethodGet, "/api/gateway/models", nil, http.StatusOK)
	var listed struct {
		Models []db.GatewayModel `json:"models"`
	}
	decodeGatewayAdminResponse(t, listResponse, &listed)
	if len(listed.Models) != 2 {
		t.Fatalf("unexpected model list: %+v", listed.Models)
	}

	gatewayAdminJSONRequest(t, app, http.MethodDelete, "/api/gateway/models?alias=quick", nil, http.StatusConflict)
	policyKey, err = store.UpdateGatewayKeyPolicyCAS(context.Background(), policyKey.ID, db.GatewayKeyPolicy{
		Name: policyKey.Name, Enabled: policyKey.Enabled, AllowedModels: []string{"occupied"}, RequestsPerMinute: policyKey.RequestsPerMinute,
		MonthlyTokenLimit: policyKey.MonthlyTokenLimit, MaxConcurrency: policyKey.MaxConcurrency, ExpiresAt: policyKey.ExpiresAt,
	}, policyKey.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	deleteResponse := gatewayAdminJSONRequest(t, app, http.MethodDelete, "/api/gateway/models?alias=quick", nil, http.StatusOK)
	var deleted struct {
		OK    bool   `json:"ok"`
		Alias string `json:"alias"`
	}
	decodeGatewayAdminResponse(t, deleteResponse, &deleted)
	if !deleted.OK || deleted.Alias != "quick" {
		t.Fatalf("unexpected delete result: %+v", deleted)
	}
	gatewayAdminJSONRequest(t, app, http.MethodDelete, "/api/gateway/models?alias=quick", nil, http.StatusNotFound)
	gatewayAdminJSONRequest(t, app, http.MethodPost, "/api/gateway/models", map[string]any{"alias": "../bad", "targetModel": "relay:valid"}, http.StatusBadRequest)
	gatewayAdminJSONRequest(t, app, http.MethodPost, "/api/gateway/models", map[string]any{"alias": "valid", "targetModel": "model-only"}, http.StatusBadRequest)
}

func TestGatewayAdminRejectsStalePatchesAndSupportsSlashAliases(t *testing.T) {
	app, store := newGatewayAdminTestServer(t, config.Config{})

	createdKeyResponse := gatewayAdminJSONRequest(t, app, http.MethodPost, "/api/gateway/keys", map[string]any{"name": "Original"}, http.StatusCreated)
	var createdKey struct {
		Key db.GatewayKey `json:"key"`
	}
	decodeGatewayAdminResponse(t, createdKeyResponse, &createdKey)
	gatewayAdminJSONRequest(t, app, http.MethodPatch, "/api/gateway/keys/"+createdKey.Key.ID, map[string]any{"name": "Missing version"}, http.StatusBadRequest)
	updatedKeyResponse := gatewayAdminJSONRequest(t, app, http.MethodPatch, "/api/gateway/keys/"+createdKey.Key.ID, map[string]any{
		"name": "Current", "expectedUpdatedAt": createdKey.Key.UpdatedAt,
	}, http.StatusOK)
	var updatedKey struct {
		Key db.GatewayKey `json:"key"`
	}
	decodeGatewayAdminResponse(t, updatedKeyResponse, &updatedKey)
	gatewayAdminJSONRequest(t, app, http.MethodPatch, "/api/gateway/keys/"+createdKey.Key.ID, map[string]any{
		"name": "Stale", "expectedUpdatedAt": createdKey.Key.UpdatedAt,
	}, http.StatusConflict)
	storedKey, err := store.GetGatewayKey(context.Background(), createdKey.Key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedKey.Name != "Current" || storedKey.UpdatedAt != updatedKey.Key.UpdatedAt {
		t.Fatalf("stale key patch changed current state: %+v", storedKey)
	}

	createdModelResponse := gatewayAdminJSONRequest(t, app, http.MethodPost, "/api/gateway/models", map[string]any{
		"alias": "public/chat", "targetModel": "relay:gpt", "enabled": true,
	}, http.StatusCreated)
	var createdModel struct {
		Model db.GatewayModel `json:"model"`
	}
	decodeGatewayAdminResponse(t, createdModelResponse, &createdModel)
	gatewayAdminJSONRequest(t, app, http.MethodPatch, "/api/gateway/models?alias=public%2Fchat", map[string]any{
		"targetModel": "relay:gpt-updated", "enabled": false, "expectedUpdatedAt": createdModel.Model.UpdatedAt,
	}, http.StatusOK)
	gatewayAdminJSONRequest(t, app, http.MethodPatch, "/api/gateway/models?alias=public%2Fchat", map[string]any{
		"targetModel": "relay:stale", "expectedUpdatedAt": createdModel.Model.UpdatedAt,
	}, http.StatusConflict)
	storedModel, err := store.GetGatewayModel(context.Background(), "public/chat")
	if err != nil {
		t.Fatal(err)
	}
	if storedModel.TargetModel != "relay:gpt-updated" || storedModel.Enabled {
		t.Fatalf("slash alias update was lost or overwritten: %+v", storedModel)
	}
	gatewayAdminJSONRequest(t, app, http.MethodDelete, "/api/gateway/models?alias=public%2Fchat", nil, http.StatusOK)
}

func TestGatewayUsageAggregatesCurrentUTCMonth(t *testing.T) {
	app, store := newGatewayAdminTestServer(t, config.Config{})
	now := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.FixedZone("test", -7*60*60))
	app.clock = func() time.Time { return now }

	first := createGatewayAdminStoredKey(t, store, "First", "first", true)
	second := createGatewayAdminStoredKey(t, store, "Second", "second", false)
	requests := []db.APIRequest{
		{ID: "first-success", GatewayKeyID: first.ID, InputTokens: 10, OutputTokens: 5, CostUSD: 0.10, CreatedAt: "2026-08-01T00:00:00Z"},
		{ID: "first-error", GatewayKeyID: first.ID, InputTokens: 3, OutputTokens: 2, CostUSD: 0.05, ErrorMessage: "upstream failed", CreatedAt: "2026-08-31T23:59:59Z"},
		{ID: "second-success", GatewayKeyID: second.ID, InputTokens: 7, OutputTokens: 1, CostUSD: 0.20, CreatedAt: "2026-08-20T00:00:00Z"},
		{ID: "previous-month", GatewayKeyID: first.ID, InputTokens: 100, OutputTokens: 100, CostUSD: 9, CreatedAt: "2026-07-31T23:59:59Z"},
	}
	for _, request := range requests {
		if _, err := store.AddAPIRequest(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}

	keysResponse := gatewayAdminJSONRequest(t, app, http.MethodGet, "/api/gateway/keys", nil, http.StatusOK)
	var keys struct {
		Keys []struct {
			ID    string `json:"id"`
			Usage struct {
				MonthlyTokens int64 `json:"monthlyTokens"`
				Requests      int64 `json:"requests"`
				Errors        int64 `json:"errors"`
			} `json:"usage"`
		} `json:"keys"`
	}
	decodeGatewayAdminResponse(t, keysResponse, &keys)
	if len(keys.Keys) != 2 || keys.Keys[0].ID != first.ID || keys.Keys[0].Usage.MonthlyTokens != 20 || keys.Keys[0].Usage.Requests != 2 || keys.Keys[0].Usage.Errors != 1 {
		t.Fatalf("unexpected key usage: %+v", keys.Keys)
	}

	usageResponse := gatewayAdminJSONRequest(t, app, http.MethodGet, "/api/gateway/usage", nil, http.StatusOK)
	var usage struct {
		Items   []gatewayUsageItem  `json:"items"`
		Summary gatewayUsageSummary `json:"summary"`
	}
	decodeGatewayAdminResponse(t, usageResponse, &usage)
	if len(usage.Items) != 2 {
		t.Fatalf("unexpected usage items: %+v", usage.Items)
	}
	if usage.Summary.Requests != 3 || usage.Summary.Tokens != 28 || usage.Summary.Errors != 1 || usage.Summary.ActiveKeys != 1 || usage.Summary.CostUSD < 0.349999 || usage.Summary.CostUSD > 0.350001 {
		t.Fatalf("unexpected usage summary: %+v", usage.Summary)
	}
	if usage.Items[0].MonthUTC != "2026-08" || usage.Items[0].GatewayKeyID != first.ID || usage.Items[0].TotalTokens != 20 || usage.Items[0].Errors != 1 {
		t.Fatalf("unexpected first usage item: %+v", usage.Items[0])
	}
}

func TestGatewayRuntimeStatusAndConfigPatchPersistDesiredConfig(t *testing.T) {
	cfg := config.Config{
		SchemaVersion: config.CurrentConfigVersion,
		Server:        config.ServerConfig{Host: "127.0.0.1", Port: 7788},
		Gateway:       config.GatewayConfig{Host: "127.0.0.1", Port: 18789, MaxGlobalConcurrency: 4, MaxRequestBytes: 1 << 20},
	}
	app, _ := newGatewayAdminTestServer(t, cfg)
	configPath := filepath.Join(t.TempDir(), "config.json")
	app.SetConfigPath(configPath)
	controller := &fakeGatewayRuntimeController{status: gatewaypkg.ManagerStatus{Address: "127.0.0.1:54321", Running: true}}
	app.SetGatewayRuntimeController(controller)

	statusResponse := gatewayAdminJSONRequest(t, app, http.MethodGet, "/api/gateway/status", nil, http.StatusOK)
	var initial gatewaypkg.ManagerStatus
	decodeGatewayAdminResponse(t, statusResponse, &initial)
	if initial.Address != "127.0.0.1:54321" || !initial.Running {
		t.Fatalf("status did not return the actual runtime address: %+v", initial)
	}

	patchResponse := gatewayAdminJSONRequest(t, app, http.MethodPatch, "/api/gateway/config", map[string]any{
		"enabled": true, "maxGlobalConcurrency": 9, "maxRequestBytes": 2 << 20,
	}, http.StatusOK)
	var patched struct {
		Gateway config.GatewayConfig     `json:"gateway"`
		Status  gatewaypkg.ManagerStatus `json:"status"`
	}
	decodeGatewayAdminResponse(t, patchResponse, &patched)
	if !patched.Gateway.Enabled || patched.Gateway.MaxGlobalConcurrency != 9 || patched.Status.Address != "127.0.0.1:54321" || !patched.Status.Running {
		t.Fatalf("unexpected Gateway patch response: %+v", patched)
	}
	if len(controller.calls) != 1 || !controller.calls[0].Enabled || controller.calls[0].MaxRequestBytes != 2<<20 {
		t.Fatalf("runtime controller did not receive the desired configuration: %+v", controller.calls)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted config.Config
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if !persisted.Gateway.Enabled || persisted.Gateway.Port != 18789 || persisted.Gateway.MaxGlobalConcurrency != 9 {
		t.Fatalf("Gateway patch was not persisted: %+v", persisted.Gateway)
	}
	settingsResponse := gatewayAdminJSONRequest(t, app, http.MethodGet, "/api/settings", nil, http.StatusOK)
	var settings struct {
		Gateway config.GatewayConfig `json:"gateway"`
	}
	decodeGatewayAdminResponse(t, settingsResponse, &settings)
	if settings.Gateway.Port != 18789 || settings.Gateway.Port == 54321 {
		t.Fatalf("settings returned runtime address instead of persistent config: %+v", settings.Gateway)
	}
}

func TestGatewayRuntimeConfigBindFailureRollsBackWithoutPublishing(t *testing.T) {
	cfg := config.Config{
		SchemaVersion: config.CurrentConfigVersion,
		Server:        config.ServerConfig{Host: "127.0.0.1", Port: 7788},
		Gateway:       config.GatewayConfig{Enabled: true, Host: "127.0.0.1", Port: 18789, MaxGlobalConcurrency: 4, MaxRequestBytes: 1 << 20},
	}
	app, _ := newGatewayAdminTestServer(t, cfg)
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	app.SetConfigPath(configPath)
	controller := &fakeGatewayRuntimeController{
		status: gatewaypkg.ManagerStatus{Address: "127.0.0.1:18789", Running: true, DesiredEnabled: true},
		fail: func(next config.GatewayConfig) error {
			if next.Port == 19000 {
				return &gatewaypkg.BindError{Address: "127.0.0.1:19000", Err: errors.New("address already in use")}
			}
			return nil
		},
	}
	app.SetGatewayRuntimeController(controller)

	gatewayAdminJSONRequest(t, app, http.MethodPatch, "/api/gateway/config", map[string]any{"port": 19000}, http.StatusConflict)
	if len(controller.calls) != 2 || controller.calls[0].Port != 19000 || controller.calls[1].Port != 18789 {
		t.Fatalf("failed reconfiguration was not rolled back: %+v", controller.calls)
	}
	if current := app.ConfigSnapshot(); current.Gateway.Port != 18789 {
		t.Fatalf("failed reconfiguration was published: %+v", current.Gateway)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted config.Config
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Gateway.Port != 18789 {
		t.Fatalf("failed reconfiguration changed persistent config: %+v", persisted.Gateway)
	}
}

func TestGatewayAccountAdminManagesExplicitGrants(t *testing.T) {
	home := t.TempDir()
	credentialStore := codexauth.NewStore(codexauth.DefaultStoreDir(home))
	if _, err := credentialStore.Import([]codexauth.ImportDocument{{Filename: "account.json", Content: []byte(`{"type":"codex","access_token":"account-secret","account_id":"account-1","alias":"Shared account"}`)}}); err != nil {
		t.Fatal(err)
	}
	accounts, err := credentialStore.ListAccounts()
	if err != nil || len(accounts) != 1 {
		t.Fatalf("create Codex account: accounts=%+v err=%v", accounts, err)
	}
	cfg := config.Config{
		Paths: config.PathsConfig{HomeDir: home},
		Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
			Name: codexauth.DefaultProviderName, Type: config.ProviderTypeCodex, GatewayEnabled: true,
		}}},
	}
	app, store := newGatewayAdminTestServer(t, cfg)

	listResponse := gatewayAdminJSONRequest(t, app, http.MethodGet, "/api/gateway/accounts", nil, http.StatusOK)
	var listed struct {
		Accounts []gatewayAccountSummary `json:"accounts"`
	}
	decodeGatewayAdminResponse(t, listResponse, &listed)
	if len(listed.Accounts) != 1 || listed.Accounts[0].AccountID != accounts[0].ID || listed.Accounts[0].Shared || listed.Accounts[0].Effective {
		t.Fatalf("unexpected unshared account list: %+v", listed.Accounts)
	}

	accountPath := "/api/gateway/accounts/codex/" + accounts[0].ID
	sharedResponse := gatewayAdminJSONRequest(t, app, http.MethodPatch, accountPath, map[string]any{"shared": true}, http.StatusOK)
	var shared struct {
		Account gatewayAccountSummary `json:"account"`
	}
	decodeGatewayAdminResponse(t, sharedResponse, &shared)
	if !shared.Account.Shared || !shared.Account.Effective || shared.Account.Reason != "eligible" {
		t.Fatalf("Gateway grant was not effective: %+v", shared.Account)
	}
	grants, err := store.ListGatewayAccountGrants(context.Background(), codexauth.DefaultProviderName)
	if err != nil || len(grants) != 1 || grants[0].AccountID != accounts[0].ID {
		t.Fatalf("Gateway grant was not persisted: %+v err=%v", grants, err)
	}

	gatewayAdminJSONRequest(t, app, http.MethodPatch, accountPath, map[string]any{"shared": false}, http.StatusOK)
	grants, err = store.ListGatewayAccountGrants(context.Background(), codexauth.DefaultProviderName)
	if err != nil || len(grants) != 0 {
		t.Fatalf("Gateway grant was not revoked: %+v err=%v", grants, err)
	}
}

func TestGatewayAccountAdminManagesSubscriptionGrants(t *testing.T) {
	home := t.TempDir()
	credentialStore := subscriptionauth.NewStore(subscriptionauth.DefaultStoreDir(home, subscriptionauth.ProviderGrok))
	stored, err := credentialStore.CreateOAuth(subscriptionauth.CreateRequest{
		Provider: subscriptionauth.ProviderGrok, Alias: "Team Grok", AccessToken: "grok-account-secret",
		RefreshToken: "grok-refresh-secret", Subject: "grok-subject",
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := config.NormalizeProviderConfig(config.ProviderConfig{Name: config.ProviderTypeGrok, Type: config.ProviderTypeGrok})
	provider.GatewayEnabled = true
	cfg := config.Config{
		Paths:     config.PathsConfig{HomeDir: home},
		Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{provider}},
	}
	app, store := newGatewayAdminTestServer(t, cfg)

	listResponse := gatewayAdminJSONRequest(t, app, http.MethodGet, "/api/gateway/accounts", nil, http.StatusOK)
	if body := listResponse.Body.String(); strings.Contains(body, "grok-account-secret") || strings.Contains(body, "grok-refresh-secret") {
		t.Fatalf("Gateway account list leaked subscription tokens: %s", body)
	}
	var listed struct {
		Accounts []gatewayAccountSummary `json:"accounts"`
	}
	decodeGatewayAdminResponse(t, listResponse, &listed)
	if len(listed.Accounts) != 1 || listed.Accounts[0].Provider != subscriptionauth.ProviderGrok || listed.Accounts[0].AccountID != stored.ID || listed.Accounts[0].Label != "Team Grok" {
		t.Fatalf("unexpected subscription account list: %+v", listed.Accounts)
	}

	accountPath := "/api/gateway/accounts/grok/" + stored.ID
	sharedResponse := gatewayAdminJSONRequest(t, app, http.MethodPatch, accountPath, map[string]any{"shared": true}, http.StatusOK)
	var shared struct {
		Account gatewayAccountSummary `json:"account"`
	}
	decodeGatewayAdminResponse(t, sharedResponse, &shared)
	if !shared.Account.Shared || !shared.Account.Effective || shared.Account.Reason != "eligible" {
		t.Fatalf("subscription Gateway grant was not effective: %+v", shared.Account)
	}
	grants, err := store.ListGatewayAccountGrants(context.Background(), subscriptionauth.ProviderGrok)
	if err != nil || len(grants) != 1 || grants[0].AccountID != stored.ID {
		t.Fatalf("subscription Gateway grant was not persisted: %+v err=%v", grants, err)
	}

	gatewayAdminJSONRequest(t, app, http.MethodPatch, accountPath, map[string]any{"shared": false}, http.StatusOK)
}

func TestGatewayRequestsReturnsSafeRecentMetadata(t *testing.T) {
	app, store := newGatewayAdminTestServer(t, config.Config{})
	key := createGatewayAdminStoredKey(t, store, "Recent", "recent", true)
	if _, err := store.AddAPIRequest(context.Background(), db.APIRequest{
		ID: "gateway-request", Kind: "gateway", Provider: "codex", CredentialID: "account-1", GatewayKeyID: key.ID, Model: "gpt-safe",
		InputTokens: 8, OutputTokens: 5, ErrorMessage: "upstream response secret-token",
		RawDumpJSON: json.RawMessage(`{"prompt":"secret prompt","response":"secret response","token":"secret-token"}`),
		CreatedAt:   "2026-07-22T12:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddAPIRequest(context.Background(), db.APIRequest{ID: "internal-request", Kind: "model", Model: "must-not-appear", CreatedAt: "2026-07-22T12:01:00Z"}); err != nil {
		t.Fatal(err)
	}

	response := gatewayAdminJSONRequest(t, app, http.MethodGet, "/api/gateway/requests?limit=1", nil, http.StatusOK)
	body := response.Body.String()
	for _, forbidden := range []string{"secret prompt", "secret response", "secret-token", "rawDumpJson", "errorMessage", key.TokenHash, "must-not-appear"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("Gateway request API leaked %q: %s", forbidden, body)
		}
	}
	var payload struct {
		Requests []db.GatewayRequestSummary `json:"requests"`
	}
	decodeGatewayAdminResponse(t, response, &payload)
	if len(payload.Requests) != 1 || payload.Requests[0].ID != "gateway-request" || payload.Requests[0].GatewayKeyName != "Recent" || payload.Requests[0].TotalTokens != 13 {
		t.Fatalf("unexpected Gateway request response: %+v", payload.Requests)
	}
	for _, limit := range []string{"0", "201", "nope"} {
		gatewayAdminJSONRequest(t, app, http.MethodGet, "/api/gateway/requests?limit="+limit, nil, http.StatusBadRequest)
	}
}

func TestGatewayProviderAllowedUsesDynamicConfigAndRejectsCodex(t *testing.T) {
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{
		{Name: " Relay ", Type: " OpenAI-Compatible ", GatewayEnabled: true},
		{Name: "codex-provider", Type: " CoDeX ", GatewayEnabled: true},
		{Name: "oauth-proxy", Type: "openai-compatible", Profile: config.ProviderProfileCLIProxyAPI, GatewayEnabled: true},
	}}}, nil, nil, nil)
	ctx := context.Background()
	if !app.GatewayProviderAllowed(ctx, "  rElAy ") {
		t.Fatal("enabled Gateway provider should be allowed with case/space normalization")
	}
	if !app.GatewayProviderAllowed(ctx, "codex-provider") {
		t.Fatal("Codex provider should now be allowed by the Gateway when enabled")
	}
	if app.GatewayProviderAllowed(ctx, "oauth-proxy") {
		t.Fatal("OAuth proxy provider must never be allowed by the Gateway")
	}
	if app.GatewayProviderAllowed(ctx, "missing") || app.GatewayProviderAllowed(ctx, " ") {
		t.Fatal("missing or blank provider should not be allowed")
	}

	app.cfgMu.Lock()
	app.cfg.Providers.Instances[0].Disabled = true
	app.cfgMu.Unlock()
	if app.GatewayProviderAllowed(ctx, "relay") {
		t.Fatal("dynamic disabled provider remained allowed")
	}

	app.cfgMu.Lock()
	app.cfg.Providers.Instances[0].Disabled = false
	app.cfg.Providers.Instances[0].GatewayEnabled = false
	app.cfgMu.Unlock()
	if app.GatewayProviderAllowed(ctx, "relay") {
		t.Fatal("dynamic gateway opt-out was ignored")
	}

	app.cfgMu.Lock()
	app.cfg.Providers.Instances[0].GatewayEnabled = true
	app.cfg.Providers.Instances[0].Type = "CODEX"
	app.cfgMu.Unlock()
	if !app.GatewayProviderAllowed(ctx, "relay") {
		t.Fatal("provider patched to Codex type should now be allowed when enabled")
	}
}

func newGatewayAdminTestServer(t *testing.T, cfg config.Config) (*Server, *db.Store) {
	t.Helper()
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "gateway-admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return New(cfg, store, nil, nil), store
}

func gatewayAdminJSONRequest(t *testing.T, app *Server, method, path string, payload any, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	request := newTestRequest(method, path, body)
	request.Header.Set(localTokenHeader, app.localToken)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	app.Routes().ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s %s returned %d, want %d: %s", method, path, response.Code, wantStatus, response.Body.String())
	}
	return response
}

func decodeGatewayAdminResponse(t *testing.T, response *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(dst); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func TestGatewayProviderConfigPrefersNativeSubscriptionTypeOnNameCollision(t *testing.T) {
	cfg := config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{
		{Name: subscriptionauth.ProviderGemini, Type: config.ProviderTypeGeminiInteractions},
		{Name: "gemini-oauth", Type: config.ProviderTypeGemini, GatewayEnabled: true},
	}}}
	provider, ok := gatewayProviderConfig(cfg, subscriptionauth.ProviderGemini)
	if !ok || provider.Name != "gemini-oauth" || provider.Type != config.ProviderTypeGemini {
		t.Fatalf("Gateway 未优先选择原生 Gemini Provider：%+v", provider)
	}
}

type fakeGatewayRuntimeController struct {
	status gatewaypkg.ManagerStatus
	calls  []config.GatewayConfig
	fail   func(config.GatewayConfig) error
}

func (f *fakeGatewayRuntimeController) Reconfigure(_ context.Context, cfg config.GatewayConfig) error {
	f.calls = append(f.calls, cfg)
	if f.fail != nil {
		if err := f.fail(cfg); err != nil {
			return err
		}
	}
	f.status.DesiredEnabled = cfg.Enabled
	f.status.AllowRemote = cfg.AllowRemote
	f.status.Running = cfg.Enabled
	if !cfg.Enabled {
		f.status.Address = ""
	}
	return nil
}

func (f *fakeGatewayRuntimeController) Status() gatewaypkg.ManagerStatus {
	return f.status
}

func createGatewayAdminStoredKey(t *testing.T, store *db.Store, name, prefix string, enabled bool) db.GatewayKey {
	t.Helper()
	generated, err := gatewaypkg.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.CreateGatewayKey(context.Background(), db.GatewayKey{
		Name: name, KeyPrefix: generated.Prefix + prefix[:1], TokenHash: gatewaypkg.HashToken(generated.Token + prefix), Enabled: enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	return key
}
