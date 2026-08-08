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

// writeAskTool is write-risk on purpose. Exec risk would be denied earlier by
// ExecCapabilityDenied for a child agent, which would hide the branch under
// test; write risk reaches the approval decision with the child policy intact.
type writeAskTool struct{}

func (writeAskTool) Name() string        { return "ChildWriteProbe" }
func (writeAskTool) Description() string { return "write-risk probe for child approval policy" }
func (writeAskTool) Schema() any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (writeAskTool) Risk(json.RawMessage) tools.Risk { return tools.RiskWrite }
func (writeAskTool) Execute(context.Context, tools.Call, tools.Env) (tools.Result, error) {
	return tools.Result{Output: "executed"}, nil
}

func newChildAgentForApprovalTest(t *testing.T, store *db.Store, parent db.Agent, mode string) db.Agent {
	t.Helper()
	child, err := store.CreateAgent(context.Background(), db.Agent{
		WorklineID: parent.WorklineID, ParentAgentID: parent.ID, Type: "subagent", SubagentType: "general",
		Title: "Probe child", Model: "fake:test", PermissionMode: mode, Status: "idle", CWD: parent.CWD,
	})
	if err != nil {
		t.Fatal(err)
	}
	return child
}

// A subagent's Run is submitted internally, so nobody is watching its agentID
// for an approval prompt. Waiting only spends toolApprovalTimeout to arrive at
// the same deny, which is what made dispatched tasks look like they hung. The
// deny has to arrive immediately and carry the reason, so the subagent can read
// why and report back instead of stalling.
func TestUnattendedChildAskIsDeniedImmediatelyWithReason(t *testing.T) {
	ctx := context.Background()
	store, parent := newAgentTestStore(t, t.TempDir(), "bypassPermissions")
	defer store.Close()
	child := newChildAgentForApprovalTest(t, store, parent, "bypassPermissions")

	if _, err := store.UpdateWorkflowPreferences(ctx, db.WorkflowPreferences{
		RequireConfirmationForExec: true, RequireConfirmationForWrites: true, AllowReadOnlyByDefault: true,
		DangerReflectionLevel: "off",
	}); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, db.Run{AgentID: child.ID, Status: "running", Source: "internal", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}

	probe := writeAskTool{}
	enableAgentTestTool(t, store, probe.Name())
	registry := tools.NewRegistry()
	registry.Register(probe)
	runner := NewRunner(store, providers.NewRegistry(), registry, NewHub(), config.AgentConfig{})
	if err := runner.RegisterChildRuntimeProfile(child.ID, ChildRoleResolution{BaseRole: "general", AllowedTools: []string{probe.Name()}}); err != nil {
		t.Fatal(err)
	}

	call := tools.Call{ID: "child-write-1", Name: probe.Name(), Input: json.RawMessage(`{}`)}
	// The whole point is that this returns without an approver. A generous
	// bound still fails long before toolApprovalTimeout, so a regression that
	// reinstates the wait shows up as a timeout rather than a hang.
	done := make(chan tools.Result, 1)
	errCh := make(chan error, 1)
	go func() {
		result, execErr := runner.executeToolForLoop(ctx, child.ID, run.ID, call, "")
		done <- result
		errCh <- execErr
	}()

	select {
	case result := <-done:
		if execErr := <-errCh; execErr != nil {
			t.Fatalf("unattended child deny returned an error: %v", execErr)
		}
		if !result.IsError {
			t.Fatalf("expected an error result for the denied call, got %+v", result)
		}
		if strings.TrimSpace(result.Output) == "" {
			t.Fatal("the deny must carry a reason the subagent can read")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("unattended child call waited for an approval nobody can give")
	}

	stored, err := store.GetToolCallByUseID(ctx, child.ID, call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "denied" {
		t.Fatalf("expected the call to be recorded as denied, got %q", stored.Status)
	}
	// "pending_approval" is what waitForToolApproval writes first. Its absence
	// is the evidence that the wait path was never entered.
	runner.approvalMu.Lock()
	pending := runner.approvals[approvalKey(child.ID, call.ID)]
	runner.approvalMu.Unlock()
	if pending != nil {
		t.Fatal("no approval should have been registered for an unattended child call")
	}
}

// The immediate deny is scoped to child agents on purpose. A schedule dispatch
// is also unattended, but its approval prompt is answerable from Telegram
// (/approve, /deny), so denying it locally would remove a working remote
// approval path rather than fix a stall.
func TestUnattendedScheduleRunStillRequestsApproval(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()

	if _, err := store.UpdateWorkflowPreferences(ctx, db.WorkflowPreferences{
		RequireConfirmationForExec: true, RequireConfirmationForWrites: true, AllowReadOnlyByDefault: true,
		DangerReflectionLevel: "off",
	}); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "running", Source: "schedule", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}

	probe := writeAskTool{}
	enableAgentTestTool(t, store, probe.Name())
	registry := tools.NewRegistry()
	registry.Register(probe)
	runner := NewRunner(store, providers.NewRegistry(), registry, NewHub(), config.AgentConfig{})

	call := tools.Call{ID: "schedule-write-1", Name: probe.Name(), Input: json.RawMessage(`{}`)}
	resultCh := make(chan tools.Result, 1)
	go func() {
		result, _ := runner.executeToolForLoop(ctx, agent.ID, run.ID, call, "")
		resultCh <- result
	}()

	// The approval must actually be published, which is what the Telegram
	// control plane answers against.
	waitForPendingApproval(t, runner, agent.ID, call.ID)
	if accepted, err := runner.ApproveToolCall(ctx, agent.ID, call.ID, ToolApprovalDecision{Decision: "allow_once", Reason: "approved remotely", DecidedBy: "telegram"}); err != nil || !accepted {
		t.Fatalf("remote approval was refused: accepted=%v err=%v", accepted, err)
	}

	select {
	case result := <-resultCh:
		if result.IsError {
			t.Fatalf("remotely approved schedule call must execute, got %+v", result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("schedule call did not finish after its approval")
	}
}

// The reflection verdict is the only gate left for a bypassPermissions
// subagent, so when it withholds approval the subagent has to receive the
// reflection's own wording rather than a bare policy string.
func TestUnattendedChildDenyCarriesReflectionReason(t *testing.T) {
	ctx := context.Background()
	store, parent := newAgentTestStore(t, t.TempDir(), "bypassPermissions")
	defer store.Close()
	child := newChildAgentForApprovalTest(t, store, parent, "bypassPermissions")

	provider := &scriptedProvider{turns: [][]providers.Event{
		reflectionToolEvents(reflectionToolConfirm, "Touches a shared credential store."),
	}}
	registry := providers.NewRegistry()
	registry.Register(provider)
	probe := writeAskTool{}
	enableAgentTestTool(t, store, probe.Name())
	toolRegistry := tools.NewRegistry()
	toolRegistry.Register(probe)
	runner := NewRunner(store, registry, toolRegistry, NewHub(), config.AgentConfig{MaxTurns: 3, SummaryModel: "fake:summary"})
	if err := runner.RegisterChildRuntimeProfile(child.ID, ChildRoleResolution{BaseRole: "general", AllowedTools: []string{probe.Name()}}); err != nil {
		t.Fatal(err)
	}

	run, err := store.CreateRun(ctx, db.Run{AgentID: child.ID, Status: "running", Source: "internal", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	call := tools.Call{ID: "child-reflected-1", Name: probe.Name(), Input: json.RawMessage(`{}`)}

	done := make(chan tools.Result, 1)
	go func() {
		result, _ := runner.executeToolForLoop(ctx, child.ID, run.ID, call, "")
		done <- result
	}()

	select {
	case result := <-done:
		if !result.IsError {
			t.Fatalf("a confirm verdict must not execute for an unattended child, got %+v", result)
		}
		if !strings.Contains(result.Output, "credential") {
			t.Fatalf("the deny did not carry the reflection's reason: %q", result.Output)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reflected unattended child call waited for an approval nobody can give")
	}
}

// The dispatch path is what the user actually hits: a parent on
// bypassPermissions creates a child that inherits bypass, then submits the
// child's Run with that cap. The submission and DB guards each independently
// rejected the value, so the task failed with child_run_submit_failed before
// the child ever ran. Inheriting bypass is only useful if it survives the whole
// chain, so this asserts the submission succeeds and the cap is what persists.
func TestInternalRunAcceptsInheritedBypassCap(t *testing.T) {
	ctx := context.Background()
	store, parent := newAgentTestStore(t, t.TempDir(), "bypassPermissions")
	defer store.Close()
	child := newChildAgentForApprovalTest(t, store, parent, "bypassPermissions")
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{MaxTurns: 3})

	run, err := runner.SubmitInternal(ctx, child.ID, "task-bypass-1", "do the work", "bypassPermissions")
	if err != nil {
		t.Fatalf("an inherited bypass cap must be submittable: %v", err)
	}
	if run.PermissionModeCap != "bypassPermissions" {
		t.Fatalf("the run must persist the inherited cap, got %q", run.PermissionModeCap)
	}
	// The cap is the ceiling actually applied at tool time. If it silently
	// narrowed here, the subagent would be back to asking for approval.
	if got := permissionModeWithCap(child.PermissionMode, run.PermissionModeCap); got != "bypassPermissions" {
		t.Fatalf("the applied mode narrowed to %q despite an inherited bypass cap", got)
	}
	waitForRunSettled(t, store, runner, child.ID, run.ID)
}

// Schedules must not gain bypass from this relaxation. Their own validation
// already restricts them to readOnly/acceptEdits, and the submission guard
// stays strict for that source as a second line.
func TestScheduleSourceStillRejectsBypassCap(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "bypassPermissions")
	defer store.Close()
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{MaxTurns: 3})

	_, err := runner.SubmitSource(ctx, SourceSubmission{
		AgentID: agent.ID, Prompt: "nightly", Source: "schedule", SourceID: "sched-1",
		DispatchID: "dispatch-1", TriggerType: "scheduled", PermissionModeCap: "bypassPermissions",
	})
	if err == nil {
		t.Fatal("a schedule dispatch must not be allowed to run under a bypass cap")
	}
	if !strings.Contains(err.Error(), "readOnly or acceptEdits") {
		t.Fatalf("unexpected rejection reason: %v", err)
	}
}
