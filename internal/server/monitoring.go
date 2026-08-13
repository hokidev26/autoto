package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"autoto/internal/audit"
	"autoto/internal/automation"
	"autoto/internal/db"
	"autoto/internal/devices"
)

type createDeviceActionRequest struct {
	ConnectionID string          `json:"connectionId"`
	Domain       string          `json:"domain"`
	Service      string          `json:"service"`
	Input        json.RawMessage `json:"input"`
}

type denyDeviceActionRequest struct {
	Reason string `json:"reason,omitempty"`
}

func (s *Server) deviceAdapter(ctx context.Context, connectionID string) (devices.Adapter, error) {
	if s.deviceAdapterFactory != nil {
		return s.deviceAdapterFactory(ctx, connectionID)
	}
	resolved, err := s.connectionService().Resolve(ctx, strings.TrimSpace(connectionID))
	if err != nil {
		return nil, err
	}
	if resolved.Kind != devices.HomeAssistantKind || !resolved.Enabled {
		return nil, errors.New("connection must be an enabled home-assistant integration")
	}
	return devices.NewAdapter(resolved, s.integrationHTTPClient())
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	connectionID := strings.TrimSpace(r.URL.Query().Get("connectionId"))
	if connectionID == "" {
		writeError(w, http.StatusBadRequest, "connectionId is required")
		return
	}
	adapter, err := s.deviceAdapter(r.Context(), connectionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "home-assistant connection is unavailable")
		return
	}
	items, err := adapter.ListDevices(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "home-assistant device listing failed")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createDeviceAction(w http.ResponseWriter, r *http.Request) {
	var req createDeviceActionRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	adapter, err := s.deviceAdapter(r.Context(), req.ConnectionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "home-assistant connection is unavailable")
		return
	}
	canonical, err := adapter.CanonicalAction(devices.Action{Domain: req.Domain, Service: req.Service, Input: req.Input})
	if err != nil || adapter.ValidateAction(canonical) != nil {
		_ = s.recordAudit(context.WithoutCancel(r.Context()), audit.Event{Category: "device", Action: "action.create", Actor: "local-api", SubjectType: "integration_connection", SubjectID: strings.TrimSpace(req.ConnectionID), Outcome: "denied", Risk: "critical", Details: map[string]any{"reason": "blocked_or_invalid_action"}})
		writeError(w, http.StatusForbidden, "device action is blocked")
		return
	}
	risk := adapter.Risk(canonical)
	if risk == devices.RiskBlocked || risk != devices.RiskMedium && risk != devices.RiskHigh {
		_ = s.recordAudit(context.WithoutCancel(r.Context()), audit.Event{Category: "device", Action: "action.create", Actor: "local-api", SubjectType: "integration_connection", SubjectID: strings.TrimSpace(req.ConnectionID), Outcome: "denied", Risk: "critical", Details: map[string]any{"reason": "critical_or_blocked_action"}})
		writeError(w, http.StatusForbidden, "device action is blocked")
		return
	}
	entityID, ok := actionEntityID(canonical.Input)
	if !ok {
		writeError(w, http.StatusBadRequest, "device action entity is invalid")
		return
	}
	created, err := s.store.CreateDeviceActionRequest(r.Context(), db.DeviceActionRequest{
		ConnectionID: strings.TrimSpace(req.ConnectionID), EntityID: entityID, Domain: canonical.Domain, Service: canonical.Service,
		PayloadJSON: append(json.RawMessage(nil), canonical.Input...), Risk: string(risk), Status: "pending", RequestedBy: "local-api",
		ExpiresAt: s.now().UTC().Add(5 * time.Minute).Format(time.RFC3339Nano),
	})
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := s.recordAudit(r.Context(), audit.Event{Category: "device", Action: "action.create", Actor: "local-api", SubjectType: "device_action", SubjectID: created.ID, Outcome: "success", Risk: created.Risk, Details: map[string]any{"connectionId": created.ConnectionID, "entityId": created.EntityID, "domain": created.Domain, "service": created.Service}}); err != nil {
		writeError(w, http.StatusInternalServerError, "device action was created but audit persistence failed")
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) approveDeviceAction(w http.ResponseWriter, r *http.Request) {
	if !strictLoopbackApprovalRequest(r) {
		writeError(w, http.StatusForbidden, "device action approval requires a direct loopback request")
		return
	}
	id := chi.URLParam(r, "id")
	request, err := s.store.GetDeviceActionRequest(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	adapter, err := s.deviceAdapter(r.Context(), request.ConnectionID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "home-assistant connection is unavailable")
		return
	}
	canonical, err := adapter.CanonicalAction(devices.Action{Domain: request.Domain, Service: request.Service, Input: request.PayloadJSON})
	if err != nil || adapter.Risk(canonical) == devices.RiskBlocked {
		writeError(w, http.StatusForbidden, "device action is blocked")
		return
	}
	entityID, ok := actionEntityID(canonical.Input)
	if !ok || entityID != request.EntityID {
		writeError(w, http.StatusConflict, "device action no longer matches its approval record")
		return
	}
	if err := s.recordAudit(r.Context(), audit.Event{
		Category: "device", Action: "action.approve", Actor: "local-loopback", SubjectType: "device_action", SubjectID: request.ID,
		Outcome: "success", Risk: request.Risk, Details: map[string]any{"connectionId": request.ConnectionID, "entityId": request.EntityID, "domain": request.Domain, "service": request.Service},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "device action approval audit failed")
		return
	}
	approved, err := s.store.ApproveDeviceActionRequest(r.Context(), id, "local-loopback")
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	executing, err := s.store.StartDeviceActionRequest(r.Context(), approved.ID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	executeErr := adapter.Execute(r.Context(), canonical)
	status := "succeeded"
	lastError := ""
	outcome := "success"
	if executeErr != nil {
		status = "failed"
		lastError = "home-assistant action execution failed"
		outcome = "failure"
	}
	completed, completeErr := s.store.CompleteDeviceActionRequest(context.Background(), executing.ID, status, lastError)
	if completeErr != nil {
		writeError(w, http.StatusInternalServerError, "device action completion persistence failed")
		return
	}
	_ = s.recordAudit(context.Background(), audit.Event{Category: "device", Action: "action.execute", Actor: "local-loopback", SubjectType: "device_action", SubjectID: completed.ID, Outcome: outcome, Risk: completed.Risk, Details: map[string]any{"connectionId": completed.ConnectionID, "entityId": completed.EntityID, "domain": completed.Domain, "service": completed.Service}})
	if executeErr != nil {
		writeJSON(w, http.StatusBadGateway, completed)
		return
	}
	writeJSON(w, http.StatusOK, completed)
}

func (s *Server) denyDeviceAction(w http.ResponseWriter, r *http.Request) {
	var req denyDeviceActionRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	denied, err := s.store.DenyDeviceActionRequest(r.Context(), chi.URLParam(r, "id"), "local-api", req.Reason)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if err := s.recordAudit(r.Context(), audit.Event{Category: "device", Action: "action.deny", Actor: "local-api", SubjectType: "device_action", SubjectID: denied.ID, Outcome: "denied", Risk: denied.Risk, Details: map[string]any{"connectionId": denied.ConnectionID, "entityId": denied.EntityID, "domain": denied.Domain, "service": denied.Service}}); err != nil {
		writeError(w, http.StatusInternalServerError, "device action was denied but audit persistence failed")
		return
	}
	writeJSON(w, http.StatusOK, denied)
}

func actionEntityID(raw json.RawMessage) (string, bool) {
	var value struct {
		EntityID string `json:"entity_id"`
	}
	if json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value.EntityID) == "" {
		return "", false
	}
	return value.EntityID, true
}

