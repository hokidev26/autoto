package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	ProtocolVersion                   = 2
	DefaultRingSize                   = 512
	DefaultRingBytes                  = 512 * 1024
	DefaultMaxEventBytes              = 32 * 1024
	DefaultToolOutputSnapshotBytes    = 48 * 1024
	DefaultReplayLimit                = 256
	DefaultSubscriberBuffer           = 64
	DefaultMaxStreams                 = 256
	DefaultStreamIdleTimeout          = 15 * time.Minute
	hubEventTextSoftLimitBytes        = 16 * 1024
	hubEventDataStringSoftLimitBytes  = 4 * 1024
	hubEventDataStringTightLimitBytes = 512
	hubEventCriticalStringLimitBytes  = 256
	hubEventDataMaxFields             = 32
	hubEventDataMaxItems              = 16
	hubEventDataTightMaxFields        = 16
	hubEventDataTightMaxItems         = 8
)

type Event struct {
	Type          string         `json:"type"`
	AgentID       string         `json:"agentId,omitempty"`
	MessageID     string         `json:"messageId,omitempty"`
	Text          string         `json:"text,omitempty"`
	Data          map[string]any `json:"data,omitempty"`
	CreatedAt     string         `json:"createdAt"`
	Protocol      int            `json:"protocol"`
	StreamSession string         `json:"streamSession"`
	Sequence      uint64         `json:"sequence"`
}

func (e Event) JSON() []byte {
	data, _ := json.Marshal(e)
	return data
}

// Subscriber is retained for protocol-1 callers. Protocol-2 callers should use
// SubscribeProtocol so that replay and resync state are observable.
type Subscriber chan Event

type ResyncReason string

const (
	ResyncSessionMismatch   ResyncReason = "session_mismatch"
	ResyncCursorExpired     ResyncReason = "cursor_expired"
	ResyncReplayLimit       ResyncReason = "replay_limit"
	ResyncSubscriberOverrun ResyncReason = "subscriber_overrun"
	ResyncStreamEvicted     ResyncReason = "stream_evicted"
)

type HubConfig struct {
	RingSize         int
	RingBytes        int
	MaxEventBytes    int
	ReplayLimit      int
	SubscriberBuffer int
	MaxStreams       int
	IdleTimeout      time.Duration
	Clock            func() time.Time
	// NewSession allocates a stream-session id. An empty result is failure:
	// the hub does not create or wrap the stream, and the operation is dropped
	// rather than panicking. The default uses crypto/rand with no fallback.
	NewSession func() string
}

type SubscribeOptions struct {
	AgentID       string
	StreamSession string
	After         uint64
	HasAfter      bool
}

// Subscription is a replay cut plus a live subscriber installed under the
// same Hub lock. Replay is complete or empty; callers must resync on Reason.
type Subscription struct {
	Events         <-chan Event
	Replay         []Event
	StreamSession  string
	OldestSequence uint64
	LatestSequence uint64
	Reason         ResyncReason
	Resync         <-chan ResyncReason

	events Subscriber
}

type hubSubscriber struct {
	events Subscriber
	resync chan ResyncReason
	mu     sync.Mutex
	closed bool
}

// deliverItem is one publish's fan-out work, queued under the hub lock so
// sequence order is fixed before any subscriber send runs outside it.
type deliverItem struct {
	event Event
	subs  []*hubSubscriber
}

// ringEntry carries the encoded size alongside the event. Publish is the hottest
// path in the process and holds a hub-wide lock, so the size is computed once
// when the event is admitted rather than re-marshalled on every eviction.
type ringEntry struct {
	event Event
	size  int
}

