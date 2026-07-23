package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/review"
	"autoto/internal/runtime"
	"autoto/internal/server"
	updatepkg "autoto/internal/update"
)

type orderedService struct {
	name   string
	mu     *sync.Mutex
	closed *[]string
}

func (s orderedService) Start(context.Context) error { return nil }
func (s orderedService) Close(context.Context) error {
	s.mu.Lock()
	*s.closed = append(*s.closed, s.name)
	s.mu.Unlock()
	return nil
}

func TestRuntimeRegistrationClosesHTTPAndGatewayBeforeWorkers(t *testing.T) {
	var mu sync.Mutex
	closed := []string{}
	service := func(name string) orderedService { return orderedService{name: name, mu: &mu, closed: &closed} }
	supervisor := runtime.NewSupervisor()
	if err := registerRuntimeServices(supervisor, service("preview"), service("channels"), service("automation"), service("background"), service("gateway"), service("http")); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"http", "gateway", "background", "automation", "channels", "preview"}
	if !reflect.DeepEqual(closed, want) {
		t.Fatalf("unexpected close order: got %v want %v", closed, want)
	}
}

func TestBindConfiguredHTTPListenersRejectsDuplicateMainProcess(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port

	httpListener, gatewayListener, err := bindConfiguredHTTPListeners(config.Config{
		Server: config.ServerConfig{Host: "127.0.0.1", Port: port},
	}, false)
	if err == nil {
		if httpListener != nil {
			_ = httpListener.Close()
		}
		if gatewayListener != nil {
			_ = gatewayListener.Close()
		}
		t.Fatal("expected duplicate main listener to be rejected")
	}
	if httpListener != nil || gatewayListener != nil {
		t.Fatalf("failed bind leaked listeners: main=%v gateway=%v", httpListener, gatewayListener)
	}
}

func TestBindConfiguredHTTPListenersEphemeralUsesPortZero(t *testing.T) {
	// Even when config listens on all interfaces, ephemeral must pin loopback.
	httpListener, gatewayListener, err := bindConfiguredHTTPListeners(config.Config{
		Server:  config.ServerConfig{Host: "0.0.0.0", Port: 16888},
		Gateway: config.GatewayConfig{Enabled: true, Host: "0.0.0.0", Port: 18789},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer httpListener.Close()
	if gatewayListener != nil {
		_ = gatewayListener.Close()
		t.Fatal("Gateway listener must not be pre-bound during runtime construction")
	}
	addr, ok := httpListener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected listener addr type %T", httpListener.Addr())
	}
	if addr.Port == 0 || addr.Port == 16888 {
		t.Fatalf("ephemeral bind did not receive an OS-assigned non-configured port: %v", addr)
	}
	if addr.IP == nil || !addr.IP.IsLoopback() {
		t.Fatalf("ephemeral bind expected loopback, got %v", addr)
	}
	if !addr.IP.Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("ephemeral bind expected 127.0.0.1, got %v", addr.IP)
	}
}

func TestBrowserFacingHostPortRewritesWildcards(t *testing.T) {
	if got := browserFacingHostPort("0.0.0.0:7788"); got != "127.0.0.1:7788" {
		t.Fatalf("0.0.0.0 rewrite: got %q", got)
	}
	if got := browserFacingHostPort("[::]:7788"); got != "127.0.0.1:7788" {
		t.Fatalf(":: rewrite: got %q", got)
	}
	if got := browserFacingHostPort("127.0.0.1:7788"); got != "127.0.0.1:7788" {
		t.Fatalf("loopback passthrough: got %q", got)
	}
}

