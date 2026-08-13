package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	SpecTaskMaxBytes          = 16 * 1024
	SpecTaskMaxCount          = 1000
	ModelAggregateMaxMembers  = 32
	ExecutionPayloadMaxBytes  = 32 * 1024
	ExecutionLeaseMaxDuration = 10 * time.Minute
)

type SpecBoard struct {
	AgentID       string             `json:"agentId"`
	Revision      int64              `json:"revision"`
	UpdatedAt     string             `json:"updatedAt"`
	Tasks         []SpecTask         `json:"tasks"`
	Confirmations []GoalConfirmation `json:"goalConfirmations"`
}

// SpecReminderSnapshot is a bounded, read-only projection for runtime Agent
// reminders. It intentionally omits task IDs, source metadata, timestamps, and
// confirmations so the reminder cannot become a mutation or audit surface.
type SpecReminderSnapshot struct {
	AgentID  string
	Revision int64
	Tasks    []SpecTask
	Omitted  int
}

type SpecTask struct {
	ID         string `json:"id"`
	AgentID    string `json:"agentId"`
	Text       string `json:"text"`
	Status     string `json:"status"`
	Protected  bool   `json:"protected"`
	Position   int    `json:"position"`
	Revision   int64  `json:"revision"`
	SourceType string `json:"sourceType"`
	SourceID   string `json:"sourceId,omitempty"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
}

type SpecTaskMutation struct {
	Text                 *string
	Status               *string
	Protected            *bool
	ExpectedRevision     int64
	AcknowledgeProtected bool
	Actor                string
}

type GoalConfirmation struct {
	ID         string `json:"id"`
	AgentID    string `json:"agentId"`
	TaskID     string `json:"taskId"`
	QueueState string `json:"queueState"`
	Status     string `json:"status"`
	CreatedAt  string `json:"createdAt"`
}

type ModelAggregate struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Mode      string   `json:"mode"`
	Members   []string `json:"members"`
	Revision  int64    `json:"revision"`
	UpdatedAt string   `json:"updatedAt"`
}

type RuntimeSettings struct {
	InstallationID         string `json:"installationId"`
	DefaultReasoningEffort string `json:"defaultReasoningEffort"`
	SubscriptionTier       string `json:"subscriptionTier"`
	AccountEmail           string `json:"accountEmail,omitempty"`
	Revision               int64  `json:"revision"`
	UpdatedAt              string `json:"updatedAt"`
}

type RuntimeSettingsPatch struct {
	DefaultReasoningEffort *string
	SubscriptionTier       *string
	AccountEmail           *string
	ExpectedRevision       int64
}

type ExecutionDevice struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	Name         string          `json:"name"`
	Enabled      bool            `json:"enabled"`
	Status       string          `json:"status"`
	Capabilities json.RawMessage `json:"capabilities"`
	CreatedAt    string          `json:"createdAt"`
	UpdatedAt    string          `json:"updatedAt"`
}

type ExecutionDeviceRegistration struct {
	ID                  string
	Name                string
	Capabilities        json.RawMessage
	IdentityFingerprint string
}

type ProjectDeviceGrant struct {
	ProjectID    string          `json:"projectId"`
	DeviceID     string          `json:"deviceId"`
	Enabled      bool            `json:"enabled"`
	Capabilities json.RawMessage `json:"capabilities"`
	UpdatedAt    string          `json:"updatedAt"`
}

type RemoteExecutionTask struct {
	ID                string          `json:"id"`
	IdempotencyKey    string          `json:"idempotencyKey"`
	ProjectID         string          `json:"projectId"`
	AgentID           string          `json:"agentId"`
	RunID             string          `json:"runId,omitempty"`
	ExecutionDeviceID string          `json:"executionDeviceId"`
	Status            string          `json:"status"`
	Payload           json.RawMessage `json:"payload"`
	Result            json.RawMessage `json:"result"`
	NoFallback        bool            `json:"noFallback"`
	LeaseOwner        string          `json:"leaseOwner,omitempty"`
	LeaseUntil        string          `json:"leaseUntil,omitempty"`
	AttemptCount      int             `json:"attemptCount"`
	LastError         string          `json:"lastError,omitempty"`
	Revision          int64           `json:"revision"`
	CreatedAt         string          `json:"createdAt"`
	UpdatedAt         string          `json:"updatedAt"`
	CompletedAt       string          `json:"completedAt,omitempty"`
}

func (s *Store) ensureRuntimeSettings(ctx context.Context) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_settings WHERE id = 'default'`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO runtime_settings (id, installation_id, default_reasoning_effort, subscription_tier, account_email, revision, updated_at) VALUES ('default', ?, 'auto', 'free', NULL, 1, ?)`, uuid.NewString(), Now())
	return err
}

func (s *Store) GetRuntimeSettings(ctx context.Context) (RuntimeSettings, error) {
	if err := s.ensureRuntimeSettings(ctx); err != nil {
		return RuntimeSettings{}, err
	}
	var settings RuntimeSettings
	err := s.db.QueryRowContext(ctx, `SELECT installation_id, default_reasoning_effort, subscription_tier, COALESCE(account_email,''), revision, updated_at FROM runtime_settings WHERE id = 'default'`).Scan(
		&settings.InstallationID, &settings.DefaultReasoningEffort, &settings.SubscriptionTier, &settings.AccountEmail, &settings.Revision, &settings.UpdatedAt,
	)
	return settings, err
}

func (s *Store) UpdateRuntimeSettings(ctx context.Context, patch RuntimeSettingsPatch) (RuntimeSettings, error) {
	if patch.ExpectedRevision < 1 {
		return RuntimeSettings{}, errors.New("runtime settings expected revision is required")
	}
	current, err := s.GetRuntimeSettings(ctx)
	if err != nil {
		return RuntimeSettings{}, err
	}
	if patch.DefaultReasoningEffort != nil {
		current.DefaultReasoningEffort = strings.TrimSpace(*patch.DefaultReasoningEffort)
	}
	if patch.SubscriptionTier != nil {
		current.SubscriptionTier = strings.TrimSpace(*patch.SubscriptionTier)
	}
	if patch.AccountEmail != nil {
		current.AccountEmail = strings.TrimSpace(*patch.AccountEmail)
	}
	if !validDefaultReasoningEffort(current.DefaultReasoningEffort) {
		return RuntimeSettings{}, errors.New("invalid default reasoning effort")
	}
	if !validSubscriptionTier(current.SubscriptionTier) {
		return RuntimeSettings{}, errors.New("invalid subscription tier")
	}
	if err := validateAccountEmail(current.AccountEmail); err != nil {
		return RuntimeSettings{}, err
	}
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE runtime_settings SET default_reasoning_effort = ?, subscription_tier = ?, account_email = NULLIF(?, ''), revision = revision + 1, updated_at = ? WHERE id = 'default' AND revision = ?`, current.DefaultReasoningEffort, current.SubscriptionTier, current.AccountEmail, now, patch.ExpectedRevision)
	if err != nil {
		return RuntimeSettings{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return RuntimeSettings{}, err
	} else if affected != 1 {
		return RuntimeSettings{}, fmt.Errorf("%w: runtime settings changed", ErrConflict)
	}
	return s.GetRuntimeSettings(ctx)
}

