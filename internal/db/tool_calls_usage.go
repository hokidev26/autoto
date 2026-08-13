package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func (s *toolCallStore) AddToolCall(ctx context.Context, call ToolCall) (ToolCall, error) {
	if call.ID == "" {
		call.ID = NewID()
	}
	if call.CreatedAt == "" {
		call.CreatedAt = Now()
	}
	if call.UpdatedAt == "" {
		call.UpdatedAt = call.CreatedAt
	}
	if call.PermissionGeneration < 1 {
		call.PermissionGeneration = 1
	}
	if call.PolicyGeneration < 1 {
		call.PolicyGeneration = 1
	}
	switch call.Status {
	case "pending_approval", "approved":
		call.StartedAt = ""
		call.CompletedAt = ""
	case "running":
		if call.StartedAt == "" {
			call.StartedAt = call.CreatedAt
		}
		call.CompletedAt = ""
	case "completed", "error", "succeeded", "failed":
		if call.StartedAt == "" {
			call.StartedAt = call.CreatedAt
		}
		if call.CompletedAt == "" {
			call.CompletedAt = call.CreatedAt
		}
	case "denied":
		call.StartedAt = ""
		if call.CompletedAt == "" {
			call.CompletedAt = call.CreatedAt
		}
	default:
		return ToolCall{}, fmt.Errorf("invalid tool call status %q", call.Status)
	}
	call.ExecutionDeviceID = strings.TrimSpace(call.ExecutionDeviceID)
	if call.ExecutionDeviceID == "" && strings.TrimSpace(call.RunID) != "" {
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(execution_device_id,'local') FROM runs WHERE id = ? AND agent_id = ?`, call.RunID, call.AgentID).Scan(&call.ExecutionDeviceID); err != nil {
			return ToolCall{}, err
		}
	}
	if call.ExecutionDeviceID == "" {
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(execution_device_id,'local') FROM agents WHERE id = ?`, call.AgentID).Scan(&call.ExecutionDeviceID); err != nil {
			return ToolCall{}, err
		}
	}
	if err := validateP2P3Text("tool call execution device id", call.ExecutionDeviceID, 128, true, false); err != nil {
		return ToolCall{}, err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_tool_calls (id, agent_id, run_id, message_id, tool_use_id, tool_name, input_json, output_json, status, duration_ms, error_message, permission_decided_by, permission_decided_at, permission_deny_message, permission_decision_reason, permission_suggestions, permission_generation, policy_generation, execution_device_id, started_at, completed_at, created_at, updated_at) VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?)`, call.ID, call.AgentID, call.RunID, call.MessageID, call.ToolUseID, call.ToolName, string(call.InputJSON), string(call.OutputJSON), call.Status, call.DurationMS, call.ErrorMessage, call.PermissionDecidedBy, call.PermissionDecidedAt, call.PermissionDenyMessage, call.PermissionDecisionReason, call.PermissionSuggestions, call.PermissionGeneration, call.PolicyGeneration, call.ExecutionDeviceID, call.StartedAt, call.CompletedAt, call.CreatedAt, call.UpdatedAt)
	if err != nil {
		return ToolCall{}, err
	}
	return call, nil
}

func (s *toolCallStore) GetToolCallByUseID(ctx context.Context, agentID, toolUseID string) (ToolCall, error) {
	var c ToolCall
	var input, output string
	err := s.db.QueryRowContext(ctx, `SELECT id, agent_id, COALESCE(run_id,''), COALESCE(message_id,''), tool_use_id, tool_name, COALESCE(input_json,''), COALESCE(output_json,''), status, COALESCE(duration_ms,0), COALESCE(error_message,''), COALESCE(permission_decided_by,''), COALESCE(permission_decided_at,''), COALESCE(permission_deny_message,''), COALESCE(permission_decision_reason,''), COALESCE(permission_suggestions,''), COALESCE(permission_generation,1), COALESCE(policy_generation,1), COALESCE(execution_device_id,'local'), COALESCE(started_at,''), COALESCE(completed_at,''), created_at, COALESCE(updated_at, created_at) FROM agent_tool_calls WHERE agent_id = ? AND tool_use_id = ?`, agentID, toolUseID).Scan(&c.ID, &c.AgentID, &c.RunID, &c.MessageID, &c.ToolUseID, &c.ToolName, &input, &output, &c.Status, &c.DurationMS, &c.ErrorMessage, &c.PermissionDecidedBy, &c.PermissionDecidedAt, &c.PermissionDenyMessage, &c.PermissionDecisionReason, &c.PermissionSuggestions, &c.PermissionGeneration, &c.PolicyGeneration, &c.ExecutionDeviceID, &c.StartedAt, &c.CompletedAt, &c.CreatedAt, &c.UpdatedAt)
	if input != "" {
		c.InputJSON = json.RawMessage(input)
	}
	if output != "" {
		c.OutputJSON = json.RawMessage(output)
	}
	return c, err
}

func (s *toolCallStore) UpdateToolCallApproval(ctx context.Context, agentID, toolUseID, status, decidedBy, denyMessage, reason, suggestions string) error {
	if status != "approved" && status != "denied" {
		return fmt.Errorf("invalid tool approval status %q", status)
	}
	decidedAt := Now()
	completedAt := ""
	if status == "denied" {
		completedAt = decidedAt
	}
	_, err := s.db.ExecContext(ctx, `UPDATE agent_tool_calls SET status = ?, completed_at = COALESCE(NULLIF(completed_at, ''), NULLIF(?, '')), permission_decided_by = NULLIF(?, ''), permission_decided_at = ?, permission_deny_message = NULLIF(?, ''), permission_decision_reason = NULLIF(?, ''), permission_suggestions = NULLIF(?, ''), updated_at = ? WHERE agent_id = ? AND tool_use_id = ? AND status = 'pending_approval'`, status, completedAt, decidedBy, decidedAt, denyMessage, reason, suggestions, decidedAt, agentID, toolUseID)
	return err
}

// MarkToolCallRunning claims an approved pending call immediately before execution.
func (s *toolCallStore) MarkToolCallRunning(ctx context.Context, agentID, toolUseID string) error {
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE agent_tool_calls SET status = 'running', started_at = COALESCE(NULLIF(started_at, ''), ?), completed_at = NULL, updated_at = ? WHERE agent_id = ? AND tool_use_id = ? AND status = 'approved'`, now, now, agentID, toolUseID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	return fmt.Errorf("%w: tool call cannot start: %s", ErrConflict, toolUseID)
}

