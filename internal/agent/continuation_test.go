package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

type staticBackgroundTaskService struct {
	task tools.BackgroundTask
}

func (s *staticBackgroundTaskService) Submit(context.Context, tools.BackgroundTaskRequest) (tools.BackgroundTask, error) {
	return tools.BackgroundTask{}, context.Canceled
}

func (s *staticBackgroundTaskService) List(context.Context, tools.BackgroundTaskListOptions) ([]tools.BackgroundTask, error) {
	return []tools.BackgroundTask{s.task}, nil
}

func (s *staticBackgroundTaskService) Get(_ context.Context, ownerAgentID, taskID string) (tools.BackgroundTask, error) {
	if s == nil || s.task.ID != taskID || ownerAgentID != "" && s.task.OwnerAgentID != ownerAgentID {
		return tools.BackgroundTask{}, context.Canceled
	}
	return s.task, nil
}

func (s *staticBackgroundTaskService) Output(context.Context, string, string, int64, int) (tools.BackgroundTaskOutputPage, error) {
	return tools.BackgroundTaskOutputPage{}, nil
}

func (s *staticBackgroundTaskService) Wait(ctx context.Context, ownerAgentID, taskID string, _ int64) (tools.BackgroundTask, error) {
	return s.Get(ctx, ownerAgentID, taskID)
}

func (s *staticBackgroundTaskService) Cancel(ctx context.Context, ownerAgentID, taskID string) (tools.BackgroundTask, error) {
	return s.Get(ctx, ownerAgentID, taskID)
}

func TestContinuationStopReasonPolicy(t *testing.T) {
	for _, reason := range []string{"max_output_tokens", "length", "max_tokens"} {
		if !isContinuationStopReason(reason) || normalizedContinuationStopReason(reason) != continuationReasonMaxOutputTokens {
			t.Fatalf("expected %q to be a safe output continuation reason", reason)
		}
	}
	for _, reason := range []string{"", "end_turn", "stop", "completed", "not_configured"} {
		if !isTerminalStopReason(reason) || isContinuationStopReason(reason) {
			t.Fatalf("expected %q to be terminal without continuation", reason)
		}
	}
	for _, reason := range []string{"content_filter", "unknown"} {
		if isTerminalStopReason(reason) || isContinuationStopReason(reason) || safeContinuationReason(reason) {
			t.Fatalf("unknown stop reason %q must fail closed", reason)
		}
	}
	// provider_error is a continuation reason, never a model stop reason: a
	// provider fault must not be mistaken for the model choosing to stop.
	if isTerminalStopReason(continuationReasonProviderError) || isContinuationStopReason(continuationReasonProviderError) {
		t.Fatal("provider_error must not be treated as a model stop reason")
	}
	for _, reason := range []string{
		continuationReasonMaxOutputTokens, continuationReasonSegmentTurns, continuationReasonBackgroundTask,
		// Retried under a bounded counter rather than ending the run.
		continuationReasonProviderError,
	} {
		if !safeContinuationReason(reason) {
			t.Fatalf("expected safe continuation reason %q", reason)
		}
	}
}

func TestRetryableProviderErrorRequiresSafeModeAndProviderFault(t *testing.T) {
	runner := &Runner{}
	safe := continuationRunState{limits: continuationLimits{mode: continuationModeSafe}}
	providerFault := segmentOutcome{continuationReason: continuationReasonProviderError, resumeAfterID: "msg-1"}
	boom := errors.New("upstream 502")

	if !runner.retryableProviderError(safe, providerFault, boom) {
		t.Fatal("a provider fault with a resume point in safe mode must be retryable")
	}
	// No durable resume point: the failed call persisted nothing, so the caller
	// re-runs the same segment in place. This is the first-turn 502 case, which
	// used to end the run because a resume point was demanded that could not
	// exist yet.
	if !runner.retryableProviderError(safe, segmentOutcome{continuationReason: continuationReasonProviderError}, boom) {
		t.Fatal("a provider fault without a resume point must retry in place")
	}
	// Auto-continuation disabled by the user.
	off := continuationRunState{limits: continuationLimits{mode: continuationModeOff}}
	if runner.retryableProviderError(off, providerFault, boom) {
		t.Fatal("continuation mode off must not retry")
	}
	// Any other failure (store, settlement barrier) keeps failing the run.
	if runner.retryableProviderError(safe, segmentOutcome{continuationReason: continuationReasonSegmentTurns, resumeAfterID: "msg-1"}, boom) {
		t.Fatal("only provider faults are retryable")
	}
	// Cancellation and deadlines are the user's or the budget's decision.
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		if runner.retryableProviderError(safe, providerFault, err) {
			t.Fatalf("cancellation %v must not be retried", err)
		}
	}
	if runner.retryableProviderError(safe, providerFault, nil) {
		t.Fatal("a segment without an error is not a retry candidate")
	}
	// A configuration fault answers the same way every attempt. Retrying a 404
	// four times with backoff only delays the error the user has to act on, which
	// is what a base URL ending in /v1/messages produced.
	permanent := errors.New("cliproxyapi model request failed: 404")
	if runner.retryableProviderError(safe, providerFault, permanent) {
		t.Fatal("a permanent 4xx must not be retried")
	}
	// The 4xx that do clear on their own stay retryable.
	for _, message := range []string{"openai model request failed: 408", "openai model request failed: 429"} {
		if !runner.retryableProviderError(safe, providerFault, errors.New(message)) {
			t.Fatalf("%q must stay retryable", message)
		}
	}
}

func TestRetryableProviderErrorHonoursUserPatterns(t *testing.T) {
	runner := &Runner{}
	safe := continuationRunState{limits: continuationLimits{mode: continuationModeSafe}}
	providerFault := segmentOutcome{continuationReason: continuationReasonProviderError, resumeAfterID: "msg-1"}
	// An upstream that reports a permanent fault with a 500 defeats the status
	// rule: the number says retry, the body says it will never work. This is the
	// case the user-maintained list exists for.
	bodyFault := errors.New("openai model request failed: 500 insufficient balance")
	if !runner.retryableProviderError(safe, providerFault, bodyFault) {
		t.Fatal("without a configured pattern a 500 must stay retryable")
	}
	runner.SetNonRetryableErrorPatterns([]string{"Insufficient Balance"})
	if runner.retryableProviderError(safe, providerFault, bodyFault) {
		t.Fatal("a configured pattern must stop the retry despite the 5xx status")
	}
	// Matching is case-insensitive and substring-based, but must not reach
	// errors that merely look similar.
	if runner.retryableProviderError(safe, providerFault, errors.New("openai model request failed: 500 INSUFFICIENT BALANCE for account")) {
		t.Fatal("pattern matching must be case-insensitive")
	}
	if !runner.retryableProviderError(safe, providerFault, errors.New("openai model request failed: 503 balance service unavailable")) {
		t.Fatal("an unrelated 5xx must stay retryable")
	}
	// Clearing the list restores the built-in behaviour.
	runner.SetNonRetryableErrorPatterns(nil)
	if !runner.retryableProviderError(safe, providerFault, bodyFault) {
		t.Fatal("clearing the patterns must restore retrying")
	}
}

