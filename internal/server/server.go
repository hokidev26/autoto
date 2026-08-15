package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	agentpkg "autoto/internal/agent"
	"autoto/internal/anthropicauth"
	"autoto/internal/appearanceassets"
	"autoto/internal/audit"
	"autoto/internal/automation"
	"autoto/internal/codexauth"
	"autoto/internal/compat"
	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/devices"
	"autoto/internal/imageassets"
	"autoto/internal/integrations"
	"autoto/internal/preview"
	"autoto/internal/providers"
	"autoto/internal/review"
	"autoto/internal/secrets"
	"autoto/internal/sysmetrics"
	"autoto/internal/themes"
	"autoto/internal/tools"
)

type redactingLogFormatter struct {
	delegate middleware.LogFormatter
}

func (f *redactingLogFormatter) NewLogEntry(r *http.Request) middleware.LogEntry {
	if f == nil || f.delegate == nil || r == nil || r.URL == nil {
		return (&middleware.DefaultLogFormatter{Logger: log.New(os.Stdout, "", log.LstdFlags), NoColor: true}).NewLogEntry(r)
	}
	query := r.URL.Query()
	redacted := false
	if _, present := query[localTokenQuery]; present {
		query.Set(localTokenQuery, "[REDACTED]")
		redacted = true
	}
	if r.URL.Path == oauthAppCallbackPath && r.URL.RawQuery != "" {
		for key := range query {
			query.Set(key, "[REDACTED]")
		}
		if len(query) == 0 {
			query.Set("callback", "[REDACTED]")
		}
		redacted = true
	}
	if !redacted {
		return f.delegate.NewLogEntry(r)
	}
	clone := r.Clone(r.Context())
	urlCopy := *r.URL
	urlCopy.RawQuery = query.Encode()
	clone.URL = &urlCopy
	clone.RequestURI = urlCopy.RequestURI()
	return f.delegate.NewLogEntry(clone)
}

var defaultRequestLogFormatter = &redactingLogFormatter{
	delegate: &middleware.DefaultLogFormatter{Logger: log.New(os.Stdout, "", log.LstdFlags), NoColor: true},
}

type agentMutationLock struct {
	mu   sync.Mutex
	refs int
}

type Server struct {
	cfg   config.Config
	cfgMu sync.RWMutex
	// configMutationMu serializes every configuration read-modify-save-publish
	// transaction. It is always acquired before any narrower runtime lock.
	configMutationMu sync.Mutex
	// providerMutationMu serializes provider runtime registry changes.
	providerMutationMu   sync.Mutex
	providerMutationHook func()
	gatewayRuntimeMu     sync.RWMutex
	gatewayRuntime       GatewayRuntimeController
	configPath           string
	startedAt            time.Time
	clock                func() time.Time
	setupStatusMu        sync.Mutex
	setupStatusCache     setupStatusResponse
	setupStatusCacheAt   time.Time
	setupInstallMu       sync.Mutex
	setupInstallJobs     map[string]*setupInstallJob
	// setupProbeFactory and setupInstallRunner exist for tests; nil means the
	// real probe and a real package-manager subprocess.
	setupProbeFactory  func() setupProbe
	setupInstallRunner func(context.Context, string, ...string) (string, error)
	localToken         string
	// remoteAccessToken remains only for source compatibility with older
	// in-package callers; it is never accepted as remote authentication.
	remoteAccessToken           string
	remoteAccessSessions        map[string]remoteAccessSession
	remoteAccessConnections     map[string]map[uint64]context.CancelFunc
	remoteAccessConnectionSeq   uint64
	remoteAccessFailure         map[string]remoteAccessFailure
	remoteAccessMu              sync.Mutex
	authSessionConnections      map[string]map[uint64]context.CancelFunc
	authSessionConnectionSeq    uint64
	authSessionMu               sync.Mutex
	authLoginFailures           map[string]authLoginFailure
	authLoginMu                 sync.Mutex
	agentMutationLocksMu        sync.Mutex
	agentMutationLocks          map[string]*agentMutationLock
	projectConversationMu       sync.Mutex
	projectConversationKeys     map[string]projectConversationResult
	legacyWarnings              *compat.Registry
	store                       *db.Store
	runner                      *agentpkg.Runner
	queueDrainer                messageQueueDrainer
	notifierMu                  sync.RWMutex
	nextNotifier                agentpkg.Notifier
	hub                         *agentpkg.Hub
	providers                   *providers.Registry
	providerVault               *secrets.ProviderVault
	codexCredentials            *codexauth.Store
	codexCredentialsMu          sync.Mutex
	codexOAuthMu                sync.Mutex
	codexOAuthLogin             *codexOAuthLoginSession
	codexOAuthTestConfig        *codexOAuthLoginTestConfig
	anthropicCredentials        *anthropicauth.Store
	anthropicCredentialsMu      sync.Mutex
	anthropicOAuthMu            sync.Mutex
	anthropicOAuthLogins        map[string]*anthropicOAuthLoginSession
	subscriptionOAuthMu         sync.Mutex
	subscriptionOAuthLogins     map[string]*subscriptionOAuthLoginSession
	geminiOAuthClientFactory    func() geminiOAuthLoginClient
	grokOAuthClientFactory      func() grokOAuthLoginClient
	kimiOAuthClientFactory      func() kimiOAuthLoginClient
	kiroOAuthClientFactory      func() kiroOAuthLoginClient
	subscriptionOAuthTestConfig *subscriptionOAuthLoginTestConfig
	toolRegistry                *tools.Registry
	toolRegistryMu              sync.RWMutex
	backgroundTasks             tools.BackgroundTaskService
	backgroundRuntime           tools.BackgroundRuntimeController
	automationToolCatalogMu     sync.RWMutex
	automationToolCatalog       *AutomationToolCatalog
	previewManager              *preview.Manager
	temporaryTunnel             *TemporaryTunnelManager
	apiTunnel                   *TemporaryTunnelManager
	peerControl                 *remoteCollaborationRuntime
	notifier                    *WebhookNotifier
	automation                  *automation.Manager
	connections                 *integrations.ConnectionService
	plugins                     PluginService
	themeStore                  *themes.Store
	appearanceAssets            *appearanceassets.Store
	generatedImages             *imageassets.Store
	reviewer                    *review.Service
	audit                       audit.Recorder
	integrationClient           *http.Client
	deviceAdapterFactory        func(context.Context, string) (devices.Adapter, error)
	oauthAppMu                  sync.Mutex
	oauthApp                    *oauthAppRuntime
	// shellDialogHost is optional; only the desktop entrypoint registers it.
	// Browser/CLI processes leave it nil so /api/desktop/dialog/* returns 404.
	shellDialogMu      sync.RWMutex
	shellDialogHost    ShellDialogHost
	shellLifecycleHost ShellLifecycleHost
	shellUpdateHost    ShellUpdateHost
	storageCache       storageSummaryCache
	// Held for the process lifetime: CPU and network utilisation are deltas
	// between consecutive readings, so the previous reading has to outlive the
	// request that took it.
	sysMetrics *sysmetrics.Sampler
}

