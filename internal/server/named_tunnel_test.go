package server

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"autoto/internal/config"
)

// namedReadyProcess emits the line cloudflared logs when an edge connection
// registers. A named tunnel never prints a public URL, so this is the only
// readiness signal available.
type namedReadyProcess struct {
	*fakeTemporaryTunnelProcess
}

func (p namedReadyProcess) Start() error {
	go func() {
		_, _ = io.WriteString(p.stdoutWriter, "2026-08-10T05:34:31Z INF Registered tunnel connection connIndex=0 ip=198.41.200.113 protocol=quic\n")
	}()
	return nil
}

func newNamedTunnelManager(t *testing.T, tunnel config.NamedTunnelConfig, capture *temporaryTunnelSpec) *TemporaryTunnelManager {
	t.Helper()
	process := newFakeTemporaryTunnelProcess()
	manager := newTemporaryTunnelManager("127.0.0.1:7788", temporaryTunnelOptions{
		lookPath: func(string) (string, error) { return "/fake/cloudflared", nil },
		command: func(_ context.Context, _ string, spec temporaryTunnelSpec) temporaryTunnelProcess {
			if capture != nil {
				*capture = spec
			}
			return namedReadyProcess{fakeTemporaryTunnelProcess: process}
		},
		startTimeout: 2 * time.Second,
	})
	attachNamedTunnel(manager, func() config.NamedTunnelConfig { return tunnel })
	return manager
}

// A named tunnel must report its configured hostname. Scraping cannot work here,
// so a manager that reported an empty URL would leave the user with a running
// tunnel and no address.
func TestNamedTunnelReportsConfiguredHostnameOnRegistration(t *testing.T) {
	t.Setenv("AUTOTO_TEST_TUNNEL_TOKEN", "fake-token-value")
	var spec temporaryTunnelSpec
	manager := newNamedTunnelManager(t, config.NamedTunnelConfig{
		Hostname: "autoto.example.com",
		TokenRef: "env:AUTOTO_TEST_TUNNEL_TOKEN",
	}, &spec)

	snapshot, err := manager.StartTunnel(context.Background())
	if err != nil {
		t.Fatalf("start named tunnel: %v", err)
	}
	if snapshot.Status != temporaryTunnelRunning {
		t.Fatalf("expected running, got %+v", snapshot)
	}
	if snapshot.PublicURL != "https://autoto.example.com" {
		t.Fatalf("unexpected public URL: %q", snapshot.PublicURL)
	}
	if snapshot.Mode != temporaryTunnelModeNamed {
		t.Fatalf("expected named mode, got %q", snapshot.Mode)
	}
	if !snapshot.NamedConfigured {
		t.Fatal("expected namedConfigured to be true")
	}
	if _, err := manager.StopTunnel(context.Background()); err != nil {
		t.Fatalf("stop named tunnel: %v", err)
	}
}

// The token must never appear in argv. Process listings are readable by other
// local processes on every supported platform, so a token passed as --token
// would be exposed to anything running on the machine.
func TestNamedTunnelPassesTokenByEnvironmentNotArgv(t *testing.T) {
	const token = "fake-token-value-must-not-appear"
	t.Setenv("AUTOTO_TEST_TUNNEL_TOKEN", token)
	var spec temporaryTunnelSpec
	manager := newNamedTunnelManager(t, config.NamedTunnelConfig{
		Hostname: "autoto.example.com",
		TokenRef: "env:AUTOTO_TEST_TUNNEL_TOKEN",
	}, &spec)

	if _, err := manager.StartTunnel(context.Background()); err != nil {
		t.Fatalf("start named tunnel: %v", err)
	}
	defer func() { _, _ = manager.StopTunnel(context.Background()) }()

	joined := strings.Join(spec.Args, "\x00")
	if strings.Contains(joined, token) {
		t.Fatalf("token leaked into argv: %q", spec.Args)
	}
	if strings.Contains(joined, "--token") {
		t.Fatalf("argv must not carry a --token flag: %q", spec.Args)
	}
	var found bool
	for _, entry := range spec.Env {
		if entry == "TUNNEL_TOKEN="+token {
			found = true
		}
	}
	if !found {
		t.Fatal("expected TUNNEL_TOKEN in the child environment")
	}
}

// --url must always be passed. Without it a token-run tunnel takes ingress from
// the dashboard, and with no ingress rule cloudflared answers every request with
// 503 while still reporting a healthy connection.
func TestNamedTunnelPinsLocalTargetAndIsolatesConfig(t *testing.T) {
	t.Setenv("AUTOTO_TEST_TUNNEL_TOKEN", "fake-token-value")
	var spec temporaryTunnelSpec
	manager := newNamedTunnelManager(t, config.NamedTunnelConfig{
		Hostname: "autoto.example.com",
		TokenRef: "env:AUTOTO_TEST_TUNNEL_TOKEN",
	}, &spec)

	if _, err := manager.StartTunnel(context.Background()); err != nil {
		t.Fatalf("start named tunnel: %v", err)
	}
	defer func() { _, _ = manager.StopTunnel(context.Background()) }()

	joined := strings.Join(spec.Args, "\x00")
	if !strings.Contains(joined, "--url\x00http://127.0.0.1:7788") {
		t.Fatalf("expected --url pinned to the local listener: %q", spec.Args)
	}
	configAt := -1
	for index, arg := range spec.Args {
		if arg == "--config" && index+1 < len(spec.Args) {
			configAt = index + 1
			break
		}
	}
	if configAt < 0 || spec.Args[configAt] == "" || spec.Args[configAt] == os.DevNull {
		t.Fatalf("expected an isolated cloudflared config file, got %q", spec.Args)
	}
	if !strings.Contains(joined, "run") {
		t.Fatalf("expected the named tunnel run subcommand: %q", spec.Args)
	}
}

