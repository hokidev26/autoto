package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DefaultSessionIdleTTL = 5 * time.Minute
	sessionSlotSep        = "\x1f"
	// maxPooledCalls bounds how many tool calls a single pooled process may
	// serve before it is recycled. Registry MCP streams are unbounded, but a
	// cap still prevents a wedged or leaky server from living forever.
	maxPooledCalls = 62
)

// ProcessPool keeps at most one warm stdio MCP process per session slot so
// consecutive MCPListTools / MCPCallTool calls skip process startup and keep
// browser pages and other server-side state.
type ProcessPool struct {
	mu      sync.Mutex
	entries map[string]*poolEntry
	idleTTL time.Duration
	closed  bool
	timer   *time.Timer
}

type poolEntry struct {
	client   SessionHandle
	key      string
	busy     bool
	calls    int
	lastUsed time.Time
}

// SessionHandle is a pooled stdio MCP client.
type SessionHandle interface {
	Initialize(context.Context) error
	ListTools(context.Context) ([]Tool, error)
	CallTool(context.Context, string, json.RawMessage) (ToolCallResult, error)
	Close() error
	Alive() bool
}

var _ SessionHandle = (*Client)(nil)

func NewProcessPool(idleTTL time.Duration) *ProcessPool {
	if idleTTL <= 0 {
		idleTTL = DefaultSessionIdleTTL
	}
	return &ProcessPool{entries: make(map[string]*poolEntry), idleTTL: idleTTL}
}

var defaultSessionPool = NewProcessPool(0)

func DefaultSessionPool() *ProcessPool {
	return defaultSessionPool
}

func InvalidateRegisteredServer(serverID string) {
	DefaultSessionPool().InvalidateServer(serverID)
}

// SessionSlot pins a warm process to one registered server, one agent, and
// one working directory so two agents never share a browser session.
func SessionSlot(serverID, agentID, cwd string) string {
	return strings.TrimSpace(serverID) + sessionSlotSep + strings.TrimSpace(agentID) + sessionSlotSep + strings.TrimSpace(cwd)
}

func serverIDFromSlot(slot string) string {
	serverID, _, _ := strings.Cut(slot, sessionSlotSep)
	return serverID
}

// LaunchFingerprint hashes launch configuration without retaining env values.
func LaunchFingerprint(command string, args []string, cwd string, env map[string]string) string {
	hash := sha256.New()
	write := func(value string) {
		hash.Write([]byte(value))
		hash.Write([]byte{0})
	}
	write(strings.TrimSpace(command))
	for _, arg := range args {
		write(arg)
	}
	write("\x1e")
	write(strings.TrimSpace(cwd))
	write(environmentFingerprint(env))
	return hex.EncodeToString(hash.Sum(nil))
}

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

func (p *ProcessPool) Acquire(slot, key string) (SessionHandle, bool) {
	if p == nil {
		return nil, false
	}
	p.mu.Lock()
	entry := p.entries[slot]
	if p.closed || entry == nil || entry.busy {
		p.mu.Unlock()
		return nil, false
	}
	if entry.key != key || !entry.client.Alive() {
		delete(p.entries, slot)
		client := entry.client
		p.mu.Unlock()
		_ = client.Close()
		return nil, false
	}
	entry.busy = true
	p.mu.Unlock()
	return entry.client, true
}

func (p *ProcessPool) Release(slot string, client SessionHandle, callErr error) {
	if p == nil || client == nil {
		return
	}
	p.mu.Lock()
	entry := p.entries[slot]
	if entry == nil || entry.client != client {
		p.mu.Unlock()
		_ = client.Close()
		return
	}
	entry.calls++
	if callErr != nil || p.closed || entry.calls >= maxPooledCalls || !client.Alive() {
		delete(p.entries, slot)
		p.mu.Unlock()
		_ = client.Close()
		return
	}
	entry.busy = false
	entry.lastUsed = time.Now()
	p.scheduleSweepLocked()
	p.mu.Unlock()
}

func (p *ProcessPool) Offer(slot, key string, client SessionHandle) {
	if p == nil || client == nil {
		return
	}
	p.mu.Lock()
	if p.closed || p.entries[slot] != nil || !client.Alive() {
		p.mu.Unlock()
		_ = client.Close()
		return
	}
	p.entries[slot] = &poolEntry{client: client, key: key, calls: 1, lastUsed: time.Now()}
	p.scheduleSweepLocked()
	p.mu.Unlock()
}

func (p *ProcessPool) InvalidateServer(serverID string) {
	if p == nil {
		return
	}
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return
	}
	p.mu.Lock()
	doomed := make([]SessionHandle, 0)
	for slot, entry := range p.entries {
		if serverIDFromSlot(slot) != serverID {
			continue
		}
		delete(p.entries, slot)
		if !entry.busy {
			doomed = append(doomed, entry.client)
		}
	}
	p.mu.Unlock()
	for _, client := range doomed {
		_ = client.Close()
	}
}

func (p *ProcessPool) scheduleSweepLocked() {
	if p.closed || p.timer != nil || len(p.entries) == 0 {
		return
	}
	p.timer = time.AfterFunc(p.idleTTL, p.sweep)
}

func (p *ProcessPool) sweep() {
	p.mu.Lock()
	p.timer = nil
	if p.closed {
		p.mu.Unlock()
		return
	}
	now := time.Now()
	doomed := make([]SessionHandle, 0)
	for slot, entry := range p.entries {
		if !entry.busy && now.Sub(entry.lastUsed) >= p.idleTTL {
			doomed = append(doomed, entry.client)
			delete(p.entries, slot)
		}
	}
	p.scheduleSweepLocked()
	p.mu.Unlock()
	for _, client := range doomed {
		_ = client.Close()
	}
}

func (p *ProcessPool) Close() {
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
	doomed := make([]SessionHandle, 0, len(p.entries))
	for slot, entry := range p.entries {
		doomed = append(doomed, entry.client)
		delete(p.entries, slot)
	}
	p.mu.Unlock()
	for _, client := range doomed {
		_ = client.Close()
	}
}
