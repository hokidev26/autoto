package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

// replayProbeTool is a minimal tool whose risk and replay answers are set per
// case, so the trusted-derivation rules can be exercised without a real tool.
type replayProbeTool struct {
	name   string
	risk   tools.Risk
	replay tools.ReplayClass
	// claims controls whether the tool implements ReplayClassifier at all.
	claims bool
}

func (t replayProbeTool) Name() string                    { return t.name }
func (t replayProbeTool) Description() string             { return "replay probe" }
func (t replayProbeTool) Schema() any                     { return struct{}{} }
func (t replayProbeTool) Risk(json.RawMessage) tools.Risk { return t.risk }
func (t replayProbeTool) Execute(context.Context, tools.Call, tools.Env) (tools.Result, error) {
	return tools.Result{Output: "ok"}, nil
}

type replayClaimingTool struct {
	replayProbeTool
}

func (t replayClaimingTool) ReplayClass(json.RawMessage) tools.ReplayClass { return t.replay }

func newReplayProbe(name string, risk tools.Risk, replay tools.ReplayClass, claims bool) tools.Tool {
	base := replayProbeTool{name: name, risk: risk, replay: replay, claims: claims}
	if !claims {
		return base
	}
	return replayClaimingTool{replayProbeTool: base}
}

// TestClassifyReplayDefaultsToNever is the fail-closed guarantee: a tool that
// does not implement ReplayClassifier is never replayable, so every existing
// tool and every future plugin/MCP adapter is safe without being aware of the
// interface.
func TestClassifyReplayDefaultsToNever(t *testing.T) {
	silent := newReplayProbe("Silent", tools.RiskRead, tools.ReplaySafe, false)
	if got := tools.ClassifyReplay(silent, json.RawMessage(`{}`)); got != tools.ReplayNever {
		t.Fatalf("a tool that does not implement ReplayClassifier must be never, got %q", got)
	}
	if got := tools.ClassifyReplay(nil, json.RawMessage(`{}`)); got != tools.ReplayNever {
		t.Fatalf("a nil tool must be never, got %q", got)
	}
}

// TestClassifyReplayRequiresReadRisk is the reason ClassifyReplay exists rather
// than trusting the tool. A dynamic tool can implement the interface and claim
// safety while carrying a risk that contradicts it; the trusted server confirms
// the claim against the same risk signal the approval path uses.
func TestClassifyReplayRequiresReadRisk(t *testing.T) {
	for _, risk := range []tools.Risk{tools.RiskWrite, tools.RiskExec, tools.RiskDanger} {
		liar := newReplayProbe("Liar", risk, tools.ReplaySafe, true)
		if got := tools.ClassifyReplay(liar, json.RawMessage(`{}`)); got != tools.ReplayNever {
			t.Fatalf("risk %q claiming replay-safe must be rejected, got %q", risk, got)
		}
	}
	honest := newReplayProbe("Honest", tools.RiskRead, tools.ReplaySafe, true)
	if got := tools.ClassifyReplay(honest, json.RawMessage(`{}`)); got != tools.ReplaySafe {
		t.Fatalf("a read-risk tool claiming replay-safe must be safe, got %q", got)
	}
	declining := newReplayProbe("Declining", tools.RiskRead, tools.ReplayNever, true)
	if got := tools.ClassifyReplay(declining, json.RawMessage(`{}`)); got != tools.ReplayNever {
		t.Fatalf("a tool declining replay must stay never, got %q", got)
	}
}

// TestNormalizeReplayClassRejectsUnknownValues covers the persisted-value path:
// a row written by a different build or edited by hand must not widen replay.
func TestNormalizeReplayClassRejectsUnknownValues(t *testing.T) {
	for _, value := range []string{"", "never", "SAFE", "safe ", "always", "true", "1"} {
		if value == "safe" {
			continue
		}
		if got := tools.NormalizeReplayClass(value); got != tools.ReplayNever {
			t.Fatalf("value %q must normalize to never, got %q", value, got)
		}
	}
	if got := tools.NormalizeReplayClass("safe"); got != tools.ReplaySafe {
		t.Fatalf("exact %q must normalize to safe, got %q", "safe", got)
	}
}

