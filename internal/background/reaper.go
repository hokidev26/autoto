package background

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"autoto/internal/db"
)

// DefaultAbandonedTaskIdleTimeout is how long a claimed task may sit with no
// executor and no change of its own before it is reaped. It only has to outlast
// the moment between claiming a task and registering it as active, because the
// active map is what actually decides whether an executor still owns it.
const DefaultAbandonedTaskIdleTimeout = 5 * time.Minute

// ReapAbandonedTasks ends tasks this instance claimed but is no longer running.
// ReconcileBackgroundTasksAfterRestart only covers a process restart, so an
// executor goroutine that disappeared inside a long-lived process left its task
// at "running" for good. Nothing then reached a terminal state, so notifyTerminal
// never ran -- and a parent parked on that task's resumeParent boundary waited
// for a wake-up that could no longer come.
func (manager *Manager) ReapAbandonedTasks(ctx context.Context, minIdle time.Duration) (int, error) {
	if manager == nil || manager.store == nil {
		return 0, nil
	}
	manager.mu.Lock()
	state := manager.state
	manager.mu.Unlock()
	if state != managerRunning {
		return 0, nil
	}
	if minIdle <= 0 {
		minIdle = DefaultAbandonedTaskIdleTimeout
	}
	tasks, err := manager.store.ListBackgroundTasks(ctx, db.BackgroundTaskListOptions{
		Statuses: []string{db.BackgroundTaskStatusRunning, db.BackgroundTaskStatusCancelRequested},
		Limit:    db.BackgroundTaskMaxListLimit,
	})
	if err != nil {
		return 0, err
	}
	reaped := 0
	for _, task := range tasks {
		if err := ctx.Err(); err != nil {
			return reaped, err
		}
		// Only this instance's claims. Another instance's tasks are its own to
		// reap, and its restart reconciliation already covers them.
		if task.WorkerInstanceID != manager.options.WorkerInstanceID {
			continue
		}
		if !manager.taskIsAbandoned(task, minIdle) {
			continue
		}
		updated, err := manager.store.TransitionBackgroundTask(ctx, task.ID, db.BackgroundTaskTransition{
			ExpectedRevision: task.Revision,
			FromStatuses:     []string{db.BackgroundTaskStatusRunning, db.BackgroundTaskStatusCancelRequested},
			Status:           db.BackgroundTaskStatusInterrupted,
			ResultJSON:       json.RawMessage(`{}`),
			ErrorCode:        "worker_lost",
			ErrorMessage:     "background task worker stopped without reporting a result",
		})
		if err != nil {
			slog.Warn("reaping an abandoned background task failed", "taskId", task.ID, "ownerAgentId", task.OwnerAgentID, "error", err)
			continue
		}
		reaped++
		slog.Warn("reaped an abandoned background task", "taskId", updated.ID, "ownerAgentId", updated.OwnerAgentID, "kind", updated.Kind, "updatedAt", task.UpdatedAt)
		manager.notifyTerminal(updated)
	}
	return reaped, nil
}

func (manager *Manager) taskIsAbandoned(task db.BackgroundTask, minIdle time.Duration) bool {
	manager.mu.Lock()
	_, active := manager.active[task.ID]
	manager.mu.Unlock()
	if active {
		return false
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(task.UpdatedAt))
	if err != nil {
		// An unparseable timestamp cannot establish staleness, and guessing would
		// end a task whose executor may be about to register itself.
		return false
	}
	return time.Since(updatedAt) >= minIdle
}
