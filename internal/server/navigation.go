package server

import (
	"net/http"
	"strings"

	"autoto/internal/db"
)

type navigationResponse struct {
	Projects      []db.Project                `json:"projects"`
	Conversations []db.NavigationConversation `json:"conversations"`
}

func (s *Server) navigation(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	includeArchived := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("includeArchived")), "true") || strings.TrimSpace(r.URL.Query().Get("includeArchived")) == "1"
	var projects []db.Project
	if hasUsers {
		user, ok := s.requireUser(w, r)
		if !ok {
			return
		}
		projects, err = s.listProjectsForAccount(r.Context(), user, includeArchived)
	} else {
		projects, err = s.store.ListProjectsWithOptions(r.Context(), includeArchived)
	}
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	conversations, err := s.store.ListNavigationConversationsWithOptions(r.Context(), includeArchived)
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	visibleProjects := make([]db.Project, 0, len(projects))
	for _, project := range projects {
		// Legacy conversation-flow containers stay out of ordinary navigation.
		// The explicit archive projection may include them so users can restore
		// or permanently delete historical data without making it interactive.
		if includeArchived || project.FlowMode != db.ProjectFlowModeConversation {
			visibleProjects = append(visibleProjects, project)
		}
	}
	allowedProjects := make(map[string]struct{}, len(visibleProjects))
	for _, project := range visibleProjects {
		allowedProjects[project.ID] = struct{}{}
	}
	filtered := make([]db.NavigationConversation, 0, len(conversations))
	for _, conversation := range conversations {
		if _, ok := allowedProjects[conversation.ProjectID]; ok {
			filtered = append(filtered, conversation)
		}
	}
	writeJSON(w, http.StatusOK, navigationResponse{
		Projects:      s.filterProjectsForRequest(r, visibleProjects),
		Conversations: s.filterNavigationConversationsForRequestWithLegacy(r, filtered, includeArchived),
	})
}
