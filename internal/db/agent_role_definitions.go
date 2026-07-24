package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"autoto/internal/agentprofile"
)

const agentRoleDefinitionSchemaSQL = `
CREATE TABLE IF NOT EXISTS agent_role_definitions (
  id TEXT PRIMARY KEY,
  definition_key TEXT NOT NULL,
  display_name TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  definition_json TEXT NOT NULL,
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
CREATE TABLE IF NOT EXISTS agent_role_definition_revisions (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL,
  definition_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  operation TEXT NOT NULL,
  restored_from_revision INTEGER,
  definition_key TEXT NOT NULL,
  display_name TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  definition_json TEXT NOT NULL,
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
CREATE INDEX IF NOT EXISTS idx_agent_role_definitions_scope ON agent_role_definitions(target_key, definition_key);
CREATE INDEX IF NOT EXISTS idx_agent_role_definition_revisions_history ON agent_role_definition_revisions(definition_id, revision DESC);
`

var agentRoleDefinitionStore = definitionStoreSpec{
	headTable: "agent_role_definitions", revisionTable: "agent_role_definition_revisions", bodyColumn: "definition_json", schemaSQL: agentRoleDefinitionSchemaSQL,
}

type AgentRoleDefinitionSummary struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	DisplayName string `json:"displayName"`
	Summary     string `json:"summary,omitempty"`
	Scope       string `json:"scope"`
	ProjectID   string `json:"projectId,omitempty"`
	WorkspaceID string `json:"workspaceId,omitempty"`
	Revision    int64  `json:"revision"`
	DeletedAt   string `json:"deletedAt,omitempty"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

type AgentRoleDefinition struct {
	AgentRoleDefinitionSummary
	DefinitionJSON json.RawMessage `json:"definition"`
}

type AgentRoleDefinitionInput struct {
	Scope          DefinitionScopeTarget
	Key            string
	DisplayName    string
	Summary        string
	DefinitionJSON json.RawMessage
}

type AgentRoleDefinitionRevision struct {
	AgentRoleDefinition
	Operation            string `json:"operation"`
	RestoredFromRevision int64  `json:"restoredFromRevision,omitempty"`
}

func normalizeAgentRoleDefinitionInput(input AgentRoleDefinitionInput) (AgentRoleDefinitionInput, error) {
	input.Key = strings.TrimSpace(input.Key)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Summary = strings.TrimSpace(input.Summary)
	if input.Key == "" || len(input.Key) > 64 {
		return AgentRoleDefinitionInput{}, errors.New("key must be between 1 and 64 bytes")
	}
	if input.DisplayName == "" || len([]byte(input.DisplayName)) > 120 || strings.ContainsAny(input.DisplayName, "\x00\r\n") {
		return AgentRoleDefinitionInput{}, errors.New("displayName is invalid")
	}
	if len([]byte(input.Summary)) > 500 || strings.ContainsRune(input.Summary, '\x00') {
		return AgentRoleDefinitionInput{}, errors.New("summary is invalid")
	}
	parsed, err := agentprofile.ParseRoleDefinition(input.DefinitionJSON)
	if err != nil {
		return AgentRoleDefinitionInput{}, err
	}
	if parsed.Key != input.Key || parsed.DisplayName != input.DisplayName {
		return AgentRoleDefinitionInput{}, errors.New("definition key and displayName must match top-level metadata")
	}
	canonical, err := json.Marshal(parsed)
	if err != nil {
		return AgentRoleDefinitionInput{}, err
	}
	input.DefinitionJSON = canonical
	return input, nil
}

func agentRoleDefinitionFromStored(value storedDefinition) AgentRoleDefinition {
	return AgentRoleDefinition{AgentRoleDefinitionSummary: AgentRoleDefinitionSummary{
		ID: value.ID, Key: value.Key, DisplayName: value.DisplayName, Summary: value.Summary, Scope: value.Scope,
		ProjectID: value.ProjectID, WorkspaceID: value.WorkspaceID, Revision: value.Revision, DeletedAt: value.DeletedAt,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}, DefinitionJSON: json.RawMessage(value.Body)}
}

func (s *Store) EnsureAgentRoleDefinitionsSchema(ctx context.Context) error {
	return s.ensureDefinitionTables(ctx, agentRoleDefinitionStore)
}

func (s *Store) CreateAgentRoleDefinition(ctx context.Context, input AgentRoleDefinitionInput) (AgentRoleDefinition, error) {
	input, err := normalizeAgentRoleDefinitionInput(input)
	if err != nil {
		return AgentRoleDefinition{}, err
	}
	value, err := s.createStoredDefinition(ctx, agentRoleDefinitionStore, input.Scope, input.Key, input.DisplayName, input.Summary, string(input.DefinitionJSON))
	return agentRoleDefinitionFromStored(value), err
}

func (s *Store) GetAgentRoleDefinition(ctx context.Context, id string) (AgentRoleDefinition, error) {
	value, err := s.getStoredDefinition(ctx, agentRoleDefinitionStore, strings.TrimSpace(id), false)
	return agentRoleDefinitionFromStored(value), err
}

func (s *Store) GetAgentRoleDefinitionIncludingDeleted(ctx context.Context, id string) (AgentRoleDefinition, error) {
	value, err := s.getStoredDefinition(ctx, agentRoleDefinitionStore, strings.TrimSpace(id), true)
	return agentRoleDefinitionFromStored(value), err
}

func (s *Store) ListAgentRoleDefinitions(ctx context.Context, target DefinitionScopeTarget) ([]AgentRoleDefinitionSummary, error) {
	values, err := s.listStoredDefinitions(ctx, agentRoleDefinitionStore, target)
	if err != nil {
		return nil, err
	}
	result := make([]AgentRoleDefinitionSummary, 0, len(values))
	for _, value := range values {
		result = append(result, agentRoleDefinitionFromStored(value).AgentRoleDefinitionSummary)
	}
	return result, nil
}

func (s *Store) UpdateAgentRoleDefinitionCAS(ctx context.Context, id string, expectedRevision int64, input AgentRoleDefinitionInput) (AgentRoleDefinition, error) {
	input, err := normalizeAgentRoleDefinitionInput(input)
	if err != nil {
		return AgentRoleDefinition{}, err
	}
	current, err := s.GetAgentRoleDefinition(ctx, id)
	if err != nil {
		return AgentRoleDefinition{}, err
	}
	if input.Scope != (DefinitionScopeTarget{}) {
		normalized, _, normalizeErr := input.Scope.normalized()
		if normalizeErr != nil {
			return AgentRoleDefinition{}, normalizeErr
		}
		if normalized.Scope != current.Scope || normalized.ProjectID != current.ProjectID || normalized.WorkspaceID != current.WorkspaceID {
			return AgentRoleDefinition{}, errors.New("scope cannot be changed by update; restore a scoped revision instead")
		}
	}
	value, err := s.updateStoredDefinitionCAS(ctx, agentRoleDefinitionStore, id, expectedRevision, input.Key, input.DisplayName, input.Summary, string(input.DefinitionJSON))
	return agentRoleDefinitionFromStored(value), err
}

func (s *Store) DeleteAgentRoleDefinitionCAS(ctx context.Context, id string, expectedRevision int64) (AgentRoleDefinition, error) {
	value, err := s.deleteStoredDefinitionCAS(ctx, agentRoleDefinitionStore, id, expectedRevision)
	return agentRoleDefinitionFromStored(value), err
}

func (s *Store) ListAgentRoleDefinitionRevisions(ctx context.Context, id string) ([]AgentRoleDefinitionRevision, error) {
	values, err := s.listStoredDefinitionRevisions(ctx, agentRoleDefinitionStore, id)
	if err != nil {
		return nil, err
	}
	result := make([]AgentRoleDefinitionRevision, 0, len(values))
	for _, value := range values {
		result = append(result, AgentRoleDefinitionRevision{AgentRoleDefinition: agentRoleDefinitionFromStored(value.storedDefinition), Operation: value.Operation, RestoredFromRevision: value.RestoredFromRevision})
	}
	return result, nil
}

func (s *Store) RestoreAgentRoleDefinitionCAS(ctx context.Context, id string, sourceRevision, expectedRevision int64) (AgentRoleDefinition, error) {
	value, err := s.restoreStoredDefinitionCAS(ctx, agentRoleDefinitionStore, id, sourceRevision, expectedRevision)
	return agentRoleDefinitionFromStored(value), err
}
