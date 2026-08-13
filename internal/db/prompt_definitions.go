package db

import (
	"context"
	"errors"
	"strings"
)

const (
	PromptLayerSystemExtension = "system_extension"
	PromptLayerGlobalUser      = "global_user"
)

const promptDefinitionSchemaSQL = `
CREATE TABLE IF NOT EXISTS prompt_definitions (
  id TEXT PRIMARY KEY,
  definition_key TEXT NOT NULL,
  display_name TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  prompt_content TEXT NOT NULL,
  scope TEXT NOT NULL,
  project_id TEXT,
  workspace_id TEXT,
  target_key TEXT NOT NULL,
  revision INTEGER NOT NULL,
  deleted_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(target_key, definition_key),
  CHECK (scope IN ('global','project','workspace')),
  CHECK (revision >= 1)
);
CREATE TABLE IF NOT EXISTS prompt_definition_revisions (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL,
  definition_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  operation TEXT NOT NULL,
  restored_from_revision INTEGER,
  definition_key TEXT NOT NULL,
  display_name TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  prompt_content TEXT NOT NULL,
  scope TEXT NOT NULL,
  project_id TEXT,
  workspace_id TEXT,
  deleted_at TEXT,
  head_created_at TEXT NOT NULL,
  head_updated_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(definition_id, revision),
  CHECK (operation IN ('create','update','delete','restore')),
  CHECK (revision >= 1)
);
CREATE INDEX IF NOT EXISTS idx_prompt_definitions_scope ON prompt_definitions(target_key, definition_key);
CREATE INDEX IF NOT EXISTS idx_prompt_definition_revisions_history ON prompt_definition_revisions(definition_id, revision DESC);
`

var promptDefinitionStore = definitionStoreSpec{
	headTable: "prompt_definitions", revisionTable: "prompt_definition_revisions", bodyColumn: "prompt_content", schemaSQL: promptDefinitionSchemaSQL,
}

