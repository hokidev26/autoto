package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/imageassets"
	"autoto/internal/providers"
	"autoto/internal/spill"
	"autoto/internal/toolpipeline"
	"autoto/internal/tools"
)

type Runner struct {
	store               *db.Store
	providers           *providers.Registry
	tools               *tools.Registry
	toolOutputPipeline  tools.ToolOutputPipelineService
	hub                 *Hub
	cfg                 config.AgentConfig
	contextManagementMu sync.RWMutex
	contextManagement   config.ContextManagementConfig

	modelSettingsMu sync.RWMutex
	defaultModel    string
	summaryModel    string
	safetyModel     string
	subagentModels  map[string]string
	subagentPools   map[string][]string

	continuationMu        sync.RWMutex
	continuationConfig    config.AgentConfig
	continuationRunLimits map[string]continuationLimits

	// User-maintained retry ceiling and permanent-error patterns. Own mutex;
	// read on every provider failure and replaced wholesale when settings change.
	retry retryPolicy

	dynamic dynamicToolSource

	backgroundMu sync.RWMutex
	background   tools.BackgroundTaskService

	peerCollaborationMu sync.RWMutex
	peerCollaboration   tools.PeerCollaborationService

	generatedImagesMu sync.RWMutex
	generatedImages   *imageassets.Store

	spillMu    sync.RWMutex
	spillStore *spill.Store

	// Consecutive-repeat ladder and streak bookkeeping. Thresholds are
	// immutable after NewRunner; the live chains still live on runtimeState
	// because they share that mutex with run snapshots.
	repeatCalls repeatToolCallDetector

	reasoningMu            sync.RWMutex
	defaultReasoningEffort string

	runMu      sync.Mutex
	running    map[string]*activeRun
	compacting map[string]struct{}
	// Cancellation handles for compactions started between runs, guarded by
	// runMu like the compacting set. Manual compactions never register here,
	// which is what preserves their ErrAgentBusy semantics under preemption.
	backgroundCompactions map[string]*backgroundCompactionHandle

	// Latest measured estimate-vs-actual input-token pair per conversation,
	// used to calibrate the char-based context estimate. In-memory only.
	// Also owns the shared cost-table lookup used when recording API usage.
	usage usageAccounting

	titlingMu sync.Mutex
	titling   map[string]struct{}

	approvalMu    sync.Mutex
	approvals     map[string]*pendingApproval
	sessionGrants map[string]map[string]sessionGrant

	// Danger reflection verdicts, keyed by agent then action fingerprint, with
	// insertion order tracked so the cache stays bounded.
	reflections dangerReflectionCache

	userQuestionMu sync.Mutex
	userQuestions  map[string]*pendingUserQuestion

	notifierMu sync.RWMutex
	notifier   Notifier

	// Isolated reviewer plus snapshot callbacks used to bind plan and
	// background-task runs to control-plane state. Own mutex; not the run loop.
	plans planProviders

	runtimeStateOnce sync.Once
	runtimeState     *runtimeSnapshotState
}

type activeRun struct {
	cancel                  context.CancelFunc
	pending                 bool
	interrupted             bool
	runID                   string
	triggerMessageID        string
	pendingRunID            string
	pendingTriggerMessageID string
}

var (
	ErrAgentBusy                  = errors.New("agent is busy")
	ErrRemoteExecutionUnavailable = errors.New("remote execution transport is not implemented")
)

type NotificationEvent struct {
	Event               string
	TaskID              string
	RunID               string
	AgentID             string
	Status              string
	Error               string
	ToolUseID           string
	ToolName            string
	ExecutionGeneration int64
}

type Notifier interface {
	Notify(context.Context, NotificationEvent)
}

const (
	toolApprovalTimeout          = 10 * time.Minute
	maxToolResultPreviewBytes    = 4 * 1024
	maxToolEventInputBytes       = 16 * 1024
	maxToolEventInputStringBytes = 2 * 1024
	maxToolEventInputItems       = 32
	maxToolEventInputDepth       = 4
	defaultContextTokenLimit     = 120000
	contextKeepRecentMessages    = 8
	maxContextToolInputBytes     = 16 * 1024
	maxDeterministicSummary      = 8000
	maxSummaryModelBytes         = 32 * 1024
	maxSummaryLineRunes          = 240
	memoryInjectionLimit         = 5
	memoryContentMaxRunes        = 2000
)