// TestBuiltinReadOnlyToolsOptIntoReplay pins the intended opt-in set. If a tool
// is added to or removed from this list the change should be deliberate.
func TestBuiltinReadOnlyToolsOptIntoReplay(t *testing.T) {
	safe := map[string]tools.Tool{
		"Read": tools.ReadTool{},
		"Glob": tools.GlobTool{},
		"Grep": tools.GrepTool{},
		"LS":   tools.LSTool{},
	}
	for name, tool := range safe {
		if got := tools.ClassifyReplay(tool, json.RawMessage(`{}`)); got != tools.ReplaySafe {
			t.Fatalf("%s must be replay-safe, got %q", name, got)
		}
	}
	// Mutating and executing tools must not have opted in.
	for name, tool := range map[string]tools.Tool{
		"Write": tools.WriteTool{},
		"Edit":  tools.EditTool{},
	} {
		if got := tools.ClassifyReplay(tool, json.RawMessage(`{}`)); got != tools.ReplayNever {
			t.Fatalf("%s must not be replay-safe, got %q", name, got)
		}
	}
}

// TestSummarizeInterruptedGroupCountsOnlyPendingItems verifies recovery
// classification: already-terminal items are settled history and must not be
// counted as stranded work.
func TestSummarizeInterruptedGroupCountsOnlyPendingItems(t *testing.T) {
	group := db.ToolExecutionGroup{
		ID: "group-1",
		Items: []db.ToolExecutionItem{
			{ToolUseID: "done", Status: db.ToolExecutionItemStatusCompleted, ReplayClass: db.ToolExecutionReplaySafe},
			{ToolUseID: "safe-1", Status: db.ToolExecutionItemStatusPending, ReplayClass: db.ToolExecutionReplaySafe},
			{ToolUseID: "safe-2", Status: db.ToolExecutionItemStatusPending, ReplayClass: db.ToolExecutionReplaySafe},
			{ToolUseID: "never-1", Status: db.ToolExecutionItemStatusPending, ReplayClass: db.ToolExecutionReplayNever},
			{ToolUseID: "blank", Status: db.ToolExecutionItemStatusPending, ReplayClass: ""},
		},
	}
	summary := summarizeInterruptedGroup(group)
	if summary.Pending != 4 {
		t.Fatalf("expected 4 pending items, got %d", summary.Pending)
	}
	if summary.Replayable != 2 {
		t.Fatalf("expected 2 replay-safe items, got %d", summary.Replayable)
	}
	// An empty class must count as unreplayable, not be skipped.
	if summary.Unreplayable != 2 {
		t.Fatalf("expected 2 unreplayable items, got %d", summary.Unreplayable)
	}
}

// TestInterruptedAbortReasonDistinguishesCases keeps the durable audit trail
// informative: the previous single opaque reason could not tell a later reader
// whether recoverable work had been discarded.
func TestInterruptedAbortReasonDistinguishesCases(t *testing.T) {
	cases := []struct {
		name    string
		summary interruptedGroupSummary
		expect  string
	}{
		{"nothing pending", interruptedGroupSummary{}, "was incomplete"},
		{"all safe", interruptedGroupSummary{Pending: 2, Replayable: 2}, "all replay-safe"},
		{"none safe", interruptedGroupSummary{Pending: 2, Unreplayable: 2}, "not safe to repeat"},
		{"mixed", interruptedGroupSummary{Pending: 3, Replayable: 1, Unreplayable: 2}, "1 replay-safe, 2 not safe"},
	}
	for _, testCase := range cases {
		reason := interruptedAbortReason(testCase.summary)
		if !strings.Contains(reason, testCase.expect) {
			t.Fatalf("%s: reason %q does not contain %q", testCase.name, reason, testCase.expect)
		}
		// The reason is persisted into a column bounded at 4096 bytes.
		if len(reason) > 4096 {
			t.Fatalf("%s: reason exceeds the durable column bound", testCase.name)
		}
	}
}

