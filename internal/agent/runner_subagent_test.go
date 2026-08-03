package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"autoto/internal/providers"
	"autoto/internal/tools"
)

type unavailableModelCatalogProvider struct{}

func (unavailableModelCatalogProvider) Name() string { return "catalogless" }
func (unavailableModelCatalogProvider) ListModels(context.Context) ([]string, error) {
	return nil, errors.New("catalog unavailable")
}
func (unavailableModelCatalogProvider) Generate(context.Context, providers.GenerateRequest) (<-chan providers.Event, error) {
	return nil, errors.New("not used")
}

func TestResolveSubagentModelNormalizesGeneralPurpose(t *testing.T) {
	runner := &Runner{}
	model, role, err := runner.ResolveSubagentModel("general-purpose", "", "parent:model")
	if err != nil {
		t.Fatal(err)
	}
	if role != "general" || model != "parent:model" {
		t.Fatalf("general-purpose did not normalize safely: model=%q role=%q", model, role)
	}
}

func TestResolveSubagentModelRejectsUnavailableExplicitModel(t *testing.T) {
	runner := &Runner{providers: providers.NewRegistry()}
	_, _, err := runner.ResolveSubagentModel("general", "missing:model", "parent:model")
	if err == nil || !strings.Contains(err.Error(), "missing:model") {
		t.Fatalf("unavailable explicit model was not rejected: %v", err)
	}
}

func TestResolveSubagentModelDefersWhenProviderCatalogIsUnavailable(t *testing.T) {
	registry := providers.NewRegistry()
	registry.Register(unavailableModelCatalogProvider{})
	runner := &Runner{providers: registry}
	model, role, err := runner.ResolveSubagentModel("general", "catalogless:known-at-runtime", "parent:model")
	if err != nil {
		t.Fatalf("catalog failure should defer to provider execution validation: %v", err)
	}
	if model != "catalogless:known-at-runtime" || role != "general" {
		t.Fatalf("unexpected deferred model resolution: model=%q role=%q", model, role)
	}
}

func TestAgentToolSchemaAcceptsExtendedReasoningEfforts(t *testing.T) {
	for _, effort := range []string{"max", "ultra"} {
		t.Run(effort, func(t *testing.T) {
			input, err := json.Marshal(map[string]any{
				"prompt":            "inspect",
				"reasoning_effort":  effort,
				"run_in_background": true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := normalizeToolCallInput(tools.Call{Name: "Agent", Input: input}, tools.AgentTool{}); err != nil {
				t.Fatalf("Agent schema rejected supported reasoning effort %q: %v", effort, err)
			}
		})
	}
}
