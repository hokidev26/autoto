package providers

import (
	"errors"
	"testing"

	"autoto/internal/config"
	"autoto/internal/network"
)

// relayConfig mirrors the reported setup: an OpenAI-compatible relay reachable
// only over plain HTTP on a public address.
func relayConfig(allowPlaintext bool) config.ProviderConfig {
	return config.ProviderConfig{
		Name:               "relay",
		Type:               "openai-compatible",
		BaseURL:            "http://154.36.172.121:3000/v1",
		APIKey:             "sk-test",
		Model:              "claude-opus-5",
		AllowPlaintextHTTP: allowPlaintext,
	}
}

func TestValidateProviderRuntimeConfigDeniesPlaintextHTTPByDefault(t *testing.T) {
	err := validateProviderRuntimeConfig(relayConfig(false))
	if !errors.Is(err, network.ErrDestinationDenied) {
		t.Fatalf("expected ErrDestinationDenied for a plain-HTTP public relay, got %v", err)
	}
}

func TestValidateProviderRuntimeConfigAllowsPlaintextHTTPWhenOptedIn(t *testing.T) {
	if err := validateProviderRuntimeConfig(relayConfig(true)); err != nil {
		t.Fatalf("expected the per-provider opt-in to permit the relay, got %v", err)
	}
}

func TestNewProviderRejectsPlaintextRelayWithoutOptIn(t *testing.T) {
	if _, err := NewProvider(relayConfig(false)); !errors.Is(err, network.ErrDestinationDenied) {
		t.Fatalf("expected NewProvider to refuse the relay, got %v", err)
	}
}

func TestNewProviderBuildsPlaintextRelayWithOptIn(t *testing.T) {
	adapter, err := NewProvider(relayConfig(true))
	if err != nil {
		t.Fatalf("expected NewProvider to accept the opted-in relay, got %v", err)
	}
	if adapter == nil {
		t.Fatal("expected a provider adapter")
	}
}

func TestPlaintextOptInDoesNotAffectHTTPSProviders(t *testing.T) {
	cfg := relayConfig(false)
	cfg.BaseURL = "https://154.36.172.121:3000/v1"
	if err := validateProviderRuntimeConfig(cfg); err != nil {
		t.Fatalf("expected HTTPS to validate without the opt-in, got %v", err)
	}
}