func NewRunner(store *db.Store, providers *providers.Registry, toolRegistry *tools.Registry, hub *Hub, cfg config.AgentConfig) *Runner {
	runner := &Runner{store: store, providers: providers, tools: toolRegistry, toolOutputPipeline: toolpipeline.NewManager(), hub: hub, cfg: cfg, contextManagement: (config.ContextManagementConfig{}).Normalized(), continuationConfig: cfg, retry: retryPolicy{nonRetryableErrorPatterns: config.NormalizeNonRetryableErrorPatterns(cfg.NonRetryableErrorPatterns)}, repeatCalls: repeatToolCallDetector{thresholds: config.NormalizeRepeatToolCallThresholds(cfg.RepeatToolCallThresholds)}, continuationRunLimits: make(map[string]continuationLimits), defaultReasoningEffort: "auto", running: make(map[string]*activeRun), compacting: make(map[string]struct{}), titling: make(map[string]struct{}), approvals: make(map[string]*pendingApproval), sessionGrants: make(map[string]map[string]sessionGrant), userQuestions: make(map[string]*pendingUserQuestion)}
	runner.SetAgentModelSettings(cfg)
	if store != nil {
		if settings, err := store.GetRuntimeSettings(context.Background()); err == nil {
			runner.SetDefaultReasoningEffort(settings.DefaultReasoningEffort)
		}
	}
	return runner
}

// dynamicToolSource holds the optional dynamic listing and resolution
// surfaces. Its mutex is private so listing and resolution never share a
// lock with the run loop.
type dynamicToolSource struct {
	mu       sync.RWMutex
	source   tools.ToolSource
	resolver tools.Resolver
}

func (d *dynamicToolSource) set(source tools.ToolSource, resolver tools.Resolver) {
	d.mu.Lock()
	d.source = source
	d.resolver = resolver
	d.mu.Unlock()
}

func (d *dynamicToolSource) get() (tools.ToolSource, tools.Resolver) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.source, d.resolver
}

// SetDynamicTools configures the optional dynamic listing and resolution
// surfaces without changing the constructor used by existing callers.
func (r *Runner) SetDynamicTools(source tools.ToolSource, resolver tools.Resolver) {
	if r == nil {
		return
	}
	r.dynamic.set(source, resolver)
}

// SetDynamicToolSource is a convenience for services implementing both
// ToolSource and Resolver.
func (r *Runner) SetDynamicToolSource(source tools.ToolSource) {
	resolver, _ := source.(tools.Resolver)
	r.SetDynamicTools(source, resolver)
}

// SetToolSource is a compatibility alias for SetDynamicToolSource.
func (r *Runner) SetToolSource(source tools.ToolSource) {
	r.SetDynamicToolSource(source)
}

func (r *Runner) dynamicTools() (tools.ToolSource, tools.Resolver) {
	if r == nil {
		return nil, nil
	}
	return r.dynamic.get()
}

// SetBackgroundTaskService installs the execution service used by background
// tools. App wiring may call this alongside the server's own setter.
func (r *Runner) SetBackgroundTaskService(service tools.BackgroundTaskService) {
	if r == nil {
		return
	}
	r.backgroundMu.Lock()
	r.background = service
	r.backgroundMu.Unlock()
}

func (r *Runner) backgroundTaskService() tools.BackgroundTaskService {
	if r == nil {
		return nil
	}
	r.backgroundMu.RLock()
	defer r.backgroundMu.RUnlock()
	return r.background
}

// SetPeerCollaborationService installs the bridge the Peer* tools use to reach
// paired remote Autoto instances. App wiring points this at the server, which
// owns the authenticated peer clients.
func (r *Runner) SetPeerCollaborationService(service tools.PeerCollaborationService) {
	if r == nil {
		return
	}
	r.peerCollaborationMu.Lock()
	r.peerCollaboration = service
	r.peerCollaborationMu.Unlock()
}

