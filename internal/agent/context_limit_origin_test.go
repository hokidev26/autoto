package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/providers"
)

// relayStubProvider models the shape that produced the reported failure: an
// adapter with a protocol-level default window and per-model limits that may or
// may not be configured. scriptedProvider cannot stand in here because it does not
// implement DefaultContextTokenLimitProvider, which is the branch under test.
type relayStubProvider struct {
	name          string
	contextLimits map[string]int
	defaultLimit  int
}

func (p *relayStubProvider) Name() string { return p.name }

func (p *relayStubProvider) ListModels(context.Context) ([]string, error) {
	return []string{"claude-opus-5"}, nil
}

func (p *relayStubProvider) Generate(context.Context, providers.GenerateRequest) (<-chan providers.Event, error) {
	out := make(chan providers.Event)
	close(out)
	return out, nil
}

func (p *relayStubProvider) ModelCapabilities(model string) providers.ModelCapabilities {
	return providers.ModelCapabilities{ContextTokenLimit: p.contextLimits[model]}
}

func (p *relayStubProvider) DefaultContextTokenLimit() int { return p.defaultLimit }

// A limit that came from the provider's protocol default has to say so. The bare
// number is what made a dropped per-model limit unreadable: a relay defaulting to
// 200 000 produced the same message as a deliberate 200 000, so the user who had
// configured 1 000 000 had no way to tell the value never reached the runtime.
func TestContextBudgetErrorNamesTheProtocolDefaultAndTheModel(t *testing.T) {
	registry := providers.NewRegistry()
	registry.Register(&relayStubProvider{name: "relay", contextLimits: map[string]int{}, defaultLimit: 200000})
	runner := &Runner{providers: registry, cfg: config.AgentConfig{ContextTokenLimit: 120000}}

	limit, origin := runner.contextTokenLimitWithOrigin("relay:claude-opus-5")
	if limit != 200000 || origin != contextLimitOriginProtocol {
		t.Fatalf("limit = %d origin = %q, want 200000 and %q", limit, origin, contextLimitOriginProtocol)
	}

	annotated := annotateContextBudgetError(errorsContextBudget(limit, 200956), "relay:claude-opus-5", origin)
	if !errors.Is(annotated, ErrContextTokenBudget) {
		t.Fatal("annotation must preserve the sentinel so callers can still classify the failure")
	}
	message := annotated.Error()
	for _, want := range []string{
		"context token budget exceeded",
		"estimated 200956 tokens exceeds limit 200000",
		`"relay:claude-opus-5"`,
		"no per-model context limit is configured",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("error message %q is missing %q", message, want)
		}
	}
}

// A configured per-model limit must not be described as a missing setting, or the
// advice contradicts what the user already did.
func TestConfiguredPerModelLimitIsReportedAsConfigured(t *testing.T) {
	registry := providers.NewRegistry()
	registry.Register(&relayStubProvider{name: "relay", contextLimits: map[string]int{"claude-opus-5": 1000000}, defaultLimit: 200000})
	runner := &Runner{providers: registry, cfg: config.AgentConfig{ContextTokenLimit: 120000}}

	limit, origin := runner.contextTokenLimitWithOrigin("relay:claude-opus-5")
	if limit != 1000000 || origin != contextLimitOriginModel {
		t.Fatalf("limit = %d origin = %q, want 1000000 and %q", limit, origin, contextLimitOriginModel)
	}
	message := annotateContextBudgetError(errorsContextBudget(limit, 1000001), "relay:claude-opus-5", origin).Error()
	if !strings.Contains(message, "configured per-model context limit") {
		t.Fatalf("error message %q must attribute the limit to configuration", message)
	}
	if strings.Contains(message, "no per-model context limit is configured") {
		t.Fatalf("error message %q must not tell the user to set a limit they already set", message)
	}
}

// Non-budget errors travel unchanged, so the annotation cannot smuggle context
// wording onto an unrelated failure.
func TestAnnotationLeavesOtherErrorsAlone(t *testing.T) {
	original := errors.New("agent context store is unavailable")
	if got := annotateContextBudgetError(original, "relay:claude-opus-5", contextLimitOriginProtocol); got != original {
		t.Fatalf("annotated = %v, want the original error unchanged", got)
	}
	if annotateContextBudgetError(nil, "relay:claude-opus-5", contextLimitOriginProtocol) != nil {
		t.Fatal("a nil error must stay nil")
	}
}

// contextTokenLimit keeps its original signature, since callers that only need
// the number must not be forced to handle provenance.
func TestContextTokenLimitStillReturnsJustTheNumber(t *testing.T) {
	registry := providers.NewRegistry()
	registry.Register(&relayStubProvider{name: "relay", contextLimits: map[string]int{"claude-opus-5": 1000000}})
	runner := &Runner{providers: registry, cfg: config.AgentConfig{ContextTokenLimit: 120000}}
	if got := runner.contextTokenLimit("relay:claude-opus-5"); got != 1000000 {
		t.Fatalf("contextTokenLimit = %d, want 1000000", got)
	}
}
