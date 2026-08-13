package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

func newRepeatTestRunner(t *testing.T, thresholds ...int) (*Runner, string) {
	t.Helper()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	t.Cleanup(func() { store.Close() })
	if len(thresholds) == 0 {
		thresholds = config.DefaultRepeatToolCallThresholds()
	}
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{RepeatToolCallThresholds: thresholds})
	return runner, agent.ID
}

func repeatTestCall(input string) tools.Call {
	return tools.Call{ID: "repeat-call", Name: "Bash", Input: json.RawMessage(input)}
}

// observeRepeat drives one attempt through the same entry point the loop uses
// and answers with the reminder the next turn would carry.
func observeRepeat(runner *Runner, agentID, runID string, call tools.Call) *providers.Message {
	runner.processToolResultForModel(agentID, runID, call, tools.Result{Output: "same answer as last time"})
	return runner.repeatToolCallControl(runID)
}

func TestRepeatedToolCallsRemindAtEveryConfiguredThreshold(t *testing.T) {
	runner, agentID := newRepeatTestRunner(t)
	call := repeatTestCall(`{"command":"ls"}`)

	fired := make([]int, 0, 3)
	for attempt := 1; attempt <= 9; attempt++ {
		reminder := observeRepeat(runner, agentID, "run-repeat", call)
		if reminder == nil {
			continue
		}
		fired = append(fired, attempt)
		if !reminder.TurnControl {
			t.Fatalf("reminder at attempt %d is not a turn control message", attempt)
		}
		if len(reminder.Blocks) != 1 || reminder.Blocks[0].Kind != "server_repeated_tool_call" {
			t.Fatalf("reminder at attempt %d has unexpected blocks: %+v", attempt, reminder.Blocks)
		}
	}

	if len(fired) != 3 || fired[0] != 3 || fired[1] != 5 || fired[2] != 8 {
		t.Fatalf("reminders fired at %v, want [3 5 8]", fired)
	}
}

func TestRepeatReminderEscalatesFromGentleToDetailed(t *testing.T) {
	runner, agentID := newRepeatTestRunner(t)
	call := repeatTestCall(`{"command":"ls -la /very/specific/path"}`)

	var gentle, detailed *providers.Message
	for attempt := 1; attempt <= 5; attempt++ {
		if reminder := observeRepeat(runner, agentID, "run-repeat", call); reminder != nil {
			if attempt == 3 {
				gentle = reminder
			} else {
				detailed = reminder
			}
		}
	}

	if gentle == nil || detailed == nil {
		t.Fatalf("missing a tier: gentle=%v detailed=%v", gentle != nil, detailed != nil)
	}
	if strings.Contains(gentle.Content, "/very/specific/path") {
		t.Fatalf("the first reminder should stay gentle, got %q", gentle.Content)
	}
	if !strings.Contains(detailed.Content, "/very/specific/path") || !strings.Contains(detailed.Content, "5 times") {
		t.Fatalf("the later reminder does not name the repeated call: %q", detailed.Content)
	}
}

func TestRepeatDetectionIgnoresArgumentKeyOrder(t *testing.T) {
	runner, agentID := newRepeatTestRunner(t, 2)
	first := tools.Call{ID: "a", Name: "Bash", Input: json.RawMessage(`{"command":"ls","timeout":5}`)}
	second := tools.Call{ID: "b", Name: "Bash", Input: json.RawMessage(`{"timeout":5,"command":"ls"}`)}

	if reminder := observeRepeat(runner, agentID, "run-repeat", first); reminder != nil {
		t.Fatal("a single call was reported as a repeat")
	}
	if reminder := observeRepeat(runner, agentID, "run-repeat", second); reminder == nil {
		t.Fatal("re-ordered arguments were treated as a different call")
	}
}

func TestDifferentArgumentsStartANewChain(t *testing.T) {
	runner, agentID := newRepeatTestRunner(t, 3)
	repeated := repeatTestCall(`{"command":"ls"}`)

	observeRepeat(runner, agentID, "run-repeat", repeated)
	observeRepeat(runner, agentID, "run-repeat", repeated)
	observeRepeat(runner, agentID, "run-repeat", repeatTestCall(`{"command":"pwd"}`))
	if reminder := observeRepeat(runner, agentID, "run-repeat", repeated); reminder != nil {
		t.Fatal("a different call in between did not reset the chain")
	}
}

