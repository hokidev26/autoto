package providers

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/subscriptionauth"
)

type subscriptionTestAccountParams struct {
	provider      string
	priority      int
	disabled      bool
	accessToken   string
	refreshToken  string
	expiresAt     string
	deviceID      string
	tokenEndpoint string
}

func createSubscriptionTestAccount(t *testing.T, store *subscriptionauth.Store, params subscriptionTestAccountParams) subscriptionauth.StoredCredential {
	t.Helper()
	item, err := store.CreateOAuth(subscriptionauth.CreateRequest{
		Provider:      params.provider,
		Priority:      params.priority,
		Disabled:      params.disabled,
		AccessToken:   params.accessToken,
		RefreshToken:  params.refreshToken,
		ExpiresAt:     params.expiresAt,
		DeviceID:      params.deviceID,
		TokenEndpoint: params.tokenEndpoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

type subscriptionGatewayPolicy struct {
	provider string
	ids      []string
	err      error
}

func (p subscriptionGatewayPolicy) ListSharedGatewayAccountIDs(_ context.Context, provider string) ([]string, error) {
	if p.err != nil {
		return nil, p.err
	}
	if provider != p.provider {
		return nil, nil
	}
	return append([]string(nil), p.ids...), nil
}

type subscriptionRecordingTelemetry struct {
	mu       sync.Mutex
	attempts []ProviderAccountAttempt
}

func (r *subscriptionRecordingTelemetry) RecordProviderAccountAttempt(_ context.Context, attempt ProviderAccountAttempt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts = append(r.attempts, attempt)
	return nil
}

func (r *subscriptionRecordingTelemetry) snapshot() []ProviderAccountAttempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ProviderAccountAttempt(nil), r.attempts...)
}

func TestSubscriptionProviderAccountsFiltersAndSorts(t *testing.T) {
	store := subscriptionauth.NewStore(t.TempDir())
	late := createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{provider: subscriptionauth.ProviderGrok, priority: 200, accessToken: "late"})
	firstTie := createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{provider: subscriptionauth.ProviderGrok, priority: 100, accessToken: "tie-a"})
	secondTie := createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{provider: subscriptionauth.ProviderGrok, priority: 100, accessToken: "tie-b"})
	createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{provider: subscriptionauth.ProviderGrok, priority: 1, disabled: true, accessToken: "disabled"})
	createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{provider: subscriptionauth.ProviderKimi, priority: 1, accessToken: "wrong-provider", deviceID: "device"})

	accounts := newSubscriptionProviderAccounts(config.ProviderConfig{Name: "grok", CredentialStorePath: store.Dir()}, subscriptionauth.ProviderGrok)
	items, err := accounts.enabledAccounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("expected three eligible Grok accounts, got %+v", items)
	}
	tieIDs := []string{firstTie.ID, secondTie.ID}
	sort.Strings(tieIDs)
	want := []string{tieIDs[0], tieIDs[1], late.ID}
	for index, id := range want {
		if items[index].ID != id {
			t.Fatalf("account %d = %q, want %q", index, items[index].ID, id)
		}
	}
	if !accounts.configured() || accounts.configuredForScenario(CallScenarioGateway) {
		t.Fatalf("unexpected configured state: internal=%v gateway=%v", accounts.configured(), accounts.configuredForScenario(CallScenarioGateway))
	}
}

