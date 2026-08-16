package mcp

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

type fakeSession struct {
	mu     sync.Mutex
	alive  bool
	closes int
}

func (f *fakeSession) Initialize(context.Context) error { return nil }
func (f *fakeSession) ListTools(context.Context) ([]Tool, error) {
	return nil, nil
}
func (f *fakeSession) CallTool(context.Context, string, json.RawMessage) (ToolCallResult, error) {
	return ToolCallResult{}, nil
}
func (f *fakeSession) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	f.alive = false
	return nil
}
func (f *fakeSession) Alive() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive
}
func (f *fakeSession) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

func TestProcessPoolReusesClientUntilReleased(t *testing.T) {
	pool := NewProcessPool(time.Minute)
	defer pool.Close()
	client := &fakeSession{alive: true}
	pool.Offer("srv\x1fagent\x1fcwd", "fp1", client)

	got, ok := pool.Acquire("srv\x1fagent\x1fcwd", "fp1")
	if !ok || got != client {
		t.Fatalf("expected pooled client, ok=%v", ok)
	}
	if _, busy := pool.Acquire("srv\x1fagent\x1fcwd", "fp1"); busy {
		t.Fatal("busy session must not be queued")
	}
	pool.Release("srv\x1fagent\x1fcwd", client, nil)
	got, ok = pool.Acquire("srv\x1fagent\x1fcwd", "fp1")
	if !ok || got != client {
		t.Fatal("expected reuse after release")
	}
	if client.closeCount() != 0 {
		t.Fatalf("reused client was closed: %d", client.closeCount())
	}
}

func TestProcessPoolDropsMismatchedFingerprintAndDeadClient(t *testing.T) {
	pool := NewProcessPool(time.Minute)
	defer pool.Close()
	stale := &fakeSession{alive: true}
	pool.Offer("srv\x1fagent\x1fcwd", "fp-old", stale)
	if _, ok := pool.Acquire("srv\x1fagent\x1fcwd", "fp-new"); ok {
		t.Fatal("mismatched fingerprint must not reuse")
	}
	if stale.closeCount() != 1 {
		t.Fatalf("stale client closes=%d", stale.closeCount())
	}

	dead := &fakeSession{alive: false}
	pool.Offer("srv\x1fagent\x1fcwd", "fp1", dead)
	if dead.closeCount() != 1 {
		t.Fatalf("dead offer must close immediately, closes=%d", dead.closeCount())
	}
}

func TestProcessPoolInvalidateServerClosesIdleOnly(t *testing.T) {
	pool := NewProcessPool(time.Minute)
	defer pool.Close()
	idle := &fakeSession{alive: true}
	busy := &fakeSession{alive: true}
	other := &fakeSession{alive: true}
	pool.Offer("srv-a\x1fa\x1fcwd", "fp", idle)
	pool.Offer("srv-a\x1fb\x1fcwd", "fp", busy)
	pool.Offer("srv-b\x1fa\x1fcwd", "fp", other)
	if _, ok := pool.Acquire("srv-a\x1fb\x1fcwd", "fp"); !ok {
		t.Fatal("expected busy acquire")
	}

	pool.InvalidateServer("srv-a")
	if idle.closeCount() != 1 {
		t.Fatalf("idle session for srv-a should close, closes=%d", idle.closeCount())
	}
	if busy.closeCount() != 0 {
		t.Fatalf("busy session should close on release, closes=%d", busy.closeCount())
	}
	if other.closeCount() != 0 {
		t.Fatalf("other server was closed: %d", other.closeCount())
	}
	pool.Release("srv-a\x1fb\x1fcwd", busy, nil)
	if busy.closeCount() != 1 {
		t.Fatalf("invalidated busy session should close on release, closes=%d", busy.closeCount())
	}
}

func TestProcessPoolIdleTTLReapsClient(t *testing.T) {
	pool := NewProcessPool(20 * time.Millisecond)
	defer pool.Close()
	client := &fakeSession{alive: true}
	pool.Offer("srv\x1fagent\x1fcwd", "fp", client)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if client.closeCount() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("idle session was not reaped")
}

func TestLaunchFingerprintIgnoresEnvOrder(t *testing.T) {
	left := LaunchFingerprint("npx", []string{"a", "b"}, "/tmp", map[string]string{"B": "2", "A": "1"})
	right := LaunchFingerprint("npx", []string{"a", "b"}, "/tmp", map[string]string{"A": "1", "B": "2"})
	if left != right || left == "" {
		t.Fatalf("fingerprint mismatch: %s vs %s", left, right)
	}
	changed := LaunchFingerprint("npx", []string{"a", "b"}, "/tmp", map[string]string{"A": "1", "B": "3"})
	if changed == left {
		t.Fatal("env value change must recycle the fingerprint")
	}
}
