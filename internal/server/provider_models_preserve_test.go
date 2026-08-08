package server

import (
	"testing"

	"autoto/internal/config"
)

// A saved provider carries per-model context limits that only the user can
// supply. An update whose models array is empty must not be read as "delete
// them all": the console builds that array from a draft, and any path that
// reaches save before the model list is populated would otherwise erase limits
// the user had already stored. There is no legitimate empty state to express
// either, because NormalizeProviderModels always re-adds the default model.
func TestProviderConfigUpdateKeepsSavedModelsWhenRequestListIsEmpty(t *testing.T) {
	existing := config.ProviderConfig{
		Name:    "ttapy",
		Type:    "anthropic",
		BaseURL: "https://relay.example.test",
		Model:   "claude-haiku-4-5",
		Models: []config.ProviderModelConfig{
			{Name: "claude-haiku-4-5"},
			{Name: "claude-opus-5", ContextTokenLimit: 1000000},
		},
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

	limit := updated.ModelContextTokenLimit("claude-opus-5")
	if limit != 1000000 {
		t.Fatalf("claude-opus-5 context limit = %d, want 1000000 (saved models must survive an empty request list); models = %+v", limit, updated.Models)
	}
	if len(updated.Models) != 2 {
		t.Fatalf("model count = %d, want 2; models = %+v", len(updated.Models), updated.Models)
	}
}

// A non-empty list is still authoritative: that is how the console removes a
// model or edits a limit.
func TestProviderConfigUpdateReplacesSavedModelsWhenRequestListHasEntries(t *testing.T) {
	existing := config.ProviderConfig{
		Name:   "ttapy",
		Type:   "anthropic",
		Model:  "claude-haiku-4-5",
		Models: []config.ProviderModelConfig{{Name: "claude-opus-5", ContextTokenLimit: 1000000}},
	}
	replacement := []config.ProviderModelConfig{{Name: "claude-opus-5", ContextTokenLimit: 250000}}
	req := providerConfigUpdateRequest{
		Name:    "ttapy",
		Type:    "anthropic",
		BaseURL: "https://relay.example.test",
		Model:   "claude-haiku-4-5",
		Models:  &replacement,
	}

	updated, err := providerConfigFromUpdateRequest("ttapy", existing, req)
	if err != nil {
		t.Fatalf("providerConfigFromUpdateRequest returned an error: %v", err)
	}
	if got := updated.ModelContextTokenLimit("claude-opus-5"); got != 250000 {
		t.Fatalf("claude-opus-5 context limit = %d, want 250000 (an explicit list must win)", got)
	}
}
