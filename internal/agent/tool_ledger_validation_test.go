package agent

import (
	"encoding/json"
	"errors"
	"testing"

	"autoto/internal/db"
	"autoto/internal/providers"
)

// ledgerFixture builds a consistent assistant message plus matching group, which
// each test then perturbs in exactly one way.
func ledgerFixture(t *testing.T) (db.ToolExecutionGroup, db.Message) {
	t.Helper()
	blocks := []providers.ContentBlock{
		{Type: "tool_use", ToolUseID: "call-a", ToolName: "Read", Input: json.RawMessage(`{"file_path":"a.go"}`)},
		{Type: "tool_use", ToolUseID: "call-b", ToolName: "Write", Input: json.RawMessage(`{"file_path":"b.go","content":"x"}`)},
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	assistant := db.Message{ID: "msg-1", Role: "assistant", RunID: "run-1", ContentJSON: raw}
	group := db.ToolExecutionGroup{
		ID:                 "group-1",
		RunID:              "run-1",
		AssistantMessageID: "msg-1",
		ExpectedCount:      2,
		Items: []db.ToolExecutionItem{
			{ToolUseID: "call-a", ToolName: "Read", Ordinal: 0, Status: db.ToolExecutionItemStatusPending, ReplayClass: db.ToolExecutionReplaySafe},
			{ToolUseID: "call-b", ToolName: "Write", Ordinal: 1, Status: db.ToolExecutionItemStatusPending, ReplayClass: db.ToolExecutionReplayNever},
		},
	}
	return group, assistant
}

func TestValidateToolExecutionLedgerAcceptsAConsistentPair(t *testing.T) {
	group, assistant := ledgerFixture(t)
	if err := ValidateToolExecutionLedger(group, assistant); err != nil {
		t.Fatalf("a consistent ledger must validate, got %v", err)
	}
}

// assertCorruption checks that validation refused with the expected named reason.
func assertCorruption(t *testing.T, err error, want LedgerCorruptionReason) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected corruption %s, got no error", want)
	}
	var corruption *LedgerCorruption
	if !errors.As(err, &corruption) {
		t.Fatalf("expected a *LedgerCorruption, got %T: %v", err, err)
	}
	if corruption.Reason != want {
		t.Fatalf("expected reason %s, got %s (%s)", want, corruption.Reason, corruption.Message)
	}
}

func TestValidateToolExecutionLedgerNamesEveryInconsistency(t *testing.T) {
	t.Run("count mismatch", func(t *testing.T) {
		group, assistant := ledgerFixture(t)
		group.ExpectedCount = 3
		assertCorruption(t, ValidateToolExecutionLedger(group, assistant), LedgerItemCountMismatch)
	})

	t.Run("ordinal out of range", func(t *testing.T) {
		group, assistant := ledgerFixture(t)
		group.Items[1].Ordinal = 7
		assertCorruption(t, ValidateToolExecutionLedger(group, assistant), LedgerOrdinalOutOfRange)
	})

	t.Run("duplicate ordinal", func(t *testing.T) {
		group, assistant := ledgerFixture(t)
		group.Items[1].Ordinal = 0
		assertCorruption(t, ValidateToolExecutionLedger(group, assistant), LedgerDuplicateOrdinal)
	})

	// The two checks below are the ones that make argument recovery safe: they
	// catch a ledger row that points at a different call than it claims.
	t.Run("tool use id mismatch", func(t *testing.T) {
		group, assistant := ledgerFixture(t)
		group.Items[0].ToolUseID = "call-elsewhere"
		assertCorruption(t, ValidateToolExecutionLedger(group, assistant), LedgerToolCallMismatch)
	})

	t.Run("tool name mismatch", func(t *testing.T) {
		group, assistant := ledgerFixture(t)
		group.Items[0].ToolName = "Bash"
		assertCorruption(t, ValidateToolExecutionLedger(group, assistant), LedgerToolCallMismatch)
	})

	t.Run("missing assistant call", func(t *testing.T) {
		group, assistant := ledgerFixture(t)
		assistant.ContentJSON = json.RawMessage(`[{"type":"text","text":"no tool calls here"}]`)
		assertCorruption(t, ValidateToolExecutionLedger(group, assistant), LedgerMissingAssistantCall)
	})
}

