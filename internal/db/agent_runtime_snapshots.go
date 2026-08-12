package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const agentRunRuntimeSnapshotLimit = 512 << 10

const agentRunRuntimeSnapshotSchema = `
CREATE TABLE IF NOT EXISTS agent_run_runtime_snapshots (
  run_id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL,
  tool_capabilities_json TEXT NOT NULL CHECK (json_valid(tool_capabilities_json)),
  prompt_snapshot_json TEXT NOT NULL CHECK (json_valid(prompt_snapshot_json)),
  status TEXT NOT NULL CHECK (status IN ('active','closed')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS agent_run_runtime_snapshots_agent_idx ON agent_run_runtime_snapshots(agent_id, created_at DESC);`

type RunToolCapabilitySnapshot struct {
	Name string `json:"name"`
	Risk string `json:"risk"`
}

type AgentRunRuntimeSnapshot struct {
	RunID            string                      `json:"runId"`
	AgentID          string                      `json:"agentId"`
	ToolCapabilities []RunToolCapabilitySnapshot `json:"toolCapabilities"`
	PromptSnapshot   json.RawMessage             `json:"promptSnapshot"`
	Status           string                      `json:"status"`
	CreatedAt        string                      `json:"createdAt"`
	UpdatedAt        string                      `json:"updatedAt"`
}

func (s *Store) EnsureAgentRunRuntimeSnapshots(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("agent runtime snapshot store is not configured")
	}
	_, err := s.db.ExecContext(ctx, agentRunRuntimeSnapshotSchema)
	return err
}

func (s *Store) CreateAgentRunRuntimeSnapshot(ctx context.Context, snapshot AgentRunRuntimeSnapshot) (AgentRunRuntimeSnapshot, error) {
	if err := s.EnsureAgentRunRuntimeSnapshots(ctx); err != nil {
		return AgentRunRuntimeSnapshot{}, err
	}
	snapshot.RunID = strings.TrimSpace(snapshot.RunID)
	snapshot.AgentID = strings.TrimSpace(snapshot.AgentID)
	if snapshot.RunID == "" || snapshot.AgentID == "" {
		return AgentRunRuntimeSnapshot{}, errors.New("run and agent ids are required for runtime snapshot")
	}
	toolsJSON, err := json.Marshal(snapshot.ToolCapabilities)
	if err != nil || len(toolsJSON) > agentRunRuntimeSnapshotLimit {
		return AgentRunRuntimeSnapshot{}, errors.New("invalid runtime tool capability snapshot")
	}
	promptJSON := append(json.RawMessage(nil), snapshot.PromptSnapshot...)
	if len(promptJSON) == 0 {
		promptJSON = json.RawMessage(`{}`)
	}
	if !json.Valid(promptJSON) || len(promptJSON) > agentRunRuntimeSnapshotLimit {
		return AgentRunRuntimeSnapshot{}, errors.New("invalid runtime prompt snapshot")
	}
	now := Now()
	snapshot.Status = "active"
	snapshot.CreatedAt, snapshot.UpdatedAt = now, now
	_, err = s.db.ExecContext(ctx, `INSERT INTO agent_run_runtime_snapshots (run_id,agent_id,tool_capabilities_json,prompt_snapshot_json,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`, snapshot.RunID, snapshot.AgentID, string(toolsJSON), string(promptJSON), snapshot.Status, now, now)
	if err != nil {
		if isUniqueConstraint(err) {
			return AgentRunRuntimeSnapshot{}, fmt.Errorf("%w: run already has a runtime snapshot", ErrConflict)
		}
		return AgentRunRuntimeSnapshot{}, err
	}
	return snapshot, nil
}

