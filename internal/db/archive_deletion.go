package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrNotArchived guards the destructive archive-deletion paths: callers must
// archive a project or conversation first, so a stray API call can never wipe
// something that is still visible in the sidebar.
var ErrNotArchived = errors.New("not archived")

// ErrHasActiveRun blocks deletion while durable run rows say the conversation is
// still working. Deleting mid-run would strand a live agent loop writing into
// rows that no longer exist.
var ErrHasActiveRun = errors.New("agent has an active run")

// activeRunStatuses mirrors beginContextCompaction's durable busy check.
const activeRunStatusList = `('pending','running','continuation_pending')`

// IsNotArchived reports whether err came from the archive-only guard.
func IsNotArchived(err error) bool { return errors.Is(err, ErrNotArchived) }

// HasActiveRun reports whether err came from the active-run guard.
func HasActiveRun(err error) bool { return errors.Is(err, ErrHasActiveRun) }

// assertNoActiveRunsTx fails when any of the given agents still has a durable
// run in flight. An empty agent list is trivially safe.
func assertNoActiveRunsTx(ctx context.Context, tx *sql.Tx, agentIDs []string) error {
	if len(agentIDs) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(agentIDs)), ",")
	args := make([]any, 0, len(agentIDs))
	for _, id := range agentIDs {
		args = append(args, id)
	}
	var busy int
	query := `SELECT COUNT(*) FROM runs WHERE agent_id IN (` + placeholders + `) AND status IN ` + activeRunStatusList
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&busy); err != nil {
		return err
	}
	if busy > 0 {
		return ErrHasActiveRun
	}
	return nil
}

// projectAgentIDsTx lists every agent reachable from a project's worklines.
// agents.workline_id is ON DELETE SET NULL, so deleting the project alone would
// orphan these rows instead of removing them.
func projectAgentIDsTx(ctx context.Context, tx *sql.Tx, projectID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.id FROM agents a JOIN worklines w ON a.workline_id = w.id WHERE w.project_id = ?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// deleteAgentsTx removes agents explicitly. Child rows keyed on agent_id are
// ON DELETE CASCADE, so this also clears messages, runs, tool calls and friends.
func deleteAgentsTx(ctx context.Context, tx *sql.Tx, agentIDs []string) error {
	for _, id := range agentIDs {
		// Detach self-referencing parents first: parent_agent_id is SET NULL, but
		// deleting in arbitrary order can otherwise leave dangling subagent links.
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET parent_agent_id = NULL WHERE parent_agent_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM agents WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// DeleteArchivedProject permanently removes an archived project along with its
// worklines, agents and all cascaded history.
//
// It never touches the filesystem: git worktrees and checked-out code under
// git_path stay exactly where they are. Only database rows go away.
func (s *Store) DeleteArchivedProject(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("project id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var archivedAt sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT archived_at FROM projects WHERE id = ?`, id).Scan(&archivedAt); err != nil {
		return err
	}
	if strings.TrimSpace(archivedAt.String) == "" {
		return fmt.Errorf("%w: archive the project before deleting it", ErrNotArchived)
	}

	agentIDs, err := projectAgentIDsTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := assertNoActiveRunsTx(ctx, tx, agentIDs); err != nil {
		return err
	}
	if err := deleteAgentsTx(ctx, tx, agentIDs); err != nil {
		return err
	}
	// Worklines, project_members and the rest are ON DELETE CASCADE from projects.
	result, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// DeleteArchivedAgent permanently removes an archived conversation and its
// cascaded history. When the agent was the last one inside an auto-created
// standalone-conversation project, that hollow project is removed too so the
// sidebar does not keep an empty shell around.
func (s *Store) DeleteArchivedAgent(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("agent id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var archivedAt sql.NullString
	var worklineID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT archived_at, workline_id FROM agents WHERE id = ?`, id).Scan(&archivedAt, &worklineID); err != nil {
		return err
	}
	if strings.TrimSpace(archivedAt.String) == "" {
		return fmt.Errorf("%w: archive the conversation before deleting it", ErrNotArchived)
	}
	if err := assertNoActiveRunsTx(ctx, tx, []string{id}); err != nil {
		return err
	}

	projectID := ""
	flowMode := ""
	if strings.TrimSpace(worklineID.String) != "" {
		row := tx.QueryRowContext(ctx, `SELECT p.id, p.flow_mode FROM projects p JOIN worklines w ON w.project_id = p.id WHERE w.id = ?`, worklineID.String)
		if err := row.Scan(&projectID, &flowMode); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}

	if err := deleteAgentsTx(ctx, tx, []string{id}); err != nil {
		return err
	}

	if projectID != "" && flowMode == ProjectFlowModeConversation {
		var remaining int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents a JOIN worklines w ON a.workline_id = w.id WHERE w.project_id = ?`, projectID).Scan(&remaining); err != nil {
			return err
		}
		if remaining == 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, projectID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
