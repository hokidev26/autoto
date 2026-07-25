package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"autoto/internal/db"
	"autoto/internal/hooks"

	"github.com/go-chi/chi/v5"
)

const lifecycleHookRequestBytes int64 = 256 << 10

type LifecycleHooksAPI struct {
	store              *db.Store
	executor           hooks.Executor
	now                func() time.Time
	authorizeHook      func(http.ResponseWriter, *http.Request, hooks.Hook) bool
	authorizeTestAgent func(http.ResponseWriter, *http.Request, db.Agent) bool
	filterHookList     func(http.ResponseWriter, *http.Request, []hooks.Hook) ([]hooks.Hook, bool)
}

type lifecycleHookWrite struct {
	ExpectedRevision int64               `json:"expectedRevision,omitempty"`
	Name             string              `json:"name"`
	Description      string              `json:"description,omitempty"`
	Enabled          *bool               `json:"enabled,omitempty"`
	Event            hooks.EventName     `json:"event"`
	Scope            hooks.Scope         `json:"scope"`
	Priority         int                 `json:"priority"`
	Filter           hooks.Filter        `json:"filter,omitempty"`
	Mode             hooks.DispatchMode  `json:"mode"`
	FailurePolicy    hooks.FailurePolicy `json:"failurePolicy"`
	Action           hooks.Action        `json:"action"`
}

type lifecycleHookTestRequest struct {
	Event hooks.Event `json:"event"`
}

type lifecycleHookActionResponse struct {
	Kind             hooks.ActionKind `json:"kind"`
	Shell            any              `json:"shell,omitempty"`
	HTTP             any              `json:"http,omitempty"`
	LLM              any              `json:"llm,omitempty"`
	SecretConfigured map[string]bool  `json:"secretConfigured,omitempty"`
}

type lifecycleHookResponse struct {
	ID            string                      `json:"id"`
	Name          string                      `json:"name"`
	Description   string                      `json:"description,omitempty"`
	Enabled       bool                        `json:"enabled"`
	Event         hooks.EventName             `json:"event"`
	Scope         hooks.Scope                 `json:"scope"`
	Priority      int                         `json:"priority"`
	Filter        hooks.Filter                `json:"filter,omitempty"`
	Mode          hooks.DispatchMode          `json:"mode"`
	FailurePolicy hooks.FailurePolicy         `json:"failurePolicy"`
	Action        lifecycleHookActionResponse `json:"action"`
	Revision      int64                       `json:"revision"`
	CreatedAt     string                      `json:"createdAt,omitempty"`
	UpdatedAt     string                      `json:"updatedAt,omitempty"`
}

type lifecycleHookHistoryItem struct {
	Execution db.LifecycleHookExecution `json:"execution"`
	Attempts  []db.LifecycleHookAttempt `json:"attempts"`
}

func NewLifecycleHooksAPI(store *db.Store, gateway hooks.Gateway) *LifecycleHooksAPI {
	return &LifecycleHooksAPI{store: store, executor: hooks.Executor{Gateway: gateway, Limiter: hooks.NewLimiter(4, 8)}, now: time.Now}
}

func (s *Server) mountLifecycleHookRoutes(router chi.Router) {
	var gateway hooks.Gateway
	if s.runner != nil {
		gateway = s.runner.LifecycleHookGateway()
	}
	api := NewLifecycleHooksAPI(s.store, gateway)
	api.authorizeHook = func(w http.ResponseWriter, r *http.Request, hook hooks.Hook) bool {
		return s.requireLifecycleHookScopeAccess(w, r, hook.Scope)
	}
	api.authorizeTestAgent = func(w http.ResponseWriter, r *http.Request, agent db.Agent) bool {
		return s.requireProjectResourceAccess(w, r, projectAccessTarget{kind: projectAccessAgent, id: agent.ID})
	}
	api.filterHookList = s.filterAccessibleLifecycleHooks
	router.Group(func(router chi.Router) {
		router.Use(s.fullRemoteAccessGuard)
		RegisterLifecycleHookRoutes(router, api)
	})
}

func (api *LifecycleHooksAPI) Routes() http.Handler {
	router := chi.NewRouter()
	RegisterLifecycleHookRoutes(router, api)
	return router
}

