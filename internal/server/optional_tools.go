package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"autoto/internal/db"
	"autoto/internal/tools"
)

const optionalToolsRequestBytes = 16 << 10

type OptionalToolCatalogSource interface {
	ListOptionalTools(context.Context) ([]tools.Tool, error)
}

type OptionalToolCatalogFunc func(context.Context) ([]tools.Tool, error)

func (fn OptionalToolCatalogFunc) ListOptionalTools(ctx context.Context) ([]tools.Tool, error) {
	return fn(ctx)
}

type RegistryOptionalToolCatalog struct {
	Registry *tools.Registry
}

func (source RegistryOptionalToolCatalog) ListOptionalTools(context.Context) ([]tools.Tool, error) {
	if source.Registry == nil {
		return []tools.Tool{}, nil
	}
	return source.Registry.List(), nil
}

type optionalToolsAPI struct {
	store           *db.Store
	catalog         OptionalToolCatalogSource
	authorizeTarget func(http.ResponseWriter, *http.Request, db.ToolAvailabilityTarget) bool
	authorizeRule   func(http.ResponseWriter, *http.Request, db.ToolAvailabilityRule) bool
}

type optionalToolCatalogItem struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Description string `json:"description,omitempty"`
	Domain      string `json:"domain"`
	Source      string `json:"source"`
	SourceID    string `json:"sourceId,omitempty"`
}

type optionalToolRuleResponse struct {
	db.ToolAvailabilityRule
	Orphan bool `json:"orphan"`
}

type optionalToolEffectiveResponse struct {
	optionalToolCatalogItem
	db.ToolAvailabilityDecision
	Orphan bool `json:"orphan"`
}

type optionalToolRevisionResponse struct {
	Sequence    int64  `json:"sequence"`
	RuleID      string `json:"ruleId"`
	Revision    int64  `json:"revision"`
	Operation   string `json:"operation"`
	ToolName    string `json:"toolName"`
	Scope       string `json:"scope"`
	ProjectID   string `json:"projectId,omitempty"`
	WorkspaceID string `json:"workspaceId,omitempty"`
	State       string `json:"state"`
	Deleted     bool   `json:"deleted"`
	CreatedAt   string `json:"createdAt"`
}

type optionalToolRulePutRequest struct {
	ToolName         string `json:"toolName"`
	Scope            string `json:"scope"`
	ProjectID        string `json:"projectId"`
	WorkspaceID      string `json:"workspaceId"`
	State            string `json:"state"`
	ExpectedRevision int64  `json:"expectedRevision"`
}

type optionalToolRuleDeleteRequest struct {
	ExpectedRevision int64 `json:"expectedRevision"`
}

// NewOptionalToolsHandler returns an independently mountable API module.
// TODO(optional-tools-integration): mount this handler at /api/optional-tools
// from the shared route assembly and pass a catalog source combining the core
// registry with dynamic plugin tools.
func NewOptionalToolsHandler(store *db.Store, catalog OptionalToolCatalogSource) http.Handler {
	api := &optionalToolsAPI{store: store, catalog: catalog}
	router := chi.NewRouter()
	router.Get("/catalog", api.listCatalog)
	router.Get("/rules", api.listRules)
	router.Put("/rules", api.putRule)
	router.Delete("/rules/{id}", api.deleteRule)
	router.Get("/rules/{id}/revisions", api.listRevisions)
	router.Get("/effective", api.listEffective)
	return router
}

// OptionalToolsHandler exposes a core-registry-only integration point without
// changing shared Server routing. The main route assembly can mount it later.
func (s *Server) OptionalToolsHandler() http.Handler {
	return NewOptionalToolsHandler(s.store, RegistryOptionalToolCatalog{Registry: s.toolRegistrySnapshot()})
}