type PromptDefinitionSummary struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	DisplayName string `json:"displayName"`
	Summary     string `json:"summary,omitempty"`
	Layer       string `json:"layer"`
	Scope       string `json:"scope"`
	ProjectID   string `json:"projectId,omitempty"`
	WorkspaceID string `json:"workspaceId,omitempty"`
	Revision    int64  `json:"revision"`
	DeletedAt   string `json:"deletedAt,omitempty"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

type PromptDefinition struct {
	PromptDefinitionSummary
	Content string `json:"content"`
}

type PromptDefinitionInput struct {
	Scope       DefinitionScopeTarget
	Key         string
	DisplayName string
	Summary     string
	Layer       string
	Content     string
}

type PromptDefinitionRevision struct {
	PromptDefinition
	Operation            string `json:"operation"`
	RestoredFromRevision int64  `json:"restoredFromRevision,omitempty"`
}

func normalizePromptDefinitionInput(input PromptDefinitionInput) (PromptDefinitionInput, error) {
	input.Key = strings.TrimSpace(input.Key)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Summary = strings.TrimSpace(input.Summary)
	input.Layer = strings.TrimSpace(strings.ToLower(input.Layer))
	input.Content = strings.TrimSpace(input.Content)
	if !validDefinitionKey(input.Key) {
		return PromptDefinitionInput{}, errors.New("key is invalid")
	}
	if input.DisplayName == "" || len([]byte(input.DisplayName)) > 120 || strings.ContainsAny(input.DisplayName, "\x00\r\n") {
		return PromptDefinitionInput{}, errors.New("displayName is invalid")
	}
	if len([]byte(input.Summary)) > 500 || strings.ContainsRune(input.Summary, '\x00') {
		return PromptDefinitionInput{}, errors.New("summary is invalid")
	}
	if input.Layer != PromptLayerSystemExtension && input.Layer != PromptLayerGlobalUser {
		return PromptDefinitionInput{}, errors.New("layer must be system_extension or global_user")
	}
	if input.Content == "" || len([]byte(input.Content)) > 64<<10 || strings.ContainsRune(input.Content, '\x00') {
		return PromptDefinitionInput{}, errors.New("content is invalid")
	}
	return input, nil
}

func encodePromptBody(layer, content string) string { return layer + "\n" + content }

func decodePromptBody(body string) (string, string) {
	layer, content, ok := strings.Cut(body, "\n")
	if !ok {
		return "", body
	}
	return layer, content
}

func promptDefinitionFromStored(value storedDefinition) PromptDefinition {
	layer, content := decodePromptBody(value.Body)
	return PromptDefinition{PromptDefinitionSummary: PromptDefinitionSummary{
		ID: value.ID, Key: value.Key, DisplayName: value.DisplayName, Summary: value.Summary, Layer: layer, Scope: value.Scope,
		ProjectID: value.ProjectID, WorkspaceID: value.WorkspaceID, Revision: value.Revision, DeletedAt: value.DeletedAt,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}, Content: content}
}

func (s *promptStore) EnsurePromptDefinitionsSchema(ctx context.Context) error {
	return s.ensureDefinitionTables(ctx, promptDefinitionStore)
}

func (s *promptStore) CreatePromptDefinition(ctx context.Context, input PromptDefinitionInput) (PromptDefinition, error) {
	input, err := normalizePromptDefinitionInput(input)
	if err != nil {
		return PromptDefinition{}, err
	}
	value, err := s.createStoredDefinition(ctx, promptDefinitionStore, input.Scope, input.Key, input.DisplayName, input.Summary, encodePromptBody(input.Layer, input.Content))
	return promptDefinitionFromStored(value), err
}

func (s *promptStore) GetPromptDefinition(ctx context.Context, id string) (PromptDefinition, error) {
	value, err := s.getStoredDefinition(ctx, promptDefinitionStore, strings.TrimSpace(id), false)
	return promptDefinitionFromStored(value), err
}

func (s *promptStore) GetPromptDefinitionIncludingDeleted(ctx context.Context, id string) (PromptDefinition, error) {
	value, err := s.getStoredDefinition(ctx, promptDefinitionStore, strings.TrimSpace(id), true)
	return promptDefinitionFromStored(value), err
}

func (s *promptStore) ListPromptDefinitions(ctx context.Context, target DefinitionScopeTarget) ([]PromptDefinitionSummary, error) {
	values, err := s.listStoredDefinitions(ctx, promptDefinitionStore, target)
	if err != nil {
		return nil, err
	}
	result := make([]PromptDefinitionSummary, 0, len(values))
	for _, value := range values {
		result = append(result, promptDefinitionFromStored(value).PromptDefinitionSummary)
	}
	return result, nil
}

func (s *promptStore) UpdatePromptDefinitionCAS(ctx context.Context, id string, expectedRevision int64, input PromptDefinitionInput) (PromptDefinition, error) {
	input, err := normalizePromptDefinitionInput(input)
	if err != nil {
		return PromptDefinition{}, err
	}
	current, err := s.GetPromptDefinition(ctx, id)
	if err != nil {
		return PromptDefinition{}, err
	}
	if input.Scope != (DefinitionScopeTarget{}) {
		normalized, _, normalizeErr := input.Scope.normalized()
		if normalizeErr != nil {
			return PromptDefinition{}, normalizeErr
		}
		if normalized.Scope != current.Scope || normalized.ProjectID != current.ProjectID || normalized.WorkspaceID != current.WorkspaceID {
			return PromptDefinition{}, errors.New("scope cannot be changed by update; restore a scoped revision instead")
		}
	}
	value, err := s.updateStoredDefinitionCAS(ctx, promptDefinitionStore, id, expectedRevision, input.Key, input.DisplayName, input.Summary, encodePromptBody(input.Layer, input.Content))
	return promptDefinitionFromStored(value), err
}

func (s *promptStore) DeletePromptDefinitionCAS(ctx context.Context, id string, expectedRevision int64) (PromptDefinition, error) {
	value, err := s.deleteStoredDefinitionCAS(ctx, promptDefinitionStore, id, expectedRevision)
	return promptDefinitionFromStored(value), err
}

func (s *promptStore) ListPromptDefinitionRevisions(ctx context.Context, id string) ([]PromptDefinitionRevision, error) {
	values, err := s.listStoredDefinitionRevisions(ctx, promptDefinitionStore, id)
	if err != nil {
		return nil, err
	}
	result := make([]PromptDefinitionRevision, 0, len(values))
	for _, value := range values {
		result = append(result, PromptDefinitionRevision{PromptDefinition: promptDefinitionFromStored(value.storedDefinition), Operation: value.Operation, RestoredFromRevision: value.RestoredFromRevision})
	}
	return result, nil
}

func (s *promptStore) RestorePromptDefinitionCAS(ctx context.Context, id string, sourceRevision, expectedRevision int64) (PromptDefinition, error) {
	value, err := s.restoreStoredDefinitionCAS(ctx, promptDefinitionStore, id, sourceRevision, expectedRevision)
	return promptDefinitionFromStored(value), err
}
