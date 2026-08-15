package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/network"
	"autoto/internal/providers"
)

func newPlaintextProviderServer(t *testing.T) *Server {
	t.Helper()
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name:    "relay",
		Type:    "openai-compatible",
		BaseURL: "https://relay.example/v1",
		Model:   "relay-model",
	}}}}, nil, nil, nil, providers.NewRegistry())
	app.SetConfigPath(filepath.Join(t.TempDir(), "config.json"))
	return app
}

func putProviderConfig(t *testing.T, app *Server, name, payload string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPut, "/api/providers/"+name+"/config", bytes.NewReader([]byte(payload)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(recorder, request)
	return recorder
}

func TestProviderConfigPersistsAllowPlaintextHTTPFlag(t *testing.T) {
	app := newPlaintextProviderServer(t)
	payload := `{"name":"relay","type":"openai-compatible","baseUrl":"http://154.36.172.121:3000/v1","apiKey":"sk-test","model":"relay-model","allowPlaintextHTTP":true}`
	if response := putProviderConfig(t, app, "relay", payload); response.Code != http.StatusOK {
		t.Fatalf("expected 200 with the opt-in, got %d: %s", response.Code, response.Body.String())
	}

	var stored config.ProviderConfig
	for _, instance := range app.configSnapshot().Providers.Instances {
		if instance.Name == "relay" {
			stored = instance
		}
	}
	if !stored.AllowPlaintextHTTP {
		t.Fatalf("expected AllowPlaintextHTTP to persist, got %+v", stored)
	}
	// The flag must round-trip back to the UI or the toggle would look off again.
	if response := app.settingsProviderResponse(context.Background(), stored); !response.AllowPlaintextHTTP {
		t.Fatalf("expected settings response to expose the flag, got %+v", response)
	}
}

func TestProviderConfigDefaultsAllowPlaintextHTTPOff(t *testing.T) {
	// A loopback upstream saves without the opt-in, which is exactly the case
	// that must not silently acquire the flag.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"relay-model"}]}`))
	}))
	defer upstream.Close()

	app := newPlaintextProviderServer(t)
	payload := `{"name":"relay","type":"openai-compatible","baseUrl":"` + upstream.URL + `/v1","apiKey":"sk-test","model":"relay-model"}`
	if response := putProviderConfig(t, app, "relay", payload); response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	for _, instance := range app.configSnapshot().Providers.Instances {
		if instance.Name == "relay" && instance.AllowPlaintextHTTP {
			t.Fatal("AllowPlaintextHTTP must stay off unless explicitly requested")
		}
	}
}

func TestProviderConfigRejectsPlaintextRelayWithoutOptIn(t *testing.T) {
	app := newPlaintextProviderServer(t)
	payload := `{"name":"relay","type":"openai-compatible","baseUrl":"http://154.36.172.121:3000/v1","apiKey":"sk-test","model":"relay-model"}`
	response := putProviderConfig(t, app, "relay", payload)
	if response.Code == http.StatusOK {
		t.Fatalf("expected a plain-HTTP public relay to be refused without the opt-in, got 200: %s", response.Body.String())
	}
}

func TestAllowPlaintextHTTPCountsAsTransportScopeChange(t *testing.T) {
	current := config.ProviderConfig{Name: "relay", Type: "openai-compatible", BaseURL: "http://relay.example/v1"}
	next := current
	next.AllowPlaintextHTTP = true
	if !providerTransportScopeChanged(current, next) {
		t.Fatal("flipping AllowPlaintextHTTP must re-register the provider transport")
	}
	if providerTransportScopeChanged(current, current) {
		t.Fatal("an unchanged provider must not report a transport scope change")
	}
}

func TestDescribeProviderConfigErrorExplainsPlaintextDenial(t *testing.T) {
	provider := config.ProviderConfig{Name: "relay", BaseURL: "http://154.36.172.121:3000/v1"}
	message := describeProviderConfigError(provider, network.ErrDestinationDenied)
	if !strings.Contains(message, "明文 HTTP") || !strings.Contains(message, "https://") {
		t.Fatalf("expected actionable plaintext guidance, got %q", message)
	}
	// The network package keeps host detail out of its errors; that must hold here too.
	if strings.Contains(message, "154.36.172.121") {
		t.Fatalf("message leaked the destination host: %q", message)
	}
}

func TestDescribeProviderConfigErrorOmitsPlaintextHintWhenNotApplicable(t *testing.T) {
	httpsProvider := config.ProviderConfig{Name: "relay", BaseURL: "https://relay.example/v1"}
	if message := describeProviderConfigError(httpsProvider, network.ErrDestinationDenied); strings.Contains(message, "明文 HTTP") {
		t.Fatalf("HTTPS denial must not suggest the plaintext toggle: %q", message)
	}

	optedIn := config.ProviderConfig{Name: "relay", BaseURL: "http://relay.example/v1", AllowPlaintextHTTP: true}
	if message := describeProviderConfigError(optedIn, network.ErrDestinationDenied); strings.Contains(message, "明文 HTTP") {
		t.Fatalf("an already opted-in provider must not be told to enable the toggle: %q", message)
	}

	if message := describeProviderConfigError(httpsProvider, errors.New("boom")); message != "Provider 設定無效。" {
		t.Fatalf("unknown causes should keep the generic message, got %q", message)
	}
}

func TestProviderDraftTestSurfacesPlaintextGuidance(t *testing.T) {
	app := newPlaintextProviderServer(t)
	payload := `{"name":"relay","type":"openai-compatible","baseUrl":"http://154.36.172.121:3000/v1","apiKey":"sk-test","model":"relay-model"}`
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/providers/test", bytes.NewReader([]byte(payload)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(recorder, request)

	body := recorder.Body.String()
	if strings.Contains(body, "Provider 設定無效") {
		t.Fatalf("draft preflight still returns the opaque message: %s", body)
	}
	if !strings.Contains(body, "明文 HTTP") {
		t.Fatalf("expected the preflight to explain the plaintext denial: %s", body)
	}
	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("preflight response was not JSON: %v", err)
	}
}
