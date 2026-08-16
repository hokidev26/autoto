package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

func TestQueuedMessageJoinsSameRunAfterToolBatch(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	if err := writeTestFile(projectDir, "note.txt", "hello"); err != nil {
		t.Fatal(err)
	}
	store, agent := newAgentTestStore(t, projectDir, "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "read it"}); err != nil {
		t.Fatal(err)
	}
	const steered = "also check the tests"
	provider := &scriptedProvider{
		turns: [][]providers.Event{
			{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "read-1", Name: "Read", Input: json.RawMessage(`{"file_path":"note.txt"}`)}}, {Type: "done", Done: true, StopReason: "tool_use"}},
			{{Type: "text", Text: "done with steering"}, {Type: "done", Done: true, StopReason: "end_turn"}},
		},
		onGenerate: func(idx int) {
			if idx != 0 {
				return
			}
			if _, err := store.EnqueueMessage(ctx, db.QueuedMessage{AgentID: agent.ID, Text: steered}); err != nil {
				t.Errorf("enqueue steered message: %v", err)
			}
		},
	}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 4})
	runner.Run(ctx, agent.ID)
	if provider.requestCount() < 2 {
		t.Fatalf("expected a second generate after the tool batch, got %d", provider.requestCount())
	}
	if !providerRequestHasUserText(provider.request(1), steered) {
		t.Fatalf("steered message missing from the same-run generate: %+v", provider.request(1).Messages)
	}
	if providerRequestHasUserText(provider.request(0), steered) {
		t.Fatal("steered message was injected before the tool batch finished")
	}
	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	runID := ""
	steeredRunID := ""
	for _, message := range messages {
		if message.Role == "assistant" && message.RunID != "" {
			runID = message.RunID
		}
		if message.Role == "user" && strings.Contains(message.ContentText, steered) {
			steeredRunID = message.RunID
		}
	}
	if runID == "" || steeredRunID != runID {
		t.Fatalf("steered message runId=%q, assistant runId=%q", steeredRunID, runID)
	}
	remaining, err := store.ListQueuedMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("queue still holds %d messages after same-run steering", len(remaining))
	}
}

func TestQueuedMessageIsNotInjectedDuringPendingApproval(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	disableReflectionForTest(t, store)
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "run bash"}); err != nil {
		t.Fatal(err)
	}
	const steered = "do not inject while waiting for approval"
	provider := &scriptedProvider{
		turns: [][]providers.Event{{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "bash-wait", Name: "Bash", Input: json.RawMessage(`{"command":"printf wait"}`)}}, {Type: "done", Done: true, StopReason: "tool_use"}}},
		onGenerate: func(idx int) {
			if idx != 0 {
				return
			}
			if _, err := store.EnqueueMessage(ctx, db.QueuedMessage{AgentID: agent.ID, Text: steered}); err != nil {
				t.Errorf("enqueue steered message: %v", err)
			}
		},
	}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 2})
	done := make(chan struct{})
	go func() { runner.Run(ctx, agent.ID); close(done) }()
	waitForPendingApproval(t, runner, agent.ID, "bash-wait")
	queued, err := store.ListQueuedMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0].Text != steered {
		t.Fatalf("queued follow-up must stay parked during approval, got %+v", queued)
	}
	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if strings.Contains(message.ContentText, steered) {
			t.Fatal("queued follow-up was injected while a tool approval was pending")
		}
	}
	interrupted, err := runner.Interrupt(ctx, agent.ID)
	if err != nil || !interrupted {
		t.Fatalf("interrupt failed interrupted=%v err=%v", interrupted, err)
	}
	waitDone(t, done)
	if runnerPendingApprovalCount(runner) != 0 {
		t.Fatal("expected pending approval cleanup")
	}
	remaining, err := store.ListQueuedMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].Text != steered {
		t.Fatalf("interrupt must not consume the queue as steering, got %+v", remaining)
	}
	finalMessages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range finalMessages {
		if strings.Contains(message.ContentText, steered) {
			t.Fatal("interrupt treated the queue as same-run steering")
		}
	}
}

