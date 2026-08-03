package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"autoto/internal/db"
)

func TestValidateBackgroundTaskAllowsOrdinaryTaskOutsideGitWorkspace(t *testing.T) {
	ctx := context.Background()
	store, owner := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()

	runner := &Runner{store: store}
	if err := runner.ValidateBackgroundTask(ctx, db.BackgroundTask{OwnerAgentID: owner.ID, Kind: db.BackgroundTaskKindAgent}); err != nil {
		t.Fatalf("ordinary background task should not require a Git snapshot: %v", err)
	}
}

func TestValidateBackgroundTaskRevalidatesToolCatalogWithoutGitSnapshot(t *testing.T) {
	ctx := context.Background()
	store, owner := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	generations, err := store.GetPermissionGenerations(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}

	runner := &Runner{store: store}
	runner.SetBackgroundTaskSnapshotProvider(func(context.Context, string) (db.PlanSnapshot, error) {
		return db.PlanSnapshot{
			PolicyGenerationSnapshot: generations.Policy,
			AgentGenerationSnapshot:  generations.Entity,
			ToolCatalogDigest:        "tools-current",
		}, nil
	})
	task := db.BackgroundTask{
		OwnerAgentID:             owner.ID,
		Kind:                     db.BackgroundTaskKindAgent,
		PolicyGenerationSnapshot: generations.Policy,
		AgentGenerationSnapshot:  generations.Entity,
		ToolCatalogDigest:        "tools-current",
	}
	if err := runner.ValidateBackgroundTask(ctx, task); err != nil {
		t.Fatalf("matching non-Git runtime snapshot should pass: %v", err)
	}
	task.ToolCatalogDigest = "tools-stale"
	if err := runner.ValidateBackgroundTask(ctx, task); err == nil || !strings.Contains(err.Error(), "tool catalog is stale") {
		t.Fatalf("stale non-Git tool catalog was not rejected: %v", err)
	}
}

func TestValidateBackgroundTaskKeepsFingerprintStrictAndLabelsWorkdirFailures(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store, owner := newAgentTestStore(t, workspace, "acceptEdits")
	defer store.Close()

	runner := &Runner{store: store}
	runner.SetPlanSnapshotProvider(func(context.Context, string) (db.PlanSnapshot, error) {
		return db.PlanSnapshot{PolicyGenerationSnapshot: 1, AgentGenerationSnapshot: 1, ToolCatalogDigest: "tools", WorkspaceFingerprint: "current"}, nil
	})
	matching := db.BackgroundTask{OwnerAgentID: owner.ID, Kind: db.BackgroundTaskKindAgent, WorkspaceFingerprint: "current", PolicyGenerationSnapshot: 1, AgentGenerationSnapshot: 1, ToolCatalogDigest: "tools"}
	if err := runner.ValidateBackgroundTask(ctx, matching); err != nil {
		t.Fatalf("matching workspace fingerprint should pass: %v", err)
	}
	mismatched := matching
	mismatched.WorkspaceFingerprint = "stale"
	if err := runner.ValidateBackgroundTask(ctx, mismatched); err == nil || !strings.Contains(err.Error(), "workspace changed") {
		t.Fatalf("stale workspace fingerprint was not rejected: %v", err)
	}

	payload, err := json.Marshal(map[string]string{"workdir": filepath.Join(workspace, "..")})
	if err != nil {
		t.Fatal(err)
	}
	invalidWorkdir := db.BackgroundTask{OwnerAgentID: owner.ID, Kind: db.BackgroundTaskKindAgent, PayloadJSON: payload}
	err = runner.ValidateBackgroundTask(ctx, invalidWorkdir)
	if err == nil {
		t.Fatalf("workdir escape was not rejected: %v", err)
	}
	coded, ok := err.(interface{ ErrorCode() string })
	if !ok || coded.ErrorCode() != "workdir_rejected" {
		t.Fatalf("workdir rejection lost structured code: %T %v", err, err)
	}
}
