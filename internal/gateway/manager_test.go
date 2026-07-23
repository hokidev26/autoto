package gateway

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
)

func TestManagerDisabledStartDoesNotBindAndCanEnableLater(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := occupied.Addr().(*net.TCPAddr).Port
	manager := newGatewayManagerForTest(t, config.GatewayConfig{
		Enabled:              false,
		Host:                 "127.0.0.1",
		Port:                 port,
		MaxGlobalConcurrency: 4,
		MaxRequestBytes:      1 << 20,
	}, false)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := manager.Status(); status.Running || status.Address != "" || status.DesiredEnabled {
		t.Fatalf("disabled manager unexpectedly bound a listener: %+v", status)
	}
	if err := occupied.Close(); err != nil {
		t.Fatal(err)
	}

	next := manager.cfg
	next.Enabled = true
	if err := manager.Reconfigure(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	status := manager.Status()
	if !status.Running || !status.DesiredEnabled || status.Address == "" {
		t.Fatalf("enabled manager did not start: %+v", status)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("second close failed: %v", err)
	}
}

func TestManagerEphemeralIsolationForcesLoopbackPortZeroAndHotSwapsService(t *testing.T) {
	cfg := config.GatewayConfig{
		Enabled:              true,
		Host:                 "0.0.0.0",
		Port:                 18789,
		AllowRemote:          true,
		MaxGlobalConcurrency: 4,
		MaxRequestBytes:      1 << 20,
	}
	manager := newGatewayManagerForTest(t, cfg, true)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := manager.Status()
	if !first.Running || first.AllowRemote || !first.EphemeralIsolation {
		t.Fatalf("unexpected ephemeral status: %+v", first)
	}
	host, port, err := net.SplitHostPort(first.Address)
	if err != nil {
		t.Fatal(err)
	}
	if host != "127.0.0.1" || port == "0" || port == "18789" {
		t.Fatalf("ephemeral manager did not force 127.0.0.1:0: %q", first.Address)
	}

	next := cfg
	next.MaxRequestBytes = 2 << 20
	if err := manager.Reconfigure(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	second := manager.Status()
	if !second.Running || second.Address != first.Address {
		t.Fatalf("service-only reconfiguration replaced the listener: before=%+v after=%+v", first, second)
	}

	next.Enabled = false
	if err := manager.Reconfigure(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if status := manager.Status(); status.Running || status.Address != "" || status.DesiredEnabled {
		t.Fatalf("disabled manager retained a listener: %+v", status)
	}
}

func TestManagerBindFailurePreservesOldListener(t *testing.T) {
	cfg := config.GatewayConfig{
		Enabled:              true,
		Host:                 "127.0.0.1",
		Port:                 0,
		MaxGlobalConcurrency: 4,
		MaxRequestBytes:      1 << 20,
	}
	manager := newGatewayManagerForTest(t, cfg, false)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	old := manager.Status()

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	next := cfg
	next.Port = occupied.Addr().(*net.TCPAddr).Port
	if err := manager.Reconfigure(context.Background(), next); !IsBindError(err) {
		t.Fatalf("expected bind error, got %v", err)
	}
	status := manager.Status()
	if !status.Running || status.Address != old.Address || status.LastError == "" {
		t.Fatalf("old listener was not preserved after bind failure: before=%+v after=%+v", old, status)
	}
	connection, err := net.DialTimeout("tcp", status.Address, time.Second)
	if err != nil {
		t.Fatalf("preserved listener is unreachable: %v", err)
	}
	_ = connection.Close()
}

func TestManagerUnexpectedServeExitOnlyStopsGateway(t *testing.T) {
	manager := newGatewayManagerForTest(t, config.GatewayConfig{
		Enabled:              true,
		Host:                 "127.0.0.1",
		Port:                 0,
		MaxGlobalConcurrency: 4,
		MaxRequestBytes:      1 << 20,
	}, false)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	listener := manager.active.listener
	manager.mu.Unlock()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := manager.Status()
		if !status.Running {
			if !status.DesiredEnabled || status.LastError == "" {
				t.Fatalf("unexpected status after serve exit: %+v", status)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("manager did not observe unexpected Serve exit")
}

func TestManagerStatusDoesNotDeadlockDuringGracefulShutdown(t *testing.T) {
	manager := newGatewayManagerForTest(t, config.GatewayConfig{
		Enabled:              true,
		Host:                 "127.0.0.1",
		Port:                 0,
		MaxGlobalConcurrency: 4,
		MaxRequestBytes:      1 << 20,
	}, false)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	manager.mu.Lock()
	manager.active.handler.Set(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(requestStarted)
		<-releaseRequest
		w.WriteHeader(http.StatusNoContent)
	}))
	address := manager.active.actualAddress
	manager.mu.Unlock()

	requestDone := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + address)
		if response != nil {
			_ = response.Body.Close()
		}
		requestDone <- err
	}()
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("test request did not reach Gateway handler")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close(shutdownCtx) }()
	listenerClosedDeadline := time.Now().Add(time.Second)
	for {
		connection, err := net.DialTimeout("tcp", address, 25*time.Millisecond)
		if err != nil {
			break
		}
		_ = connection.Close()
		if time.Now().After(listenerClosedDeadline) {
			t.Fatal("Gateway listener did not begin graceful shutdown")
		}
	}

	statusDone := make(chan ManagerStatus, 1)
	go func() { statusDone <- manager.Status() }()
	select {
	case status := <-statusDone:
		if status.Running {
			t.Fatalf("closing Gateway still reported as running: %+v", status)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Gateway status blocked behind graceful shutdown")
	}

	close(releaseRequest)
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
}

func newGatewayManagerForTest(t *testing.T, cfg config.GatewayConfig, ephemeral bool) *Manager {
	t.Helper()
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(ManagerOptions{
		Store:              store,
		Registry:           providers.NewRegistry(),
		Config:             cfg,
		EphemeralIsolation: ephemeral,
	})
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = manager.Close(context.Background())
		_ = store.Close()
	})
	return manager
}
