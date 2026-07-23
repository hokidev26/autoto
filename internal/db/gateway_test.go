package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGatewayFreshSchemaAndV40Migration(t *testing.T) {
	ctx := context.Background()
	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"gateway_keys", "gateway_models"} {
		if !testTableExists(t, ctx, fresh.DB(), table) {
			fresh.Close()
			t.Fatalf("fresh schema missing %s", table)
		}
	}
	if !testColumnExists(t, ctx, fresh.DB(), "api_requests", "credential_id") || !testColumnExists(t, ctx, fresh.DB(), "api_requests", "gateway_key_id") {
		fresh.Close()
		t.Fatal("fresh api_requests schema is missing attribution columns")
	}
	if _, err := fresh.DB().ExecContext(ctx, `INSERT INTO gateway_keys (id, name, key_prefix, token_hash, enabled, allowed_models_json, created_at, updated_at) VALUES ('bad', 'Bad', '-unsafe', ?, 1, '[]', ?, ?)`, strings.Repeat("A", 64), Now(), Now()); err == nil {
		fresh.Close()
		t.Fatal("fresh schema accepted an invalid prefix and token hash")
	}
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "v40.db")
	raw := openRawDB(t, path)
	v40Schema := strings.TrimSuffix(schemaSQL, gatewaySchemaSQL)
	v40Schema = strings.Replace(v40Schema, "  gateway_key_id TEXT REFERENCES gateway_keys(id) ON DELETE SET NULL,\n", "", 1)
	v40Schema = strings.Replace(v40Schema, "CREATE INDEX IF NOT EXISTS idx_api_requests_gateway_key_created ON api_requests(gateway_key_id, created_at DESC, id DESC);\n", "", 1)
	if _, err := raw.ExecContext(ctx, v40Schema); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO api_requests (id, kind, credential_id, created_at) VALUES ('legacy-request', 'model', 'credential-legacy', '2026-01-01T00:00:00Z'); PRAGMA user_version = 40`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if version := readUserVersion(t, ctx, migrated.DB()); version != CurrentDBVersion {
		t.Fatalf("migrated version = %d, want %d", version, CurrentDBVersion)
	}
	if !testTableExists(t, ctx, migrated.DB(), "gateway_keys") || !testTableExists(t, ctx, migrated.DB(), "gateway_models") || !testColumnExists(t, ctx, migrated.DB(), "api_requests", "gateway_key_id") {
		t.Fatal("v40 to v41 migration is incomplete")
	}
	var credentialID string
	if err := migrated.DB().QueryRowContext(ctx, `SELECT COALESCE(credential_id,'') FROM api_requests WHERE id = 'legacy-request'`).Scan(&credentialID); err != nil || credentialID != "credential-legacy" {
		t.Fatalf("legacy credential attribution was not preserved: %q, %v", credentialID, err)
	}
	tx, err := migrated.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateV41PrivateAPIGateway(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatalf("first idempotent migration rerun: %v", err)
	}
	if err := migrateV41PrivateAPIGateway(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatalf("second idempotent migration rerun: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayKeyCRUDValidationAndNoSecretLeakage(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	tokenHash := strings.Repeat("a", 64)
	key, err := store.CreateGatewayKey(ctx, GatewayKey{
		Name: "Production", KeyPrefix: "gw_prod", TokenHash: tokenHash, Enabled: true,
		AllowedModels: []string{"z-model", "a-model", "z-model"}, RequestsPerMinute: 60,
		MonthlyTokenLimit: 5000, MaxConcurrency: 3, ExpiresAt: "2027-01-01T01:00:00+01:00",
	})
	if err != nil {
		t.Fatal(err)
	}
	if key.ID == "" || key.ExpiresAt != "2027-01-01T00:00:00Z" || fmt.Sprint(key.AllowedModels) != "[a-model z-model]" {
		t.Fatalf("unexpected canonical gateway key: %+v", key)
	}
	encoded, err := json.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), tokenHash) || strings.Contains(string(encoded), "tokenHash") {
		t.Fatalf("gateway key JSON leaked its token hash: %s", encoded)
	}
	var allowedJSON string
	if err := store.DB().QueryRowContext(ctx, `SELECT allowed_models_json FROM gateway_keys WHERE id = ?`, key.ID).Scan(&allowedJSON); err != nil {
		t.Fatal(err)
	}
	if allowedJSON != `["a-model","z-model"]` {
		t.Fatalf("allowed models were not stored canonically: %s", allowedJSON)
	}

	byID, err := store.GetGatewayKey(ctx, key.ID)
	if err != nil || byID.TokenHash != tokenHash {
		t.Fatalf("get by id failed: %+v, %v", byID, err)
	}
	byHash, err := store.GetGatewayKeyByTokenHash(ctx, tokenHash)
	if err != nil || byHash.ID != key.ID {
		t.Fatalf("get by token hash failed: %+v, %v", byHash, err)
	}
	keys, err := store.ListGatewayKeys(ctx)
	if err != nil || len(keys) != 1 || keys[0].ID != key.ID {
		t.Fatalf("unexpected gateway key list: %+v, %v", keys, err)
	}

	updated, err := store.UpdateGatewayKeyPolicy(ctx, key.ID, GatewayKeyPolicy{
		Name: "Production limited", Enabled: false, AllowedModels: []string{"b", "a", "a"},
		RequestsPerMinute: 10, MonthlyTokenLimit: 1000, MaxConcurrency: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled || updated.Name != "Production limited" || fmt.Sprint(updated.AllowedModels) != "[a b]" || updated.TokenHash != tokenHash || updated.KeyPrefix != key.KeyPrefix {
		t.Fatalf("unexpected policy update: %+v", updated)
	}

	plaintext := "gateway-secret-plaintext"
	_, err = store.CreateGatewayKey(ctx, GatewayKey{Name: "Invalid", KeyPrefix: "gw_bad", TokenHash: plaintext, Enabled: true})
	if err == nil || strings.Contains(err.Error(), plaintext) {
		t.Fatalf("invalid secret error leaked input or was accepted: %v", err)
	}
	var leaked int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM gateway_keys WHERE id = ? OR name = ? OR key_prefix = ? OR token_hash = ? OR allowed_models_json = ?`, plaintext, plaintext, plaintext, plaintext, plaintext).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatal("plaintext gateway secret entered the database")
	}
	if _, err := store.CreateGatewayKey(ctx, GatewayKey{Name: "Upper", KeyPrefix: "gw_upper", TokenHash: strings.Repeat("A", 64), Enabled: true}); err == nil {
		t.Fatal("uppercase token hash was accepted")
	}
	if _, err := store.CreateGatewayKey(ctx, GatewayKey{Name: "Prefix", KeyPrefix: "../unsafe", TokenHash: strings.Repeat("b", 64), Enabled: true}); err == nil {
		t.Fatal("unsafe key prefix was accepted")
	}
	if _, err := store.CreateGatewayKey(ctx, GatewayKey{Name: "Negative", KeyPrefix: "gw_neg", TokenHash: strings.Repeat("c", 64), RequestsPerMinute: -1}); err == nil {
		t.Fatal("negative policy limit was accepted")
	}
}

