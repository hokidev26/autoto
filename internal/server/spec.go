package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"autoto/internal/db"
)

const (
	maxAgentJSONBody = 32 << 10
	maxSpecJSONBody  = 256 << 10
	maxGoalJSONBody  = 32 << 10
)

type createSpecTaskRequest struct {
	Text      string  `json:"text"`
	Status    *string `json:"status,omitempty"`
	Protected *bool   `json:"protected,omitempty"`
}

type updateSpecTaskRequest struct {
	Text                 *string `json:"text,omitempty"`
	Status               *string `json:"status,omitempty"`
	Protected            *bool   `json:"protected,omitempty"`
	ExpectedRevision     int64   `json:"expectedRevision"`
	AcknowledgeProtected bool    `json:"acknowledgeProtected,omitempty"`
}

type deleteSpecTaskRequest struct {
	ExpectedRevision     int64 `json:"expectedRevision"`
	AcknowledgeProtected bool  `json:"acknowledgeProtected,omitempty"`
}

type reorderSpecTasksRequest struct {
	TaskIDs               []string `json:"taskIds"`
	ExpectedBoardRevision *int64   `json:"expectedBoardRevision"`
}

type createGoalRequest struct {
	Text string `json:"text"`
}

func (s *Server) getSpecBoard(w http.ResponseWriter, r *http.Request) {
	agentID := chi.URLParam(r, "id")
	if err := validateAPIIdentifier("agent id", agentID); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	board, err := s.store.GetSpecBoard(r.Context(), agentID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, board)
}

func (s *Server) createSpecTask(w http.ResponseWriter, r *http.Request) {
	var req createSpecTaskRequest
	if err := decodeLimitedJSON(w, r, &req, maxSpecJSONBody); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	agentID := chi.URLParam(r, "id")
	if err := validateAPIIdentifier("agent id", agentID); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	text := strings.TrimSpace(req.Text)
	if err := validateAPIText("text", text, db.SpecTaskMaxBytes, true, true); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	status := "todo"
	if req.Status != nil {
		status = strings.TrimSpace(*req.Status)
	}
	if !validSpecTaskStatus(status) {
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}
	protected := false
	if req.Protected != nil {
		protected = *req.Protected
	}
	board, err := s.store.CreateSpecTask(r.Context(), db.SpecTask{
		AgentID: agentID, Text: text, Status: status, Protected: protected, SourceType: "manual",
	})
	if err != nil {
		s.writeSpecStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, board)
}

func (s *Server) updateSpecTask(w http.ResponseWriter, r *http.Request) {
	var req updateSpecTaskRequest
	if err := decodeLimitedJSON(w, r, &req, maxSpecJSONBody); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	agentID, taskID := chi.URLParam(r, "id"), chi.URLParam(r, "taskId")
	if err := validateAPIIdentifier("agent id", agentID); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := validateAPIIdentifier("task id", taskID); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if req.ExpectedRevision < 1 {
		writeError(w, http.StatusBadRequest, "expectedRevision is required")
		return
	}
	if req.Text == nil && req.Status == nil && req.Protected == nil {
		writeError(w, http.StatusBadRequest, "at least one task field is required")
		return
	}
	if req.Text != nil {
		trimmed := strings.TrimSpace(*req.Text)
		if err := validateAPIText("text", trimmed, db.SpecTaskMaxBytes, true, true); err != nil {
			s.writeRequestError(w, r, http.StatusBadRequest, err)
			return
		}
		req.Text = &trimmed
	}
	if req.Status != nil {
		trimmed := strings.TrimSpace(*req.Status)
		if !validSpecTaskStatus(trimmed) {
			writeError(w, http.StatusBadRequest, "invalid status")
			return
		}
		req.Status = &trimmed
	}
	board, err := s.store.UpdateSpecTask(r.Context(), agentID, taskID, db.SpecTaskMutation{
		Text: req.Text, Status: req.Status, Protected: req.Protected, ExpectedRevision: req.ExpectedRevision,
		AcknowledgeProtected: req.AcknowledgeProtected, Actor: "local-api",
	})
	if err != nil {
		s.writeSpecStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, board)
}

func (s *Server) deleteSpecTask(w http.ResponseWriter, r *http.Request) {
	var req deleteSpecTaskRequest
	if err := decodeLimitedJSON(w, r, &req, 8<<10); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	agentID, taskID := chi.URLParam(r, "id"), chi.URLParam(r, "taskId")
	if err := validateAPIIdentifier("agent id", agentID); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := validateAPIIdentifier("task id", taskID); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if req.ExpectedRevision < 1 {
		writeError(w, http.StatusBadRequest, "expectedRevision is required")
		return
	}
	board, err := s.store.DeleteSpecTask(r.Context(), agentID, taskID, req.ExpectedRevision, req.AcknowledgeProtected, "local-api")
	if err != nil {
		s.writeSpecStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, board)
}