// An unusable token must fail the start. Falling back to a quick tunnel would
// publish a different hostname than the one the user configured and expects.
func TestNamedTunnelFailsClosedWhenTokenIsMissing(t *testing.T) {
	var spec temporaryTunnelSpec
	manager := newNamedTunnelManager(t, config.NamedTunnelConfig{
		Hostname: "autoto.example.com",
		TokenRef: "env:AUTOTO_TEST_TUNNEL_TOKEN_ABSENT",
	}, &spec)

	snapshot, err := manager.StartTunnel(context.Background())
	if err == nil {
		t.Fatal("expected a start failure when the token cannot be resolved")
	}
	if len(spec.Args) != 0 {
		t.Fatalf("cloudflared must not be launched without a token: %q", spec.Args)
	}
	if snapshot.PublicURL != "" {
		t.Fatalf("expected no public URL, got %q", snapshot.PublicURL)
	}
	if snapshot.Status == temporaryTunnelRunning {
		t.Fatal("a failed named tunnel must not report running")
	}
}

// The resolution error must not disclose the token or the variable's value.
func TestNamedTunnelErrorDoesNotDiscloseTokenMaterial(t *testing.T) {
	const token = "fake-token-value-must-not-appear"
	t.Setenv("AUTOTO_TEST_TUNNEL_TOKEN_EMPTY", "   ")
	manager := newNamedTunnelManager(t, config.NamedTunnelConfig{
		Hostname: "autoto.example.com",
		TokenRef: "env:AUTOTO_TEST_TUNNEL_TOKEN_EMPTY",
	}, nil)

	_, err := manager.StartTunnel(context.Background())
	if err == nil {
		t.Fatal("expected a start failure for an empty token")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error disclosed token material: %v", err)
	}
}

// The resolver must read the server's live configuration. Configuration is held
// by value in several places, so a closure over one of those copies would keep
// using the hostname captured at wiring time and silently ignore later edits,
// leaving a tunnel that publishes the old hostname with no visible error.
func TestNamedTunnelResolverFollowsLiveConfigurationEdits(t *testing.T) {
	t.Setenv("AUTOTO_TEST_TUNNEL_TOKEN", "fake-token-value")
	process := newFakeTemporaryTunnelProcess()
	manager := newTemporaryTunnelManager("127.0.0.1:7788", temporaryTunnelOptions{
		lookPath: func(string) (string, error) { return "/fake/cloudflared", nil },
		command: func(_ context.Context, _ string, _ temporaryTunnelSpec) temporaryTunnelProcess {
			return namedReadyProcess{fakeTemporaryTunnelProcess: process}
		},
		startTimeout: 2 * time.Second,
	})

	app := New(config.Config{Security: config.SecurityConfig{
		NamedTunnel: config.NamedTunnelConfig{
			Hostname: "before.example.com",
			TokenRef: "env:AUTOTO_TEST_TUNNEL_TOKEN",
		},
	}}, nil, nil, nil)
	app.AttachNamedTunnel(manager, func(current config.Config) config.NamedTunnelConfig {
		return current.Security.NamedTunnel
	})

	// Simulate a settings change landing after the manager was wired.
	app.cfgMu.Lock()
	app.cfg.Security.NamedTunnel.Hostname = "after.example.com"
	app.cfgMu.Unlock()

	snapshot, err := manager.StartTunnel(context.Background())
	if err != nil {
		t.Fatalf("start named tunnel: %v", err)
	}
	defer func() { _, _ = manager.StopTunnel(context.Background()) }()

	if snapshot.PublicURL != "https://after.example.com" {
		t.Fatalf("resolver used stale configuration: %q", snapshot.PublicURL)
	}
}

// With no named tunnel configured the quick tunnel must remain untouched, so the
// existing behaviour is not changed by adding the feature.
func TestUnconfiguredNamedTunnelLeavesQuickTunnelBehaviour(t *testing.T) {
	process := newFakeTemporaryTunnelProcess()
	var spec temporaryTunnelSpec
	manager := newTemporaryTunnelManager("127.0.0.1:7788", temporaryTunnelOptions{
		lookPath: func(string) (string, error) { return "/fake/cloudflared", nil },
		command: func(_ context.Context, _ string, got temporaryTunnelSpec) temporaryTunnelProcess {
			spec = got
			return process
		},
		startTimeout: 2 * time.Second,
	})
	attachNamedTunnel(manager, func() config.NamedTunnelConfig { return config.NamedTunnelConfig{} })

	snapshot, err := manager.StartTunnel(context.Background())
	if err != nil {
		t.Fatalf("start quick tunnel: %v", err)
	}
	defer func() { _, _ = manager.StopTunnel(context.Background()) }()

	if snapshot.PublicURL != "https://example.trycloudflare.com" {
		t.Fatalf("unexpected quick tunnel URL: %q", snapshot.PublicURL)
	}
	if snapshot.Mode != temporaryTunnelModeQuick {
		t.Fatalf("expected quick mode, got %q", snapshot.Mode)
	}
	if snapshot.NamedConfigured {
		t.Fatal("expected namedConfigured to be false")
	}
	if len(spec.Env) != 0 {
		t.Fatalf("quick tunnel must not carry extra environment: %q", spec.Env)
	}
	if strings.Contains(strings.Join(spec.Args, "\x00"), "run") {
		t.Fatalf("quick tunnel must not use the run subcommand: %q", spec.Args)
	}
}
