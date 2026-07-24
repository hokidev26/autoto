package db

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"autoto/internal/hooks"
)

func lifecycleDBTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "lifecycle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func lifecycleDBHook() hooks.Hook {
	return hooks.Hook{Name: "Project audit", Enabled: true, Event: hooks.EventToolAfter, Scope: hooks.Scope{Kind: hooks.ScopeProject, ID: "project-1"}, Priority: 50, Mode: hooks.ModeAsync, FailurePolicy: hooks.FailureRetry, Action: hooks.Action{Kind: hooks.ActionHTTP, HTTP: &hooks.HTTPAction{URL: "https://hooks.example.test/events", Method: "POST", SecretRefs: map[string]string{"Authorization": "env:LIFECYCLE_TOKEN"}}}}
}

func TestLifecycleHookStoreCRUDCASAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hooks.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateLifecycleHook(ctx, lifecycleDBHook())
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Revision != 1 || created.CreatedAt == "" {
		t.Fatalf("created=%+v", created)
	}
	listed, err := store.ListLifecycleHooks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("listed=%+v", listed)
	}
	update := created
	update.Name = "Updated audit"
	update.Priority = 75
	updated, err := store.UpdateLifecycleHookCAS(ctx, created.ID, created.Revision, update)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.Name != "Updated audit" || updated.Priority != 75 {
		t.Fatalf("updated=%+v", updated)
	}
	if _, err := store.UpdateLifecycleHookCAS(ctx, created.ID, 1, update); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update err=%v", err)
	}
	if err := store.DeleteLifecycleHookCAS(ctx, created.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale delete err=%v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.GetLifecycleHook(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Revision != 2 || persisted.Action.HTTP.SecretRefs["Authorization"] != "env:LIFECYCLE_TOKEN" {
		t.Fatalf("persisted=%+v", persisted)
	}
	if err := reopened.DeleteLifecycleHookCAS(ctx, created.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.GetLifecycleHook(ctx, created.ID); !IsNotFound(err) {
		t.Fatalf("get deleted err=%v", err)
	}
}

func TestLifecycleHookSchemaConstraintsAndSecretRefs(t *testing.T) {
	ctx := context.Background()
	store := lifecycleDBTestStore(t)
	if err := store.EnsureLifecycleHookSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"lifecycle_hooks", "lifecycle_hook_run_bindings", "lifecycle_hook_events", "lifecycle_hook_executions", "lifecycle_hook_attempts"} {
		if !testTableExists(t, ctx, store.DB(), table) {
			t.Fatalf("missing table %s", table)
		}
	}
	invalid := lifecycleDBHook()
	invalid.Action.HTTP.SecretRefs = map[string]string{"Authorization": "raw-secret"}
	if _, err := store.CreateLifecycleHook(ctx, invalid); err == nil {
		t.Fatal("plaintext secret reference was stored")
	}
	invalid = lifecycleDBHook()
	invalid.Action.HTTP.URL = "https://user:pass@example.test/hook"
	if _, err := store.CreateLifecycleHook(ctx, invalid); err == nil {
		t.Fatal("URL userinfo was stored")
	}
}

func TestLifecycleHookRunBindingEventExecutionAttemptStates(t *testing.T) {
	ctx := context.Background()
	store := lifecycleDBTestStore(t)
	hook, err := store.CreateLifecycleHook(ctx, lifecycleDBHook())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := hooks.NewSnapshot([]hooks.Hook{hook}, time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.CreateLifecycleHookRunBinding(ctx, "run-1", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if binding.Status != hooks.BindingActive || binding.Snapshot.Digest != snapshot.Digest {
		t.Fatalf("binding=%+v", binding)
	}
	if _, err := store.CreateLifecycleHookRunBinding(ctx, "run-1", snapshot); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate binding err=%v", err)
	}
	event, err := store.CreateLifecycleHookEvent(ctx, LifecycleHookEvent{BindingID: binding.ID, RunID: "run-1", Name: hooks.EventToolAfter, Payload: json.RawMessage(`{"toolName":"read"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if event.Status != hooks.EventPending {
		t.Fatalf("event=%+v", event)
	}
	execution, err := store.CreateLifecycleHookExecution(ctx, LifecycleHookExecution{EventID: event.ID, HookID: hook.ID, HookRevision: hook.Revision, Mode: hook.Mode, FailurePolicy: hook.FailurePolicy})
	if err != nil {
		t.Fatal(err)
	}
	running, err := store.TransitionLifecycleHookExecution(ctx, execution.ID, hooks.ExecutionRunning, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if running.StartedAt == "" {
		t.Fatal("missing startedAt")
	}
	attempt, err := store.CreateLifecycleHookAttempt(ctx, LifecycleHookAttempt{ExecutionID: execution.ID, AttemptNumber: 1, Request: json.RawMessage(`{"authorization":"Bearer private","safe":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(attempt.Request), "private") {
		t.Fatalf("attempt secret leaked: %s", attempt.Request)
	}
	attempt, err = store.CompleteLifecycleHookAttempt(ctx, attempt.ID, hooks.AttemptFailed, json.RawMessage(`{"token":"private-response"}`), "Authorization: Bearer private-error")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(attempt.Response), "private-response") || strings.Contains(attempt.Error, "private-error") {
		t.Fatalf("completed attempt leaked: %+v", attempt)
	}
	failed, err := store.TransitionLifecycleHookExecution(ctx, execution.ID, hooks.ExecutionFailed, json.RawMessage(`{"secret":"hidden"}`), "token=hidden-error")
	if err != nil {
		t.Fatal(err)
	}
	if failed.CompletedAt == "" || strings.Contains(string(failed.Result), "hidden") || strings.Contains(failed.Error, "hidden-error") {
		t.Fatalf("failed=%+v", failed)
	}
	retry, err := store.RetryLifecycleHookExecution(ctx, failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Status != hooks.ExecutionPending || retry.RetryOfExecutionID != failed.ID {
		t.Fatalf("retry=%+v", retry)
	}
	cancelled, err := store.CancelLifecycleHookExecution(ctx, retry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != hooks.ExecutionCancelled || !cancelled.CancelRequested {
		t.Fatalf("cancelled=%+v", cancelled)
	}
	secondRetry, err := store.RetryLifecycleHookExecution(ctx, cancelled.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondRetry, err = store.TransitionLifecycleHookExecution(ctx, secondRetry.ID, hooks.ExecutionRunning, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	cancelRequested, err := store.CancelLifecycleHookExecution(ctx, secondRetry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelRequested.Status != hooks.ExecutionRunning || !cancelRequested.CancelRequested {
		t.Fatalf("running cancel=%+v", cancelRequested)
	}
	history, err := store.ListLifecycleHookExecutions(ctx, hook.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("history=%+v", history)
	}
	attempts, err := store.ListLifecycleHookAttempts(ctx, execution.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Status != hooks.AttemptFailed {
		t.Fatalf("attempts=%+v", attempts)
	}
	closed, err := store.CloseLifecycleHookRunBinding(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != hooks.BindingClosed {
		t.Fatalf("closed=%+v", closed)
	}
}