type ProviderRetrySnapshot struct {
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"maxAttempts"`
	BackoffMs   int64  `json:"backoffMs,omitempty"`
	Scope       string `json:"scope,omitempty"`
	RunID       string `json:"runId,omitempty"`
}

type stream struct {
	session       string
	sequence      uint64
	ring          []ringEntry
	ringBytes     int
	subscribers   map[*hubSubscriber]struct{}
	lastActivity  time.Time
	pending       []deliverItem
	fanoutMu      sync.Mutex
	providerRetry *ProviderRetrySnapshot
}

type Hub struct {
	mu      sync.Mutex
	config  HubConfig
	streams map[string]*stream

	// testFanoutStall, if set, runs after Publish has released the hub lock
	// and taken the per-stream fan-out lock. Tests use it to prove other hub
	// operations are not blocked by subscriber delivery.
	testFanoutStall func()
}

type StreamWatermark struct {
	StreamSession  string `json:"streamSession"`
	OldestSequence uint64 `json:"oldestSequence"`
	LatestSequence uint64 `json:"latestSequence"`
}

type ToolOutputSnapshot struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

func (h *Hub) Watermark(agentID string) StreamWatermark {
	now := h.now()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.collectGarbageLocked(now)
	current := h.ensureStreamLocked(agentID, now)
	if current == nil {
		return StreamWatermark{}
	}
	watermark := StreamWatermark{StreamSession: current.session, LatestSequence: current.sequence}
	if len(current.ring) > 0 {
		watermark.OldestSequence = current.ring[0].event.Sequence
	}
	return watermark
}

func (h *Hub) ProviderRetrySnapshot(agentID string) *ProviderRetrySnapshot {
	now := h.now()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.collectGarbageLocked(now)
	current := h.streams[agentID]
	if current == nil || current.providerRetry == nil {
		return nil
	}
	copy := *current.providerRetry
	return &copy
}

func (h *Hub) ToolOutputSnapshots(agentID string) map[string]ToolOutputSnapshot {
	return h.toolTextSnapshots(agentID, "tool.output")
}

// ToolInputPreviewSnapshots rebuilds the streamed argument previews
// (tool.input_delta) still held in the ring, so a client that reconnects
// mid-call can restore what the model had already composed.
func (h *Hub) ToolInputPreviewSnapshots(agentID string) map[string]ToolOutputSnapshot {
	return h.toolTextSnapshots(agentID, "tool.input_delta")
}

func (h *Hub) toolTextSnapshots(agentID, eventType string) map[string]ToolOutputSnapshot {
	now := h.now()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.collectGarbageLocked(now)
	current := h.streams[agentID]
	if current == nil || len(current.ring) == 0 {
		return nil
	}
	result := make(map[string]ToolOutputSnapshot)
	for _, entry := range current.ring {
		event := entry.event
		if event.Type != eventType || event.Text == "" {
			continue
		}
		toolUseID, _ := event.Data["toolUseId"].(string)
		toolUseID = strings.TrimSpace(toolUseID)
		if toolUseID == "" {
			continue
		}
		snapshot := result[toolUseID]
		// Snapshot-style previews (Bash commands) resend the whole text, so
		// each replace event supersedes what was accumulated before it.
		if replace, _ := event.Data["replace"].(bool); replace {
			snapshot.Text, snapshot.Truncated = appendToolOutputSnapshot("", event.Text, false)
		} else {
			snapshot.Text, snapshot.Truncated = appendToolOutputSnapshot(snapshot.Text, event.Text, snapshot.Truncated)
		}
		if truncated, _ := event.Data["truncated"].(bool); truncated {
			snapshot.Truncated = true
		}
		result[toolUseID] = snapshot
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func NewHub() *Hub {
	return NewHubWithConfig(HubConfig{})
}

func NewHubWithConfig(config HubConfig) *Hub {
	if config.RingSize <= 0 {
		config.RingSize = DefaultRingSize
	}
	if config.RingBytes <= 0 {
		config.RingBytes = DefaultRingBytes
	}
	if config.MaxEventBytes <= 0 {
		config.MaxEventBytes = DefaultMaxEventBytes
	}
	if config.RingBytes < config.MaxEventBytes {
		config.RingBytes = config.MaxEventBytes
	}
	if config.ReplayLimit <= 0 {
		config.ReplayLimit = DefaultReplayLimit
	}
	if config.SubscriberBuffer <= 0 {
		config.SubscriberBuffer = DefaultSubscriberBuffer
	}
	if config.MaxStreams <= 0 {
		config.MaxStreams = DefaultMaxStreams
	}
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = DefaultStreamIdleTimeout
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.NewSession == nil {
		config.NewSession = randomStreamSession
	}
	return &Hub{config: config, streams: make(map[string]*stream)}
}

// randRead is crypto/rand.Read in production. Tests replace it to simulate
// generator failure without panicking the process.
var randRead = rand.Read

func randomStreamSession() string {
	id, err := generateStreamSession()
	if err != nil {
		return ""
	}
	return id
}

func generateStreamSession() (string, error) {
	var bytes [16]byte
	if _, err := randRead(bytes[:]); err != nil {
		// Stream-session ids are identity/replay tokens, not secrets, but a
		// non-crypto fallback could collide with a live session after wrap and
		// make a client treat a reset ring as the same stream. Fail closed:
		// an empty id means the caller must drop the operation.
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

// Subscribe retains the old realtime-only API. It intentionally does not
// replay events; websocket protocol 2 uses SubscribeProtocol instead.
func (h *Hub) Subscribe(ctx context.Context, agentID string) Subscriber {
	subscription := h.SubscribeProtocol(ctx, SubscribeOptions{AgentID: agentID})
	if subscription.Reason != "" {
		ch := make(Subscriber)
		close(ch)
		return ch
	}
	return subscription.events
}

// SubscribeProtocol atomically captures replay and installs the live
// subscription. A non-empty Reason means no events were replayed or subscribed.
func (h *Hub) SubscribeProtocol(ctx context.Context, options SubscribeOptions) *Subscription {
	now := h.now()
	h.mu.Lock()
	h.collectGarbageLocked(now)
	current := h.ensureStreamLocked(options.AgentID, now)
	if current == nil {
		h.mu.Unlock()
		return &Subscription{Reason: ResyncStreamEvicted}
	}

	result := &Subscription{
		StreamSession:  current.session,
		LatestSequence: current.sequence,
	}
	if len(current.ring) > 0 {
		result.OldestSequence = current.ring[0].event.Sequence
	}
	if options.StreamSession != "" && options.StreamSession != current.session {
		result.Reason = ResyncSessionMismatch
		h.mu.Unlock()
		return result
	}
	if options.HasAfter {
		if options.StreamSession == "" || options.After > current.sequence {
			result.Reason = ResyncSessionMismatch
			h.mu.Unlock()
			return result
		}
		if len(current.ring) > 0 {
			earliest := current.ring[0].event.Sequence
			if options.After < earliest-1 {
				result.Reason = ResyncCursorExpired
				h.mu.Unlock()
				return result
			}
		}
		replay := replayAfter(current.ring, options.After)
		if len(replay) > h.config.ReplayLimit {
			result.Reason = ResyncReplayLimit
			h.mu.Unlock()
			return result
		}
		result.Replay = replay
	}

	sub := &hubSubscriber{
		events: make(Subscriber, h.config.SubscriberBuffer),
		resync: make(chan ResyncReason, 1),
	}
	current.subscribers[sub] = struct{}{}
	current.lastActivity = now
	result.Events = sub.events
	result.Resync = sub.resync
	result.events = sub.events
	h.mu.Unlock()

	go func() {
		<-ctx.Done()
		h.removeSubscriber(options.AgentID, sub)
	}()
	return result
}

func replayAfter(entries []ringEntry, after uint64) []Event {
	first := 0
	for first < len(entries) && entries[first].event.Sequence <= after {
		first++
	}
	if first == len(entries) {
		return nil
	}
	replay := make([]Event, 0, len(entries)-first)
	for _, entry := range entries[first:] {
		replay = append(replay, entry.event)
	}
	return replay
}

// Publish assigns the protocol/session/sequence envelope before retaining and
// delivering the event. Slow subscribers are explicitly marked for resync.
//
// Sequence assignment, ring admission, and the subscriber snapshot happen
// under the hub lock. Fan-out runs after the lock is released. Per-subscriber
// order is the sequence order: each publish appends to the stream's pending
// queue under the lock, and one drain (serialized by fanoutMu) delivers that
// queue FIFO. Two Publish calls therefore cannot interleave sends to the same
// subscriber even though they no longer hold the hub lock during the send.
func (h *Hub) Publish(event Event) {
	now := h.now()
	// Bounded outside the lock, and the size it computed is carried forward so the
	// recheck after the envelope is assigned does not have to re-encode the event.
	event, boundedSize := boundedHubEventWithSize(event, h.config.MaxEventBytes)
	h.mu.Lock()
	h.collectGarbageLocked(now)
	current := h.ensureStreamLocked(event.AgentID, now)
	if current == nil {
		h.mu.Unlock()
		return
	}
	if current.sequence == math.MaxUint64 {
		nextSession := h.config.NewSession()
		if nextSession == "" {
			h.mu.Unlock()
			return
		}
		h.resyncAllLocked(current, ResyncSessionMismatch)
		current.session = nextSession
		current.sequence = 0
		current.ring = nil
		current.ringBytes = 0
	}
	current.sequence++
	event.Protocol = ProtocolVersion
	event.StreamSession = current.session
	event.Sequence = current.sequence
	if event.CreatedAt == "" {
		event.CreatedAt = now.UTC().Format(time.RFC3339Nano)
	}
	// The envelope assigned above (protocol, session, sequence, createdAt) grows the
	// encoding, so the bound has to be rechecked. Re-bounding unconditionally cost a
	// second full pass -- up to four more Marshal calls -- on every event while the
	// hub-wide lock was held. The envelope's contribution is bounded and measurable,
	// so the common case now only re-bounds when the headroom is actually at risk.
	eventBytes := boundedSize
	if boundedSize+hubEventEnvelopeMaxGrowth(event) > h.maxEventBytes() {
		event, eventBytes = boundedHubEventWithSize(event, h.config.MaxEventBytes)
	} else {
		// Still exact enough for the byte budget: the stamped values are known, so
		// add their real lengths instead of re-encoding the whole event.
		eventBytes += hubEventEnvelopeMaxGrowth(event)
	}
	current.ring = append(current.ring, ringEntry{event: event, size: eventBytes})
	current.ringBytes += eventBytes
	// Evictions are counted first and dropped in one reslice. The previous form
	// shifted the whole slice per eviction, so a single large event that forced
	// several evictions turned this into O(n^2) inside the lock.
	evict := 0
	for evict < len(current.ring) && (len(current.ring)-evict > h.config.RingSize || current.ringBytes > h.config.RingBytes) {
		current.ringBytes -= current.ring[evict].size
		evict++
	}
	if evict > 0 {
		// Copied down rather than resliced so the evicted events become unreachable
		// and the backing array does not grow without bound.
		remaining := copy(current.ring, current.ring[evict:])
		clear(current.ring[remaining:])
		current.ring = current.ring[:remaining]
	}
	if current.ringBytes < 0 {
		current.ringBytes = 0
	}
	current.lastActivity = now
	applyProviderRetryLocked(current, event)
	var enqueued bool
	if n := len(current.subscribers); n > 0 {
		subs := make([]*hubSubscriber, 0, n)
		for sub := range current.subscribers {
			subs = append(subs, sub)
		}
		current.pending = append(current.pending, deliverItem{event: event, subs: subs})
		enqueued = true
	}
	h.mu.Unlock()
	if enqueued {
		h.fanoutPending(current)
	}
}

// fanoutPending delivers queued events in the order they were enqueued under
// the hub lock. It must not be called while holding h.mu: it takes fanoutMu
// first and then re-acquires h.mu only to dequeue the next item.
func (h *Hub) fanoutPending(current *stream) {
	current.fanoutMu.Lock()
	defer current.fanoutMu.Unlock()
	if h.testFanoutStall != nil {
		h.testFanoutStall()
	}
	for {
		h.mu.Lock()
		if len(current.pending) == 0 {
			h.mu.Unlock()
			return
		}
		item := current.pending[0]
		copy(current.pending, current.pending[1:])
		current.pending[len(current.pending)-1] = deliverItem{}
		current.pending = current.pending[:len(current.pending)-1]
		h.mu.Unlock()

		var overruns []*hubSubscriber
		for _, sub := range item.subs {
			if sub.trySend(item.event) {
				overruns = append(overruns, sub)
			}
		}
		if len(overruns) == 0 {
			continue
		}
		h.mu.Lock()
		for _, sub := range overruns {
			if _, ok := current.subscribers[sub]; ok {
				h.resyncSubscriberLocked(current, sub, ResyncSubscriberOverrun)
			}
		}
		h.mu.Unlock()
	}
}

// trySend delivers one event without blocking. It reports overrun only when
// the subscriber is still open and the buffer is full. A subscriber closed
// after this snapshot was taken is a no-op so the send cannot panic.
func (s *hubSubscriber) trySend(event Event) (overrun bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	select {
	case s.events <- event:
		return false
	default:
		return true
	}
}

// closeIfOpen closes subscriber channels at most once. Send and close both
// take s.mu so a snapshot still in flight cannot send on a closed channel.
func (s *hubSubscriber) closeIfOpen(reason ResyncReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if reason != "" {
		select {
		case s.resync <- reason:
		default:
		}
	}
	close(s.events)
	close(s.resync)
}

func boundedHubEvent(event Event, maximum int) Event {
	bounded, _ := boundedHubEventWithSize(event, maximum)
	return bounded
}

// boundedHubEventWithSize returns the bounded event and its encoded size. The
// size is a by-product of the checks below, so handing it back saves the caller a
// full re-marshal of an event it just measured.
func boundedHubEventWithSize(event Event, maximum int) (Event, int) {
	if maximum <= 0 {
		maximum = DefaultMaxEventBytes
	}
	event.Type, _ = truncateHubString(event.Type, 128)
	event.AgentID, _ = truncateHubString(event.AgentID, 512)
	event.MessageID, _ = truncateHubString(event.MessageID, 512)
	var truncated bool
	event.Text, truncated = truncateHubString(event.Text, min(maximum/2, hubEventTextSoftLimitBytes))
	if truncated {
		event.Data = markHubEventTruncated(event.Data)
	}
	if size := hubEventSize(event); size <= maximum {
		return event, size
	}
	event.Data = boundedHubEventData(event.Data, hubEventDataStringSoftLimitBytes, hubEventDataMaxFields, hubEventDataMaxItems, 4)
	event.Data = markHubEventTruncated(event.Data)
	if size := hubEventSize(event); size <= maximum {
		return event, size
	}
	event.Data = boundedHubEventData(event.Data, hubEventDataStringTightLimitBytes, hubEventDataTightMaxFields, hubEventDataTightMaxItems, 3)
	event.Data = markHubEventTruncated(event.Data)
	if size := hubEventSize(event); size <= maximum {
		return event, size
	}
	event.Data = criticalHubEventData(event.Data)
	event.Text = hubEventTextThatFits(event, maximum)
	if size := hubEventSize(event); size <= maximum {
		return event, size
	}
	event.Data = map[string]any{"truncated": true}
	event.Text = hubEventTextThatFits(event, maximum)
	return event, hubEventSize(event)
}

func boundedHubEventData(data map[string]any, stringLimit, maxFields, maxItems, maxDepth int) map[string]any {
	if len(data) == 0 {
		return nil
	}
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make(map[string]any, min(len(keys), maxFields)+1)
	truncated := len(keys) > maxFields
	for index, key := range keys {
		if index >= maxFields {
			break
		}
		boundedKey, keyTruncated := truncateHubString(key, 128)
		if boundedKey == "" {
			truncated = true
			continue
		}
		value, valueTruncated := boundedHubEventValue(data[key], stringLimit, maxFields, maxItems, maxDepth, 0)
		if _, exists := result[boundedKey]; exists && boundedKey != key {
			truncated = true
			continue
		}
		result[boundedKey] = value
		truncated = truncated || keyTruncated || valueTruncated
	}
	if truncated {
		result["truncated"] = true
	}
	return result
}

func boundedHubEventValue(value any, stringLimit, maxFields, maxItems, maxDepth, depth int) (any, bool) {
	if depth >= maxDepth {
		return nil, true
	}
	switch typed := value.(type) {
	case nil, bool, float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
		return typed, false
	case string:
		return truncateHubString(typed, stringLimit)
	case json.RawMessage:
		if len(typed) == 0 {
			return json.RawMessage(`null`), false
		}
		var normalized any
		if !utf8.Valid(typed) || json.Unmarshal(typed, &normalized) != nil {
			return map[string]any{"bytes": len(typed), "truncated": true}, true
		}
		return boundedHubEventValue(normalized, stringLimit, maxFields, maxItems, maxDepth, depth+1)
	case []any:
		result := make([]any, 0, min(len(typed), maxItems))
		truncated := len(typed) > maxItems
		for index, item := range typed {
			if index >= maxItems {
				break
			}
			bounded, itemTruncated := boundedHubEventValue(item, stringLimit, maxFields, maxItems, maxDepth, depth+1)
			result = append(result, bounded)
			truncated = truncated || itemTruncated
		}
		return result, truncated
	case map[string]any:
		return boundedHubEventDataAtDepth(typed, stringLimit, maxFields, maxItems, maxDepth, depth+1)
	default:
		encoded, err := json.Marshal(value)
		if err != nil || len(encoded) > max(stringLimit*maxItems, stringLimit) || !utf8.Valid(encoded) {
			return nil, true
		}
		var normalized any
		if json.Unmarshal(encoded, &normalized) != nil {
			return nil, true
		}
		return boundedHubEventValue(normalized, stringLimit, maxFields, maxItems, maxDepth, depth+1)
	}
}

func boundedHubEventDataAtDepth(data map[string]any, stringLimit, maxFields, maxItems, maxDepth, depth int) (map[string]any, bool) {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make(map[string]any, min(len(keys), maxFields)+1)
	truncated := len(keys) > maxFields
	for index, key := range keys {
		if index >= maxFields {
			break
		}
		boundedKey, keyTruncated := truncateHubString(key, 128)
		if boundedKey == "" {
			truncated = true
			continue
		}
		bounded, valueTruncated := boundedHubEventValue(data[key], stringLimit, maxFields, maxItems, maxDepth, depth)
		if _, exists := result[boundedKey]; exists && boundedKey != key {
			truncated = true
			continue
		}
		result[boundedKey] = bounded
		truncated = truncated || keyTruncated || valueTruncated
	}
	if truncated {
		result["truncated"] = true
	}
	return result, truncated
}

func criticalHubEventData(data map[string]any) map[string]any {
	result := map[string]any{"truncated": true}
	for _, key := range []string{"runId", "requestId", "toolUseId", "toolName", "status", "risk", "executionDeviceId", "durationMs", "stream", "inputTruncated", "resultTruncated"} {
		value, ok := data[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			result[key], _ = truncateHubString(typed, hubEventCriticalStringLimitBytes)
		case nil, bool, float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
			result[key] = typed
		}
	}
	return result
}

func markHubEventTruncated(data map[string]any) map[string]any {
	result := make(map[string]any, len(data)+1)
	for key, value := range data {
		result[key] = value
	}
	result["truncated"] = true
	return result
}

func hubEventTextThatFits(event Event, maximum int) string {
	original := event.Text
	best := ""
	low, high := 0, len(original)
	for low <= high {
		middle := low + (high-low)/2
		candidate, _ := truncateHubString(original, middle)
		event.Text = candidate
		if hubEventSize(event) <= maximum {
			best = candidate
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	return best
}

// maxEventBytes is the configured ceiling with the same zero-value fallback
// boundedHubEventWithSize applies, so the two agree on what "too big" means.
func (h *Hub) maxEventBytes() int {
	if h.config.MaxEventBytes <= 0 {
		return DefaultMaxEventBytes
	}
	return h.config.MaxEventBytes
}

// hubEventEnvelopeMaxGrowth is the most the JSON can grow when Publish stamps the
// envelope onto an event.
//
// The four envelope fields have no omitempty, so their keys, quotes and
// separators are already in the size measured before the stamp; only the values
// get longer. This deliberately ignores the characters the zero values already
// occupied, which makes it an upper bound rather than an estimate: if it says the
// event still fits, it fits, and the re-bound is genuinely unnecessary.
func hubEventEnvelopeMaxGrowth(event Event) int {
	// uint64 is at most 20 digits, int at most 11 with sign.
	return len(event.StreamSession) + len(event.CreatedAt) + 20 + 11
}

func hubEventSize(event Event) int {
	encoded, err := json.Marshal(event)
	if err != nil {
		return math.MaxInt
	}
	return len(encoded)
}

func truncateHubString(value string, maximum int) (string, bool) {
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "�")
	}
	if maximum < 0 {
		maximum = 0
	}
	if len(value) <= maximum {
		return value, false
	}
	value = value[:maximum]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value, true
}

func appendToolOutputSnapshot(current, next string, alreadyTruncated bool) (string, bool) {
	current = strings.ToValidUTF8(current, "�")
	next = strings.ToValidUTF8(next, "�")
	combined := current + next
	if len(combined) <= DefaultToolOutputSnapshotBytes {
		return combined, alreadyTruncated
	}
	start := len(combined) - DefaultToolOutputSnapshotBytes
	for start < len(combined) && !utf8.RuneStart(combined[start]) {
		start++
	}
	return combined[start:], true
}

// CollectGarbage removes inactive streams immediately when the configured
// clock says that their idle timeout has elapsed. It is useful for tests and
// for applications that want deterministic reclamation without new traffic.
func (h *Hub) CollectGarbage() {
	h.mu.Lock()
	h.collectGarbageLocked(h.now())
	h.mu.Unlock()
}

func (h *Hub) removeSubscriber(agentID string, sub *hubSubscriber) {
	now := h.now()
	h.mu.Lock()
	current := h.streams[agentID]
	if current != nil {
		if _, ok := current.subscribers[sub]; ok {
			delete(current.subscribers, sub)
			sub.closeIfOpen("")
			current.lastActivity = now
		}
	}
	h.mu.Unlock()
}

func (h *Hub) now() time.Time { return h.config.Clock().UTC() }

func (h *Hub) ensureStreamLocked(agentID string, now time.Time) *stream {
	if current := h.streams[agentID]; current != nil {
		return current
	}
	session := h.config.NewSession()
	if session == "" {
		return nil
	}
	for len(h.streams) >= h.config.MaxStreams {
		victimID, victim := h.oldestStreamLocked()
		if victim == nil {
			return nil
		}
		h.resyncAllLocked(victim, ResyncStreamEvicted)
		delete(h.streams, victimID)
	}
	current := &stream{
		session:      session,
		subscribers:  make(map[*hubSubscriber]struct{}),
		lastActivity: now,
	}
	h.streams[agentID] = current
	return current
}

func (h *Hub) oldestStreamLocked() (string, *stream) {
	var victimID string
	var victim *stream
	for agentID, current := range h.streams {
		if victim == nil || current.lastActivity.Before(victim.lastActivity) {
			victimID, victim = agentID, current
		}
	}
	return victimID, victim
}

func (h *Hub) collectGarbageLocked(now time.Time) {
	for agentID, current := range h.streams {
		if len(current.subscribers) == 0 && !now.Before(current.lastActivity.Add(h.config.IdleTimeout)) {
			delete(h.streams, agentID)
		}
	}
}

func (h *Hub) resyncAllLocked(current *stream, reason ResyncReason) {
	for sub := range current.subscribers {
		h.resyncSubscriberLocked(current, sub, reason)
	}
}

func (h *Hub) resyncSubscriberLocked(current *stream, sub *hubSubscriber, reason ResyncReason) {
	delete(current.subscribers, sub)
	sub.closeIfOpen(reason)
}

func applyProviderRetryLocked(current *stream, event Event) {
	if current == nil {
		return
	}
	switch event.Type {
	case "agent.provider_error_retry":
		current.providerRetry = providerRetrySnapshotFromEvent(event)
	case "model.started", "agent.started", "agent.done", "agent.error", "agent.interrupted", "agent.waiting":
		current.providerRetry = nil
	}
}

func providerRetrySnapshotFromEvent(event Event) *ProviderRetrySnapshot {
	snapshot := &ProviderRetrySnapshot{Attempt: 1}
	if event.Data == nil {
		return snapshot
	}
	if attempt := intFromEventData(event.Data["attempt"]); attempt > 0 {
		snapshot.Attempt = attempt
	}
	if maxAttempts := intFromEventData(event.Data["maxAttempts"]); maxAttempts > 0 {
		snapshot.MaxAttempts = maxAttempts
	}
	snapshot.BackoffMs = int64FromEventData(event.Data["backoffMs"])
	snapshot.Scope, _ = event.Data["scope"].(string)
	if runID, _ := event.Data["runId"].(string); runID != "" {
		snapshot.RunID = runID
	}
	return snapshot
}

func intFromEventData(value any) int {
	return int(int64FromEventData(value))
}

func int64FromEventData(value any) int64 {
	switch n := value.(type) {
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	case uint:
		return int64(n)
	case uint32:
		return int64(n)
	case uint64:
		if n > uint64(math.MaxInt64) {
			return 0
		}
		return int64(n)
	case float64:
		if n < 0 {
			return 0
		}
		return int64(n)
	case json.Number:
		parsed, err := n.Int64()
		if err != nil || parsed < 0 {
			return 0
		}
		return parsed
	default:
		return 0
	}
}