// validDefaultReasoningEffort mirrors validAgentReasoningEffort's level set,
// including "xhigh", which Codex serves. Keeping the two in step matters: this
// is the storage-layer gate, so a level the API accepts but this rejects
// surfaces as a 500 rather than a validation error.
func validDefaultReasoningEffort(value string) bool {
	switch value {
	case "auto", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}

func (s *Store) RotateInstallationID(ctx context.Context, expectedRevision int64) (RuntimeSettings, error) {
	if expectedRevision < 1 {
		return RuntimeSettings{}, errors.New("runtime settings expected revision is required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE runtime_settings SET installation_id = ?, revision = revision + 1, updated_at = ? WHERE id = 'default' AND revision = ?`, uuid.NewString(), Now(), expectedRevision)
	if err != nil {
		return RuntimeSettings{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return RuntimeSettings{}, err
	} else if affected != 1 {
		return RuntimeSettings{}, fmt.Errorf("%w: runtime settings changed", ErrConflict)
	}
	return s.GetRuntimeSettings(ctx)
}

func validSubscriptionTier(value string) bool {
	switch value {
	case "free", "plus", "pro", "team", "enterprise", "education_k12":
		return true
	default:
		return false
	}
}

func validateAccountEmail(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 320 || !utf8.ValidString(value) || strings.Count(value, "@") != 1 || strings.ContainsAny(value, "\r\n\t ") {
		return errors.New("invalid account email")
	}
	parts := strings.SplitN(value, "@", 2)
	if parts[0] == "" || parts[1] == "" || !strings.Contains(parts[1], ".") {
		return errors.New("invalid account email")
	}
	return nil
}

func (s *Store) ListScheduleRuns(ctx context.Context, scheduleID string, limit int) ([]Run, error) {
	scheduleID = strings.TrimSpace(scheduleID)
	if err := validateP2P3Text("schedule id", scheduleID, 128, true, false); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, runSelectSQL+` WHERE source_id = ? AND source IN ('schedule','scheduled') ORDER BY execution_generation DESC, id DESC LIMIT ?`, scheduleID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Run, 0)
	for rows.Next() {
		run, err := scanRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (s *Store) ListRunsAfterExecutionGeneration(ctx context.Context, agentID string, after int64, limit int) ([]Run, bool, error) {
	if after < 0 {
		return nil, false, errors.New("execution generation must not be negative")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, runSelectSQL+` WHERE agent_id = ? AND execution_generation > ? ORDER BY execution_generation ASC, id ASC LIMIT ?`, strings.TrimSpace(agentID), after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := make([]Run, 0, limit)
	truncated := false
	for rows.Next() {
		run, err := scanRun(rows.Scan)
		if err != nil {
			return nil, false, err
		}
		if len(out) == limit {
			truncated = true
			continue
		}
		out = append(out, run)
	}
	return out, truncated, rows.Err()
}

func (s *Store) MaxExecutionGeneration(ctx context.Context, agentID string) (int64, error) {
	var generation int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(execution_generation,0) FROM agents WHERE id = ?`, strings.TrimSpace(agentID)).Scan(&generation)
	return generation, err
}

func (s *Store) ListAgents(ctx context.Context, limit int) ([]Agent, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, agentSelectSQL+` ORDER BY updated_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Agent, 0)
	for rows.Next() {
		agent, err := scanAgent(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, agent)
	}
	return out, rows.Err()
}

func (s *Store) ListChildAgents(ctx context.Context, parentAgentID string) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, agentSelectSQL+` WHERE parent_agent_id = ? ORDER BY created_at ASC, id ASC`, strings.TrimSpace(parentAgentID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Agent, 0)
	for rows.Next() {
		agent, err := scanAgent(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, agent)
	}
	return out, rows.Err()
}

func (s *Store) CreateAgent(ctx context.Context, agent Agent) (Agent, error) {
	agent.ID = strings.TrimSpace(agent.ID)
	agent.WorklineID = strings.TrimSpace(agent.WorklineID)
	agent.ParentAgentID = strings.TrimSpace(agent.ParentAgentID)
	agent.Title = strings.TrimSpace(agent.Title)
	agent.Model = strings.TrimSpace(agent.Model)
	agent.PermissionMode = strings.TrimSpace(agent.PermissionMode)
	agent.ReasoningEffort = strings.TrimSpace(agent.ReasoningEffort)
	agent.ExecutionDeviceID = strings.TrimSpace(agent.ExecutionDeviceID)
	agent.CWD = strings.TrimSpace(agent.CWD)
	if agent.ID == "" {
		agent.ID = NewID()
	}
	if agent.Type == "" {
		agent.Type = "primary"
	}
	if agent.Status == "" {
		agent.Status = "idle"
	}
	if agent.PermissionMode == "" {
		agent.PermissionMode = "acceptEdits"
	}
	if agent.ExecutionDeviceID == "" {
		agent.ExecutionDeviceID = "local"
	}
	if agent.Title == "" || agent.Model == "" {
		return Agent{}, errors.New("agent title and model are required")
	}
	if agent.Type != "primary" && agent.Type != "subagent" {
		return Agent{}, errors.New("invalid agent type")
	}
	if !validPermissionModeForDB(agent.PermissionMode) || !validAgentReasoningEffort(agent.ReasoningEffort, true) {
		return Agent{}, errors.New("invalid agent settings")
	}
	now := Now()
	agent.CreatedAt, agent.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx, `INSERT INTO agents (id, workline_id, type, subagent_type, title, inherit_mode, parent_agent_id, fork_message_id, model, system_prompt, permission_mode, reasoning_effort, fast_mode, execution_device_id, status, plan_mode, cwd, created_at, updated_at) VALUES (?, NULLIF(?,''), ?, NULLIF(?,''), ?, NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), ?, NULLIF(?,''), ?, NULLIF(?,''), ?, ?, ?, ?, NULLIF(?,''), ?, ?)`, agent.ID, agent.WorklineID, agent.Type, agent.SubagentType, agent.Title, agent.InheritMode, agent.ParentAgentID, agent.ForkMessageID, agent.Model, agent.SystemPrompt, agent.PermissionMode, agent.ReasoningEffort, boolInt(agent.FastMode), agent.ExecutionDeviceID, agent.Status, boolInt(agent.PlanMode), agent.CWD, agent.CreatedAt, agent.UpdatedAt)
	if err != nil {
		return Agent{}, err
	}
	return s.GetAgent(ctx, agent.ID)
}

func validPermissionModeForDB(value string) bool {
	switch value {
	case "readOnly", "acceptEdits", "bypassPermissions", "default", "dontAsk":
		return true
	default:
		return false
	}
}

func (s *Store) GetSpecBoard(ctx context.Context, agentID string) (SpecBoard, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SpecBoard{}, err
	}
	defer tx.Rollback()
	board, err := readSpecBoardTx(ctx, tx, strings.TrimSpace(agentID))
	if err != nil {
		return SpecBoard{}, err
	}
	if err := tx.Commit(); err != nil {
		return SpecBoard{}, err
	}
	return board, nil
}

// ReadSpecReminderSnapshot returns active tasks only and never mutates the Spec
// board. The caller controls the bounded task count; Omitted reports how many
// additional active tasks were not returned.
func (s *Store) ReadSpecReminderSnapshot(ctx context.Context, agentID string, limit int) (SpecReminderSnapshot, error) {
	agentID = strings.TrimSpace(agentID)
	if limit < 0 {
		limit = 0
	}
	if limit > SpecTaskMaxCount {
		limit = SpecTaskMaxCount
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SpecReminderSnapshot{}, err
	}
	defer tx.Rollback()

	snapshot := SpecReminderSnapshot{AgentID: agentID}
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM spec_boards WHERE agent_id = ?`, agentID).Scan(&snapshot.Revision); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return SpecReminderSnapshot{}, err
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE id = ?`, agentID).Scan(&exists); err != nil {
			return SpecReminderSnapshot{}, err
		}
	}

	var activeCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM spec_tasks WHERE agent_id = ? AND status IN ('todo','doing','blocked')`, agentID).Scan(&activeCount); err != nil {
		return SpecReminderSnapshot{}, err
	}
	if limit > 0 && activeCount > 0 {
		rows, err := tx.QueryContext(ctx, `SELECT id, agent_id, text, status, protected, position, revision, source_type, COALESCE(source_id,''), created_at, updated_at FROM spec_tasks WHERE agent_id = ? AND status IN ('todo','doing','blocked') ORDER BY position ASC, id ASC LIMIT ?`, agentID, limit)
		if err != nil {
			return SpecReminderSnapshot{}, err
		}
		for rows.Next() {
			var task SpecTask
			var protected int
			if err := rows.Scan(&task.ID, &task.AgentID, &task.Text, &task.Status, &protected, &task.Position, &task.Revision, &task.SourceType, &task.SourceID, &task.CreatedAt, &task.UpdatedAt); err != nil {
				rows.Close()
				return SpecReminderSnapshot{}, err
			}
			task.Protected = protected != 0
			snapshot.Tasks = append(snapshot.Tasks, task)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return SpecReminderSnapshot{}, err
		}
		if err := rows.Close(); err != nil {
			return SpecReminderSnapshot{}, err
		}
	}
	snapshot.Omitted = activeCount - len(snapshot.Tasks)
	if err := tx.Commit(); err != nil {
		return SpecReminderSnapshot{}, err
	}
	return snapshot, nil
}

func readSpecBoardTx(ctx context.Context, tx *sql.Tx, agentID string) (SpecBoard, error) {
	var board SpecBoard
	board.AgentID = agentID
	if err := tx.QueryRowContext(ctx, `SELECT revision, updated_at FROM spec_boards WHERE agent_id = ?`, agentID).Scan(&board.Revision, &board.UpdatedAt); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return SpecBoard{}, err
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE id = ?`, agentID).Scan(&exists); err != nil {
			return SpecBoard{}, err
		}
		board.UpdatedAt = ""
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, agent_id, text, status, protected, position, revision, source_type, COALESCE(source_id,''), created_at, updated_at FROM spec_tasks WHERE agent_id = ? ORDER BY position ASC, id ASC`, agentID)
	if err != nil {
		return SpecBoard{}, err
	}
	for rows.Next() {
		var task SpecTask
		var protected int
		if err := rows.Scan(&task.ID, &task.AgentID, &task.Text, &task.Status, &protected, &task.Position, &task.Revision, &task.SourceType, &task.SourceID, &task.CreatedAt, &task.UpdatedAt); err != nil {
			rows.Close()
			return SpecBoard{}, err
		}
		task.Protected = protected != 0
		board.Tasks = append(board.Tasks, task)
	}
	if err := rows.Close(); err != nil {
		return SpecBoard{}, err
	}
	goalRows, err := tx.QueryContext(ctx, `SELECT id, agent_id, task_id, queue_state, status, created_at FROM goal_confirmations WHERE agent_id = ? ORDER BY created_at DESC, id DESC LIMIT 100`, agentID)
	if err != nil {
		return SpecBoard{}, err
	}
	defer goalRows.Close()
	for goalRows.Next() {
		var confirmation GoalConfirmation
		if err := goalRows.Scan(&confirmation.ID, &confirmation.AgentID, &confirmation.TaskID, &confirmation.QueueState, &confirmation.Status, &confirmation.CreatedAt); err != nil {
			return SpecBoard{}, err
		}
		board.Confirmations = append(board.Confirmations, confirmation)
	}
	return board, goalRows.Err()
}

func ensureSpecBoardTx(ctx context.Context, tx *sql.Tx, agentID, now string) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE id = ?`, agentID).Scan(&exists); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO spec_boards (agent_id, revision, updated_at) VALUES (?, 0, ?)`, agentID, now)
	return err
}

func validateSpecTask(task SpecTask) error {
	task.Text = strings.TrimSpace(task.Text)
	if task.Text == "" || len([]byte(task.Text)) > SpecTaskMaxBytes || !utf8.ValidString(task.Text) {
		return errors.New("invalid spec task text")
	}
	switch task.Status {
	case "todo", "doing", "done", "blocked":
	default:
		return errors.New("invalid spec task status")
	}
	switch task.SourceType {
	case "manual", "goal", "automation", "migration", "system":
	default:
		return errors.New("invalid spec task source")
	}
	return nil
}

func (s *Store) CreateSpecTask(ctx context.Context, task SpecTask) (SpecBoard, error) {
	task.AgentID = strings.TrimSpace(task.AgentID)
	task.Text = strings.TrimSpace(task.Text)
	if task.ID == "" {
		task.ID = NewID()
	}
	if task.Status == "" {
		task.Status = "todo"
	}
	if task.SourceType == "" {
		task.SourceType = "manual"
	}
	if err := validateSpecTask(task); err != nil {
		return SpecBoard{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SpecBoard{}, err
	}
	defer tx.Rollback()
	now := Now()
	if err := ensureSpecBoardTx(ctx, tx, task.AgentID, now); err != nil {
		return SpecBoard{}, err
	}
	var count, position int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(position),-1)+1 FROM spec_tasks WHERE agent_id = ?`, task.AgentID).Scan(&count, &position); err != nil {
		return SpecBoard{}, err
	}
	if count >= SpecTaskMaxCount {
		return SpecBoard{}, errors.New("spec task limit reached")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO spec_tasks (id, agent_id, text, status, protected, position, revision, source_type, source_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 1, ?, NULLIF(?,''), ?, ?)`, task.ID, task.AgentID, task.Text, task.Status, boolInt(task.Protected), position, task.SourceType, strings.TrimSpace(task.SourceID), now, now)
	if err != nil {
		return SpecBoard{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE spec_boards SET revision = revision + 1, updated_at = ? WHERE agent_id = ?`, now, task.AgentID); err != nil {
		return SpecBoard{}, err
	}
	board, err := readSpecBoardTx(ctx, tx, task.AgentID)
	if err != nil {
		return SpecBoard{}, err
	}
	if err := tx.Commit(); err != nil {
		return SpecBoard{}, err
	}
	return board, nil
}

