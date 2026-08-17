package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"autoto/internal/config"
)

func loopbackRelayBaseURL(t *testing.T) string {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"relay-model"}]}`))
	}))
	t.Cleanup(upstream.Close)
	return upstream.URL + "/v1"
}

func TestProviderConfigPersistsClientIdentityPerProvider(t *testing.T) {
	app := newPlaintextProviderServer(t)
	baseURL := loopbackRelayBaseURL(t)
	payload := `{"name":"relay","type":"openai-compatible","baseUrl":"` + baseURL + `","apiKey":"sk-test","model":"relay-model","clientIdentity":"codex"}`
	if response := putProviderConfig(t, app, "relay", payload); response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}

	var stored config.ProviderConfig
	for _, instance := range app.configSnapshot().Providers.Instances {
		if instance.Name == "relay" {
			stored = instance
		}
	}
	if stored.ClientIdentity != config.ClientIdentityCodex {
		t.Fatalf("expected Codex identity to persist, got %+v", stored)
	}
	if response := app.settingsProviderResponse(context.Background(), stored); response.ClientIdentity != config.ClientIdentityCodex {
		t.Fatalf("expected settings response to expose clientIdentity, got %+v", response)
	}

	omit := `{"name":"relay","type":"openai-compatible","baseUrl":"` + baseURL + `","apiKey":"sk-test","model":"relay-model"}`
	if response := putProviderConfig(t, app, "relay", omit); response.Code != http.StatusOK {
		t.Fatalf("expected 200 omitting identity, got %d: %s", response.Code, response.Body.String())
	}
	for _, instance := range app.configSnapshot().Providers.Instances {
		if instance.Name == "relay" && instance.ClientIdentity != config.ClientIdentityCodex {
			t.Fatalf("omitted clientIdentity must keep the saved value, got %q", instance.ClientIdentity)
		}
	}

	reset := `{"name":"relay","type":"openai-compatible","baseUrl":"` + baseURL + `","apiKey":"sk-test","model":"relay-model","clientIdentity":"autoto"}`
	if response := putProviderConfig(t, app, "relay", reset); response.Code != http.StatusOK {
		t.Fatalf("expected 200 resetting identity, got %d: %s", response.Code, response.Body.String())
	}
	for _, instance := range app.configSnapshot().Providers.Instances {
		if instance.Name == "relay" && instance.ClientIdentity != "" {
			t.Fatalf("autoto should persist as empty default, got %q", instance.ClientIdentity)
		}
	}
}

func TestProviderConfigRejectsUnknownClientIdentity(t *testing.T) {
	app := newPlaintextProviderServer(t)
	payload := `{"name":"relay","type":"openai-compatible","baseUrl":"https://relay.example/v1","apiKey":"sk-test","model":"relay-model","clientIdentity":"evil"}`
	if response := putProviderConfig(t, app, "relay", payload); response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown clientIdentity, got %d: %s", response.Code, response.Body.String())
	}
}

func TestProviderConfigDefaultsClientIdentityToAutoto(t *testing.T) {
	app := newPlaintextProviderServer(t)
	payload := `{"name":"relay","type":"openai-compatible","baseUrl":"` + loopbackRelayBaseURL(t) + `","apiKey":"sk-test","model":"relay-model"}`
	if response := putProviderConfig(t, app, "relay", payload); response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	for _, instance := range app.configSnapshot().Providers.Instances {
		if instance.Name == "relay" && instance.ClientIdentity != "" {
			t.Fatalf("omitted clientIdentity must stay Autoto default, got %q", instance.ClientIdentity)
		}
	}
}