func RegisterLifecycleHookRoutes(router chi.Router, api *LifecycleHooksAPI) {
	if api == nil {
		return
	}
	router.Get("/api/lifecycle-hooks", api.listHooks)
	router.Post("/api/lifecycle-hooks", api.createHook)
	router.Route("/api/lifecycle-hooks", func(router chi.Router) {
		router.Get("/", api.listHooks)
		router.Post("/", api.createHook)
		router.Get("/{hookID}", api.getHook)
		router.Patch("/{hookID}", api.updateHook)
		router.Delete("/{hookID}", api.deleteHook)
		router.Get("/{hookID}/history", api.history)
		router.Post("/{hookID}/test", api.testHook)
		router.Post("/executions/{executionID}/cancel", api.cancelExecution)
		router.Post("/executions/{executionID}/retry", api.retryExecution)
	})
	router.Post("/api/lifecycle-hook-executions/{executionID}/cancel", api.cancelExecution)
	router.Post("/api/lifecycle-hook-executions/{executionID}/retry", api.retryExecution)
}

func (api *LifecycleHooksAPI) available(w http.ResponseWriter) bool {
	if api.store == nil {
		writeError(w, http.StatusServiceUnavailable, "lifecycle hook store is unavailable")
		return false
	}
	return true
}

func (api *LifecycleHooksAPI) authorizeExecution(w http.ResponseWriter, r *http.Request, executionID string) bool {
	if api.authorizeHook == nil {
		return true
	}
	execution, err := api.store.GetLifecycleHookExecution(r.Context(), executionID)
	if err != nil {
		api.writeStoreError(w, err)
		return false
	}
	hook, err := api.store.GetLifecycleHook(r.Context(), execution.HookID)
	if err != nil {
		api.writeStoreError(w, err)
		return false
	}
	return api.authorizeHook(w, r, hook)
}

