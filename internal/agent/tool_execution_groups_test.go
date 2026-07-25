package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
)

func TestContinuationPersistsAndSettlesToolExecutionGroupBeforeNextTurn(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	if err := writeTestFile(projectDir, "ledger.txt", "ledger"); err != nil {
		t.Fatal(err)
	}
	store, agent := newAgentTestStore(t, projectDir, "acceptEdits")
	defer store.Close()
	trigger, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "read twice"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, TriggerMessageID: trigger.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AssignMessageRun(ctx, agent.ID, trigger.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{
			{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "ledger-read-1", Name: "Read", Input: json.RawMessage(`{"file_path":"ledger.txt"}`)}},
			{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "ledger-read-2", Name: "Read", Input: json.RawMessage(`{"file_path":"ledger.txt"}`)}},
			{Type: "done", Done: true, StopReason: "tool_use"},
		},
		{{Type: "text", Text: "settled"}, {Type: "done", Done: true, StopReason: "end_turn"}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 3})
	if err := runner.run(ctx, agent.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var assistantToolMessageID string
	for _, message := range messages {
		if message.RunID == run.ID && message.Role == "assistant" && strings.Contains(message.ContentText, "ledger-read-1") {
			assistantToolMessageID = message.ID
			break
		}
	}
	if assistantToolMessageID == "" {
		t.Fatalf("missing assistant tool-call message: %+v", messages)
	}
	group, err := store.GetToolExecutionGroupByAssistantMessage(ctx, run.ID, assistantToolMessageID)
	if err != nil {
		t.Fatal(err)
	}
	if group.Status != db.ToolExecutionGroupStatusSettled || group.ExpectedCount != 2 || len(group.Items) != 2 {
		t.Fatalf("tool execution group was not settled: %+v", group)
	}
	for _, item := range group.Items {
		if item.Status != db.ToolExecutionItemStatusCompleted || item.ResultMessageID == "" || item.TerminalAt == "" || len(item.OutputSummaryJSON) == 0 {
			t.Fatalf("incomplete tool ledger item: %+v", item)
		}
	}
	if provider.requestCount() != 2 || !requestHasToolResult(provider.request(1), "ledger-read-1", false) || !requestHasToolResult(provider.request(1), "ledger-read-2", false) {
		t.Fatalf("next model turn did not start from the settled result set")
	}
}

func TestContinuationSettlesDeniedAndErrorToolItems(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "readOnly")
	defer store.Close()
	trigger, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "exercise terminal failures"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, TriggerMessageID: trigger.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute, PermissionModeCap: "readOnly"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AssignMessageRun(ctx, agent.ID, trigger.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{
			{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "ledger-denied", Name: "Write", Input: json.RawMessage(`{"file_path":"denied.txt","content":"no"}`)}},
			{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "ledger-error", Name: "MissingTool", Input: json.RawMessage(`{}`)}},
			{Type: "done", Done: true, StopReason: "tool_use"},
		},
		{{Type: "text", Text: "handled"}, {Type: "done", Done: true, StopReason: "end_turn"}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 3})
	if err := runner.run(ctx, agent.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	groups, err := store.DB().QueryContext(ctx, `SELECT id FROM tool_execution_groups WHERE run_id = ?`, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var groupID string
	if groups.Next() {
		if err := groups.Scan(&groupID); err != nil {
			groups.Close()
			t.Fatal(err)
		}
	}
	if err := groups.Close(); err != nil {
		t.Fatal(err)
	}
	group, err := store.GetToolExecutionGroup(ctx, groupID)
	if err != nil {
		t.Fatal(err)
	}
	if group.Status != db.ToolExecutionGroupStatusSettled || len(group.Items) != 2 || group.Items[0].Status != db.ToolExecutionItemStatusDenied || group.Items[1].Status != db.ToolExecutionItemStatusError {
		t.Fatalf("denied/error items did not settle as terminal: %+v", group)
	}
	if provider.requestCount() != 2 || !requestHasToolResult(provider.request(1), "ledger-denied", true) || !requestHasToolResult(provider.request(1), "ledger-error", true) {
		t.Fatalf("terminal denied/error results were not returned after settlement")
	}
}

func TestContinuationFailsClosedWhenToolLedgerTerminalWriteFails(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	if err := writeTestFile(projectDir, "ledger.txt", "ledger"); err != nil {
		t.Fatal(err)
	}
	store, agent := newAgentTestStore(t, projectDir, "acceptEdits")
	defer store.Close()
	trigger, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "read"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, TriggerMessageID: trigger.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AssignMessageRun(ctx, agent.ID, trigger.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `CREATE TRIGGER fail_tool_execution_item_terminal BEFORE UPDATE OF status ON tool_execution_group_items WHEN OLD.status = 'pending' AND NEW.status <> 'pending' BEGIN SELECT RAISE(ABORT, 'forced tool ledger failure'); END;`); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "ledger-fail", Name: "Read", Input: json.RawMessage(`{"file_path":"ledger.txt"}`)}}, {Type: "done", Done: true, StopReason: "tool_use"}},
		{{Type: "text", Text: "must not run"}, {Type: "done", Done: true, StopReason: "end_turn"}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 3})
	err = runner.run(ctx, agent.ID, run.ID)
	if err == nil || !strings.Contains(err.Error(), "persist terminal tool execution item") || !strings.Contains(err.Error(), "forced tool ledger failure") {
		t.Fatalf("expected fail-closed ledger error, got %v", err)
	}
	if provider.requestCount() != 1 {
		t.Fatalf("model advanced past failed settlement barrier: requests=%d", provider.requestCount())
	}
	groups, err := store.ListUnsettledToolExecutionGroups(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].RunID != run.ID || groups[0].Status != db.ToolExecutionGroupStatusOpen || groups[0].Items[0].Status != db.ToolExecutionItemStatusPending {
		t.Fatalf("failed ledger write did not leave an explicit unsettled boundary: %+v", groups)
	}
	if err := store.RequireRunToolExecutionGroupsSettled(ctx, run.ID); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("unsettled group did not block run: %v", err)
	}
}