func New(cfg config.Config, store *db.Store, runner *agentpkg.Runner, hub *agentpkg.Hub, providerRegistries ...*providers.Registry) *Server {
	var providerRegistry *providers.Registry
	if len(providerRegistries) > 0 {
		providerRegistry = providerRegistries[0]
	}
	server := &Server{
		cfg:                     cfg,
		startedAt:               time.Now().UTC(),
		clock:                   time.Now,
		localToken:              resolveLocalToken(cfg.Paths.HomeDir),
		remoteAccessToken:       newLocalToken(),
		remoteAccessSessions:    make(map[string]remoteAccessSession),
		remoteAccessConnections: make(map[string]map[uint64]context.CancelFunc),
		remoteAccessFailure:     make(map[string]remoteAccessFailure),
		authSessionConnections:  make(map[string]map[uint64]context.CancelFunc),
		authLoginFailures:       make(map[string]authLoginFailure),
		agentMutationLocks:      make(map[string]*agentMutationLock),
		projectConversationKeys: make(map[string]projectConversationResult),
		sysMetrics:              sysmetrics.NewSampler(),
		legacyWarnings: compat.NewRegistry(func(usage compat.Usage) {
			slog.Warn(
				"legacy compatibility used",
				"legacy", usage.Legacy,
				"replacement", usage.Replacement,
				"removalVersion", compat.RemovalVersion,
			)
		}),
		store:                   store,
		runner:                  runner,
		hub:                     hub,
		providers:               providerRegistry,
		codexCredentials:        codexauth.NewStore(codexauth.DefaultStoreDir(cfg.Paths.HomeDir)),
		anthropicCredentials:    anthropicauth.NewStore(anthropicauth.DefaultStoreDir(cfg.Paths.HomeDir)),
		subscriptionOAuthLogins: make(map[string]*subscriptionOAuthLoginSession),
		toolRegistry:            newCoreToolRegistry(),
	}
	if cfg.Paths.HomeDir != "" {
		if themeStore, err := themes.NewStore(cfg.Paths.HomeDir); err != nil {
			slog.Warn("initialize theme store", "error", err)
		} else {
			server.themeStore = themeStore
		}
		if appearanceStore, err := appearanceassets.New(cfg.Paths.HomeDir); err != nil {
			slog.Warn("initialize appearance background store", "error", err)
		} else {
			server.appearanceAssets = appearanceStore
		}
	}
	server.SetReviewService(NewReviewService(providerRegistry, cfg.Agent.ReviewModel))
	if runner != nil {
		runner.SetPlanSnapshotProvider(server.currentPlanSnapshot)
		runner.SetBackgroundTaskSnapshotProvider(server.currentBackgroundTaskSnapshot)
		runner.SetContextManagementConfig(cfg.ContextManagement)
	}
	return server
}