func (s *Store) UpdateSpecTask(ctx context.Context, agentID, taskID string, mutation SpecTaskMutation) (SpecBoard, error) {
	if mutation.ExpectedRevision < 1 {
		return SpecBoard{}, errors.New("spec task expected revision is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SpecBoard{}, err
	}
	defer tx.Rollback()
	var current SpecTask
	var protected int
	if err := tx.QueryRowContext(ctx, `SELECT id, agent_id, text, status, protected, position, revision, source_type, COALESCE(source_id,''), created_at, updated_at FROM spec_tasks WHERE agent_id = ? AND id = ?`, strings.TrimSpace(agentID), strings.TrimSpace(taskID)).Scan(&current.ID, &current.AgentID, &current.Text, &current.Status, &protected, &current.Position, &current.Revision, &current.SourceType, &current.SourceID, &current.CreatedAt, &current.UpdatedAt); err != nil {
		return SpecBoard{}, err
	}
	current.Protected = protected != 0
	if current.Revision != mutation.ExpectedRevision {
		return SpecBoard{}, fmt.Errorf("%w: spec task changed", ErrConflict)
	}
	changed := false
	if mutation.Text != nil {
		text := strings.TrimSpace(*mutation.Text)
		if text != current.Text {
			current.Text = text
			changed = true
		}
	}
	if mutation.Status != nil {
		status := strings.TrimSpace(*mutation.Status)
		if status != current.Status {
			current.Status = status
			changed = true
		}
	}
	if mutation.Protected != nil && *mutation.Protected != current.Protected {
		current.Protected = *mutation.Protected
		changed = true
	}
	if !changed {
		return readSpecBoardTx(ctx, tx, current.AgentID)
	}
	if protected != 0 && !mutation.AcknowledgeProtected {
		return SpecBoard{}, fmt.Errorf("%w: protected task requires acknowledgement", ErrConflict)
	}
	if err := validateSpecTask(current); err != nil {
		return SpecBoard{}, err
	}
	now := Now()
	result, err := tx.ExecContext(ctx, `UPDATE spec_tasks SET text = ?, status = ?, protected = ?, revision = revision + 1, updated_at = ? WHERE agent_id = ? AND id = ? AND revision = ?`, current.Text, current.Status, boolInt(current.Protected), now, current.AgentID, current.ID, mutation.ExpectedRevision)
	if err != nil {
		return SpecBoard{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return SpecBoard{}, err
	} else if affected != 1 {
		return SpecBoard{}, fmt.Errorf("%w: spec task changed", ErrConflict)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE spec_boards SET revision = revision + 1, updated_at = ? WHERE agent_id = ?`, now, current.AgentID); err != nil {
		return SpecBoard{}, err
	}
	if protected != 0 {
		if err := insertSpecAuditTx(ctx, tx, current.AgentID, current.ID, "task.update_protected", mutation.Actor); err != nil {
			return SpecBoard{}, err
		}
	}
	board, err := readSpecBoardTx(ctx, tx, current.AgentID)
	if err != nil {
		return SpecBoard{}, err
	}
	if err := tx.Commit(); err != nil {
		return SpecBoard{}, err
	}
	return board, nil
}

func (s *Store) DeleteSpecTask(ctx context.Context, agentID, taskID string, expectedRevision int64, acknowledgeProtected bool, actor string) (SpecBoard, error) {
	if expectedRevision < 1 {
		return SpecBoard{}, errors.New("spec task expected revision is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SpecBoard{}, err
	}
	defer tx.Rollback()
	var protected int
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT protected, revision FROM spec_tasks WHERE agent_id = ? AND id = ?`, strings.TrimSpace(agentID), strings.TrimSpace(taskID)).Scan(&protected, &revision); err != nil {
		return SpecBoard{}, err
	}
	if revision != expectedRevision {
		return SpecBoard{}, fmt.Errorf("%w: spec task changed", ErrConflict)
	}
	if protected != 0 && !acknowledgeProtected {
		return SpecBoard{}, fmt.Errorf("%w: protected task requires acknowledgement", ErrConflict)
	}
	if protected != 0 {
		if err := insertSpecAuditTx(ctx, tx, agentID, taskID, "task.delete_protected", actor); err != nil {
			return SpecBoard{}, err
		}
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM spec_tasks WHERE agent_id = ? AND id = ? AND revision = ?`, agentID, taskID, expectedRevision)
	if err != nil {
		return SpecBoard{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return SpecBoard{}, err
	} else if affected != 1 {
		return SpecBoard{}, fmt.Errorf("%w: spec task changed", ErrConflict)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM spec_tasks WHERE agent_id = ? ORDER BY position ASC, id ASC`, agentID)
	if err != nil {
		return SpecBoard{}, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return SpecBoard{}, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for position, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE spec_tasks SET position = ? WHERE id = ?`, position, id); err != nil {
			return SpecBoard{}, err
		}
	}
	now := Now()
	if _, err := tx.ExecContext(ctx, `UPDATE spec_boards SET revision = revision + 1, updated_at = ? WHERE agent_id = ?`, now, agentID); err != nil {
		return SpecBoard{}, err
	}
	board, err := readSpecBoardTx(ctx, tx, agentID)
	if err != nil {
		return SpecBoard{}, err
	}
	if err := tx.Commit(); err != nil {
		return SpecBoard{}, err
	}
	return board, nil
}

func (s *Store) ReorderSpecTasks(ctx context.Context, agentID string, taskIDs []string, expectedBoardRevision int64) (SpecBoard, error) {
	if expectedBoardRevision < 0 || len(taskIDs) > SpecTaskMaxCount {
		return SpecBoard{}, errors.New("invalid spec board revision or task order")
	}
	seen := make(map[string]struct{}, len(taskIDs))
	for index := range taskIDs {
		taskIDs[index] = strings.TrimSpace(taskIDs[index])
		if taskIDs[index] == "" {
			return SpecBoard{}, errors.New("spec task id is required")
		}
		if _, ok := seen[taskIDs[index]]; ok {
			return SpecBoard{}, errors.New("spec task order contains duplicates")
		}
		seen[taskIDs[index]] = struct{}{}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SpecBoard{}, err
	}
	defer tx.Rollback()
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM spec_boards WHERE agent_id = ?`, strings.TrimSpace(agentID)).Scan(&revision); err != nil {
		return SpecBoard{}, err
	}
	if revision != expectedBoardRevision {
		return SpecBoard{}, fmt.Errorf("%w: spec board changed", ErrConflict)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM spec_tasks WHERE agent_id = ? ORDER BY position ASC, id ASC`, agentID)
	if err != nil {
		return SpecBoard{}, err
	}
	actual := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return SpecBoard{}, err
		}
		actual[id] = struct{}{}
	}
	rows.Close()
	if len(actual) != len(taskIDs) {
		return SpecBoard{}, errors.New("spec task order must include every task exactly once")
	}
	for _, id := range taskIDs {
		if _, ok := actual[id]; !ok {
			return SpecBoard{}, errors.New("spec task order contains an unknown task")
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE spec_tasks SET position = position + 1000000 WHERE agent_id = ?`, agentID); err != nil {
		return SpecBoard{}, err
	}
	for position, id := range taskIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE spec_tasks SET position = ?, updated_at = ? WHERE agent_id = ? AND id = ?`, position, Now(), agentID, id); err != nil {
			return SpecBoard{}, err
		}
	}
	now := Now()
	result, err := tx.ExecContext(ctx, `UPDATE spec_boards SET revision = revision + 1, updated_at = ? WHERE agent_id = ? AND revision = ?`, now, agentID, expectedBoardRevision)
	if err != nil {
		return SpecBoard{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return SpecBoard{}, err
	} else if affected != 1 {
		return SpecBoard{}, fmt.Errorf("%w: spec board changed", ErrConflict)
	}
	board, err := readSpecBoardTx(ctx, tx, agentID)
	if err != nil {
		return SpecBoard{}, err
	}
	if err := tx.Commit(); err != nil {
		return SpecBoard{}, err
	}
	return board, nil
}

func (s *Store) CreateGoal(ctx context.Context, agentID, text string) (SpecBoard, GoalConfirmation, error) {
	agentID = strings.TrimSpace(agentID)
	text = strings.TrimSpace(text)
	task := SpecTask{ID: NewID(), AgentID: agentID, Text: text, Status: "todo", Protected: true, SourceType: "goal", SourceID: NewID()}
	if err := validateSpecTask(task); err != nil {
		return SpecBoard{}, GoalConfirmation{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SpecBoard{}, GoalConfirmation{}, err
	}
	defer tx.Rollback()
	now := Now()
	if err := ensureSpecBoardTx(ctx, tx, agentID, now); err != nil {
		return SpecBoard{}, GoalConfirmation{}, err
	}
	var busy int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE agent_id = ? AND status IN ('pending','running'))`, agentID).Scan(&busy); err != nil {
		return SpecBoard{}, GoalConfirmation{}, err
	}
	queueState := "idle"
	if busy != 0 {
		queueState = "busy"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE goal_confirmations SET status = 'superseded' WHERE agent_id = ? AND status = 'confirmed'`, agentID); err != nil {
		return SpecBoard{}, GoalConfirmation{}, err
	}
	var position int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position),-1)+1 FROM spec_tasks WHERE agent_id = ?`, agentID).Scan(&position); err != nil {
		return SpecBoard{}, GoalConfirmation{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO spec_tasks (id, agent_id, text, status, protected, position, revision, source_type, source_id, created_at, updated_at) VALUES (?, ?, ?, 'todo', 1, ?, 1, 'goal', ?, ?, ?)`, task.ID, agentID, task.Text, position, task.SourceID, now, now); err != nil {
		return SpecBoard{}, GoalConfirmation{}, err
	}
	confirmation := GoalConfirmation{ID: NewID(), AgentID: agentID, TaskID: task.ID, QueueState: queueState, Status: "confirmed", CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO goal_confirmations (id, agent_id, task_id, queue_state, status, created_at) VALUES (?, ?, ?, ?, 'confirmed', ?)`, confirmation.ID, confirmation.AgentID, confirmation.TaskID, confirmation.QueueState, confirmation.CreatedAt); err != nil {
		return SpecBoard{}, GoalConfirmation{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE spec_boards SET revision = revision + 1, updated_at = ? WHERE agent_id = ?`, now, agentID); err != nil {
		return SpecBoard{}, GoalConfirmation{}, err
	}
	if err := insertSpecAuditTx(ctx, tx, agentID, task.ID, "goal.confirm", "local-api"); err != nil {
		return SpecBoard{}, GoalConfirmation{}, err
	}
	board, err := readSpecBoardTx(ctx, tx, agentID)
	if err != nil {
		return SpecBoard{}, GoalConfirmation{}, err
	}
	if err := tx.Commit(); err != nil {
		return SpecBoard{}, GoalConfirmation{}, err
	}
	return board, confirmation, nil
}

func insertSpecAuditTx(ctx context.Context, tx *sql.Tx, agentID, taskID, action, actor string) error {
	if strings.TrimSpace(actor) == "" {
		actor = "local-api"
	}
	details, _ := json.Marshal(map[string]any{"protected": true})
	event, err := canonicalAutomationAuditEvent(AutomationAuditEvent{Category: "spec", Action: action, Actor: actor, AgentID: agentID, SubjectType: "spec_task", SubjectID: taskID, Outcome: "success", Risk: "medium", DetailsJSON: details})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO automation_audit_events (id, category, action, actor, agent_id, run_id, subject_type, subject_id, outcome, risk, details_json, created_at) VALUES (?, ?, ?, ?, NULLIF(?,''), NULL, ?, ?, ?, ?, ?, ?)`, event.ID, event.Category, event.Action, event.Actor, event.AgentID, event.SubjectType, event.SubjectID, event.Outcome, event.Risk, string(event.DetailsJSON), event.CreatedAt)
	return err
}

var aggregateNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,119}$`)

func validateAggregate(name, mode string, members []string) ([]string, error) {
	name = strings.TrimSpace(name)
	if !aggregateNamePattern.MatchString(name) {
		return nil, errors.New("invalid model aggregate name")
	}
	if mode == "" {
		mode = "priority"
	}
	if mode != "priority" {
		return nil, errors.New("only priority model aggregates are supported")
	}
	if len(members) == 0 || len(members) > ModelAggregateMaxMembers {
		return nil, errors.New("model aggregate members must contain 1 to 32 items")
	}
	seen := make(map[string]struct{}, len(members))
	out := make([]string, 0, len(members))
	for _, member := range members {
		member = strings.TrimSpace(member)
		parts := strings.SplitN(member, ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" || strings.EqualFold(strings.TrimSpace(parts[0]), "aggregate") || len(member) > 256 {
			return nil, errors.New("aggregate members must be non-aggregate provider:model references")
		}
		if _, ok := seen[member]; ok {
			return nil, errors.New("model aggregate members must be unique")
		}
		seen[member] = struct{}{}
		out = append(out, member)
	}
	return out, nil
}

func (s *Store) ListModelAggregates(ctx context.Context) ([]ModelAggregate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, mode, revision, updated_at FROM model_aggregates ORDER BY name COLLATE NOCASE ASC`)
	if err != nil {
		return nil, err
	}
	var out []ModelAggregate
	for rows.Next() {
		var aggregate ModelAggregate
		if err := rows.Scan(&aggregate.ID, &aggregate.Name, &aggregate.Mode, &aggregate.Revision, &aggregate.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, aggregate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range out {
		members, err := s.modelAggregateMembers(ctx, out[index].ID)
		if err != nil {
			return nil, err
		}
		out[index].Members = members
	}
	return out, nil
}

func (s *Store) GetModelAggregate(ctx context.Context, name string) (ModelAggregate, error) {
	var aggregate ModelAggregate
	err := s.db.QueryRowContext(ctx, `SELECT id, name, mode, revision, updated_at FROM model_aggregates WHERE name = ?`, strings.TrimSpace(name)).Scan(&aggregate.ID, &aggregate.Name, &aggregate.Mode, &aggregate.Revision, &aggregate.UpdatedAt)
	if err != nil {
		return ModelAggregate{}, err
	}
	aggregate.Members, err = s.modelAggregateMembers(ctx, aggregate.ID)
	return aggregate, err
}

func (s *Store) modelAggregateMembers(ctx context.Context, aggregateID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT model_ref FROM model_aggregate_members WHERE aggregate_id = ? ORDER BY position ASC`, aggregateID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []string
	for rows.Next() {
		var member string
		if err := rows.Scan(&member); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

func (s *Store) UpsertModelAggregate(ctx context.Context, aggregate ModelAggregate, expectedRevision int64) (ModelAggregate, error) {
	aggregate.Name = strings.TrimSpace(aggregate.Name)
	if aggregate.Mode == "" {
		aggregate.Mode = "priority"
	}
	members, err := validateAggregate(aggregate.Name, aggregate.Mode, aggregate.Members)
	if err != nil {
		return ModelAggregate{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ModelAggregate{}, err
	}
	defer tx.Rollback()
	now := Now()
	var existingID string
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT id, revision FROM model_aggregates WHERE name = ?`, aggregate.Name).Scan(&existingID, &revision)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if expectedRevision != 0 {
			return ModelAggregate{}, fmt.Errorf("%w: model aggregate does not exist", ErrConflict)
		}
		if aggregate.ID == "" {
			aggregate.ID = NewID()
		}
		aggregate.Revision = 1
		if _, err := tx.ExecContext(ctx, `INSERT INTO model_aggregates (id, name, mode, revision, updated_at) VALUES (?, ?, 'priority', 1, ?)`, aggregate.ID, aggregate.Name, now); err != nil {
			return ModelAggregate{}, err
		}
	case err != nil:
		return ModelAggregate{}, err
	default:
		if expectedRevision < 1 || revision != expectedRevision {
			return ModelAggregate{}, fmt.Errorf("%w: model aggregate changed", ErrConflict)
		}
		aggregate.ID = existingID
		aggregate.Revision = revision + 1
		result, err := tx.ExecContext(ctx, `UPDATE model_aggregates SET mode = 'priority', revision = revision + 1, updated_at = ? WHERE id = ? AND revision = ?`, now, existingID, expectedRevision)
		if err != nil {
			return ModelAggregate{}, err
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			if err != nil {
				return ModelAggregate{}, err
			}
			return ModelAggregate{}, fmt.Errorf("%w: model aggregate changed", ErrConflict)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM model_aggregate_members WHERE aggregate_id = ?`, aggregate.ID); err != nil {
			return ModelAggregate{}, err
		}
	}
	for position, member := range members {
		if _, err := tx.ExecContext(ctx, `INSERT INTO model_aggregate_members (aggregate_id, position, model_ref) VALUES (?, ?, ?)`, aggregate.ID, position, member); err != nil {
			return ModelAggregate{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ModelAggregate{}, err
	}
	return s.GetModelAggregate(ctx, aggregate.Name)
}

func (s *Store) DeleteModelAggregate(ctx context.Context, name string, expectedRevision int64) error {
	if expectedRevision < 1 {
		return errors.New("model aggregate expected revision is required")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM model_aggregates WHERE name = ? AND revision = ?`, strings.TrimSpace(name), expectedRevision)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return fmt.Errorf("%w: model aggregate changed", ErrConflict)
	}
	return nil
}

