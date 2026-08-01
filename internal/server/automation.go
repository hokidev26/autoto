package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"autoto/internal/agent"
	"autoto/internal/audit"
	"autoto/internal/db"
	"autoto/internal/schedules"
)

type scheduleRequest struct {
	Name            string  `json:"name"`
	AgentID         string  `json:"agentId"`
	Expression      string  `json:"expression"`
	Timezone        string  `json:"timezone"`
	Prompt          string  `json:"prompt"`
	PermissionMode  string  `json:"permissionMode"`
	EnvironmentMode *string `json:"environmentMode,omitempty"`
	NarratorMode    *string `json:"narratorMode,omitempty"`
	Enabled         *bool   `json:"enabled,omitempty"`
}

type schedulePatchRequest struct {
	Name            *string `json:"name,omitempty"`
	AgentID         *string `json:"agentId,omitempty"`
	Expression      *string `json:"expression,omitempty"`
	Timezone        *string `json:"timezone,omitempty"`
	Prompt          *string `json:"prompt,omitempty"`
	PermissionMode  *string `json:"permissionMode,omitempty"`
	EnvironmentMode *string `json:"environmentMode,omitempty"`
	NarratorMode    *string `json:"narratorMode,omitempty"`
	Enabled         *bool   `json:"enabled,omitempty"`
}

