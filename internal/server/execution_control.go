package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"autoto/internal/db"
)

const remoteExecutionTaskListLimit = 100

type registerRemoteExecutionDeviceRequest struct {
	Name                string          `json:"name"`
	Capabilities        json.RawMessage `json:"capabilities,omitempty"`
	Fingerprint         string          `json:"fingerprint,omitempty"`
	IdentityFingerprint string          `json:"identityFingerprint,omitempty"`
}

type projectExecutionDeviceGrantRequest struct {
	DeviceID          string          `json:"deviceId,omitempty"`
	ExecutionDeviceID string          `json:"executionDeviceId,omitempty"`
	Enabled           *bool           `json:"enabled"`
	Capabilities      json.RawMessage `json:"capabilities,omitempty"`
}

type agentExecutionDeviceRequest struct {
	DeviceID          string `json:"deviceId,omitempty"`
	ExecutionDeviceID string `json:"executionDeviceId,omitempty"`
}

type createRemoteExecutionTaskRequest struct {
	IdempotencyKey    string          `json:"idempotencyKey"`
	ProjectID         string          `json:"projectId"`
	AgentID           string          `json:"agentId"`
	RunID             string          `json:"runId,omitempty"`
	ExecutionDeviceID string          `json:"executionDeviceId"`
	Payload           json.RawMessage `json:"payload,omitempty"`
}

// remoteExecutionTaskLedgerResponse is intentionally a control-plane record.
// Lease ownership, raw execution errors, and transport results are not exposed.
type remoteExecutionTaskLedgerResponse struct {
	ID                   string          `json:"id"`
	IdempotencyKey       string          `json:"idempotencyKey"`
	ProjectID            string          `json:"projectId"`
	AgentID              string          `json:"agentId"`
	RunID                string          `json:"runId,omitempty"`
	ExecutionDeviceID    string          `json:"executionDeviceId"`
	Status               string          `json:"status"`
	Payload              json.RawMessage `json:"payload"`
	NoFallback           bool            `json:"noFallback"`
	AttemptCount         int             `json:"attemptCount"`
	Revision             int64           `json:"revision"`
	CreatedAt            string          `json:"createdAt"`
	UpdatedAt            string          `json:"updatedAt"`
	CompletedAt          string          `json:"completedAt,omitempty"`
	TransportImplemented bool            `json:"transportImplemented"`
}

func (s *Server) listExecutionDevices(w http.ResponseWriter, r *http.Request) {
	if !s.executionStoreAvailable(w) {
		return
	}
	devices, err := s.store.ListExecutionDevices(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list execution devices")
		return
	}
	if devices == nil {
		devices = []db.ExecutionDevice{}
	}
	writeJSON(w, http.StatusOK, devices)
}

func (s *Server) registerRemoteExecutionDevice(w http.ResponseWriter, r *http.Request) {
	if !s.executionStoreAvailable(w) {
		return
	}
	var req registerRemoteExecutionDeviceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid remote execution device registration")
		return
	}
	fingerprint, ok := req.identityFingerprint()
	if !ok {
		writeError(w, http.StatusBadRequest, "remote execution device fingerprint is required")
		return
	}
	device, err := s.store.RegisterRemoteExecutionDevice(r.Context(), db.ExecutionDeviceRegistration{
		Name:                strings.TrimSpace(req.Name),
		Capabilities:        req.Capabilities,
		IdentityFingerprint: fingerprint,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid remote execution device registration")
		return
	}
	writeJSON(w, http.StatusCreated, device)
}

func (s *Server) enableRemoteExecutionDevice(w http.ResponseWriter, r *http.Request) {
	s.setRemoteExecutionDeviceEnabled(w, r, true)
}

func (s *Server) disableRemoteExecutionDevice(w http.ResponseWriter, r *http.Request) {
	s.setRemoteExecutionDeviceEnabled(w, r, false)
}

// Short aliases keep route mounting concise while retaining the remote-only
// implementation and database guard.
func (s *Server) enableExecutionDevice(w http.ResponseWriter, r *http.Request) {
	s.enableRemoteExecutionDevice(w, r)
}

func (s *Server) disableExecutionDevice(w http.ResponseWriter, r *http.Request) {
	s.disableRemoteExecutionDevice(w, r)
}

func (s *Server) setRemoteExecutionDeviceEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	if !s.executionStoreAvailable(w) {
		return
	}
	if err := decodeOptionalEmptyJSON(r); err != nil {
		writeError(w, http.StatusBadRequest, "invalid execution device state request")
		return
	}
	deviceID := executionRouteParam(r, "deviceId", "executionDeviceId", "id")
	if deviceID == "" {
		writeError(w, http.StatusBadRequest, "execution device id is required")
		return
	}
	device, err := s.store.SetExecutionDeviceEnabled(r.Context(), deviceID, enabled)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "remote execution device not found")
			return
		}
		writeError(w, http.StatusBadRequest, "execution device state change was rejected")
		return
	}
	writeJSON(w, http.StatusOK, device)
}

func (s *Server) setProjectExecutionDeviceGrant(w http.ResponseWriter, r *http.Request) {
	if !s.executionStoreAvailable(w) {
		return
	}
	var req projectExecutionDeviceGrantRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid project execution device grant")
		return
	}
	projectID := executionRouteParam(r, "projectId", "id")
	deviceID, ok := selectExecutionDeviceID(
		executionRouteParam(r, "deviceId", "executionDeviceId"),
		req.DeviceID,
		req.ExecutionDeviceID,
	)
	if projectID == "" || !ok || req.Enabled == nil {
		writeError(w, http.StatusBadRequest, "project, execution device, and enabled state are required")
		return
	}
	if _, err := s.store.GetProject(r.Context(), projectID); err != nil {
		writeExecutionResourceLookupError(w, err, "project not found")
		return
	}
	if _, err := s.store.GetExecutionDevice(r.Context(), deviceID); err != nil {
		writeExecutionResourceLookupError(w, err, "execution device not found")
		return
	}
	grant, err := s.store.SetProjectDeviceGrant(r.Context(), db.ProjectDeviceGrant{
		ProjectID:    projectID,
		DeviceID:     deviceID,
		Enabled:      *req.Enabled,
		Capabilities: req.Capabilities,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "project execution device grant was rejected")
		return
	}
	writeJSON(w, http.StatusOK, grant)
}

func (s *Server) setProjectDeviceGrant(w http.ResponseWriter, r *http.Request) {
	s.setProjectExecutionDeviceGrant(w, r)
}

func (s *Server) setAgentExecutionDevice(w http.ResponseWriter, r *http.Request) {
	if !s.executionStoreAvailable(w) {
		return
	}
	var req agentExecutionDeviceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid agent execution device request")
		return
	}
	deviceID, ok := selectExecutionDeviceID("", req.DeviceID, req.ExecutionDeviceID)
	agentID := executionRouteParam(r, "agentId", "id")
	if agentID == "" || !ok {
		writeError(w, http.StatusBadRequest, "agent and execution device are required")
		return
	}
	agent, err := s.store.SetAgentExecutionDevice(r.Context(), agentID, deviceID)
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			writeError(w, http.StatusNotFound, "agent not found")
		case db.IsConflict(err):
			writeError(w, http.StatusConflict, "remote execution device is unavailable or unauthorized")
		default:
			writeError(w, http.StatusBadRequest, "agent execution device change was rejected")
		}
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) listRemoteExecutionTasks(w http.ResponseWriter, r *http.Request) {
	if !s.executionStoreAvailable(w) {
		return
	}
	rows, err := s.store.DB().QueryContext(r.Context(), `SELECT id, idempotency_key, project_id, agent_id, COALESCE(run_id,''), execution_device_id, status, payload_json, no_fallback, attempt_count, revision, created_at, updated_at, COALESCE(completed_at,'') FROM remote_execution_tasks ORDER BY created_at DESC, id DESC LIMIT ?`, remoteExecutionTaskListLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list remote execution tasks")
		return
	}
	defer rows.Close()

	tasks := make([]remoteExecutionTaskLedgerResponse, 0)
	transportReady := s.remoteExecutionTransportReady()
	for rows.Next() {
		task, scanErr := scanRemoteExecutionTaskLedger(rows.Scan, transportReady)
		if scanErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to list remote execution tasks")
			return
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list remote execution tasks")
		return
	}
	filtered, ok := s.filterRemoteExecutionTasksForRequest(w, r, tasks)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, filtered)
}

