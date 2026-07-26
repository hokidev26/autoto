package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/tools"
)

func TestManagedAutomationMCPExecCannotBypassOneTimeApproval(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "bypassPermissions")
	defer store.Close()
	if _, err := store.CreateToolPermissionRule(ctx, db.ToolPermissionRule{
		Mode: "*", ToolName: "MCPCallTool", Risk: "exec", Decision: "allow", Priority: 100, Enabled: true,
		Description: "ordinary allow rule must not bypass managed automation approval",
	}); err != nil {
		t.Fatal(err)
	}
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{})
	input := json.RawMessage(`{"serverId":"optional-automation-playwright-mcp","toolName":"browser_click","arguments":{"element":"Submit"}}`)
	risk := (tools.MCPCallToolTool{}).Risk(input)
	if risk != tools.RiskExec {
		t.Fatalf("side-effecting managed call risk=%s", risk)
	}
	for _, mode := range []string{"bypassPermissions", "acceptEdits", "default", "dontAsk"} {
		resolution := runner.resolveToolPermission(ctx, agent.ID, mode, "MCPCallTool", risk, input)
		if resolution.Decision != toolPermissionAsk || resolution.Scope != "once" || !strings.Contains(resolution.Reason, "one-time") {
			t.Fatalf("mode %s bypassed managed approval: %+v", mode, resolution)
		}
	}
	if key := sessionGrantKey("MCPCallTool", input); key != "" {
		t.Fatalf("managed side effect received reusable session key %q", key)
	}
	if toolAllowsSessionApproval(tools.MCPCallToolTool{}, input) {
		t.Fatal("managed side effect exposed session approval")
	}

	if _, err := store.CreateToolPermissionRule(ctx, db.ToolPermissionRule{
		Mode: "*", ToolName: "MCPCallTool", Risk: "exec", Decision: "deny", Priority: 200, Enabled: true,
		Description: "administrator deny must remain stronger than approval",
	}); err != nil {
		t.Fatal(err)
	}
	resolution := runner.resolveToolPermission(ctx, agent.ID, "bypassPermissions", "MCPCallTool", risk, input)
	if resolution.Decision != toolPermissionDeny || !strings.Contains(resolution.Reason, "administrator deny") {
		t.Fatalf("managed policy ignored explicit deny rule: %+v", resolution)
	}
}

func TestManagedAutomationMCPReadOnlySeparatesObservationFromSideEffects(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "readOnly")
	defer store.Close()
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{})
	serverID := tools.ManagedAutomationMCPServerID("chrome-devtools-mcp")

	listInput := json.RawMessage(`{"serverId":"` + serverID + `"}`)
	listRisk := (tools.MCPListToolsTool{}).Risk(listInput)
	listResolution := runner.resolveToolPermission(ctx, agent.ID, agent.PermissionMode, "MCPListTools", listRisk, listInput)
	if listRisk != tools.RiskRead || listResolution.Decision != toolPermissionAllow {
		t.Fatalf("managed discovery should remain read-only: risk=%s resolution=%+v", listRisk, listResolution)
	}

	observationInput := json.RawMessage(`{"serverId":"` + serverID + `","toolName":"take_snapshot"}`)
	observationRisk := (tools.MCPCallToolTool{}).Risk(observationInput)
	observationResolution := runner.resolveToolPermission(ctx, agent.ID, agent.PermissionMode, "MCPCallTool", observationRisk, observationInput)
	if observationRisk != tools.RiskRead || observationResolution.Decision != toolPermissionAllow {
		t.Fatalf("managed observation should remain read-only: risk=%s resolution=%+v", observationRisk, observationResolution)
	}

	sideEffectInput := json.RawMessage(`{"serverId":"` + serverID + `","toolName":"click"}`)
	sideEffectRisk := (tools.MCPCallToolTool{}).Risk(sideEffectInput)
	sideEffectResolution := runner.resolveToolPermission(ctx, agent.ID, agent.PermissionMode, "MCPCallTool", sideEffectRisk, sideEffectInput)
	if sideEffectRisk != tools.RiskExec || sideEffectResolution.Decision != toolPermissionDeny || !strings.Contains(sideEffectResolution.Reason, "readOnly") {
		t.Fatalf("readOnly mode did not hard-deny side effect: risk=%s resolution=%+v", sideEffectRisk, sideEffectResolution)
	}
}