func (s *Server) reorderSpecTasks(w http.ResponseWriter, r *http.Request) {
	var req reorderSpecTasksRequest
	if err := decodeLimitedJSON(w, r, &req, maxSpecJSONBody); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	agentID := chi.URLParam(r, "id")
	if err := validateAPIIdentifier("agent id", agentID); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if req.ExpectedBoardRevision == nil || *req.ExpectedBoardRevision < 0 {
		writeError(w, http.StatusBadRequest, "expectedBoardRevision is required")
		return
	}
	if len(req.TaskIDs) > db.SpecTaskMaxCount {
		writeError(w, http.StatusBadRequest, "taskIds exceeds size limit")
		return
	}
	seen := make(map[string]struct{}, len(req.TaskIDs))
	for index, id := range req.TaskIDs {
		id = strings.TrimSpace(id)
		if err := validateAPIIdentifier("task id", id); err != nil {
			s.writeRequestError(w, r, http.StatusBadRequest, err)
			return
		}
		if _, exists := seen[id]; exists {
			writeError(w, http.StatusBadRequest, "taskIds contains duplicates")
			return
		}
		seen[id] = struct{}{}
		req.TaskIDs[index] = id
	}
	board, err := s.store.ReorderSpecTasks(r.Context(), agentID, req.TaskIDs, *req.ExpectedBoardRevision)
	if err != nil {
		s.writeSpecStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, board)
}

func (s *Server) createSpecGoal(w http.ResponseWriter, r *http.Request) {
	var req createGoalRequest
	if err := decodeLimitedJSON(w, r, &req, maxGoalJSONBody); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	s.createGoalResponse(w, r, chi.URLParam(r, "id"), req.Text)
}

func (s *Server) createGoalResponse(w http.ResponseWriter, r *http.Request, agentID, text string) {
	agentID = strings.TrimSpace(agentID)
	text = strings.TrimSpace(text)
	if err := validateAPIIdentifier("agent id", agentID); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := validateAPIText("text", text, db.SpecTaskMaxBytes, true, true); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	board, confirmation, err := s.store.CreateGoal(r.Context(), agentID, text)
	if err != nil {
		s.writeSpecStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"kind": "goal.confirmation", "board": board, "confirmation": confirmation,
	})
}

func validSpecTaskStatus(status string) bool {
	switch status {
	case "todo", "doing", "done", "blocked":
		return true
	default:
		return false
	}
}

func parseGoalCommand(text string) (string, bool) {
	const prefix = "/goal "
	if !strings.HasPrefix(text, prefix) {
		return "", false
	}
	goal := strings.TrimSpace(strings.TrimPrefix(text, prefix))
	if goal == "" {
		return "", false
	}
	return goal, true
}

func decodeLimitedJSON(w http.ResponseWriter, r *http.Request, dst any, maximum int64) error {
	if maximum <= 0 {
		return errors.New("invalid request size limit")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maximum)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			return errors.New("request body exceeds size limit")
		}
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain exactly one JSON value")
		}
		return err
	}
	return nil
}

func validateAPIIdentifier(name, value string) error {
	value = strings.TrimSpace(value)
	return validateAPIText(name, value, 128, true, false)
}

func validateAPIText(name, value string, maximum int, required, multiline bool) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", name)
	}
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len([]byte(value)) > maximum {
		return fmt.Errorf("%s exceeds size limit", name)
	}
	for _, char := range value {
		if char == 0 || char == 0x7f || char < 0x20 && (!multiline || char != '\n' && char != '\t' && char != '\r') {
			return fmt.Errorf("%s contains invalid control characters", name)
		}
	}
	return nil
}

func rejectUnknownQuery(r *http.Request, allowed ...string) error {
	set := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		set[name] = struct{}{}
	}
	for name, values := range r.URL.Query() {
		if _, ok := set[name]; !ok {
			return fmt.Errorf("unknown query parameter %q", name)
		}
		if len(values) != 1 {
			return fmt.Errorf("query parameter %q must appear once", name)
		}
	}
	return nil
}

func (s *Server) writeSpecStoreError(w http.ResponseWriter, r *http.Request, err error) {
	if db.IsConflict(err) || db.IsNotFound(err) {
		s.writeStoreError(w, r, err)
		return
	}
	message := err.Error()
	for _, marker := range []string{"invalid spec", "spec task limit", "task order", "expected revision", "expected board"} {
		if strings.Contains(message, marker) {
			s.writeRequestError(w, r, http.StatusBadRequest, err)
			return
		}
	}
	writeError(w, http.StatusInternalServerError, "internal error")
}

// mountLearnedFeatureRoutes keeps the new endpoints in one place so the main
// router can opt in without coupling their handlers to server construction.
func (s *Server) mountLearnedFeatureRoutes(r chi.Router) {
	r.Get("/api/schedules/{id}/runs", s.listScheduleRuns)
	for _, prefix := range []string{"/api/agents", "/api/narrators"} {
		r.Get(prefix, s.listAgents)
		r.Post(prefix, s.createAgent)
		r.Get(prefix+"/{id}/children", s.listAgentChildren)
		r.Get(prefix+"/{id}/spec", s.getSpecBoard)
		r.Post(prefix+"/{id}/spec/tasks", s.createSpecTask)
		r.Patch(prefix+"/{id}/spec/tasks/{taskId}", s.updateSpecTask)
		r.Delete(prefix+"/{id}/spec/tasks/{taskId}", s.deleteSpecTask)
		r.Post(prefix+"/{id}/spec/tasks/{taskId}/assign", s.assignSpecTask)
		r.Put(prefix+"/{id}/spec/tasks/order", s.reorderSpecTasks)
		r.Put(prefix+"/{id}/spec/order", s.reorderSpecTasks)
		r.Post(prefix+"/{id}/spec/goal", s.createSpecGoal)
		r.Post(prefix+"/{id}/spec/goals", s.createSpecGoal)
	}
}