func (s *Store) GetAgentRunRuntimeSnapshot(ctx context.Context, runID string) (AgentRunRuntimeSnapshot, error) {
	if err := s.EnsureAgentRunRuntimeSnapshots(ctx); err != nil {
		return AgentRunRuntimeSnapshot{}, err
	}
	var snapshot AgentRunRuntimeSnapshot
	var toolsJSON, promptJSON string
	err := s.db.QueryRowContext(ctx, `SELECT run_id,agent_id,tool_capabilities_json,prompt_snapshot_json,status,created_at,updated_at FROM agent_run_runtime_snapshots WHERE run_id=?`, strings.TrimSpace(runID)).Scan(&snapshot.RunID, &snapshot.AgentID, &toolsJSON, &promptJSON, &snapshot.Status, &snapshot.CreatedAt, &snapshot.UpdatedAt)
	if err != nil {
		return AgentRunRuntimeSnapshot{}, err
	}
	if err := json.Unmarshal([]byte(toolsJSON), &snapshot.ToolCapabilities); err != nil || !json.Valid([]byte(promptJSON)) {
		return AgentRunRuntimeSnapshot{}, errors.New("invalid stored agent runtime snapshot")
	}
	snapshot.PromptSnapshot = json.RawMessage(promptJSON)
	return snapshot, nil
}

// LatestAgentRunRuntimeSnapshot returns the newest snapshot an agent's runs
// recorded, closed ones included. A finished run's snapshot is the only durable
// record of the capability set that run was granted, so it is what a later
// caller can replay to rebuild the same contract instead of guessing one.
func (s *Store) LatestAgentRunRuntimeSnapshot(ctx context.Context, agentID string) (AgentRunRuntimeSnapshot, error) {
	if err := s.EnsureAgentRunRuntimeSnapshots(ctx); err != nil {
		return AgentRunRuntimeSnapshot{}, err
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return AgentRunRuntimeSnapshot{}, errors.New("agent id is required for runtime snapshot lookup")
	}
	var snapshot AgentRunRuntimeSnapshot
	var toolsJSON, promptJSON string
	err := s.db.QueryRowContext(ctx, `SELECT run_id,agent_id,tool_capabilities_json,prompt_snapshot_json,status,created_at,updated_at FROM agent_run_runtime_snapshots WHERE agent_id=? ORDER BY created_at DESC, run_id DESC LIMIT 1`, agentID).Scan(&snapshot.RunID, &snapshot.AgentID, &toolsJSON, &promptJSON, &snapshot.Status, &snapshot.CreatedAt, &snapshot.UpdatedAt)
	if err != nil {
		return AgentRunRuntimeSnapshot{}, err
	}
	if err := json.Unmarshal([]byte(toolsJSON), &snapshot.ToolCapabilities); err != nil || !json.Valid([]byte(promptJSON)) {
		return AgentRunRuntimeSnapshot{}, errors.New("invalid stored agent runtime snapshot")
	}
	snapshot.PromptSnapshot = json.RawMessage(promptJSON)
	return snapshot, nil
}

func (s *Store) CloseAgentRunRuntimeSnapshot(ctx context.Context, runID string) (AgentRunRuntimeSnapshot, error) {
	if err := s.EnsureAgentRunRuntimeSnapshots(ctx); err != nil {
		return AgentRunRuntimeSnapshot{}, err
	}
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE agent_run_runtime_snapshots SET status='closed',updated_at=? WHERE run_id=? AND status='active'`, now, strings.TrimSpace(runID))
	if err != nil {
		return AgentRunRuntimeSnapshot{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return AgentRunRuntimeSnapshot{}, err
	}
	if affected != 1 {
		if _, getErr := s.GetAgentRunRuntimeSnapshot(ctx, runID); errors.Is(getErr, sql.ErrNoRows) {
			return AgentRunRuntimeSnapshot{}, getErr
		}
		return AgentRunRuntimeSnapshot{}, fmt.Errorf("%w: agent runtime snapshot is not active", ErrConflict)
	}
	return s.GetAgentRunRuntimeSnapshot(ctx, runID)
}