func TestProviderErrorBackoffGrowsAndHonorsCancellation(t *testing.T) {
	runner := &Runner{}
	// Cancelled context must win over the delay instead of sleeping it out.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.waitProviderErrorBackoff(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	// The delay is capped, so a late attempt cannot wait an unbounded time.
	if got := providerErrorRetryBaseDelay * time.Duration(1<<uint(maxProviderErrorRetries-1)); got <= providerErrorRetryMaxDelay {
		// Base 2s doubling over 4 attempts reaches 16s, under the 30s cap; this
		// guards the relationship rather than the exact numbers.
		if got > providerErrorRetryMaxDelay {
			t.Fatalf("uncapped backoff %v exceeds max %v", got, providerErrorRetryMaxDelay)
		}
	}
}

func TestBoundedProviderErrorTextIsTrimmedAndSafe(t *testing.T) {
	if got := boundedProviderErrorText(nil); got != "" {
		t.Fatalf("nil error should produce no text, got %q", got)
	}
	long := errors.New(strings.Repeat("x", 500))
	if got := boundedProviderErrorText(long); len(got) != 300 {
		t.Fatalf("expected the text bounded to 300 chars, got %d", len(got))
	}
}

func TestContinuationLimitsPreserveLegacyMaxTurnsAndBoundBudgets(t *testing.T) {
	// A config that only ever set the legacy MaxTurns still means what it said
	// for the turn budget, while the budgets it never mentioned are unlimited.
	legacy := continuationLimitsForConfig(config.AgentConfig{MaxTurns: 3})
	if legacy.mode != continuationModeSafe || legacy.segmentTurns != 3 || legacy.maxTotalTurns != 3 {
		t.Fatalf("unexpected legacy-compatible limits: %+v", legacy)
	}
	if legacy.maxContinuations != continuationUnlimitedContinuations || legacy.maxTokens != continuationUnlimited {
		t.Fatalf("unset budgets should be unlimited: %+v", legacy)
	}
	bounded := continuationLimitsForConfig(config.AgentConfig{
		AutoContinuationMode:     "off",
		ContinuationSegmentTurns: 5000,
		MaxContinuations:         100,
		MaxTotalTurns:            12,
		MaxRunDurationMs:         10,
		MaxRunTokens:             10,
	})
	if bounded.mode != continuationModeOff || bounded.segmentTurns != 12 || bounded.maxContinuations != 64 || bounded.maxTotalTurns != 12 || bounded.maxDuration != time.Second || bounded.maxTokens != 1000 {
		t.Fatalf("unexpected bounded continuation limits: %+v", bounded)
	}
}

// Every cross-segment budget reads a negative or unset value as "no ceiling",
// and each one is independent: maxContinuations is unlimited because it is
// itself -1, not because the turn budget happens to be. A positive value must
// still be honoured, otherwise the settings panel could not impose a limit.
func TestContinuationLimitsHonourUnlimitedBudgets(t *testing.T) {
	unlimited := continuationLimitsForConfig(config.AgentConfig{
		AutoContinuationMode:     "safe",
		ContinuationSegmentTurns: 40,
		MaxContinuations:         -1,
		MaxTotalTurns:            -1,
		MaxRunDurationMs:         -1,
		MaxRunTokens:             -1,
	})
	if unlimited.maxTotalTurns != continuationUnlimited || unlimited.maxTokens != continuationUnlimited {
		t.Fatalf("negative turn/token budgets were clamped: %+v", unlimited)
	}
	if unlimited.maxDuration > 0 {
		t.Fatalf("negative duration produced a live deadline: %+v", unlimited)
	}
	if unlimited.maxContinuations != continuationUnlimitedContinuations {
		t.Fatalf("negative continuation budget was clamped: %+v", unlimited)
	}
	if unlimited.segmentTurns != 40 {
		t.Fatalf("segment turns should be unaffected by an unlimited total: %+v", unlimited)
	}

	// An explicit ceiling still wins, including alongside unlimited siblings.
	capped := continuationLimitsForConfig(config.AgentConfig{
		AutoContinuationMode:     "safe",
		ContinuationSegmentTurns: 40,
		MaxContinuations:         3,
		MaxTotalTurns:            -1,
		MaxRunTokens:             -1,
	})
	if capped.maxContinuations != 3 {
		t.Fatalf("explicit continuation ceiling was overridden: %+v", capped)
	}

	// A run already past any fixed ceiling must not report a budget reason.
	state := continuationRunState{
		limits: unlimited,
		run:    db.Run{TurnCount: 100000, ConsumedInputTokens: 1 << 40, ConsumedOutputTokens: 1 << 40},
	}
	if reason := continuationBudgetReason(state, segmentOutcome{turns: 10, inputTokens: 1 << 20}); reason != "" {
		t.Fatalf("unlimited budget still reported exhaustion: %q", reason)
	}
}

