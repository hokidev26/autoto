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
	localToken           string
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
	legacyWarnings              *compat.Registry
	store                       *db.Store
	runner                      *agentpkg.Runner
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
	subscriptionOAuthTestConfig *subscriptionOAuthLoginTestConfig
	toolRegistry                *tools.Registry
	toolRegistryMu              sync.RWMutex
	backgroundTasks             tools.BackgroundTaskService
	previewManager              *preview.Manager
	temporaryTunnel             *TemporaryTunnelManager
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

	r.Get("/api/health", s.health)
	r.Get("/api/setup/status", s.setupStatus)
	s.mountDesktopShellRoutes(r)
	s.mountDesktopShellExtraRoutes(r)
	r.Get("/api/auth/status", s.authStatus)
	r.Post("/api/auth/register", s.register)
	r.Post("/api/auth/login", s.login)
	r.Post("/api/auth/logout", s.logout)
	r.Get("/api/auth/me", s.me)
	r.Get("/api/users", s.listUsers)
	r.Get("/api/preferences", s.getAccountPreferences)
	r.Patch("/api/preferences", s.patchAccountPreferences)
	r.Post("/api/preferences/import-local", s.importLocalAccountPreferences)
	r.Get("/api/settings", s.settings)
	r.Get("/api/security/remote-access", s.getRemoteAccessSettings)
	r.Get("/api/security/remote-access/tunnel", s.getTemporaryTunnel)
	r.Post(temporaryTunnelInstallPath, s.installTemporaryTunnel)
	r.Post("/api/security/remote-access/tunnel", s.startTemporaryTunnel)
	r.Delete("/api/security/remote-access/tunnel", s.stopTemporaryTunnel)
	r.Patch("/api/security/remote-access/policy", s.updateRemoteAccessPolicy)
	r.Put("/api/security/remote-access/password", s.updateRemoteAccessPassword)
	r.Get("/api/models", s.models)
	r.Get("/api/licenses", s.licenses)
	r.Get("/api/runtime/summary", s.runtimeSummary)
	r.Get("/api/storage/summary", s.storageSummary)
	r.Get("/api/usage/summary", s.usageSummary)
	r.Get("/api/usage/history", s.usageHistory)
	r.Get("/api/navigation", s.navigation)
	r.Get("/api/overview", s.overview)
	r.Post("/api/conversations", s.createConversation)
	r.Get("/api/task-workspace", s.taskWorkspace)
	r.Group(func(r chi.Router) {
		r.Use(s.sensitiveLocalTokenGuard)
		r.Post("/api/providers/test", s.testProviderConfigDraft)
		r.Post("/api/providers/test-message", s.testProviderMessageDraft)
		r.Put("/api/providers/{name}/config", s.updateProviderConfig)
		r.Patch("/api/providers/{name}", s.patchProviderConfig)
		r.Delete("/api/providers/{name}", s.deleteProviderConfig)
		r.Post("/api/providers/{name}/test", s.testProviderConfig)
		r.Get("/api/gateway/status", s.gatewayRuntimeStatus)
		r.Patch("/api/gateway/config", s.patchGatewayRuntimeConfig)
		r.Get("/api/gateway/accounts", s.listGatewayAccounts)
		r.Patch("/api/gateway/accounts/{provider}/{accountId}", s.patchGatewayAccount)
		r.Get("/api/gateway/requests", s.listGatewayRequests)
		r.Get("/api/gateway/keys", s.listGatewayKeys)
		r.Post("/api/gateway/keys", s.createGatewayKey)
		r.Patch("/api/gateway/keys/{id}", s.updateGatewayKey)
		r.Post("/api/gateway/keys/{id}/rotate", s.rotateGatewayKey)
		r.Post("/api/gateway/keys/{id}/revoke", s.revokeGatewayKey)
		r.Get("/api/gateway/models", s.listGatewayModels)
		r.Post("/api/gateway/models", s.createGatewayModel)
		r.Patch("/api/gateway/models", s.updateGatewayModel)
		r.Delete("/api/gateway/models", s.deleteGatewayModel)
		r.Patch("/api/gateway/models/{alias}", s.updateGatewayModel)
		r.Delete("/api/gateway/models/{alias}", s.deleteGatewayModel)
		r.Get("/api/gateway/usage", s.gatewayUsage)
		r.Post("/api/providers/oauth/codex/login/start", s.startCodexOAuthLogin)
		r.Get("/api/providers/oauth/codex/login/{loginId}", s.getCodexOAuthLogin)
		r.Delete("/api/providers/oauth/codex/login/{loginId}", s.cancelCodexOAuthLogin)
		r.Get("/api/providers/oauth/codex/accounts", s.listCodexOAuthAccounts)
		r.Post("/api/providers/oauth/codex/accounts/batch", s.batchCodexOAuthAccounts)
		r.Get("/api/providers/oauth/codex/accounts/{id}/export", s.exportCodexOAuthAccount)
		r.Patch("/api/providers/oauth/codex/accounts/{id}", s.patchCodexOAuthAccount)
		r.Post("/api/providers/oauth/codex/accounts/{id}/refresh", s.refreshCodexOAuthAccount)
		r.Delete("/api/providers/oauth/codex/accounts/{id}", s.deleteCodexOAuthAccount)
		r.Post("/api/providers/oauth/codex/import", s.importCodexOAuthCredentials)
		r.Post("/api/providers/oauth/codex/import/batch", s.batchImportCodexOAuthCredentials)
		r.Post("/api/providers/oauth/anthropic/login/start", s.startAnthropicOAuthLogin)
		r.Get("/api/providers/oauth/anthropic/login/{loginId}", s.getAnthropicOAuthLogin)
		r.Post("/api/providers/oauth/anthropic/login/{loginId}/submit", s.submitAnthropicOAuthLogin)
		r.Delete("/api/providers/oauth/anthropic/login/{loginId}", s.cancelAnthropicOAuthLogin)
		r.Post("/api/providers/oauth/{provider}/login/start", s.startSubscriptionOAuthLogin)
		r.Get("/api/providers/oauth/{provider}/login/{loginId}", s.getSubscriptionOAuthLogin)
		r.Delete("/api/providers/oauth/{provider}/login/{loginId}", s.cancelSubscriptionOAuthLogin)
		r.Get("/api/providers/auth/anthropic/accounts", s.listAnthropicAccounts)
		r.Post("/api/providers/auth/anthropic/accounts", s.createAnthropicAccount)
		r.Patch("/api/providers/auth/anthropic/accounts/{id}", s.patchAnthropicAccount)
		r.Post("/api/providers/auth/anthropic/accounts/{id}/sync", s.syncAnthropicAccount)
		r.Delete("/api/providers/auth/anthropic/accounts/{id}", s.deleteAnthropicAccount)
		r.Get("/api/providers/auth/{provider}/accounts", s.listSubscriptionAccounts)
		r.Patch("/api/providers/auth/{provider}/accounts/{id}", s.patchSubscriptionAccount)
		r.Post("/api/providers/auth/{provider}/accounts/{id}/sync", s.syncSubscriptionAccount)
		r.Delete("/api/providers/auth/{provider}/accounts/{id}", s.deleteSubscriptionAccount)
		r.Get("/api/providers/{name}/auth-files", s.listProviderAuthFiles)
		r.Post("/api/providers/{name}/auth-files/import", s.importProviderAuthFile)
		r.Get("/api/plugins", s.listPlugins)
		r.Post("/api/plugins/install", s.installPlugin)
		r.Get("/api/plugins/{id}", s.getPlugin)
		r.Post("/api/plugins/{id}/enable", s.enablePlugin)
		r.Post("/api/plugins/{id}/disable", s.disablePlugin)
		r.Post("/api/plugins/{id}/discover", s.discoverPlugin)
		r.Delete("/api/plugins/{id}", s.uninstallPlugin)
		r.Patch("/api/runtime/continuation-settings", s.continuationSettingsEndpoint)
		r.Post("/api/agents/{id}/background-tasks", s.createBackgroundTask)
		r.Post("/api/background-tasks/{taskId}/cancel", s.cancelBackgroundTask)
	})
	r.Route("/api/backends", func(r chi.Router) {
		r.Get("/", s.listBackends)
		r.With(s.fullRemoteAccessGuard).Post("/", s.createBackend)
		r.Get("/{id}", s.getBackend)
		r.With(s.fullRemoteAccessGuard).Patch("/{id}", s.updateBackend)
		r.With(s.fullRemoteAccessGuard).Delete("/{id}", s.deleteBackend)
		r.With(s.fullRemoteAccessGuard).Post("/{id}/activate", s.activateBackend)
		r.Get("/{id}/health", s.backendHealth)
	})
	r.Route("/api/memories", func(r chi.Router) {
		r.Get("/", s.listMemories)
		r.Post("/", s.createMemory)
		r.Get("/{id}", s.getMemory)
		r.Patch("/{id}", s.updateMemory)
		r.Delete("/{id}", s.deleteMemory)
	})
	r.Route("/api/skills", func(r chi.Router) {
		r.Get("/", s.listSkills)
		r.Post("/", s.createSkill)
		r.Post("/import/preview", s.previewSkillImport)
		r.Post("/import", s.importSkill)
		r.Get("/{id}", s.getSkill)
		r.Patch("/{id}", s.updateSkill)
		r.Delete("/{id}", s.deleteSkill)
	})
	r.Route("/api/v2/skills", func(r chi.Router) {
		r.Get("/", s.listSkillsV2)
		r.Post("/", s.createSkillV2)
		r.Post("/import/preview", s.previewSkillImport)
		r.Post("/import", s.importSkillV2)
		r.Get("/{id}", s.getSkillV2)
		r.Patch("/{id}", s.updateSkillV2)
		r.Delete("/{id}", s.deleteSkillV2)
		r.Get("/{id}/revisions", s.listSkillRevisionsV2)
		r.Get("/{id}/revisions/{revisionNo}", s.getSkillRevisionV2)
		r.Post("/{id}/restore", s.restoreSkillV2)
		r.Post("/{id}/revisions/{revisionNo}/restore", s.restoreSkillV2)
	})
	r.Route("/api/mcp/servers", func(r chi.Router) {
		r.Get("/", s.listMCPServers)
		r.With(s.fullRemoteAccessGuard).Post("/", s.createMCPServer)
		r.Get("/{id}", s.getMCPServer)
		r.With(s.fullRemoteAccessGuard).Patch("/{id}", s.updateMCPServer)
		r.With(s.fullRemoteAccessGuard).Delete("/{id}", s.deleteMCPServer)
		r.Get("/{id}/tools", s.listMCPServerTools)
	})
	r.Route("/api/notifications", func(r chi.Router) {
		r.Get("/settings", s.getNotificationSettings)
		r.With(s.fullRemoteAccessGuard).Put("/settings", s.updateNotificationSettings)
		r.With(s.fullRemoteAccessGuard).Post("/test", s.testNotification)
		r.Get("/deliveries", s.listNotificationDeliveries)
		r.With(s.fullRemoteAccessGuard).Post("/deliveries/{id}/retry", s.retryNotificationDelivery)
	})
	r.Route("/api/schedules", func(r chi.Router) {
		r.Get("/", s.listSchedules)
		r.With(s.fullRemoteAccessGuard).Post("/", s.createSchedule)
		r.With(s.fullRemoteAccessGuard).Patch("/{id}", s.updateSchedule)
		r.With(s.fullRemoteAccessGuard).Delete("/{id}", s.deleteSchedule)
		r.With(s.fullRemoteAccessGuard).Post("/{id}/run", s.runSchedule)
	})
	r.Route("/api/integrations/connections", func(r chi.Router) {
		r.Get("/", s.listIntegrationConnections)
		r.With(s.fullRemoteAccessGuard).Post("/", s.createIntegrationConnection)
		r.With(s.fullRemoteAccessGuard).Patch("/{id}", s.updateIntegrationConnection)
		r.With(s.fullRemoteAccessGuard).Delete("/{id}", s.deleteIntegrationConnection)
		r.With(s.fullRemoteAccessGuard).Post("/{id}/test", s.testIntegrationConnection)
	})
	r.With(s.fullRemoteAccessGuard).Post("/api/channels/pairing-codes", s.createChannelPairingCode)
	r.Get("/api/channels/pairings", s.listChannelPairings)
	r.With(s.fullRemoteAccessGuard).Post("/api/channels/pairings/{id}/revoke", s.revokeChannelPairing)
	r.Get("/api/audit/events", s.listAuditEvents)
	r.Get("/api/devices", s.listDevices)
	r.With(s.fullRemoteAccessGuard).Post("/api/device-actions", s.createDeviceAction)
	r.With(s.fullRemoteAccessGuard).Post("/api/device-actions/{id}/approve", s.approveDeviceAction)
	r.With(s.fullRemoteAccessGuard).Post("/api/device-actions/{id}/deny", s.denyDeviceAction)
	r.Get("/api/monitoring/snapshot", s.monitoringSnapshot)

	r.Route("/api/workflow", func(r chi.Router) {
		r.Get("/preferences", s.getWorkflowPreferences)
		r.With(s.fullRemoteAccessGuard).Put("/preferences", s.updateWorkflowPreferences)
		r.Get("/tool-permissions", s.listToolPermissionRules)
		r.With(s.fullRemoteAccessGuard).Post("/tool-permissions", s.createToolPermissionRule)
		r.With(s.fullRemoteAccessGuard).Patch("/tool-permissions/{id}", s.updateToolPermissionRule)
		r.With(s.fullRemoteAccessGuard).Delete("/tool-permissions/{id}", s.deleteToolPermissionRule)
	})

	r.Route("/api/fs", func(r chi.Router) {
		r.Get("/browse", s.fsBrowse)
		r.Get("/directories", s.fsDirectories)
		r.Post("/native-directory", s.fsNativeDirectory)
		r.Get("/preview", s.fsPreview)
		r.Post("/mkdir", s.fsMkdir)
	})

	r.Route("/api/projects", func(r chi.Router) {
		r.Get("/", s.listProjects)
		r.Post("/", s.createProject)
		r.Patch("/{id}/navigation-state", s.patchProjectNavigationState)
		r.Get("/{id}", s.getProject)
		r.Get("/{id}/worklines", s.listProjectWorklines)
		r.Get("/{id}/chapters", s.listProjectWorklines)
	})
	r.Get("/api/worklines/{id}", s.getWorkline)
	r.Post("/api/worklines/{id}/fork", s.forkWorkline)
	r.Get("/api/worklines/{id}/merge-check", s.worklineMergeCheck)
	r.Post("/api/worklines/{id}/merge", s.worklineMerge)
	r.Get("/api/worklines/{id}/agents", s.listWorklineAgents)
	r.Get("/api/chapters/{id}", s.getWorkline)
	r.Post("/api/chapters/{id}/fork", s.forkWorkline)
	r.Get("/api/chapters/{id}/merge-check", s.worklineMergeCheck)
	r.Post("/api/chapters/{id}/merge", s.worklineMerge)
	r.Get("/api/chapters/{id}/narrators", s.listWorklineAgents)
	agentRoutes := func(r chi.Router) {
		r.Get("/{id}", s.getAgent)
		r.Get("/{id}/live-snapshot", s.getAgentLiveSnapshot)
		r.Get("/{id}/context", s.getAgentContext)
		r.With(s.fullRemoteAccessGuard).Patch("/{id}/context/preferences", s.patchAgentContextPreferences)
		r.With(s.fullRemoteAccessGuard).Post("/{id}/context/compact", s.compactAgentContext)
		r.With(s.fullRemoteAccessGuard).Post("/{id}/context/clear", s.clearAgentContext)
		r.Patch("/{id}/title", s.updateAgentTitle)
		r.Patch("/{id}/cwd", s.updateAgentCWD)
		r.Patch("/{id}/model", s.updateAgentModel)
		r.Patch("/{id}/reasoning-effort", s.updateAgentReasoningEffort)
		r.Patch("/{id}/fast-mode", s.updateAgentFastMode)
		r.Patch("/{id}/permission-mode", s.updateAgentPermissionMode)
		r.Patch("/{id}/plan-mode", s.updateAgentPlanMode)
		r.Patch("/{id}/navigation-state", s.patchAgentNavigationState)
		r.Get("/{id}/plans", s.listReviewPlans)
		r.Post("/{id}/plans", s.createReviewPlan)
		r.Get("/{id}/plans/{planId}", s.getReviewPlan)
		r.Post("/{id}/plans/{planId}/approve", s.approveReviewPlan)
		r.Post("/{id}/plans/{planId}/execute", s.executeReviewPlan)
		r.Post("/{id}/plans/{planId}/cancel", s.cancelReviewPlan)
		r.Post("/{id}/plans/{planId}/replan", s.replanReviewPlan)
		r.Post("/{id}/interrupt", s.interruptAgent)
		r.Get("/{id}/messages", s.listMessages)
		r.Post("/{id}/messages", s.postMessage)
		r.Get("/{id}/draft", s.getMessageDraft)
		r.Put("/{id}/draft", s.putMessageDraft)
		r.Delete("/{id}/draft", s.deleteMessageDraft)
		r.Post("/{id}/messages/{messageId}/corrections", s.createCorrection)
		r.Get("/{id}/messages/{messageId}/attachments/{attachmentId}", s.getMessageAttachment)
		r.Get("/{id}/messages/{messageId}/generated-images/{assetId}", s.getGeneratedImage)
		r.MethodFunc(http.MethodHead, "/{id}/messages/{messageId}/generated-images/{assetId}", s.getGeneratedImage)
		r.Get("/{id}/runs", s.listRuns)
		r.Get("/{id}/runs/active", s.getActiveRunSummary)
		r.Get("/{id}/runs/{runId}", s.getRunSummary)
		r.Get("/{id}/runs/{runId}/rollback", s.rollbackRunPreview)
		r.Post("/{id}/runs/{runId}/rollback", s.rollbackRun)
		r.Get("/{id}/runs/{runId}/tool-calls", s.listRunToolCalls)
		r.Get("/{id}/background-tasks", s.listBackgroundTasks)
		r.Get("/{id}/tools", s.listTools)
		r.Post("/{id}/tool-calls", s.executeTool)
		r.Get("/{id}/tool-calls/pending", s.listPendingToolCalls)
		r.Post("/{id}/tool-calls/{toolUseId}/approval", s.approveToolCall)
		r.Post("/{id}/tool-calls/{toolUseId}/user-answer", s.answerUserQuestion)
		r.Get("/{id}/tool-calls/{toolUseId}", s.getToolCall)
		r.Get("/{id}/git/status", s.gitStatus)
		r.Get("/{id}/git/diff", s.gitDiff)
		r.Get("/{id}/git/log", s.gitLog)
		r.Post("/{id}/git/commit", s.gitCommit)
		r.Get("/{id}/workspace/tree", s.workspaceTree)
		r.Get("/{id}/workspace/file", s.workspaceFile)
		r.Put("/{id}/workspace/file", s.updateWorkspaceFile)
		r.Get("/{id}/preview/detect", s.detectPreview)
		r.Post("/{id}/preview/start", s.startPreview)
		r.Post("/{id}/preview/stop", s.stopPreview)
		r.Get("/{id}/preview/status", s.previewStatus)
		r.Get("/{id}/preview/logs", s.previewLogs)
	}
	r.Route("/api/agents", agentRoutes)
	r.Route("/api/narrators", agentRoutes)
	r.Post("/api/agents/{id}/spec/tasks/{taskId}/assign", s.assignSpecTask)
	s.mountLearnedFeatureRoutes(r)
	s.MountUpdateRoutes(r)
	r.Get("/api/model-aggregates", s.listModelAggregates)
	r.Get("/api/model-aggregates/{name}", s.getModelAggregate)
	r.With(s.fullRemoteAccessGuard).Put("/api/model-aggregates/{name}", s.putModelAggregate)
	r.With(s.fullRemoteAccessGuard).Delete("/api/model-aggregates/{name}", s.deleteModelAggregate)
	r.With(s.fullRemoteAccessGuard).Patch("/api/runtime/model-settings", s.updateRuntimeModelSettings)
	r.With(s.fullRemoteAccessGuard).Patch("/api/runtime/agent-model-settings", s.updateAgentModelSettings)
	r.With(s.fullRemoteAccessGuard).Patch("/api/runtime/context-settings", s.updateRuntimeContextSettings)
	r.Patch("/api/agents/{id}/reasoning-effort", s.updateAgentReasoningEffort)
	r.Get("/api/client/identity", s.clientIdentity)
	r.With(s.fullRemoteAccessGuard).Post("/api/client/identity/rotate", s.rotateClientIdentity)
	r.Get("/api/execution/devices", s.listExecutionDevices)
	r.With(s.fullRemoteAccessGuard).Post("/api/execution/devices", s.registerRemoteExecutionDevice)
	r.With(s.fullRemoteAccessGuard).Post("/api/execution/devices/{deviceId}/enable", s.enableExecutionDevice)
	r.With(s.fullRemoteAccessGuard).Post("/api/execution/devices/{deviceId}/disable", s.disableExecutionDevice)
	r.With(s.fullRemoteAccessGuard).Put("/api/projects/{projectId}/execution-devices/{deviceId}", s.setProjectExecutionDeviceGrant)
	r.With(s.fullRemoteAccessGuard).Patch("/api/agents/{id}/execution-device", s.setAgentExecutionDevice)
	r.Get("/api/execution/tasks", s.listRemoteExecutionTasks)
	r.Post("/api/execution/tasks", s.createRemoteExecutionTask)
	r.Get("/api/execution/tasks/{taskId}", s.getRemoteExecutionTask)
	r.Get("/api/background-tasks/{taskId}", s.getBackgroundTask)
	r.Get("/api/background-tasks/{taskId}/output", s.backgroundTaskOutput)
	r.Post("/api/background-tasks/{taskId}/wait", s.waitBackgroundTask)
	r.Get("/api/network/diagnostics", s.getNetworkDiagnostics)
	r.Post("/api/network/diagnostics/probe", s.runNetworkDiagnostic)
	r.Get("/api/v2/agents/{id}/live-snapshot", s.getAgentLiveSnapshot)
	r.Get("/api/v2/agents/{id}/stream-state", s.getAgentStreamState)
	r.Get("/api/v2/agents/{id}/skills/effective", s.listEffectiveSkillsV2)
	r.Get("/ws/agent", s.agentWS)
	r.Get("/ws/narrator", s.agentWS)
	r.Get("/ws/terminal", s.terminalWS)
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
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
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
