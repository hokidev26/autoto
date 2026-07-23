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

type temporaryTunnelOptions struct {
	lookPath     temporaryTunnelLookPath
	command      temporaryTunnelCommand
	installer    temporaryTunnelInstaller
	startTimeout time.Duration
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
	mu           sync.Mutex
	bindAddress  string
	binaryPath   string
	available    bool
	availableErr string
	status       string
	publicURL    string
	errorMessage string
	startedAt    time.Time
	process      *temporaryTunnelProcessState
	lookPath     temporaryTunnelLookPath
	command      temporaryTunnelCommand
	installer    temporaryTunnelInstaller
	startTimeout time.Duration
}

func NewTemporaryTunnelManager(bindAddress, homeDir string) *TemporaryTunnelManager {
	return newTemporaryTunnelManager(bindAddress, temporaryTunnelOptions{installer: newGitHubCloudflaredInstaller(homeDir)})
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
		bindAddress:  strings.TrimSpace(bindAddress),
		lookPath:     lookPath,
		command:      command,
		installer:    options.installer,
		startTimeout: timeout,
		status:       temporaryTunnelUnavailable,
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
	port, err := tunnelPort(m.bindAddress)
	if err != nil {
		m.setErrorLocked(err)
		m.mu.Unlock()
		return m.Snapshot(), err
	}
	m.status = temporaryTunnelStarting
	m.publicURL = ""
	m.errorMessage = ""
	m.startedAt = time.Time{}
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