func (s *Server) listSchedules(w http.ResponseWriter, r *http.Request) {
	if err := rejectUnknownQuery(r, "agentId", "enabled", "limit"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agentId"))
	if agentID != "" {
		if err := validateAPIIdentifier("agentId", agentID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	enabled, err := optionalBoolQuery(r, "enabled")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, err := queryInt(r, "limit", 50, 1, db.P2P3MaxListLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	items, err := s.store.ListSchedules(r.Context(), db.ScheduleListOptions{AgentID: agentID, Enabled: enabled, Limit: limit})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createSchedule(w http.ResponseWriter, r *http.Request) {
	var req scheduleRequest
	if err := decodeLimitedJSON(w, r, &req, 160<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	environmentMode := "workline"
	if req.EnvironmentMode != nil {
		environmentMode = strings.TrimSpace(*req.EnvironmentMode)
	}
	narratorMode := "reuse"
	if req.NarratorMode != nil {
		narratorMode = strings.TrimSpace(*req.NarratorMode)
	}
	schedule := db.Schedule{
		Name: strings.TrimSpace(req.Name), AgentID: strings.TrimSpace(req.AgentID), Expression: strings.TrimSpace(req.Expression), Timezone: strings.TrimSpace(req.Timezone),
		Prompt: strings.TrimSpace(req.Prompt), PermissionMode: strings.TrimSpace(req.PermissionMode), EnvironmentMode: environmentMode, NarratorMode: narratorMode, Enabled: enabled,
	}
	if schedule.Timezone == "" {
		schedule.Timezone = "UTC"
	}
	if schedule.PermissionMode == "" {
		schedule.PermissionMode = "readOnly"
	}
	if err := validateScheduleRequest(schedule); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.store.GetAgent(r.Context(), schedule.AgentID); err != nil {
		writeStoreError(w, err)
		return
	}
	if enabled {
		next, err := nextScheduleTime(schedule, s.now())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		schedule.NextRunAt = next
	} else if _, err := schedules.Parse(schedule.Expression, defaultTimezone(schedule.Timezone)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := s.store.CreateSchedule(r.Context(), schedule)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.recordAudit(r.Context(), audit.Event{Category: "schedule", Action: "schedule.create", Actor: "local-api", AgentID: created.AgentID, SubjectType: "schedule", SubjectID: created.ID, Outcome: "success", Risk: "low", Details: map[string]any{"enabled": created.Enabled, "permissionModeCap": created.PermissionMode}}); err != nil {
		writeError(w, http.StatusInternalServerError, "schedule was created but audit persistence failed")
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateSchedule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	current, err := s.store.GetSchedule(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req schedulePatchRequest
	if err := decodeLimitedJSON(w, r, &req, 160<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == nil && req.AgentID == nil && req.Expression == nil && req.Timezone == nil && req.Prompt == nil && req.PermissionMode == nil && req.EnvironmentMode == nil && req.NarratorMode == nil && req.Enabled == nil {
		writeError(w, http.StatusBadRequest, "at least one schedule field is required")
		return
	}
	if req.Name != nil {
		current.Name = *req.Name
	}
	if req.AgentID != nil {
		current.AgentID = *req.AgentID
	}
	if req.Expression != nil {
		current.Expression = *req.Expression
	}
	if req.Timezone != nil {
		current.Timezone = *req.Timezone
	}
	if req.Prompt != nil {
		current.Prompt = *req.Prompt
	}
	if req.PermissionMode != nil {
		current.PermissionMode = *req.PermissionMode
	}
	if req.EnvironmentMode != nil {
		current.EnvironmentMode = *req.EnvironmentMode
	}
	if req.NarratorMode != nil {
		current.NarratorMode = *req.NarratorMode
	}
	if req.Enabled != nil {
		current.Enabled = *req.Enabled
	}
	current.Name = strings.TrimSpace(current.Name)
	current.AgentID = strings.TrimSpace(current.AgentID)
	current.Expression = strings.TrimSpace(current.Expression)
	current.Timezone = strings.TrimSpace(current.Timezone)
	current.Prompt = strings.TrimSpace(current.Prompt)
	current.PermissionMode = strings.TrimSpace(current.PermissionMode)
	current.EnvironmentMode = strings.TrimSpace(current.EnvironmentMode)
	current.NarratorMode = strings.TrimSpace(current.NarratorMode)
	if err := validateScheduleRequest(current); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.store.GetAgent(r.Context(), current.AgentID); err != nil {
		writeStoreError(w, err)
		return
	}
	if current.Enabled {
		next, err := nextScheduleTime(current, s.now())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		current.NextRunAt = next
	} else {
		if _, err := schedules.Parse(current.Expression, defaultTimezone(current.Timezone)); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		current.NextRunAt = ""
		current.LeaseUntil = ""
	}
	updated, err := s.store.UpdateSchedule(r.Context(), current)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.recordAudit(r.Context(), audit.Event{Category: "schedule", Action: "schedule.update", Actor: "local-api", AgentID: updated.AgentID, SubjectType: "schedule", SubjectID: updated.ID, Outcome: "success", Risk: "low", Details: map[string]any{"enabled": updated.Enabled, "permissionModeCap": updated.PermissionMode}}); err != nil {
		writeError(w, http.StatusInternalServerError, "schedule was updated but audit persistence failed")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	current, err := s.store.GetSchedule(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.store.DeleteSchedule(r.Context(), id, current.UpdatedAt); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.recordAudit(r.Context(), audit.Event{Category: "schedule", Action: "schedule.delete", Actor: "local-api", AgentID: current.AgentID, SubjectType: "schedule", SubjectID: current.ID, Outcome: "success", Risk: "low", Details: map[string]any{"enabled": current.Enabled}}); err != nil {
		writeError(w, http.StatusInternalServerError, "schedule was deleted but audit persistence failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) runSchedule(w http.ResponseWriter, r *http.Request) {
	if s.automation == nil {
		writeError(w, http.StatusServiceUnavailable, "automation manager is unavailable")
		return
	}
	result, err := s.automation.TriggerSchedule(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, agent.ErrAgentBusy) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": agent.ErrAgentBusy.Error(), "outcome": "skipped", "run": result.Run})
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) listScheduleRuns(w http.ResponseWriter, r *http.Request) {
	if err := rejectUnknownQuery(r, "limit"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	scheduleID := strings.TrimSpace(chi.URLParam(r, "id"))
	if err := validateAPIIdentifier("schedule id", scheduleID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, err := queryInt(r, "limit", 50, 1, 200)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.store.GetSchedule(r.Context(), scheduleID); err != nil {
		writeStoreError(w, err)
		return
	}
	runs, err := s.store.ListScheduleRuns(r.Context(), scheduleID, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func validateScheduleRequest(schedule db.Schedule) error {
	for _, field := range []struct {
		name      string
		value     string
		maximum   int
		required  bool
		multiline bool
	}{
		{"name", schedule.Name, 120, true, false},
		{"agentId", schedule.AgentID, 128, true, false},
		{"expression", schedule.Expression, 256, true, false},
		{"timezone", schedule.Timezone, 128, true, false},
		{"prompt", schedule.Prompt, db.SchedulePromptMaxBytes, true, true},
	} {
		if err := validateAPIText(field.name, field.value, field.maximum, field.required, field.multiline); err != nil {
			return err
		}
	}
	// Both the create and the update handler validate through here, so a
	// schedule cannot acquire a self-restart prompt after creation either.
	if err := schedules.CheckLifecycleCommand(schedule.Prompt); err != nil {
		return err
	}
	if schedule.PermissionMode != "readOnly" && schedule.PermissionMode != "acceptEdits" {
		return errors.New("invalid permissionMode")
	}
	if schedule.EnvironmentMode != "workline" && schedule.EnvironmentMode != "standalone" {
		return errors.New("invalid environmentMode")
	}
	if schedule.NarratorMode != "reuse" && schedule.NarratorMode != "new" {
		return errors.New("invalid narratorMode")
	}
	return nil
}

func nextScheduleTime(schedule db.Schedule, after time.Time) (string, error) {
	expression, err := schedules.Parse(strings.TrimSpace(schedule.Expression), defaultTimezone(schedule.Timezone))
	if err != nil {
		return "", err
	}
	next, err := expression.Next(after.UTC())
	if err != nil {
		return "", err
	}
	return next.UTC().Format(time.RFC3339Nano), nil
}

func defaultTimezone(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "UTC"
	}
	return value
}

type deliveryView struct {
	ID                  string          `json:"id"`
	SinkType            string          `json:"sinkType"`
	SinkID              string          `json:"sinkId,omitempty"`
	EventType           string          `json:"eventType"`
	AgentID             string          `json:"agentId,omitempty"`
	RunID               string          `json:"runId,omitempty"`
	ToolUseID           string          `json:"toolUseId,omitempty"`
	ExecutionGeneration int64           `json:"executionGeneration"`
	Payload             json.RawMessage `json:"payload"`
	Status              string          `json:"status"`
	AttemptCount        int             `json:"attemptCount"`
	MaxAttempts         int             `json:"maxAttempts"`
	NextAttemptAt       string          `json:"nextAttemptAt"`
	LastHTTPStatus      int             `json:"lastHttpStatus,omitempty"`
	LastError           string          `json:"lastError,omitempty"`
	DeliveredAt         string          `json:"deliveredAt,omitempty"`
	CreatedAt           string          `json:"createdAt"`
	UpdatedAt           string          `json:"updatedAt"`
}

func publicDelivery(item db.NotificationDelivery) deliveryView {
	sinkID := item.SinkID
	if item.SinkType == "webhook" {
		sinkID = "configured-webhook"
	}
	return deliveryView{
		ID: item.ID, SinkType: item.SinkType, SinkID: sinkID, EventType: item.EventType,
		AgentID: item.AgentID, RunID: item.RunID, ToolUseID: item.ToolUseID, ExecutionGeneration: item.ExecutionGeneration, Payload: append(json.RawMessage(nil), item.PayloadJSON...),
		Status: item.Status, AttemptCount: item.AttemptCount, MaxAttempts: item.MaxAttempts, NextAttemptAt: item.NextAttemptAt,
		LastHTTPStatus: item.LastHTTPStatus, LastError: item.LastError, DeliveredAt: item.DeliveredAt, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func (s *Server) listNotificationDeliveries(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 50, 1, db.P2P3MaxListLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	offset, err := queryInt(r, "offset", 0, 0, 1_000_000)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	items, err := s.store.ListNotificationDeliveries(r.Context(), db.NotificationDeliveryListOptions{
		Status: r.URL.Query().Get("status"), SinkType: r.URL.Query().Get("sinkType"), AgentID: r.URL.Query().Get("agentId"),
		RunID: r.URL.Query().Get("runId"), EventType: r.URL.Query().Get("eventType"), Limit: limit, Offset: offset,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	views := make([]deliveryView, 0, len(items))
	for _, item := range items {
		views = append(views, publicDelivery(item))
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) retryNotificationDelivery(w http.ResponseWriter, r *http.Request) {
	if s.automation == nil {
		writeError(w, http.StatusServiceUnavailable, "automation manager is unavailable")
		return
	}
	item, err := s.automation.RetryDelivery(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, publicDelivery(item))
}

type pairingCodeRequest struct {
	ConnectionID string `json:"connectionId"`
	AgentID      string `json:"agentId"`
}

func (s *Server) createChannelPairingCode(w http.ResponseWriter, r *http.Request) {
	var req pairingCodeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	connection, err := s.store.GetIntegrationConnection(r.Context(), strings.TrimSpace(req.ConnectionID))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if connection.Kind != "telegram" || !connection.Enabled {
		writeError(w, http.StatusBadRequest, "connection must be an enabled telegram integration")
		return
	}
	if _, err := s.store.GetAgent(r.Context(), strings.TrimSpace(req.AgentID)); err != nil {
		writeStoreError(w, err)
		return
	}
	codeBytes := make([]byte, 18)
	if _, err := rand.Read(codeBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "pairing code generation failed")
		return
	}
	code := base64.RawURLEncoding.EncodeToString(codeBytes)
	hash := sha256.Sum256([]byte(code))
	pairing, err := s.store.CreateChannelPairing(r.Context(), db.ChannelPairing{
		ConnectionID: connection.ID, AgentID: req.AgentID, Status: "pending", CodeHash: hex.EncodeToString(hash[:]),
		ExpiresAt: s.now().UTC().Add(10 * time.Minute).Format(time.RFC3339Nano),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.recordAudit(r.Context(), audit.Event{Category: "channel", Action: "pairing.create", Actor: "local-api", AgentID: pairing.AgentID, SubjectType: "channel_pairing", SubjectID: pairing.ID, Outcome: "success", Risk: "medium", Details: map[string]any{"connectionId": pairing.ConnectionID, "expiresAt": pairing.ExpiresAt}}); err != nil {
		writeError(w, http.StatusInternalServerError, "pairing was created but audit persistence failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"code": code, "pairing": pairing})
}

func (s *Server) listChannelPairings(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListChannelPairings(r.Context(), db.ChannelPairingListOptions{
		ConnectionID: r.URL.Query().Get("connectionId"), AgentID: r.URL.Query().Get("agentId"), Status: r.URL.Query().Get("status"), Limit: 200,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) revokeChannelPairing(w http.ResponseWriter, r *http.Request) {
	pairing, err := s.store.RevokeChannelPairing(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.recordAudit(r.Context(), audit.Event{Category: "channel", Action: "pairing.revoke", Actor: "local-api", AgentID: pairing.AgentID, SubjectType: "channel_pairing", SubjectID: pairing.ID, Outcome: "success", Risk: "medium", Details: map[string]any{"connectionId": pairing.ConnectionID}}); err != nil {
		writeError(w, http.StatusInternalServerError, "pairing was revoked but audit persistence failed")
		return
	}
	writeJSON(w, http.StatusOK, pairing)
}

func (s *Server) listAuditEvents(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 50, 1, db.AutomationAuditMaxListLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	offset, err := queryInt(r, "offset", 0, 0, 1_000_000)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	items, err := s.store.ListAutomationAuditEvents(r.Context(), limit, offset)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) recordAudit(ctx context.Context, event audit.Event) error {
	if s.audit == nil {
		return nil
	}
	return s.audit.Record(ctx, event)
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows), db.IsNotFound(err):
		writeError(w, http.StatusNotFound, "resource not found")
	case db.IsConflict(err):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func queryInt(r *http.Request, name string, fallback, minimum, maximum int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, errors.New("invalid " + name)
	}
	return value, nil
}

func optionalBoolQuery(r *http.Request, name string) (*bool, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, errors.New("invalid " + name)
	}
	return &value, nil
}
