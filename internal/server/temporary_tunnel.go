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

	"autoto/internal/process"
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

type TemporaryTunnelSnapshot struct {
	Available   bool   `json:"available"`
	Installable bool   `json:"installable"`
	Status      string `json:"status"`
	PublicURL   string `json:"publicUrl,omitempty"`
	Error       string `json:"error,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
}

type temporaryTunnelProcess interface {
	Start() error
	StdoutPipe() (io.ReadCloser, error)
	StderrPipe() (io.ReadCloser, error)
	Wait() error
	Interrupt() error
	Kill() error
}

type temporaryTunnelCommand func(context.Context, string, ...string) temporaryTunnelProcess
type temporaryTunnelLookPath func(string) (string, error)

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

func defaultTemporaryTunnelCommand(ctx context.Context, name string, args ...string) temporaryTunnelProcess {
	command := exec.CommandContext(ctx, name, args...)
	process.HideWindow(command)
	command.Env = append(os.Environ(), "NO_COLOR=1")
	return &execTemporaryTunnelProcess{command: command}
}

type temporaryTunnelProcessState struct {
	process       temporaryTunnelProcess
	cancel        context.CancelFunc
	done          chan error
	url           chan string
	stopRequested bool
}

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
}

func NewTemporaryTunnelManager(bindAddress, homeDir string) *TemporaryTunnelManager {
	return newTemporaryTunnelManager(bindAddress, temporaryTunnelOptions{installer: newGitHubCloudflaredInstaller(homeDir)})
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
	}
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
	m.status = temporaryTunnelStarting
	m.publicURL = ""
	m.errorMessage = ""
	m.startedAt = time.Time{}
	m.startedAddress = address
	binaryPath := m.binaryPath
	command := m.command
	timeout := m.startTimeout
	m.mu.Unlock()

	processContext, cancel := context.WithCancel(context.Background())
	process := command(processContext, binaryPath, "--config", os.DevNull, "tunnel", "--no-autoupdate", "--url", "http://127.0.0.1:"+strconv.Itoa(port))
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
	go scanTemporaryTunnelOutput(stdout, running.url)
	go scanTemporaryTunnelOutput(stderr, running.url)
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
		err := errors.New("cloudflared did not expose a temporary URL before the startup timeout")
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

func scanTemporaryTunnelOutput(reader io.ReadCloser, urls chan<- string) {
	defer reader.Close()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024), 64*1024)
	for scanner.Scan() {
		if publicURL := parseCloudflareQuickTunnelURL(scanner.Text()); publicURL != "" {
			select {
			case urls <- publicURL:
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