func TestRepeatChainsAreScopedPerRun(t *testing.T) {
	runner, agentID := newRepeatTestRunner(t, 2)
	call := repeatTestCall(`{"command":"ls"}`)

	observeRepeat(runner, agentID, "run-one", call)
	if reminder := observeRepeat(runner, agentID, "run-two", call); reminder != nil {
		t.Fatal("a repeat chain leaked across runs")
	}
}

func TestANewUserMessageResetsTheChain(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	provider := &scriptedProvider{turns: [][]providers.Event{{{Type: "text", Text: "done"}, {Type: "done", Done: true, StopReason: "end_turn"}}}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{RepeatToolCallThresholds: []int{3}})
	call := repeatTestCall(`{"command":"ls"}`)
	runner.observeRepeatedToolCall(agent.ID, "run-before", call)
	runner.observeRepeatedToolCall(agent.ID, "run-before", call)

	message, err := runner.SubmitUserMessage(ctx, agent.ID, "try something else", "")
	if err != nil {
		t.Fatal(err)
	}
	waitForRunSettled(t, store, runner, agent.ID, message.RunID)

	if reminder := observeRepeat(runner, agent.ID, "run-before", call); reminder != nil {
		t.Fatal("the chain survived a new user message, so repetition across an interjection counts as a loop")
	}
}

func TestClosingARunReleasesItsRepeatChain(t *testing.T) {
	runner, agentID := newRepeatTestRunner(t, 3)
	call := repeatTestCall(`{"command":"ls"}`)
	runner.observeRepeatedToolCall(agentID, "run-closed", call)

	runner.closeRunRuntimeSnapshot("run-closed", agentID)

	state := runner.ensureRuntimeState()
	state.mu.RLock()
	remaining := len(state.repeats)
	state.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("closing a run left %d repeat chains behind", remaining)
	}
}

func TestRepeatDetectionIsDisabledByAnEmptyLadder(t *testing.T) {
	runner, agentID := newRepeatTestRunner(t, 0)
	call := repeatTestCall(`{"command":"ls"}`)

	for range 10 {
		if reminder := observeRepeat(runner, agentID, "run-repeat", call); reminder != nil {
			t.Fatal("reminders fired with the detector disabled")
		}
	}
}

// The loop this feature exists for: a model retrying a call that policy keeps
// refusing. The denial never reaches an executor, so it only counts because
// counting happens where every result converges.
func TestDeniedToolCallsCountTowardsTheRepeatChain(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.CreateToolPermissionRule(ctx, db.ToolPermissionRule{Mode: "acceptEdits", ToolName: "Bash", Risk: "exec", Decision: "deny", Priority: 50, Enabled: true, Description: "deny bash exec"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "run bash"}); err != nil {
		t.Fatal(err)
	}
	deniedTurn := []providers.Event{}
	for index := range 3 {
		deniedTurn = append(deniedTurn, providers.Event{Type: "tool_call", ToolCall: &providers.ToolCall{
			ID:    "denied-" + string(rune('a'+index)),
			Name:  "Bash",
			Input: json.RawMessage(`{"command":"printf denied"}`),
		}})
	}
	deniedTurn = append(deniedTurn, providers.Event{Type: "done", Done: true, StopReason: "tool_use"})
	provider := &scriptedProvider{turns: [][]providers.Event{
		deniedTurn,
		{{Type: "text", Text: "giving up"}, {Type: "done", Done: true, StopReason: "end_turn"}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 3, RepeatToolCallThresholds: []int{3}})

	runner.Run(ctx, agent.ID)

	if provider.requestCount() < 2 {
		t.Fatalf("the loop stopped before the reminder could be delivered: %d requests", provider.requestCount())
	}
	if !requestContainsSystemKind(provider.request(1), "server_repeated_tool_call") {
		t.Fatal("three denied attempts at the same call did not produce a repeat reminder")
	}
}

func TestCanonicalToolArgumentsFallsBackToTheRawText(t *testing.T) {
	if got := canonicalToolArguments(json.RawMessage(`{"command":`)); got != `{"command":` {
		t.Fatalf("canonicalToolArguments(malformed) = %q, want the raw text", got)
	}
	if got := canonicalToolArguments(nil); got != "" {
		t.Fatalf("canonicalToolArguments(nil) = %q, want empty", got)
	}
}