func TestGatewayKeyRotationRevocationAndConcurrentSafety(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	key, err := store.CreateGatewayKey(ctx, GatewayKey{Name: "Rotating", KeyPrefix: "gw_0", TokenHash: strings.Repeat("0", 64), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	pairs := make(map[string]string)
	var pairsMu sync.Mutex
	var wg sync.WaitGroup
	errs := make(chan error, 24)
	for i := 1; i <= 12; i++ {
		prefix := fmt.Sprintf("gw_%d", i)
		hash := fmt.Sprintf("%064x", i)
		pairsMu.Lock()
		pairs[hash] = prefix
		pairsMu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.RotateGatewayKey(ctx, key.ID, prefix, hash); err != nil {
				errs <- err
			}
		}()
	}
	latestUse := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 12; i++ {
		usedAt := latestUse.Add(-time.Duration(i) * time.Hour).Format(time.RFC3339Nano)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.TouchGatewayKeyLastUsed(ctx, key.ID, usedAt); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent gateway mutation failed: %v", err)
	}
	current, err := store.GetGatewayKey(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pairs[current.TokenHash] != current.KeyPrefix {
		t.Fatalf("rotation stored a torn prefix/hash pair: %+v", current)
	}
	if current.LastUsedAt != latestUse.Format(time.RFC3339Nano) {
		t.Fatalf("concurrent touches lost the latest timestamp: %s", current.LastUsedAt)
	}
	wholeSecond := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	fractionalSecond := wholeSecond.Add(500 * time.Millisecond)
	if _, err := store.TouchGatewayKeyLastUsed(ctx, key.ID, wholeSecond.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	fractional, err := store.TouchGatewayKeyLastUsed(ctx, key.ID, fractionalSecond.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	if fractional.LastUsedAt != fractionalSecond.Format(time.RFC3339Nano) {
		t.Fatalf("fractional timestamp did not supersede whole second: %s", fractional.LastUsedAt)
	}

	sharedHash := strings.Repeat("f", 64)
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := store.CreateGatewayKey(ctx, GatewayKey{ID: fmt.Sprintf("concurrent-%d", index), Name: "Concurrent", KeyPrefix: fmt.Sprintf("gw_c%d", index), TokenHash: sharedHash, Enabled: true})
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent create error: %v", err)
		}
	}
	if successes != 1 || conflicts != 7 {
		t.Fatalf("unique token hash concurrency: successes=%d conflicts=%d", successes, conflicts)
	}

	revoked, err := store.RevokeGatewayKey(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.Enabled || revoked.RevokedAt == "" {
		t.Fatalf("gateway key was not revoked: %+v", revoked)
	}
	revokedAgain, err := store.RevokeGatewayKey(ctx, key.ID)
	if err != nil || revokedAgain.RevokedAt != revoked.RevokedAt {
		t.Fatalf("revoke was not idempotent: %+v, %v", revokedAgain, err)
	}
	if _, err := store.RotateGatewayKey(ctx, key.ID, "gw_after", strings.Repeat("e", 64)); !errors.Is(err, ErrGatewayKeyRevoked) {
		t.Fatalf("rotating revoked key returned %v", err)
	}
	if _, err := store.TouchGatewayKeyLastUsed(ctx, key.ID, Now()); !errors.Is(err, ErrGatewayKeyRevoked) {
		t.Fatalf("touching revoked key returned %v", err)
	}
	if _, err := store.UpdateGatewayKeyPolicy(ctx, key.ID, GatewayKeyPolicy{Name: "Cannot enable", Enabled: true}); !errors.Is(err, ErrGatewayKeyRevoked) {
		t.Fatalf("enabling revoked key returned %v", err)
	}
}

func TestGatewayModelCRUD(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	created, err := store.UpsertGatewayModel(ctx, GatewayModel{Alias: "fast", TargetModel: "openai:gpt-fast", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if created.CreatedAt == "" || created.UpdatedAt == "" {
		t.Fatalf("missing gateway model timestamps: %+v", created)
	}
	updated, err := store.UpsertGatewayModel(ctx, GatewayModel{Alias: "fast", TargetModel: "anthropic:sonnet", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if updated.TargetModel != "anthropic:sonnet" || updated.Enabled || updated.CreatedAt != created.CreatedAt || updated.UpdatedAt == created.UpdatedAt {
		t.Fatalf("unexpected gateway model upsert: %+v", updated)
	}
	model, err := store.GetGatewayModel(ctx, "fast")
	if err != nil || model != updated {
		t.Fatalf("get gateway model: %+v, %v", model, err)
	}
	models, err := store.ListGatewayModels(ctx)
	if err != nil || len(models) != 1 || models[0] != updated {
		t.Fatalf("list gateway models: %+v, %v", models, err)
	}
	if _, err := store.UpsertGatewayModel(ctx, GatewayModel{Alias: "../bad", TargetModel: "valid", Enabled: true}); err == nil {
		t.Fatal("unsafe gateway model alias was accepted")
	}
	if err := store.DeleteGatewayModel(ctx, "fast"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetGatewayModel(ctx, "fast"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted gateway model returned %v", err)
	}
	if err := store.DeleteGatewayModel(ctx, "fast"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleting missing gateway model returned %v", err)
	}
}

func TestGatewayKeyPolicyCASAndTouchVersionIsolation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "gateway-cas.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	key, err := store.CreateGatewayKey(ctx, GatewayKey{
		Name: "Shared", KeyPrefix: "gw_cas", TokenHash: strings.Repeat("7", 64), Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	originalVersion := key.UpdatedAt
	touched, err := store.TouchGatewayKeyLastUsed(ctx, key.ID, "2026-07-17T13:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if touched.UpdatedAt != originalVersion {
		t.Fatalf("last-used touch changed the policy version: before=%q after=%q", originalVersion, touched.UpdatedAt)
	}

	errs := make(chan error, 2)
	for _, name := range []string{"First writer", "Second writer"} {
		name := name
		go func() {
			_, err := store.UpdateGatewayKeyPolicyCAS(ctx, key.ID, GatewayKeyPolicy{Name: name, Enabled: true}, originalVersion)
			errs <- err
		}()
	}
	successes, conflicts := 0, 0
	for range 2 {
		switch err := <-errs; {
		case err == nil:
			successes++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected CAS result: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("CAS did not select exactly one writer: successes=%d conflicts=%d", successes, conflicts)
	}
	current, err := store.GetGatewayKey(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.UpdatedAt == originalVersion || (current.Name != "First writer" && current.Name != "Second writer") {
		t.Fatalf("unexpected CAS winner: %+v", current)
	}
}

func TestGatewayModelReferencesPreventGhostAuthorization(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "gateway-model-reference.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const alias = "public/chat"
	if _, err := store.CreateGatewayModel(ctx, GatewayModel{Alias: alias, TargetModel: "backend:gpt", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	key, err := store.CreateGatewayKey(ctx, GatewayKey{
		Name: "Restricted", KeyPrefix: "gw_model", TokenHash: strings.Repeat("8", 64), Enabled: true, AllowedModels: []string{alias},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteGatewayModel(ctx, alias); !errors.Is(err, ErrConflict) {
		t.Fatalf("referenced alias deletion returned %v", err)
	}

	// Simulate a database left by an older release that allowed a referenced
	// alias to be deleted. Recreating the alias must not revive that grant.
	if _, err := store.DB().ExecContext(ctx, `DELETE FROM gateway_models WHERE alias = ?`, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateGatewayModel(ctx, GatewayModel{Alias: alias, TargetModel: "backend:replacement", Enabled: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("orphaned whitelist entry did not block alias recreation: %v", err)
	}

	key, err = store.UpdateGatewayKeyPolicyCAS(ctx, key.ID, GatewayKeyPolicy{Name: key.Name, Enabled: true}, key.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	recreated, err := store.CreateGatewayModel(ctx, GatewayModel{Alias: alias, TargetModel: "backend:replacement", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if recreated.Alias != alias {
		t.Fatalf("unexpected recreated model: %+v", recreated)
	}
	key, err = store.GetGatewayKey(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(key.AllowedModels) != 0 {
		t.Fatalf("alias recreation restored a removed grant: %+v", key.AllowedModels)
	}
}

func TestAPIRequestGatewayAttributionAndMonthlyUsage(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	key, err := store.CreateGatewayKey(ctx, GatewayKey{Name: "Usage", KeyPrefix: "gw_usage", TokenHash: strings.Repeat("1", 64), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateGatewayKey(ctx, GatewayKey{Name: "Other", KeyPrefix: "gw_other", TokenHash: strings.Repeat("2", 64), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	requests := []APIRequest{
		{ID: "jan-1", CredentialID: "credential-a", GatewayKeyID: key.ID, Model: "fast", InputTokens: 10, OutputTokens: 5, CostUSD: 0.10, CreatedAt: "2026-01-01T00:30:00Z"},
		{ID: "jan-2", CredentialID: "credential-b", GatewayKeyID: key.ID, Model: "fast", InputTokens: 20, OutputTokens: 7, CostUSD: 0.20, CreatedAt: "2026-01-31T23:00:00Z", RawDumpJSON: json.RawMessage(`not-json-and-not-read`)},
		{ID: "jan-offset", GatewayKeyID: key.ID, InputTokens: 3, OutputTokens: 4, CostUSD: 0.05, CreatedAt: "2026-02-01T01:00:00+02:00"},
		{ID: "feb-offset", GatewayKeyID: key.ID, InputTokens: 100, OutputTokens: 100, CostUSD: 1.00, CreatedAt: "2026-01-31T20:00:00-05:00"},
		{ID: "other-key", GatewayKeyID: other.ID, InputTokens: 999, OutputTokens: 999, CostUSD: 9.00, CreatedAt: "2026-01-15T00:00:00Z"},
	}
	for _, request := range requests {
		if _, err := store.AddAPIRequest(ctx, request); err != nil {
			t.Fatalf("add api request %s: %v", request.ID, err)
		}
	}
	var credentialID, gatewayKeyID string
	if err := store.DB().QueryRowContext(ctx, `SELECT COALESCE(credential_id,''), COALESCE(gateway_key_id,'') FROM api_requests WHERE id = 'jan-1'`).Scan(&credentialID, &gatewayKeyID); err != nil {
		t.Fatal(err)
	}
	if credentialID != "credential-a" || gatewayKeyID != key.ID {
		t.Fatalf("api request attribution mismatch: credential=%q gateway=%q", credentialID, gatewayKeyID)
	}

	january, err := store.GetGatewayKeyMonthlyUsage(ctx, key.ID, time.Date(2026, time.January, 20, 12, 0, 0, 0, time.FixedZone("test", -8*60*60)))
	if err != nil {
		t.Fatal(err)
	}
	if january.MonthUTC != "2026-01" || january.RequestCount != 3 || january.InputTokens != 33 || january.OutputTokens != 16 || january.TotalTokens != 49 || january.CostUSD < 0.349999 || january.CostUSD > 0.350001 {
		t.Fatalf("unexpected January gateway usage: %+v", january)
	}
	february, err := store.GetGatewayKeyMonthlyUsage(ctx, key.ID, time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if february.RequestCount != 1 || february.TotalTokens != 200 || february.CostUSD != 1 {
		t.Fatalf("unexpected February gateway usage: %+v", february)
	}
	encoded, err := json.Marshal(january)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "not-json-and-not-read") {
		t.Fatal("monthly usage exposed raw request or prompt content")
	}
}

func TestListRecentGatewayRequestsReturnsOnlySafeMetadata(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "gateway-requests.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	key, err := store.CreateGatewayKey(ctx, GatewayKey{Name: "Recent key", KeyPrefix: "gw_recent", TokenHash: strings.Repeat("a", 64), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []APIRequest{
		{
			ID: "older", Kind: "gateway", GatewayKeyID: key.ID, Provider: "anthropic", CredentialID: "account-1", Model: "claude-safe",
			InputTokens: 4, OutputTokens: 3, CachedInputTokens: 2, ReasoningTokens: 1, TTFTMS: 12, DurationMS: 34, CostUSD: 0.01,
			ErrorMessage: "response body secret-token", RawDumpJSON: json.RawMessage(`{"prompt":"secret prompt","response":"secret response","token":"secret-token"}`),
			StopReason: "end_turn", CreatedAt: "2026-01-01T01:00:00Z",
		},
		{
			ID: "newer", Kind: "gateway", GatewayKeyID: key.ID, Provider: "codex", CredentialID: "account-2", Model: "gpt-safe",
			InputTokens: 10, OutputTokens: 5, RawDumpJSON: json.RawMessage(`{"prompt":"newer secret"}`), CreatedAt: "2025-12-31T22:00:00-05:00",
		},
		{ID: "internal-newest", Kind: "model", GatewayKeyID: key.ID, Model: "must-not-appear", CreatedAt: "2026-01-02T00:00:00Z"},
	} {
		if _, err := store.AddAPIRequest(ctx, request); err != nil {
			t.Fatal(err)
		}
	}

	limited, err := store.ListRecentGatewayRequests(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || limited[0].ID != "newer" || limited[0].GatewayKeyName != "Recent key" || limited[0].GatewayKeyPrefix != "gw_recent" || limited[0].TotalTokens != 15 {
		t.Fatalf("unexpected limited request summaries: %+v", limited)
	}
	requests, err := store.ListRecentGatewayRequests(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].ID != "newer" || requests[1].ID != "older" {
		t.Fatalf("unexpected recent Gateway request order/filter: %+v", requests)
	}
	encoded, err := json.Marshal(requests)
	if err != nil {
		t.Fatal(err)
	}
	payload := string(encoded)
	for _, forbidden := range []string{"secret prompt", "secret response", "secret-token", "rawDumpJson", "errorMessage", strings.Repeat("a", 64)} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("safe request summaries leaked %q: %s", forbidden, payload)
		}
	}
	if _, err := store.ListRecentGatewayRequests(ctx, 0); err == nil {
		t.Fatal("non-positive request limit was accepted")
	}
}
