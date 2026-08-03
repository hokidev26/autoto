package background

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"autoto/internal/agent"
	"autoto/internal/db"
	"autoto/internal/tools"
)

// Service is the concrete tools.BackgroundTaskService boundary. It deliberately
// projects database records into public DTOs so task payloads and worker details
// never leave the background subsystem.
type Service struct {
	manager *Manager
	store   *db.Store
	runner  *agent.Runner
}

var _ tools.BackgroundTaskService = (*Service)(nil)

func NewService(manager *Manager, store *db.Store, runners ...*agent.Runner) *Service {
	var runner *agent.Runner
	if len(runners) > 0 {
		runner = runners[0]
	}
	return &Service{manager: manager, store: store, runner: runner}
}

func (s *Service) PreflightAgentTask(ctx context.Context, request tools.AgentTaskPreflightRequest) error {
	if s == nil || s.runner == nil || s.store == nil {
		return nil
	}
	parent, err := s.store.GetAgent(ctx, strings.TrimSpace(request.OwnerAgentID))
	if err != nil {
		return &tools.PreflightError{Code: "subagent_role_rejected", Message: fmt.Sprintf("load parent agent for preflight: %v", err)}
	}
	if err := validateAgentTaskNesting(s.store, ctx, parent, s.manager.BackgroundRuntimeSettings()); err != nil {
		code := "scope_rejected"
		if coded, ok := err.(interface{ ErrorCode() string }); ok && strings.TrimSpace(coded.ErrorCode()) != "" {
			code = strings.TrimSpace(coded.ErrorCode())
		}
		return &tools.PreflightError{Code: code, Message: err.Error()}
	}
	requested := strings.ToLower(strings.TrimSpace(request.SubagentType))
	if requested == "general-purpose" {
		requested = "general"
	}
	resolution, resolveErr := s.runner.ResolveChildRole(ctx, parent.ID, request.ParentRunID, requested)
	if resolveErr != nil {
		return &tools.PreflightError{Code: "subagent_role_rejected", Message: resolveErr.Error()}
	}
	role := resolution.ModelRole
	if explicit := strings.TrimSpace(request.ExplicitModel); explicit != "" {
		if _, _, resolveErr := s.runner.ResolveSubagentModel(role, explicit, parent.Model); resolveErr != nil {
			return &tools.PreflightError{Code: "subagent_model_rejected", Message: resolveErr.Error()}
		}
	}
	return nil
}

func (s *Service) Submit(ctx context.Context, request tools.BackgroundTaskRequest) (tools.BackgroundTask, error) {
	if s == nil || s.manager == nil {
		return tools.BackgroundTask{}, ErrClosed
	}
	request.Kind = strings.ToLower(strings.TrimSpace(request.Kind))
	request.OwnerAgentID = strings.TrimSpace(request.OwnerAgentID)
	if request.OwnerAgentID == "" {
		return tools.BackgroundTask{}, errors.New("background task owner is required")
	}
	if request.Kind != tools.BackgroundTaskKindShell && request.Kind != tools.BackgroundTaskKindAgent {
		return tools.BackgroundTask{}, errors.New("invalid background task kind")
	}
	created, err := s.manager.Submit(ctx, db.BackgroundTask{
		OwnerAgentID: request.OwnerAgentID, ParentRunID: strings.TrimSpace(request.ParentRunID), ParentToolUseID: strings.TrimSpace(request.ParentToolUseID),
		Kind: request.Kind, PayloadJSON: append(json.RawMessage(nil), request.Payload...), PublicSummaryJSON: append(json.RawMessage(nil), request.PublicSummary...),
		ResumeParent: request.ResumeParent, PermissionModeCap: strings.TrimSpace(request.PermissionModeCap),
		PermissionGenerationSnapshot: request.PermissionGenerationSnapshot, PolicyGenerationSnapshot: request.PolicyGenerationSnapshot,
		AgentGenerationSnapshot: request.AgentGenerationSnapshot, ToolCatalogDigest: strings.TrimSpace(request.ToolCatalogDigest), WorkspaceFingerprint: strings.TrimSpace(request.WorkspaceFingerprint),
	})
	if err != nil {
		return tools.BackgroundTask{}, err
	}
	return publicTask(created), nil
}

