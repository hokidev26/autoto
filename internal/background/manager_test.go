package background

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"autoto/internal/db"
	"autoto/internal/tools"
)

func TestManagerLifecycleWaitOutputAndTerminalHook(t *testing.T) {
	ctx := context.Background()
	store, agent := testStoreAndAgent(t)
	defer store.Close()
	manager := NewManager(store, Options{WorkerCount: 2, PerAgentLimit: 1, PollInterval: 5 * time.Millisecond})
	if manager.options.OutputChunkBytes != db.BackgroundTaskOutputChunkBytes || manager.options.OutputLimitBytes != db.BackgroundTaskDefaultOutputMax {
		t.Fatalf("unexpected output defaults: %+v", manager.options)
	}
	if err := manager.RegisterExecutor(db.BackgroundTaskKindAgent, ExecutorFunc(func(ctx context.Context, task db.BackgroundTask, output OutputWriter) (Result, error) {
		if err := output.Write("stdout", []byte("started\n")); err != nil {
			return Result{}, err
		}
		return Result{JSON: json.RawMessage(`{"ok":true}`)}, nil
	})); err != nil {
		t.Fatal(err)
	}
	hooked := make(chan db.BackgroundTask, 1)
	manager.SetTerminalHook(func(_ context.Context, task db.BackgroundTask) { hooked <- task })
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	task, err := manager.Submit(ctx, db.BackgroundTask{OwnerAgentID: agent.ID, Kind: db.BackgroundTaskKindAgent, PayloadJSON: json.RawMessage(`{"prompt":"private"}`)})
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	completed, err := manager.Wait(waitCtx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != db.BackgroundTaskStatusSucceeded || string(completed.ResultJSON) != `{"ok":true}` {
		t.Fatalf("unexpected completed task: %+v", completed)
	}
	page, err := manager.ListOutput(ctx, task.ID, 0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || string(page.Items[0].Chunk) != "started\n" {
		t.Fatalf("unexpected output: %+v", page)
	}
	select {
	case terminal := <-hooked:
		if terminal.ID != task.ID || !terminal.Terminal() {
			t.Fatalf("unexpected terminal hook task: %+v", terminal)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal hook was not called")
	}
	if err := manager.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(ctx); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestManagerRuntimeSettingsExpandConcurrencyWithoutInterruptingTasks(t *testing.T) {
	ctx := context.Background()
	store, firstAgent := testStoreAndAgent(t)
	defer store.Close()
	secondAgent, err := store.CreateAgent(ctx, db.Agent{
		WorklineID: firstAgent.WorklineID, Type: "primary", Title: "Second", Model: firstAgent.Model,
		PermissionMode: "acceptEdits", ExecutionDeviceID: "local", Status: "idle", CWD: firstAgent.CWD,
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, Options{WorkerCount: 1, PerAgentLimit: 1, PollInterval: 5 * time.Millisecond})
	started := make(chan db.BackgroundTask, 3)
	release := make(chan struct{}, 3)
	if err := manager.RegisterExecutor(db.BackgroundTaskKindAgent, ExecutorFunc(func(_ context.Context, task db.BackgroundTask, _ OutputWriter) (Result, error) {
		started <- task
		<-release
		return Result{}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	for _, owner := range []string{firstAgent.ID, firstAgent.ID, secondAgent.ID} {
		if _, err := manager.Submit(ctx, db.BackgroundTask{OwnerAgentID: owner, Kind: db.BackgroundTaskKindAgent}); err != nil {
			t.Fatal(err)
		}
	}
	first := <-started
	if first.OwnerAgentID != firstAgent.ID {
		t.Fatalf("unexpected first owner: %+v", first)
	}
	select {
	case task := <-started:
		t.Fatalf("worker limit was not enforced before update: %+v", task)
	case <-time.After(50 * time.Millisecond):
	}
	applied, err := manager.UpdateBackgroundRuntimeSettings(tools.BackgroundRuntimeSettings{WorkerCount: 2, PerAgentLimit: 1, AllowNestedSubagents: true, MaxSubagentDepth: 3})
	if err != nil {
		t.Fatal(err)
	}
	if applied.WorkerCount != 2 || applied.PerAgentLimit != 1 || !applied.AllowNestedSubagents || applied.MaxSubagentDepth != 3 {
		t.Fatalf("unexpected applied settings: %+v", applied)
	}
	select {
	case second := <-started:
		if second.OwnerAgentID != secondAgent.ID {
			t.Fatalf("per-agent limit did not skip the busy owner: %+v", second)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("increased worker count did not start another owner")
	}
	if _, err := manager.UpdateBackgroundRuntimeSettings(tools.BackgroundRuntimeSettings{WorkerCount: 3, PerAgentLimit: 2, MaxSubagentDepth: 2}); err != nil {
		t.Fatal(err)
	}
	select {
	case third := <-started:
		if third.OwnerAgentID != firstAgent.ID {
			t.Fatalf("expanded per-agent limit started the wrong task: %+v", third)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expanded per-agent limit did not release the queued task")
	}
	release <- struct{}{}
	release <- struct{}{}
	release <- struct{}{}
	if _, err := manager.UpdateBackgroundRuntimeSettings(tools.BackgroundRuntimeSettings{WorkerCount: 0, PerAgentLimit: 1, MaxSubagentDepth: 2}); err == nil {
		t.Fatal("invalid worker count was accepted")
	}
}

func TestManagerRuntimeSettingsDecreaseQueuesNewWorkWithoutInterruptingRunningTasks(t *testing.T) {
	ctx := context.Background()
	store, agent := testStoreAndAgent(t)
	defer store.Close()
	manager := NewManager(store, Options{WorkerCount: 3, PerAgentLimit: 3, PollInterval: 5 * time.Millisecond})
	started := make(chan string, 4)
	release := make(chan struct{}, 4)
	interrupted := make(chan string, 4)
	if err := manager.RegisterExecutor(db.BackgroundTaskKindAgent, ExecutorFunc(func(ctx context.Context, task db.BackgroundTask, _ OutputWriter) (Result, error) {
		started <- task.ID
		select {
		case <-release:
			return Result{}, nil
		case <-ctx.Done():
			interrupted <- task.ID
			return Result{}, ctx.Err()
		}
	})); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())

	tasks := make([]db.BackgroundTask, 0, 4)
	for range 4 {
		task, err := manager.Submit(ctx, db.BackgroundTask{OwnerAgentID: agent.ID, Kind: db.BackgroundTaskKindAgent})
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	for range 3 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("initial workers did not start")
		}
	}
	if _, err := manager.UpdateBackgroundRuntimeSettings(tools.BackgroundRuntimeSettings{WorkerCount: 1, PerAgentLimit: 1, MaxSubagentDepth: 2}); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-interrupted:
		t.Fatalf("lowering limits interrupted running task %s", id)
	case id := <-started:
		t.Fatalf("queued task %s started above lowered limits", id)
	case <-time.After(75 * time.Millisecond):
	}

	for remaining := 2; remaining >= 0; remaining-- {
		release <- struct{}{}
		if remaining > 0 {
			select {
			case id := <-started:
				t.Fatalf("queued task %s started while %d task(s) still occupied the lowered limit", id, remaining)
			case id := <-interrupted:
				t.Fatalf("running task %s was interrupted after lowering limits", id)
			case <-time.After(75 * time.Millisecond):
			}
		}
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("queued task did not start after running count fell below the lowered limit")
	}
	release <- struct{}{}
	for _, task := range tasks {
		waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		completed, err := manager.Wait(waitCtx, task.ID)
		cancel()
		if err != nil || completed.Status != db.BackgroundTaskStatusSucceeded {
			t.Fatalf("task %s did not complete successfully: status=%s err=%v", task.ID, completed.Status, err)
		}
	}
}

func TestManagerRevalidatesSafetyBeforeExecution(t *testing.T) {
	ctx := context.Background()
	store, agent := testStoreAndAgent(t)
	defer store.Close()
	manager := NewManager(store, Options{WorkerCount: 1, PollInterval: 5 * time.Millisecond})
	executed := make(chan struct{}, 1)
	if err := manager.RegisterExecutor(db.BackgroundTaskKindAgent, ExecutorFunc(func(context.Context, db.BackgroundTask, OutputWriter) (Result, error) {
		executed <- struct{}{}
		return Result{}, nil
	})); err != nil {
		t.Fatal(err)
	}
	manager.SetValidator(func(context.Context, db.BackgroundTask) error {
		return errors.New("policy snapshot changed")
	})
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	task, err := manager.Submit(ctx, db.BackgroundTask{OwnerAgentID: agent.ID, Kind: db.BackgroundTaskKindAgent})
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	failed, err := manager.Wait(waitCtx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != db.BackgroundTaskStatusFailed || failed.ErrorCode != "safety_snapshot_invalid" {
		t.Fatalf("unexpected validation failure: %+v", failed)
	}
	select {
	case <-executed:
		t.Fatal("executor ran after safety validation failed")
	default:
	}
}

func TestManagerCancelRunningTask(t *testing.T) {
	ctx := context.Background()
	store, agent := testStoreAndAgent(t)
	defer store.Close()
	manager := NewManager(store, Options{WorkerCount: 1, PollInterval: 5 * time.Millisecond})
	started := make(chan struct{})
	if err := manager.RegisterExecutor(db.BackgroundTaskKindAgent, ExecutorFunc(func(ctx context.Context, task db.BackgroundTask, output OutputWriter) (Result, error) {
		close(started)
		<-ctx.Done()
		return Result{}, ctx.Err()
	})); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	task, err := manager.Submit(ctx, db.BackgroundTask{OwnerAgentID: agent.ID, Kind: db.BackgroundTaskKindAgent})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("executor did not start")
	}
	requested, err := manager.Cancel(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if requested.Status != db.BackgroundTaskStatusCancelRequested {
		t.Fatalf("cancel request status = %s", requested.Status)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	canceled, err := manager.Wait(waitCtx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if canceled.Status != db.BackgroundTaskStatusCanceled || canceled.ErrorCode != "canceled" {
		t.Fatalf("unexpected canceled task: %+v", canceled)
	}
}

func TestManagerStartReconcilesOldRunningTasks(t *testing.T) {
	ctx := context.Background()
	store, agent := testStoreAndAgent(t)
	defer store.Close()
	task, err := store.CreateBackgroundTask(ctx, db.BackgroundTask{OwnerAgentID: agent.ID, Kind: db.BackgroundTaskKindAgent})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimQueuedBackgroundTask(ctx, db.BackgroundTaskClaimOptions{WorkerInstanceID: "old-worker"}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, Options{WorkerCount: 1, WorkerInstanceID: "new-worker", PollInterval: 5 * time.Millisecond})
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	reconciled, err := manager.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Status != db.BackgroundTaskStatusInterrupted || reconciled.ErrorCode != "process_restarted" {
		t.Fatalf("unexpected reconciled task: %+v", reconciled)
	}
}

func TestManagerRejectsDuplicateExecutorsAndClosedSubmit(t *testing.T) {
	store, _ := testStoreAndAgent(t)
	defer store.Close()
	manager := NewManager(store, Options{})
	executor := ExecutorFunc(func(context.Context, db.BackgroundTask, OutputWriter) (Result, error) { return Result{}, nil })
	if err := manager.RegisterExecutor("agent", executor); err != nil {
		t.Fatal(err)
	}
	if err := manager.RegisterExecutor("agent", executor); !errors.Is(err, ErrExecutorExists) {
		t.Fatalf("duplicate executor error = %v", err)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit(context.Background(), db.BackgroundTask{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed submit error = %v", err)
	}
}

func testStoreAndAgent(t *testing.T) (*db.Store, db.Agent) {
	t.Helper()
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "background.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, agent, err := store.CreateProject(ctx, "Background", "", t.TempDir(), "fake:model", "acceptEdits")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, agent
}
