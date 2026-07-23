package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
)

// ManagerOptions contains the stable dependencies used to rebuild the Gateway
// service when its runtime configuration changes.
type ManagerOptions struct {
	Store              *db.Store
	Registry           *providers.Registry
	ProviderAllowed    ProviderPolicy
	Config             config.GatewayConfig
	EphemeralIsolation bool
	Logger             *slog.Logger
}

// ManagerStatus is the live Gateway state returned by the management API.
type ManagerStatus struct {
	Address            string `json:"address,omitempty"`
	Running            bool   `json:"running"`
	DesiredEnabled     bool   `json:"desiredEnabled"`
	AllowRemote        bool   `json:"allowRemote"`
	LastError          string `json:"lastError,omitempty"`
	EphemeralIsolation bool   `json:"ephemeralIsolation"`
}

// BindError identifies listener failures so API callers can report a conflict
// without exposing platform-specific socket errors.
type BindError struct {
	Address string
	Err     error
}

func (e *BindError) Error() string {
	if e == nil {
		return "gateway bind failed"
	}
	return fmt.Sprintf("listen on gateway %s: %v", e.Address, e.Err)
}

func (e *BindError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func IsBindError(err error) bool {
	var bindErr *BindError
	return errors.As(err, &bindErr)
}

type switchHandler struct {
	mu      sync.RWMutex
	handler http.Handler
}

func (h *switchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	handler := h.handler
	h.mu.RUnlock()
	if handler == nil {
		http.Error(w, "Gateway service unavailable", http.StatusServiceUnavailable)
		return
	}
	handler.ServeHTTP(w, r)
}

func (h *switchHandler) Set(handler http.Handler) {
	h.mu.Lock()
	h.handler = handler
	h.mu.Unlock()
}

type managedServer struct {
	cfg           config.GatewayConfig
	listenAddress string
	actualAddress string
	listener      net.Listener
	server        *http.Server
	handler       *switchHandler
	stopping      bool
}

// Manager owns the optional Gateway listener. It always exists, even when the
// configured Gateway is disabled, so enabling it never requires restarting the
// main Autoto HTTP runtime.
type Manager struct {
	opMu sync.Mutex
	mu   sync.Mutex

	store           *db.Store
	registry        *providers.Registry
	providerAllowed ProviderPolicy
	logger          *slog.Logger
	ephemeral       bool

	cfg     config.GatewayConfig
	started bool
	closed  bool
	active  *managedServer
	lastErr string
}

func NewManager(options ManagerOptions) (*Manager, error) {
	if options.Store == nil {
		return nil, errors.New("gateway manager store is required")
	}
	if options.Registry == nil {
		return nil, errors.New("gateway manager provider registry is required")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		store:           options.Store,
		registry:        options.Registry,
		providerAllowed: options.ProviderAllowed,
		logger:          logger,
		ephemeral:       options.EphemeralIsolation,
		cfg:             options.Config,
	}, nil
}

// Start applies the desired configuration. A disabled Manager starts without
// binding any port.
func (m *Manager) Start(ctx context.Context) error {
	if m == nil {
		return errors.New("gateway manager is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("gateway manager is closed")
	}
	if m.started {
		m.mu.Unlock()
		return errors.New("gateway manager is already started")
	}
	m.started = true
	cfg := m.cfg
	m.mu.Unlock()

	if !cfg.Enabled {
		m.setLastError("")
		return nil
	}
	managed, err := m.prepareManagedServer(cfg)
	if err != nil {
		m.mu.Lock()
		m.started = false
		m.lastErr = err.Error()
		m.mu.Unlock()
		return err
	}
	m.mu.Lock()
	m.activateLocked(managed)
	m.lastErr = ""
	m.mu.Unlock()
	return nil
}

// Close stops the current Gateway listener. It is safe to call repeatedly.
func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	managed := m.detachActiveLocked()
	m.mu.Unlock()
	return shutdownManagedServer(ctx, managed)
}

// Reconfigure applies a new desired configuration. Service-only changes swap
// handlers without releasing the listener. Address changes pre-bind the new
// listener whenever possible; if an overlapping bind requires releasing the old
// listener and the new bind still fails, Manager attempts to restore the old one.
func (m *Manager) Reconfigure(ctx context.Context, next config.GatewayConfig) error {
	if m == nil {
		return errors.New("gateway manager is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("gateway manager is closed")
	}
	if !m.started {
		m.cfg = next
		m.mu.Unlock()
		return nil
	}
	oldCfg := m.cfg
	old := m.active
	m.mu.Unlock()

	if !next.Enabled {
		m.mu.Lock()
		managed := m.detachActiveLocked()
		m.mu.Unlock()
		if err := shutdownManagedServer(ctx, managed); err != nil {
			m.setLastError(err.Error())
			return err
		}
		m.mu.Lock()
		m.cfg = next
		m.lastErr = ""
		m.mu.Unlock()
		return nil
	}

	if old != nil && old.listenAddress == m.listenAddress(next) {
		service, err := m.newService(next)
		if err != nil {
			m.setLastError(err.Error())
			return err
		}
		m.mu.Lock()
		if m.active == old {
			old.handler.Set(service.Handler())
			m.cfg = next
			m.lastErr = ""
		}
		m.mu.Unlock()
		return nil
	}

	if old == nil {
		prepared, err := m.prepareManagedServer(next)
		if err != nil {
			m.setLastError(err.Error())
			return err
		}
		m.mu.Lock()
		m.activateLocked(prepared)
		m.cfg = next
		m.lastErr = ""
		m.mu.Unlock()
		return nil
	}

	if !sameFixedPort(old.listenAddress, m.listenAddress(next)) {
		prepared, err := m.prepareManagedServer(next)
		if err != nil {
			m.setLastError(err.Error())
			return err
		}
		m.mu.Lock()
		m.activateLocked(prepared)
		m.cfg = next
		m.lastErr = ""
		old.stopping = true
		m.mu.Unlock()
		_ = shutdownManagedServer(ctx, old)
		return nil
	}

	// The same fixed TCP port cannot be pre-bound. Stop the old listener, bind the
	// replacement, and restore the previous configuration if replacement fails.
	m.mu.Lock()
	managed := m.detachActiveLocked()
	m.mu.Unlock()
	if err := shutdownManagedServer(ctx, managed); err != nil {
		m.setLastError(err.Error())
		return err
	}
	prepared, replaceErr := m.prepareManagedServer(next)
	if replaceErr == nil {
		m.mu.Lock()
		m.activateLocked(prepared)
		m.cfg = next
		m.lastErr = ""
		m.mu.Unlock()
		return nil
	}
	rolledBack, restoreErr := m.prepareManagedServer(oldCfg)
	if restoreErr != nil {
		combined := fmt.Sprintf("%v; gateway rollback failed: %v", replaceErr, restoreErr)
		m.setLastError(combined)
		return fmt.Errorf("gateway reconfiguration failed: %w (rollback failed: %v)", replaceErr, restoreErr)
	}
	m.mu.Lock()
	m.activateLocked(rolledBack)
	m.cfg = oldCfg
	m.lastErr = replaceErr.Error()
	m.mu.Unlock()
	return replaceErr
}

func (m *Manager) Status() ManagerStatus {
	if m == nil {
		return ManagerStatus{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	status := ManagerStatus{
		DesiredEnabled:     m.cfg.Enabled,
		AllowRemote:        m.cfg.AllowRemote && !m.ephemeral,
		LastError:          m.lastErr,
		EphemeralIsolation: m.ephemeral,
	}
	if m.active != nil {
		status.Address = m.active.actualAddress
		status.Running = true
	}
	return status
}

func (m *Manager) prepareManagedServer(cfg config.GatewayConfig) (*managedServer, error) {
	service, err := m.newService(cfg)
	if err != nil {
		return nil, err
	}
	listenAddress := m.listenAddress(cfg)
	listener, err := listenGateway(listenAddress)
	if err != nil {
		return nil, err
	}
	handler := &switchHandler{handler: service.Handler()}
	managed := &managedServer{
		cfg:           cfg,
		listenAddress: listenAddress,
		actualAddress: listener.Addr().String(),
		listener:      listener,
		handler:       handler,
	}
	managed.server = &http.Server{
		Addr:              managed.actualAddress,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	return managed, nil
}

func (m *Manager) activateLocked(managed *managedServer) {
	m.active = managed
	go m.serve(managed)
}

func (m *Manager) detachActiveLocked() *managedServer {
	managed := m.active
	m.active = nil
	if managed != nil {
		managed.stopping = true
	}
	return managed
}

func (m *Manager) setLastError(message string) {
	m.mu.Lock()
	m.lastErr = message
	m.mu.Unlock()
}

func (m *Manager) serve(managed *managedServer) {
	err := managed.server.Serve(managed.listener)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != managed || managed.stopping {
		return
	}
	m.active = nil
	if err != nil {
		m.lastErr = err.Error()
		m.logger.Error("private API gateway stopped unexpectedly", "addr", managed.actualAddress, "error", err)
	} else {
		m.lastErr = "gateway listener stopped unexpectedly"
		m.logger.Error("private API gateway stopped unexpectedly", "addr", managed.actualAddress)
	}
}

func shutdownManagedServer(ctx context.Context, managed *managedServer) error {
	if managed == nil {
		return nil
	}
	err := managed.server.Shutdown(ctx)
	if err != nil {
		closeErr := managed.server.Close()
		return errors.Join(err, closeErr)
	}
	return nil
}

func (m *Manager) newService(cfg config.GatewayConfig) (*Service, error) {
	return New(m.store, m.registry, Options{
		MaxGlobalConcurrency:         cfg.MaxGlobalConcurrency,
		MaxRequestBytes:              cfg.MaxRequestBytes,
		ProviderAllowed:              m.providerAllowed,
		AllowSubscriptionCredentials: true,
	})
}

func (m *Manager) listenAddress(cfg config.GatewayConfig) string {
	if m.ephemeral {
		return "127.0.0.1:0"
	}
	return net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
}

func listenGateway(address string) (net.Listener, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, &BindError{Address: address, Err: err}
	}
	return listener, nil
}

func sameFixedPort(first, second string) bool {
	_, firstPort, firstErr := net.SplitHostPort(first)
	_, secondPort, secondErr := net.SplitHostPort(second)
	return firstErr == nil && secondErr == nil && firstPort != "0" && firstPort == secondPort
}
