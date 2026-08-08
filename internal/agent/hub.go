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
	NewSession       func() string
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
}

// ringEntry carries the encoded size alongside the event. Publish is the hottest
// path in the process and holds a hub-wide lock, so the size is computed once
// when the event is admitted rather than re-marshalled on every eviction.
type ringEntry struct {
	event Event
	size  int
}

type stream struct {
	session      string
	sequence     uint64
	ring         []ringEntry
	ringBytes    int
	subscribers  map[*hubSubscriber]struct{}
	lastActivity time.Time
}

type Hub struct {
	mu      sync.Mutex
	config  HubConfig
	streams map[string]*stream
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

func (h *Hub) ToolOutputSnapshots(agentID string) map[string]ToolOutputSnapshot {
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
		if event.Type != "tool.output" || event.Text == "" {
			continue
		}
		toolUseID, _ := event.Data["toolUseId"].(string)
		toolUseID = strings.TrimSpace(toolUseID)
		if toolUseID == "" {
			continue
		}
		snapshot := result[toolUseID]
		snapshot.Text, snapshot.Truncated = appendToolOutputSnapshot(snapshot.Text, event.Text, snapshot.Truncated)
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

func randomStreamSession() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic("generate stream session: " + err.Error())
	}
	return hex.EncodeToString(bytes[:])
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
		h.resyncAllLocked(current, ResyncSessionMismatch)
		current.session = h.config.NewSession()
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
	for sub := range current.subscribers {
		select {
		case sub.events <- event:
		default:
			h.resyncSubscriberLocked(current, sub, ResyncSubscriberOverrun)
		}
	}
	h.mu.Unlock()
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
			close(sub.events)
			close(sub.resync)
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
	for len(h.streams) >= h.config.MaxStreams {
		victimID, victim := h.oldestStreamLocked()
		if victim == nil {
			return nil
		}
		h.resyncAllLocked(victim, ResyncStreamEvicted)
		delete(h.streams, victimID)
	}
	current := &stream{
		session:      h.config.NewSession(),
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
	select {
	case sub.resync <- reason:
	default:
	}
	close(sub.events)
	close(sub.resync)
}