func TestSubscriptionProviderAccountsRequiresExplicitGatewayGrant(t *testing.T) {
	store := subscriptionauth.NewStore(t.TempDir())
	first := createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{provider: subscriptionauth.ProviderGrok, priority: 10, accessToken: "first"})
	second := createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{provider: subscriptionauth.ProviderGrok, priority: 20, accessToken: "second"})
	accounts := newSubscriptionProviderAccounts(config.ProviderConfig{Name: "grok", CredentialStorePath: store.Dir()}, subscriptionauth.ProviderGrok)

	if _, err := accounts.accountsForRequest(context.Background(), GenerateRequest{Scenario: CallScenarioGateway}); err == nil {
		t.Fatal("Gateway request without subscription sharing must fail")
	}
	if _, err := accounts.accountsForRequest(context.Background(), GenerateRequest{Scenario: CallScenarioGateway, AllowSubscriptionCredentials: true}); err == nil {
		t.Fatal("Gateway request without explicit policy grants must fail")
	}
	accounts.setGatewayAccountPolicy(subscriptionGatewayPolicy{provider: "grok", ids: []string{second.ID}})
	items, err := accounts.accountsForRequest(context.Background(), GenerateRequest{Scenario: CallScenarioGateway, AllowSubscriptionCredentials: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != second.ID || items[0].ID == first.ID {
		t.Fatalf("unexpected granted accounts: %+v", items)
	}
	if !accounts.availableForScenario(context.Background(), ScenarioAvailability{Scenario: CallScenarioGateway, AllowSubscriptionCredentials: true}) {
		t.Fatal("granted Gateway account should be dynamically available")
	}
	accounts.setGatewayAccountPolicy(subscriptionGatewayPolicy{provider: "grok", err: errors.New("database unavailable")})
	if accounts.availableForScenario(context.Background(), ScenarioAvailability{Scenario: CallScenarioGateway, AllowSubscriptionCredentials: true}) {
		t.Fatal("grant lookup failure must not make subscription accounts available")
	}
}

// Renaming a provider must not detach it from its grants. Antigravity is
// conventionally configured as "gemini-oauth" while its accounts live under
// "gemini", so a lookup keyed on the config name found no grant and every
// Antigravity model silently vanished from the Gateway model list even with the
// account shared and the provider marked gateway-eligible.
func TestSubscriptionProviderAccountsMatchGrantsAfterProviderRename(t *testing.T) {
	store := subscriptionauth.NewStore(t.TempDir())
	account := createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{provider: subscriptionauth.ProviderGemini, priority: 10, accessToken: "granted"})
	accounts := newSubscriptionProviderAccounts(config.ProviderConfig{Name: "gemini-oauth", CredentialStorePath: store.Dir()}, subscriptionauth.ProviderGemini)

	// The account pool files grants under the credential store's provider name.
	accounts.setGatewayAccountPolicy(subscriptionGatewayPolicy{provider: subscriptionauth.ProviderGemini, ids: []string{account.ID}})
	items, err := accounts.accountsForRequest(context.Background(), GenerateRequest{Scenario: CallScenarioGateway, AllowSubscriptionCredentials: true})
	if err != nil {
		t.Fatalf("renamed provider must still match its grants: %v", err)
	}
	if len(items) != 1 || items[0].ID != account.ID {
		t.Fatalf("unexpected granted accounts: %+v", items)
	}
	if !accounts.availableForScenario(context.Background(), ScenarioAvailability{Scenario: CallScenarioGateway, AllowSubscriptionCredentials: true}) {
		t.Fatal("granted account must keep a renamed provider available to the Gateway")
	}

	// A grant filed under the user-editable config name is not what the account
	// pool writes, so honouring it would hide the real lookup key regressing.
	accounts.setGatewayAccountPolicy(subscriptionGatewayPolicy{provider: "gemini-oauth", ids: []string{account.ID}})
	if accounts.availableForScenario(context.Background(), ScenarioAvailability{Scenario: CallScenarioGateway, AllowSubscriptionCredentials: true}) {
		t.Fatal("config-name grants must not authorize Gateway access")
	}
}