func (s *Service) List(ctx context.Context, options tools.BackgroundTaskListOptions) ([]tools.BackgroundTask, error) {
	if s == nil || s.manager == nil {
		return nil, ErrClosed
	}
	options.OwnerAgentID = strings.TrimSpace(options.OwnerAgentID)
	if options.OwnerAgentID == "" {
		return nil, errors.New("background task owner is required")
	}
	statuses := []string(nil)
	if status := strings.TrimSpace(options.Status); status != "" {
		statuses = []string{status}
	}
	tasks, err := s.manager.List(ctx, db.BackgroundTaskListOptions{OwnerAgentID: options.OwnerAgentID, Statuses: statuses, Limit: options.Limit})
	if err != nil {
		return nil, err
	}
	out := make([]tools.BackgroundTask, 0, len(tasks))
	for _, task := range tasks {
		if kind := strings.TrimSpace(options.Kind); kind != "" && task.Kind != kind {
			continue
		}
		out = append(out, publicTask(task))
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, ownerAgentID, taskID string) (tools.BackgroundTask, error) {
	task, err := s.scopedTask(ctx, ownerAgentID, taskID)
	if err != nil {
		return tools.BackgroundTask{}, err
	}
	return publicTask(task), nil
}

func (s *Service) Output(ctx context.Context, ownerAgentID, taskID string, afterSequence int64, byteLimit int) (tools.BackgroundTaskOutputPage, error) {
	task, err := s.scopedTask(ctx, ownerAgentID, taskID)
	if err != nil {
		return tools.BackgroundTaskOutputPage{}, err
	}
	page, err := s.manager.ListOutput(ctx, task.ID, afterSequence, byteLimit)
	if err != nil {
		return tools.BackgroundTaskOutputPage{}, err
	}
	out := tools.BackgroundTaskOutputPage{TaskID: task.ID, Chunks: make([]tools.BackgroundTaskOutputChunk, 0, len(page.Items)), NextSequence: page.NextSequence, HasMore: page.HasMore, Truncated: page.Truncated}
	for _, item := range page.Items {
		out.Chunks = append(out.Chunks, tools.BackgroundTaskOutputChunk{Sequence: item.Sequence, Stream: item.Stream, Text: validUTF8Text(item.Chunk), ByteCount: item.ByteCount, CreatedAt: item.CreatedAt})
	}
	return out, nil
}

func (s *Service) Wait(ctx context.Context, ownerAgentID, taskID string, timeoutMS int64) (tools.BackgroundTask, error) {
	task, err := s.scopedTask(ctx, ownerAgentID, taskID)
	if err != nil {
		return tools.BackgroundTask{}, err
	}
	if timeoutMS <= 0 {
		return tools.BackgroundTask{}, errors.New("background task wait timeout must be positive")
	}
	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	updated, err := s.manager.Wait(waitCtx, task.ID)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			current, getErr := s.manager.Get(ctx, task.ID)
			if getErr != nil {
				return tools.BackgroundTask{}, getErr
			}
			return publicTask(current), nil
		}
		return tools.BackgroundTask{}, err
	}
	return publicTask(updated), nil
}

func (s *Service) Cancel(ctx context.Context, ownerAgentID, taskID string) (tools.BackgroundTask, error) {
	task, err := s.scopedTask(ctx, ownerAgentID, taskID)
	if err != nil {
		return tools.BackgroundTask{}, err
	}
	updated, err := s.manager.Cancel(ctx, task.ID)
	if err != nil {
		return tools.BackgroundTask{}, err
	}
	return publicTask(updated), nil
}

// scopedTask allows an empty owner only for server-side lookup before its
// authorization check. All tool calls pass their concrete owner id.
func (s *Service) scopedTask(ctx context.Context, ownerAgentID, taskID string) (db.BackgroundTask, error) {
	if s == nil || s.manager == nil {
		return db.BackgroundTask{}, ErrClosed
	}
	task, err := s.manager.Get(ctx, strings.TrimSpace(taskID))
	if err != nil {
		return db.BackgroundTask{}, err
	}
	ownerAgentID = strings.TrimSpace(ownerAgentID)
	if ownerAgentID != "" && task.OwnerAgentID != ownerAgentID {
		return db.BackgroundTask{}, sql.ErrNoRows
	}
	return task, nil
}

func publicTask(task db.BackgroundTask) tools.BackgroundTask {
	return tools.BackgroundTask{
		ID: task.ID, OwnerAgentID: task.OwnerAgentID, ParentRunID: task.ParentRunID, ParentToolUseID: task.ParentToolUseID,
		Kind: task.Kind, Status: task.Status, Revision: task.Revision, ResumeParent: task.ResumeParent,
		ChildAgentID: task.ChildAgentID, ChildRunID: task.ChildRunID, PublicSummary: append(json.RawMessage(nil), task.PublicSummaryJSON...),
		Result: append(json.RawMessage(nil), task.ResultJSON...), ErrorCode: task.ErrorCode, ErrorMessage: publicError(task.ErrorMessage),
		ExitCode: task.ExitCode, OutputBytes: task.OutputBytes, OutputTruncated: task.OutputTruncated,
		CreatedAt: task.CreatedAt, StartedAt: task.StartedAt, CompletedAt: task.CompletedAt, UpdatedAt: task.UpdatedAt,
	}
}

func validUTF8Text(value []byte) string {
	text := string(value)
	if utf8.ValidString(text) {
		return text
	}
	return strings.ToValidUTF8(text, "�")
}

func publicError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 1024 {
		value = value[:1024]
		for !utf8.ValidString(value) && len(value) > 0 {
			value = value[:len(value)-1]
		}
	}
	return value
}

// NewManagerHooks installs safe task lifecycle publication and terminal
// behavior. Events intentionally contain identifiers and state only.
func NewManagerHooks(hub *agent.Hub, notifier agent.Notifier, runner *agent.Runner) (TaskEventHook, TerminalHook) {
	eventHook := func(_ context.Context, event string, task db.BackgroundTask) {
		if hub == nil {
			return
		}
		hub.Publish(agent.Event{Type: "task." + event, AgentID: task.OwnerAgentID, Data: map[string]any{
			"taskId": task.ID, "kind": task.Kind, "status": task.Status, "revision": task.Revision,
			"outputBytes": task.OutputBytes, "outputTruncated": task.OutputTruncated,
		}})
	}
	terminalHook := func(ctx context.Context, task db.BackgroundTask) {
		if notifier != nil {
			notifier.Notify(context.Background(), agent.NotificationEvent{Event: "task_terminal", TaskID: task.ID, RunID: task.ParentRunID, AgentID: task.OwnerAgentID, Status: task.Status})
		}
		if task.ResumeParent && strings.TrimSpace(task.ParentRunID) != "" && runner != nil {
			if _, err := runner.WakeBackgroundContinuation(ctx, task.ParentRunID, task.ID); err != nil && !errors.Is(err, agent.ErrAgentBusy) && !errors.Is(err, db.ErrConflict) && notifier != nil {
				notifier.Notify(context.Background(), agent.NotificationEvent{Event: "continuation_blocked", TaskID: task.ID, RunID: task.ParentRunID, AgentID: task.OwnerAgentID, Status: "blocked"})
			}
		}
	}
	return eventHook, terminalHook
}