func (s *toolCallStore) UpdateToolCallResult(ctx context.Context, agentID, toolUseID string, outputJSON json.RawMessage, status string, durationMS int64, errorMessage string) error {
	if status != "completed" && status != "error" && status != "denied" {
		return fmt.Errorf("invalid terminal tool call status %q", status)
	}
	now := Now()
	_, err := s.db.ExecContext(ctx, `UPDATE agent_tool_calls SET output_json = ?, status = ?, duration_ms = ?, completed_at = COALESCE(NULLIF(completed_at, ''), ?), error_message = NULLIF(?, ''), updated_at = ? WHERE agent_id = ? AND tool_use_id = ?`, string(outputJSON), status, durationMS, now, errorMessage, now, agentID, toolUseID)
	return err
}

func (s *toolCallStore) ListPendingToolCalls(ctx context.Context, agentID string) ([]ToolCall, error) {
	return s.listToolCalls(ctx, `WHERE agent_id = ? AND status = 'pending_approval' ORDER BY created_at ASC`, agentID)
}

func (s *toolCallStore) ListToolCallsByRun(ctx context.Context, agentID, runID string) ([]ToolCall, error) {
	return s.listToolCalls(ctx, `WHERE agent_id = ? AND run_id = ? ORDER BY created_at ASC, id ASC`, agentID, runID)
}

