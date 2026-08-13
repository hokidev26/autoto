package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	agentpkg "autoto/internal/agent"
	"autoto/internal/db"
	"autoto/internal/providers"
)

type modelRuntimeService struct {
	store        *db.Store
	runner       *agentpkg.Runner
	lockMutation func(string) func()
	capabilities func(string) providers.Capabilities
	fastMode     func(string) bool
}

func (s *Server) modelRuntime() modelRuntimeService {
	var store *db.Store
	var runner *agentpkg.Runner
	var lockMutation func(string) func()
	var capabilities func(string) providers.Capabilities
	var fastMode func(string) bool
	if s != nil {
		store = s.store
		runner = s.runner
		lockMutation = s.lockAgentMutation
		capabilities = s.capabilitiesForAgentModel
		fastMode = func(model string) bool { return s.modelCapabilitiesForAgentModel(model).FastMode }
	}
	return modelRuntimeService{store: store, runner: runner, lockMutation: lockMutation, capabilities: capabilities, fastMode: fastMode}
}

func (m modelRuntimeService) listAggregates(ctx context.Context) ([]db.ModelAggregate, error) {
	if m.store == nil {
		return nil, apiErr(http.StatusInternalServerError, "model aggregate store is unavailable")
	}
	aggregates, err := m.store.ListModelAggregates(ctx)
	if err != nil {
		return nil, err
	}
	if aggregates == nil {
		aggregates = []db.ModelAggregate{}
	}
	return aggregates, nil
}

func (m modelRuntimeService) getAggregate(ctx context.Context, name string) (db.ModelAggregate, error) {
	if m.store == nil {
		return db.ModelAggregate{}, apiErr(http.StatusInternalServerError, "model aggregate store is unavailable")
	}
	return m.store.GetModelAggregate(ctx, name)
}

func (m modelRuntimeService) putAggregate(ctx context.Context, name string, request modelAggregatePutRequest) (db.ModelAggregate, error) {
	revision, err := requestRevision(request.Revision, request.ExpectedRevision, true)
	if err != nil {
		return db.ModelAggregate{}, apiErr(http.StatusBadRequest, err.Error())
	}
	if !request.Members.set {
		return db.ModelAggregate{}, apiErr(http.StatusBadRequest, "members are required")
	}
	mode := providers.AggregateStrategyPriority
	if request.Mode.set {
		mode = strings.TrimSpace(request.Mode.value)
		if mode != providers.AggregateStrategyPriority {
			return db.ModelAggregate{}, apiErr(http.StatusBadRequest, "mode must be priority")
		}
	}
	members, err := validateModelAggregateMembers(request.Members.values)
	if err != nil {
		return db.ModelAggregate{}, apiErr(http.StatusBadRequest, err.Error())
	}
	if m.store == nil {
		return db.ModelAggregate{}, apiErr(http.StatusInternalServerError, "model aggregate store is unavailable")
	}
	return m.store.UpsertModelAggregate(ctx, db.ModelAggregate{Name: name, Mode: mode, Members: members}, revision)
}

func (m modelRuntimeService) deleteAggregate(ctx context.Context, name string, request revisionCASRequest) (int64, error) {
	revision, err := requestRevision(request.Revision, request.ExpectedRevision, false)
	if err != nil {
		return 0, apiErr(http.StatusBadRequest, err.Error())
	}
	if m.store == nil {
		return 0, apiErr(http.StatusInternalServerError, "model aggregate store is unavailable")
	}
	if err := m.store.DeleteModelAggregate(ctx, name, revision); err != nil {
		return 0, err
	}
	return revision, nil
}

func (m modelRuntimeService) updateRuntimeSettings(ctx context.Context, request runtimeModelSettingsRequest) (db.RuntimeSettings, error) {
	revision, err := requestRevision(request.Revision, request.ExpectedRevision, false)
	if err != nil {
		return db.RuntimeSettings{}, apiErr(http.StatusBadRequest, err.Error())
	}
	if !request.DefaultReasoningEffort.set && !request.SubscriptionTier.set && !request.AccountEmail.set {
		return db.RuntimeSettings{}, apiErr(http.StatusBadRequest, "at least one runtime model setting is required")
	}
	patch := db.RuntimeSettingsPatch{ExpectedRevision: revision}
	if request.DefaultReasoningEffort.set {
		effort := strings.ToLower(strings.TrimSpace(request.DefaultReasoningEffort.value))
		if !validDefaultReasoningEffort(effort) {
			return db.RuntimeSettings{}, apiErr(http.StatusBadRequest, "defaultReasoningEffort must be auto, low, medium, high, xhigh, max, or ultra")
		}
		patch.DefaultReasoningEffort = &effort
	}
	if request.SubscriptionTier.set {
		tier := strings.ToLower(strings.TrimSpace(request.SubscriptionTier.value))
		if !validSubscriptionTier(tier) {
			return db.RuntimeSettings{}, apiErr(http.StatusBadRequest, "invalid subscriptionTier")
		}
		patch.SubscriptionTier = &tier
	}
	if request.AccountEmail.set {
		email := strings.TrimSpace(request.AccountEmail.value)
		if err := validateRuntimeAccountEmail(email); err != nil {
			return db.RuntimeSettings{}, apiErr(http.StatusBadRequest, err.Error())
		}
		patch.AccountEmail = &email
	}
	if m.store == nil {
		return db.RuntimeSettings{}, apiErr(http.StatusInternalServerError, "runtime settings store is unavailable")
	}
	settings, err := m.store.UpdateRuntimeSettings(ctx, patch)
	if err != nil {
		return db.RuntimeSettings{}, err
	}
	if m.runner != nil {
		m.runner.SetDefaultReasoningEffort(settings.DefaultReasoningEffort)
	}
	return settings, nil
}

