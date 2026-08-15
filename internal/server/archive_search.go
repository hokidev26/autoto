package server

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"autoto/internal/db"
	"autoto/internal/hooks"
)

const (
	archiveSearchMaxQueryRunes = 80
	archiveSearchMaxResults    = 20
	archiveSearchMaxSnippets   = 3
)

type archiveSearchMatch struct {
	MessageID string `json:"messageId"`
	Role      string `json:"role"`
	CreatedAt string `json:"createdAt"`
	Snippet   string `json:"snippet"`
}

type archiveSearchResult struct {
	AgentID         string               `json:"agentId"`
	AgentTitle      string               `json:"agentTitle"`
	ProjectID       string               `json:"projectId"`
	ProjectName     string               `json:"projectName"`
	ProjectArchived bool                 `json:"projectArchived"`
	AgentArchived   bool                 `json:"agentArchived"`
	TitleMatch      bool                 `json:"titleMatch"`
	Matches         []archiveSearchMatch `json:"matches"`
}

type archiveSearchResponse struct {
	Query   string                `json:"query"`
	Results []archiveSearchResult `json:"results"`
}

func (s *Server) archiveSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" || utf8.RuneCountInString(query) > archiveSearchMaxQueryRunes {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	conversations, ok := s.archivedSearchConversations(w, r)
	if !ok {
		return
	}
	agentIDs := make([]string, 0, len(conversations))
	byAgent := make(map[string]db.NavigationConversation, len(conversations))
	for _, conversation := range conversations {
		if conversation.AgentID == "" {
			continue
		}
		agentIDs = append(agentIDs, conversation.AgentID)
		byAgent[conversation.AgentID] = conversation
	}
	hits, err := s.store.SearchAgentMessages(r.Context(), agentIDs, query, 0)
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}

	needle := strings.ToLower(query)
	contains := func(value string) bool {
		return needle != "" && strings.Contains(strings.ToLower(value), needle)
	}
	resultsByAgent := make(map[string]*archiveSearchResult, len(conversations))
	order := make([]string, 0, archiveSearchMaxResults)
	ensure := func(conversation db.NavigationConversation, titleMatch bool) *archiveSearchResult {
		if result := resultsByAgent[conversation.AgentID]; result != nil {
			result.TitleMatch = result.TitleMatch || titleMatch
			return result
		}
		if len(order) >= archiveSearchMaxResults {
			return nil
		}
		result := &archiveSearchResult{
			AgentID:         conversation.AgentID,
			AgentTitle:      conversation.AgentTitle,
			ProjectID:       conversation.ProjectID,
			ProjectName:     conversation.ProjectName,
			ProjectArchived: strings.TrimSpace(conversation.ProjectArchivedAt) != "",
			AgentArchived:   strings.TrimSpace(conversation.AgentArchivedAt) != "",
			TitleMatch:      titleMatch,
			Matches:         []archiveSearchMatch{},
		}
		resultsByAgent[conversation.AgentID] = result
		order = append(order, conversation.AgentID)
		return result
	}

	for _, hit := range hits {
		conversation, found := byAgent[hit.AgentID]
		if !found {
			continue
		}
		result := ensure(conversation, contains(conversation.AgentTitle) || contains(conversation.ProjectName))
		if result == nil || len(result.Matches) >= archiveSearchMaxSnippets {
			continue
		}
		result.Matches = append(result.Matches, archiveSearchMatch{
			MessageID: hit.MessageID,
			Role:      hit.Role,
			CreatedAt: hit.CreatedAt,
			Snippet:   hooks.RedactText(hit.Snippet),
		})
	}
	for _, conversation := range conversations {
		if contains(conversation.AgentTitle) || contains(conversation.ProjectName) {
			ensure(conversation, true)
		}
	}

	results := make([]archiveSearchResult, 0, len(order))
	for _, agentID := range order {
		if result := resultsByAgent[agentID]; result != nil {
			results = append(results, *result)
		}
		if len(results) >= archiveSearchMaxResults {
			break
		}
	}
	writeJSON(w, http.StatusOK, archiveSearchResponse{Query: query, Results: results})
}

func (s *Server) archivedSearchConversations(w http.ResponseWriter, r *http.Request) ([]db.NavigationConversation, bool) {
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return nil, false
	}
	var projects []db.Project
	if hasUsers {
		user, ok := s.requireUser(w, r)
		if !ok {
			return nil, false
		}
		projects, err = s.store.ListProjectsForUserWithOptions(r.Context(), user.ID, true)
	} else {
		projects, err = s.store.ListProjectsWithOptions(r.Context(), true)
	}
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return nil, false
	}
	conversations, err := s.store.ListNavigationConversationsWithOptions(r.Context(), true)
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return nil, false
	}
	allowedProjects := make(map[string]struct{}, len(projects))
	for _, project := range projects {
		allowedProjects[project.ID] = struct{}{}
	}
	filtered := make([]db.NavigationConversation, 0, len(conversations))
	for _, conversation := range conversations {
		if _, ok := allowedProjects[conversation.ProjectID]; !ok {
			continue
		}
		if strings.TrimSpace(conversation.AgentArchivedAt) == "" && strings.TrimSpace(conversation.ProjectArchivedAt) == "" {
			continue
		}
		filtered = append(filtered, conversation)
	}
	return s.filterNavigationConversationsForRequestWithLegacy(r, filtered, true), true
}