func (s *toolCallStore) ListToolCallsByRunWindow(ctx context.Context, agentID, runID string, limit, offset int) ([]ToolCall, error) {
	if limit <= 0 || offset < 0 {
		return nil, fmt.Errorf("invalid tool call window limit=%d offset=%d", limit, offset)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, agent_id, COALESCE(run_id,''), COALESCE(message_id,''), tool_use_id, tool_name, COALESCE(input_json,''), COALESCE(output_json,''), status, COALESCE(duration_ms,0), COALESCE(error_message,''), COALESCE(execution_device_id,'local'), COALESCE(started_at,''), COALESCE(completed_at,''), created_at, COALESCE(updated_at, created_at) FROM agent_tool_calls WHERE agent_id = ? AND run_id = ? ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, agentID, runID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	calls := make([]ToolCall, 0, limit)
	for rows.Next() {
		var call ToolCall
		var input, output string
		if err := rows.Scan(&call.ID, &call.AgentID, &call.RunID, &call.MessageID, &call.ToolUseID, &call.ToolName, &input, &output, &call.Status, &call.DurationMS, &call.ErrorMessage, &call.ExecutionDeviceID, &call.StartedAt, &call.CompletedAt, &call.CreatedAt, &call.UpdatedAt); err != nil {
			return nil, err
		}
		if input != "" {
			call.InputJSON = json.RawMessage(input)
		}
		if output != "" {
			call.OutputJSON = json.RawMessage(output)
		}
		calls = append(calls, call)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for left, right := 0, len(calls)-1; left < right; left, right = left+1, right-1 {
		calls[left], calls[right] = calls[right], calls[left]
	}
	return calls, nil
}

func (s *toolCallStore) listToolCalls(ctx context.Context, where string, args ...any) ([]ToolCall, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, agent_id, COALESCE(run_id,''), COALESCE(message_id,''), tool_use_id, tool_name, COALESCE(input_json,''), COALESCE(output_json,''), status, COALESCE(duration_ms,0), COALESCE(error_message,''), COALESCE(permission_decided_by,''), COALESCE(permission_decided_at,''), COALESCE(permission_deny_message,''), COALESCE(permission_decision_reason,''), COALESCE(permission_suggestions,''), COALESCE(permission_generation,1), COALESCE(policy_generation,1), COALESCE(execution_device_id,'local'), COALESCE(started_at,''), COALESCE(completed_at,''), created_at, COALESCE(updated_at, created_at) FROM agent_tool_calls `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	calls := make([]ToolCall, 0)
	for rows.Next() {
		var c ToolCall
		var input, output string
		if err := rows.Scan(&c.ID, &c.AgentID, &c.RunID, &c.MessageID, &c.ToolUseID, &c.ToolName, &input, &output, &c.Status, &c.DurationMS, &c.ErrorMessage, &c.PermissionDecidedBy, &c.PermissionDecidedAt, &c.PermissionDenyMessage, &c.PermissionDecisionReason, &c.PermissionSuggestions, &c.PermissionGeneration, &c.PolicyGeneration, &c.ExecutionDeviceID, &c.StartedAt, &c.CompletedAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		if input != "" {
			c.InputJSON = json.RawMessage(input)
		}
		if output != "" {
			c.OutputJSON = json.RawMessage(output)
		}
		calls = append(calls, c)
	}
	return calls, rows.Err()
}

func (s *toolCallStore) RunSummary(ctx context.Context, agentID, runID string) (RunSummary, error) {
	run, err := s.GetRun(ctx, agentID, runID)
	if err != nil {
		return RunSummary{}, err
	}
	summary := RunSummary{Run: run}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_messages WHERE agent_id = ? AND run_id = ?`, agentID, runID).Scan(&summary.MessageCount); err != nil {
		return RunSummary{}, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN status = 'pending_approval' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN status = 'denied' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END),0) FROM agent_tool_calls WHERE agent_id = ? AND run_id = ?`, agentID, runID).Scan(&summary.ToolCallCount, &summary.PendingApprovals, &summary.DeniedToolCalls, &summary.ErrorToolCalls); err != nil {
		return RunSummary{}, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(cached_input_tokens),0), COALESCE(SUM(cost_usd),0) FROM api_requests WHERE agent_id = ? AND run_id = ?`, agentID, runID).Scan(&summary.APIRequestCount, &summary.InputTokens, &summary.OutputTokens, &summary.CachedInputTokens, &summary.CostUSD); err != nil {
		return RunSummary{}, err
	}
	summary.ToolCalls, err = s.listToolCallPreviewsByRun(ctx, agentID, runID, 12)
	if err != nil {
		return RunSummary{}, err
	}
	summary.RecentMessages, err = s.listRunMessagePreviews(ctx, agentID, runID, 6)
	if err != nil {
		return RunSummary{}, err
	}
	return summary, nil
}

