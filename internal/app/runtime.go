package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"autoto/internal/agent"
	"autoto/internal/audit"
	"autoto/internal/automation"
	"autoto/internal/background"
	"autoto/internal/channels"
	"autoto/internal/compat"
	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/gateway"
	"autoto/internal/imageassets"
	"autoto/internal/integrations"
	"autoto/internal/peercontrol"
	"autoto/internal/plugins"
	"autoto/internal/preview"
	"autoto/internal/providers"
	"autoto/internal/runtime"
	"autoto/internal/secrets"
	"autoto/internal/server"
	"autoto/internal/tools"
)

// Runtime owns a fully wired Autoto process that can be started and closed by
// either the CLI entrypoint or a future desktop shell.
type Runtime struct {
	logger *slog.Logger

	cfg        config.Config
	configPath string

	store             *db.Store
	generatedImages   *imageassets.Store
	runner            *agent.Runner
	application       *server.Server
	httpServer        *http.Server
	httpListener      net.Listener
	gatewayManager    *gateway.Manager
	supervisor        *runtime.Supervisor
	previewManager    *preview.Manager
	temporaryTunnel   *server.TemporaryTunnelManager
	apiTunnel         *server.TemporaryTunnelManager
	peerManager       *peercontrol.Manager
	channelManager    *channels.Manager
	automationManager *automation.Manager
	backgroundManager *background.Manager
	providerRegistry  *providers.Registry
	actualHTTPAddr    string
	ephemeralHTTP     bool

	mu      sync.Mutex
	state   runtimeLifecycleState
	closeCh chan struct{}
}

type runtimeLifecycleState uint8

const (
	runtimeNew runtimeLifecycleState = iota
	runtimeStarted
	runtimeClosed
)