func (s *Server) getRemoteExecutionTask(w http.ResponseWriter, r *http.Request) {
	if !s.executionStoreAvailable(w) {
		return
	}
	taskID := executionRouteParam(r, "taskId", "id")
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "remote execution task id is required")
		return
	}
	task, err := s.store.GetRemoteExecutionTask(r.Context(), taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "remote execution task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load remote execution task")
		return
	}
	if !s.requireExecutionTaskAccess(w, r, task.ProjectID, task.AgentID) {
		return
	}
	writeJSON(w, http.StatusOK, makeRemoteExecutionTaskLedger(task, s.remoteExecutionTransportReady()))
}

func (s *Server) createRemoteExecutionTask(w http.ResponseWriter, r *http.Request) {
	if !s.executionStoreAvailable(w) {
		return
	}
	var req createRemoteExecutionTaskRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid remote execution task request")
		return
	}
	task := db.RemoteExecutionTask{
		IdempotencyKey:    strings.TrimSpace(req.IdempotencyKey),
		ProjectID:         strings.TrimSpace(req.ProjectID),
		AgentID:           strings.TrimSpace(req.AgentID),
		RunID:             strings.TrimSpace(req.RunID),
		ExecutionDeviceID: strings.TrimSpace(req.ExecutionDeviceID),
		Payload:           req.Payload,
	}
	if task.IdempotencyKey == "" || task.ProjectID == "" || task.AgentID == "" || task.ExecutionDeviceID == "" || task.ExecutionDeviceID == "local" {
		writeError(w, http.StatusBadRequest, "remote execution task identity is invalid")
		return
	}
	if !s.requireExecutionTaskAccess(w, r, task.ProjectID, task.AgentID) {
		return
	}
	created, err := s.store.CreateRemoteExecutionTask(r.Context(), task)
	if err != nil {
		switch {
		case db.IsConflict(err):
			writeError(w, http.StatusConflict, "remote execution target is unavailable or unauthorized")
		case errors.Is(err, sql.ErrNoRows):
			writeError(w, http.StatusNotFound, "remote execution resource not found")
		default:
			// Deliberately do not echo validation, payload, SQLite, or transport
			// details. Payloads can contain user-controlled sensitive material.
			writeError(w, http.StatusBadRequest, "remote execution task request was rejected")
		}
		return
	}
	// A duplicate idempotency key returns the previously persisted task. Check
	// that record too, otherwise a caller authorized for its requested target
	// could receive another tenant's task payload via a key collision.
	if !s.requireExecutionTaskAccess(w, r, created.ProjectID, created.AgentID) {
		return
	}
	writeJSON(w, http.StatusCreated, makeRemoteExecutionTaskLedger(created, s.remoteExecutionTransportReady()))
}

func (req registerRemoteExecutionDeviceRequest) identityFingerprint() (string, bool) {
	fingerprint := strings.TrimSpace(req.Fingerprint)
	identityFingerprint := strings.TrimSpace(req.IdentityFingerprint)
	if fingerprint != "" && identityFingerprint != "" && fingerprint != identityFingerprint {
		return "", false
	}
	if fingerprint == "" {
		fingerprint = identityFingerprint
	}
	return fingerprint, fingerprint != ""
}

func selectExecutionDeviceID(pathValue string, requestValues ...string) (string, bool) {
	selected := strings.TrimSpace(pathValue)
	for _, value := range requestValues {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if selected != "" && selected != value {
			return "", false
		}
		selected = value
	}
	return selected, selected != ""
}

func executionRouteParam(r *http.Request, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(chi.URLParam(r, name)); value != "" {
			return value
		}
	}
	return ""
}

