package background

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"autoto/internal/agentrole"
	"autoto/internal/db"
	"autoto/internal/tools"
)

func TestAgentRoleContractAcceptsScopedPresetKeyForRuntimeResolution(t *testing.T) {
	payload, err := parseAgentPayload(json.RawMessage(`{"prompt":"inspect the repository","subagentType":"review.safe"}`))
	if err != nil {
		t.Fatal(err)
	}
	if payload.SubagentType != "review.safe" {
		t.Fatalf("preset key = %q, want review.safe", payload.SubagentType)
	}
	if _, err := parseAgentPayload(json.RawMessage(`{"prompt":"inspect the repository","subagentType":"bad role!"}`)); err == nil {
		t.Fatal("invalid preset key was accepted")
	}
}

func TestAgentRoleContractPreservesCanonicalRolesAndResolverCompatibility(t *testing.T) {
	tests := []struct {
		input    string
		wantRole agentrole.Role
		resolver string
	}{
		{input: "general", wantRole: agentrole.RoleGeneral, resolver: "general"},
		{input: "executor", wantRole: agentrole.RoleExecutor, resolver: "general"},
		{input: "explorer", wantRole: agentrole.RoleExplorer, resolver: "explore"},
		{input: "reviewer", wantRole: agentrole.RoleReviewer, resolver: "plan"},
		{input: "tester", wantRole: agentrole.RoleTester, resolver: "general"},
		{input: "plan", wantRole: agentrole.RolePlan, resolver: "plan"},
		{input: "search", wantRole: agentrole.RoleSearch, resolver: "search"},
		{input: "background", wantRole: agentrole.RoleGeneral, resolver: "general"},
		{input: "explore", wantRole: agentrole.RoleExplorer, resolver: "explore"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			payload, err := parseAgentPayload(json.RawMessage(`{"prompt":"inspect","subagentType":"` + test.input + `"}`))
			if err != nil {
				t.Fatal(err)
			}
			if payload.SubagentType != string(test.wantRole) {
				t.Fatalf("payload role = %q, want %q", payload.SubagentType, test.wantRole)
			}
			if got := subagentModelRole(test.wantRole); got != test.resolver {
				t.Fatalf("model resolver = %q, want %q", got, test.resolver)
			}
		})
	}
}

func TestAgentRoleContractPublicResultExposesCountNotCriteria(t *testing.T) {
	const secretCriterion = "PRIVATE_ACCEPTANCE_SENTINEL"
	prompt, err := agentPromptWithAcceptance("fixed contract", "inspect", []string{secretCriterion})
	if err != nil || !strings.Contains(prompt, secretCriterion) {
		t.Fatalf("private child prompt did not contain bounded acceptance criterion: prompt=%q err=%v", prompt, err)
	}
	result, err := marshalAgentPublicResult("reviewer", 1, "child-agent", "child-run", "running")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(result), secretCriterion) || strings.Contains(string(result), "acceptanceCriteria") {
		t.Fatalf("public result leaked acceptance criteria: %s", result)
	}
	var projected agentPublicResult
	if err := json.Unmarshal(result, &projected); err != nil {
		t.Fatal(err)
	}
	if projected.Role != "reviewer" || projected.AcceptanceCount != 1 || projected.Status != "running" {
		t.Fatalf("unexpected public result: %+v", projected)
	}
}

