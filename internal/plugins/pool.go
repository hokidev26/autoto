package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
	"time"
)

const (
	defaultPoolIdleTTL = 5 * time.Minute
	// maxPooledCalls bounds how many tool calls a single pooled process may
	// serve. Pooled processes run with a lifetime stdout budget of
	// pooledResponseBudgetFactor times the per-call response limit; recycling
	// after maxPooledCalls calls guarantees every call still has more than one
	// full per-call budget of stdout headroom left.
	maxPooledCalls             = 62
	pooledResponseBudgetFactor = 64
)

// poolKey pins a pooled process to the exact manifest revision and resolved
// environment it was launched with. Any mismatch recycles the process, so a
// warm process can never keep serving stale code, arguments, or secrets.
type poolKey struct {
	revision     int64
	manifestHash string
	envHash      string
}

type poolEntry struct {
	client   MCPClient
	key      poolKey
	busy     bool
	calls    int
	lastUsed time.Time
}

// processPool keeps at most one warm MCP stdio process per plugin so
// consecutive tool calls skip process startup and MCP initialization.
// Discovery and health checks intentionally bypass the pool: their point is to
// prove the plugin still starts from scratch.
type processPool struct {
	mu      sync.Mutex
	entries map[string]*poolEntry
	idleTTL time.Duration
	closed  bool
	timer   *time.Timer
}

func newProcessPool(idleTTL time.Duration) *processPool {
	if idleTTL <= 0 {
		idleTTL = defaultPoolIdleTTL
	}
	return &processPool{entries: make(map[string]*poolEntry), idleTTL: idleTTL}
}

// acquire returns the plugin's warm client when one exists for exactly the
// given key. The caller must hand it back with release. A key mismatch closes
// the stale process; a busy entry reports a miss so the caller falls back to
// a fresh process instead of queueing behind a slow call.
func (p *processPool) acquire(pluginID string, key poolKey) (MCPClient, bool) {
	if p == nil {
		return nil, false
	}
	p.mu.Lock()
	entry := p.entries[pluginID]
	if p.closed || entry == nil || entry.busy {
		p.mu.Unlock()
		return nil, false
	}
	if entry.key != key {
		delete(p.entries, pluginID)
		p.mu.Unlock()
		_ = entry.client.Close()
		return nil, false
	}
	entry.busy = true
	p.mu.Unlock()
	return entry.client, true
}

// release hands an acquired client back. Any call error closes and drops the
// process: a failed or timed-out request can leave the shared stdio decoder
// mid-stream, so a client that has ever errored is never reused.
func (p *processPool) release(pluginID string, client MCPClient, callErr error) {
	if p == nil || client == nil {
		return
	}
	p.mu.Lock()
	entry := p.entries[pluginID]
	if entry == nil || entry.client != client {
		// Invalidated while busy: the lifecycle path detached the entry and
		// left the in-flight call to finish. The process ends here.
		p.mu.Unlock()
		_ = client.Close()
		return
	}
	entry.calls++
	if callErr != nil || p.closed || entry.calls >= maxPooledCalls {
		delete(p.entries, pluginID)
		p.mu.Unlock()
		_ = client.Close()
		return
	}
	entry.busy = false
	entry.lastUsed = time.Now()
	p.scheduleSweepLocked()
	p.mu.Unlock()
}

// offer places a freshly spawned client that just served a successful call
// into the pool. The pool keeps at most one process per plugin; a concurrent
// duplicate is closed instead of stored.
func (p *processPool) offer(pluginID string, key poolKey, client MCPClient) {
	if p == nil || client == nil {
		return
	}
	p.mu.Lock()
	if p.closed || p.entries[pluginID] != nil {
		p.mu.Unlock()
		_ = client.Close()
		return
	}
	p.entries[pluginID] = &poolEntry{client: client, key: key, calls: 1, lastUsed: time.Now()}
	p.scheduleSweepLocked()
	p.mu.Unlock()
}

// invalidate drops the plugin's pooled process after a lifecycle change
// (enable, disable, discover, update, uninstall). An idle process is closed
// immediately; a busy one is detached and closed by its release call, so the
// in-flight tool call finishes exactly like the per-call processes used to.
func (p *processPool) invalidate(pluginID string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	entry := p.entries[pluginID]
	if entry == nil {
		p.mu.Unlock()
		return
	}
	delete(p.entries, pluginID)
	busy := entry.busy
	p.mu.Unlock()
	if !busy {
		_ = entry.client.Close()
	}
}

func (p *processPool) scheduleSweepLocked() {
	if p.closed || p.timer != nil || len(p.entries) == 0 {
		return
	}
	p.timer = time.AfterFunc(p.idleTTL, p.sweep)
}

func (p *processPool) sweep() {
	p.mu.Lock()
	p.timer = nil
	if p.closed {
		p.mu.Unlock()
		return
	}
	now := time.Now()
	doomed := make([]MCPClient, 0)
	for id, entry := range p.entries {
		if !entry.busy && now.Sub(entry.lastUsed) >= p.idleTTL {
			doomed = append(doomed, entry.client)
			delete(p.entries, id)
		}
	}
	p.scheduleSweepLocked()
	p.mu.Unlock()
	for _, client := range doomed {
		_ = client.Close()
	}
}

// close terminates every pooled process, busy ones included: it only runs at
// service shutdown, where killing in-flight plugin calls is the correct
// behavior. The pool stays closed; later calls run on per-call processes.
func (p *processPool) close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	doomed := make([]MCPClient, 0, len(p.entries))
	for id, entry := range p.entries {
		doomed = append(doomed, entry.client)
		delete(p.entries, id)
	}
	p.mu.Unlock()
	for _, client := range doomed {
		_ = client.Close()
	}
}

// environmentFingerprint hashes the fully resolved child environment so a
// pooled process is recycled whenever a secret or env value changes. Only the
// hash is retained; resolved values are never stored.
func environmentFingerprint(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, key := range keys {
		hash.Write([]byte(key))
		hash.Write([]byte{0})
		hash.Write([]byte(env[key]))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