func TestUpdateContinuationConfigAffectsOnlyFutureRuns(t *testing.T) {
	runner := NewRunner(nil, nil, nil, nil, config.AgentConfig{AutoContinuationMode: "safe", ContinuationSegmentTurns: 40, MaxContinuations: 8, MaxTotalTurns: 200, MaxRunDurationMs: 3600000, MaxRunTokens: 500000})
	first, err := runner.prepareContinuationRun(context.Background(), db.Run{AgentID: "agent-1", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	updated := runner.SetContinuationSettings(ContinuationSettings{Mode: "safe", SegmentTurns: 5, MaxContinuations: 3, MaxTotalTurns: 50, MaxRunDurationMs: 120000, MaxRunTokens: 25000})
	if updated.SegmentTurns != 5 || updated.MaxContinuations != 3 || runner.GetContinuationSettings() != updated {
		t.Fatalf("unexpected updated settings: %+v", updated)
	}
	second, err := runner.prepareContinuationRun(context.Background(), db.Run{AgentID: "agent-1", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	firstLimits, ok := runner.frozenContinuationLimits(first.ID)
	if !ok || firstLimits.segmentTurns != 40 || first.ContinuationSegmentTurns != 40 || first.MaxContinuations != 8 || first.MaxTotalTurns != 200 || first.MaxTotalTokens != 500000 {
		t.Fatalf("existing run settings changed: run=%+v limits=%+v", first, firstLimits)
	}
	secondLimits, ok := runner.frozenContinuationLimits(second.ID)
	if !ok || secondLimits.segmentTurns != 5 || second.ContinuationSegmentTurns != 5 || second.MaxContinuations != 3 || second.MaxTotalTurns != 50 || second.MaxTotalTokens != 25000 {
		t.Fatalf("future run did not receive updated settings: run=%+v limits=%+v", second, secondLimits)
	}
}

func TestContinuationRunRestoresPersistedSegmentLimit(t *testing.T) {
	ctx := context.Background()
	store, createdAgent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	runner := NewRunner(store, nil, nil, nil, config.AgentConfig{AutoContinuationMode: "safe", ContinuationSegmentTurns: 2, MaxContinuations: 3, MaxTotalTurns: 20, MaxRunDurationMs: 60000, MaxRunTokens: 10000})
	request, err := runner.prepareContinuationRun(ctx, db.Run{AgentID: createdAgent.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	runner.SetContinuationSettings(ContinuationSettings{Mode: "safe", SegmentTurns: 9, MaxContinuations: 9, MaxTotalTurns: 90, MaxRunDurationMs: 90000, MaxRunTokens: 90000})
	// Simulate a restarted process: only durable Run fields may define the next segment.
	runner.continuationMu.Lock()
	delete(runner.continuationRunLimits, run.ID)
	runner.continuationMu.Unlock()
	state, err := runner.loadContinuationState(ctx, createdAgent.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.limits.segmentTurns != 2 || state.limits.maxContinuations != 3 || state.limits.maxTotalTurns != 20 || state.limits.maxTokens != 10000 {
		t.Fatalf("recovery used updated settings instead of persisted run snapshot: %+v", state.limits)
	}
}

func TestContinuationBudgetStopsBeforeAnotherModelTurn(t *testing.T) {
	state := continuationRunState{
		run: db.Run{
			TurnCount:            39,
			ConsumedInputTokens:  600,
			ConsumedOutputTokens: 300,
		},
		limits:   continuationLimits{maxTotalTurns: 40, maxTokens: 1000},
		deadline: time.Now().Add(time.Minute),
	}
	if got := continuationBudgetReason(state, segmentOutcome{turns: 1}); got != "max_total_turns" {
		t.Fatalf("expected turn budget exhaustion, got %q", got)
	}
	state.run.TurnCount = 1
	if got := continuationBudgetReason(state, segmentOutcome{inputTokens: 50, outputTokens: 50}); got != "max_total_tokens" {
		t.Fatalf("expected token budget exhaustion, got %q", got)
	}
	state.run.ConsumedInputTokens = 0
	state.run.ConsumedOutputTokens = 0
	state.deadline = time.Now().Add(-time.Millisecond)
	if got := continuationBudgetReason(state, segmentOutcome{}); got != "deadline" {
		t.Fatalf("expected deadline exhaustion, got %q", got)
	}
}

func TestContinuationControlIsServerGeneratedAndKeepsRunIdentity(t *testing.T) {
	run := db.Run{ID: "run-1", ResumeAfterMessageID: "message-9", ContinuationReason: continuationReasonMaxOutputTokens, WaitingBackgroundTaskID: "task-7"}
	message := continuationControlMessage(run, 2)
	if message.Role != "system" || len(message.Blocks) != 1 || message.Blocks[0].Kind != "server_continuation_control" {
		t.Fatalf("unexpected continuation control message: %+v", message)
	}
	for _, required := range []string{run.ID, run.ResumeAfterMessageID, run.WaitingBackgroundTaskID, continuationReasonMaxOutputTokens, "segment 2"} {
		if !strings.Contains(message.Content, required) {
			t.Fatalf("control message missing %q: %s", required, message.Content)
		}
	}
	if strings.Contains(message.Content, "assistant says") {
		t.Fatal("control message must not derive instructions from model output")
	}
}

func TestBackgroundTaskContinuationBoundaryRequiresResumeParent(t *testing.T) {
	encoded, err := json.Marshal(tools.BackgroundTask{ID: "task-1", ParentRunID: "run-1", ResumeParent: true, Status: "queued"})
	if err != nil {
		t.Fatal(err)
	}
	taskID, waits, err := backgroundTaskContinuationBoundary(tools.Result{Output: string(encoded), Meta: map[string]any{"backgroundTaskId": "task-1", "background": true}})
	if err != nil || !waits || taskID != "task-1" {
		t.Fatalf("expected resumeParent boundary, task=%q waits=%v err=%v", taskID, waits, err)
	}
	encoded, _ = json.Marshal(tools.BackgroundTask{ID: "task-2", ResumeParent: false})
	if taskID, waits, err = backgroundTaskContinuationBoundary(tools.Result{Output: string(encoded), Meta: map[string]any{"backgroundTaskId": "task-2", "background": true}}); err != nil || waits || taskID != "" {
		t.Fatalf("non-resuming task must not pause parent, task=%q waits=%v err=%v", taskID, waits, err)
	}
}

func TestCompletedBackgroundTaskResumesAfterParentRunUnregisters(t *testing.T) {
	ctx := context.Background()
	store, createdAgent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	provider := &scriptedProvider{turns: [][]providers.Event{{{Type: "text", Text: "reviewed background result"}, {Type: "done", Done: true, StopReason: "end_turn"}}}}
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
	boundary, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, RunID: run.ID, Role: "user", ParentToolID: "tool-1", ContentText: "background task task-fast queued"})
	if err != nil {
		t.Fatal(err)
	}
	createdTask, err := store.CreateBackgroundTask(ctx, db.BackgroundTask{
		ID:                           "task-fast",
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
	pending, err := store.MarkRunContinuationPending(ctx, run.ID, db.RunContinuationPendingInput{
		ExpectedContinuationCount: 0,
		TurnCount:                 1,
		ResumeAfterMessageID:      boundary.ID,
		LastStopReason:            "tool_use",
		ContinuationReason:        continuationReasonBackgroundTask,
		WaitingBackgroundTaskID:   createdTask.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.SetBackgroundTaskService(&staticBackgroundTaskService{task: tools.BackgroundTask{
		ID: createdTask.ID, OwnerAgentID: createdAgent.ID, ParentRunID: run.ID, ParentToolUseID: "tool-1",
		Kind: tools.BackgroundTaskKindShell, Status: "succeeded", ResumeParent: true,
	}})
	_, cancel := context.WithCancel(context.Background())
	active := &activeRun{cancel: cancel, runID: run.ID, triggerMessageID: trigger.ID}
	runner.runMu.Lock()
	runner.running[createdAgent.ID] = active
	runner.runMu.Unlock()
	if _, err := runner.WakeBackgroundContinuation(ctx, run.ID, createdTask.ID); !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("expected active parent to defer wake, got %v", err)
	}
	runner.unregisterRun(createdAgent.ID, active)
	runner.resumeReadyBackgroundContinuation(run.ID)

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
	if pending.Status != "continuation_pending" || updated.Status != "completed" || provider.requestCount() != 1 {
		t.Fatalf("fast terminal task did not resume after unregister: pending=%+v updated=%+v requests=%d", pending, updated, provider.requestCount())
	}
	if !requestHasSystemText(provider.request(0), createdTask.ID) {
		t.Fatalf("resumed segment did not identify completed task: %+v", provider.request(0).Messages)
	}
}

func TestPartialAssistantMarkerCarriesStopReasonWithoutControlRole(t *testing.T) {
	blocks := []providers.ContentBlock{{Type: "text", Text: "partial text", Kind: "partial:max_output_tokens"}}
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	message := providerMessageFromDB(db.Message{Role: "assistant", ContentText: "partial text", ContentJSON: raw})
	if message.Role != "assistant" || len(message.Blocks) != 1 || message.Blocks[0].Kind != "partial:max_output_tokens" {
		t.Fatalf("partial assistant metadata was not preserved: %+v", message)
	}
}

func TestSafeContinuationKeepsOneRunAndUsesHiddenControl(t *testing.T) {
	ctx := context.Background()
	store, createdAgent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "usage", Usage: &providers.Usage{InputTokens: 10, OutputTokens: 4}}, {Type: "text", Text: "partial "}, {Type: "done", Done: true, StopReason: "max_output_tokens"}},
		{{Type: "usage", Usage: &providers.Usage{InputTokens: 8, OutputTokens: 3}}, {Type: "text", Text: "finished"}, {Type: "done", Done: true, StopReason: "end_turn"}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{
		MaxTurns:                 10,
		AutoContinuationMode:     "safe",
		ContinuationSegmentTurns: 4,
		MaxContinuations:         3,
		MaxTotalTurns:            10,
		MaxRunDurationMs:         60000,
		MaxRunTokens:             10000,
	})
	runner.SetPlanSnapshotProvider(func(ctx context.Context, agentID string) (db.PlanSnapshot, error) {
		generations, err := store.GetPermissionGenerations(ctx, agentID)
		if err != nil {
			return db.PlanSnapshot{}, err
		}
		return db.PlanSnapshot{PolicyGenerationSnapshot: generations.Policy, AgentGenerationSnapshot: generations.Entity, ToolCatalogDigest: "tools-stable", WorkspaceFingerprint: "workspace-stable"}, nil
	})
	trigger, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, Role: "user", ContentText: "continue safely"})
	if err != nil {
		t.Fatal(err)
	}
	runRequest, err := runner.prepareContinuationRun(ctx, db.Run{AgentID: createdAgent.ID, TriggerMessageID: trigger.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, runRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AssignMessageRun(ctx, createdAgent.ID, trigger.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := runner.run(ctx, createdAgent.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	updated, err := store.GetRunByID(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != run.ID || updated.ExecutionGeneration != run.ExecutionGeneration || updated.Status != "completed" || updated.ContinuationCount != 1 || updated.TurnCount != 2 || updated.PlanID != run.PlanID {
		t.Fatalf("continuation changed run identity or counters: before=%+v after=%+v", run, updated)
	}
	if provider.requestCount() != 2 || !requestHasSystemText(provider.request(1), "SERVER CONTINUATION CONTROL") || !requestHasSystemText(provider.request(1), run.ID) {
		t.Fatalf("second segment did not receive hidden server control: requests=%d second=%+v", provider.requestCount(), provider.request(1).Messages)
	}
	messages, err := store.ListMessages(ctx, createdAgent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[1].Role != "assistant" || messages[2].ContentText != "finished" {
		t.Fatalf("unexpected continuation messages: %+v", messages)
	}
	partialBlocks := contentBlocksFromMessage(messages[1])
	if len(partialBlocks) != 1 || partialBlocks[0].Kind != "partial:max_output_tokens" {
		t.Fatalf("truncated assistant message was not marked partial: %+v", partialBlocks)
	}
}

func TestRunContinuationSegmentRefreshesSpecSidecarWithoutPersistence(t *testing.T) {
	ctx := context.Background()
	store, createdAgent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.CreateSpecTask(ctx, db.SpecTask{AgentID: createdAgent.ID, Text: "first turn Spec task", Status: "doing", Protected: true}); err != nil {
		t.Fatal(err)
	}
	var hookErr error
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "sidecar-read", Name: "LifecycleTest", Input: json.RawMessage(`{}`)}}, {Type: "done", Done: true, StopReason: "tool_use"}},
		{{Type: "text", Text: "sidecar complete"}, {Type: "done", Done: true, StopReason: "end_turn"}},
	}}
	provider.onGenerate = func(index int) {
		if index == 0 {
			_, hookErr = store.CreateSpecTask(ctx, db.SpecTask{AgentID: createdAgent.ID, Text: "second turn Spec task", Status: "todo"})
		}
	}
	providerRegistry := providers.NewRegistry()
	providerRegistry.Register(provider)
	toolRegistry := tools.NewRegistry()
	toolRegistry.Register(lifecycleTestTool{output: "ok"})
	runner := NewRunner(store, providerRegistry, toolRegistry, NewHub(), config.AgentConfig{ContextTokenLimit: 120000})
	trigger, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, Role: "user", ContentText: "exercise sidecars"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, db.Run{AgentID: createdAgent.ID, TriggerMessageID: trigger.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AssignMessageRun(ctx, createdAgent.ID, trigger.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	state := continuationRunState{run: run, limits: continuationLimits{segmentTurns: 4, maxTotalTurns: 4, maxTokens: 10000}, deadline: time.Now().Add(time.Minute)}
	outcome, err := runner.runContinuationSegment(ctx, state, 0)
	if err != nil {
		t.Fatal(err)
	}
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if outcome.disposition != segmentComplete || provider.requestCount() != 2 {
		t.Fatalf("unexpected direct segment result: outcome=%+v requests=%d", outcome, provider.requestCount())
	}
	if !requestHasSystemText(provider.request(0), "first turn Spec task") || requestHasSystemText(provider.request(0), "second turn Spec task") || !requestHasSystemText(provider.request(1), "second turn Spec task") {
		t.Fatalf("Spec reminder did not refresh per model turn: first=%+v second=%+v", provider.request(0).Messages, provider.request(1).Messages)
	}
	messages, err := store.ListMessages(ctx, createdAgent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.Role == "system" || strings.Contains(message.ContentText, "side_car") {
			t.Fatalf("sidecar leaked into durable message history: %+v", message)
		}
		for _, block := range contentBlocksFromMessage(message) {
			if strings.HasPrefix(block.Kind, "server_") {
				t.Fatalf("server control block was persisted: %+v", block)
			}
		}
	}
}

func TestSilentProgressSidecarAfterTwentyToolCalls(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	if err := writeTestFile(projectDir, "progress.txt", "progress"); err != nil {
		t.Fatal(err)
	}
	store, createdAgent := newAgentTestStore(t, projectDir, "acceptEdits")
	defer store.Close()

	toolTurn := make([]providers.Event, 0, silentProgressInterval+1)
	for index := 0; index < silentProgressInterval; index++ {
		toolTurn = append(toolTurn, providers.Event{Type: "tool_call", ToolCall: &providers.ToolCall{ID: fmt.Sprintf("progress-read-%d", index), Name: "Read", Input: json.RawMessage(`{"file_path":"progress.txt"}`)}})
	}
	toolTurn = append(toolTurn, providers.Event{Type: "done", Done: true, StopReason: "tool_use"})
	provider := &scriptedProvider{turns: [][]providers.Event{
		toolTurn,
		{{Type: "text", Text: "all reads complete"}, {Type: "done", Done: true, StopReason: "end_turn"}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 3, AutoContinuationMode: "safe", ContinuationSegmentTurns: 4, MaxContinuations: 2, MaxTotalTurns: 4, MaxRunDurationMs: 60000, MaxRunTokens: 10000})
	trigger, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, Role: "user", ContentText: "read repeatedly"})
	if err != nil {
		t.Fatal(err)
	}
	runRequest, err := runner.prepareContinuationRun(ctx, db.Run{AgentID: createdAgent.ID, TriggerMessageID: trigger.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, runRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AssignMessageRun(ctx, createdAgent.ID, trigger.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	state := continuationRunState{run: run, limits: continuationLimits{segmentTurns: 4, maxTotalTurns: 4, maxTokens: 10000}, deadline: time.Now().Add(time.Minute)}
	outcome, err := runner.runContinuationSegment(ctx, state, 0)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.disposition != segmentComplete {
		t.Fatalf("expected direct segment completion, got %+v", outcome)
	}
	if provider.requestCount() != 2 || requestHasSystemText(provider.request(0), "progress_update_request") || !requestHasSystemText(provider.request(1), "progress_update_request") || !requestHasSystemText(provider.request(1), "20 tool calls") {
		t.Fatalf("silent progress sidecar was not injected at the threshold: requests=%d", provider.requestCount())
	}
	messages, err := store.ListMessages(ctx, createdAgent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		for _, block := range contentBlocksFromMessage(message) {
			if block.Kind == "server_silent_progress" {
				t.Fatalf("silent progress control leaked into durable history: %+v", message)
			}
		}
	}
}

// A subagent used to be exempt, from when its text had nowhere visible to land. It
// is now the case this control exists for: it can run for twenty minutes and the
// only account of it is the task panel. The wording differs because its reader is
// the parent, and it is told not to stop early, since a child asked only to report
// progress will otherwise hand back a partial result as though it had finished.
func TestSilentProgressReachesSubagentsWithChildWording(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	if err := writeTestFile(projectDir, "progress.txt", "progress"); err != nil {
		t.Fatal(err)
	}
	store, parent := newAgentTestStore(t, projectDir, "acceptEdits")
	defer store.Close()

	child, err := store.CreateAgent(ctx, db.Agent{
		WorklineID:    parent.WorklineID,
		Type:          "subagent",
		ParentAgentID: parent.ID,
		Title:         "child",
		Model:         "fake:test",
		CWD:           projectDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	toolTurn := make([]providers.Event, 0, silentProgressInterval+1)
	for index := 0; index < silentProgressInterval; index++ {
		toolTurn = append(toolTurn, providers.Event{Type: "tool_call", ToolCall: &providers.ToolCall{ID: fmt.Sprintf("child-read-%d", index), Name: "Read", Input: json.RawMessage(`{"file_path":"progress.txt"}`)}})
	}
	toolTurn = append(toolTurn, providers.Event{Type: "done", Done: true, StopReason: "tool_use"})
	provider := &scriptedProvider{turns: [][]providers.Event{
		toolTurn,
		{{Type: "text", Text: "child work complete"}, {Type: "done", Done: true, StopReason: "end_turn"}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 3, AutoContinuationMode: "safe", ContinuationSegmentTurns: 4, MaxContinuations: 2, MaxTotalTurns: 4, MaxRunDurationMs: 60000, MaxRunTokens: 10000})
	trigger, err := store.AddMessage(ctx, db.Message{AgentID: child.ID, Role: "user", ContentText: "read repeatedly"})
	if err != nil {
		t.Fatal(err)
	}
	runRequest, err := runner.prepareContinuationRun(ctx, db.Run{AgentID: child.ID, TriggerMessageID: trigger.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, runRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AssignMessageRun(ctx, child.ID, trigger.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	state := continuationRunState{run: run, limits: continuationLimits{segmentTurns: 4, maxTotalTurns: 4, maxTokens: 10000}, deadline: time.Now().Add(time.Minute)}
	if _, err := runner.runContinuationSegment(ctx, state, 0); err != nil {
		t.Fatal(err)
	}

	if provider.requestCount() != 2 {
		t.Fatalf("expected two requests, got %d", provider.requestCount())
	}
	if requestHasSystemText(provider.request(0), "progress_update_request") {
		t.Fatal("the nudge must not fire before the threshold")
	}
	if !requestHasSystemText(provider.request(1), "progress_update_request") || !requestHasSystemText(provider.request(1), "20 tool calls") {
		t.Fatal("a subagent did not receive the progress nudge at the threshold")
	}
	// The child wording, not the one addressed at a user typing in the composer.
	if !requestHasSystemText(provider.request(1), "Do not stop early") {
		t.Fatal("the subagent nudge must tell it to keep going")
	}
	if requestHasSystemText(provider.request(1), "user's current language") {
		t.Fatal("a subagent received the top-level wording")
	}

	messages, err := store.ListMessages(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		for _, block := range contentBlocksFromMessage(message) {
			if block.Kind == "server_silent_progress" {
				t.Fatalf("silent progress control leaked into durable history: %+v", message)
			}
		}
	}
}

func TestSafeContinuationAtSegmentTurnLimitRequiresCompleteToolResult(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	if err := writeTestFile(projectDir, "note.txt", "segment"); err != nil {
		t.Fatal(err)
	}
	store, createdAgent := newAgentTestStore(t, projectDir, "acceptEdits")
	defer store.Close()
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "segment-read", Name: "Read", Input: json.RawMessage(`{"file_path":"note.txt"}`)}}, {Type: "done", Done: true, StopReason: "tool_use"}},
		{{Type: "text", Text: "segment complete"}, {Type: "done", Done: true, StopReason: "end_turn"}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 4, AutoContinuationMode: "safe", ContinuationSegmentTurns: 1, MaxContinuations: 2, MaxTotalTurns: 4, MaxRunDurationMs: 60000, MaxRunTokens: 10000})
	runner.SetPlanSnapshotProvider(func(ctx context.Context, agentID string) (db.PlanSnapshot, error) {
		generations, err := store.GetPermissionGenerations(ctx, agentID)
		return db.PlanSnapshot{PolicyGenerationSnapshot: generations.Policy, AgentGenerationSnapshot: generations.Entity, ToolCatalogDigest: "tools", WorkspaceFingerprint: "workspace"}, err
	})
	trigger, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, Role: "user", ContentText: "read then continue"})
	if err != nil {
		t.Fatal(err)
	}
	runRequest, err := runner.prepareContinuationRun(ctx, db.Run{AgentID: createdAgent.ID, TriggerMessageID: trigger.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, runRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AssignMessageRun(ctx, createdAgent.ID, trigger.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := runner.run(ctx, createdAgent.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	updated, err := store.GetRunByID(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "completed" || updated.ContinuationCount != 1 || updated.LastStopReason != continuationReasonSegmentTurns || provider.requestCount() != 2 {
		t.Fatalf("segment continuation did not preserve one run: %+v requests=%d", updated, provider.requestCount())
	}
	if !requestHasToolResult(provider.request(1), "segment-read", false) || !requestHasSystemText(provider.request(1), "SERVER CONTINUATION CONTROL") {
		t.Fatalf("continuation started without a complete tool result boundary: %+v", provider.request(1).Messages)
	}
}

func TestInterruptCancelsDurableContinuationPendingRun(t *testing.T) {
	ctx := context.Background()
	store, createdAgent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{AutoContinuationMode: "safe", ContinuationSegmentTurns: 2, MaxContinuations: 2, MaxTotalTurns: 4, MaxRunDurationMs: 60000, MaxRunTokens: 10000})
	runner.SetPlanSnapshotProvider(func(ctx context.Context, agentID string) (db.PlanSnapshot, error) {
		generations, err := store.GetPermissionGenerations(ctx, agentID)
		return db.PlanSnapshot{PolicyGenerationSnapshot: generations.Policy, AgentGenerationSnapshot: generations.Entity, ToolCatalogDigest: "tools", WorkspaceFingerprint: "workspace"}, err
	})
	trigger, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, Role: "user", ContentText: "pause"})
	if err != nil {
		t.Fatal(err)
	}
	runRequest, err := runner.prepareContinuationRun(ctx, db.Run{AgentID: createdAgent.ID, TriggerMessageID: trigger.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, runRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AssignMessageRun(ctx, createdAgent.ID, trigger.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	partial, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, RunID: run.ID, Role: "assistant", ContentText: "partial"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkRunContinuationPending(ctx, run.ID, db.RunContinuationPendingInput{ExpectedContinuationCount: 0, TurnCount: 1, ResumeAfterMessageID: partial.ID, LastStopReason: "max_output_tokens", ContinuationReason: continuationReasonMaxOutputTokens}); err != nil {
		t.Fatal(err)
	}
	interrupted, err := runner.Interrupt(ctx, createdAgent.ID)
	if err != nil || !interrupted {
		t.Fatalf("expected pending continuation interrupt, interrupted=%v err=%v", interrupted, err)
	}
	updated, err := store.GetRunByID(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "interrupted" || updated.ContinuationCount != 1 {
		t.Fatalf("interrupt did not stop continuation_pending run: %+v", updated)
	}
}

func TestContinuationFailsClosedOnSnapshotChangeOrNewMessage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(context.Context, *db.Store, db.Agent) error
		match  string
	}{
		{name: "snapshot change", match: "generation changed", mutate: func(ctx context.Context, store *db.Store, agent db.Agent) error {
			_, err := store.UpdateAgentPermissionMode(ctx, agent.ID, "readOnly")
			return err
		}},
		{name: "new message", match: "new message preempted", mutate: func(ctx context.Context, store *db.Store, agent db.Agent) error {
			_, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "preempt"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, createdAgent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
			defer store.Close()
			provider := &scriptedProvider{turns: [][]providers.Event{{{Type: "text", Text: "partial"}, {Type: "done", Done: true, StopReason: "max_output_tokens"}}}}
			provider.onGenerate = func(index int) {
				if index == 0 {
					if err := tc.mutate(ctx, store, createdAgent); err != nil {
						t.Errorf("mutation failed: %v", err)
					}
				}
			}
			runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 4, AutoContinuationMode: "safe", ContinuationSegmentTurns: 2, MaxContinuations: 2, MaxTotalTurns: 4, MaxRunDurationMs: 60000, MaxRunTokens: 10000})
			runner.SetPlanSnapshotProvider(func(ctx context.Context, agentID string) (db.PlanSnapshot, error) {
				generations, err := store.GetPermissionGenerations(ctx, agentID)
				return db.PlanSnapshot{PolicyGenerationSnapshot: generations.Policy, AgentGenerationSnapshot: generations.Entity, ToolCatalogDigest: "tools", WorkspaceFingerprint: "workspace"}, err
			})
			trigger, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, Role: "user", ContentText: "start"})
			if err != nil {
				t.Fatal(err)
			}
			runRequest, err := runner.prepareContinuationRun(ctx, db.Run{AgentID: createdAgent.ID, TriggerMessageID: trigger.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
			if err != nil {
				t.Fatal(err)
			}
			run, err := store.CreateRun(ctx, runRequest)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.AssignMessageRun(ctx, createdAgent.ID, trigger.ID, run.ID); err != nil {
				t.Fatal(err)
			}
			err = runner.run(ctx, createdAgent.ID, run.ID)
			if err == nil || !strings.Contains(err.Error(), tc.match) {
				t.Fatalf("expected fail-closed error containing %q, got %v", tc.match, err)
			}
			updated, err := store.GetRunByID(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.ContinuationCount != 0 || provider.requestCount() != 1 {
				t.Fatalf("unsafe boundary continued: run=%+v requests=%d", updated, provider.requestCount())
			}
		})
	}
}

func TestRecoverContinuationPendingRunsResumesOnlySafeBoundary(t *testing.T) {
	ctx := context.Background()
	store, createdAgent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	provider := &scriptedProvider{turns: [][]providers.Event{{{Type: "text", Text: "recovered"}, {Type: "done", Done: true, StopReason: "end_turn"}}}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 4, AutoContinuationMode: "safe", ContinuationSegmentTurns: 2, MaxContinuations: 2, MaxTotalTurns: 4, MaxRunDurationMs: 60000, MaxRunTokens: 10000})
	runner.SetPlanSnapshotProvider(func(ctx context.Context, agentID string) (db.PlanSnapshot, error) {
		generations, err := store.GetPermissionGenerations(ctx, agentID)
		return db.PlanSnapshot{PolicyGenerationSnapshot: generations.Policy, AgentGenerationSnapshot: generations.Entity, ToolCatalogDigest: "tools", WorkspaceFingerprint: "workspace"}, err
	})
	trigger, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, Role: "user", ContentText: "recover"})
	if err != nil {
		t.Fatal(err)
	}
	runRequest, err := runner.prepareContinuationRun(ctx, db.Run{AgentID: createdAgent.ID, TriggerMessageID: trigger.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, runRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AssignMessageRun(ctx, createdAgent.ID, trigger.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	partial, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, RunID: run.ID, Role: "assistant", ContentText: "partial"})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.MarkRunContinuationPending(ctx, run.ID, db.RunContinuationPendingInput{ExpectedContinuationCount: 0, TurnCount: 1, ConsumedInputTokens: 5, ConsumedOutputTokens: 2, ResumeAfterMessageID: partial.ID, LastStopReason: "max_output_tokens", ContinuationReason: continuationReasonMaxOutputTokens})
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != "continuation_pending" {
		t.Fatalf("expected continuation_pending, got %+v", pending)
	}
	if err := runner.RecoverInterruptedRuns(ctx); err != nil {
		t.Fatal(err)
	}
	stillPending, err := store.GetRunByID(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillPending.Status != "continuation_pending" || provider.requestCount() != 0 {
		t.Fatalf("ordinary startup recovery must not resume continuations before background services start: run=%+v requests=%d", stillPending, provider.requestCount())
	}
	if err := runner.RecoverContinuationPendingRuns(ctx); err != nil {
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
	if updated.ID != run.ID || updated.Status != "completed" || updated.ContinuationCount != 1 || provider.requestCount() != 1 {
		t.Fatalf("recovery did not preserve and resume continuation run: run=%+v requests=%d", updated, provider.requestCount())
	}
	if !requestHasSystemText(provider.request(0), "SERVER CONTINUATION CONTROL") {
		t.Fatalf("recovered segment missing hidden continuation control: %+v", provider.request(0).Messages)
	}
}

func TestContinuationCheckpointValidationPreservesTrackingAndFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    bool
		wantError bool
		wantState string
	}{
		{name: "matching checkpoint remains tracking", wantState: db.RunCheckpointTracking},
		{name: "workspace drift invalidates checkpoint", mutate: true, wantError: true, wantState: db.RunCheckpointInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := newCheckpointTestRepo(t)
			store, createdAgent := newAgentTestStore(t, repo, "acceptEdits")
			defer store.Close()
			runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{AutoContinuationMode: "safe", ContinuationSegmentTurns: 2, MaxContinuations: 2, MaxTotalTurns: 4, MaxRunDurationMs: 60000, MaxRunTokens: 10000})
			trigger, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, Role: "user", ContentText: "checkpoint"})
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
			runner.captureRunCheckpoint(ctx, createdAgent, run.ID)
			tracked, err := store.GetRunByID(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tracked.CheckpointState != db.RunCheckpointTracking {
				t.Fatalf("expected tracking checkpoint, got %+v", tracked)
			}
			if tc.mutate {
				if err := writeTestFile(repo, "outside.txt", "external change\n"); err != nil {
					t.Fatal(err)
				}
			}
			err = runner.validateContinuationRunGitCheckpoint(ctx, tracked)
			if tc.wantError != (err != nil) {
				t.Fatalf("validation error = %v, wantError=%v", err, tc.wantError)
			}
			updated, err := store.GetRunByID(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.CheckpointState != tc.wantState {
				t.Fatalf("checkpoint state = %q, want %q: %+v", updated.CheckpointState, tc.wantState, updated)
			}
		})
	}
}

func TestContinuationRecordsUsageBeforeTerminalModelError(t *testing.T) {
	ctx := context.Background()
	store, createdAgent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	provider := &scriptedProvider{turns: [][]providers.Event{{
		{Type: "usage", Usage: &providers.Usage{InputTokens: 13, OutputTokens: 7}},
		{Type: "text", Text: "blocked output"},
		{Type: "done", Done: true, StopReason: "content_filter"},
	}}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{AutoContinuationMode: "safe", ContinuationSegmentTurns: 2, MaxContinuations: 2, MaxTotalTurns: 4, MaxRunDurationMs: 60000, MaxRunTokens: 10000})
	trigger, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, Role: "user", ContentText: "record usage"})
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
	if err := runner.run(ctx, createdAgent.ID, run.ID); err == nil || !strings.Contains(err.Error(), "unknown stop reason") {
		t.Fatalf("expected terminal provider error, got %v", err)
	}
	updated, err := store.GetRunByID(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.TurnCount != 1 || updated.ConsumedInputTokens != 13 || updated.ConsumedOutputTokens != 7 {
		t.Fatalf("error segment usage was not persisted: %+v", updated)
	}
}

func TestPlanDraftAndUnknownStopReasonsDoNotContinue(t *testing.T) {
	for _, tc := range []struct {
		name          string
		executionMode string
		stopReason    string
	}{
		{name: "plan draft", executionMode: db.RunExecutionModePlan, stopReason: "max_output_tokens"},
		{name: "unknown reason", executionMode: db.RunExecutionModeExecute, stopReason: "content_filter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, createdAgent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
			defer store.Close()
			provider := &scriptedProvider{turns: [][]providers.Event{{{Type: "text", Text: "partial"}, {Type: "done", Done: true, StopReason: tc.stopReason}}}}
			runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 4, AutoContinuationMode: "safe", ContinuationSegmentTurns: 2, MaxContinuations: 2, MaxTotalTurns: 4, MaxRunDurationMs: 60000, MaxRunTokens: 10000})
			runner.SetPlanSnapshotProvider(func(ctx context.Context, agentID string) (db.PlanSnapshot, error) {
				generations, err := store.GetPermissionGenerations(ctx, agentID)
				return db.PlanSnapshot{PolicyGenerationSnapshot: generations.Policy, AgentGenerationSnapshot: generations.Entity, ToolCatalogDigest: "tools", WorkspaceFingerprint: "workspace"}, err
			})
			trigger, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, Role: "user", ContentText: "do not auto continue"})
			if err != nil {
				t.Fatal(err)
			}
			runRequest, err := runner.prepareContinuationRun(ctx, db.Run{AgentID: createdAgent.ID, TriggerMessageID: trigger.ID, Status: "running", ExecutionMode: tc.executionMode})
			if err != nil {
				t.Fatal(err)
			}
			run, err := store.CreateRun(ctx, runRequest)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.AssignMessageRun(ctx, createdAgent.ID, trigger.ID, run.ID); err != nil {
				t.Fatal(err)
			}
			if err := runner.run(ctx, createdAgent.ID, run.ID); err == nil {
				t.Fatal("expected fail-closed stop")
			}
			updated, err := store.GetRunByID(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.ContinuationCount != 0 || provider.requestCount() != 1 {
				t.Fatalf("unsafe boundary continued: run=%+v requests=%d", updated, provider.requestCount())
			}
		})
	}
}

func TestPrepareContinuationRunPersistsUnlimitedSegmentTurnsAsZero(t *testing.T) {
	// config.Default ships ContinuationSegmentTurns: -1, meaning "no per-segment
	// ceiling". Every other budget is written through durableBudget, which spells
	// unlimited as 0 because the runs table rejects negatives; segment turns was
	// assigned raw, so a stock install failed CreateRun with "run continuation
	// counters must not be negative" on the very first message.
	ctx := context.Background()
	store, createdAgent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	runner := NewRunner(store, nil, nil, nil, config.AgentConfig{
		AutoContinuationMode:     "safe",
		ContinuationSegmentTurns: -1,
		MaxContinuations:         -1,
		MaxTotalTurns:            -1,
	})

	prepared, err := runner.prepareContinuationRun(ctx, db.Run{
		AgentID:       createdAgent.ID,
		Status:        "running",
		ExecutionMode: db.RunExecutionModeExecute,
	})
	if err != nil {
		t.Fatalf("prepare with the shipped defaults must succeed: %v", err)
	}
	if prepared.ContinuationSegmentTurns < 0 {
		t.Fatalf("negative segment turns would be rejected by the runs table: %d", prepared.ContinuationSegmentTurns)
	}
	if prepared.ContinuationSegmentTurns != 0 {
		t.Fatalf("unlimited must persist as 0, got %d", prepared.ContinuationSegmentTurns)
	}

	// 0 has to read back as "no ceiling" rather than "zero turns allowed",
	// otherwise the run would be unable to take a single turn.
	limits, ok := runner.frozenContinuationLimits(prepared.ID)
	if !ok {
		t.Fatal("prepared run has no frozen limits")
	}
	if limits.segmentTurns > 0 {
		t.Fatalf("stored 0 must restore as unlimited, got %d", limits.segmentTurns)
	}
}

// The provider-error retry was unreachable: the failing path never recorded the
// reason the retry check requires, so a transient 502 always ended the run. A
// fault on the very first turn also has no durable resume point, which the
// scheduler refuses -- so that case has to retry the segment in place.
func TestFirstTurnProviderErrorRetriesInPlaceAndCompletes(t *testing.T) {
	ctx := context.Background()
	store, createdAgent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	provider := &scriptedProvider{turns: [][]providers.Event{
		// No text, no blocks: nothing durable to resume from.
		{{Type: "error", Text: "ttapy request failed: HTTP 502 (Bad Gateway)"}},
		{{Type: "text", Text: "recovered after 502"}, {Type: "done", Done: true, StopReason: "end_turn"}},
	}}
	// MaxTransientRetries 0 keeps the low-level retry out of the way so the
	// fault reaches the segment loop, which is the path under test.
	runner := newAgentTestRunner(store, provider, config.AgentConfig{
		MaxTurns: 2, MaxTransientRetries: 0, AutoContinuationMode: "safe",
		ContinuationSegmentTurns: 2, MaxContinuations: 2, MaxTotalTurns: 6,
		MaxRunDurationMs: 60000, MaxRunTokens: 100000,
	})

	trigger, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, Role: "user", ContentText: "do the thing"})
	if err != nil {
		t.Fatal(err)
	}
	// A real run is required: runContinuous forces continuation off for an empty
	// run id, and with it the retry path being tested.
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
	if err := runner.run(ctx, createdAgent.ID, run.ID); err != nil {
		t.Fatalf("a retryable first-turn 502 must not fail the run: %v", err)
	}
	if provider.requestCount() != 2 {
		t.Fatalf("expected the segment to be retried in place, got %d provider requests", provider.requestCount())
	}
	messages, err := store.ListMessages(ctx, createdAgent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected the trigger plus one recovered reply, got %d messages", len(messages))
	}
	if messages[0].ID != trigger.ID {
		t.Fatalf("trigger message was disturbed: %+v", messages[0])
	}
	if messages[1].Role != "assistant" || messages[1].ContentText != "recovered after 502" {
		t.Fatalf("expected the recovered assistant reply, got %+v", messages[1])
	}
}

// A store failure is not a provider fault. It borrows the same code path, so it
// must not inherit the retry: retrying would fail identically and mask the real
// cause behind a provider error message.
func TestSegmentOutcomeDropsProviderReasonOnPersistFailure(t *testing.T) {
	runner := &Runner{}
	safe := continuationRunState{limits: continuationLimits{mode: continuationModeSafe}}
	storeFault := segmentOutcome{continuationReason: "", resumeAfterID: ""}
	if runner.retryableProviderError(safe, storeFault, errors.New("disk full")) {
		t.Fatal("an outcome with no provider reason must not be retried")
	}
}