func decodeOptionalEmptyJSON(r *http.Request) error {
	if r == nil || r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var request struct{}
	if err := decoder.Decode(&request); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
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

func (s *Server) requireExecutionTaskAccess(w http.ResponseWriter, r *http.Request, projectID, agentID string) bool {
	projectID = strings.TrimSpace(projectID)
	agentID = strings.TrimSpace(agentID)
	if projectID == "" || agentID == "" {
		writeError(w, http.StatusNotFound, "remote execution task not found")
		return false
	}
	if !s.requireProjectResourceAccess(w, r, projectAccessTarget{kind: projectAccessProject, id: projectID}) {
		return false
	}
	if !s.requireProjectResourceAccess(w, r, projectAccessTarget{kind: projectAccessAgent, id: agentID}) {
		return false
	}
	return true
}

func (s *Server) filterRemoteExecutionTasksForRequest(w http.ResponseWriter, r *http.Request, tasks []remoteExecutionTaskLedgerResponse) ([]remoteExecutionTaskLedgerResponse, bool) {
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	var userID string
	if hasUsers {
		user, ok, err := s.currentUser(r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return nil, false
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "login required")
			return nil, false
		}
		userID = user.ID
	}

	filtered := make([]remoteExecutionTaskLedgerResponse, 0, len(tasks))
	for _, task := range tasks {
		if s.executionTaskVisibleForRequest(r, userID, task.ProjectID, task.AgentID) {
			filtered = append(filtered, task)
		}
	}
	return filtered, true
}

func (s *Server) executionTaskVisibleForRequest(r *http.Request, userID, projectID, agentID string) bool {
	projectID = strings.TrimSpace(projectID)
	agentID = strings.TrimSpace(agentID)
	if projectID == "" || agentID == "" {
		return false
	}
	if s.capabilitiesForRequest(r).FilesystemScope == "project" {
		project, err := s.store.GetProject(r.Context(), projectID)
		if err != nil || !s.filesystemPathWithinProjectRoot(project.GitPath) {
			return false
		}
		agent, err := s.store.GetAgent(r.Context(), agentID)
		if err != nil || !s.filesystemPathWithinProjectRoot(agent.CWD) {
			return false
		}
	}
	if userID == "" {
		return true
	}
	projectAllowed, err := s.store.CanAccessProject(r.Context(), userID, projectID)
	if err != nil || !projectAllowed {
		return false
	}
	agentAllowed, err := s.store.CanAccessAgent(r.Context(), userID, agentID)
	return err == nil && agentAllowed
}

func (s *Server) executionStoreAvailable(w http.ResponseWriter) bool {
	if s != nil && s.store != nil && s.store.DB() != nil {
		return true
	}
	writeError(w, http.StatusServiceUnavailable, "execution control store is unavailable")
	return false
}

func writeExecutionResourceLookupError(w http.ResponseWriter, err error, notFoundMessage string) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, notFoundMessage)
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to load execution control resource")
}

func makeRemoteExecutionTaskLedger(task db.RemoteExecutionTask, transportReady bool) remoteExecutionTaskLedgerResponse {
	payload := append(json.RawMessage(nil), task.Payload...)
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	return remoteExecutionTaskLedgerResponse{
		ID:                   task.ID,
		IdempotencyKey:       task.IdempotencyKey,
		ProjectID:            task.ProjectID,
		AgentID:              task.AgentID,
		RunID:                task.RunID,
		ExecutionDeviceID:    task.ExecutionDeviceID,
		Status:               task.Status,
		Payload:              payload,
		NoFallback:           task.NoFallback,
		AttemptCount:         task.AttemptCount,
		Revision:             task.Revision,
		CreatedAt:            task.CreatedAt,
		UpdatedAt:            task.UpdatedAt,
		CompletedAt:          task.CompletedAt,
		TransportImplemented: transportReady,
	}
}

func scanRemoteExecutionTaskLedger(scan func(...any) error, transportReady bool) (remoteExecutionTaskLedgerResponse, error) {
	var task remoteExecutionTaskLedgerResponse
	var payload string
	var noFallback int
	err := scan(
		&task.ID,
		&task.IdempotencyKey,
		&task.ProjectID,
		&task.AgentID,
		&task.RunID,
		&task.ExecutionDeviceID,
		&task.Status,
		&payload,
		&noFallback,
		&task.AttemptCount,
		&task.Revision,
		&task.CreatedAt,
		&task.UpdatedAt,
		&task.CompletedAt,
	)
	if err != nil {
		return remoteExecutionTaskLedgerResponse{}, err
	}
	task.Payload = json.RawMessage(payload)
	task.NoFallback = noFallback != 0
	task.TransportImplemented = transportReady
	return task, nil
}
