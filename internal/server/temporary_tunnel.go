package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"autoto/internal/config"
	"autoto/internal/process"
	"autoto/internal/secrets"
)

const (
	temporaryTunnelBinary      = "cloudflared"
	temporaryTunnelIdle        = "idle"
	temporaryTunnelInstalling  = "installing"
	temporaryTunnelStarting    = "starting"
	temporaryTunnelRunning     = "running"
	temporaryTunnelStopping    = "stopping"
	temporaryTunnelUnavailable = "unavailable"
	temporaryTunnelError       = "error"
)

var cloudflareQuickTunnelURL = regexp.MustCompile(`https://[a-z0-9][a-z0-9-]*\.trycloudflare\.com(?:/[^\s"'<>]*)?`)

// cloudflareNamedTunnelReady matches the line cloudflared logs once an edge
// connection is live. A named tunnel never prints a public URL, because its
// hostname was chosen in advance, so this is the only available readiness
// signal. Waiting for it rather than assuming success keeps a failed tunnel
// from being reported as running.
var cloudflareNamedTunnelReady = regexp.MustCompile(`Registered tunnel connection`)

const (
	temporaryTunnelModeQuick = "quick"
	temporaryTunnelModeNamed = "named"
)

type TemporaryTunnelSnapshot struct {
	Available   bool   `json:"available"`
	Installable bool   `json:"installable"`
	Status      string `json:"status"`
	PublicURL   string `json:"publicUrl,omitempty"`
	Error       string `json:"error,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	// Mode tells the client which kind of tunnel this is. The two behave
	// differently enough to matter: a quick tunnel's URL changes on every start
	// and carries no uptime guarantee, a named tunnel's hostname is stable.
	Mode string `json:"mode"`
	// NamedConfigured reports whether a named tunnel could be started. It is
	// derived from configuration, never from the token value, so it is safe to
	// return to a client.
	NamedConfigured bool `json:"namedConfigured"`
}

type temporaryTunnelProcess interface {
	Start() error
	StdoutPipe() (io.ReadCloser, error)
	StderrPipe() (io.ReadCloser, error)
	Wait() error
	Interrupt() error
	Kill() error
}

// temporaryTunnelSpec is the full description of the child process. Environment
// is part of the spec rather than a separate argument because the named tunnel
// token must travel in the environment: process listings on every supported
// platform expose argv, so a token passed as --token would be readable by any
// local process.
type temporaryTunnelSpec struct {
	Args []string
	// Env holds additional KEY=VALUE entries merged over the parent environment.
	// It may contain credential material and must never be logged or surfaced.
	Env []string
}

type temporaryTunnelCommand func(context.Context, string, temporaryTunnelSpec) temporaryTunnelProcess
type temporaryTunnelLookPath func(string) (string, error)

// namedTunnelSettings is the manager's view of the configured named tunnel. The
// token is resolved fresh at start time rather than cached, so rotating the
// environment value takes effect on the next start without a restart.
type namedTunnelSettings struct {
	Hostname string
	Token    string
}

// temporaryTunnelNamedResolver reports the named tunnel to start. Returning a
// zero value with no error means none is configured, which selects the quick
// tunnel instead. An error means a named tunnel was configured but unusable,
// and that must fail the start rather than silently downgrade: falling back to
// a quick tunnel would publish a different, unexpected hostname.
//
// This runs only at start time. It resolves credential material, so it is
// deliberately not called on the status path, which is polled.
type temporaryTunnelNamedResolver func(context.Context) (namedTunnelSettings, error)

// temporaryTunnelNamedProbe reports whether a named tunnel is configured without
// resolving its token. Status is polled frequently, and a probe that touched the
// environment on every poll would turn a display concern into repeated credential
// access.
type temporaryTunnelNamedProbe func() bool

// temporaryTunnelAddressResolver reports the address a tunnel should point at.
// An empty return means there is nothing listening yet, which is a refusal to
// start rather than an error in the tunnel itself.
type temporaryTunnelAddressResolver func() (string, error)

type temporaryTunnelOptions struct {
	lookPath        temporaryTunnelLookPath
	command         temporaryTunnelCommand
	installer       temporaryTunnelInstaller
	startTimeout    time.Duration
	addressResolver temporaryTunnelAddressResolver
	namedResolver   temporaryTunnelNamedResolver
	namedProbe      temporaryTunnelNamedProbe
}

type execTemporaryTunnelProcess struct {
	command *exec.Cmd
}

func (p *execTemporaryTunnelProcess) Start() error {
	return p.command.Start()
}

func (p *execTemporaryTunnelProcess) StdoutPipe() (io.ReadCloser, error) {
	return p.command.StdoutPipe()
}

func (p *execTemporaryTunnelProcess) StderrPipe() (io.ReadCloser, error) {
	return p.command.StderrPipe()
}

func (p *execTemporaryTunnelProcess) Wait() error {
	return p.command.Wait()
}

func (p *execTemporaryTunnelProcess) Interrupt() error {
	if p.command.Process == nil {
		return errors.New("cloudflared process has not started")
	}
	return p.command.Process.Signal(os.Interrupt)
}

func (p *execTemporaryTunnelProcess) Kill() error {
	if p.command.Process == nil {
		return errors.New("cloudflared process has not started")
	}
	return p.command.Process.Kill()
}

// resolveNamedTunnel asks the resolver for a named tunnel and validates what it
// returns. A resolver that reports a hostname without a token, or the reverse,
// is a bug rather than a configuration state, and starting on it would either
// publish nothing or authenticate as the wrong tunnel.
func resolveNamedTunnel(ctx context.Context, resolver temporaryTunnelNamedResolver) (namedTunnelSettings, error) {
	if resolver == nil {
		return namedTunnelSettings{}, nil
	}
	named, err := resolver(ctx)
	if err != nil {
		return namedTunnelSettings{}, err
	}
	named.Hostname = strings.TrimSpace(named.Hostname)
	named.Token = strings.TrimSpace(named.Token)
	if named.Hostname == "" && named.Token == "" {
		return namedTunnelSettings{}, nil
	}
	if named.Hostname == "" || named.Token == "" {
		// The error text deliberately names neither value.
		return namedTunnelSettings{}, errors.New("named tunnel is incompletely configured")
	}
	return named, nil
}

// AttachNamedTunnel connects a manager to live named-tunnel configuration.
//
// select must read from the server's current configuration rather than from a
// captured copy. Configuration is held by value in several places, so a closure
// over one of those copies would keep resolving the hostname and token reference
// that were present at wiring time and silently ignore every later edit.
//
// The token is resolved from the environment at start time and handed straight to
// the child process. It is never stored on the manager, so it cannot be read back
// out through a snapshot or leak into a later start, and rotating the environment
// value takes effect on the next start without restarting Autoto.
func (s *Server) AttachNamedTunnel(manager *TemporaryTunnelManager, selectTunnel func(config.Config) config.NamedTunnelConfig) {
	if s == nil || manager == nil || selectTunnel == nil {
		return
	}
	load := func() config.NamedTunnelConfig { return selectTunnel(s.configSnapshot()) }
	attachNamedTunnel(manager, load)
}

func attachNamedTunnel(manager *TemporaryTunnelManager, load func() config.NamedTunnelConfig) {
	if manager == nil || load == nil {
		return
	}
	resolver := func(ctx context.Context) (namedTunnelSettings, error) {
		settings := load()
		if !settings.Configured() {
			return namedTunnelSettings{}, nil
		}
		// ResolveString validates the reference and resolves it without ever
		// including the resolved value in an error.
		token, err := secrets.ResolveString(ctx, secrets.EnvResolver{}, settings.TokenRef)
		if err != nil {
			return namedTunnelSettings{}, fmt.Errorf("named tunnel token is unavailable: %w", err)
		}
		if strings.TrimSpace(token) == "" {
			return namedTunnelSettings{}, errors.New("named tunnel token is empty")
		}
		return namedTunnelSettings{Hostname: settings.Hostname, Token: token}, nil
	}
	// The probe reads configuration only. Status is polled, and resolving a
	// credential on every poll would be unnecessary access to secret material.
	probe := func() bool { return load().Configured() }
	manager.SetNamedTunnelResolver(resolver, probe)
}

// namedPublicURL builds the origin a named tunnel will serve. cloudflared only
// terminates TLS at the Cloudflare edge, so the public scheme is always https
// regardless of the plaintext hop to the local listener.
func namedPublicURL(named namedTunnelSettings) string {
	if named.Hostname == "" {
		return ""
	}
	return "https://" + named.Hostname
}

// temporaryTunnelProcessSpec builds the cloudflared invocation for either mode.
//
// Two decisions are load-bearing:
//
//   - --config os.DevNull is kept for both modes. It isolates the child from any
//     cloudflared configuration already on the machine, so a stray local config
//     cannot redirect Autoto's tunnel somewhere else.
//   - --url is always passed. Without it a token-run tunnel takes its ingress
//     from the dashboard, and if no ingress rule exists cloudflared answers every
//     request with 503 while still reporting a healthy connection.
//
// The token goes in the environment via TUNNEL_TOKEN, never argv, because argv is
// world-readable through process listings.
func temporaryTunnelProcessSpec(port int, named namedTunnelSettings) temporaryTunnelSpec {
	args := []string{"--config", os.DevNull, "tunnel", "--no-autoupdate"}
	if named.Hostname != "" {
		// "run" with no tunnel argument uses the token from the environment.
		args = append(args, "run", "--url", "http://127.0.0.1:"+strconv.Itoa(port))
		return temporaryTunnelSpec{Args: args, Env: []string{"TUNNEL_TOKEN=" + named.Token}}
	}
	return temporaryTunnelSpec{Args: append(args, "--url", "http://127.0.0.1:"+strconv.Itoa(port))}
}

func defaultTemporaryTunnelCommand(ctx context.Context, name string, spec temporaryTunnelSpec) temporaryTunnelProcess {
	command := exec.CommandContext(ctx, name, spec.Args...)
	process.HideWindow(command)
	command.Env = append(os.Environ(), "NO_COLOR=1")
	command.Env = append(command.Env, spec.Env...)
	return &execTemporaryTunnelProcess{command: command}
}

type temporaryTunnelProcessState struct {
	process       temporaryTunnelProcess
	cancel        context.CancelFunc
	done          chan error
	url           chan string
	stopRequested bool
}

// ready reports the channel that signals a usable tunnel. Both modes converge on
// the same channel so the start path has a single wait: a quick tunnel sends the
// scraped URL, a named tunnel sends its configured hostname once an edge
// connection registers.
func (s *temporaryTunnelProcessState) ready() <-chan string { return s.url }

type TemporaryTunnelManager struct {
	mu sync.Mutex
	// bindAddress is fixed for the tunnel that fronts Autoto's own listener.
	// The gateway tunnel leaves it empty and supplies addressResolver instead,
	// because the gateway's listener can move: it is bound separately, can be
	// switched off, and can be reconfigured onto another host or port.
	bindAddress     string
	addressResolver temporaryTunnelAddressResolver
	// startedAddress records what the running process was pointed at, so a
	// later gateway move can be detected rather than silently breaking the
	// public URL.
	startedAddress string
	binaryPath     string
	available      bool
	availableErr   string
	status         string
	publicURL      string
	errorMessage   string
	startedAt      time.Time
	process        *temporaryTunnelProcessState
	lookPath       temporaryTunnelLookPath
	command        temporaryTunnelCommand
	installer      temporaryTunnelInstaller
	startTimeout   time.Duration
	namedResolver  temporaryTunnelNamedResolver
	namedProbe     temporaryTunnelNamedProbe
	// mode records which kind of tunnel is running or last ran. It is reported to
	// clients so a stable named hostname is not mistaken for a throwaway one.
	mode string
}

func NewTemporaryTunnelManager(bindAddress, homeDir string) *TemporaryTunnelManager {
	return newTemporaryTunnelManager(bindAddress, temporaryTunnelOptions{installer: newGitHubCloudflaredInstaller(homeDir)})
}

// SetNamedTunnelResolver installs the named tunnel lookup. It is separate from
// construction because configuration is loaded after the managers are built, and
// because the resolver reads live configuration on every start so a rotated
// token or a changed hostname applies without restarting Autoto.
func (m *TemporaryTunnelManager) SetNamedTunnelResolver(resolver temporaryTunnelNamedResolver, probe temporaryTunnelNamedProbe) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.namedResolver = resolver
	m.namedProbe = probe
}

// NewResolvedTunnelManager builds a manager whose target is resolved each time
// it starts. The shared API gateway owns a listener of its own, so its address
// is only known at start time and can change afterwards.
func NewResolvedTunnelManager(resolver temporaryTunnelAddressResolver, homeDir string) *TemporaryTunnelManager {
	return newTemporaryTunnelManager("", temporaryTunnelOptions{
		installer:       newGitHubCloudflaredInstaller(homeDir),
		addressResolver: resolver,
	})
}

func newTemporaryTunnelManager(bindAddress string, options temporaryTunnelOptions) *TemporaryTunnelManager {
	lookPath := options.lookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	command := options.command
	if command == nil {
		command = defaultTemporaryTunnelCommand
	}
	timeout := options.startTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	manager := &TemporaryTunnelManager{
		bindAddress:     strings.TrimSpace(bindAddress),
		addressResolver: options.addressResolver,
		lookPath:        lookPath,
		command:         command,
		installer:       options.installer,
		startTimeout:    timeout,
		status:          temporaryTunnelUnavailable,
		namedResolver:   options.namedResolver,
		namedProbe:      options.namedProbe,
		mode:            temporaryTunnelModeQuick,
	}
	manager.refreshAvailabilityLocked()
	return manager
}

func (m *TemporaryTunnelManager) Start(context.Context) error {
	return nil
}

func (m *TemporaryTunnelManager) Close(ctx context.Context) error {
	_, err := m.StopTunnel(ctx)
	return err
}

func (m *TemporaryTunnelManager) refreshAvailabilityLocked() {
	if m.available || m.process != nil || m.status == temporaryTunnelInstalling {
		return
	}
	if binaryPath, err := m.lookPath(temporaryTunnelBinary); err == nil && strings.TrimSpace(binaryPath) != "" {
		m.binaryPath = binaryPath
		m.available = true
		m.availableErr = ""
		m.errorMessage = ""
		m.status = temporaryTunnelIdle
		return
	}
	if m.installer != nil {
		managedPath := m.installer.ManagedPath()
		if validCloudflaredBinary(managedPath, runtime.GOOS) {
			m.binaryPath = managedPath
			m.available = true
			m.availableErr = ""
			m.errorMessage = ""
			m.status = temporaryTunnelIdle
			return
		}
	}
	m.binaryPath = ""
	m.available = false
	if m.availableErr == "" {
		m.availableErr = "cloudflared is not installed or is not available in PATH"
	}
	if m.status != temporaryTunnelError {
		m.status = temporaryTunnelUnavailable
	}
}

// targetAddressLocked reports where the tunnel should point. A resolver that
// returns nothing means the service being fronted is not listening, which is
// reported as a refusal the user can act on rather than a malformed address.
func (m *TemporaryTunnelManager) targetAddressLocked() (string, error) {
	if m.addressResolver == nil {
		return m.bindAddress, nil
	}
	address, err := m.addressResolver()
	if err != nil {
		return "", err
	}
	address = strings.TrimSpace(address)
	if address == "" {
		return "", errors.New("the shared API is not running, so there is no address to expose")
	}
	return address, nil
}

// RevalidateAddress stops a running tunnel whose target has moved.
//
// The gateway rebinds when it is switched off or reconfigured onto another host
// or port, and cloudflared keeps forwarding to the address it was given at
// start. Without this the public URL stays up while every request behind it
// fails, which is both invisible from here and very hard to diagnose from the
// outside.
//
// Stopping is deliberate rather than restarting: a restart mints a different
// public hostname, so a URL already handed out would break anyway, and doing it
// silently is harder to understand than being told the tunnel closed.
func (m *TemporaryTunnelManager) RevalidateAddress(ctx context.Context) (TemporaryTunnelSnapshot, bool) {
	if m == nil || m.addressResolver == nil {
		return TemporaryTunnelSnapshot{}, false
	}
	m.mu.Lock()
	if m.process == nil || m.startedAddress == "" {
		m.mu.Unlock()
		return m.Snapshot(), false
	}
	current, err := m.targetAddressLocked()
	// A resolver failure is treated the same as a move: either way the recorded
	// address can no longer be trusted to be the live one.
	if err == nil && current == m.startedAddress {
		m.mu.Unlock()
		return m.Snapshot(), false
	}
	m.mu.Unlock()

	if _, stopErr := m.StopTunnel(ctx); stopErr != nil {
		m.mu.Lock()
		m.setErrorLocked(stopErr)
		snapshot := m.snapshotLocked()
		m.mu.Unlock()
		return snapshot, true
	}
	m.mu.Lock()
	m.startedAddress = ""
	m.status = temporaryTunnelError
	m.errorMessage = "The shared API address changed, so the public URL was closed. Start it again to get a new one."
	snapshot := m.snapshotLocked()
	m.mu.Unlock()
	return snapshot, true
}

func (m *TemporaryTunnelManager) Snapshot() TemporaryTunnelSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshAvailabilityLocked()
	return m.snapshotLocked()
}

func (m *TemporaryTunnelManager) snapshotLocked() TemporaryTunnelSnapshot {
	installable := !m.available && m.installer != nil && m.installer.Supported()
	snapshot := TemporaryTunnelSnapshot{
		Available:   m.available,
		Installable: installable,
		Status:      m.status,
		PublicURL:   m.publicURL,
		Error:       m.errorMessage,
		Mode:        m.mode,
	}
	if snapshot.Mode == "" {
		snapshot.Mode = temporaryTunnelModeQuick
	}
	// Reporting whether a named tunnel is configured lets the UI describe what a
	// start will actually do. Only the boolean crosses the boundary; the hostname
	// is already public once running, but the token reference is not exposed.
	snapshot.NamedConfigured = m.namedProbe != nil && m.namedProbe()
	if !m.available && snapshot.Error == "" {
		snapshot.Error = m.availableErr
	}
	if !m.startedAt.IsZero() {
		snapshot.StartedAt = m.startedAt.UTC().Format(time.RFC3339Nano)
	}
	return snapshot
}

func (m *TemporaryTunnelManager) InstallCloudflared(ctx context.Context) (TemporaryTunnelSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.process != nil {
		snapshot := m.snapshotLocked()
		m.mu.Unlock()
		return snapshot, errors.New("stop the temporary tunnel before installing cloudflared")
	}
	if m.status == temporaryTunnelInstalling {
		snapshot := m.snapshotLocked()
		m.mu.Unlock()
		return snapshot, errCloudflaredInstallInProgress
	}
	m.refreshAvailabilityLocked()
	if m.available {
		snapshot := m.snapshotLocked()
		m.mu.Unlock()
		return snapshot, nil
	}
	installer := m.installer
	if installer == nil || !installer.Supported() {
		snapshot := m.snapshotLocked()
		m.mu.Unlock()
		return snapshot, errCloudflaredInstallUnsupported
	}
	m.status = temporaryTunnelInstalling
	m.binaryPath = ""
	m.available = false
	m.availableErr = ""
	m.errorMessage = ""
	m.publicURL = ""
	m.startedAt = time.Time{}
	m.mu.Unlock()

	binaryPath, err := installer.Install(ctx)
	if err == nil && !validCloudflaredBinary(binaryPath, runtime.GOOS) {
		err = cloudflaredInstallFailure("the installed executable could not be verified")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.binaryPath = ""
		m.available = false
		m.status = temporaryTunnelError
		m.errorMessage = err.Error()
		m.availableErr = err.Error()
		return m.snapshotLocked(), err
	}
	m.binaryPath = binaryPath
	m.available = true
	m.status = temporaryTunnelIdle
	m.errorMessage = ""
	m.availableErr = ""
	return m.snapshotLocked(), nil
}

func (m *TemporaryTunnelManager) StartTunnel(ctx context.Context) (TemporaryTunnelSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.process != nil {
		snapshot := m.snapshotLocked()
		m.mu.Unlock()
		return snapshot, nil
	}
	if m.status == temporaryTunnelInstalling {
		snapshot := m.snapshotLocked()
		m.mu.Unlock()
		return snapshot, errors.New("cloudflared installation is in progress")
	}
	m.refreshAvailabilityLocked()
	if !m.available {
		snapshot := m.snapshotLocked()
		m.mu.Unlock()
		if snapshot.Error == "" {
			return snapshot, errors.New("cloudflared is unavailable")
		}
		return snapshot, errors.New(snapshot.Error)
	}
	address, err := m.targetAddressLocked()
	if err != nil {
		m.setErrorLocked(err)
		m.mu.Unlock()
		return m.Snapshot(), err
	}
	port, err := tunnelPort(address)
	if err != nil {
		m.setErrorLocked(err)
		m.mu.Unlock()
		return m.Snapshot(), err
	}
	namedResolver := m.namedResolver
	m.mu.Unlock()

	// Resolving the named tunnel happens outside the lock because it reads the
	// environment and must not be held across a start. A configured-but-broken
	// named tunnel fails the start instead of falling back to a quick tunnel,
	// which would publish an unexpected hostname.
	named, err := resolveNamedTunnel(ctx, namedResolver)
	if err != nil {
		m.mu.Lock()
		m.setErrorLocked(err)
		m.mu.Unlock()
		return m.Snapshot(), err
	}

	m.mu.Lock()
	m.status = temporaryTunnelStarting
	m.publicURL = ""
	m.errorMessage = ""
	m.startedAt = time.Time{}
	m.startedAddress = address
	m.mode = temporaryTunnelModeQuick
	if named.Hostname != "" {
		m.mode = temporaryTunnelModeNamed
	}
	binaryPath := m.binaryPath
	command := m.command
	timeout := m.startTimeout
	m.mu.Unlock()

	processContext, cancel := context.WithCancel(context.Background())
	process := command(processContext, binaryPath, temporaryTunnelProcessSpec(port, named))
	stdout, err := process.StdoutPipe()
	if err != nil {
		cancel()
		return m.failStart(err)
	}
	stderr, err := process.StderrPipe()
	if err != nil {
		_ = stdout.Close()
		cancel()
		return m.failStart(err)
	}
	running := &temporaryTunnelProcessState{
		process: process,
		cancel:  cancel,
		done:    make(chan error, 1),
		url:     make(chan string, 1),
	}
	m.mu.Lock()
	m.process = running
	m.mu.Unlock()
	if err := process.Start(); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		cancel()
		m.mu.Lock()
		if m.process == running {
			m.process = nil
			m.setErrorLocked(err)
		}
		m.mu.Unlock()
		return m.Snapshot(), err
	}
	go scanTemporaryTunnelOutput(stdout, running.url, namedPublicURL(named))
	go scanTemporaryTunnelOutput(stderr, running.url, namedPublicURL(named))
	go m.waitTemporaryTunnel(running)

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case publicURL := <-running.url:
		m.mu.Lock()
		if m.process == running {
			m.status = temporaryTunnelRunning
			m.publicURL = publicURL
			m.startedAt = time.Now().UTC()
		}
		snapshot := m.snapshotLocked()
		m.mu.Unlock()
		return snapshot, nil
	case err := <-running.done:
		if err == nil {
			err = errors.New("cloudflared exited before exposing a temporary tunnel")
		}
		return m.Snapshot(), err
	case <-timer.C:
		// The two modes fail for different reasons, and the distinction is what
		// makes the message actionable: a quick tunnel never got a URL, a named
		// tunnel never registered an edge connection, which usually means a
		// rejected token or a hostname that routes elsewhere.
		err := errors.New("cloudflared did not expose a temporary URL before the startup timeout")
		if named.Hostname != "" {
			err = errors.New("cloudflared did not register a named tunnel connection before the startup timeout")
		}
		_ = m.stopProcess(ctx, running)
		return m.failStart(err)
	case <-ctx.Done():
		_ = m.stopProcess(context.Background(), running)
		return m.Snapshot(), ctx.Err()
	}
}

func (m *TemporaryTunnelManager) StopTunnel(ctx context.Context) (TemporaryTunnelSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	running := m.process
	if running == nil {
		if m.status == temporaryTunnelInstalling {
			snapshot := m.snapshotLocked()
			m.mu.Unlock()
			return snapshot, nil
		}
		m.publicURL = ""
		m.startedAt = time.Time{}
		m.startedAddress = ""
		if m.available {
			m.status = temporaryTunnelIdle
		} else {
			m.status = temporaryTunnelUnavailable
		}
		snapshot := m.snapshotLocked()
		m.mu.Unlock()
		return snapshot, nil
	}
	running.stopRequested = true
	m.status = temporaryTunnelStopping
	m.mu.Unlock()
	return m.snapshotAfterStop(ctx, running)
}

func (m *TemporaryTunnelManager) snapshotAfterStop(ctx context.Context, running *temporaryTunnelProcessState) (TemporaryTunnelSnapshot, error) {
	if err := m.stopProcess(ctx, running); err != nil {
		return m.Snapshot(), err
	}
	return m.Snapshot(), nil
}

func (m *TemporaryTunnelManager) stopProcess(ctx context.Context, running *temporaryTunnelProcessState) error {
	if err := running.process.Interrupt(); err != nil {
		_ = running.process.Kill()
	}
	select {
	case <-running.done:
		return nil
	case <-ctx.Done():
		_ = running.process.Kill()
		select {
		case <-running.done:
			return ctx.Err()
		default:
			return ctx.Err()
		}
	}
}

func (m *TemporaryTunnelManager) waitTemporaryTunnel(running *temporaryTunnelProcessState) {
	err := running.process.Wait()
	running.cancel()
	m.mu.Lock()
	if m.process == running {
		m.process = nil
		m.publicURL = ""
		m.startedAt = time.Time{}
		m.startedAddress = ""
		if running.stopRequested {
			if m.available {
				m.status = temporaryTunnelIdle
			} else {
				m.status = temporaryTunnelUnavailable
			}
			m.errorMessage = ""
		} else {
			m.status = temporaryTunnelError
			if err != nil {
				m.errorMessage = "cloudflared stopped: " + err.Error()
			} else {
				m.errorMessage = "cloudflared stopped unexpectedly"
			}
		}
	}
	m.mu.Unlock()
	running.done <- err
}

func (m *TemporaryTunnelManager) failStart(err error) (TemporaryTunnelSnapshot, error) {
	m.mu.Lock()
	m.setErrorLocked(err)
	snapshot := m.snapshotLocked()
	m.mu.Unlock()
	return snapshot, err
}

func (m *TemporaryTunnelManager) setErrorLocked(err error) {
	m.process = nil
	m.publicURL = ""
	m.startedAt = time.Time{}
	m.status = temporaryTunnelError
	if err == nil {
		m.errorMessage = "temporary tunnel failed to start"
	} else {
		m.errorMessage = err.Error()
	}
}

// scanTemporaryTunnelOutput watches cloudflared's output for the readiness
// signal. When namedURL is empty this is a quick tunnel and the URL is scraped
// from the output. When it is set this is a named tunnel, whose hostname is
// already known, so the scanner instead waits for an edge connection to register
// and then reports the configured URL.
//
// Output lines are never forwarded anywhere else. cloudflared logs request URLs
// and headers at debug level, so treating its output as a data source to scan
// rather than a stream to surface keeps that out of Autoto's logs and API.
func scanTemporaryTunnelOutput(reader io.ReadCloser, urls chan<- string, namedURL string) {
	defer reader.Close()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024), 64*1024)
	for scanner.Scan() {
		line := scanner.Text()
		ready := ""
		if namedURL != "" {
			if cloudflareNamedTunnelReady.MatchString(line) {
				ready = namedURL
			}
		} else {
			ready = parseCloudflareQuickTunnelURL(line)
		}
		if ready != "" {
			select {
			case urls <- ready:
			default:
			}
		}
	}
}

func parseCloudflareQuickTunnelURL(output string) string {
	match := cloudflareQuickTunnelURL.FindString(output)
	return strings.TrimRight(match, ".,;:)")
}

func tunnelPort(address string) (int, error) {
	_, portText, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return 0, fmt.Errorf("invalid Autoto listen address %q: %w", address, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid Autoto listen port %q", portText)
	}
	return port, nil
}
