package telemetry

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Event struct {
	Type      string         `json:"type"`
	Timestamp time.Time      `json:"timestamp"`
	Payload   map[string]any `json:"payload"`
	StreamID  string         `json:"stream_id,omitempty"`
	Sequence  uint64         `json:"sequence,omitempty"`
}

// Audience is a neutral routing description for realtime telemetry. Public
// Relay uses the empty partition. Extensions may map an opaque runtime
// partition and subject into these fields without teaching the core what they
// represent.
type Audience struct {
	Scoped      bool
	Partition   string
	Subject     string
	SubjectOnly bool
}

type AudienceResolver func(Event) Audience

// SubscriptionScope describes one visibility group. Format separates browser
// frames from internal/raw consumers so each representation is prepared once.
type SubscriptionScope struct {
	Partition string
	Subject   string
	Format    string
}

type Cursor struct {
	StreamID string
	Sequence uint64
}

type Encoder func(Event) ([]byte, bool, error)

const (
	subscriberBuffer = 128
	dispatchBuffer   = 4096
	initialSequence  = uint64(1)
)

type subscriber struct {
	events     chan []byte
	overflowed atomic.Bool
	mu         sync.RWMutex
	closed     bool
}

func newSubscriber() *subscriber {
	return &subscriber{events: make(chan []byte, subscriberBuffer)}
}

func (s *subscriber) deliver(payload []byte) {
	if s == nil {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	select {
	case s.events <- payload:
	default:
		s.overflowed.Store(true)
	}
}

func (s *subscriber) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.events)
}

type groupKey struct {
	partition string
	subject   string
	format    string
}

type subscriberGroup struct {
	key         groupKey
	streamID    string
	sequence    uint64
	encoder     Encoder
	subscribers map[*subscriber]struct{}
}

type dispatchItem struct {
	event   *Event
	barrier chan struct{}
}

type Hub struct {
	mu             sync.RWMutex
	groups         map[groupKey]*subscriberGroup
	partitions     map[string]map[groupKey]*subscriberGroup
	events         []Event
	resolver       AudienceResolver
	dispatch       chan dispatchItem
	done           chan struct{}
	closeOnce      sync.Once
	version        atomic.Uint64
	globalOverflow atomic.Uint64
}

func NewHub() *Hub {
	h := &Hub{
		groups:     make(map[groupKey]*subscriberGroup),
		partitions: make(map[string]map[groupKey]*subscriberGroup),
		dispatch:   make(chan dispatchItem, dispatchBuffer),
		done:       make(chan struct{}),
	}
	go h.run()
	return h
}

// SetAudienceResolver installs the one event-to-audience classifier used by
// the dispatcher. Classification happens once per event, never per socket.
func (h *Hub) SetAudienceResolver(resolver AudienceResolver) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.resolver = resolver
	h.mu.Unlock()
}

// Version changes synchronously for every publish attempt. Snapshot caches can
// therefore reject stale entries even while delivery remains asynchronous.
func (h *Hub) Version() uint64 {
	if h == nil {
		return 0
	}
	return h.version.Load()
}

// Publish never waits for subscriber work. A saturated dispatcher marks all
// subscribers out of sync so they reconnect from an authoritative baseline.
func (h *Hub) Publish(event Event) {
	if h == nil {
		return
	}
	event.Timestamp = time.Now().UTC()
	h.version.Add(1)
	select {
	case <-h.done:
		return
	default:
	}
	select {
	case h.dispatch <- dispatchItem{event: &event}:
	default:
		h.globalOverflow.Add(1)
	}
}

func (h *Hub) Subscribe() (chan []byte, func()) {
	ch, _, cancel := h.SubscribeWithOverflow()
	return ch, cancel
}

// SubscribeWithOverflow preserves the raw single-scope interface used by
// internal preview streams and tests.
func (h *Hub) SubscribeWithOverflow() (chan []byte, func() bool, func()) {
	ch, overflowed, cancel, _ := h.SubscribePrepared(SubscriptionScope{Format: "raw-v1"}, func(event Event) ([]byte, bool, error) {
		payload, err := json.Marshal(event)
		return payload, err == nil, err
	})
	return ch, overflowed, cancel
}