func (r *Runner) peerCollaborationService() tools.PeerCollaborationService {
	if r == nil {
		return nil
	}
	r.peerCollaborationMu.RLock()
	defer r.peerCollaborationMu.RUnlock()
	return r.peerCollaboration
}

// SetGeneratedImageStore installs the optional disk store used for generated
// image persistence and history hydration without changing NewRunner callers.
func (r *Runner) SetGeneratedImageStore(store *imageassets.Store) {
	if r == nil {
		return
	}
	r.generatedImagesMu.Lock()
	r.generatedImages = store
	r.generatedImagesMu.Unlock()
}

func (r *Runner) generatedImageStore() *imageassets.Store {
	if r == nil {
		return nil
	}
	r.generatedImagesMu.RLock()
	defer r.generatedImagesMu.RUnlock()
	return r.generatedImages
}

func (r *Runner) SetAgentModelSettings(cfg config.AgentConfig) {
	if r == nil {
		return
	}
	models := make(map[string]string, len(cfg.SubagentModels))
	for role, model := range cfg.SubagentModels {
		role = normalizeSubagentRole(role)
		model = strings.TrimSpace(model)
		if role != "" && model != "" {
			models[role] = model
		}
	}
	pools := make(map[string][]string, len(cfg.SubagentModelPools))
	for role, values := range cfg.SubagentModelPools {
		role = normalizeSubagentRole(role)
		if role == "" {
			continue
		}
		seen := make(map[string]struct{}, len(values))
		pool := make([]string, 0, len(values))
		for _, model := range values {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			if _, exists := seen[model]; exists {
				continue
			}
			seen[model] = struct{}{}
			pool = append(pool, model)
		}
		if len(pool) > 0 {
			pools[role] = pool
		}
	}
	r.modelSettingsMu.Lock()
	r.defaultModel = strings.TrimSpace(cfg.DefaultModel)
	r.summaryModel = strings.TrimSpace(cfg.SummaryModel)
	r.safetyModel = strings.TrimSpace(cfg.SafetyModel)
	r.subagentModels = models
	r.subagentPools = pools
	r.modelSettingsMu.Unlock()
}

func (r *Runner) SummaryModel() string {
	if r == nil {
		return ""
	}
	r.modelSettingsMu.RLock()
	model := r.summaryModel
	r.modelSettingsMu.RUnlock()
	return model
}

// SafetyModel is retained for configuration compatibility. Danger reflection
// uses the active conversation's model instead; this accessor is no longer used
// to select the reflection model.
func (r *Runner) SafetyModel() string {
	if r == nil {
		return ""
	}
	r.modelSettingsMu.RLock()
	model := r.safetyModel
	if model == "" {
		model = r.summaryModel
	}
	r.modelSettingsMu.RUnlock()
	return model
}

func (r *Runner) ResolveSubagentModel(role, explicitModel, parentModel string) (string, string, error) {
	role = normalizeSubagentRole(role)
	if role == "" {
		role = "general"
	}
	explicitModel = strings.TrimSpace(explicitModel)
	parentModel = strings.TrimSpace(parentModel)
	r.modelSettingsMu.RLock()
	preferred := strings.TrimSpace(r.subagentModels[role])
	pool := append([]string(nil), r.subagentPools[role]...)
	defaultModel := strings.TrimSpace(r.defaultModel)
	r.modelSettingsMu.RUnlock()
	allowed := func(model string) bool {
		if len(pool) == 0 {
			return true
		}
		for _, candidate := range pool {
			if candidate == model {
				return true
			}
		}
		return false
	}
	if explicitModel != "" {
		if !allowed(explicitModel) {
			return "", role, fmt.Errorf("model %s is not allowed for %s subagents", explicitModel, role)
		}
		if err := r.ValidateSubagentModel(explicitModel); err != nil {
			// The caller is usually a parent model that guessed a model name it
			// cannot see; telling it how to recover lets it retry instead of
			// bouncing the failure to the user.
			if parentModel != "" {
				return "", role, fmt.Errorf("%w; omit the model field to inherit the parent model %q", err, parentModel)
			}
			return "", role, fmt.Errorf("%w; omit the model field to use the default model", err)
		}
		return explicitModel, role, nil
	}
	for _, candidate := range []string{preferred, parentModel, defaultModel} {
		if candidate != "" && allowed(candidate) {
			return candidate, role, nil
		}
	}
	if len(pool) > 0 {
		return pool[0], role, nil
	}
	return "", role, errors.New("subagent model is not configured")
}

