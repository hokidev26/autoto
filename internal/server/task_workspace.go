package server

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"autoto/internal/db"
)

type assignSpecTaskRequest struct {
	TargetAgentID        string `json:"targetAgentId"`
	ExpectedRevision     int64  `json:"expectedRevision"`
	AcknowledgeProtected bool   `json:"acknowledgeProtected,omitempty"`
}

func (s *Server) taskWorkspace(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}

	var projects []db.Project
	if hasUsers {
		user, ok := s.requireUser(w, r)
		if !ok {
			return
		}
		projects, err = s.store.ListProjectsForUser(r.Context(), user.ID)
	} else {
		projects, err = s.store.ListProjects(r.Context())
	}
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	projects = s.filterProjectsForRequest(r, projects)

	workspace, err := s.store.ListTaskWorkspace(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.filterTaskWorkspaceForRequest(r, workspace, projects))
}

func (s *Server) filterTaskWorkspaceForRequest(r *http.Request, workspace db.TaskWorkspace, projects []db.Project) db.TaskWorkspace {
	allowedProjects := make(map[string]struct{}, len(projects))
	for _, project := range projects {
		allowedProjects[project.ID] = struct{}{}
	}

	filtered := db.TaskWorkspace{Projects: make([]db.TaskWorkspaceProject, 0, len(projects))}
	restrictedFilesystem := s.capabilitiesForRequest(r).FilesystemScope == "project"
	for _, project := range workspace.Projects {
		if _, ok := allowedProjects[project.ID]; !ok {
			continue
		}
		project.Worklines = append([]db.TaskWorkspaceWorkline(nil), project.Worklines...)
		project.Agents = append([]db.TaskWorkspaceAgent(nil), project.Agents...)
		if project.Worklines == nil {
			project.Worklines = make([]db.TaskWorkspaceWorkline, 0)
		}
		if project.Agents == nil {
			project.Agents = make([]db.TaskWorkspaceAgent, 0)
		}
		if restrictedFilesystem {
			allowedWorklines := make(map[string]struct{}, len(project.Worklines))
			worklines := make([]db.TaskWorkspaceWorkline, 0, len(project.Worklines))
			for _, workline := range project.Worklines {
				if strings.TrimSpace(workline.WorktreePath) != "" && !s.filesystemPathWithinProjectRoot(workline.WorktreePath) {
					continue
				}
				allowedWorklines[workline.ID] = struct{}{}
				worklines = append(worklines, workline)
			}
			agents := make([]db.TaskWorkspaceAgent, 0, len(project.Agents))
			for _, agent := range project.Agents {
				if _, ok := allowedWorklines[agent.WorklineID]; ok && s.filesystemPathWithinProjectRoot(agent.CWD) {
					agents = append(agents, agent)
				}
			}
			project.Worklines = worklines
			project.Agents = agents
		}
		project.Counts = db.SpecTaskStatusCounts{}
		for _, agent := range project.Agents {
			addSpecTaskCounts(&project.Counts, agent.Counts)
		}
		filtered.Summary.ProjectCount++
		filtered.Summary.AgentCount += len(project.Agents)
		addSpecTaskCounts(&filtered.Summary.SpecTaskStatusCounts, project.Counts)
		filtered.Projects = append(filtered.Projects, project)
	}
	return filtered
}

func addSpecTaskCounts(target *db.SpecTaskStatusCounts, source db.SpecTaskStatusCounts) {
	if target == nil {
		return
	}
	target.Todo += source.Todo
	target.Doing += source.Doing
	target.Blocked += source.Blocked
	target.Done += source.Done
	target.Total += source.Total
}

func (s *Server) assignSpecTask(w http.ResponseWriter, r *http.Request) {
	var req assignSpecTaskRequest
	if err := decodeLimitedJSON(w, r, &req, 8<<10); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}

	sourceAgentID := strings.TrimSpace(chi.URLParam(r, "id"))
	taskID := strings.TrimSpace(chi.URLParam(r, "taskId"))
	targetAgentID := strings.TrimSpace(req.TargetAgentID)
	if err := validateAPIIdentifier("agent id", sourceAgentID); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := validateAPIIdentifier("task id", taskID); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := validateAPIIdentifier("target agent id", targetAgentID); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if req.ExpectedRevision < 1 {
		writeError(w, http.StatusBadRequest, "expectedRevision is required")
		return
	}
	if !s.requireProjectResourceAccess(w, r, projectAccessTarget{kind: projectAccessAgent, id: targetAgentID}) {
		return
	}

	result, err := s.store.AssignSpecTask(r.Context(), sourceAgentID, taskID, targetAgentID, req.ExpectedRevision, req.AcknowledgeProtected, "local-api")
	if err != nil {
		s.writeSpecStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