func (s *toolCallStore) ActiveRunSummary(ctx context.Context, agentID string) (ActiveRunSummary, error) {
	var summary ActiveRunSummary
	run, err := scanRun(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, runSelectSQL+` WHERE agent_id = ? AND status IN ('pending', 'running', 'continuation_pending') ORDER BY CASE status WHEN 'running' THEN 0 WHEN 'continuation_pending' THEN 1 ELSE 2 END, COALESCE(started_at, created_at) DESC, id DESC LIMIT 1`, agentID).Scan(dest...)
	})
	if err != nil {
		return ActiveRunSummary{}, err
	}
	summary.Run = run
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_messages WHERE agent_id = ? AND run_id = ?`, agentID, summary.Run.ID).Scan(&summary.MessageCount); err != nil {
		return ActiveRunSummary{}, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN status = 'pending_approval' THEN 1 ELSE 0 END),0) FROM agent_tool_calls WHERE agent_id = ? AND run_id = ?`, agentID, summary.Run.ID).Scan(&summary.ToolCallCount, &summary.PendingApprovals); err != nil {
		return ActiveRunSummary{}, err
	}
	summary.ToolCalls, err = s.listToolCallPreviewsByRun(ctx, agentID, summary.Run.ID, 6)
	if err != nil {
		return ActiveRunSummary{}, err
	}
	return summary, nil
}

func (s *toolCallStore) listToolCallPreviewsByRun(ctx context.Context, agentID, runID string, limit int) ([]ToolCallPreview, error) {
	if limit <= 0 || limit > 50 {
		limit = 12
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, COALESCE(run_id,''), COALESCE(message_id,''), tool_use_id, tool_name, status, COALESCE(duration_ms,0), COALESCE(error_message,''), COALESCE(permission_decided_by,''), COALESCE(permission_decided_at,''), COALESCE(started_at,''), COALESCE(completed_at,''), created_at, COALESCE(updated_at, created_at) FROM agent_tool_calls WHERE agent_id = ? AND run_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, agentID, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	calls := make([]ToolCallPreview, 0)
	for rows.Next() {
		var call ToolCallPreview
		if err := rows.Scan(&call.ID, &call.RunID, &call.MessageID, &call.ToolUseID, &call.ToolName, &call.Status, &call.DurationMS, &call.ErrorMessage, &call.PermissionDecidedBy, &call.PermissionDecidedAt, &call.StartedAt, &call.CompletedAt, &call.CreatedAt, &call.UpdatedAt); err != nil {
			return nil, err
		}
		calls = append(calls, call)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(calls)-1; i < j; i, j = i+1, j-1 {
		calls[i], calls[j] = calls[j], calls[i]
	}
	return calls, nil
}

func (s *toolCallStore) listRunMessagePreviews(ctx context.Context, agentID, runID string, limit int) ([]RunMessagePreview, error) {
	if limit <= 0 || limit > 20 {
		limit = 6
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, role, COALESCE(content_text,''), COALESCE(parent_tool_use_id,''), created_at FROM agent_messages WHERE agent_id = ? AND run_id = ? ORDER BY created_at DESC LIMIT ?`, agentID, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := make([]RunMessagePreview, 0)
	for rows.Next() {
		var message RunMessagePreview
		if err := rows.Scan(&message.ID, &message.Role, &message.ContentText, &message.ParentToolID, &message.CreatedAt); err != nil {
			return nil, err
		}
		message.ContentText = truncateRunes(message.ContentText, 280)
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return messages, nil
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func (s *apiRequestStore) AddAPIRequest(ctx context.Context, request APIRequest) (APIRequest, error) {
	if request.ID == "" {
		request.ID = NewID()
	}
	if request.CreatedAt == "" {
		request.CreatedAt = Now()
	}
	if request.Kind == "" {
		request.Kind = "model"
	}
	if request.TurnIndex < 0 || request.ContinuationIndex < 0 {
		return APIRequest{}, errors.New("api request turn indexes must not be negative")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_requests (id, agent_id, run_id, message_id, kind, provider, credential_id, gateway_key_id, model, input_tokens, output_tokens, cached_input_tokens, reasoning_tokens, ttft_ms, duration_ms, cost_usd, error_message, raw_dump_json, stop_reason, turn_index, continuation_index, created_at) VALUES (?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?)`, request.ID, request.AgentID, request.RunID, request.MessageID, request.Kind, request.Provider, request.CredentialID, request.GatewayKeyID, request.Model, request.InputTokens, request.OutputTokens, request.CachedInputTokens, request.ReasoningTokens, request.TTFTMS, request.DurationMS, request.CostUSD, request.ErrorMessage, string(request.RawDumpJSON), request.StopReason, request.TurnIndex, request.ContinuationIndex, request.CreatedAt)
	if err != nil {
		return APIRequest{}, err
	}
	return request, nil
}