func TestRecoverInterruptedToolExecutionGroupsSettlesCompleteAndAbortsIncomplete(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	incompleteAssistant, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, RunID: run.ID, Role: "assistant", ContentText: "incomplete"})
	if err != nil {
		t.Fatal(err)
	}
	incomplete, err := store.CreateToolExecutionGroup(ctx, db.ToolExecutionGroupCreateInput{RunID: run.ID, AssistantMessageID: incompleteAssistant.ID, ExpectedCount: 1, Items: []db.ToolExecutionItemInput{{ToolUseID: "recover-incomplete", ToolName: "Read"}}})
	if err != nil {
		t.Fatal(err)
	}
	completeAssistant, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, RunID: run.ID, Role: "assistant", ContentText: "complete"})
	if err != nil {
		t.Fatal(err)
	}
	complete, err := store.CreateToolExecutionGroup(ctx, db.ToolExecutionGroupCreateInput{RunID: run.ID, AssistantMessageID: completeAssistant.ID, ExpectedCount: 1, Items: []db.ToolExecutionItemInput{{ToolUseID: "recover-complete", ToolName: "Read"}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, RunID: run.ID, Role: "user", ParentToolID: "recover-complete", ContentText: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordToolExecutionItemTerminal(ctx, complete.ID, db.ToolExecutionItemTerminalInput{ToolUseID: "recover-complete", Status: db.ToolExecutionItemStatusCompleted, ResultMessageID: result.ID, OutputSummaryJSON: json.RawMessage(`{"sha256":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","byteCount":4}`)}); err != nil {
		t.Fatal(err)
	}
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{})
	if err := runner.RecoverInterruptedToolExecutionGroups(ctx); err != nil {
		t.Fatal(err)
	}
	recoveredComplete, err := store.GetToolExecutionGroup(ctx, complete.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredComplete.Status != db.ToolExecutionGroupStatusSettled {
		t.Fatalf("fully terminal group was not settled during recovery: %+v", recoveredComplete)
	}
	recoveredIncomplete, err := store.GetToolExecutionGroup(ctx, incomplete.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredIncomplete.Status != db.ToolExecutionGroupStatusAborted || recoveredIncomplete.Items[0].Status != db.ToolExecutionItemStatusAborted || !strings.Contains(recoveredIncomplete.AbortReason, "process restarted") {
		t.Fatalf("interrupted group was not explicitly aborted: %+v", recoveredIncomplete)
	}
	if err := store.RequireRunToolExecutionGroupsSettled(ctx, run.ID); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("aborted recovered group must prevent silent resume: %v", err)
	}
}