// NewRuntime loads configuration, binds listeners, opens persistence, and wires
// services. It does not start serving until Start is called.
func NewRuntime(options Options) (*Runtime, error) {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if options.LegacyCommand {
		logger.Warn("codeharbor command is deprecated; use autoto", "replacement", "autoto", "removalVersion", compat.RemovalVersion)
	}

	resolvedConfigPath, err := config.ResolvePath(options.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config: %w", err)
	}
	cfg, legacyReport, err := config.LoadWithReport(resolvedConfigPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	providerAPIKeyInputs, err := config.InspectProviderAPIKeyInputs(resolvedConfigPath, cfg)
	if err != nil {
		return nil, fmt.Errorf("inspect provider credential sources: %w", err)
	}
	if !legacyReport.Empty() {
		logger.Warn(
			"legacy compatibility used",
			"legacy", legacyReport.LegacyNames(),
			"replacement", legacyReport.Replacements(),
			"removalVersion", compat.RemovalVersion,
		)
	}

	httpListener, _, err := bindConfiguredHTTPListeners(cfg, options.EphemeralHTTP)
	if err != nil {
		return nil, err
	}
	actualHTTPAddr := httpListener.Addr().String()

	cleanup := func(store *db.Store) {
		if store != nil {
			_ = store.Close()
		}
		if httpListener != nil {
			_ = httpListener.Close()
		}
	}

	store, err := db.Open(context.Background(), cfg.Paths.DatabasePath)
	if err != nil {
		cleanup(nil)
		return nil, fmt.Errorf("open database: %w", err)
	}
	generatedImages, err := imageassets.New(cfg.Paths.HomeDir)
	if err != nil {
		cleanup(store)
		return nil, fmt.Errorf("open generated image store: %w", err)
	}
	providerVault := secrets.NewProviderVault(store, cfg.Paths.HomeDir)
	cfg, providerSecretWarnings := hydrateProviderSecrets(context.Background(), cfg, providerVault, providerAPIKeyInputs, resolvedConfigPath)
	for _, warning := range providerSecretWarnings {
		logger.Warn("provider credential recovery warning", "error", warning)
	}
	runtimeSettings, err := store.GetRuntimeSettings(context.Background())
	if err != nil {
		cleanup(store)
		return nil, fmt.Errorf("load runtime settings: %w", err)
	}
	if err := store.SeedBackends(context.Background(), configuredBackends(cfg.Backends.Instances)); err != nil {
		cleanup(store)
		return nil, fmt.Errorf("seed backends: %w", err)
	}

	providerRegistry := providers.NewRegistry()
	providerRegistry.SetAggregateSource(aggregateSourceFromStore(store))
	for _, providerCfg := range cfg.Providers.Instances {
		if providerCfg.Disabled {
			logger.Info("skip disabled provider", "name", providerCfg.Name, "type", providerCfg.Type)
			continue
		}
		providerCfg = providerConfigForRuntime(providerCfg, runtimeSettings)
		providerCfg = providers.ApplyCredentialStorePath(providerCfg, cfg.Paths.HomeDir)
		provider, err := providers.NewProvider(providerCfg)
		if err != nil {
			logger.Warn("skip unsupported provider", "name", providerCfg.Name, "type", providerCfg.Type, "error", err)
			continue
		}
		if codexProvider, ok := provider.(*providers.CodexProvider); ok {
			codexProvider.SetAccountTelemetry(store)
			codexProvider.SetGatewayAccountPolicy(store)
		}
		if anthropicProvider, ok := provider.(*providers.AnthropicProvider); ok {
			anthropicProvider.SetAccountTelemetry(store)
			anthropicProvider.SetGatewayAccountPolicy(store)
		}
		if geminiProvider, ok := provider.(*providers.GeminiProvider); ok {
			geminiProvider.SetAccountTelemetry(store)
			geminiProvider.SetGatewayAccountPolicy(store)
		}
		if grokProvider, ok := provider.(*providers.GrokProvider); ok {
			grokProvider.SetAccountTelemetry(store)
			grokProvider.SetGatewayAccountPolicy(store)
		}
		if kimiProvider, ok := provider.(*providers.KimiProvider); ok {
			kimiProvider.SetAccountTelemetry(store)
			kimiProvider.SetGatewayAccountPolicy(store)
		}
		providerRegistry.Register(provider)
	}
	if !providerRegistry.SetDefaultFromConfig(cfg.Agent.DefaultModel, cfg.Providers.Instances) {
		logger.Warn("no configured provider registered as default", "defaultModel", cfg.Agent.DefaultModel)
	}

	toolRegistry := tools.NewRegistry()
	tools.RegisterCore(toolRegistry)
	secretResolver := secrets.EnvResolver{}
	pluginService := plugins.NewService(store, secretResolver)

	hub := agent.NewHub()
	runner := agent.NewRunner(store, providerRegistry, toolRegistry, hub, cfg.Agent)
	runner.SetGeneratedImageStore(generatedImages)
	runner.SetDynamicToolSource(pluginService)
	runner.SetDefaultReasoningEffort(runtimeSettings.DefaultReasoningEffort)
	if err := runner.RecoverInterruptedRuns(context.Background()); err != nil {
		cleanup(store)
		return nil, fmt.Errorf("recover interrupted runs: %w", err)
	}
	connectionService := integrations.NewConnectionService(store, secretResolver)
	auditRecorder := audit.NewRecorder(store)
	automationManager, err := automation.NewManager(automation.Config{Store: store, Runner: runner, Audit: auditRecorder})
	if err != nil {
		cleanup(store)
		return nil, fmt.Errorf("create automation manager: %w", err)
	}
	channelManager, err := channels.New(store, connectionService, channelApprovalAdapter{runner: runner}, toolRegistry)
	if err != nil {
		cleanup(store)
		return nil, fmt.Errorf("create channel manager: %w", err)
	}
	automationManager.SetTelegramSender(channelManager)
	runner.SetNotifier(automationManager)

	backgroundManager := background.NewManager(store, background.Options{
		WorkerCount:          cfg.Background.WorkerCount,
		PerAgentLimit:        cfg.Background.PerAgentLimit,
		AllowNestedSubagents: cfg.Background.AllowNestedSubagents,
		MaxSubagentDepth:     cfg.Background.MaxSubagentDepth,
	})
	if err := backgroundManager.RegisterExecutor(db.BackgroundTaskKindShell, background.NewShellExecutor()); err != nil {
		cleanup(store)
		return nil, fmt.Errorf("register background shell executor: %w", err)
	}
	if err := backgroundManager.RegisterExecutor(db.BackgroundTaskKindAgent, background.NewAgentExecutor(store, runner, backgroundManager)); err != nil {
		cleanup(store)
		return nil, fmt.Errorf("register background agent executor: %w", err)
	}
	backgroundService := background.NewService(backgroundManager, store, runner)
	backgroundManager.SetValidator(runner.ValidateBackgroundTask)
	eventHook, terminalHook := background.NewManagerHooks(hub, automationManager, runner)
	backgroundManager.SetEventHook(eventHook)
	backgroundManager.SetTerminalHook(terminalHook)
	runner.SetBackgroundTaskService(backgroundService)

	previewManager := preview.NewManager()
	// Prefer the actual bound address so temporary tunnels point at the live
	// listener when EphemeralHTTP or OS-assigned ports are in use.
	temporaryTunnelManager := server.NewTemporaryTunnelManager(actualHTTPAddr, cfg.Paths.HomeDir)
	peerManager, err := peercontrol.NewManager(peercontrol.ManagerOptions{HomeDir: cfg.Paths.HomeDir, Store: store})
	if err != nil {
		cleanup(store)
		return nil, fmt.Errorf("create peer control manager: %w", err)
	}
	reviewService := server.NewReviewService(providerRegistry, cfg.Agent.ReviewModel)
	runner.SetReviewService(reviewService)
	application := server.New(cfg, store, runner, hub, providerRegistry)
	application.SetGeneratedImageStore(generatedImages)
	application.SetProviderVault(providerVault)
	application.SetToolRegistry(toolRegistry)
	application.SetBackgroundTaskService(backgroundService)
	application.SetBackgroundRuntimeController(backgroundManager)
	application.SetAutomationToolCatalog(server.NewAutomationToolCatalog(cfg.Paths.HomeDir, store, server.AutomationToolCatalogOptions{}))
	application.SetAutomationManager(automationManager)
	application.SetConnectionService(connectionService)
	application.SetPluginService(pluginService)
	application.SetReviewService(reviewService)
	application.SetAuditRecorder(auditRecorder)
	application.SetPreviewManager(previewManager)
	application.SetTemporaryTunnelManager(temporaryTunnelManager)
	application.SetPeerControlManager(peerManager)
	application.SetConfigPath(resolvedConfigPath)

	gatewayManager, err := gateway.NewManager(gateway.ManagerOptions{
		Store:              store,
		Registry:           providerRegistry,
		ProviderAllowed:    application.GatewayProviderAllowed,
		Config:             cfg.Gateway,
		EphemeralIsolation: options.EphemeralHTTP,
		Logger:             logger,
	})
	if err != nil {
		cleanup(store)
		return nil, fmt.Errorf("create gateway manager: %w", err)
	}
	application.SetGatewayRuntimeController(gatewayManager)

	// A second tunnel, pointed at the gateway rather than at Autoto's own
	// listener. Keeping them separate is the point: the public API URL can be
	// handed out without also exposing the management UI, and either can be
	// closed without disturbing the other. The address is resolved on each start
	// because the gateway binds its own listener, which may be off or may move.
	apiTunnelManager := server.NewResolvedTunnelManager(func() (string, error) {
		return gatewayManager.Status().Address, nil
	}, cfg.Paths.HomeDir)
	application.SetAPITunnelManager(apiTunnelManager)

	httpServer := &http.Server{
		Addr:              actualHTTPAddr,
		Handler:           application.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	return &Runtime{
		logger:            logger,
		cfg:               cfg,
		configPath:        resolvedConfigPath,
		store:             store,
		generatedImages:   generatedImages,
		runner:            runner,
		application:       application,
		httpServer:        httpServer,
		httpListener:      httpListener,
		gatewayManager:    gatewayManager,
		previewManager:    previewManager,
		temporaryTunnel:   temporaryTunnelManager,
		apiTunnel:         apiTunnelManager,
		peerManager:       peerManager,
		channelManager:    channelManager,
		automationManager: automationManager,
		backgroundManager: backgroundManager,
		providerRegistry:  providerRegistry,
		actualHTTPAddr:    actualHTTPAddr,
		ephemeralHTTP:     options.EphemeralHTTP,
		closeCh:           make(chan struct{}),
	}, nil
}

// Start begins serving HTTP and runtime workers. The provided context bounds
// only the synchronous start phase (ctx.Err checks and service Start returns).
// Long-lived workers must not inherit that context: callers cancel it after
// Start returns (desktop ReadyTimeout), while lifecycle continues until Close.
func (r *Runtime) Start(ctx context.Context) error {
	if r == nil {
		return errors.New("app: nil runtime")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == runtimeClosed {
		return errors.New("app: runtime already closed")
	}
	if r.state == runtimeStarted {
		return errors.New("app: runtime already started")
	}

	supervisor := runtime.NewSupervisor()
	onServeError := func(component string) func(error) {
		return func(err error) {
			r.logger.Error("serve "+component, "error", err)
			r.requestClose()
		}
	}
	services := []runtime.Service{r.previewManager, r.temporaryTunnel, r.apiTunnel, r.peerManager, r.channelManager, r.automationManager, r.backgroundManager, r.gatewayManager}
	services = append(services, runtime.NewHTTPServiceWithListener(r.httpServer, r.httpListener, onServeError("http")))
	// Register cleanup after HTTP so the server is started before the one-shot
	// sweep begins. Supervisor owns the worker until Runtime.Close.
	services = append(services, newGeneratedImagesCleanupService(r.logger, r.store, r.generatedImages))
	if err := registerRuntimeServices(supervisor, services...); err != nil {
		return err
	}

	r.logger.Info("autoto listening", "addr", r.URL(), "config", r.configPath, "ephemeralHTTP", r.ephemeralHTTP)
	// Detach from caller start timeout so channel/automation/http workers keep
	// running until Close. Still honor an already-cancelled start context.
	runCtx := context.WithoutCancel(ctx)
	if err := supervisor.Start(runCtx); err != nil {
		return err
	}
	if status := r.gatewayManager.Status(); status.Running {
		r.logger.Info("private API gateway listening", "addr", fmt.Sprintf("http://%s", status.Address), "ephemeralIsolation", status.EphemeralIsolation)
	}
	// Background reconciliation runs as part of the supervisor start. Only then
	// can a continuation safely decide whether its task boundary is terminal.
	if err := r.runner.RecoverContinuationPendingRuns(context.Background()); err != nil {
		_ = supervisor.Close(context.Background())
		return fmt.Errorf("recover continuation pending runs: %w", err)
	}

	r.supervisor = supervisor
	r.state = runtimeStarted
	// Proactively refresh any expired Anthropic OAuth tokens in the background
	// so the first real inference request does not pay a cold TLS+DNS penalty
	// against the token endpoint on Windows or slow-network environments.
	go r.warmupAnthropicOAuthTokens(runCtx)
	return nil
}

// WaitReady polls /api/health until the HTTP server answers or ctx ends.
func (r *Runtime) WaitReady(ctx context.Context) error {
	if r == nil {
		return errors.New("app: nil runtime")
	}
	client := &http.Client{Timeout: 500 * time.Millisecond}
	url := r.URL() + "/api/health"
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("health status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("wait ready: %w (%v)", ctx.Err(), lastErr)
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// warmupAnthropicOAuthTokens proactively refreshes expired Anthropic OAuth
// tokens at startup so the first real request does not pay a cold-start
// penalty (DNS + TLS handshake to console.anthropic.com) on slow networks or
// Windows where the network stack may not be fully ready immediately.
func (r *Runtime) warmupAnthropicOAuthTokens(ctx context.Context) {
	if r == nil || r.providerRegistry == nil {
		return
	}
	warmupCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// Anthropic (OAuth subscription)
	if p, ok := r.providerRegistry.Get("anthropic"); ok {
		if ap, ok := p.(*providers.AnthropicProvider); ok {
			ap.WarmupOAuthTokens(warmupCtx)
		}
	}

	// Subscription providers: Gemini / Grok / Kimi / Kiro all use the same
	// expiring-token pattern and suffer the same cold-start failure on Windows.
	type tokenWarmer interface {
		WarmupTokens(context.Context)
	}
	for _, name := range r.providerRegistry.Names() {
		if warmupCtx.Err() != nil {
			return
		}
		p, ok := r.providerRegistry.Get(name)
		if !ok {
			continue
		}
		if tw, ok := p.(tokenWarmer); ok {
			tw.WarmupTokens(warmupCtx)
		}
	}
}

// Close stops services, closes the database, and releases listeners. It is
// safe to call more than once.
func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.state == runtimeClosed {
		r.mu.Unlock()
		return nil
	}
	supervisor := r.supervisor
	store := r.store
	httpListener := r.httpListener
	r.supervisor = nil
	r.store = nil
	r.state = runtimeClosed
	r.requestCloseLocked()
	r.mu.Unlock()

	var errs []error
	if supervisor != nil {
		if err := supervisor.Close(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if store != nil {
		if err := store.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	// Listeners are normally closed by HTTP shutdown; close defensively when
	// Start never ran or serve failed before takeover.
	if httpListener != nil {
		_ = httpListener.Close()
	}
	return errors.Join(errs...)
}

// Done returns a channel that closes when the runtime requests shutdown, for
// example after a fatal serve error.
func (r *Runtime) Done() <-chan struct{} {
	if r == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return r.closeCh
}

// URL returns the browser-facing base URL for the bound HTTP listener.
// Wildcard binds (0.0.0.0 / ::) are rewritten to 127.0.0.1 so WebViews and
// health probes hit a loopback Host that localRequestGuard treats as local.
func (r *Runtime) URL() string {
	if r == nil || r.actualHTTPAddr == "" {
		return ""
	}
	return "http://" + browserFacingHostPort(r.actualHTTPAddr)
}

// Addr returns the actual HTTP bind address (host:port).
func (r *Runtime) Addr() string {
	if r == nil {
		return ""
	}
	return r.actualHTTPAddr
}

// ConfigPath returns the resolved config path used by this runtime.
func (r *Runtime) ConfigPath() string {
	if r == nil {
		return ""
	}
	return r.configPath
}

// Config returns a snapshot of the loaded configuration.
func (r *Runtime) Config() config.Config {
	if r == nil {
		return config.Config{}
	}
	if r.application != nil {
		return r.application.ConfigSnapshot()
	}
	return r.cfg
}

// GatewayStatus returns the live private Gateway state.
func (r *Runtime) GatewayStatus() gateway.ManagerStatus {
	if r == nil || r.gatewayManager == nil {
		return gateway.ManagerStatus{}
	}
	return r.gatewayManager.Status()
}

// GatewayAddr returns the actual bound Gateway address, or an empty string when
// the Gateway is disabled or not currently running.
func (r *Runtime) GatewayAddr() string {
	return r.GatewayStatus().Address
}

// SetShellDialogHost registers shell-only native dialogs on the HTTP server.
// Browser/CLI runtimes leave this unset. The desktop shell registers a host
// that shows OS dialogs without exposing Agent APIs.
func (r *Runtime) SetShellDialogHost(host server.ShellDialogHost) {
	if r == nil || r.application == nil {
		return
	}
	r.application.SetShellDialogHost(host)
}

// SetShellLifecycleHost registers desktop-only autostart / deep-link handlers.
func (r *Runtime) SetShellLifecycleHost(host server.ShellLifecycleHost) {
	if r == nil || r.application == nil {
		return
	}
	r.application.SetShellLifecycleHost(host)
}

// SetShellUpdateHost registers desktop-only local update staging (no network).
func (r *Runtime) SetShellUpdateHost(host server.ShellUpdateHost) {
	if r == nil || r.application == nil {
		return
	}
	r.application.SetShellUpdateHost(host)
}

func (r *Runtime) requestClose() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requestCloseLocked()
}

func (r *Runtime) requestCloseLocked() {
	select {
	case <-r.closeCh:
	default:
		close(r.closeCh)
	}
}

func bindConfiguredHTTPListeners(cfg config.Config, ephemeralHTTP bool) (net.Listener, net.Listener, error) {
	httpAddr := cfg.Addr()
	if ephemeralHTTP {
		// Desktop shells and parallel CLI smokes must not steal the configured
		// port. Always bind IPv4 loopback so local token/origin checks treat the
		// instance as local even when user config uses 0.0.0.0 or ::.
		httpAddr = "127.0.0.1:0"
	}
	httpListener, err := net.Listen("tcp", httpAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("listen on %s: %w", httpAddr, err)
	}
	// The private Gateway is owned by gateway.Manager and is intentionally not
	// pre-bound during NewRuntime. Keep the second return value for compatibility
	// with existing in-package callers while always returning nil.
	return httpListener, nil, nil
}

const generatedImageCleanupGrace = 24 * time.Hour

func cleanupGeneratedImages(ctx context.Context, logger *slog.Logger, store *db.Store, assets *imageassets.Store) {
	if store == nil || assets == nil {
		return
	}
	staging, err := assets.CleanupStaging(generatedImageCleanupGrace)
	if err != nil {
		logger.Warn("cleanup generated image staging", "error", err)
	}
	logGeneratedImageCleanupFailures(logger, "staging", staging)
	referenced, err := store.ListReferencedGeneratedImageStorageKeys(ctx)
	if err != nil {
		logger.Warn("list referenced generated images for cleanup", "error", err)
		return
	}
	objects, err := assets.MarkAndSweep(referenced, generatedImageCleanupGrace)
	if err != nil {
		logger.Warn("sweep generated image objects", "error", err)
	}
	logGeneratedImageCleanupFailures(logger, "objects", objects)
	if len(staging.Removed) > 0 || len(objects.Removed) > 0 {
		logger.Info("cleaned generated image assets", "stagingRemoved", len(staging.Removed), "objectsRemoved", len(objects.Removed))
	}
}

func logGeneratedImageCleanupFailures(logger *slog.Logger, area string, report imageassets.CleanupReport) {
	for key, err := range report.Failed {
		logger.Warn("generated image cleanup could not remove file", "area", area, "key", key, "error", err)
	}
}

// browserFacingHostPort rewrites wildcard listener addresses to loopback so
// clients open a URL that passes the local request guard.
func browserFacingHostPort(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch strings.Trim(strings.ToLower(host), "[]") {
	case "", "0.0.0.0", "::", "*":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func newGatewayHTTPServer(cfg config.Config, store *db.Store, registry *providers.Registry, providerAllowed gateway.ProviderPolicy) (*http.Server, error) {
	if !cfg.Gateway.Enabled {
		return nil, nil
	}
	service, err := gateway.New(store, registry, gateway.Options{
		MaxGlobalConcurrency:         cfg.Gateway.MaxGlobalConcurrency,
		MaxRequestBytes:              cfg.Gateway.MaxRequestBytes,
		ProviderAllowed:              providerAllowed,
		AllowSubscriptionCredentials: true,
	})
	if err != nil {
		return nil, err
	}
	return &http.Server{
		Addr:              cfg.GatewayAddr(),
		Handler:           service.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}, nil
}

func registerRuntimeServices(supervisor *runtime.Supervisor, services ...runtime.Service) error {
	for _, service := range services {
		if err := supervisor.Register(service); err != nil {
			return err
		}
	}
	return nil
}

func providerConfigForRuntime(providerCfg config.ProviderConfig, settings db.RuntimeSettings) config.ProviderConfig {
	providerCfg.ClientVersion = config.Version
	providerCfg.InstallationID = settings.InstallationID
	return providerCfg
}

func aggregateSourceFromStore(store *db.Store) providers.AggregateSource {
	return providers.AggregateSourceFunc(func(ctx context.Context, name string) (providers.AggregateDefinition, error) {
		aggregate, err := store.GetModelAggregate(ctx, name)
		if err != nil {
			return providers.AggregateDefinition{}, err
		}
		return providers.AggregateDefinition{
			Name:    aggregate.Name,
			Mode:    aggregate.Mode,
			Members: append([]string(nil), aggregate.Members...),
		}, nil
	})
}

func configuredBackends(backends []config.BackendConfig) []db.Backend {
	out := make([]db.Backend, 0, len(backends))
	for _, backend := range backends {
		out = append(out, db.Backend{
			ID:      backend.ID,
			Name:    backend.Name,
			Kind:    backend.Kind,
			BaseURL: backend.BaseURL,
			APIKey:  backend.APIKey,
			Active:  backend.Active,
		})
	}
	return out
}
