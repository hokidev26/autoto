package server

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeTemporaryTunnelProcess struct {
	stdoutReader *io.PipeReader
	stdoutWriter *io.PipeWriter
	stderrReader *io.PipeReader
	stderrWriter *io.PipeWriter
	waitDone     chan struct{}
	waitOnce     sync.Once
	startErr     error
	waitErr      error
}

func newFakeTemporaryTunnelProcess() *fakeTemporaryTunnelProcess {
	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()
	return &fakeTemporaryTunnelProcess{
		stdoutReader: stdoutReader,
		stdoutWriter: stdoutWriter,
		stderrReader: stderrReader,
		stderrWriter: stderrWriter,
		waitDone:     make(chan struct{}),
	}
}

func (p *fakeTemporaryTunnelProcess) Start() error {
	if p.startErr != nil {
		return p.startErr
	}
	go func() {
		_, _ = io.WriteString(p.stdoutWriter, "INF temporary tunnel available at https://example.trycloudflare.com\n")
	}()
	return nil
}

func (p *fakeTemporaryTunnelProcess) StdoutPipe() (io.ReadCloser, error) {
	return p.stdoutReader, nil
}

func (p *fakeTemporaryTunnelProcess) StderrPipe() (io.ReadCloser, error) {
	return p.stderrReader, nil
}

func (p *fakeTemporaryTunnelProcess) Wait() error {
	<-p.waitDone
	_ = p.stdoutWriter.Close()
	_ = p.stderrWriter.Close()
	return p.waitErr
}

func (p *fakeTemporaryTunnelProcess) Interrupt() error {
	p.waitOnce.Do(func() { close(p.waitDone) })
	return nil
}

func (p *fakeTemporaryTunnelProcess) Kill() error {
	p.waitOnce.Do(func() { close(p.waitDone) })
	return nil
}

func TestParseCloudflareQuickTunnelURL(t *testing.T) {
	if got := parseCloudflareQuickTunnelURL(`2026-07-18T10:00:00Z https://bright-sun-123.trycloudflare.com.`); got != "https://bright-sun-123.trycloudflare.com" {
		t.Fatalf("unexpected URL: %q", got)
	}
	if got := parseCloudflareQuickTunnelURL(`INF Requesting new quick Tunnel on https://api.trycloudflare.com/tunnel`); got != "" {
		t.Fatalf("must not treat API endpoint as public URL: %q", got)
	}
	if got := parseCloudflareQuickTunnelURL(`https://api.trycloudflare.com/tunnel then https://bright-sun-123.trycloudflare.com`); got != "https://bright-sun-123.trycloudflare.com" {
		t.Fatalf("expected public URL after API endpoint, got %q", got)
	}
	if got := parseCloudflareQuickTunnelURL("no public URL"); got != "" {
		t.Fatalf("expected no URL, got %q", got)
	}
}

func TestTemporaryTunnelManagerStartsAndStopsWithFakeProcess(t *testing.T) {
	process := newFakeTemporaryTunnelProcess()
	var commandName string
	var commandArgs []string
	var commandEnv []string
	manager := newTemporaryTunnelManager("127.0.0.1:7788", temporaryTunnelOptions{
		lookPath: func(string) (string, error) { return "/fake/cloudflared", nil },
		command: func(_ context.Context, name string, spec temporaryTunnelSpec) temporaryTunnelProcess {
			commandName = name
			commandArgs = append([]string(nil), spec.Args...)
			commandEnv = append([]string(nil), spec.Env...)
			return process
		},
		startTimeout: time.Second,
	})

	snapshot, err := manager.StartTunnel(context.Background())
	if err != nil {
		t.Fatalf("start tunnel: %v", err)
	}
	if commandName != "/fake/cloudflared" {
		t.Fatalf("unexpected cloudflared binary: %q", commandName)
	}
	if len(commandArgs) != 10 || commandArgs[0] != "--config" || commandArgs[1] == "" || commandArgs[2] != "tunnel" || commandArgs[3] != "--no-autoupdate" || commandArgs[4] != "--edge-ip-version" || commandArgs[5] != "4" || commandArgs[6] != "--protocol" || commandArgs[7] != "http2" || commandArgs[8] != "--url" || commandArgs[9] != "http://127.0.0.1:7788" {
		t.Fatalf("unexpected cloudflared arguments: %q", commandArgs)
	}
	if commandArgs[1] == os.DevNull {
		t.Fatalf("cloudflared config must not use the null device: %q", commandArgs)
	}
	if len(commandEnv) != 0 {
		t.Fatalf("unexpected quick tunnel environment: %q", commandEnv)
	}

	if snapshot.Status != temporaryTunnelRunning || snapshot.PublicURL != "https://example.trycloudflare.com" {
		t.Fatalf("unexpected running snapshot: %+v", snapshot)
	}

	stopped, err := manager.StopTunnel(context.Background())
	if err != nil {
		t.Fatalf("stop tunnel: %v", err)
	}
	if stopped.Status != temporaryTunnelIdle || stopped.PublicURL != "" {
		t.Fatalf("unexpected stopped snapshot: %+v", stopped)
	}
}

func TestTemporaryTunnelManagerTimesOutAndCleansUp(t *testing.T) {
	process := newFakeTemporaryTunnelProcess()
	manager := newTemporaryTunnelManager("127.0.0.1:7788", temporaryTunnelOptions{
		lookPath: func(string) (string, error) { return "/fake/cloudflared", nil },
		command: func(context.Context, string, temporaryTunnelSpec) temporaryTunnelProcess {
			return processWithoutURL{fakeTemporaryTunnelProcess: process}
		},
		startTimeout: 15 * time.Millisecond,
	})

	snapshot, err := manager.StartTunnel(context.Background())
	if err == nil || !strings.Contains(err.Error(), "startup timeout") {
		t.Fatalf("expected startup timeout, got snapshot=%+v err=%v", snapshot, err)
	}
	if snapshot.Status != temporaryTunnelError {
		t.Fatalf("expected error status after timeout, got %+v", snapshot)
	}
	if manager.Snapshot().Status != temporaryTunnelError {
		t.Fatalf("expected manager cleanup after timeout, got %+v", manager.Snapshot())
	}
}

type processWithoutURL struct {
	*fakeTemporaryTunnelProcess
}

func (p processWithoutURL) Start() error {
	if p.startErr != nil {
		return p.startErr
	}
	return nil
}

func TestTemporaryTunnelManagerReportsUnavailableCloudflared(t *testing.T) {
	manager := newTemporaryTunnelManager("127.0.0.1:7788", temporaryTunnelOptions{
		lookPath: func(string) (string, error) { return "", errors.New("not found") },
	})
	snapshot, err := manager.StartTunnel(context.Background())
	if err == nil {
		t.Fatal("expected unavailable error")
	}
	if snapshot.Available || snapshot.Status != temporaryTunnelUnavailable {
		t.Fatalf("unexpected unavailable snapshot: %+v", snapshot)
	}
	if snapshot.Error == "" {
		t.Fatal("expected unavailable error message")
	}
}