func TestQueuedMessageIsNotInjectedAfterPermissionGenerationChange(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	if err := writeTestFile(projectDir, "note.txt", "hello"); err != nil {
		t.Fatal(err)
	}
	store, agent := newAgentTestStore(t, projectDir, "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "read it"}); err != nil {
		t.Fatal(err)
	}
	const steered = "stale generation must not steer"
	provider := &scriptedProvider{
		turns: [][]providers.Event{
			{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "read-gen", Name: "Read", Input: json.RawMessage(`{"file_path":"note.txt"}`)}}, {Type: "done", Done: true, StopReason: "tool_use"}},
			{{Type: "text", Text: "done"}, {Type: "done", Done: true, StopReason: "end_turn"}},
		},
		onGenerate: func(idx int) {
			if idx != 0 {
				return
			}
			if _, err := store.EnqueueMessage(ctx, db.QueuedMessage{AgentID: agent.ID, Text: steered}); err != nil {
				t.Errorf("enqueue steered message: %v", err)
			}
			if _, err := store.DB().ExecContext(ctx, `UPDATE agents SET entity_generation = entity_generation + 1, permission_generation = permission_generation + 1 WHERE id = ?`, agent.ID); err != nil {
				t.Errorf("bump generations: %v", err)
			}
		},
	}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 4})
	runner.Run(ctx, agent.ID)
	if provider.requestCount() >= 2 && providerRequestHasUserText(provider.request(1), steered) {
		t.Fatal("queued follow-up was injected after a generation change")
	}
	remaining, err := store.ListQueuedMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].Text != steered {
		t.Fatalf("generation mismatch must leave the queue untouched, got %+v", remaining)
	}
}

func TestQueuedMessageWithMismatchedModeStaysParked(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	if err := writeTestFile(projectDir, "note.txt", "hello"); err != nil {
		t.Fatal(err)
	}
	store, agent := newAgentTestStore(t, projectDir, "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "read it"}); err != nil {
		t.Fatal(err)
	}
	const steered = "switch to plan mode and propose the refactor"
	provider := &scriptedProvider{
		turns: [][]providers.Event{
			{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "read-mode", Name: "Read", Input: json.RawMessage(`{"file_path":"note.txt"}`)}}, {Type: "done", Done: true, StopReason: "tool_use"}},
			{{Type: "text", Text: "done without steering"}, {Type: "done", Done: true, StopReason: "end_turn"}},
		},
		onGenerate: func(idx int) {
			if idx != 0 {
				return
			}
			if _, err := store.EnqueueMessage(ctx, db.QueuedMessage{AgentID: agent.ID, Text: steered, RunMode: db.RunExecutionModePlan}); err != nil {
				t.Errorf("enqueue steered message: %v", err)
			}
		},
	}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 4})
	runner.Run(ctx, agent.ID)
	if provider.requestCount() < 2 {
		t.Fatalf("expected a second generate after the tool batch, got %d", provider.requestCount())
	}
	if providerRequestHasUserText(provider.request(1), steered) {
		t.Fatal("plan-mode follow-up was injected into an execute run")
	}
	remaining, err := store.ListQueuedMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].Text != steered {
		t.Fatalf("mismatched mode must leave the queue parked, got %+v", remaining)
	}
}

