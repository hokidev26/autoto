package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"autoto/internal/providers"
)

// A provider whose credentials are missing reports ErrProviderUnavailable from
// every call, including the model listing. Treating that as a catalog deferral
// spent a whole child Run to discover something knowable at dispatch: the child
// passed preflight, started, and died on its first model call. Two research
// children were lost that way before this was fixed.
type credentiallessProvider struct{}

func (credentiallessProvider) Name() string { return "nocreds" }

func (credentiallessProvider) ListModels(context.Context) ([]string, error) {
	return nil, providersUnavailable("nocreds credentials are not configured")
}

func (credentiallessProvider) Generate(context.Context, providers.GenerateRequest) (<-chan providers.Event, error) {
	return nil, providersUnavailable("nocreds credentials are not configured")
}

func providersUnavailable(detail string) error {
	return errors.Join(providers.ErrProviderUnavailable, errors.New(detail))
}

func TestValidateSubagentModelRejectsUnavailableProvider(t *testing.T) {
	registry := providers.NewRegistry()
	registry.Register(credentiallessProvider{})
	runner := &Runner{providers: registry}

	err := runner.ValidateSubagentModel("nocreds:some-model")
	if err == nil {
		t.Fatal("a provider that cannot run must be rejected at preflight, not at execution")
	}
	if !errors.Is(err, providers.ErrProviderUnavailable) {
		t.Fatalf("the rejection must preserve the provider-unavailable sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), "nocreds:some-model") {
		t.Fatalf("the rejection must name the model that was asked for: %v", err)
	}
}

// Dispatch goes through ResolveSubagentModel, so the rejection has to surface
// there too rather than only in the lower-level check.
func TestResolveSubagentModelRejectsUnavailableProvider(t *testing.T) {
	registry := providers.NewRegistry()
	registry.Register(credentiallessProvider{})
	runner := &Runner{providers: registry}

	if _, _, err := runner.ResolveSubagentModel("general", "nocreds:some-model", "parent:model"); err == nil {
		t.Fatal("dispatch must not accept a model whose provider cannot run")
	}
}

// The deferral this replaces must survive for its own case: a provider that runs
// but cannot list models keeps its execution-time validation. Covered by
// TestResolveSubagentModelDefersWhenProviderCatalogIsUnavailable; asserted here
// against the lower-level check as well, so the two cannot drift apart.
func TestValidateSubagentModelStillDefersOnPlainCatalogFailure(t *testing.T) {
	registry := providers.NewRegistry()
	registry.Register(unavailableModelCatalogProvider{})
	runner := &Runner{providers: registry}

	if err := runner.ValidateSubagentModel("catalogless:known-at-runtime"); err != nil {
		t.Fatalf("a plain catalog failure must still defer: %v", err)
	}
}

// Omitting the model is the path that inherits the parent's, which is what keeps
// a child on a provider already known to work. It must not consult the catalog at
// all, or an unrelated provider outage would block dispatch.
func TestResolveSubagentModelInheritsParentWithoutValidation(t *testing.T) {
	registry := providers.NewRegistry()
	registry.Register(credentiallessProvider{})
	runner := &Runner{providers: registry}

	model, role, err := runner.ResolveSubagentModel("general", "", "ttapy:claude-opus-5")
	if err != nil {
		t.Fatalf("inheriting the parent model must not fail: %v", err)
	}
	if model != "ttapy:claude-opus-5" {
		t.Fatalf("child did not inherit the parent model: %q", model)
	}
	if role != "general" {
		t.Fatalf("unexpected role: %q", role)
	}
}