// TestReplayClassPersistsThroughLedger is the end-to-end durability check for
// item 1: the class derived at start time must survive a reload, and an
// unrecognised stored value must be narrowed on read.
func TestReplayClassPersistsThroughLedger(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()

	trigger, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "ledger"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, TriggerMessageID: trigger.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	assistant, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "assistant", ContentText: "calling tools", RunID: run.ID})
	if err != nil {
		t.Fatal(err)
	}

	group, err := store.CreateToolExecutionGroup(ctx, db.ToolExecutionGroupCreateInput{
		RunID:              run.ID,
		AssistantMessageID: assistant.ID,
		ExpectedCount:      3,
		Items: []db.ToolExecutionItemInput{
			{ToolUseID: "call-read", ToolName: "Read", ReplayClass: db.ToolExecutionReplaySafe},
			{ToolUseID: "call-write", ToolName: "Write", ReplayClass: db.ToolExecutionReplayNever},
			// An unrecognised value must be stored as never rather than rejected or
			// trusted, so a caller mistake fails closed.
			{ToolUseID: "call-odd", ToolName: "Odd", ReplayClass: "always"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	reloaded, err := store.GetToolExecutionGroup(ctx, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, item := range reloaded.Items {
		got[item.ToolUseID] = item.ReplayClass
	}
	if got["call-read"] != db.ToolExecutionReplaySafe {
		t.Fatalf("expected the read call to persist as safe, got %q", got["call-read"])
	}
	if got["call-write"] != db.ToolExecutionReplayNever {
		t.Fatalf("expected the write call to persist as never, got %q", got["call-write"])
	}
	if got["call-odd"] != db.ToolExecutionReplayNever {
		t.Fatalf("expected an unrecognised class to persist as never, got %q", got["call-odd"])
	}
}

// TestTruncatedToolCallsAreNeverExecuted is the item-2 regression test.
//
// When the provider stops on the output-token limit *and* emits tool calls, the
// arguments may have been finalized by a best-effort salvage parser: they can
// parse and validate while being semantically incomplete. Autoto already refuses
// to execute them, and this test exists so that behaviour cannot regress into
// "arguments parsed, so run it".
func TestTruncatedToolCallsAreNeverExecuted(t *testing.T) {
	executed := make(chan string, 4)
	toolRegistry := tools.NewRegistry()
	toolRegistry.Register(recordingProbeTool{name: "TruncatedWrite", risk: tools.RiskRead, executed: executed})

	provider := &scriptedProvider{turns: [][]providers.Event{
		{
			{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "call-truncated", Name: "TruncatedWrite", Input: json.RawMessage(`{}`)}},
			// Valid JSON arguments, but the turn was cut off by the token limit.
			{Type: "done", Done: true, StopReason: "length"},
		},
	}}

	store, _, run := newPrefetchTestFixture(t, provider, toolRegistry)
	defer store.Close()
	runner := newPrefetchTestRunner(store, provider, toolRegistry)

	state := continuationRunState{run: run, limits: continuationLimits{segmentTurns: 2, maxTotalTurns: 2, maxTokens: 10000}, deadline: time.Now().Add(time.Minute)}
	_, err := runner.runContinuationSegment(context.Background(), state, 0)
	if err == nil {
		t.Fatal("a truncated tool-call turn must not be treated as a usable tool boundary")
	}
	if !strings.Contains(err.Error(), "unsafe tool stop reason") {
		t.Fatalf("expected the unsafe-tool-stop-reason rejection, got %v", err)
	}

	select {
	case name := <-executed:
		t.Fatalf("tool %q executed with possibly-truncated arguments", name)
	default:
	}
}

// recordingProbeTool records execution so a test can assert a tool never ran.
type recordingProbeTool struct {
	name     string
	risk     tools.Risk
	executed chan string
}

func (t recordingProbeTool) Name() string                    { return t.name }
func (t recordingProbeTool) Description() string             { return "records execution" }
func (t recordingProbeTool) Schema() any                     { return struct{}{} }
func (t recordingProbeTool) Risk(json.RawMessage) tools.Risk { return t.risk }
func (t recordingProbeTool) Execute(context.Context, tools.Call, tools.Env) (tools.Result, error) {
	select {
	case t.executed <- t.name:
	default:
	}
	return tools.Result{Output: "executed"}, nil
}