func (s *Server) mountOptionalToolsRoutes(router chi.Router) {
	api := &optionalToolsAPI{
		store:           s.store,
		catalog:         RegistryOptionalToolCatalog{Registry: s.toolRegistrySnapshot()},
		authorizeTarget: s.requireToolAvailabilityTargetAccess,
		authorizeRule:   s.requireToolAvailabilityRuleAccess,
	}
	router.Route("/api/optional-tools", func(router chi.Router) {
		router.Get("/catalog", api.listCatalog)
		router.Get("/rules", api.listRules)
		router.With(s.fullRemoteAccessGuard).Put("/rules", api.putRule)
		router.With(s.fullRemoteAccessGuard).Delete("/rules/{id}", api.deleteRule)
		router.Get("/rules/{id}/revisions", api.listRevisions)
		router.Get("/effective", api.listEffective)
	})
}

func (api *optionalToolsAPI) ready(w http.ResponseWriter) bool {
	if api == nil || api.store == nil {
		writeError(w, http.StatusServiceUnavailable, "optional tools are not configured")
		return false
	}
	return true
}

func (api *optionalToolsAPI) catalogItems(ctx context.Context) ([]optionalToolCatalogItem, error) {
	if api.catalog == nil {
		return []optionalToolCatalogItem{}, nil
	}
	listed, err := api.catalog.ListOptionalTools(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]optionalToolCatalogItem, len(listed))
	for _, tool := range listed {
		if tool == nil {
			continue
		}
		name := tool.Name()
		if !validOptionalToolName(name) {
			continue
		}
		metadata := tools.CatalogMetadata{}
		if provider, ok := tool.(tools.CatalogMetadataProvider); ok {
			metadata = provider.CatalogMetadata()
		}
		item := optionalToolCatalogItem{
			Name:        name,
			DisplayName: safeCatalogText(metadata.DisplayName, 160),
			Description: safeCatalogText(tool.Description(), 2000),
			Domain:      safeCatalogToken(metadata.Domain, 64),
			Source:      safeCatalogToken(metadata.Source, 64),
			SourceID:    safeCatalogText(metadata.SourceID, 128),
		}
		if item.DisplayName == "" {
			item.DisplayName = name
		}
		if item.Domain == "" {
			item.Domain = defaultToolDomain(name)
		}
		if item.Source == "" {
			item.Source = "core"
		}
		if _, exists := byName[name]; !exists {
			byName[name] = item
		}
	}
	items := make([]optionalToolCatalogItem, 0, len(byName))
	for _, item := range byName {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Domain != items[j].Domain {
			return items[i].Domain < items[j].Domain
		}
		return strings.ToLower(items[i].DisplayName) < strings.ToLower(items[j].DisplayName)
	})
	return items, nil
}

func safeCatalogText(value string, max int) string {
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) {
		return ""
	}
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, value)
	if len(value) > max {
		value = value[:max]
		value = strings.ToValidUTF8(value, "")
	}
	return value
}

func safeCatalogToken(value string, max int) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > max {
		return ""
	}
	for _, r := range value {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_') {
			return ""
		}
	}
	return value
}

func defaultToolDomain(name string) string {
	lower := strings.ToLower(name)
	switch {
	case lower == "read" || lower == "write" || lower == "edit" || lower == "glob" || lower == "grep":
		return "filesystem"
	case lower == "bash" || strings.Contains(lower, "pipeline"):
		return "execution"
	case strings.HasPrefix(lower, "web"):
		return "web"
	case strings.HasPrefix(lower, "mcp") || strings.Contains(lower, "__"):
		return "integrations"
	case strings.Contains(lower, "agent") || strings.Contains(lower, "task") || strings.Contains(lower, "context") || strings.Contains(lower, "question"):
		return "coordination"
	default:
		return "core"
	}
}

func (api *optionalToolsAPI) listCatalog(w http.ResponseWriter, r *http.Request) {
	if !api.ready(w) {
		return
	}
	if api.authorizeTarget != nil && !api.authorizeTarget(w, r, db.ToolAvailabilityTarget{Scope: db.ToolAvailabilityScopeGlobal}) {
		return
	}
	if !optionalToolsQueryAllowed(r, nil) {
		writeError(w, http.StatusBadRequest, "unsupported query parameter")
		return
	}
	items, err := api.catalogItems(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load tool catalog")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": items, "count": len(items)})
}

