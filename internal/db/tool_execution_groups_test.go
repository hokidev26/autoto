package db

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolExecutionGroupLedgerIsIdempotentAndSettlesWithCAS(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "tool-groups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB().ExecContext(ctx, ToolExecutionGroupSchemaSQL()); err != nil {
		t.Fatal(err)
	}
	_, _, agent, err := store.CreateProject(ctx, "Tool groups", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	trigger, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "user", ContentText: "run tools"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, Run{AgentID: agent.ID, TriggerMessageID: trigger.ID, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AssignMessageRun(ctx, agent.ID, trigger.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	assistant, err := store.AddMessage(ctx, Message{AgentID: agent.ID, RunID: run.ID, Role: "assistant", ContentText: "tools"})
	if err != nil {
		t.Fatal(err)
	}
	input := ToolExecutionGroupCreateInput{
		RunID:              run.ID,
		AssistantMessageID: assistant.ID,
		ExpectedCount:      2,
		Items: []ToolExecutionItemInput{
			{ToolUseID: "tool-1", ToolName: "Read"},
			{ToolUseID: "tool-2", ToolName: "Write"},
		},
	}
	group, err := store.CreateToolExecutionGroup(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.CreateToolExecutionGroup(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.ID != group.ID || duplicate.Status != ToolExecutionGroupStatusOpen || len(duplicate.Items) != 2 {
		t.Fatalf("idempotent create changed group: first=%+v duplicate=%+v", group, duplicate)
	}
	mismatch := input
	mismatch.Items = append([]ToolExecutionItemInput(nil), input.Items...)
	mismatch.Items[1].ToolName = "Bash"
	if _, err := store.CreateToolExecutionGroup(ctx, mismatch); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched duplicate must conflict, got %v", err)
	}

	result1, err := store.AddMessage(ctx, Message{AgentID: agent.ID, RunID: run.ID, Role: "user", ParentToolID: "tool-1", ContentText: "first"})
	if err != nil {
		t.Fatal(err)
	}
	summary1, err := json.Marshal(ToolExecutionOutputSummary{SHA256: strings.Repeat("a", 64), ByteCount: 5, Preview: "first"})
	if err != nil {
		t.Fatal(err)
	}
	item1, err := store.RecordToolExecutionItemTerminal(ctx, group.ID, ToolExecutionItemTerminalInput{ToolUseID: "tool-1", Status: ToolExecutionItemStatusCompleted, ResultMessageID: result1.ID, OutputSummaryJSON: summary1})
	if err != nil {
		t.Fatal(err)
	}
	if item1.Status != ToolExecutionItemStatusCompleted || item1.TerminalAt == "" {
		t.Fatalf("first item was not terminal: %+v", item1)
	}
	if _, err := store.RecordToolExecutionItemTerminal(ctx, group.ID, ToolExecutionItemTerminalInput{ToolUseID: "tool-1", Status: ToolExecutionItemStatusCompleted, ResultMessageID: result1.ID, OutputSummaryJSON: summary1}); err != nil {
		t.Fatalf("exact terminal retry must be idempotent: %v", err)
	}
	if _, err := store.RecordToolExecutionItemTerminal(ctx, group.ID, ToolExecutionItemTerminalInput{ToolUseID: "tool-1", Status: ToolExecutionItemStatusError, ResultMessageID: result1.ID, OutputSummaryJSON: summary1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal status rewrite must conflict, got %v", err)
	}
	if _, err := store.SettleToolExecutionGroup(ctx, group.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("incomplete group must not settle, got %v", err)
	}

	result2, err := store.AddMessage(ctx, Message{AgentID: agent.ID, RunID: run.ID, Role: "user", ParentToolID: "tool-2", ContentText: "denied"})
	if err != nil {
		t.Fatal(err)
	}
	summary2, err := json.Marshal(ToolExecutionOutputSummary{SHA256: strings.Repeat("b", 64), ByteCount: 6, Preview: "denied", IsError: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordToolExecutionItemTerminal(ctx, group.ID, ToolExecutionItemTerminalInput{ToolUseID: "tool-2", Status: ToolExecutionItemStatusDenied, ResultMessageID: result2.ID, OutputSummaryJSON: summary2}); err != nil {
		t.Fatal(err)
	}
	settled, err := store.SettleToolExecutionGroup(ctx, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != ToolExecutionGroupStatusSettled || settled.SettledAt == "" || len(settled.Items) != settled.ExpectedCount {
		t.Fatalf("group did not settle completely: %+v", settled)
	}
	settledAgain, err := store.SettleToolExecutionGroup(ctx, group.ID)
	if err != nil || settledAgain.SettledAt != settled.SettledAt {
		t.Fatalf("settlement retry changed state: %+v err=%v", settledAgain, err)
	}
	if err := store.RequireRunToolExecutionGroupsSettled(ctx, run.ID); err != nil {
		t.Fatalf("settled run rejected by barrier: %v", err)
	}
}

func TestToolExecutionGroupRejectsWrongGroupAndAbortsIncompleteLedger(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "tool-groups-abort.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB().ExecContext(ctx, ToolExecutionGroupSchemaSQL()); err != nil {
		t.Fatal(err)
	}
	_, _, agent, err := store.CreateProject(ctx, "Tool group abort", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, Run{AgentID: agent.ID, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	assistantA, err := store.AddMessage(ctx, Message{AgentID: agent.ID, RunID: run.ID, Role: "assistant", ContentText: "A"})
	if err != nil {
		t.Fatal(err)
	}
	assistantB, err := store.AddMessage(ctx, Message{AgentID: agent.ID, RunID: run.ID, Role: "assistant", ContentText: "B"})
	if err != nil {
		t.Fatal(err)
	}
	groupA, err := store.CreateToolExecutionGroup(ctx, ToolExecutionGroupCreateInput{RunID: run.ID, AssistantMessageID: assistantA.ID, ExpectedCount: 1, Items: []ToolExecutionItemInput{{ToolUseID: "tool-a", ToolName: "Read"}}})
	if err != nil {
		t.Fatal(err)
	}
	groupB, err := store.CreateToolExecutionGroup(ctx, ToolExecutionGroupCreateInput{RunID: run.ID, AssistantMessageID: assistantB.ID, ExpectedCount: 1, Items: []ToolExecutionItemInput{{ToolUseID: "tool-b", ToolName: "Read"}}})
	if err != nil {
		t.Fatal(err)
	}
	resultA, err := store.AddMessage(ctx, Message{AgentID: agent.ID, RunID: run.ID, Role: "user", ParentToolID: "tool-a", ContentText: "A result"})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := json.Marshal(ToolExecutionOutputSummary{SHA256: strings.Repeat("a", 64), ByteCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordToolExecutionItemTerminal(ctx, groupB.ID, ToolExecutionItemTerminalInput{ToolUseID: "tool-a", Status: ToolExecutionItemStatusCompleted, ResultMessageID: resultA.ID, OutputSummaryJSON: summary}); !IsNotFound(err) {
		t.Fatalf("wrong group must not accept a foreign item, got %v", err)
	}
	unchanged, err := store.GetToolExecutionGroup(ctx, groupA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Items[0].Status != ToolExecutionItemStatusPending {
		t.Fatalf("wrong-group attempt mutated item: %+v", unchanged.Items[0])
	}
	aborted, err := store.AbortToolExecutionGroup(ctx, groupA.ID, "interrupted")
	if err != nil {
		t.Fatal(err)
	}
	if aborted.Status != ToolExecutionGroupStatusAborted || aborted.AbortReason != "interrupted" || aborted.Items[0].Status != ToolExecutionItemStatusAborted {
		t.Fatalf("incomplete group was not durably aborted: %+v", aborted)
	}
	abortedAgain, err := store.AbortToolExecutionGroup(ctx, groupA.ID, "different reason")
	if err != nil {
		t.Fatal(err)
	}
	if abortedAgain.AbortReason != "interrupted" {
		t.Fatalf("abort retry mutated terminal group: %+v", abortedAgain)
	}
	if err := store.RequireRunToolExecutionGroupsSettled(ctx, run.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("aborted/open groups must block the run barrier, got %v", err)
	}
}

func TestMigrationV50AddsToolExecutionSettlementLedger(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-v49.db")
	raw := openRawDB(t, path)
	if _, err := raw.ExecContext(ctx, schemaSQL); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `DROP TABLE tool_execution_group_items; DROP TABLE tool_execution_groups; PRAGMA user_version = 49;`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, table := range []string{"tool_execution_groups", "tool_execution_group_items"} {
		if !testTableExists(t, ctx, store.DB(), table) {
			t.Fatalf("expected v50 migration to create %s", table)
		}
	}
	if version := readUserVersion(t, ctx, store.DB()); version != CurrentDBVersion {
		t.Fatalf("expected version %d, got %d", CurrentDBVersion, version)
	}
}