func TestSubscriptionProviderAccountsRefreshRotationAndPreservation(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	t.Run("rotates refresh token atomically", func(t *testing.T) {
		store := subscriptionauth.NewStore(t.TempDir())
		item := createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{
			provider: subscriptionauth.ProviderGrok, priority: 10, accessToken: "old-access", refreshToken: "old-refresh",
			expiresAt: now.Add(time.Minute).Format(time.RFC3339Nano), tokenEndpoint: "https://auth.x.ai/token",
		})
		accounts := newSubscriptionProviderAccounts(config.ProviderConfig{Name: "grok", CredentialStorePath: store.Dir()}, subscriptionauth.ProviderGrok)
		accounts.clock = func() time.Time { return now }
		prepared, err := accounts.prepareCredential(context.Background(), item, func(_ context.Context, credential subscriptionauth.Credential) (subscriptionauth.TokenUpdate, error) {
			if credential.RefreshToken != "old-refresh" || credential.TokenEndpoint != "https://auth.x.ai/token" {
				t.Fatalf("refresh received wrong credential metadata: %+v", subscriptionauth.Summary(subscriptionauth.StoredCredential{Credential: credential}))
			}
			return subscriptionauth.TokenUpdate{AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano)}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if prepared.AccessToken != "new-access" || prepared.RefreshToken != "new-refresh" || prepared.TokenEndpoint != "https://auth.x.ai/token" {
			t.Fatalf("unexpected refreshed credential: %+v", subscriptionauth.Summary(prepared))
		}
		stored, err := store.GetByID(item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.AccessToken != "new-access" || stored.RefreshToken != "new-refresh" {
			t.Fatalf("rotated tokens were not stored atomically: %+v", subscriptionauth.Summary(stored))
		}
	})

	t.Run("preserves unrotated refresh token", func(t *testing.T) {
		store := subscriptionauth.NewStore(t.TempDir())
		item := createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{
			provider: subscriptionauth.ProviderKimi, priority: 10, accessToken: "old-access", refreshToken: "keep-refresh",
			expiresAt: now.Add(time.Minute).Format(time.RFC3339Nano), deviceID: "device-1",
		})
		accounts := newSubscriptionProviderAccounts(config.ProviderConfig{Name: "kimi", CredentialStorePath: store.Dir()}, subscriptionauth.ProviderKimi)
		accounts.clock = func() time.Time { return now }
		prepared, err := accounts.prepareCredential(context.Background(), item, func(_ context.Context, credential subscriptionauth.Credential) (subscriptionauth.TokenUpdate, error) {
			return subscriptionauth.TokenUpdate{AccessToken: "new-access", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano), DeviceID: credential.DeviceID}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if prepared.RefreshToken != "keep-refresh" || prepared.DeviceID != "device-1" {
			t.Fatalf("refresh preservation failed: %+v", subscriptionauth.Summary(prepared))
		}
	})
}

func TestSubscriptionProviderAccountsRereadsStoreBeforeRefresh(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	store := subscriptionauth.NewStore(t.TempDir())
	item := createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{
		provider: subscriptionauth.ProviderGrok, priority: 10, accessToken: "stale-access", refreshToken: "stale-refresh",
		expiresAt: now.Add(time.Minute).Format(time.RFC3339Nano), tokenEndpoint: "https://auth.x.ai/token",
	})
	if _, err := store.UpdateTokens(item.ID, subscriptionauth.TokenUpdate{
		AccessToken: "fresh-from-other-request", RefreshToken: "rotated-elsewhere", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano), TokenEndpoint: "https://auth.x.ai/token",
	}); err != nil {
		t.Fatal(err)
	}
	accounts := newSubscriptionProviderAccounts(config.ProviderConfig{Name: "grok", CredentialStorePath: store.Dir()}, subscriptionauth.ProviderGrok)
	accounts.clock = func() time.Time { return now }
	refreshCalled := false
	prepared, err := accounts.prepareCredential(context.Background(), item, func(context.Context, subscriptionauth.Credential) (subscriptionauth.TokenUpdate, error) {
		refreshCalled = true
		return subscriptionauth.TokenUpdate{}, errors.New("must not refresh stale copy")
	})
	if err != nil {
		t.Fatal(err)
	}
	if refreshCalled || prepared.AccessToken != "fresh-from-other-request" || prepared.RefreshToken != "rotated-elsewhere" {
		t.Fatalf("store reread did not win: called=%v credential=%+v", refreshCalled, subscriptionauth.Summary(prepared))
	}
}

func TestSubscriptionProviderAccountsUsesUnexpiredAccessWithoutRefreshToken(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	store := subscriptionauth.NewStore(t.TempDir())
	unexpired := createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{
		provider: subscriptionauth.ProviderGrok, priority: 10, accessToken: "still-valid", expiresAt: now.Add(time.Minute).Format(time.RFC3339Nano),
	})
	expired := createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{
		provider: subscriptionauth.ProviderGrok, priority: 20, accessToken: "expired-token", expiresAt: now.Add(-time.Second).Format(time.RFC3339Nano),
	})
	accounts := newSubscriptionProviderAccounts(config.ProviderConfig{Name: "grok", CredentialStorePath: store.Dir()}, subscriptionauth.ProviderGrok)
	accounts.clock = func() time.Time { return now }
	prepared, err := accounts.prepareCredential(context.Background(), unexpired, nil)
	if err != nil || prepared.AccessToken != "still-valid" {
		t.Fatalf("unexpired access token should remain usable: %+v %v", subscriptionauth.Summary(prepared), err)
	}
	if _, err := accounts.prepareCredential(context.Background(), expired, nil); err == nil {
		t.Fatal("actually expired access token without refresh token must fail")
	}
}

func TestSubscriptionProviderAccountTelemetryAndErrorsDoNotContainTokens(t *testing.T) {
	const secret = "secret-access-token"
	accounts := newSubscriptionProviderAccounts(config.ProviderConfig{Name: "grok"}, subscriptionauth.ProviderGrok)
	telemetry := &subscriptionRecordingTelemetry{}
	accounts.setAccountTelemetry(telemetry)
	err := newSubscriptionHTTPError("grok", 429, "rate_limit_error "+secret)
	accounts.recordAttempt(context.Background(), "account-1", false, 429, "", err)
	attempts := telemetry.snapshot()
	if len(attempts) != 1 {
		t.Fatalf("expected one telemetry attempt, got %+v", attempts)
	}
	serialized := attempts[0].Provider + attempts[0].AccountID + attempts[0].StatusCode + attempts[0].ErrorCode
	if strings.Contains(serialized, secret) {
		t.Fatalf("telemetry leaked token: %+v", attempts[0])
	}
	safe := sanitizeSubscriptionError(context.Background(), "grok", err).Error()
	if !strings.Contains(safe, "429") || strings.Contains(safe, secret) {
		t.Fatalf("unexpected sanitized error %q", safe)
	}
}