func (api *LifecycleHooksAPI) listHooks(w http.ResponseWriter, r *http.Request) {
	if !api.available(w) {
		return
	}
	items, err := api.store.ListLifecycleHooks(r.Context())
	if err != nil {
		api.writeStoreError(w, err)
		return
	}
	if api.filterHookList != nil {
		var ok bool
		items, ok = api.filterHookList(w, r, items)
		if !ok {
			return
		}
	}
	responses := make([]lifecycleHookResponse, 0, len(items))
	for _, item := range items {
		responses = append(responses, lifecycleHookResponseFromHook(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"hooks": responses})
}

func (api *LifecycleHooksAPI) getHook(w http.ResponseWriter, r *http.Request) {
	if !api.available(w) {
		return
	}
	item, err := api.store.GetLifecycleHook(r.Context(), chi.URLParam(r, "hookID"))
	if err != nil {
		api.writeStoreError(w, err)
		return
	}
	if api.authorizeHook != nil && !api.authorizeHook(w, r, item) {
		return
	}
	writeJSON(w, http.StatusOK, lifecycleHookResponseFromHook(item))
}

func (api *LifecycleHooksAPI) createHook(w http.ResponseWriter, r *http.Request) {
	if !api.available(w) {
		return
	}
	var request lifecycleHookWrite
	if err := decodeLifecycleHookJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.ExpectedRevision != 0 {
		writeError(w, http.StatusBadRequest, "expectedRevision is not valid when creating a hook")
		return
	}
	candidate, err := hooks.NormalizeAndValidateHook(lifecycleHookFromWrite(request, nil))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if api.authorizeHook != nil && !api.authorizeHook(w, r, candidate) {
		return
	}
	created, err := api.store.CreateLifecycleHook(r.Context(), candidate)
	if err != nil {
		api.writeMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, lifecycleHookResponseFromHook(created))
}

func (api *LifecycleHooksAPI) updateHook(w http.ResponseWriter, r *http.Request) {
	if !api.available(w) {
		return
	}
	var request lifecycleHookWrite
	if err := decodeLifecycleHookJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.ExpectedRevision < 1 {
		writeError(w, http.StatusBadRequest, "expectedRevision must be positive")
		return
	}
	id := chi.URLParam(r, "hookID")
	current, err := api.store.GetLifecycleHook(r.Context(), id)
	if err != nil {
		api.writeStoreError(w, err)
		return
	}
	if api.authorizeHook != nil && !api.authorizeHook(w, r, current) {
		return
	}
	candidate, err := hooks.NormalizeAndValidateHook(lifecycleHookFromWrite(request, &current))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if api.authorizeHook != nil && !api.authorizeHook(w, r, candidate) {
		return
	}
	updated, err := api.store.UpdateLifecycleHookCAS(r.Context(), id, request.ExpectedRevision, candidate)
	if err != nil {
		api.writeMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lifecycleHookResponseFromHook(updated))
}

func (api *LifecycleHooksAPI) deleteHook(w http.ResponseWriter, r *http.Request) {
	if !api.available(w) {
		return
	}
	revision, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("expectedRevision")), 10, 64)
	if err != nil || revision < 1 {
		writeError(w, http.StatusBadRequest, "expectedRevision query parameter must be positive")
		return
	}
	id := chi.URLParam(r, "hookID")
	current, err := api.store.GetLifecycleHook(r.Context(), id)
	if err != nil {
		api.writeStoreError(w, err)
		return
	}
	if api.authorizeHook != nil && !api.authorizeHook(w, r, current) {
		return
	}
	if err := api.store.DeleteLifecycleHookCAS(r.Context(), id, revision); err != nil {
		api.writeMutationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (api *LifecycleHooksAPI) history(w http.ResponseWriter, r *http.Request) {
	if !api.available(w) {
		return
	}
	hookID := chi.URLParam(r, "hookID")
	hook, err := api.store.GetLifecycleHook(r.Context(), hookID)
	if err != nil {
		api.writeStoreError(w, err)
		return
	}
	if api.authorizeHook != nil && !api.authorizeHook(w, r, hook) {
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	executions, err := api.store.ListLifecycleHookExecutions(r.Context(), hookID, limit)
	if err != nil {
		api.writeStoreError(w, err)
		return
	}
	items := make([]lifecycleHookHistoryItem, 0, len(executions))
	for _, execution := range executions {
		attempts, err := api.store.ListLifecycleHookAttempts(r.Context(), execution.ID)
		if err != nil {
			api.writeStoreError(w, err)
			return
		}
		items = append(items, lifecycleHookHistoryItem{Execution: execution, Attempts: attempts})
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": items})
}

func (api *LifecycleHooksAPI) testHook(w http.ResponseWriter, r *http.Request) {
	if !api.available(w) {
		return
	}
	var request lifecycleHookTestRequest
	if err := decodeLifecycleHookJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hook, err := api.store.GetLifecycleHook(r.Context(), chi.URLParam(r, "hookID"))
	if err != nil {
		api.writeStoreError(w, err)
		return
	}
	if api.authorizeHook != nil && !api.authorizeHook(w, r, hook) {
		return
	}
	if request.Event.Name == "" {
		request.Event.Name = hook.Event
	}
	if request.Event.Name != hook.Event {
		writeError(w, http.StatusBadRequest, "test event must match the hook event")
		return
	}
	request.Event.AgentID = strings.TrimSpace(request.Event.AgentID)
	if request.Event.AgentID == "" {
		writeError(w, http.StatusBadRequest, "test event agentId is required")
		return
	}
	agent, err := api.store.GetAgent(r.Context(), request.Event.AgentID)
	if err != nil {
		api.writeStoreError(w, err)
		return
	}
	if api.authorizeTestAgent != nil && !api.authorizeTestAgent(w, r, agent) {
		return
	}
	projectID := ""
	if strings.TrimSpace(agent.WorklineID) != "" {
		workline, err := api.store.GetWorkline(r.Context(), agent.WorklineID)
		if err != nil {
			api.writeStoreError(w, err)
			return
		}
		projectID = strings.TrimSpace(workline.ProjectID)
	}
	if supplied := strings.TrimSpace(request.Event.ProjectID); supplied != "" && supplied != projectID {
		writeError(w, http.StatusBadRequest, "test event projectId does not match the selected agent")
		return
	}
	switch hook.Scope.Kind {
	case hooks.ScopeProject:
		if projectID == "" || hook.Scope.ID != projectID {
			writeError(w, http.StatusForbidden, "selected agent is outside the lifecycle hook project scope")
			return
		}
	case hooks.ScopeAgent:
		if hook.Scope.ID != agent.ID {
			writeError(w, http.StatusForbidden, "selected agent is outside the lifecycle hook agent scope")
			return
		}
	}
	request.Event.AgentID = agent.ID
	request.Event.ProjectID = projectID
	request.Event.RunID = "hook-test-" + db.NewID()
	request.Event.RunKind = hooks.RunKindHookTest
	request.Event.ID = db.NewID()
	if request.Event.OccurredAt.IsZero() {
		request.Event.OccurredAt = api.now().UTC()
	}
	testHook := hook
	testHook.Enabled = true
	snapshot, err := hooks.NewSnapshot([]hooks.Hook{testHook}, api.now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	binding, err := api.store.CreateLifecycleHookRunBinding(r.Context(), request.Event.RunID, snapshot)
	if err != nil {
		api.writeMutationError(w, err)
		return
	}
	eventPayload, err := hooks.CanonicalEventJSON(request.Event)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	storedEvent, err := api.store.CreateLifecycleHookEvent(r.Context(), db.LifecycleHookEvent{BindingID: binding.ID, RunID: request.Event.RunID, Name: request.Event.Name, Payload: eventPayload})
	if err != nil {
		api.writeMutationError(w, err)
		return
	}
	execution, err := api.store.CreateLifecycleHookExecution(r.Context(), db.LifecycleHookExecution{EventID: storedEvent.ID, HookID: hook.ID, HookRevision: hook.Revision, Mode: hook.Mode, FailurePolicy: hook.FailurePolicy})
	if err != nil {
		api.writeMutationError(w, err)
		return
	}
	execution, err = api.store.TransitionLifecycleHookExecution(r.Context(), execution.ID, hooks.ExecutionRunning, nil, "")
	if err != nil {
		api.writeMutationError(w, err)
		return
	}
	attempt, err := api.store.CreateLifecycleHookAttempt(r.Context(), db.LifecycleHookAttempt{ExecutionID: execution.ID, AttemptNumber: 1, Request: eventPayload})
	if err != nil {
		api.writeMutationError(w, err)
		return
	}
	result, executeErr := api.executor.Execute(r.Context(), testHook, request.Event)
	resultJSON, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		executeErr = marshalErr
		resultJSON = []byte(`{}`)
	}
	if executeErr != nil {
		_, _ = api.store.CompleteLifecycleHookAttempt(r.Context(), attempt.ID, hooks.AttemptFailed, resultJSON, executeErr.Error())
		execution, _ = api.store.TransitionLifecycleHookExecution(r.Context(), execution.ID, hooks.ExecutionFailed, resultJSON, executeErr.Error())
		_, _ = api.store.UpdateLifecycleHookEventStatus(r.Context(), storedEvent.ID, hooks.EventFailed, executeErr.Error())
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": hooks.RedactText(executeErr.Error()), "execution": execution})
		return
	}
	_, _ = api.store.CompleteLifecycleHookAttempt(r.Context(), attempt.ID, hooks.AttemptSucceeded, resultJSON, "")
	execution, err = api.store.TransitionLifecycleHookExecution(r.Context(), execution.ID, hooks.ExecutionSucceeded, resultJSON, "")
	if err != nil {
		api.writeMutationError(w, err)
		return
	}
	_, _ = api.store.UpdateLifecycleHookEventStatus(r.Context(), storedEvent.ID, hooks.EventCompleted, "")
	writeJSON(w, http.StatusOK, map[string]any{"execution": execution, "result": result})
}

func (api *LifecycleHooksAPI) cancelExecution(w http.ResponseWriter, r *http.Request) {
	if !api.available(w) {
		return
	}
	if err := requireEmptyLifecycleBody(r); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	executionID := chi.URLParam(r, "executionID")
	if !api.authorizeExecution(w, r, executionID) {
		return
	}
	execution, err := api.store.CancelLifecycleHookExecution(r.Context(), executionID)
	if err != nil {
		api.writeMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, execution)
}

func (api *LifecycleHooksAPI) retryExecution(w http.ResponseWriter, r *http.Request) {
	if !api.available(w) {
		return
	}
	if err := requireEmptyLifecycleBody(r); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	executionID := chi.URLParam(r, "executionID")
	if !api.authorizeExecution(w, r, executionID) {
		return
	}
	execution, err := api.store.RetryLifecycleHookExecution(r.Context(), executionID)
	if err != nil {
		api.writeMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, execution)
}

func lifecycleHookFromWrite(request lifecycleHookWrite, current *hooks.Hook) hooks.Hook {
	enabled := true
	if current != nil {
		enabled = current.Enabled
	}
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	action := request.Action
	if current != nil && action.Kind == current.Action.Kind {
		if action.Shell != nil && current.Action.Shell != nil && action.Shell.SecretRefs == nil {
			action.Shell.SecretRefs = cloneLifecycleStrings(current.Action.Shell.SecretRefs)
		}
		if action.HTTP != nil && current.Action.HTTP != nil && action.HTTP.SecretRefs == nil {
			action.HTTP.SecretRefs = cloneLifecycleStrings(current.Action.HTTP.SecretRefs)
		}
	}
	return hooks.Hook{Name: request.Name, Description: request.Description, Enabled: enabled, Event: request.Event, Scope: request.Scope, Priority: request.Priority, Filter: request.Filter, Mode: request.Mode, FailurePolicy: request.FailurePolicy, Action: action}
}

func lifecycleHookResponseFromHook(hook hooks.Hook) lifecycleHookResponse {
	action := lifecycleHookActionResponse{Kind: hook.Action.Kind}
	if hook.Action.Shell != nil {
		action.Shell = struct {
			Executable       string            `json:"executable"`
			Args             []string          `json:"args,omitempty"`
			CWD              string            `json:"cwd,omitempty"`
			Env              map[string]string `json:"env,omitempty"`
			TimeoutSeconds   int               `json:"timeoutSeconds"`
			CanonicalStdinV1 bool              `json:"canonicalStdinV1"`
		}{hook.Action.Shell.Executable, append([]string(nil), hook.Action.Shell.Args...), hook.Action.Shell.CWD, cloneLifecycleStrings(hook.Action.Shell.Env), hook.Action.Shell.TimeoutSeconds, hook.Action.Shell.CanonicalStdinV1}
		action.SecretConfigured = configuredLifecycleSecrets(hook.Action.Shell.SecretRefs)
	}
	if hook.Action.HTTP != nil {
		action.HTTP = struct {
			URL            string            `json:"url"`
			Method         string            `json:"method"`
			Headers        map[string]string `json:"headers,omitempty"`
			TimeoutSeconds int               `json:"timeoutSeconds"`
		}{hook.Action.HTTP.URL, hook.Action.HTTP.Method, cloneLifecycleStrings(hook.Action.HTTP.Headers), hook.Action.HTTP.TimeoutSeconds}
		action.SecretConfigured = configuredLifecycleSecrets(hook.Action.HTTP.SecretRefs)
	}
	if hook.Action.LLM != nil {
		action.LLM = struct {
			Model           string `json:"model"`
			Prompt          string `json:"prompt"`
			MaxOutputTokens int    `json:"maxOutputTokens"`
			TimeoutSeconds  int    `json:"timeoutSeconds"`
		}{hook.Action.LLM.Model, hook.Action.LLM.Prompt, hook.Action.LLM.MaxOutputTokens, hook.Action.LLM.TimeoutSeconds}
	}
	return lifecycleHookResponse{ID: hook.ID, Name: hook.Name, Description: hook.Description, Enabled: hook.Enabled, Event: hook.Event, Scope: hook.Scope, Priority: hook.Priority, Filter: hook.Filter, Mode: hook.Mode, FailurePolicy: hook.FailurePolicy, Action: action, Revision: hook.Revision, CreatedAt: hook.CreatedAt, UpdatedAt: hook.UpdatedAt}
}

func decodeLifecycleHookJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	defer r.Body.Close()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, lifecycleHookRequestBytes))
	if err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			return errors.New("request body exceeds size limit")
		}
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return errors.New("request body is required")
	}
	if err := rejectDuplicateLifecycleJSONKeys(body); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
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

func rejectDuplicateLifecycleJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("JSON object key must be a string")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate JSON field %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("invalid JSON object")
			}
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("invalid JSON array")
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain exactly one JSON value")
		}
		return err
	}
	return nil
}

func requireEmptyLifecycleBody(r *http.Request) error {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1024))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) != 0 {
		return errors.New("request body must be empty")
	}
	return nil
}

func (api *LifecycleHooksAPI) writeMutationError(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrConflict) {
		writeError(w, http.StatusConflict, "lifecycle hook revision conflict")
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "lifecycle hook resource not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "lifecycle hook request failed")
}

func (api *LifecycleHooksAPI) writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "lifecycle hook resource not found")
		return
	}
	if errors.Is(err, db.ErrConflict) {
		writeError(w, http.StatusConflict, "lifecycle hook revision conflict")
		return
	}
	writeError(w, http.StatusInternalServerError, "lifecycle hook store request failed")
}

func configuredLifecycleSecrets(refs map[string]string) map[string]bool {
	if len(refs) == 0 {
		return nil
	}
	result := make(map[string]bool, len(refs))
	for name := range refs {
		result[name] = true
	}
	return result
}
func cloneLifecycleStrings(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