// TestValidateToolExecutionLedgerCatchesSwappedOrdinals is the case a naive
// id-only check would miss: both ids exist and both names are right, but the
// positions are exchanged, so recovering arguments by ordinal would return the
// wrong call's input.
func TestValidateToolExecutionLedgerCatchesSwappedOrdinals(t *testing.T) {
	group, assistant := ledgerFixture(t)
	group.Items[0].Ordinal = 1
	group.Items[1].Ordinal = 0
	assertCorruption(t, ValidateToolExecutionLedger(group, assistant), LedgerToolCallMismatch)
}

// TestRecoverToolCallInputReturnsTheMatchingArguments is the payoff: the
// arguments come back from the assistant message, so no second copy has to be
// stored and the two records cannot drift.
func TestRecoverToolCallInputReturnsTheMatchingArguments(t *testing.T) {
	group, assistant := ledgerFixture(t)

	input, err := RecoverToolCallInput(group, assistant, "call-a")
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(input, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["file_path"] != "a.go" {
		t.Fatalf("recovered the wrong call's arguments: %s", input)
	}

	second, err := RecoverToolCallInput(group, assistant, "call-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["file_path"] != "b.go" {
		t.Fatalf("recovered the wrong call's arguments: %s", second)
	}
}

// TestRecoverToolCallInputRefusesACorruptPair is the fail-closed guarantee. A
// caller must never receive arguments from a ledger that disagrees with its
// message, because it would then run a tool with another call's input.
func TestRecoverToolCallInputRefusesACorruptPair(t *testing.T) {
	group, assistant := ledgerFixture(t)
	group.Items[0].ToolName = "Bash"

	if _, err := RecoverToolCallInput(group, assistant, "call-a"); err == nil {
		t.Fatal("expected argument recovery to refuse a corrupt ledger")
	} else {
		assertCorruption(t, err, LedgerToolCallMismatch)
	}
}

func TestRecoverToolCallInputRejectsAnUnknownToolUseID(t *testing.T) {
	group, assistant := ledgerFixture(t)
	if _, err := RecoverToolCallInput(group, assistant, "call-missing"); err == nil {
		t.Fatal("expected an unknown tool use id to be refused")
	}
}

// TestGetToolExecutionGroupAssistantMessageRoundTripsToolCalls proves the narrow
// store getter returns enough for validation to work against a real row.
func TestGetToolExecutionGroupAssistantMessageRoundTripsToolCalls(t *testing.T) {
	ctx := t.Context()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()

	trigger, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "go"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, TriggerMessageID: trigger.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	blocks := []providers.ContentBlock{
		{Type: "tool_use", ToolUseID: "stored-call", ToolName: "Read", Input: json.RawMessage(`{"file_path":"stored.go"}`)},
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	assistant, err := store.AddMessage(ctx, db.Message{
		AgentID: agent.ID, Role: "assistant", RunID: run.ID,
		ContentText: "reading", ContentJSON: raw,
	})
	if err != nil {
		t.Fatal(err)
	}

	group, err := store.CreateToolExecutionGroup(ctx, db.ToolExecutionGroupCreateInput{
		RunID:              run.ID,
		AssistantMessageID: assistant.ID,
		ExpectedCount:      1,
		Items: []db.ToolExecutionItemInput{
			{ToolUseID: "stored-call", ToolName: "Read", ReplayClass: db.ToolExecutionReplaySafe},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := store.GetToolExecutionGroupAssistantMessage(ctx, assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateToolExecutionLedger(group, loaded); err != nil {
		t.Fatalf("a real persisted pair must validate, got %v", err)
	}
	input, err := RecoverToolCallInput(group, loaded, "stored-call")
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(input, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["file_path"] != "stored.go" {
		t.Fatalf("arguments did not survive the round trip: %s", input)
	}
}
