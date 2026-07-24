package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

type runtimeProbeTool struct {
	calls atomic.Int64
}

func (*runtimeProbeTool) Name() string                    { return "RuntimeProbe" }
func (*runtimeProbeTool) Description() string             { return "Probe run-scoped tool snapshots." }
func (*runtimeProbeTool) Schema() any                     { return struct{}{} }
func (*runtimeProbeTool) Risk(json.RawMessage) tools.Risk { return tools.RiskRead }
func (tool *runtimeProbeTool) Execute(context.Context, tools.Call, tools.Env) (tools.Result, error) {
	tool.calls.Add(1)
	return tools.Result{Output: "ok"}, nil
}

func TestRuntimeToolAvailabilityDefaultsAllowAndScopesShadow(t *testing.T) {
	ctx := context.Background()
	store, createdAgent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	probe := &runtimeProbeTool{}
	registry := tools.NewRegistry()
	registry.Register(probe)
	runner := NewRunner(store, providers.NewRegistry(), registry, NewHub(), config.AgentConfig{})
	scope, err := runner.agentRuntimeScope(ctx, createdAgent)
	if err != nil {
		t.Fatal(err)
	}
	resolution := tools.ResolutionContext{AgentID: createdAgent.ID, CWD: createdAgent.CWD}

	snapshot, err := runner.snapshotTools(ctx, resolution)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.tools[probe.Name()] == nil {
		t.Fatal("tool without an availability rule must remain available by default")
	}
	global := db.ToolAvailabilityTarget{Scope: db.ToolAvailabilityScopeGlobal}
	if _, err := store.SetToolAvailabilityRuleCAS(ctx, global, probe.Name(), db.ToolAvailabilityEnabled, 0, "test"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = runner.snapshotTools(ctx, resolution)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.tools[probe.Name()] == nil {
		t.Fatal("global enabled rule was not applied")
	}
	project := db.ToolAvailabilityTarget{Scope: db.ToolAvailabilityScopeProject, ProjectID: scope.ProjectID}
	if _, err := store.SetToolAvailabilityRuleCAS(ctx, project, probe.Name(), db.ToolAvailabilityDisabled, 0, "test"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = runner.snapshotTools(ctx, resolution)
	if err != nil {
		t.Fatal(err)
	}
	if _, exposed := snapshot.tools[probe.Name()]; exposed {
		t.Fatal("project disabled rule did not shadow global enable")
	}
	workspace := db.ToolAvailabilityTarget{Scope: db.ToolAvailabilityScopeWorkspace, ProjectID: scope.ProjectID, WorkspaceID: scope.WorkspaceID}
	workspaceRule, err := store.SetToolAvailabilityRuleCAS(ctx, workspace, probe.Name(), db.ToolAvailabilityEnabled, 0, "test")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = runner.snapshotTools(ctx, resolution)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.tools[probe.Name()] == nil {
		t.Fatal("workspace enabled rule did not shadow project disable")
	}

	run, err := store.CreateRun(ctx, db.Run{AgentID: createdAgent.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runner.prepareRunRuntimeSnapshot(ctx, createdAgent.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetToolAvailabilityRuleCAS(ctx, workspace, probe.Name(), db.ToolAvailabilityDisabled, workspaceRule.Revision, "test"); err != nil {
		t.Fatal(err)
	}
	result, err := runner.ExecuteToolForRun(ctx, createdAgent.ID, run.ID, tools.Call{ID: "frozen-probe", Name: probe.Name(), Input: json.RawMessage(`{}`)})
	if err != nil || result.IsError {
		t.Fatalf("frozen run snapshot did not retain the authorized tool: result=%+v err=%v", result, err)
	}
	if probe.calls.Load() != 1 {
		t.Fatalf("probe executions = %d, want 1", probe.calls.Load())
	}
	freshRun, err := store.CreateRun(ctx, db.Run{AgentID: createdAgent.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.ExecuteToolForRun(ctx, createdAgent.ID, freshRun.ID, tools.Call{ID: "disabled-probe", Name: probe.Name(), Input: json.RawMessage(`{}`)}); err == nil || !strings.Contains(err.Error(), "tool not found") {
		t.Fatalf("disabled tool remained executable in a fresh run: %v", err)
	}
}

func TestCustomChildRoleCannotExpandParentToolSnapshot(t *testing.T) {
	ctx := context.Background()
	store, createdAgent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	probe := &runtimeProbeTool{}
	registry := tools.NewRegistry()
	registry.Register(probe)
	runner := NewRunner(store, providers.NewRegistry(), registry, NewHub(), config.AgentConfig{})
	if _, err := store.SetToolAvailabilityRuleCAS(ctx, db.ToolAvailabilityTarget{Scope: db.ToolAvailabilityScopeGlobal}, probe.Name(), db.ToolAvailabilityEnabled, 0, "test"); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, db.Run{AgentID: createdAgent.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runner.prepareRunRuntimeSnapshot(ctx, createdAgent.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	definition := json.RawMessage(`{"version":1,"key":"unsafe","displayName":"Unsafe","baseRole":"general","toolAllowlist":["Write"]}`)
	if _, err := store.CreateAgentRoleDefinition(ctx, db.AgentRoleDefinitionInput{
		Scope:          db.DefinitionScopeTarget{Scope: db.DefinitionScopeGlobal},
		Key:            "unsafe",
		DisplayName:    "Unsafe",
		DefinitionJSON: definition,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.ResolveChildRole(ctx, createdAgent.ID, run.ID, "unsafe"); err == nil || !strings.Contains(err.Error(), "unavailable parent tool") {
		t.Fatalf("custom role expanded beyond parent snapshot: %v", err)
	}
}

func TestEffectivePromptPreviewKeepsGlobalUserOutOfSystem(t *testing.T) {
	ctx := context.Background()
	store, createdAgent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	const marker = "GLOBAL USER MUST STAY UNTRUSTED"
	if _, err := store.CreatePromptDefinition(ctx, db.PromptDefinitionInput{
		Scope:       db.DefinitionScopeTarget{Scope: db.DefinitionScopeGlobal},
		Key:         "global-user",
		DisplayName: "Global user",
		Layer:       db.PromptLayerGlobalUser,
		Content:     marker,
	}); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(store, providers.NewRegistry(), tools.NewRegistry(), NewHub(), config.AgentConfig{})
	preview, err := runner.EffectivePromptSnapshot(ctx, createdAgent.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundUser := false
	for _, layer := range preview.Layers {
		if layer.Immutable && layer.ContentPreview != "" {
			t.Fatalf("immutable prompt layer %q exposed hidden content", layer.Name)
		}
		if layer.Bytes > 0 && layer.Digest == "" {
			t.Fatalf("prompt layer %q omitted safe digest metadata", layer.Name)
		}
		if layer.Role == "system" && strings.Contains(layer.ContentPreview, marker) {
			t.Fatalf("global_user content leaked into system layer %q", layer.Name)
		}
		if layer.Name == "global_user" && layer.Role == "user" && strings.Contains(layer.ContentPreview, marker) {
			foundUser = true
		}
	}
	if !foundUser {
		t.Fatal("global_user layer was not exposed as untrusted user context")
	}
}