// SubscribePrepared joins a visibility group. The encoder is invoked once for
// the entire group/event, and every connection receives the same immutable
// bytes. The returned cursor is the baseline cursor for a new connection.
func (h *Hub) SubscribePrepared(scope SubscriptionScope, encoder Encoder) (chan []byte, func() bool, func(), Cursor) {
	if h == nil {
		closed := make(chan []byte)
		close(closed)
		return closed, func() bool { return true }, func() {}, Cursor{}
	}
	scope.Partition = strings.TrimSpace(scope.Partition)
	scope.Subject = strings.TrimSpace(scope.Subject)
	scope.Format = strings.TrimSpace(scope.Format)
	if scope.Format == "" {
		scope.Format = "default"
	}
	if encoder == nil {
		encoder = func(event Event) ([]byte, bool, error) {
			payload, err := json.Marshal(event)
			return payload, err == nil, err
		}
	}
	key := groupKey{partition: scope.Partition, subject: scope.Subject, format: scope.Format}
	sub := newSubscriber()
	h.mu.Lock()
	group := h.groups[key]
	if group == nil {
		group = &subscriberGroup{
			key:         key,
			streamID:    newStreamID(),
			sequence:    initialSequence,
			encoder:     encoder,
			subscribers: make(map[*subscriber]struct{}),
		}
		h.groups[key] = group
		partitionGroups := h.partitions[key.partition]
		if partitionGroups == nil {
			partitionGroups = make(map[groupKey]*subscriberGroup)
			h.partitions[key.partition] = partitionGroups
		}
		partitionGroups[key] = group
	}
	group.subscribers[sub] = struct{}{}
	cursor := Cursor{StreamID: group.streamID, Sequence: group.sequence}
	overflowGeneration := h.globalOverflow.Load()
	h.mu.Unlock()

	overflowed := func() bool {
		global := h.globalOverflow.Load()
		globalChanged := global != overflowGeneration
		overflowGeneration = global
		return globalChanged || sub.overflowed.Swap(false)
	}
	cancel := func() {
		h.mu.Lock()
		if current := h.groups[key]; current != nil {
			delete(current.subscribers, sub)
			if len(current.subscribers) == 0 {
				delete(h.groups, key)
				if partitionGroups := h.partitions[key.partition]; partitionGroups != nil {
					delete(partitionGroups, key)
					if len(partitionGroups) == 0 {
						delete(h.partitions, key.partition)
					}
				}
			}
		}
		h.mu.Unlock()
		sub.close()
	}
	return sub.events, overflowed, cancel, cursor
}

func (h *Hub) Events() []Event {
	if h == nil {
		return nil
	}
	h.Flush()
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]Event, len(h.events))
	copy(out, h.events)
	return out
}

// Flush is primarily a deterministic test/diagnostic barrier.
func (h *Hub) Flush() {
	if h == nil {
		return
	}
	barrier := make(chan struct{})
	select {
	case <-h.done:
		return
	case h.dispatch <- dispatchItem{barrier: barrier}:
	}
	select {
	case <-h.done:
	case <-barrier:
	}
}

func (h *Hub) Close() {
	if h == nil {
		return
	}
	h.closeOnce.Do(func() {
		close(h.done)
		h.mu.Lock()
		for _, group := range h.groups {
			for sub := range group.subscribers {
				sub.close()
			}
		}
		h.groups = make(map[groupKey]*subscriberGroup)
		h.partitions = make(map[string]map[groupKey]*subscriberGroup)
		h.mu.Unlock()
	})
}

func (h *Hub) run() {
	for {
		select {
		case <-h.done:
			return
		case item := <-h.dispatch:
			if item.barrier != nil {
				close(item.barrier)
				continue
			}
			if item.event != nil {
				h.dispatchEvent(*item.event)
			}
		}
	}
}

func (h *Hub) dispatchEvent(event Event) {
	h.mu.Lock()
	h.events = append(h.events, event)
	if len(h.events) > 200 {
		h.events = h.events[len(h.events)-200:]
	}
	resolver := h.resolver
	h.mu.Unlock()

	audience := Audience{}
	if resolver != nil {
		audience = resolver(event)
	}
	audience.Partition = strings.TrimSpace(audience.Partition)
	audience.Subject = strings.TrimSpace(audience.Subject)

	groups := h.groupsForAudience(audience)
	for _, group := range groups {
		h.dispatchToGroup(group, event)
	}
}

func (h *Hub) groupsForAudience(audience Audience) []*subscriberGroup {
	h.mu.RLock()
	defer h.mu.RUnlock()
	groups := make([]*subscriberGroup, 0)
	if !audience.Scoped {
		groups = make([]*subscriberGroup, 0, len(h.groups))
		for _, group := range h.groups {
			groups = append(groups, group)
		}
		return groups
	}
	for _, group := range h.partitions[audience.Partition] {
		if audience.SubjectOnly && group.key.subject != "" && !strings.EqualFold(group.key.subject, audience.Subject) {
			continue
		}
		groups = append(groups, group)
	}
	return groups
}

func (h *Hub) dispatchToGroup(group *subscriberGroup, event Event) {
	if group == nil {
		return
	}
	h.mu.RLock()
	current := h.groups[group.key]
	if current != group || len(group.subscribers) == 0 {
		h.mu.RUnlock()
		return
	}
	nextSequence := group.sequence + 1
	streamID := group.streamID
	encoder := group.encoder
	h.mu.RUnlock()

	event.StreamID = streamID
	event.Sequence = nextSequence
	payload, allowed, err := encoder(event)
	if err != nil {
		h.markGroupOverflow(group)
		return
	}
	if !allowed {
		return
	}

	h.mu.Lock()
	current = h.groups[group.key]
	if current != group || len(group.subscribers) == 0 {
		h.mu.Unlock()
		return
	}
	group.sequence = nextSequence
	subs := make([]*subscriber, 0, len(group.subscribers))
	for sub := range group.subscribers {
		subs = append(subs, sub)
	}
	h.mu.Unlock()
	for _, sub := range subs {
		sub.deliver(payload)
	}
}

func (h *Hub) markGroupOverflow(group *subscriberGroup) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if current := h.groups[group.key]; current == group {
		for sub := range group.subscribers {
			sub.overflowed.Store(true)
		}
	}
}

func newStreamID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("%x", time.Now().UnixNano())
}
