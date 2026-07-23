package db

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGatewayAccountGrantsFreshSchemaAndV45Migration(t *testing.T) {
	ctx := context.Background()
	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	if !testTableExists(t, ctx, fresh.DB(), "gateway_account_grants") {
		fresh.Close()
		t.Fatal("fresh schema is missing gateway_account_grants")
	}
	wantColumns := []string{"provider", "account_id", "created_at", "updated_at"}
	if got := gatewayAccountGrantColumnNames(t, ctx, fresh); !reflect.DeepEqual(got, wantColumns) {
		fresh.Close()
		t.Fatalf("gateway_account_grants columns = %v, want %v", got, wantColumns)
	}
	if _, err := fresh.DB().ExecContext(ctx, `INSERT INTO gateway_account_grants (provider, account_id, created_at, updated_at) VALUES (?, 'account', 'created', 'updated')`, strings.Repeat("p", gatewayAccountGrantIDMaxBytes+1)); err == nil {
		fresh.Close()
		t.Fatal("fresh schema accepted an oversized provider id")
	}
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "v45.db")
	raw := openRawDB(t, path)
	// schemaSQL ends with "... + gatewayAccountGrantsSchemaSQL (v46) + generatedImagesSchemaSQL (v47)";
	// strip both trailing fragments to reconstruct the v45 schema before either table existed.
	v45Schema := strings.TrimSuffix(schemaSQL, generatedImagesSchemaSQL)
	v45Schema = strings.TrimSuffix(v45Schema, gatewayAccountGrantsSchemaSQL)
	if strings.Contains(v45Schema, gatewayAccountGrantsSchemaSQL) {
		raw.Close()
		t.Fatal("v45 schema fixture did not remove gateway_account_grants")
	}
	if _, err := raw.ExecContext(ctx, v45Schema); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `
INSERT INTO gateway_models (alias, target_model, enabled, created_at, updated_at)
VALUES ('legacy-model', 'openai:legacy', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
PRAGMA user_version = 45;
`); err != nil {
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
		t.Fatalf("migrated database version = %d, want %d", version, CurrentDBVersion)
	}
	if !testTableExists(t, ctx, migrated.DB(), "gateway_account_grants") {
		t.Fatal("v45 to v46 migration did not create gateway_account_grants")
	}
	var legacyTarget string
	if err := migrated.DB().QueryRowContext(ctx, `SELECT target_model FROM gateway_models WHERE alias = 'legacy-model'`).Scan(&legacyTarget); err != nil || legacyTarget != "openai:legacy" {
		t.Fatalf("v46 migration did not preserve existing gateway data: target=%q err=%v", legacyTarget, err)
	}
	tx, err := migrated.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateV46GatewayAccountGrants(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatalf("first idempotent migration rerun: %v", err)
	}
	if err := migrateV46GatewayAccountGrants(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatalf("second idempotent migration rerun: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayAccountGrantCRUDIdempotencyAndSafeShape(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "gateway-account-grants.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	grants, err := store.ListGatewayAccountGrants(ctx, "codex")
	if err != nil || len(grants) != 0 {
		t.Fatalf("initial grants = %+v, err=%v", grants, err)
	}
	if err := store.SetGatewayAccountGrant(ctx, " codex ", " account-b ", true); err != nil {
		t.Fatal(err)
	}
	grants, err = store.ListGatewayAccountGrants(ctx, " codex ")
	if err != nil || len(grants) != 1 {
		t.Fatalf("created grants = %+v, err=%v", grants, err)
	}
	first := grants[0]
	if first.Provider != "codex" || first.AccountID != "account-b" || first.CreatedAt == "" || first.UpdatedAt == "" {
		t.Fatalf("unexpected canonical grant: %+v", first)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"token", "secret", "credential", "password", "apiKey"} {
		if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(forbidden)) {
			t.Fatalf("gateway account grant exposed credential-shaped data %q: %s", forbidden, encoded)
		}
	}

	if err := store.SetGatewayAccountGrant(ctx, "codex", "account-b", true); err != nil {
		t.Fatal(err)
	}
	grants, err = store.ListGatewayAccountGrants(ctx, "codex")
	if err != nil || len(grants) != 1 || grants[0].CreatedAt != first.CreatedAt || grants[0].UpdatedAt == first.UpdatedAt {
		t.Fatalf("grant UPSERT was not idempotent: before=%+v after=%+v err=%v", first, grants, err)
	}
	if err := store.SetGatewayAccountGrant(ctx, "codex", "account-a", true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetGatewayAccountGrant(ctx, "anthropic", "account-z", true); err != nil {
		t.Fatal(err)
	}
	ids, err := store.ListSharedGatewayAccountIDs(ctx, "codex")
	if err != nil || !reflect.DeepEqual(ids, []string{"account-a", "account-b"}) {
		t.Fatalf("shared account ids = %v, err=%v", ids, err)
	}

	if err := store.SetGatewayAccountGrant(ctx, "codex", "account-b", false); err != nil {
		t.Fatal(err)
	}
	if err := store.SetGatewayAccountGrant(ctx, "codex", "account-b", false); err != nil {
		t.Fatalf("shared=false delete was not idempotent: %v", err)
	}
	if err := store.DeleteGatewayAccountGrant(ctx, "codex", "account-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteGatewayAccountGrant(ctx, "codex", "account-a"); err != nil {
		t.Fatalf("explicit delete was not idempotent: %v", err)
	}
	ids, err = store.ListSharedGatewayAccountIDs(ctx, "codex")
	if err != nil || len(ids) != 0 {
		t.Fatalf("deleted grants remain: %v, err=%v", ids, err)
	}
	otherIDs, err := store.ListSharedGatewayAccountIDs(ctx, "anthropic")
	if err != nil || !reflect.DeepEqual(otherIDs, []string{"account-z"}) {
		t.Fatalf("provider filtering failed: %v, err=%v", otherIDs, err)
	}
}

func TestGatewayAccountGrantRejectsUnsafeIDs(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "gateway-account-grants-invalid.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	invalidUTF8 := string([]byte{0xff, 0xfe})
	tests := []struct {
		name      string
		provider  string
		accountID string
	}{
		{name: "empty provider", provider: " ", accountID: "account"},
		{name: "empty account", provider: "codex", accountID: " "},
		{name: "long provider", provider: strings.Repeat("p", gatewayAccountGrantIDMaxBytes+1), accountID: "account"},
		{name: "long account", provider: "codex", accountID: strings.Repeat("a", gatewayAccountGrantIDMaxBytes+1)},
		{name: "invalid provider utf8", provider: invalidUTF8, accountID: "account"},
		{name: "invalid account utf8", provider: "codex", accountID: invalidUTF8},
		{name: "provider control", provider: "codex\n", accountID: "account"},
		{name: "account control", provider: "codex", accountID: "account\tbad"},
		{name: "provider format control", provider: "co\u200bdex", accountID: "account"},
		{name: "account format control", provider: "codex", accountID: "account\u200bbad"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := store.SetGatewayAccountGrant(ctx, test.provider, test.accountID, true); err == nil {
				t.Fatal("unsafe gateway account grant was accepted")
			}
			if err := store.SetGatewayAccountGrant(ctx, test.provider, test.accountID, false); err == nil {
				t.Fatal("unsafe gateway account grant delete was accepted")
			}
		})
	}
	if _, err := store.ListGatewayAccountGrants(ctx, "codex\n"); err == nil {
		t.Fatal("unsafe provider filter was accepted")
	}
	var count int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM gateway_account_grants`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unsafe gateway account ids entered the database: %d rows", count)
	}
}

func gatewayAccountGrantColumnNames(t *testing.T, ctx context.Context, store *Store) []string {
	t.Helper()
	rows, err := store.DB().QueryContext(ctx, `PRAGMA table_info(gateway_account_grants)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := make([]string, 0)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
}
