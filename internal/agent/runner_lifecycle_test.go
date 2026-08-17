package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

func TestRunnerCapturesScopedGitCheckpointAtRunCompletion(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main", "--template="}, {"config", "user.name", "Autoto Test"}, {"config", "user.email", "test@example.com"}} {
		if _, err := runCheckpointGit(ctx, repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeTestFile(repo, "tracked.txt", "base\n"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "tracked.txt"}, {"commit", "-m", "initial"}} {
		if _, err := runCheckpointGit(ctx, repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	baseHead, ok := gitHead(ctx, repo)
	if !ok {
		t.Fatal("expected git head")
	}
	repoRoot, ok := gitRepoRoot(ctx, repo)
	if !ok {
		t.Fatal("expected git repository root")
	}
	store, agent := newAgentTestStore(t, repo, "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "create file"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "checkpoint-write", Name: "Write", Input: json.RawMessage(`{"file_path":"run-new.txt","content":"created by run\n"}`)}}, {Type: "done", Done: true}},
		{{Type: "text", Text: "done"}, {Type: "done", Done: true}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 3})

	runner.Run(ctx, agent.ID)

	runs, err := store.ListRuns(ctx, agent.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected one run, got %+v", runs)
	}
	run := runs[0]
	if run.BaseHead != baseHead || run.EndHead != baseHead || run.CheckpointRepoRoot != repoRoot || run.GitSnapshotAt == "" {
		t.Fatalf("unexpected git checkpoint metadata: %+v", run)
	}
	changes, err := store.ListRunGitChanges(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Path != "run-new.txt" || !changes[0].Untracked || changes[0].WorktreeFingerprint == "" {
		t.Fatalf("unexpected run git changes: %+v", changes)
	}
}

func TestRunnerExcludesChangesOutsideToolWindowFromScopedSnapshot(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main", "--template="}, {"config", "user.name", "Autoto Test"}, {"config", "user.email", "test@example.com"}} {
		if _, err := runCheckpointGit(ctx, repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeTestFile(repo, "tracked.txt", "base\n"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "tracked.txt"}, {"commit", "-m", "initial"}} {
		if _, err := runCheckpointGit(ctx, repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	store, agent := newAgentTestStore(t, repo, "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "create one file"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "owned-write", Name: "Write", Input: json.RawMessage(`{"file_path":"owned.txt","content":"run\n"}`)}}, {Type: "done", Done: true}},
		{{Type: "text", Text: "done"}, {Type: "done", Done: true}},
	}}
	provider.onGenerate = func(index int) {
		if index != 1 {
			return
		}
		if err := os.WriteFile(filepath.Join(repo, "concurrent-user.txt"), []byte("user\n"), 0o644); err != nil {
			t.Errorf("write concurrent user file: %v", err)
		}
	}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 3})

	runner.Run(ctx, agent.ID)

	runs, err := store.ListRuns(ctx, agent.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected one run, got %+v", runs)
	}
	changes, err := store.ListRunGitChanges(ctx, runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Path != "owned.txt" {
		t.Fatalf("expected only tool-window file to be owned, got %+v", changes)
	}
	if _, err := os.Stat(filepath.Join(repo, "concurrent-user.txt")); err != nil {
		t.Fatalf("expected concurrent user file to remain: %v", err)
	}
}

func TestRunnerInvalidatesCheckpointWhenToolOverwritesPreDirtyPath(t *testing.T) {
	ctx := context.Background()
	repo := newCheckpointTestRepo(t)
	store, agent := newAgentTestStore(t, repo, "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "overwrite tracked"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "overwrite-dirty", Name: "Write", Input: json.RawMessage(`{"file_path":"tracked.txt","content":"agent\n"}`)}}, {Type: "done", Done: true}},
		{{Type: "text", Text: "done"}, {Type: "done", Done: true}},
	}}
	provider.onGenerate = func(index int) {
		if index == 0 {
			if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("user before tool\n"), 0o644); err != nil {
				t.Errorf("write pre-tool user change: %v", err)
			}
		}
	}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 3})

	runner.Run(ctx, agent.ID)

	assertRunGitCheckpointUnavailable(t, ctx, store, agent.ID)
}