func TestRuntimeStartWaitReadyAndClose(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "config.json")
	cfg := config.Config{
		SchemaVersion: config.CurrentConfigVersion,
		Server:        config.ServerConfig{Host: "127.0.0.1", Port: 16888},
		Paths: config.PathsConfig{
			HomeDir:           home,
			DatabasePath:      filepath.Join(home, "autoto.db"),
			DefaultProjectDir: filepath.Join(home, "projects"),
		},
		Agent: config.AgentConfig{DefaultModel: "openai:gpt-4.1-mini", SummaryModel: "openai:gpt-4.1-mini"},
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	rt, err := NewRuntime(Options{
		ConfigPath:    configPath,
		EphemeralHTTP: true,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rt.URL() == "" || rt.Addr() == "" {
		t.Fatalf("runtime missing bound address: url=%q addr=%q", rt.URL(), rt.Addr())
	}
	if rt.Addr() == "127.0.0.1:16888" {
		t.Fatal("ephemeral runtime unexpectedly used configured port")
	}
	if got := rt.ConfigPath(); got != configPath {
		t.Fatalf("ConfigPath=%q want %q", got, configPath)
	}
	if snap := rt.Config(); snap.Server.Host != "127.0.0.1" {
		t.Fatalf("Config host=%q", snap.Server.Host)
	}
	// Shell hosts are optional; registering no-ops keeps the desktop API surface
	// reachable when the Wails package is build-tag excluded.
	rt.SetShellDialogHost(stubShellDialogHost{})
	rt.SetShellDialogHost(nil)
	rt.SetShellLifecycleHost(stubShellLifecycleHost{})
	rt.SetShellLifecycleHost(nil)
	rt.SetShellUpdateHost(stubShellUpdateHost{})
	rt.SetShellUpdateHost(nil)

	// Desktop ReadyTimeout cancels the start context after Start returns.
	// Workers must keep serving until Close, not inherit that cancellation.
	startCtx, cancelStart := context.WithTimeout(context.Background(), 15*time.Second)
	if err := rt.Start(startCtx); err != nil {
		cancelStart()
		t.Fatal(err)
	}
	cancelStart()
	readyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rt.WaitReady(readyCtx); err != nil {
		_ = rt.Close(context.Background())
		t.Fatal(err)
	}

	resp, err := http.Get(rt.URL() + "/api/health")
	if err != nil {
		_ = rt.Close(context.Background())
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = rt.Close(context.Background())
		t.Fatalf("health status %d", resp.StatusCode)
	}
	// Start context is already canceled; health must still work.
	resp2, err := http.Get(rt.URL() + "/api/health")
	if err != nil {
		_ = rt.Close(context.Background())
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		_ = rt.Close(context.Background())
		t.Fatalf("health after start cancel status %d", resp2.StatusCode)
	}

	if err := rt.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("second close should be idempotent: %v", err)
	}
}

type stubShellDialogHost struct{}

func (stubShellDialogHost) Confirm(context.Context, string, string) (bool, error) {
	return false, nil
}
func (stubShellDialogHost) Alert(context.Context, string, string) error { return nil }
func (stubShellDialogHost) PickDirectory(context.Context, string, string) (string, bool, error) {
	return "", true, nil
}
func (stubShellDialogHost) PickFile(context.Context, string, string, []server.ShellFileFilter) (string, bool, error) {
	return "", true, nil
}

type stubShellLifecycleHost struct{}

func (stubShellLifecycleHost) AutostartStatus() (bool, string, string, error) {
	return false, "", "", nil
}
func (stubShellLifecycleHost) AutostartEnable() error  { return nil }
func (stubShellLifecycleHost) AutostartDisable() error { return nil }
func (stubShellLifecycleHost) NotifyDeepLink(string) error {
	return nil
}

type stubShellUpdateHost struct{}

func (stubShellUpdateHost) StageLocalUpdate(string, string, string) (updatepkg.PendingReplace, error) {
	return updatepkg.PendingReplace{}, nil
}
func (stubShellUpdateHost) PendingUpdate() (updatepkg.PendingReplace, bool, error) {
	return updatepkg.PendingReplace{}, false, nil
}
func (stubShellUpdateHost) ClearPendingUpdate() error { return nil }

func TestNewGatewayHTTPServerHonorsDisabledConfig(t *testing.T) {
	server, err := newGatewayHTTPServer(config.Config{}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if server != nil {
		t.Fatal("disabled gateway unexpectedly created an HTTP server")
	}
}

func TestNewGatewayHTTPServerUsesIndependentGatewayRouter(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := config.Config{Gateway: config.GatewayConfig{
		Enabled:              true,
		Host:                 "127.0.0.1",
		Port:                 8788,
		MaxGlobalConcurrency: 4,
		MaxRequestBytes:      1 << 20,
	}}
	server, err := newGatewayHTTPServer(cfg, store, providers.NewRegistry(), func(context.Context, string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if server == nil || server.Addr != "127.0.0.1:8788" || server.MaxHeaderBytes != 32<<10 || server.ReadHeaderTimeout != 10*time.Second || server.ReadTimeout != 30*time.Second || server.IdleTimeout != 60*time.Second || server.WriteTimeout != 0 {
		t.Fatalf("unexpected gateway HTTP server: %+v", server)
	}

	gatewayResponse := httptest.NewRecorder()
	server.Handler.ServeHTTP(gatewayResponse, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if gatewayResponse.Code != http.StatusUnauthorized {
		t.Fatalf("gateway route returned %d: %s", gatewayResponse.Code, gatewayResponse.Body.String())
	}
	adminResponse := httptest.NewRecorder()
	server.Handler.ServeHTTP(adminResponse, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if adminResponse.Code != http.StatusNotFound {
		t.Fatalf("gateway router exposed an admin route: %d %s", adminResponse.Code, adminResponse.Body.String())
	}
}

func TestProviderConfigForRuntimeInjectsInstallationIdentity(t *testing.T) {
	settings := db.RuntimeSettings{InstallationID: "123e4567-e89b-42d3-a456-426614174000"}
	original := config.ProviderConfig{Name: "openai", Type: "openai", Model: "gpt-5"}
	got := providerConfigForRuntime(original, settings)
	if got.ClientVersion != config.Version || got.InstallationID != settings.InstallationID {
		t.Fatalf("runtime identity was not injected: %+v", got)
	}
	if original.ClientVersion != "" || original.InstallationID != "" {
		t.Fatalf("provider config input was mutated: %+v", original)
	}
	if _, err := providers.NewProvider(got); err != nil {
		t.Fatalf("injected provider config should remain valid: %v", err)
	}
}

type reviewRegistrationProvider struct {
	request providers.GenerateRequest
}

func (p *reviewRegistrationProvider) Name() string { return "review" }
func (p *reviewRegistrationProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{Streaming: true}
}
func (p *reviewRegistrationProvider) ListModels(context.Context) ([]string, error) {
	return []string{"dedicated"}, nil
}
func (p *reviewRegistrationProvider) Generate(_ context.Context, request providers.GenerateRequest) (<-chan providers.Event, error) {
	p.request = request
	out := make(chan providers.Event, 2)
	out <- providers.Event{Type: "text", Text: `{"verdict":"pass","reason":"looks good"}`}
	out <- providers.Event{Type: "done", Done: true, StopReason: "end_turn"}
	close(out)
	return out, nil
}

func TestConfiguredReviewServiceUsesDedicatedModelWithoutTools(t *testing.T) {
	provider := &reviewRegistrationProvider{}
	registry := providers.NewRegistry()
	registry.Register(provider)
	service := server.NewReviewService(registry, "review:dedicated")
	result, err := service.Review(context.Background(), review.Request{
		Subject: "review planned change",
		Draft:   review.PlanDraft{Goal: "change", Assumptions: []string{}, Steps: []string{"edit"}, Risks: []string{}, Tests: []string{"test"}, Rollback: []string{}},
	})
	if err != nil || result.Verdict != review.VerdictPass {
		t.Fatalf("unexpected review service result: result=%+v err=%v", result, err)
	}
	if provider.request.Model != "dedicated" || provider.request.Tools != nil {
		t.Fatalf("review service must use configured dedicated model without tools: %+v", provider.request)
	}
}

func TestAggregateSourceFromStorePreservesMemberOrder(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	want := []string{"second:model-b", "first:model-a"}
	if _, err := store.UpsertModelAggregate(ctx, db.ModelAggregate{Name: "fast", Mode: "priority", Members: want}, 0); err != nil {
		t.Fatal(err)
	}
	registry := providers.NewRegistry()
	registry.SetAggregateSource(aggregateSourceFromStore(store))
	provider, model, err := registry.Resolve("aggregate:fast")
	if err != nil {
		t.Fatal(err)
	}
	if model != "fast" {
		t.Fatalf("unexpected aggregate model name %q", model)
	}
	models, err := provider.ListModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("aggregate order changed: got %v want %v", models, want)
	}
}
