package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"autoto/internal/db"
	"autoto/internal/tools"
)

const (
	maxBackgroundTaskJSONBody = 64 * 1024
	maxBackgroundTaskList     = 100
	maxBackgroundOutputBytes  = 64 * 1024
	maxBackgroundWaitMS       = int64(30_000)
)

type createBackgroundTaskRequest struct {
	Kind            string `json:"kind"`
	Command         string `json:"command,omitempty"`
	TimeoutMS       int    `json:"timeoutMs,omitempty"`
	Prompt          string `json:"prompt,omitempty"`
	Description     string `json:"description,omitempty"`
	SubagentType    string `json:"subagentType,omitempty"`
	Model           string `json:"model,omitempty"`
	Workdir         string `json:"workdir,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	ResumeParent    bool   `json:"resumeParent,omitempty"`
}

type waitBackgroundTaskRequest struct {
	AfterRevision int64 `json:"afterRevision,omitempty"`
	TimeoutMS     int64 `json:"timeoutMs,omitempty"`
}

type backgroundTaskRequestErrorResponse struct {
	Error        string `json:"error"`
	ErrorCode    string `json:"errorCode"`
	ErrorMessage string `json:"errorMessage"`
}

func backgroundTaskRequestError(result tools.Result) backgroundTaskRequestErrorResponse {
	response := backgroundTaskRequestErrorResponse{
		Error:        "background task request was not authorized",
		ErrorCode:    "background_task_rejected",
		ErrorMessage: "background task request was not authorized",
	}
	if result.Meta != nil {
		if code, ok := result.Meta["errorCode"].(string); ok && strings.TrimSpace(code) != "" {
			response.ErrorCode = strings.TrimSpace(code)
		}
		if message, ok := result.Meta["errorMessage"].(string); ok && strings.TrimSpace(message) != "" {
			response.Error = strings.TrimSpace(message)
			response.ErrorMessage = response.Error
		}
	}
	var payload struct {
		ErrorCode    string `json:"errorCode"`
		ErrorMessage string `json:"errorMessage"`
	}
	if json.Unmarshal([]byte(result.Output), &payload) == nil {
		if strings.TrimSpace(payload.ErrorCode) != "" {
			response.ErrorCode = strings.TrimSpace(payload.ErrorCode)
		}
		if strings.TrimSpace(payload.ErrorMessage) != "" {
			response.Error = strings.TrimSpace(payload.ErrorMessage)
			response.ErrorMessage = response.Error
		}
	} else if message := publicRunErrorText(strings.TrimSpace(result.Output)); message != "" {
		response.Error = message
		response.ErrorMessage = message
	}
	return response
}

func (s *Server) requireBackgroundTasks(w http.ResponseWriter) tools.BackgroundTaskService {
	if s.backgroundTasks == nil {
		writeError(w, http.StatusServiceUnavailable, "background task service is unavailable")
		return nil
	}
	return s.backgroundTasks
}

func (s *Server) listBackgroundTasks(w http.ResponseWriter, r *http.Request) {
	service := s.requireBackgroundTasks(w)
	if service == nil {
		return
	}
	if err := rejectUnknownQuery(r, "status", "kind", "limit"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agentID := strings.TrimSpace(chi.URLParam(r, "id"))
	if err := validateAPIIdentifier("agent id", agentID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.store.GetAgent(r.Context(), agentID); err != nil {
		writeStoreError(w, err)
		return
	}
	limit, err := queryInt(r, "limit", 20, 1, maxBackgroundTaskList)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tasks, err := service.List(r.Context(), tools.BackgroundTaskListOptions{
		OwnerAgentID: agentID,
		Status:       strings.TrimSpace(r.URL.Query().Get("status")),
		Kind:         strings.TrimSpace(r.URL.Query().Get("kind")),
		Limit:        limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "background tasks could not be listed")
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (s *Server) createBackgroundTask(w http.ResponseWriter, r *http.Request) {
	if s.requireBackgroundTasks(w) == nil {
		return
	}
	if s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "agent runner is unavailable")
		return
	}
	agentID := strings.TrimSpace(chi.URLParam(r, "id"))
	if err := validateAPIIdentifier("agent id", agentID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.store.GetAgent(r.Context(), agentID); err != nil {
		writeStoreError(w, err)
		return
	}
	var req createBackgroundTaskRequest
	if err := decodeLimitedJSON(w, r, &req, maxBackgroundTaskJSONBody); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ResumeParent {
		writeError(w, http.StatusBadRequest, "resumeParent is only available from a durable agent run")
		return
	}
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	var call tools.Call
	call.ID = db.NewID()
	switch kind {
	case tools.BackgroundTaskKindShell:
		command := strings.TrimSpace(req.Command)
		if command == "" || len([]byte(command)) > maxBackgroundTaskJSONBody {
			writeError(w, http.StatusBadRequest, "valid command is required")
			return
		}
		input, _ := json.Marshal(map[string]any{"command": command, "timeout": req.TimeoutMS, "run_in_background": true})
		call.Name, call.Input = "Bash", input
	case tools.BackgroundTaskKindAgent:
		prompt := strings.TrimSpace(req.Prompt)
		if prompt == "" || len([]byte(prompt)) > maxBackgroundTaskJSONBody {
			writeError(w, http.StatusBadRequest, "valid prompt is required")
			return
		}
		input, _ := json.Marshal(map[string]any{
			"prompt": prompt, "description": strings.TrimSpace(req.Description), "subagent_type": strings.TrimSpace(req.SubagentType),
			"model": strings.TrimSpace(req.Model), "workdir": strings.TrimSpace(req.Workdir),
			"reasoning_effort": strings.TrimSpace(req.ReasoningEffort), "run_in_background": true,
		})
		call.Name, call.Input = "Agent", input
	default:
		writeError(w, http.StatusBadRequest, "kind must be shell or agent")
		return
	}
	result, err := s.runner.ExecuteTool(r.Context(), agentID, call)
	if err != nil {
		writeError(w, statusFromError(err), "background task request failed")
		return
	}
	if result.IsError {
		writeJSON(w, http.StatusConflict, backgroundTaskRequestError(result))
		return
	}
	var task tools.BackgroundTask
	if err := json.Unmarshal([]byte(result.Output), &task); err != nil || strings.TrimSpace(task.ID) == "" {
		writeError(w, http.StatusInternalServerError, "background task creation returned an invalid handle")
		return
	}
	writeJSON(w, http.StatusAccepted, task)
}

func (s *Server) backgroundTaskForRequest(w http.ResponseWriter, r *http.Request) (tools.BackgroundTaskService, tools.BackgroundTask, bool) {
	service := s.requireBackgroundTasks(w)
	if service == nil {
		return nil, tools.BackgroundTask{}, false
	}
	taskID := strings.TrimSpace(chi.URLParam(r, "taskId"))
	if err := validateAPIIdentifier("background task id", taskID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, tools.BackgroundTask{}, false
	}
	task, err := service.Get(r.Context(), "", taskID)
	if err != nil {
		writeError(w, http.StatusNotFound, "background task not found")
		return nil, tools.BackgroundTask{}, false
	}
	if !s.requireAgentAccess(w, r, task.OwnerAgentID) {
		return nil, tools.BackgroundTask{}, false
	}
	return service, task, true
}

func (s *Server) getBackgroundTask(w http.ResponseWriter, r *http.Request) {
	_, task, ok := s.backgroundTaskForRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) backgroundTaskOutput(w http.ResponseWriter, r *http.Request) {
	service, task, ok := s.backgroundTaskForRequest(w, r)
	if !ok {
		return
	}
	if err := rejectUnknownQuery(r, "afterSequence", "limitBytes"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	after, err := backgroundQueryInt64(r, "afterSequence", 0, 0, 1<<62)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limitBytes, err := queryInt(r, "limitBytes", maxBackgroundOutputBytes, 1, maxBackgroundOutputBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	page, err := service.Output(r.Context(), task.OwnerAgentID, task.ID, after, limitBytes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "background task output is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"taskId": page.TaskID, "chunks": page.Chunks, "items": page.Chunks, "nextSequence": page.NextSequence, "hasMore": page.HasMore, "truncated": page.Truncated})
}

func (s *Server) waitBackgroundTask(w http.ResponseWriter, r *http.Request) {
	service, task, ok := s.backgroundTaskForRequest(w, r)
	if !ok {
		return
	}
	var req waitBackgroundTaskRequest
	if err := decodeLimitedJSON(w, r, &req, 4*1024); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.AfterRevision < 0 {
		writeError(w, http.StatusBadRequest, "afterRevision must not be negative")
		return
	}
	if req.TimeoutMS <= 0 {
		req.TimeoutMS = maxBackgroundWaitMS
	}
	if req.TimeoutMS > maxBackgroundWaitMS {
		writeError(w, http.StatusBadRequest, "timeoutMs exceeds maximum")
		return
	}
	updated, err := service.Wait(r.Context(), task.OwnerAgentID, task.ID, req.TimeoutMS)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		writeError(w, http.StatusInternalServerError, "background task wait failed")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) cancelBackgroundTask(w http.ResponseWriter, r *http.Request) {
	service, task, ok := s.backgroundTaskForRequest(w, r)
	if !ok {
		return
	}
	updated, err := service.Cancel(r.Context(), task.OwnerAgentID, task.ID)
	if err != nil {
		writeError(w, http.StatusConflict, "background task could not be canceled")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func backgroundQueryInt64(r *http.Request, name string, fallback, minValue, maxValue int64) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < minValue || value > maxValue {
		return 0, strconv.ErrSyntax
	}
	return value, nil
}