func TestQueuedMessageJoinsSameRunAfterBackgroundWake(t *testing.T) {
	ctx := context.Background()
	store, createdAgent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	const steered = "also fix the comment while the child is running"
	provider := &scriptedProvider{turns: [][]providers.Event{{{Type: "text", Text: "reviewed background result with steering"}, {Type: "done", Done: true, StopReason: "end_turn"}}}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{AutoContinuationMode: "safe", ContinuationSegmentTurns: 2, MaxContinuations: 2, MaxTotalTurns: 4, MaxRunDurationMs: 60000, MaxRunTokens: 10000})
	runner.SetPlanSnapshotProvider(func(ctx context.Context, agentID string) (db.PlanSnapshot, error) {
		generations, err := store.GetPermissionGenerations(ctx, agentID)
		return db.PlanSnapshot{PolicyGenerationSnapshot: generations.Policy, AgentGenerationSnapshot: generations.Entity, ToolCatalogDigest: "tools", WorkspaceFingerprint: "workspace"}, err
	})
	trigger, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, Role: "user", ContentText: "start background work"})
	if err != nil {
		t.Fatal(err)
	}
	request, err := runner.prepareContinuationRun(ctx, db.Run{AgentID: createdAgent.ID, TriggerMessageID: trigger.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AssignMessageRun(ctx, createdAgent.ID, trigger.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	boundary, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, RunID: run.ID, Role: "user", ParentToolID: "tool-1", ContentText: "background task task-steer queued"})
	if err != nil {
		t.Fatal(err)
	}
	createdTask, err := store.CreateBackgroundTask(ctx, db.BackgroundTask{
		ID:                           "task-steer",
		OwnerAgentID:                 createdAgent.ID,
		ParentRunID:                  run.ID,
		ParentToolUseID:              "tool-1",
		Kind:                         db.BackgroundTaskKindShell,
		ResumeParent:                 true,
		PermissionModeCap:            run.PermissionModeCap,
		PermissionGenerationSnapshot: 1,
		PolicyGenerationSnapshot:     run.PolicyGenerationSnapshot,
		AgentGenerationSnapshot:      run.AgentGenerationSnapshot,
		ToolCatalogDigest:            run.ToolCatalogDigest,
		WorkspaceFingerprint:         run.WorkspaceFingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkRunContinuationPending(ctx, run.ID, db.RunContinuationPendingInput{
		ExpectedContinuationCount: 0,
		TurnCount:                 1,
		ResumeAfterMessageID:      boundary.ID,
		LastStopReason:            "tool_use",
		ContinuationReason:        continuationReasonBackgroundTask,
		WaitingBackgroundTaskID:   createdTask.ID,
	}); err != nil {
		t.Fatal(err)
	}
	runner.SetBackgroundTaskService(&staticBackgroundTaskService{task: tools.BackgroundTask{
		ID: createdTask.ID, OwnerAgentID: createdAgent.ID, ParentRunID: run.ID, ParentToolUseID: "tool-1",
		Kind: tools.BackgroundTaskKindShell, Status: "succeeded", ResumeParent: true,
	}})
	if _, err := store.EnqueueMessage(ctx, db.QueuedMessage{AgentID: createdAgent.ID, Text: steered}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.WakeBackgroundContinuation(ctx, run.ID, createdTask.ID); err != nil {
		t.Fatal(err)
	}
	var updated db.Run
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		updated, err = store.GetRunByID(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if updated.Status == "completed" && provider.requestCount() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if updated.Status != "completed" || provider.requestCount() != 1 {
		t.Fatalf("wake did not complete: updated=%+v requests=%d", updated, provider.requestCount())
	}
	if !providerRequestHasUserText(provider.request(0), steered) {
		t.Fatalf("steered message missing from the wake generate: %+v", provider.request(0).Messages)
	}
	messages, err := store.ListMessages(ctx, createdAgent.ID)
	if err != nil {
		t.Fatal(err)
	}
	steeredRunID := ""
	for _, message := range messages {
		if message.Role == "user" && strings.Contains(message.ContentText, steered) {
			steeredRunID = message.RunID
		}
	}
	if steeredRunID != run.ID {
		t.Fatalf("steered message runId=%q, parked runId=%q", steeredRunID, run.ID)
	}
	remaining, err := store.ListQueuedMessages(ctx, createdAgent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("queue still holds %d messages after wake steering", len(remaining))
	}
}

func providerRequestHasUserText(req providers.GenerateRequest, text string) bool {
	for _, message := range req.Messages {
		if message.Role != "user" {
			continue
		}
		if strings.Contains(message.Content, text) {
			return true
		}
		for _, block := range message.Blocks {
			if block.Type == "text" && strings.Contains(block.Text, text) {
				return true
			}
		}
	}
	return false
}
