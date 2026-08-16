package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"

	"autoto/internal/audit"
	"autoto/internal/db"
)

type createAgentRequest struct {
	WorklineID     *string `json:"worklineId"`
	ParentAgentID  string  `json:"parentAgentId,omitempty"`
	Title          string  `json:"title"`
	Model          string  `json:"model"`
	PermissionMode string  `json:"permissionMode"`
	CWD            string  `json:"cwd"`
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	if err := rejectUnknownQuery(r, "limit"); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	limit, err := queryInt(r, "limit", 200, 1, 500)
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	agents, err := s.store.ListAgents(r.Context(), limit)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	agents, ok := s.filterAgentsByMembership(w, r, agents)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.filterAgentsForRequest(r, agents))
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	var req createAgentRequest
	if err := decodeLimitedJSON(w, r, &req, maxAgentJSONBody); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	worklineID := ""
	if req.WorklineID != nil {
		worklineID = strings.TrimSpace(*req.WorklineID)
	}
	parentAgentID := strings.TrimSpace(req.ParentAgentID)
	title := strings.TrimSpace(req.Title)
	model := strings.TrimSpace(req.Model)
	permissionMode := strings.TrimSpace(req.PermissionMode)
	cwd := strings.TrimSpace(req.CWD)
	for _, field := range []struct {
		name     string
		value    string
		max      int
		required bool
	}{
		{"worklineId", worklineID, 128, false}, {"parentAgentId", parentAgentID, 128, false},
		{"title", title, 120, true}, {"model", model, 256, true}, {"cwd", cwd, 4096, true},
	} {
		if err := validateAPIText(field.name, field.value, field.max, field.required, false); err != nil {
			s.writeRequestError(w, r, http.StatusBadRequest, err)
			return
		}
	}
	permissionMode, ok, message := s.permissionModeAllowedForRequest(r, permissionMode)
	if !ok {
		writeError(w, http.StatusBadRequest, message)
		return
	}
	if worklineID != "" && !s.requireProjectResourceAccess(w, r, projectAccessTarget{kind: projectAccessWorkline, id: worklineID}) {
		return
	}
	agentType := "primary"
	if parentAgentID != "" {
		if !s.requireProjectResourceAccess(w, r, projectAccessTarget{kind: projectAccessAgent, id: parentAgentID}) {
			return
		}
		agentType = "subagent"
	}
	cwd, err := s.resolveCWDForRequest(r, cwd)
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	info, err := os.Stat(cwd)
	if err != nil {
		s.writeRequestError(w, r, statusFromFSError(err), err)
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, "cwd must be a directory")
		return
	}
	created, err := s.store.CreateAgent(r.Context(), db.Agent{
		WorklineID: worklineID, ParentAgentID: parentAgentID, Type: agentType,
		Title: title, Model: model, PermissionMode: permissionMode, Status: "idle", CWD: cwd,
		PlanMode: s.configSnapshot().Agent.DefaultStartInPlanMode,
	})
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

type updatePlanModeRequest struct {
	PlanMode *bool `json:"planMode"`
}

func (s *Server) updateAgentPlanMode(w http.ResponseWriter, r *http.Request) {
	var req updatePlanModeRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if req.PlanMode == nil {
		writeError(w, http.StatusBadRequest, "planMode is required")
		return
	}
	agentID := strings.TrimSpace(chi.URLParam(r, "id"))
	agent, err := s.updatePersistedAgentPlanMode(r.Context(), agentID, *req.PlanMode)
	if err != nil {
		s.writeRequestError(w, r, statusFromError(err), err)
		return
	}
	if s.runner != nil {
		s.runner.InvalidateAgentApprovals(agentID, "tool approval invalidated because plan mode changed")
	}
	actor, actorErr := s.reviewActor(r)
	if actorErr != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, actorErr)
		return
	}
	if err := s.recordAudit(r.Context(), audit.Event{
		Category: "review", Action: "agent.plan_mode.update", Actor: actor, AgentID: agent.ID,
		SubjectType: "agent", SubjectID: agent.ID, Outcome: "success", Risk: "medium",
		Details: map[string]any{"planMode": agent.PlanMode, "entityGeneration": agent.EntityGeneration, "permissionGeneration": agent.PermissionGeneration},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "plan mode was updated but audit persistence failed")
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

type updatePlanReflectionRequest struct {
	PlanReflection *bool `json:"planReflection"`
}

func (s *Server) updateAgentPlanReflection(w http.ResponseWriter, r *http.Request) {
	var req updatePlanReflectionRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if req.PlanReflection == nil {
		writeError(w, http.StatusBadRequest, "planReflection is required")
		return
	}
	agentID := strings.TrimSpace(chi.URLParam(r, "id"))
	if err := validateAPIIdentifier("agent id", agentID); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	agent, err := s.store.UpdateAgentPlanReflection(r.Context(), agentID, *req.PlanReflection)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	actor, actorErr := s.reviewActor(r)
	if actorErr != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, actorErr)
		return
	}
	if err := s.recordAudit(r.Context(), audit.Event{
		Category: "review", Action: "agent.plan_reflection.update", Actor: actor, AgentID: agent.ID,
		SubjectType: "agent", SubjectID: agent.ID, Outcome: "success", Risk: "low",
		Details: map[string]any{"planReflection": agent.PlanReflection, "entityGeneration": agent.EntityGeneration},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "plan reflection was updated but audit persistence failed")
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

// updatePersistedAgentPlanMode is a compatibility bridge for the existing
// agents.plan_mode column. It keeps entity and permission generations in sync
// until the Agent workflow owns a first-class mode mutation API.
func (s *Server) updatePersistedAgentPlanMode(ctx context.Context, agentID string, planMode bool) (db.Agent, error) {
	if s == nil || s.store == nil {
		return db.Agent{}, errors.New("agent store is unavailable")
	}
	agentID = strings.TrimSpace(agentID)
	if err := validateAPIIdentifier("agent id", agentID); err != nil {
		return db.Agent{}, err
	}
	result, err := s.store.DB().ExecContext(ctx, `UPDATE agents SET plan_mode = ?, entity_generation = entity_generation + 1, permission_generation = permission_generation + 1, updated_at = ? WHERE id = ?`, boolToInt(planMode), db.Now(), agentID)
	if err != nil {
		return db.Agent{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return db.Agent{}, err
	}
	if affected != 1 {
		return db.Agent{}, sql.ErrNoRows
	}
	return s.store.GetAgent(ctx, agentID)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *Server) listAgentChildren(w http.ResponseWriter, r *http.Request) {
	if err := rejectUnknownQuery(r); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	parentAgentID := strings.TrimSpace(chi.URLParam(r, "id"))
	if err := validateAPIIdentifier("agent id", parentAgentID); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if _, err := s.store.GetAgent(r.Context(), parentAgentID); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	children, err := s.store.ListChildAgents(r.Context(), parentAgentID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	children, ok := s.filterAgentsByMembership(w, r, children)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.filterAgentsForRequest(r, children))
}
