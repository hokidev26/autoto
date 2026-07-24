package db

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestAgentRoleDefinitionsScopedCASDeleteAndRestore(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "roles.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	definition := json.RawMessage(`{"version":1,"key":"safe-review","displayName":"Safe review","baseRole":"reviewer","toolAllowlist":["Read","Grep"]}`)
	created, err := store.CreateAgentRoleDefinition(ctx, AgentRoleDefinitionInput{
		Scope: DefinitionScopeTarget{Scope: DefinitionScopeProject, ProjectID: "project-a"}, Key: "safe-review", DisplayName: "Safe review", Summary: "Read-only review", DefinitionJSON: definition,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 || len(created.DefinitionJSON) == 0 {
		t.Fatalf("unexpected create: %+v", created)
	}
	list, err := store.ListAgentRoleDefinitions(ctx, DefinitionScopeTarget{Scope: DefinitionScopeProject, ProjectID: "project-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Summary != "Read-only review" {
		t.Fatalf("unexpected summary list: %+v", list)
	}

	updatedJSON := json.RawMessage(`{"version":1,"key":"safe-review","displayName":"Safe review","baseRole":"reviewer","roleExtension":"Check API validation.","toolAllowlist":["Read"]}`)
	updated, err := store.UpdateAgentRoleDefinitionCAS(ctx, created.ID, 1, AgentRoleDefinitionInput{Key: "safe-review", DisplayName: "Safe review", Summary: "Updated", DefinitionJSON: updatedJSON})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 {
		t.Fatalf("revision = %d", updated.Revision)
	}
	if _, err := store.UpdateAgentRoleDefinitionCAS(ctx, created.ID, 1, AgentRoleDefinitionInput{Key: "safe-review", DisplayName: "Safe review", DefinitionJSON: updatedJSON}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update = %v", err)
	}

	deleted, err := store.DeleteAgentRoleDefinitionCAS(ctx, created.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Revision != 3 || deleted.DeletedAt == "" {
		t.Fatalf("unexpected delete: %+v", deleted)
	}
	if _, err := store.GetAgentRoleDefinition(ctx, created.ID); !IsNotFound(err) {
		t.Fatalf("deleted get = %v", err)
	}

	restored, err := store.RestoreAgentRoleDefinitionCAS(ctx, created.ID, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Revision != 4 || restored.DeletedAt != "" || restored.Summary != "Updated" {
		t.Fatalf("unexpected restore: %+v", restored)
	}
	revisions, err := store.ListAgentRoleDefinitionRevisions(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 4 || revisions[0].Operation != "restore" || revisions[0].RestoredFromRevision != 2 {
		t.Fatalf("unexpected revisions: %+v", revisions)
	}
}

func TestAgentRoleDefinitionValidationAndScopeIsolation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "roles-validation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	invalid := json.RawMessage(`{"version":1,"key":"x","displayName":"X","baseRole":"general","basePrompt":"override"}`)
	if _, err := store.CreateAgentRoleDefinition(ctx, AgentRoleDefinitionInput{Scope: DefinitionScopeTarget{Scope: DefinitionScopeGlobal}, Key: "x", DisplayName: "X", DefinitionJSON: invalid}); err == nil {
		t.Fatal("base prompt override was accepted")
	}
	valid := json.RawMessage(`{"version":1,"key":"x","displayName":"X","baseRole":"general"}`)
	if _, err := store.CreateAgentRoleDefinition(ctx, AgentRoleDefinitionInput{Scope: DefinitionScopeTarget{Scope: DefinitionScopeWorkspace, ProjectID: "p"}, Key: "x", DisplayName: "X", DefinitionJSON: valid}); err == nil {
		t.Fatal("invalid workspace scope accepted")
	}
}
