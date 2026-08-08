package agent

import (
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
)

// Danger reflection follows the model assigned to the active conversation;
// global summary/safety settings must not affect that selection.
func TestDangerReflectionEnabledTracksTheActiveConversationModel(t *testing.T) {
	runner := &Runner{providers: providers.NewRegistry()}

	runner.SetAgentModelSettings(config.AgentConfig{SummaryModel: "fake:small", SafetyModel: "fake:strong"})
	if runner.dangerReflectionEnabled(db.Agent{}) {
		t.Fatal("reflection must stay disabled when the active agent has no model")
	}
	if !runner.dangerReflectionEnabled(db.Agent{Model: "fake:conversation"}) {
		t.Fatal("reflection must follow a configured active conversation model")
	}
}