func TestManagedAutomationMCPPendingApprovalRejectsSessionDecision(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{})
	input := json.RawMessage(`{"serverId":"optional-automation-playwright-mcp","toolName":"browser_type","arguments":{"text":"secret"}}`)
	call := tools.Call{ID: "managed-mcp-once", Name: "MCPCallTool", Input: input}
	risk := (tools.MCPCallToolTool{}).Risk(input)
	resolution := runner.resolveToolPermission(ctx, agent.ID, agent.PermissionMode, call.Name, risk, input)
	allowSession := toolAllowsSessionApproval(tools.MCPCallToolTool{}, input)
	if resolution.Decision != toolPermissionAsk || allowSession {
		t.Fatalf("unexpected managed approval setup: resolution=%+v allowSession=%v", resolution, allowSession)
	}

	decisionCh := make(chan ToolApprovalDecision, 1)
	errorCh := make(chan error, 1)
	go func() {
		decision, err := runner.waitForToolApproval(ctx, agent, "", call, risk, "", resolution, allowSession)
		if err != nil {
			errorCh <- err
			return
		}
		decisionCh <- decision
	}()
	waitForPendingApproval(t, runner, agent.ID, call.ID)

	accepted, err := runner.ApproveToolCall(ctx, agent.ID, call.ID, ToolApprovalDecision{Decision: "allow_session", Reason: "must be rejected", DecidedBy: "test"})
	if err == nil || accepted || !strings.Contains(err.Error(), "allow_once") {
		t.Fatalf("managed approval accepted a session grant: accepted=%v err=%v", accepted, err)
	}
	accepted, err = runner.ApproveToolCall(ctx, agent.ID, call.ID, ToolApprovalDecision{Decision: "deny", Reason: "finish test", DecidedBy: "test"})
	if err != nil || !accepted {
		t.Fatalf("failed to resolve pending approval after session rejection: accepted=%v err=%v", accepted, err)
	}
	select {
	case err := <-errorCh:
		t.Fatal(err)
	case decision := <-decisionCh:
		if decision.Decision != "deny" {
			t.Fatalf("unexpected final decision: %+v", decision)
		}
	}
}

func TestOrdinaryMCPPermissionBehaviorRemainsCompatible(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.CreateToolPermissionRule(ctx, db.ToolPermissionRule{
		Mode: "acceptEdits", ToolName: "MCPCallTool", Risk: "exec", Decision: "allow", Priority: 100, Enabled: true,
		Description: "ordinary MCP allow",
	}); err != nil {
		t.Fatal(err)
	}
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{})
	input := json.RawMessage(`{"serverId":"user-mcp","toolName":"echo","arguments":{"value":"ok"}}`)
	risk := (tools.MCPCallToolTool{}).Risk(input)
	resolution := runner.resolveToolPermission(ctx, agent.ID, agent.PermissionMode, "MCPCallTool", risk, input)
	if risk != tools.RiskExec || resolution.Decision != toolPermissionAllow || !strings.Contains(resolution.Reason, "ordinary MCP allow") {
		t.Fatalf("ordinary MCP allow rule changed: risk=%s resolution=%+v", risk, resolution)
	}
	if !toolAllowsSessionApproval(tools.MCPCallToolTool{}, input) || sessionGrantKey("MCPCallTool", input) == "" {
		t.Fatal("ordinary MCP calls unexpectedly lost session approval compatibility")
	}
}