func normalizeSafeJSONObject(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if len(raw) > ExecutionPayloadMaxBytes {
		return nil, errors.New("execution JSON exceeds size limit")
	}
	var value map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("execution JSON must be an object")
	}
	if err := rejectSensitiveExecutionKeys(value); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func rejectSensitiveExecutionKeys(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.ToLower(strings.NewReplacer("-", "", "_", "", " ", "").Replace(key))
			switch normalized {
			case "secret", "secrets", "token", "password", "apikey", "authorization", "credential", "credentials", "rawinput":
				return errors.New("execution JSON contains a sensitive key")
			}
			if err := rejectSensitiveExecutionKeys(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := rejectSensitiveExecutionKeys(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func scanExecutionDevice(scan func(...any) error) (ExecutionDevice, error) {
	var device ExecutionDevice
	var enabled int
	var capabilities string
	err := scan(&device.ID, &device.Kind, &device.Name, &enabled, &device.Status, &capabilities, &device.CreatedAt, &device.UpdatedAt)
	device.Enabled = enabled != 0
	device.Capabilities = json.RawMessage(capabilities)
	return device, err
}

func (s *Store) ListExecutionDevices(ctx context.Context) ([]ExecutionDevice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, name, enabled, status, capabilities_json, created_at, updated_at FROM execution_devices ORDER BY kind ASC, name COLLATE NOCASE ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExecutionDevice
	for rows.Next() {
		device, err := scanExecutionDevice(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, device)
	}
	return out, rows.Err()
}

func (s *Store) GetExecutionDevice(ctx context.Context, id string) (ExecutionDevice, error) {
	return scanExecutionDevice(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT id, kind, name, enabled, status, capabilities_json, created_at, updated_at FROM execution_devices WHERE id = ?`, strings.TrimSpace(id)).Scan(dest...)
	})
}

func (s *Store) RegisterRemoteExecutionDevice(ctx context.Context, registration ExecutionDeviceRegistration) (ExecutionDevice, error) {
	registration.ID = strings.TrimSpace(registration.ID)
	registration.Name = strings.TrimSpace(registration.Name)
	registration.IdentityFingerprint = strings.TrimSpace(registration.IdentityFingerprint)
	if registration.ID == "" {
		registration.ID = NewID()
	}
	if registration.Name == "" || len(registration.Name) > 120 || len(registration.IdentityFingerprint) < 16 || len(registration.IdentityFingerprint) > 512 {
		return ExecutionDevice{}, errors.New("invalid remote execution device registration")
	}
	capabilities, err := normalizeSafeJSONObject(registration.Capabilities)
	if err != nil {
		return ExecutionDevice{}, err
	}
	now := Now()
	_, err = s.db.ExecContext(ctx, `INSERT INTO execution_devices (id, kind, name, enabled, status, capabilities_json, identity_fingerprint, created_at, updated_at) VALUES (?, 'remote', ?, 0, 'disabled', ?, ?, ?, ?)`, registration.ID, registration.Name, string(capabilities), registration.IdentityFingerprint, now, now)
	if err != nil {
		return ExecutionDevice{}, err
	}
	return s.GetExecutionDevice(ctx, registration.ID)
}

func (s *Store) SetExecutionDeviceEnabled(ctx context.Context, id string, enabled bool) (ExecutionDevice, error) {
	if strings.TrimSpace(id) == "local" && !enabled {
		return ExecutionDevice{}, errors.New("local execution device cannot be disabled")
	}
	status := "disabled"
	if enabled {
		status = "unknown"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE execution_devices SET enabled = ?, status = ?, updated_at = ? WHERE id = ? AND kind = 'remote'`, boolInt(enabled), status, Now(), strings.TrimSpace(id))
	if err != nil {
		return ExecutionDevice{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return ExecutionDevice{}, err
	} else if affected != 1 {
		return ExecutionDevice{}, sql.ErrNoRows
	}
	return s.GetExecutionDevice(ctx, id)
}

func (s *Store) SetProjectDeviceGrant(ctx context.Context, grant ProjectDeviceGrant) (ProjectDeviceGrant, error) {
	grant.ProjectID = strings.TrimSpace(grant.ProjectID)
	grant.DeviceID = strings.TrimSpace(grant.DeviceID)
	capabilities, err := normalizeSafeJSONObject(grant.Capabilities)
	if err != nil {
		return ProjectDeviceGrant{}, err
	}
	if grant.DeviceID == "local" && !grant.Enabled {
		return ProjectDeviceGrant{}, errors.New("local project execution cannot be disabled")
	}
	grant.Capabilities = capabilities
	grant.UpdatedAt = Now()
	_, err = s.db.ExecContext(ctx, `INSERT INTO project_device_grants (project_id, device_id, enabled, capabilities_json, updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(project_id, device_id) DO UPDATE SET enabled = excluded.enabled, capabilities_json = excluded.capabilities_json, updated_at = excluded.updated_at`, grant.ProjectID, grant.DeviceID, boolInt(grant.Enabled), string(grant.Capabilities), grant.UpdatedAt)
	return grant, err
}

func (s *Store) SetAgentExecutionDevice(ctx context.Context, agentID, deviceID string) (Agent, error) {
	agentID = strings.TrimSpace(agentID)
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return Agent{}, errors.New("execution device is required")
	}
	if deviceID != "local" {
		var allowed int
		err := s.db.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM agents a
			JOIN worklines w ON w.id = a.workline_id
			JOIN execution_devices d ON d.id = ? AND d.kind = 'remote' AND d.enabled = 1 AND d.status IN ('online','ready')
			JOIN project_device_grants g ON g.project_id = w.project_id AND g.device_id = d.id AND g.enabled = 1
			WHERE a.id = ?
		)`, deviceID, agentID).Scan(&allowed)
		if err != nil {
			return Agent{}, err
		}
		if allowed == 0 {
			return Agent{}, fmt.Errorf("%w: remote execution device is disabled, unavailable, or unauthorized", ErrConflict)
		}
	}
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET execution_device_id = ?, entity_generation = entity_generation + 1, permission_generation = permission_generation + 1, updated_at = ? WHERE id = ?`, deviceID, Now(), agentID)
	if err != nil {
		return Agent{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Agent{}, err
	} else if affected != 1 {
		return Agent{}, sql.ErrNoRows
	}
	return s.GetAgent(ctx, agentID)
}

func (s *Store) CreateRemoteExecutionTask(ctx context.Context, task RemoteExecutionTask) (RemoteExecutionTask, error) {
	task.IdempotencyKey = strings.TrimSpace(task.IdempotencyKey)
	task.ProjectID = strings.TrimSpace(task.ProjectID)
	task.AgentID = strings.TrimSpace(task.AgentID)
	task.RunID = strings.TrimSpace(task.RunID)
	task.ExecutionDeviceID = strings.TrimSpace(task.ExecutionDeviceID)
	if task.ID == "" {
		task.ID = NewID()
	}
	if task.IdempotencyKey == "" || len(task.IdempotencyKey) > 256 || task.ExecutionDeviceID == "" || task.ExecutionDeviceID == "local" {
		return RemoteExecutionTask{}, errors.New("invalid remote execution task identity")
	}
	payload, err := normalizeSafeJSONObject(task.Payload)
	if err != nil {
		return RemoteExecutionTask{}, err
	}
	var allowed int
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM agents a
		JOIN worklines w ON w.id = a.workline_id AND w.project_id = ?
		JOIN execution_devices d ON d.id = ? AND d.kind = 'remote' AND d.enabled = 1 AND d.status IN ('online','ready')
		JOIN project_device_grants g ON g.project_id = w.project_id AND g.device_id = d.id AND g.enabled = 1
		WHERE a.id = ?
	)`, task.ProjectID, task.ExecutionDeviceID, task.AgentID).Scan(&allowed); err != nil {
		return RemoteExecutionTask{}, err
	}
	if allowed == 0 {
		return RemoteExecutionTask{}, fmt.Errorf("%w: remote execution target is unavailable or unauthorized", ErrConflict)
	}
	now := Now()
	task.Status = "queued"
	task.Payload = payload
	task.Result = json.RawMessage(`{}`)
	task.NoFallback = true
	task.Revision = 1
	task.CreatedAt, task.UpdatedAt = now, now
	_, err = s.db.ExecContext(ctx, `INSERT INTO remote_execution_tasks (id, idempotency_key, project_id, agent_id, run_id, execution_device_id, status, payload_json, result_json, no_fallback, attempt_count, revision, created_at, updated_at) VALUES (?, ?, ?, ?, NULLIF(?,''), ?, 'queued', ?, '{}', 1, 0, 1, ?, ?)`, task.ID, task.IdempotencyKey, task.ProjectID, task.AgentID, task.RunID, task.ExecutionDeviceID, string(task.Payload), now, now)
	if err != nil {
		if isUniqueConstraint(err) {
			return s.GetRemoteExecutionTaskByIdempotencyKey(ctx, task.IdempotencyKey)
		}
		return RemoteExecutionTask{}, err
	}
	return task, nil
}

func (s *Store) GetRemoteExecutionTaskByIdempotencyKey(ctx context.Context, key string) (RemoteExecutionTask, error) {
	return scanRemoteExecutionTask(s.db.QueryRowContext(ctx, `SELECT id, idempotency_key, project_id, agent_id, COALESCE(run_id,''), execution_device_id, status, payload_json, result_json, no_fallback, COALESCE(lease_owner,''), COALESCE(lease_until,''), attempt_count, COALESCE(last_error,''), revision, created_at, updated_at, COALESCE(completed_at,'') FROM remote_execution_tasks WHERE idempotency_key = ?`, strings.TrimSpace(key)).Scan)
}

func (s *Store) GetRemoteExecutionTask(ctx context.Context, id string) (RemoteExecutionTask, error) {
	return scanRemoteExecutionTask(s.db.QueryRowContext(ctx, `SELECT id, idempotency_key, project_id, agent_id, COALESCE(run_id,''), execution_device_id, status, payload_json, result_json, no_fallback, COALESCE(lease_owner,''), COALESCE(lease_until,''), attempt_count, COALESCE(last_error,''), revision, created_at, updated_at, COALESCE(completed_at,'') FROM remote_execution_tasks WHERE id = ?`, strings.TrimSpace(id)).Scan)
}

func scanRemoteExecutionTask(scan func(...any) error) (RemoteExecutionTask, error) {
	var task RemoteExecutionTask
	var payload, result string
	var noFallback int
	err := scan(&task.ID, &task.IdempotencyKey, &task.ProjectID, &task.AgentID, &task.RunID, &task.ExecutionDeviceID, &task.Status, &payload, &result, &noFallback, &task.LeaseOwner, &task.LeaseUntil, &task.AttemptCount, &task.LastError, &task.Revision, &task.CreatedAt, &task.UpdatedAt, &task.CompletedAt)
	task.Payload = json.RawMessage(payload)
	task.Result = json.RawMessage(result)
	task.NoFallback = noFallback != 0
	return task, err
}

func (s *Store) TransitionRemoteExecutionTask(ctx context.Context, id string, expectedRevision int64, status string, resultJSON json.RawMessage, lastError string) (RemoteExecutionTask, error) {
	current, err := s.GetRemoteExecutionTask(ctx, id)
	if err != nil {
		return RemoteExecutionTask{}, err
	}
	if current.Revision != expectedRevision {
		return RemoteExecutionTask{}, fmt.Errorf("%w: remote execution task changed", ErrConflict)
	}
	if !validRemoteTransition(current.Status, status) {
		return RemoteExecutionTask{}, errors.New("invalid remote execution task transition")
	}
	result, err := normalizeSafeJSONObject(resultJSON)
	if err != nil {
		return RemoteExecutionTask{}, err
	}
	lastError = strings.TrimSpace(lastError)
	if status == "failed" && lastError == "" {
		return RemoteExecutionTask{}, errors.New("failed remote execution task requires an error")
	}
	terminal := status == "succeeded" || status == "failed" || status == "cancelled" || status == "expired"
	completedAt := ""
	leaseOwner, leaseUntil := current.LeaseOwner, current.LeaseUntil
	if terminal {
		completedAt = Now()
		leaseOwner, leaseUntil = "", ""
	}
	if status == "running" && (leaseOwner == "" || leaseUntil == "") {
		return RemoteExecutionTask{}, errors.New("running remote execution task requires an active lease")
	}
	update, err := s.db.ExecContext(ctx, `UPDATE remote_execution_tasks SET status = ?, result_json = ?, last_error = NULLIF(?,''), lease_owner = NULLIF(?,''), lease_until = NULLIF(?,''), completed_at = NULLIF(?,''), revision = revision + 1, updated_at = ? WHERE id = ? AND revision = ?`, status, string(result), lastError, leaseOwner, leaseUntil, completedAt, Now(), id, expectedRevision)
	if err != nil {
		return RemoteExecutionTask{}, err
	}
	if affected, err := update.RowsAffected(); err != nil {
		return RemoteExecutionTask{}, err
	} else if affected != 1 {
		return RemoteExecutionTask{}, fmt.Errorf("%w: remote execution task changed", ErrConflict)
	}
	return s.GetRemoteExecutionTask(ctx, id)
}

func validRemoteTransition(from, to string) bool {
	switch from {
	case "leased":
		return to == "running" || to == "cancelled" || to == "expired"
	case "running":
		return to == "succeeded" || to == "failed" || to == "cancelled" || to == "expired"
	case "queued":
		return to == "cancelled" || to == "expired"
	default:
		return false
	}
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
