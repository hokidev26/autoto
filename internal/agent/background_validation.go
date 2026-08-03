package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"autoto/internal/db"
	"autoto/internal/tools"
)

type backgroundValidationError struct {
	code string
	err  error
}

func (e *backgroundValidationError) Error() string {
	if e == nil || e.err == nil {
		return "background task validation failed"
	}
	return e.err.Error()
}

func (e *backgroundValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *backgroundValidationError) ErrorCode() string {
	if e == nil || strings.TrimSpace(e.code) == "" {
		return "safety_snapshot_invalid"
	}
	return e.code
}

func backgroundValidationFailure(code string, err error) error {
	return &backgroundValidationError{code: strings.TrimSpace(code), err: err}
}

// ValidateBackgroundTask revalidates the durable permission and workspace
// boundary immediately before a queued task starts. A background task may
// outlive its parent Run, but it must never outlive the policy approval or
// workspace snapshot under which it was created.
func (r *Runner) ValidateBackgroundTask(ctx context.Context, task db.BackgroundTask) error {
	if r == nil || r.store == nil {
		return errors.New("agent runner is unavailable")
	}
	agent, err := r.store.GetAgent(ctx, strings.TrimSpace(task.OwnerAgentID))
	if err != nil {
		return fmt.Errorf("load background task owner: %w", err)
	}
	generations, err := r.store.GetPermissionGenerations(ctx, agent.ID)
	if err != nil {
		return fmt.Errorf("load background task generations: %w", err)
	}
	if task.PermissionGenerationSnapshot > 0 && generations.Permission != task.PermissionGenerationSnapshot {
		return errors.New("background task permission generation changed")
	}
	if task.PolicyGenerationSnapshot > 0 && generations.Policy != task.PolicyGenerationSnapshot {
		return errors.New("background task policy generation changed")
	}
	if task.AgentGenerationSnapshot > 0 && generations.Entity != task.AgentGenerationSnapshot {
		return errors.New("background task agent generation changed")
	}

	if parentRunID := strings.TrimSpace(task.ParentRunID); parentRunID != "" {
		run, err := r.store.GetRunByID(ctx, parentRunID)
		if err != nil {
			return fmt.Errorf("load background task parent run: %w", err)
		}
		if run.AgentID != agent.ID {
			return errors.New("background task parent run owner changed")
		}
		if task.PolicyGenerationSnapshot == 0 || task.AgentGenerationSnapshot == 0 {
			return errors.New("background task generation snapshot is missing")
		}
		if run.PolicyGenerationSnapshot != task.PolicyGenerationSnapshot || run.AgentGenerationSnapshot != task.AgentGenerationSnapshot {
			return errors.New("background task parent run snapshot changed")
		}
		if strings.TrimSpace(run.ToolCatalogDigest) != strings.TrimSpace(task.ToolCatalogDigest) || strings.TrimSpace(run.WorkspaceFingerprint) != strings.TrimSpace(task.WorkspaceFingerprint) {
			return errors.New("background task parent tool or workspace snapshot changed")
		}
		if strings.TrimSpace(run.PermissionModeCap) != strings.TrimSpace(task.PermissionModeCap) {
			return errors.New("background task permission cap changed")
		}
	}

	// The plan snapshot provider intentionally remains Git-strict. Tasks bound
	// to a workspace fingerprint use it unchanged. Ordinary non-Git tasks use a
	// separate runtime snapshot so a frozen tool catalog is still revalidated
	// without importing the plan approval requirement that CWD itself be a repo.
	fingerprint := strings.TrimSpace(task.WorkspaceFingerprint)
	digest := strings.TrimSpace(task.ToolCatalogDigest)
	if fingerprint != "" {
		if snapshot, configured, err := r.currentPlanSnapshot(ctx, agent.ID); err != nil {
			return fmt.Errorf("load background task workspace snapshot: %w", err)
		} else if !configured {
			return errors.New("background task workspace snapshot is unavailable")
		} else {
			if task.PolicyGenerationSnapshot > 0 && snapshot.PolicyGenerationSnapshot != task.PolicyGenerationSnapshot {
				return errors.New("background task policy snapshot is stale")
			}
			if task.AgentGenerationSnapshot > 0 && snapshot.AgentGenerationSnapshot != task.AgentGenerationSnapshot {
				return errors.New("background task agent snapshot is stale")
			}
			if digest != "" && snapshot.ToolCatalogDigest != digest {
				return errors.New("background task tool catalog is stale")
			}
			if snapshot.WorkspaceFingerprint != fingerprint {
				return errors.New("background task workspace changed")
			}
		}
	} else if digest != "" {
		if snapshot, configured, err := r.currentBackgroundTaskSnapshot(ctx, agent.ID); err != nil {
			return fmt.Errorf("load background task runtime snapshot: %w", err)
		} else if !configured {
			return errors.New("background task runtime snapshot is unavailable")
		} else {
			if task.PolicyGenerationSnapshot > 0 && snapshot.PolicyGenerationSnapshot != task.PolicyGenerationSnapshot {
				return errors.New("background task policy snapshot is stale")
			}
			if task.AgentGenerationSnapshot > 0 && snapshot.AgentGenerationSnapshot != task.AgentGenerationSnapshot {
				return errors.New("background task agent snapshot is stale")
			}
			if snapshot.ToolCatalogDigest != digest {
				return errors.New("background task tool catalog is stale")
			}
		}
	}

	var scope struct {
		CWD     string `json:"cwd"`
		Workdir string `json:"workdir"`
	}
	if len(task.PayloadJSON) > 0 && json.Unmarshal(task.PayloadJSON, &scope) == nil {
		requested := strings.TrimSpace(scope.Workdir)
		if requested == "" {
			requested = strings.TrimSpace(scope.CWD)
		}
		if requested != "" {
			resolved, err := tools.ResolveWorkdirWithin(agent.CWD, requested)
			if err != nil {
				return backgroundValidationFailure("workdir_rejected", fmt.Errorf("background task workdir rejected: %w", err))
			}
			parent, err := tools.ResolveWorkdirWithin(agent.CWD, "")
			if err != nil || resolved == "" || parent == "" {
				return errors.New("background task parent workspace is unavailable")
			}
		}
	}
	return nil
}