// lockAgentMutation serializes model and reasoning mutations for one agent.
// The lock entry is reference-counted so independent agents remain concurrent
// and completed agents do not accumulate entries indefinitely.
func (s *Server) lockAgentMutation(agentID string) func() {
	s.agentMutationLocksMu.Lock()
	if s.agentMutationLocks == nil {
		s.agentMutationLocks = make(map[string]*agentMutationLock)
	}
	lock := s.agentMutationLocks[agentID]
	if lock == nil {
		lock = &agentMutationLock{}
		s.agentMutationLocks[agentID] = lock
	}
	lock.refs++
	s.agentMutationLocksMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		s.agentMutationLocksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(s.agentMutationLocks, agentID)
		}
		s.agentMutationLocksMu.Unlock()
	}
}

func newCoreToolRegistry() *tools.Registry {
	registry := tools.NewRegistry()
	tools.RegisterCore(registry)
	return registry
}

func (s *Server) SetToolRegistry(registry *tools.Registry) {
	if registry == nil {
		registry = newCoreToolRegistry()
	}
	s.toolRegistryMu.Lock()
	defer s.toolRegistryMu.Unlock()
	s.toolRegistry = registry
}

func (s *Server) SetBackgroundTaskService(service tools.BackgroundTaskService) {
	s.backgroundTasks = service
}

func (s *Server) SetBackgroundRuntimeController(controller tools.BackgroundRuntimeController) {
	s.backgroundRuntime = controller
}

func (s *Server) SetAutomationToolCatalog(catalog *AutomationToolCatalog) {
	if s == nil {
		return
	}
	s.automationToolCatalogMu.Lock()
	s.automationToolCatalog = catalog
	s.automationToolCatalogMu.Unlock()
}

func (s *Server) automationToolCatalogSnapshot() *AutomationToolCatalog {
	if s == nil {
		return nil
	}
	s.automationToolCatalogMu.RLock()
	catalog := s.automationToolCatalog
	s.automationToolCatalogMu.RUnlock()
	return catalog
}

func (s *Server) toolRegistrySnapshot() *tools.Registry {
	s.toolRegistryMu.RLock()
	registry := s.toolRegistry
	s.toolRegistryMu.RUnlock()
	if registry != nil {
		return registry
	}

	registry = newCoreToolRegistry()
	s.toolRegistryMu.Lock()
	if s.toolRegistry == nil {
		s.toolRegistry = registry
	} else {
		registry = s.toolRegistry
	}
	s.toolRegistryMu.Unlock()
	return registry
}

func (s *Server) SetConfigPath(path string) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	s.configPath = path
}

func (s *Server) SetGatewayRuntimeController(controller GatewayRuntimeController) {
	if s == nil {
		return
	}
	s.gatewayRuntimeMu.Lock()
	s.gatewayRuntime = controller
	s.gatewayRuntimeMu.Unlock()
}

// ConfigSnapshot returns the currently published persistent configuration.
func (s *Server) ConfigSnapshot() config.Config {
	if s == nil {
		return config.Config{}
	}
	return s.configSnapshot()
}

func (s *Server) gatewayRuntimeController() GatewayRuntimeController {
	if s == nil {
		return nil
	}
	s.gatewayRuntimeMu.RLock()
	controller := s.gatewayRuntime
	s.gatewayRuntimeMu.RUnlock()
	return controller
}

func (s *Server) SetProviderVault(vault *secrets.ProviderVault) {
	s.providerVault = vault
}

func (s *Server) SetWebhookNotifier(notifier *WebhookNotifier) {
	s.notifier = notifier
}

func (s *Server) SetAutomationManager(manager *automation.Manager) {
	s.automation = manager
}

func (s *Server) SetConnectionService(service *integrations.ConnectionService) {
	s.connections = service
}

func (s *Server) SetPluginService(service PluginService) {
	s.plugins = service
}

func (s *Server) SetThemeStore(store *themes.Store) {
	s.themeStore = store
}

func (s *Server) SetAppearanceAssetStore(store *appearanceassets.Store) {
	s.appearanceAssets = store
}

func (s *Server) SetGeneratedImageStore(store *imageassets.Store) {
	s.generatedImages = store
}

// NewReviewService constructs the isolated, tool-free reviewer used by plan
// runs. It deliberately receives only the provider registry and review model.
func NewReviewService(registry *providers.Registry, model string) *review.Service {
	return review.NewService(registry, model)
}

