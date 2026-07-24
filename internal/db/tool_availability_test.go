package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func openToolAvailabilityTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "tools.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestToolAvailabilityInheritanceAndDisabledShadow(t *testing.T) {
	ctx := context.Background()
	store := openToolAvailabilityTestStore(t)
	global := ToolAvailabilityTarget{Scope: ToolAvailabilityScopeGlobal}
	project := ToolAvailabilityTarget{Scope: ToolAvailabilityScopeProject, ProjectID: "project-1"}
	workspace := ToolAvailabilityTarget{Scope: ToolAvailabilityScopeWorkspace, ProjectID: "project-1", WorkspaceID: "workspace-1"}

	decision, err := store.ResolveToolAvailability(ctx, workspace, "bash")
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Enabled || !decision.Default {
		t.Fatalf("no rule must default allow, got %+v", decision)
	}

	globalRule, err := store.SetToolAvailabilityRuleCAS(ctx, global, "bash", ToolAvailabilityDisabled, 0, "test")
	if err != nil {
		t.Fatal(err)
	}
	projectRule, err := store.SetToolAvailabilityRuleCAS(ctx, project, "bash", ToolAvailabilityEnabled, 0, "test")
	if err != nil {
		t.Fatal(err)
	}
	workspaceRule, err := store.SetToolAvailabilityRuleCAS(ctx, workspace, "bash", ToolAvailabilityDisabled, 0, "test")
	if err != nil {
		t.Fatal(err)
	}

	decision, err = store.ResolveToolAvailability(ctx, workspace, "bash")
	if err != nil {
		t.Fatal(err)
	}
	if decision.Enabled || decision.SourceRuleID != workspaceRule.ID || decision.SourceScope != ToolAvailabilityScopeWorkspace {
		t.Fatalf("workspace disabled rule must shadow enabled project rule, got %+v", decision)
	}

	deleted, err := store.DeleteToolAvailabilityRuleCAS(ctx, workspaceRule.ID, workspaceRule.Revision, "test")
	if err != nil {
		t.Fatal(err)
	}
	decision, err = store.ResolveToolAvailability(ctx, workspace, "bash")
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Enabled || decision.SourceRuleID != projectRule.ID {
		t.Fatalf("soft delete must restore project inheritance, got %+v", decision)
	}

	if _, err := store.DeleteToolAvailabilityRuleCAS(ctx, projectRule.ID, projectRule.Revision, "test"); err != nil {
		t.Fatal(err)
	}
	decision, err = store.ResolveToolAvailability(ctx, workspace, "bash")
	if err != nil {
		t.Fatal(err)
	}
	if decision.Enabled || decision.SourceRuleID != globalRule.ID {
		t.Fatalf("deleted project rule must reveal global disabled rule, got %+v", decision)
	}

	restored, err := store.SetToolAvailabilityRuleCAS(ctx, workspace, "bash", ToolAvailabilityEnabled, deleted.Revision, "test")
	if err != nil {
		t.Fatal(err)
	}
	if restored.DeletedAt != "" || restored.Revision != deleted.Revision+1 {
		t.Fatalf("expected deleted logical rule to restore with a new revision, got %+v", restored)
	}
}

func TestToolAvailabilityCASRevisionsAndOrphans(t *testing.T) {
	ctx := context.Background()
	store := openToolAvailabilityTestStore(t)
	target := ToolAvailabilityTarget{Scope: ToolAvailabilityScopeProject, ProjectID: "project-1"}

	rule, err := store.SetToolAvailabilityRuleCAS(ctx, target, "removed_plugin__tool", ToolAvailabilityDisabled, 0, "api_request")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetToolAvailabilityRuleCAS(ctx, target, rule.ToolName, ToolAvailabilityEnabled, 0, "stale"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected stale create revision conflict, got %v", err)
	}
	updated, err := store.SetToolAvailabilityRuleCAS(ctx, target, rule.ToolName, ToolAvailabilityEnabled, rule.Revision, "api_request")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteToolAvailabilityRuleCAS(ctx, rule.ID, rule.Revision, "stale"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected stale delete revision conflict, got %v", err)
	}

	rules, err := store.ListToolAvailabilityRules(ctx, target, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].ToolName != "removed_plugin__tool" {
		t.Fatalf("rules for tools absent from a catalog must remain listable, got %+v", rules)
	}
	revisions, err := store.ListToolAvailabilityRevisions(ctx, updated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 2 || revisions[0].Revision != 2 || revisions[0].Operation != "update" || revisions[1].Operation != "create" {
		t.Fatalf("unexpected immutable revisions: %+v", revisions)
	}
}

func TestToolAvailabilityStrictTargetValidation(t *testing.T) {
	ctx := context.Background()
	store := openToolAvailabilityTestStore(t)
	invalid := []ToolAvailabilityTarget{
		{Scope: ToolAvailabilityScopeGlobal, ProjectID: "p"},
		{Scope: ToolAvailabilityScopeProject},
		{Scope: ToolAvailabilityScopeProject, ProjectID: "p", WorkspaceID: "w"},
		{Scope: ToolAvailabilityScopeWorkspace, ProjectID: "p"},
		{Scope: "agent", ProjectID: "p"},
	}
	for _, target := range invalid {
		if _, err := store.SetToolAvailabilityRuleCAS(ctx, target, "read", ToolAvailabilityEnabled, 0, "test"); err == nil {
			t.Fatalf("expected target %+v to fail validation", target)
		}
	}
	if _, err := store.SetToolAvailabilityRuleCAS(ctx, ToolAvailabilityTarget{}, "bad tool", ToolAvailabilityEnabled, 0, "test"); err == nil {
		t.Fatal("expected whitespace in tool name to fail validation")
	}
}