func strictLoopbackApprovalRequest(r *http.Request) bool {
	if r == nil || !isLoopbackHost(r.RemoteAddr) {
		return false
	}
	for name := range r.Header {
		normalized := strings.ToLower(strings.TrimSpace(name))
		if normalized == "forwarded" || strings.HasPrefix(normalized, "x-forwarded-") || strings.HasPrefix(normalized, "cf-") || normalized == "true-client-ip" || normalized == "x-real-ip" {
			return false
		}
	}
	return true
}

type monitoringSnapshotResponse struct {
	CapturedAt        string                       `json:"capturedAt"`
	ActiveRuns        int                          `json:"activeRuns"`
	PendingApprovals  int64                        `json:"pendingApprovals"`
	ScheduleCount     int64                        `json:"scheduleCount"`
	NotificationCount int64                        `json:"notificationCount"`
	ChannelCount      int64                        `json:"channelCount"`
	DeviceCount       int64                        `json:"deviceCount"`
	Schedules         db.ScheduleStats             `json:"schedules"`
	Deliveries        db.NotificationDeliveryStats `json:"deliveries"`
	Channels          db.ChannelStats              `json:"channels"`
	DeviceActions     db.DeviceActionRequestStats  `json:"deviceActions"`
	BackgroundTasks   db.BackgroundTaskStats       `json:"backgroundTasks"`
	Workers           automation.WorkerStatus      `json:"workers"`
}

func (s *Server) monitoringSnapshot(w http.ResponseWriter, r *http.Request) {
	capturedAt := s.now().UTC().Format(time.RFC3339Nano)
	scheduleStats, err := s.store.ScheduleStats(r.Context(), capturedAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "monitoring snapshot unavailable")
		return
	}
	deliveryStats, err := s.store.NotificationDeliveryStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "monitoring snapshot unavailable")
		return
	}
	channelStats, err := s.store.ChannelStats(r.Context(), "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "monitoring snapshot unavailable")
		return
	}
	deviceStats, err := s.store.DeviceActionRequestStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "monitoring snapshot unavailable")
		return
	}
	backgroundStats, err := s.store.BackgroundTaskStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "monitoring snapshot unavailable")
		return
	}
	connections, err := s.store.ListIntegrationConnections(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "monitoring snapshot unavailable")
		return
	}
	var channelCount, deviceCount int64
	for _, connection := range connections {
		if !connection.Enabled {
			continue
		}
		switch connection.Kind {
		case "telegram":
			channelCount++
		case devices.HomeAssistantKind:
			deviceCount++
		}
	}
	var pendingApprovals int64
	if err := s.store.DB().QueryRowContext(r.Context(), `SELECT COUNT(*) FROM agent_tool_calls WHERE status = 'pending_approval'`).Scan(&pendingApprovals); err != nil {
		writeError(w, http.StatusInternalServerError, "monitoring snapshot unavailable")
		return
	}
	activeRuns := 0
	if s.runner != nil {
		activeRuns = s.runner.ActiveRunCount()
	}
	workers := automation.WorkerStatus{}
	if s.automation != nil {
		workers = s.automation.Status()
	}
	writeJSON(w, http.StatusOK, monitoringSnapshotResponse{
		CapturedAt: capturedAt, ActiveRuns: activeRuns, PendingApprovals: pendingApprovals,
		ScheduleCount: scheduleStats.Total, NotificationCount: deliveryStats.Total, ChannelCount: channelCount, DeviceCount: deviceCount,
		Schedules: scheduleStats, Deliveries: deliveryStats, Channels: channelStats, DeviceActions: deviceStats, BackgroundTasks: backgroundStats, Workers: workers,
	})
}
