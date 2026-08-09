package server

import (
	"testing"

	"autoto/internal/config"
)

// A provider saved before per-model limits existed has an empty Models list. The
// update path treats an empty request list as "keep what is saved", and
// NormalizeProviderModels always re-adds the default model, so the first
// connection-only edit writes back a one-row list: the default model at limit 0.
//
// That row is not harmless. Once it exists the list is no longer empty, so the
// preserve-on-empty branch stops firing, and the provider is pinned to a single
// model row until something sends a full list. This is the shape ttapy has on
// disk while its console offers nine discovered models.
func TestConnectionOnlyEditOnAProviderWithNoSavedModels(t *testing.T) {
	existing := config.ProviderConfig{
		Name:    "ttapy",
		Type:    "anthropic",
		BaseURL: "https://relay.example.test",
		Model:   "claude-haiku-4-5",
		Models:  nil,
	}

	empty := []config.ProviderModelConfig{}
	req := providerConfigUpdateRequest{
		Name:    "ttapy",
		Type:    "anthropic",
		BaseURL: "https://relay.example.test",
		Model:   "claude-haiku-4-5",
		Models:  &empty,
	}

	updated, err := providerConfigFromUpdateRequest("ttapy", existing, req)
	if err != nil {
		t.Fatalf("providerConfigFromUpdateRequest returned an error: %v", err)
	}
	t.Logf("after a connection-only edit, models = %+v", updated.Models)

	// This is the observed on-disk state, recorded so a change of behaviour is
	// visible rather than silent.
	if len(updated.Models) != 1 || updated.Models[0].Name != "claude-haiku-4-5" || updated.Models[0].ContextTokenLimit != 0 {
		t.Fatalf("models = %+v, want exactly the default model at limit 0", updated.Models)
	}
}

// The user's actual goal: a limit set on a model that is not the provider default
// has to survive, and it has to be reachable by ModelContextTokenLimit, which is
// what the runner asks.
func TestLimitOnANonDefaultModelSurvivesAndIsReadable(t *testing.T) {
	existing := config.ProviderConfig{
		Name:   "ttapy",
		Type:   "anthropic",
		Model:  "claude-haiku-4-5",
		Models: []config.ProviderModelConfig{{Name: "claude-haiku-4-5"}},
	}

	// What the console sends once the drawer has its rows: every offered model,
	// with the one the user typed into carrying the limit.
	full := []config.ProviderModelConfig{
		{Name: "claude-haiku-4-5"},
		{Name: "claude-opus-4-5"},
		{Name: "claude-opus-5", ContextTokenLimit: 1000000},
	}
	req := providerConfigUpdateRequest{
		Name:    "ttapy",
		Type:    "anthropic",
		BaseURL: "https://relay.example.test",
		Model:   "claude-haiku-4-5",
		Models:  &full,
	}

	updated, err := providerConfigFromUpdateRequest("ttapy", existing, req)
	if err != nil {
		t.Fatalf("providerConfigFromUpdateRequest returned an error: %v", err)
	}
	if got := updated.ModelContextTokenLimit("claude-opus-5"); got != 1000000 {
		t.Fatalf("claude-opus-5 limit = %d, want 1000000; models = %+v", got, updated.Models)
	}
	// The default model keeps its unset limit, which the runner reads as
	// "fall back to the protocol default" rather than as zero context.
	if got := updated.ModelContextTokenLimit("claude-haiku-4-5"); got != 0 {
		t.Fatalf("claude-haiku-4-5 limit = %d, want 0 (unset)", got)
	}
}

// A limit above the accepted ceiling is clamped rather than stored, so a user who
// types a very large number gets the ceiling and not an unusable value.
func TestOversizedLimitIsClampedNotDropped(t *testing.T) {
	oversized := []config.ProviderModelConfig{
		{Name: "claude-opus-5", ContextTokenLimit: config.ProviderModelContextTokenLimitMax + 1},
	}
	req := providerConfigUpdateRequest{
		Name:    "ttapy",
		Type:    "anthropic",
		BaseURL: "https://relay.example.test",
		Model:   "claude-opus-5",
		Models:  &oversized,
	}
	updated, err := providerConfigFromUpdateRequest("ttapy", config.ProviderConfig{Name: "ttapy"}, req)
	if err != nil {
		t.Fatalf("providerConfigFromUpdateRequest returned an error: %v", err)
	}
	if got := updated.ModelContextTokenLimit("claude-opus-5"); got != config.ProviderModelContextTokenLimitMax {
		t.Fatalf("limit = %d, want the clamped ceiling %d", got, config.ProviderModelContextTokenLimitMax)
	}
}
