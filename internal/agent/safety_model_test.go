package agent

import (
	"testing"

	"autoto/internal/config"
	"autoto/internal/providers"
)

// The safety gate used to share the summary model, which is routinely pointed at
// a small cheap model for titles and context compaction. That silently graded
// safety decisions down to the same tier, so the override exists to break the
// coupling — while still falling back, so an untouched config keeps working.
func TestSafetyModelFallsBackToSummaryModel(t *testing.T) {
	for _, test := range []struct {
		name    string
		summary string
		safety  string
		want    string
	}{
		{name: "unset falls back", summary: "fake:summary", safety: "", want: "fake:summary"},
		{name: "override wins", summary: "fake:cheap", safety: "fake:strong", want: "fake:strong"},
		{name: "both unset", summary: "", safety: "", want: ""},
		{name: "override without summary", summary: "", safety: "fake:strong", want: "fake:strong"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &Runner{}
			runner.SetAgentModelSettings(config.AgentConfig{
				SummaryModel: test.summary,
				SafetyModel:  test.safety,
			})
			if got := runner.SafetyModel(); got != test.want {
				t.Fatalf("SafetyModel() = %q, want %q", got, test.want)
			}
		})
	}
}

// A cheap summary model must not disable the gate, and must not be what judges.
func TestDangerReflectionEnabledTracksTheSafetyModel(t *testing.T) {
	runner := &Runner{providers: providers.NewRegistry()}

	runner.SetAgentModelSettings(config.AgentConfig{})
	if runner.dangerReflectionEnabled() {
		t.Fatal("reflection must stay disabled when no model is configured at all")
	}

	runner.SetAgentModelSettings(config.AgentConfig{SummaryModel: "fake:summary"})
	if !runner.dangerReflectionEnabled() {
		t.Fatal("reflection must run off the summary model when no override is set")
	}

	runner.SetAgentModelSettings(config.AgentConfig{SafetyModel: "fake:strong"})
	if !runner.dangerReflectionEnabled() {
		t.Fatal("an override alone must enable reflection")
	}
	if got := runner.SafetyModel(); got != "fake:strong" {
		t.Fatalf("reflection would use %q, not the configured safety model", got)
	}
}