func TestRunnerInvalidatesCheckpointWhenOwnedPathChangesBetweenTools(t *testing.T) {
	ctx := context.Background()
	repo := newCheckpointTestRepo(t)
	store, agent := newAgentTestStore(t, repo, "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "overwrite tracked twice"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "owned-first", Name: "Write", Input: json.RawMessage(`{"file_path":"tracked.txt","content":"agent first\n"}`)}}, {Type: "done", Done: true}},
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "owned-second", Name: "Write", Input: json.RawMessage(`{"file_path":"tracked.txt","content":"agent second\n"}`)}}, {Type: "done", Done: true}},
		{{Type: "text", Text: "done"}, {Type: "done", Done: true}},
	}}
	provider.onGenerate = func(index int) {
		if index == 1 {
			if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("user between tools\n"), 0o644); err != nil {
				t.Errorf("write between-tool user change: %v", err)
			}
		}
	}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 4})

	runner.Run(ctx, agent.ID)

	assertRunGitCheckpointUnavailable(t, ctx, store, agent.ID)
}

func TestRunnerInterruptCancelsPendingApproval(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "run bash"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "bash-wait", Name: "Bash", Input: json.RawMessage(`{"command":"printf wait"}`)}}, {Type: "done", Done: true}}}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 2})
	done := make(chan struct{})
	go func() { runner.Run(ctx, agent.ID); close(done) }()
	waitForPendingApproval(t, runner, agent.ID, "bash-wait")
	interrupted, err := runner.Interrupt(ctx, agent.ID)
	if err != nil || !interrupted {
		t.Fatalf("interrupt failed interrupted=%v err=%v", interrupted, err)
	}
	waitDone(t, done)
	if runnerPendingApprovalCount(runner) != 0 {
		t.Fatalf("expected pending approval cleanup")
	}
	call, err := store.GetToolCallByUseID(ctx, agent.ID, "bash-wait")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != "denied" {
		t.Fatalf("expected canceled approval to be denied, got %+v", call)
	}
}

func TestRunnerStopsAtMaxTurns(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	if err := writeTestFile(projectDir, "note.txt", "loop"); err != nil {
		t.Fatal(err)
	}
	store, agent := newAgentTestStore(t, projectDir, "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "loop"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "tool-a", Name: "Read", Input: json.RawMessage(`{"file_path":"note.txt"}`)}}, {Type: "done", Done: true}},
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "tool-b", Name: "Read", Input: json.RawMessage(`{"file_path":"note.txt"}`)}}, {Type: "done", Done: true}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 2})

	err := runner.run(ctx, agent.ID, "")
	if err == nil || !strings.Contains(err.Error(), "max turns") {
		t.Fatalf("expected max turns error, got %v", err)
	}
}

func TestRunnerPendingRunCancelsActiveAndRerunsWithLatestMessages(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "first"}); err != nil {
		t.Fatal(err)
	}
	provider := &pendingProvider{started: make(chan struct{})}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 2})

	firstDone := make(chan struct{})
	go func() {
		runner.Run(ctx, agent.ID)
		close(firstDone)
	}()
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "second"}); err != nil {
		t.Fatal(err)
	}
	runner.Run(ctx, agent.ID)

	waitForAgentStatus(t, store, agent.ID, "idle")
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first run did not finish after pending rerun")
	}
	if provider.requestCount() != 2 {
		t.Fatalf("expected canceled first turn plus pending rerun, got %d provider calls", provider.requestCount())
	}
	secondRequest := provider.request(1)
	if !requestHasUserText(secondRequest, "first") || !requestHasUserText(secondRequest, "second") {
		t.Fatalf("expected pending rerun to include both user messages, got %+v", secondRequest.Messages)
	}
	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[2].Role != "assistant" || messages[2].ContentText != "handled latest" {
		t.Fatalf("unexpected pending rerun messages: %+v", messages)
	}
}