func optionalToolsTargetFromQuery(r *http.Request) (db.ToolAvailabilityTarget, error) {
	target := db.ToolAvailabilityTarget{
		Scope:       r.URL.Query().Get("scope"),
		ProjectID:   r.URL.Query().Get("projectId"),
		WorkspaceID: r.URL.Query().Get("workspaceId"),
	}
	return validateOptionalToolsTarget(target)
}

func validateOptionalToolsTarget(target db.ToolAvailabilityTarget) (db.ToolAvailabilityTarget, error) {
	if target.Scope == "" {
		target.Scope = db.ToolAvailabilityScopeGlobal
	}
	if strings.TrimSpace(target.Scope) != target.Scope || strings.TrimSpace(target.ProjectID) != target.ProjectID || strings.TrimSpace(target.WorkspaceID) != target.WorkspaceID {
		return db.ToolAvailabilityTarget{}, errors.New("scope and target ids must use canonical whitespace")
	}
	if !validOptionalOpaqueID(target.ProjectID) || !validOptionalOpaqueID(target.WorkspaceID) {
		return db.ToolAvailabilityTarget{}, errors.New("projectId and workspaceId must be at most 128 bytes without control characters")
	}
	switch target.Scope {
	case db.ToolAvailabilityScopeGlobal:
		if target.ProjectID != "" || target.WorkspaceID != "" {
			return db.ToolAvailabilityTarget{}, errors.New("global scope forbids projectId and workspaceId")
		}
	case db.ToolAvailabilityScopeProject:
		if target.ProjectID == "" || target.WorkspaceID != "" {
			return db.ToolAvailabilityTarget{}, errors.New("project scope requires projectId and forbids workspaceId")
		}
	case db.ToolAvailabilityScopeWorkspace:
		if target.ProjectID == "" || target.WorkspaceID == "" {
			return db.ToolAvailabilityTarget{}, errors.New("workspace scope requires projectId and workspaceId")
		}
	default:
		return db.ToolAvailabilityTarget{}, errors.New("scope must be global, project, or workspace")
	}
	return target, nil
}

func validOptionalOpaqueID(value string) bool {
	if len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validOptionalToolName(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for i, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) || strings.ContainsRune("/\\?#", r) || (i == 0 && (r == '.' || r == ':')) {
			return false
		}
	}
	return true
}

func optionalToolsQueryAllowed(r *http.Request, allowed map[string]bool) bool {
	for key := range r.URL.Query() {
		if allowed == nil || !allowed[key] {
			return false
		}
	}
	return true
}

