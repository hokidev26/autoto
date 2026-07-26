package agent

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/tools"
)

func reviewTestRunner(t *testing.T, mode string) (*Runner, db.Agent) {
	t.Helper()
	store, agent := newAgentTestStore(t, t.TempDir(), mode)
	t.Cleanup(func() { store.Close() })
	// No summary model: this exercises the static review gate on its own, with
	// danger reflection inert.
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{MaxTurns: 3})
	return runner, agent
}

func bashInput(command string) json.RawMessage {
	input, _ := json.Marshal(map[string]string{"command": command})
	return json.RawMessage(input)
}

func platformCommands() (hardDanger, sensitive, unclassified, safe string) {
	if runtime.GOOS == "windows" {
		return `del /f /s /q C:\work\*`, "git push --force origin main", `%COMSPEC% /c echo hi`, "go test ./..."
	}
	return "rm -rf /", "git push --force origin main", `X=rm; $X -rf /tmp/x`, "go test ./..."
}

// TestBypassPermissionsStillReviewsSensitiveCommands is the regression test for
// the core policy hole: bypassPermissions used to allow every exec-risk call
// unconditionally, so anything the danger tier failed to hard-block ran with no
// prompt at all. Serious-but-recoverable and unclassifiable commands must now
// always reach a human, while ordinary work stays friction-free.
func TestBypassPermissionsStillReviewsSensitiveCommands(t *testing.T) {
	runner, agent := reviewTestRunner(t, "bypassPermissions")
	hardDanger, sensitive, unclassified, safe := platformCommands()
	ctx := context.Background()

	t.Run("sensitive requires approval", func(t *testing.T) {
		resolution := runner.resolveToolPermission(ctx, agent.ID, "bypassPermissions", "Bash", tools.RiskExec, bashInput(sensitive))
		if resolution.Decision != toolPermissionAsk {
			t.Fatalf("expected %q to require approval in bypassPermissions, got %+v", sensitive, resolution)
		}
		if resolution.Warning == "" {
			t.Fatalf("expected an explanatory warning, got %+v", resolution)
		}
	})

	t.Run("unclassified requires approval", func(t *testing.T) {
		resolution := runner.resolveToolPermission(ctx, agent.ID, "bypassPermissions", "Bash", tools.RiskExec, bashInput(unclassified))
		if resolution.Decision != toolPermissionAsk {
			t.Fatalf("expected unclassified %q to require approval, got %+v", unclassified, resolution)
		}
	})

	t.Run("hard danger stays blocked", func(t *testing.T) {
		risk := (tools.BashTool{}).Risk(bashInput(hardDanger))
		if risk != tools.RiskDanger {
			t.Fatalf("expected %q to classify as danger, got %s", hardDanger, risk)
		}
		resolution := runner.resolveToolPermission(ctx, agent.ID, "bypassPermissions", "Bash", risk, bashInput(hardDanger))
		if resolution.Decision != toolPermissionDeny || resolution.Source != decisionSourceHardDangerBlock {
			t.Fatalf("expected %q to be hard-blocked, got %+v", hardDanger, resolution)
		}
	})

	t.Run("ordinary work stays allowed", func(t *testing.T) {
		resolution := runner.resolveToolPermission(ctx, agent.ID, "bypassPermissions", "Bash", tools.RiskExec, bashInput(safe))
		if resolution.Decision != toolPermissionAllow {
			t.Fatalf("expected %q to stay allowed without friction, got %+v", safe, resolution)
		}
	})
}

// TestWildcardAllowRuleCannotSkipCommandReview verifies the review gate outranks
// stored permission rules, so a broad convenience rule cannot silently
// re-enable the behavior the gate exists to prevent.
func TestWildcardAllowRuleCannotSkipCommandReview(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	// The store already refuses wildcard-risk allow rules, so this is the
	// broadest allow a user can actually create for shell execution.
	if _, err := store.CreateToolPermissionRule(ctx, db.ToolPermissionRule{Mode: "*", ToolName: "Bash", Risk: "exec", Decision: "allow", Priority: 100, Enabled: true, Description: "allow all bash exec"}); err != nil {
		t.Fatal(err)
	}
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{MaxTurns: 3})
	_, sensitive, unclassified, safe := platformCommands()

	for _, command := range []string{sensitive, unclassified} {
		resolution := runner.resolveToolPermission(ctx, agent.ID, "acceptEdits", "Bash", tools.RiskExec, bashInput(command))
		if resolution.Decision != toolPermissionAsk {
			t.Fatalf("wildcard allow rule must not skip review for %q, got %+v", command, resolution)
		}
	}
	// The rule must still work for commands that do not need review.
	resolution := runner.resolveToolPermission(ctx, agent.ID, "acceptEdits", "Bash", tools.RiskExec, bashInput(safe))
	if resolution.Decision != toolPermissionAllow {
		t.Fatalf("wildcard allow rule should still allow %q, got %+v", safe, resolution)
	}
}

// TestExecConfirmationDisabledStillReviewsSensitiveCommands covers the other
// silent-execution path: turning off exec confirmation in workflow preferences.
func TestExecConfirmationDisabledStillReviewsSensitiveCommands(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "default")
	defer store.Close()
	if _, err := store.UpdateWorkflowPreferences(ctx, db.WorkflowPreferences{RequireConfirmationForExec: false, RequireConfirmationForWrites: false, AllowReadOnlyByDefault: true}); err != nil {
		t.Fatal(err)
	}
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{MaxTurns: 3})
	_, sensitive, _, safe := platformCommands()

	resolution := runner.resolveToolPermission(ctx, agent.ID, "default", "Bash", tools.RiskExec, bashInput(sensitive))
	if resolution.Decision != toolPermissionAsk {
		t.Fatalf("disabling exec confirmation must not skip review for %q, got %+v", sensitive, resolution)
	}
	resolution = runner.resolveToolPermission(ctx, agent.ID, "default", "Bash", tools.RiskExec, bashInput(safe))
	if resolution.Decision != toolPermissionAllow {
		t.Fatalf("expected %q to remain allowed, got %+v", safe, resolution)
	}
}

// TestWhitelistedExecWorksOnEveryPlatform guards the usability half of the fix.
// The allowlist previously required POSIX command facts, so on Windows it never
// matched and even `go test` prompted, pushing users toward bypassPermissions.
func TestWhitelistedExecWorksOnEveryPlatform(t *testing.T) {
	for _, command := range []string{"go test ./...", "go vet ./internal/...", "go build ./...", "npm test", "npm run lint", "git status --short", "git diff --stat"} {
		if !isWhitelistedExecCommand(command) {
			t.Errorf("expected %q to be auto-approvable on %s", command, runtime.GOOS)
		}
	}
	for _, command := range []string{"go test ./... | cat", "git status > out.txt", "rm -rf /", "git push --force origin main", "npm install left-pad"} {
		if isWhitelistedExecCommand(command) {
			t.Errorf("expected %q NOT to be auto-approvable on %s", command, runtime.GOOS)
		}
	}
}