func (m modelRuntimeService) updateReasoningEffort(ctx context.Context, agentID string, request agentReasoningRequest) (db.Agent, error) {
	if !request.ReasoningEffort.set {
		return db.Agent{}, apiErr(http.StatusBadRequest, "reasoningEffort is required")
	}
	effort := strings.ToLower(strings.TrimSpace(request.ReasoningEffort.value))
	if !validAgentReasoningEffort(effort, true) {
		return db.Agent{}, apiErr(http.StatusBadRequest, "reasoningEffort must be empty, auto, low, medium, high, xhigh, max, or ultra")
	}
	if agentID == "" || len(agentID) > 128 {
		return db.Agent{}, apiErr(http.StatusBadRequest, "invalid agent id")
	}
	if m.store == nil {
		return db.Agent{}, apiErr(http.StatusInternalServerError, "agent store is unavailable")
	}
	if m.lockMutation != nil {
		unlock := m.lockMutation(agentID)
		defer unlock()
	}
	current, err := m.store.GetAgent(ctx, agentID)
	if err != nil {
		return db.Agent{}, err
	}
	if request.Model.set && strings.TrimSpace(request.Model.value) != current.Model {
		return db.Agent{}, fmt.Errorf("%w: agent model changed", db.ErrConflict)
	}
	if request.EntityGeneration.set && request.EntityGeneration.value != current.EntityGeneration {
		return db.Agent{}, fmt.Errorf("%w: agent settings changed", db.ErrConflict)
	}
	capabilities := providers.Capabilities{}
	if m.capabilities != nil {
		capabilities = m.capabilities(current.Model)
	}
	if effort == "" {
		effort = agentService{store: m.store}.safeReasoningEffort(ctx, effort, capabilities)
	} else if !capabilities.SupportsReasoningEffort(effort) {
		return db.Agent{}, apiErr(http.StatusBadRequest, "reasoningEffort is not supported by the current model")
	}
	return m.store.UpdateAgentReasoningEffort(ctx, agentID, effort)
}

func (m modelRuntimeService) updateFastMode(ctx context.Context, agentID string, request agentFastModeRequest) (db.Agent, error) {
	if !request.FastMode.set {
		return db.Agent{}, apiErr(http.StatusBadRequest, "fastMode is required")
	}
	if agentID == "" || len(agentID) > 128 {
		return db.Agent{}, apiErr(http.StatusBadRequest, "invalid agent id")
	}
	if m.store == nil {
		return db.Agent{}, apiErr(http.StatusInternalServerError, "agent store is unavailable")
	}
	if m.lockMutation != nil {
		unlock := m.lockMutation(agentID)
		defer unlock()
	}
	current, err := m.store.GetAgent(ctx, agentID)
	if err != nil {
		return db.Agent{}, err
	}
	if request.Model.set && strings.TrimSpace(request.Model.value) != current.Model {
		return db.Agent{}, fmt.Errorf("%w: agent model changed", db.ErrConflict)
	}
	if request.EntityGeneration.set && request.EntityGeneration.value != current.EntityGeneration {
		return db.Agent{}, fmt.Errorf("%w: agent settings changed", db.ErrConflict)
	}
	if request.FastMode.value && m.fastMode != nil && !m.fastMode(current.Model) {
		return db.Agent{}, apiErr(http.StatusBadRequest, "fastMode is not supported by the current model")
	}
	return m.store.UpdateAgentFastMode(ctx, agentID, request.FastMode.value)
}

func (m modelRuntimeService) clientIdentity(ctx context.Context) (db.RuntimeSettings, error) {
	if m.store == nil {
		return db.RuntimeSettings{}, apiErr(http.StatusInternalServerError, "runtime settings store is unavailable")
	}
	return m.store.GetRuntimeSettings(ctx)
}

func (m modelRuntimeService) rotateClientIdentity(ctx context.Context, request revisionCASRequest) (db.RuntimeSettings, error) {
	revision, err := requestRevision(request.Revision, request.ExpectedRevision, false)
	if err != nil {
		return db.RuntimeSettings{}, apiErr(http.StatusBadRequest, err.Error())
	}
	if m.store == nil {
		return db.RuntimeSettings{}, apiErr(http.StatusInternalServerError, "runtime settings store is unavailable")
	}
	return m.store.RotateInstallationID(ctx, revision)
}
