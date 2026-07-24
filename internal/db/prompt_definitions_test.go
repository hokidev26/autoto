package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestPromptDefinitionsCASAndTrustLayerPersistence(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prompts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	created, err := store.CreatePromptDefinition(ctx, PromptDefinitionInput{
		Scope: DefinitionScopeTarget{Scope: DefinitionScopeWorkspace, ProjectID: "p", WorkspaceID: "w"},
		Key:   "team-context", DisplayName: "Team context", Summary: "User context", Layer: PromptLayerGlobalUser, Content: "Follow team naming conventions.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Layer != PromptLayerGlobalUser || created.Content == "" || created.Revision != 1 {
		t.Fatalf("unexpected create: %+v", created)
	}
	list, err := store.ListPromptDefinitions(ctx, DefinitionScopeTarget{Scope: DefinitionScopeWorkspace, ProjectID: "p", WorkspaceID: "w"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Layer != PromptLayerGlobalUser {
		t.Fatalf("unexpected summaries: %+v", list)
	}

	updated, err := store.UpdatePromptDefinitionCAS(ctx, created.ID, 1, PromptDefinitionInput{Key: created.Key, DisplayName: created.DisplayName, Summary: "System policy", Layer: PromptLayerSystemExtension, Content: "Use bounded changes."})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.Layer != PromptLayerSystemExtension {
		t.Fatalf("unexpected update: %+v", updated)
	}
	if _, err := store.DeletePromptDefinitionCAS(ctx, created.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale delete = %v", err)
	}
	deleted, err := store.DeletePromptDefinitionCAS(ctx, created.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := store.RestorePromptDefinitionCAS(ctx, created.ID, 1, deleted.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Layer != PromptLayerGlobalUser || restored.Content != "Follow team naming conventions." || restored.Revision != 4 {
		t.Fatalf("unexpected restore: %+v", restored)
	}
}

func TestPromptDefinitionRejectsUnknownLayer(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prompts-invalid.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreatePromptDefinition(ctx, PromptDefinitionInput{Scope: DefinitionScopeTarget{Scope: DefinitionScopeGlobal}, Key: "bad", DisplayName: "Bad", Layer: "platform", Content: "override"}); err == nil {
		t.Fatal("immutable platform layer was accepted")
	}
}