// ValidateSubagentModel checks an explicitly requested provider:model without
// replacing it with a fallback. Providers that cannot expose a model catalog
// keep their existing provider-level validation semantics.
func (r *Runner) ValidateSubagentModel(model string) error {
	model = strings.TrimSpace(model)
	if model == "" || r == nil || r.providers == nil {
		return nil
	}
	provider, modelName, err := r.providers.Resolve(model)
	if err != nil {
		return fmt.Errorf("explicit subagent model %q is unavailable: %w", model, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	models, err := provider.ListModels(ctx)
	if err != nil && errors.Is(err, providers.ErrProviderUnavailable) {
		// The provider itself cannot run, which is not a catalog problem and will
		// not resolve by trying. Deferring here spent a whole child Run to discover
		// something knowable now: the child passed preflight, started, and died on
		// the first model call.
		return fmt.Errorf("explicit subagent model %q is unavailable: %w", model, err)
	}
	if err != nil || len(models) == 0 {
		// Some otherwise runnable providers cannot expose a model catalog (for
		// example, discovery may require a separate credential or endpoint). Keep
		// their existing provider-level execution validation instead of turning a
		// transient catalog failure into a permanent subagent rejection.
		return nil
	}
	for _, candidate := range models {
		if strings.TrimSpace(candidate) == modelName {
			return nil
		}
	}
	return fmt.Errorf("explicit subagent model %q is not available", model)
}

func normalizeSubagentRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", "background", "general", "general-purpose":
		return "general"
	case "explore", "plan", "search":
		return strings.ToLower(strings.TrimSpace(role))
	default:
		return ""
	}
}

func (r *Runner) SetDefaultReasoningEffort(effort string) {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" {
		effort = "auto"
	}
	r.reasoningMu.Lock()
	r.defaultReasoningEffort = effort
	r.reasoningMu.Unlock()
}

func (r *Runner) reasoningEffort(agentEffort string) string {
	if effort := strings.ToLower(strings.TrimSpace(agentEffort)); effort != "" {
		return effort
	}
	r.reasoningMu.RLock()
	effort := r.defaultReasoningEffort
	r.reasoningMu.RUnlock()
	if effort == "" {
		return "auto"
	}
	return effort
}

func (r *Runner) EnsureLocalExecution(ctx context.Context, agentID string) error {
	if r == nil || r.store == nil {
		return errors.New("agent runner is not initialized")
	}
	agent, err := r.store.GetAgent(ctx, strings.TrimSpace(agentID))
	if err != nil {
		return err
	}
	if deviceID := strings.TrimSpace(agent.ExecutionDeviceID); deviceID != "" && deviceID != "local" {
		return fmt.Errorf("%w: agent %s targets device %s", ErrRemoteExecutionUnavailable, agent.ID, deviceID)
	}
	return nil
}

func (r *Runner) SetNotifier(notifier Notifier) {
	r.notifierMu.Lock()
	defer r.notifierMu.Unlock()
	r.notifier = notifier
}

func (r *Runner) notify(event NotificationEvent) {
	r.notifierMu.RLock()
	notifier := r.notifier
	r.notifierMu.RUnlock()
	if notifier == nil {
		return
	}
	if event.ExecutionGeneration == 0 && strings.TrimSpace(event.RunID) != "" && r.store != nil {
		if run, err := r.store.GetRunByID(context.Background(), event.RunID); err == nil {
			event.ExecutionGeneration = run.ExecutionGeneration
		}
	}
	notifier.Notify(context.Background(), event)
}
