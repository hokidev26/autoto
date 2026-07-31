package agent

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"autoto/internal/db"
	"autoto/internal/tools"
)

// bypassPermissions stops the static review gate from prompting, which is what
// its label promises. The safety property that makes that acceptable is that the
// gate moves rather than disappears: because the permission now resolves to
// allow, danger reflection becomes reachable for exactly these commands and can
// still refuse them.
//
// This test fails if either half regresses — a static prompt coming back, or
// reflection being skipped so nothing reviews the action at all.
func TestBypassPermissionsHandsRecoverableCommandsToReflection(t *testing.T) {
	sensitive := "git push --force origin main"
	catastrophic := "somecmd --wipe-home"
	ctx := context.Background()

	t.Run("static gate resolves to allow so reflection can run", func(t *testing.T) {
		runner, agent, _ := reflectionRunner(t, "bypassPermissions")
		resolution := runner.resolveToolPermission(ctx, agent.ID, "bypassPermissions", "Bash", tools.RiskExec, bashInput(sensitive))
		if resolution.Decision != toolPermissionAllow {
			t.Fatalf("expected allow so reflectBeforeExecution is reachable, got %+v", resolution)
		}
	})

	t.Run("reflection still blocks catastrophic actions under bypass", func(t *testing.T) {
		runner, agent, _ := reflectionRunner(t, "bypassPermissions",
			`{"verdict":"block","severity":"critical","reason":"This wipes the user's home directory.","alternative":"Delete a specific project path instead."}`)
		allowed := runner.resolveToolPermission(ctx, agent.ID, "bypassPermissions", "Bash", tools.RiskExec, bashInput(catastrophic))
		if allowed.Decision != toolPermissionAllow {
			t.Fatalf("precondition: static policy should allow before reflection, got %+v", allowed)
		}
		reflected := runner.reflectBeforeExecution(ctx, agent, "bypassPermissions", "run-1", bashCall(catastrophic), tools.RiskExec, allowed)
		if reflected.Decision != toolPermissionDeny || reflected.Source != decisionSourceDangerReflection {
			t.Fatalf("reflection must still be able to refuse under bypassPermissions, got %+v", reflected)
		}
	})

	t.Run("reflection lets an ordinary helper script through", func(t *testing.T) {
		script := "powershell -File tools/read.ps1 -Start 1 -End 40"
		if runtime.GOOS != "windows" {
			script = "bash tools/read.sh --start 1 --end 40"
		}
		runner, agent, _ := reflectionRunner(t, "bypassPermissions",
			`{"verdict":"proceed","severity":"low","reason":"This reads a range of lines from a file."}`)
		allowed := runner.resolveToolPermission(ctx, agent.ID, "bypassPermissions", "Bash", tools.RiskExec, bashInput(script))
		reflected := runner.reflectBeforeExecution(ctx, agent, "bypassPermissions", "run-1", bashCall(script), tools.RiskExec, allowed)
		if reflected.Decision != toolPermissionAllow {
			t.Fatalf("a plain helper script must not prompt in bypassPermissions, got %+v", reflected)
		}
	})
}

// Inside an agent loop nobody can answer a prompt, so the denied tool result is
// the model's only feedback channel. A reflection "confirm" verdict must carry
// its reason and safer alternative through, otherwise the model just retries the
// same escalated action.
func TestAgentLoopSurfacesReflectionReasonToTheModel(t *testing.T) {
	runner, agent, _ := reflectionRunner(t, "bypassPermissions",
		`{"verdict":"confirm","severity":"high","reason":"This force-pushes over a shared branch.","alternative":"Push to a new branch instead."}`)
	run, err := runner.store.CreateRun(context.Background(), db.Run{AgentID: agent.ID, Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	call := bashCall("git push --force origin main")
	// ExecuteToolForRun is the no-human path used by subagents, lifecycle hooks,
	// and dynamic tools. executeToolForLoop would block on a human approval that
	// nobody is there to give.
	result, err := runner.ExecuteToolForRun(context.Background(), agent.ID, run.ID, call)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("escalated call must come back as an error result, got %+v", result)
	}
	for _, want := range []string{"force-pushes over a shared branch", "Safer alternative", "new branch"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("model feedback lost %q: %s", want, result.Output)
		}
	}
}