func TestRunnerInterruptCancelsProviderTurn(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "wait"}); err != nil {
		t.Fatal(err)
	}
	provider := &blockingProvider{started: make(chan struct{})}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 2})

	done := make(chan struct{})
	go func() {
		runner.Run(ctx, agent.ID)
		close(done)
	}()
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}
	interrupted, err := runner.Interrupt(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !interrupted {
		t.Fatal("expected active run to be interrupted")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not stop after interrupt")
	}
	updated, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "interrupted" {
		t.Fatalf("expected interrupted status, got %q", updated.Status)
	}
}

type pendingProvider struct {
	mu       sync.Mutex
	requests []providers.GenerateRequest
	started  chan struct{}
	once     sync.Once
}

func (p *pendingProvider) Name() string { return "fake" }

func (p *pendingProvider) ListModels(context.Context) ([]string, error) {
	return []string{"test"}, nil
}

func (p *pendingProvider) Generate(ctx context.Context, req providers.GenerateRequest) (<-chan providers.Event, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	call := len(p.requests)
	p.mu.Unlock()
	if call == 1 {
		p.once.Do(func() { close(p.started) })
		return make(chan providers.Event), nil
	}
	out := make(chan providers.Event, 2)
	out <- providers.Event{Type: "text", Text: "handled latest"}
	out <- providers.Event{Type: "done", Done: true}
	close(out)
	return out, nil
}

func (p *pendingProvider) request(index int) providers.GenerateRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests[index]
}

func (p *pendingProvider) requestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func assertRunGitCheckpointUnavailable(t *testing.T, ctx context.Context, store *db.Store, agentID string) {
	t.Helper()
	runs, err := store.ListRuns(ctx, agentID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected one run, got %+v", runs)
	}
	if runs[0].GitSnapshotAt != "" {
		t.Fatalf("expected unavailable scoped snapshot after ownership conflict, got %+v", runs[0])
	}
	changes, err := store.ListRunGitChanges(ctx, runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected no owned paths after ownership conflict, got %+v", changes)
	}
}