func TestAgentRoleContractNestedSubagentPolicyAndDepth(t *testing.T) {
	ctx := context.Background()
	store, root := testStoreAndAgent(t)
	defer store.Close()

	child, err := store.CreateAgent(ctx, db.Agent{
		WorklineID: root.WorklineID, ParentAgentID: root.ID, Type: "subagent", SubagentType: "general",
		Title: "first-level child", Model: root.Model, PermissionMode: "readOnly", Status: "idle", CWD: root.CWD,
	})
	if err != nil {
		t.Fatal(err)
	}
	task := db.BackgroundTask{OwnerAgentID: child.ID, Kind: db.BackgroundTaskKindAgent}
	disabled := tools.BackgroundRuntimeSettings{WorkerCount: 8, PerAgentLimit: 4, MaxSubagentDepth: 2}
	if err := validateAgentTaskScope(store, ctx, task, child, disabled); err == nil || errorCode(err) != "nested_not_enabled" {
		t.Fatalf("disabled nested spawn returned %v", err)
	}
	enabled := tools.BackgroundRuntimeSettings{WorkerCount: 8, PerAgentLimit: 4, AllowNestedSubagents: true, MaxSubagentDepth: 2}
	if err := validateAgentTaskScope(store, ctx, task, child, enabled); err != nil {
		t.Fatalf("enabled first nested level was rejected: %v", err)
	}
	grandchild, err := store.CreateAgent(ctx, db.Agent{
		WorklineID: root.WorklineID, ParentAgentID: child.ID, Type: "subagent", SubagentType: "general",
		Title: "second-level child", Model: root.Model, PermissionMode: "readOnly", Status: "idle", CWD: root.CWD,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAgentTaskScope(store, ctx, db.BackgroundTask{OwnerAgentID: grandchild.ID, Kind: db.BackgroundTaskKindAgent}, grandchild, enabled); err == nil || errorCode(err) != "nested_depth_exceeded" {
		t.Fatalf("depth limit returned %v", err)
	}
}

func TestAgentRoleContractRejectsInvalidNestedAncestry(t *testing.T) {
	ctx := context.Background()
	store, root := testStoreAndAgent(t)
	defer store.Close()
	settings := tools.BackgroundRuntimeSettings{WorkerCount: 8, PerAgentLimit: 4, AllowNestedSubagents: true, MaxSubagentDepth: 4}

	t.Run("cross workline", func(t *testing.T) {
		_, otherWorkline, _, err := store.CreateProject(ctx, "Other", "", t.TempDir(), root.Model, "acceptEdits")
		if err != nil {
			t.Fatal(err)
		}
		child, err := store.CreateAgent(ctx, db.Agent{
			WorklineID: otherWorkline.ID, ParentAgentID: root.ID, Type: "subagent", SubagentType: "general",
			Title: "cross-workline child", Model: root.Model, PermissionMode: "readOnly", Status: "idle", CWD: root.CWD,
		})
		if err != nil {
			t.Fatal(err)
		}
		err = validateAgentTaskNesting(store, ctx, child, settings)
		if err == nil || errorCode(err) != "nested_ancestry_invalid" {
			t.Fatalf("cross-workline ancestry returned %v", err)
		}
	})

	t.Run("cycle", func(t *testing.T) {
		child, err := store.CreateAgent(ctx, db.Agent{
			WorklineID: root.WorklineID, ParentAgentID: root.ID, Type: "subagent", SubagentType: "general",
			Title: "cyclic child", Model: root.Model, PermissionMode: "readOnly", Status: "idle", CWD: root.CWD,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.DB().ExecContext(ctx, `UPDATE agents SET parent_agent_id = ? WHERE id = ?`, child.ID, child.ID); err != nil {
			t.Fatal(err)
		}
		child.ParentAgentID = child.ID
		err = validateAgentTaskNesting(store, ctx, child, settings)
		if err == nil || errorCode(err) != "nested_ancestry_invalid" {
			t.Fatalf("cyclic ancestry returned %v", err)
		}
	})
}

func errorCode(err error) string {
	if coded, ok := err.(interface{ ErrorCode() string }); ok {
		return coded.ErrorCode()
	}
	return ""
}

func TestAgentRoleContractPermissionCapCanOnlyStayEqualOrNarrow(t *testing.T) {
	tests := []struct {
		name      string
		parent    string
		requested string
		want      string
		wantErr   bool
	}{
		{name: "inherit read only", parent: "readOnly", requested: "", want: "readOnly"},
		{name: "retain read only", parent: "readOnly", requested: "readOnly", want: "readOnly"},
		{name: "narrow edit to read only", parent: "acceptEdits", requested: "readOnly", want: "readOnly"},
		{name: "normalize edit aliases", parent: "bypassPermissions", requested: "default", want: "acceptEdits"},
		{name: "reject widening", parent: "readOnly", requested: "acceptEdits", wantErr: true},
		{name: "reject unknown requested mode", parent: "acceptEdits", requested: "root", wantErr: true},
		{name: "reject unknown parent mode", parent: "root", requested: "readOnly", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := childPermissionCap(test.parent, test.requested)
			if test.wantErr {
				if err == nil {
					t.Fatalf("childPermissionCap(%q, %q) = %q, want error", test.parent, test.requested, got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("childPermissionCap(%q, %q) = %q, %v; want %q", test.parent, test.requested, got, err, test.want)
			}
		})
	}
}