// SetReviewService registers the reviewer with both the Server summary surface
// and the Runner. The Runner keeps plan persistence behind its PlanStore API.
func (s *Server) SetReviewService(service *review.Service) {
	if service == nil {
		service = NewReviewService(s.providers, s.configSnapshot().Agent.ReviewModel)
	}
	s.reviewer = service
	if s.runner != nil {
		s.runner.SetReviewService(service)
	}
}

func (s *Server) SetAuditRecorder(recorder audit.Recorder) {
	s.audit = recorder
}

func (s *Server) SetIntegrationHTTPClient(client *http.Client) {
	s.integrationClient = client
}

func (s *Server) SetDeviceAdapterFactory(factory func(context.Context, string) (devices.Adapter, error)) {
	s.deviceAdapterFactory = factory
}

func (s *Server) SetPreviewManager(manager *preview.Manager) {
	s.previewManager = manager
}

func (s *Server) SetTemporaryTunnelManager(manager *TemporaryTunnelManager) {
	s.temporaryTunnel = manager
}

// SetAPITunnelManager installs the tunnel that exposes the shared API gateway.
// It is separate from the tunnel above so the public API URL and the management
// UI never share a hostname.
func (s *Server) SetAPITunnelManager(manager *TemporaryTunnelManager) {
	s.apiTunnel = manager
}

func (s *Server) warnLegacy(key, legacy, replacement, kind string) {
	if s.legacyWarnings == nil {
		return
	}
	s.legacyWarnings.Warn(compat.Usage{
		Key:         key,
		Legacy:      legacy,
		Replacement: replacement,
		Kind:        kind,
	})
}

func (s *Server) configSnapshot() config.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequestLogger(defaultRequestLogFormatter))
	r.Use(middleware.Recoverer)
	r.Use(s.localRequestGuard)
	r.Use(s.projectAccessGuard)
	s.mountUI(r)
	s.mountOAuthApp(r)
	s.mountThemeRoutes(r)
	s.mountAppearanceAssetRoutes(r)
	s.mountOptionalToolsRoutes(r)
	s.mountAutomationToolCatalogRoutes(r)
	s.mountProfileDefinitionRoutes(r)
	s.mountLifecycleHookRoutes(r)
	s.mountRemoteCollaborationRoutes(r)
	s.mountPeerAPIRoutes(r)

	r.Get("/api/health", s.health)
	r.Get("/api/setup/status", s.setupStatus)
	s.mountDesktopShellRoutes(r)
	s.mountDesktopShellExtraRoutes(r)
	s.mountAuthRoutes(r)
	r.Get("/api/settings", s.settings)
	s.mountSecurityRoutes(r)
	r.Get("/api/models", s.models)
	r.Get("/api/licenses", s.licenses)
	r.Get("/api/runtime/summary", s.runtimeSummary)
	r.Get("/api/storage/summary", s.storageSummary)
	r.Get("/api/usage/summary", s.usageSummary)
	r.Get("/api/usage/history", s.usageHistory)
	r.Get("/api/navigation", s.navigation)
	r.Get("/api/overview", s.overview)
	r.Get("/api/system/metrics", s.systemMetrics)
	r.Post("/api/conversations", s.createConversation)
	r.Get("/api/task-workspace", s.taskWorkspace)
	s.mountSensitiveTokenRoutes(r)
	s.mountBackendRoutes(r)
	s.mountMemoryRoutes(r)
	s.mountSkillRoutes(r)
	s.mountSkillV2Routes(r)
	s.mountMCPServerRoutes(r)
	s.mountNotificationRoutes(r)
	s.mountScheduleRoutes(r)
	s.mountIntegrationConnectionRoutes(r)
	s.mountChannelRoutes(r)
	s.mountAuditRoutes(r)
	s.mountDeviceRoutes(r)
	s.mountMonitoringRoutes(r)
	s.mountWorkflowRoutes(r)
	s.mountFSRoutes(r)
	s.mountProjectRoutes(r)
	s.mountWorklineRoutes(r)
	s.mountAgentRoutes(r)
	s.mountLearnedFeatureRoutes(r)
	s.MountUpdateRoutes(r)
	s.mountModelAggregateRoutes(r)
	s.mountClientIdentityRoutes(r)
	s.mountExecutionRoutes(r)
	s.mountBackgroundTaskRoutes(r)
	s.mountNetworkDiagnosticRoutes(r)
	s.mountV2AgentRoutes(r)
	s.mountWebsocketRoutes(r)
	return r
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 256<<10))
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

func statusFromError(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if db.IsNotFound(err) || errors.Is(err, http.ErrMissingFile) {
		return http.StatusNotFound
	}
	if db.IsConflict(err) {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}