func TestRecoverInterruptedRunsFinalizesConsistentTrackingCheckpoint(t *testing.T) {
	ctx := context.Background()
	repo := newCheckpointTestRepo(t)
	store, agent := newAgentTestStore(t, repo, "acceptEdits")
	defer store.Close()
	baseHead, ok := gitHead(ctx, repo)
	if !ok {
		t.Fatal("expected git HEAD")
	}
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginRunGitCheckpoint(ctx, run.ID, baseHead, repo); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(repo, "tracked.txt", "run change\n"); err != nil {
		t.Fatal(err)
	}
	changes, err := gitRunChangeSnapshot(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunGitCheckpointCapturing(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceRunGitCheckpointChanges(ctx, run.ID, runGitChangeSlice(changes)); err != nil {
		t.Fatal(err)
	}
	for _, call := range []db.ToolCall{
		{AgentID: agent.ID, RunID: run.ID, ToolUseID: "recovery-pending", ToolName: "Bash", Status: "pending_approval"},
		{AgentID: agent.ID, RunID: run.ID, ToolUseID: "recovery-approved", ToolName: "Write", Status: "approved"},
	} {
		if _, err := store.AddToolCall(ctx, call); err != nil {
			t.Fatal(err)
		}
	}

	runner := NewRunner(store, nil, nil, NewHub(), config.AgentConfig{})
	if err := runner.RecoverInterruptedRuns(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetRun(ctx, agent.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "interrupted" || got.CheckpointState != db.RunCheckpointReady || got.GitSnapshotAt == "" || got.EndHead != baseHead || got.ErrorMessage != "process restarted" {
		t.Fatalf("unexpected recovered run: %+v", got)
	}
	updatedAgent, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedAgent.Status != "interrupted" {
		t.Fatalf("expected agent interruption recovery, got %+v", updatedAgent)
	}
	pending, err := store.ListPendingToolCalls(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no zombie pending approvals, got %+v", pending)
	}
	for _, toolUseID := range []string{"recovery-pending", "recovery-approved"} {
		call, err := store.GetToolCallByUseID(ctx, agent.ID, toolUseID)
		if err != nil {
			t.Fatal(err)
		}
		if call.Status != "denied" || call.PermissionDecidedBy != "system" || call.PermissionDecisionReason != "process restarted" || call.ErrorMessage != "process restarted" {
			t.Fatalf("expected restarted tool call cleanup, got %+v", call)
		}
	}
}

func TestRecoverInterruptedRunsInvalidatesCapturingOrMismatchedCheckpoint(t *testing.T) {
	ctx := context.Background()
	repo := newCheckpointTestRepo(t)
	store, agent := newAgentTestStore(t, repo, "acceptEdits")
	defer store.Close()
	baseHead, ok := gitHead(ctx, repo)
	if !ok {
		t.Fatal("expected git HEAD")
	}
	capturing, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginRunGitCheckpoint(ctx, capturing.ID, baseHead, repo); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunGitCheckpointCapturing(ctx, capturing.ID); err != nil {
		t.Fatal(err)
	}
	rolling, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginRunGitCheckpoint(ctx, rolling.ID, baseHead, repo); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunGitCheckpointCapturing(ctx, rolling.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceRunGitCheckpointChanges(ctx, rolling.ID, nil); err != nil {
		t.Fatal(err)
	}
	ready, err := store.FinalizeRunGitCheckpoint(ctx, rolling.ID, baseHead)
	if err != nil || !ready {
		t.Fatalf("expected rolling checkpoint to become ready, ready=%v err=%v", ready, err)
	}
	if err := store.ClaimRunGitRollback(ctx, rolling.ID); err != nil {
		t.Fatal(err)
	}
	mismatched, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginRunGitCheckpoint(ctx, mismatched.ID, baseHead, repo); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(repo, "tracked.txt", "outside checkpoint window\n"); err != nil {
		t.Fatal(err)
	}

	runner := NewRunner(store, nil, nil, NewHub(), config.AgentConfig{})
	if err := runner.RecoverInterruptedRuns(ctx); err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{capturing.ID, mismatched.ID} {
		got, err := store.GetRun(ctx, agent.ID, runID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "interrupted" || got.CheckpointState != db.RunCheckpointInvalid || got.GitSnapshotAt != "" || got.CheckpointError == "" {
			t.Fatalf("expected invalid interrupted checkpoint, got %+v", got)
		}
	}
	rolled, err := store.GetRun(ctx, agent.ID, rolling.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rolled.Status != "interrupted" || rolled.CheckpointState != db.RunCheckpointInvalid || !strings.Contains(rolled.CheckpointError, "rollback was in progress") {
		t.Fatalf("expected rolling_back checkpoint to be invalidated conservatively, got %+v", rolled)
	}
}

func TestRecoverInterruptedRunsInvalidatesCompletedRollingBackCheckpointIdempotently(t *testing.T) {
	ctx := context.Background()
	repo := newCheckpointTestRepo(t)
	path := filepath.Join(t.TempDir(), "recovery.db")
	store, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, db.ToolExecutionGroupSchemaSQL()); err != nil {
		store.Close()
		t.Fatal(err)
	}
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", repo, "fake:test", "acceptEdits")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	baseHead, ok := gitHead(ctx, repo)
	if !ok {
		store.Close()
		t.Fatal("expected git HEAD")
	}
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "completed"})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.BeginRunGitCheckpoint(ctx, run.ID, baseHead, repo); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.MarkRunGitCheckpointCapturing(ctx, run.ID); err != nil {
		store.Close()
		t.Fatal(err)
	}
	change := db.RunGitChange{Path: "owned.txt", IndexStatus: " ", WorktreeStatus: "M", WorktreeFingerprint: "audit"}
	if err := store.ReplaceRunGitCheckpointChanges(ctx, run.ID, []db.RunGitChange{change}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	ready, err := store.FinalizeRunGitCheckpoint(ctx, run.ID, baseHead)
	if err != nil || !ready {
		store.Close()
		t.Fatalf("expected ready checkpoint, ready=%v err=%v", ready, err)
	}
	if err := store.ClaimRunGitRollback(ctx, run.ID); err != nil {
		store.Close()
		t.Fatal(err)
	}

	runner := NewRunner(store, nil, nil, NewHub(), config.AgentConfig{})
	if err := runner.RecoverInterruptedRuns(ctx); err != nil {
		store.Close()
		t.Fatal(err)
	}
	assertCompletedRollingBackRecovery(t, ctx, store, agent.ID, run.ID)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner = NewRunner(store, nil, nil, NewHub(), config.AgentConfig{})
	if err := runner.RecoverInterruptedRuns(ctx); err != nil {
		t.Fatal(err)
	}
	assertCompletedRollingBackRecovery(t, ctx, store, agent.ID, run.ID)
}

func assertCompletedRollingBackRecovery(t *testing.T, ctx context.Context, store *db.Store, agentID, runID string) {
	t.Helper()
	run, err := store.GetRun(ctx, agentID, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" || run.CheckpointState != db.RunCheckpointInvalid || run.CheckpointError != "process restarted while rollback was in progress" {
		t.Fatalf("expected completed run rollback recovery to remain terminal and become invalid, got %+v", run)
	}
	changes, err := store.ListRunGitChanges(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Path != "owned.txt" {
		t.Fatalf("expected rollback recovery to preserve audit changes, got %+v", changes)
	}
}

type largeCheckpointWriteTool struct {
	size int
}

func (largeCheckpointWriteTool) Name() string { return "LargeCheckpointWrite" }
func (largeCheckpointWriteTool) Description() string {
	return "Writes a test file beyond the checkpoint fingerprint budget."
}
func (largeCheckpointWriteTool) Schema() any { return struct{}{} }
func (largeCheckpointWriteTool) Risk(json.RawMessage) tools.Risk {
	return tools.RiskWrite
}
func (tool largeCheckpointWriteTool) Execute(_ context.Context, _ tools.Call, env tools.Env) (tools.Result, error) {
	path := filepath.Join(env.CWD, "large.bin")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", tool.size)), 0o644); err != nil {
		return tools.Result{Output: err.Error(), IsError: true}, nil
	}
	return tools.Result{Output: "large test file written"}, nil
}

func TestRunnerInvalidatesLargeFileCheckpointWithoutBlockingRun(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main", "--template="}, {"config", "user.name", "Autoto Test"}, {"config", "user.email", "test@example.com"}} {
		if _, err := runCheckpointGit(ctx, repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeTestFile(repo, "tracked.txt", "base\n"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "tracked.txt"}, {"commit", "-m", "initial"}} {
		if _, err := runCheckpointGit(ctx, repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	store, agent := newAgentTestStore(t, repo, "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "write a large file"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "large-write", Name: "LargeCheckpointWrite", Input: json.RawMessage(`{}`)}}, {Type: "done", Done: true}},
		{{Type: "text", Text: "done"}, {Type: "done", Done: true}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 3})
	runner.tools.Register(largeCheckpointWriteTool{size: int(gitCheckpointMaxFileBytes) + 1})
	// This test is about the checkpoint budget; disable reflection so the
	// scripted provider does not need a reflection turn and the test does not
	// hang waiting for approval.
	disableReflectionForTest(t, store)
	runner.Run(ctx, agent.ID)

	updated, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "idle" {
		t.Fatalf("checkpoint budget must not block the run, got agent status %q", updated.Status)
	}
	runs, err := store.ListRuns(ctx, agent.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].CheckpointState != db.RunCheckpointInvalid || !strings.Contains(runs[0].CheckpointError, "file") || !strings.Contains(runs[0].CheckpointError, "budget") {
		t.Fatalf("expected invalid large-file checkpoint, got %+v", runs)
	}
}

func TestCheckpointSnapshotBoundsFailClosed(t *testing.T) {
	var buffer checkpointLimitedBuffer
	buffer.max = 4
	if _, err := buffer.Write([]byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	if !buffer.truncated || buffer.String() != "abcd" {
		t.Fatalf("expected bounded checkpoint output, got truncated=%v output=%q", buffer.truncated, buffer.String())
	}
	entries, err := checkpointStatusEntries(strings.Repeat("?? owned.txt\x00", gitCheckpointMaxPaths+1))
	if err == nil || entries != nil || !strings.Contains(err.Error(), "path count") {
		t.Fatalf("expected checkpoint path limit error, entries=%+v err=%v", entries, err)
	}
}