func (api *optionalToolsAPI) listRules(w http.ResponseWriter, r *http.Request) {
	if !api.ready(w) {
		return
	}
	allowed := map[string]bool{"scope": true, "projectId": true, "workspaceId": true, "includeDeleted": true}
	if !optionalToolsQueryAllowed(r, allowed) {
		writeError(w, http.StatusBadRequest, "unsupported query parameter")
		return
	}
	target, err := optionalToolsTargetFromQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if api.authorizeTarget != nil && !api.authorizeTarget(w, r, target) {
		return
	}
	includeDeleted := false
	if raw := r.URL.Query().Get("includeDeleted"); raw != "" {
		includeDeleted, err = strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "includeDeleted must be true or false")
			return
		}
	}
	rules, err := api.store.ListToolAvailabilityRules(r.Context(), target, includeDeleted)
	if err != nil {
		writeOptionalToolsStoreError(w, err)
		return
	}
	catalog, err := api.catalogItems(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load tool catalog")
		return
	}
	known := make(map[string]bool, len(catalog))
	for _, item := range catalog {
		known[item.Name] = true
	}
	responses := make([]optionalToolRuleResponse, 0, len(rules))
	for _, rule := range rules {
		responses = append(responses, optionalToolRuleResponse{ToolAvailabilityRule: rule, Orphan: !known[rule.ToolName]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": responses, "count": len(responses)})
}

func decodeOptionalToolsJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, optionalToolsRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
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

func (api *optionalToolsAPI) putRule(w http.ResponseWriter, r *http.Request) {
	if !api.ready(w) {
		return
	}
	if !optionalToolsQueryAllowed(r, nil) {
		writeError(w, http.StatusBadRequest, "unsupported query parameter")
		return
	}
	var request optionalToolRulePutRequest
	if err := decodeOptionalToolsJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	if !validOptionalToolName(request.ToolName) {
		writeError(w, http.StatusBadRequest, "invalid toolName")
		return
	}
	if request.State != db.ToolAvailabilityEnabled && request.State != db.ToolAvailabilityDisabled {
		writeError(w, http.StatusBadRequest, "state must be enabled or disabled")
		return
	}
	if request.ExpectedRevision < 0 {
		writeError(w, http.StatusBadRequest, "expectedRevision cannot be negative")
		return
	}
	target, err := validateOptionalToolsTarget(db.ToolAvailabilityTarget{Scope: request.Scope, ProjectID: request.ProjectID, WorkspaceID: request.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if api.authorizeTarget != nil && !api.authorizeTarget(w, r, target) {
		return
	}
	rule, err := api.store.SetToolAvailabilityRuleCAS(r.Context(), target, request.ToolName, request.State, request.ExpectedRevision, "api_request")
	if err != nil {
		writeOptionalToolsStoreError(w, err)
		return
	}
	status := http.StatusOK
	if rule.Revision == 1 {
		status = http.StatusCreated
	}
	writeJSON(w, status, rule)
}

func (api *optionalToolsAPI) deleteRule(w http.ResponseWriter, r *http.Request) {
	if !api.ready(w) {
		return
	}
	if !optionalToolsQueryAllowed(r, nil) {
		writeError(w, http.StatusBadRequest, "unsupported query parameter")
		return
	}
	id := chi.URLParam(r, "id")
	if strings.TrimSpace(id) != id || !validOptionalOpaqueID(id) || id == "" {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	var request optionalToolRuleDeleteRequest
	if err := decodeOptionalToolsJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	if request.ExpectedRevision < 1 {
		writeError(w, http.StatusBadRequest, "positive expectedRevision is required")
		return
	}
	current, err := api.store.GetToolAvailabilityRule(r.Context(), id)
	if err != nil {
		writeOptionalToolsStoreError(w, err)
		return
	}
	if api.authorizeRule != nil && !api.authorizeRule(w, r, current) {
		return
	}
	rule, err := api.store.DeleteToolAvailabilityRuleCAS(r.Context(), id, request.ExpectedRevision, "api_request")
	if err != nil {
		writeOptionalToolsStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "rule": rule})
}

func (api *optionalToolsAPI) listRevisions(w http.ResponseWriter, r *http.Request) {
	if !api.ready(w) {
		return
	}
	if !optionalToolsQueryAllowed(r, nil) {
		writeError(w, http.StatusBadRequest, "unsupported query parameter")
		return
	}
	id := chi.URLParam(r, "id")
	if strings.TrimSpace(id) != id || !validOptionalOpaqueID(id) || id == "" {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	current, err := api.store.GetToolAvailabilityRule(r.Context(), id)
	if err != nil {
		writeOptionalToolsStoreError(w, err)
		return
	}
	if api.authorizeRule != nil && !api.authorizeRule(w, r, current) {
		return
	}
	revisions, err := api.store.ListToolAvailabilityRevisions(r.Context(), id)
	if err != nil {
		writeOptionalToolsStoreError(w, err)
		return
	}
	responses := make([]optionalToolRevisionResponse, 0, len(revisions))
	for _, revision := range revisions {
		responses = append(responses, optionalToolRevisionResponse{
			Sequence: revision.Sequence, RuleID: revision.RuleID, Revision: revision.Revision,
			Operation: revision.Operation, ToolName: revision.ToolName, Scope: revision.Scope,
			ProjectID: revision.ProjectID, WorkspaceID: revision.WorkspaceID, State: revision.State,
			Deleted: revision.Deleted, CreatedAt: revision.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"revisions": responses, "count": len(responses)})
}

func (api *optionalToolsAPI) listEffective(w http.ResponseWriter, r *http.Request) {
	if !api.ready(w) {
		return
	}
	allowed := map[string]bool{"scope": true, "projectId": true, "workspaceId": true, "toolName": true}
	if !optionalToolsQueryAllowed(r, allowed) {
		writeError(w, http.StatusBadRequest, "unsupported query parameter")
		return
	}
	target, err := optionalToolsTargetFromQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if api.authorizeTarget != nil && !api.authorizeTarget(w, r, target) {
		return
	}
	catalog, err := api.catalogItems(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load tool catalog")
		return
	}
	byName := make(map[string]optionalToolCatalogItem, len(catalog))
	for _, item := range catalog {
		byName[item.Name] = item
	}
	if requested := r.URL.Query().Get("toolName"); requested != "" {
		if !validOptionalToolName(requested) {
			writeError(w, http.StatusBadRequest, "invalid toolName")
			return
		}
		item, exists := byName[requested]
		if !exists {
			item = optionalToolCatalogItem{Name: requested, DisplayName: requested, Domain: "orphan", Source: "orphan"}
		}
		byName = map[string]optionalToolCatalogItem{requested: item}
	} else {
		for _, relevant := range relevantOptionalToolTargets(target) {
			rules, listErr := api.store.ListToolAvailabilityRules(r.Context(), relevant, false)
			if listErr != nil {
				writeOptionalToolsStoreError(w, listErr)
				return
			}
			for _, rule := range rules {
				if _, exists := byName[rule.ToolName]; !exists {
					byName[rule.ToolName] = optionalToolCatalogItem{Name: rule.ToolName, DisplayName: rule.ToolName, Domain: "orphan", Source: "orphan"}
				}
			}
		}
	}
	responses := make([]optionalToolEffectiveResponse, 0, len(byName))
	for name, item := range byName {
		decision, resolveErr := api.store.ResolveToolAvailability(r.Context(), target, name)
		if resolveErr != nil {
			writeOptionalToolsStoreError(w, resolveErr)
			return
		}
		responses = append(responses, optionalToolEffectiveResponse{optionalToolCatalogItem: item, ToolAvailabilityDecision: decision, Orphan: item.Source == "orphan"})
	}
	sort.Slice(responses, func(i, j int) bool {
		if responses[i].Domain != responses[j].Domain {
			return responses[i].Domain < responses[j].Domain
		}
		return strings.ToLower(responses[i].DisplayName) < strings.ToLower(responses[j].DisplayName)
	})
	writeJSON(w, http.StatusOK, map[string]any{"tools": responses, "count": len(responses)})
}

func relevantOptionalToolTargets(target db.ToolAvailabilityTarget) []db.ToolAvailabilityTarget {
	targets := []db.ToolAvailabilityTarget{{Scope: db.ToolAvailabilityScopeGlobal}}
	if target.Scope == db.ToolAvailabilityScopeProject || target.Scope == db.ToolAvailabilityScopeWorkspace {
		targets = append(targets, db.ToolAvailabilityTarget{Scope: db.ToolAvailabilityScopeProject, ProjectID: target.ProjectID})
	}
	if target.Scope == db.ToolAvailabilityScopeWorkspace {
		targets = append(targets, target)
	}
	return targets
}

func writeOptionalToolsStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrConflict):
		writeError(w, http.StatusConflict, "tool availability rule revision conflict")
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "tool availability rule not found")
	default:
		writeError(w, http.StatusInternalServerError, "tool availability operation failed")
	}
}
