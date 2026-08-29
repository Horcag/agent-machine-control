package events

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

var (
	// ErrHubClosed indicates the event hub is closed and not accepting events.
	ErrHubClosed = errors.New("events: hub is closed")
)

type subscriber struct {
	mu     sync.Mutex
	id     uint64
	ch     chan Event
	done   chan struct{}
	closed bool
}

func (s *subscriber) send(ev Event) (overflowed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	select {
	case s.ch <- ev:
		return false
	default:
		s.closed = true
		overflowEv := Event{
			Sequence:    ev.Sequence + 1,
			Timestamp:   time.Now().UTC(),
			OperationID: ev.OperationID,
			EventType:   "overflow",
			Message:     "slow subscriber buffer overflow; disconnected",
		}
		select {
		case s.ch <- overflowEv:
		default:
		}
		close(s.ch)
		close(s.done)
		return true
	}
}

func (s *subscriber) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.ch)
		close(s.done)
	}
}

// Lock hierarchy and concurrency invariants:
//
//  1. Hub.mu (sync.RWMutex) guards stream registry (Hub.streams), global subscriber map
//     (Hub.globalSubs), and the Hub.closed transition.
//  2. opStream.mu (sync.Mutex) guards per-operation sequence ordering (opStream.lastSeq),
//     replay history (opStream.history), subscriber map (opStream.subscribers), and opStream.closed.
//  3. subscriber.mu (sync.Mutex) guards subscriber channel delivery and close state (subscriber.closed).
//
// Lock ordering is strictly top-down: Hub.mu -> opStream.mu -> subscriber.mu.
// Code holding opStream.mu or subscriber.mu must NEVER acquire Hub.mu.
//
// Close linearizability:
// Close acquires Hub.mu, sets Hub.closed to true, snapshots and clears both Hub.streams
// and Hub.globalSubs. It then releases Hub.mu and sequentially locks each opStream.mu to set
// opStream.closed = true and close all active subscribers. Finally, it closes all global subscribers.
// Any Subscribe or Publish operation admitted to an opStream prior to stream closure finishes
// safely under opStream.mu. Any operation arriving at opStream.mu after Close has acquired it
// detects opStream.closed == true and rejects the operation with ErrHubClosed.
// SubscribeGlobal acquires Hub.mu and immediately closes the subscriber if Hub.closed is true.
type opStream struct {
	mu          sync.Mutex
	lastSeq     uint64
	history     []Event
	subscribers map[uint64]*subscriber
	closed      bool
}

// Hub manages structured event publishing, replay buffering, disk logging, and fan-out.
type Hub struct {
	operationsDir string
	nowFn         func() time.Time
	mu            sync.RWMutex
	streams       map[string]*opStream
	globalSubs    map[uint64]*subscriber
	subCounter    atomic.Uint64
	closed        atomic.Bool
}

// Option configures Hub dependencies.
type Option func(*Hub)

// WithClock sets a custom clock function for the Hub.
func WithClock(fn func() time.Time) Option {
	return func(h *Hub) {
		h.nowFn = fn
	}
}

// NewHub creates a new Hub for the given operations directory.
func NewHub(operationsDir string, opts ...Option) *Hub {
	h := &Hub{
		operationsDir: operationsDir,
		nowFn:         time.Now,
		streams:       make(map[string]*opStream),
		globalSubs:    make(map[uint64]*subscriber),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

func (h *Hub) now() time.Time {
	if h.nowFn != nil {
		return h.nowFn().UTC()
	}
	return time.Now().UTC()
}

func loadOrCreateStream(operationsDir, opID string) (*opStream, error) {
	s := &opStream{
		subscribers: make(map[uint64]*subscriber),
	}
	if operationsDir == "" {
		return s, nil
	}
	history, err := loadHistoryFromDisk(operationsDir, opID)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("events: failed to load history from disk: %w", err)
	}
	s.history = history
	for _, ev := range history {
		if ev.Sequence > s.lastSeq {
			s.lastSeq = ev.Sequence
		}
	}
	return s, nil
}

func (h *Hub) getOrCreateStream(opID string) (*opStream, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed.Load() {
		return nil, ErrHubClosed
	}

	if s, ok := h.streams[opID]; ok {
		return s, nil
	}

	s, err := loadOrCreateStream(h.operationsDir, opID)
	if err != nil {
		return nil, err
	}
	h.streams[opID] = s
	return s, nil
}

// Publish assigns a sequence, saves the event to disk, updates replay buffer, and broadcasts.
func (h *Hub) Publish(_ context.Context, ev Event) (Event, error) {
	if h.closed.Load() {
		return Event{}, ErrHubClosed
	}
	if err := domain.ValidateOperationID(ev.OperationID); err != nil {
		return Event{}, err
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = h.now()
	}

	stream, err := h.getOrCreateStream(ev.OperationID)
	if err != nil {
		return Event{}, err
	}

	h.mu.RLock()
	if h.closed.Load() {
		h.mu.RUnlock()
		return Event{}, ErrHubClosed
	}

	stream.mu.Lock()
	if stream.closed {
		stream.mu.Unlock()
		h.mu.RUnlock()
		return Event{}, ErrHubClosed
	}
	stream.lastSeq++
	ev.Sequence = stream.lastSeq

	if err := ev.Validate(); err != nil {
		stream.mu.Unlock()
		h.mu.RUnlock()
		return Event{}, err
	}

	if err := persistEventToDisk(h.operationsDir, ev); err != nil {
		stream.mu.Unlock()
		h.mu.RUnlock()
		return Event{}, err
	}

	stream.history = append(stream.history, ev)
	if len(stream.history) > MaxReplayBuffer {
		stream.history = stream.history[len(stream.history)-MaxReplayBuffer:]
	}

	h.broadcastToStreamLocked(stream, ev)
	stream.mu.Unlock()

	var slowSubIDs []uint64
	for _, sub := range h.globalSubs {
		overflowed := sub.send(ev)
		if overflowed {
			slowSubIDs = append(slowSubIDs, sub.id)
		}
	}
	h.mu.RUnlock()

	if len(slowSubIDs) > 0 {
		h.mu.Lock()
		for _, id := range slowSubIDs {
			delete(h.globalSubs, id)
		}
		h.mu.Unlock()
	}

	return ev, nil
}

// BroadcastEphemeral broadcasts an event in-memory to stream and global subscribers without disk persistence.
func (h *Hub) BroadcastEphemeral(ev Event) {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = h.now()
	}

	h.mu.RLock()
	if h.closed.Load() {
		h.mu.RUnlock()
		return
	}

	stream := h.streams[ev.OperationID]
	if stream != nil {
		stream.mu.Lock()
		if !stream.closed {
			h.broadcastToStreamLocked(stream, ev)
		}
		stream.mu.Unlock()
	}

	var slowSubIDs []uint64
	for _, sub := range h.globalSubs {
		overflowed := sub.send(ev)
		if overflowed {
			slowSubIDs = append(slowSubIDs, sub.id)
		}
	}
	h.mu.RUnlock()

	if len(slowSubIDs) > 0 {
		h.mu.Lock()
		for _, id := range slowSubIDs {
			delete(h.globalSubs, id)
		}
		h.mu.Unlock()
	}
}

func (h *Hub) broadcastToStreamLocked(stream *opStream, ev Event) {
	var slowSubIDs []uint64
	for id, sub := range stream.subscribers {
		overflowed := sub.send(ev)
		if overflowed {
			slowSubIDs = append(slowSubIDs, id)
		}
	}
	for _, id := range slowSubIDs {
		delete(stream.subscribers, id)
	}
}

// Subscribe returns an event channel delivering replayed events > afterSeq, followed by live events.
func (h *Hub) Subscribe(ctx context.Context, opID string, afterSeq uint64) (<-chan Event, func(), error) {
	if h.closed.Load() {
		return nil, nil, ErrHubClosed
	}
	if err := domain.ValidateOperationID(opID); err != nil {
		return nil, nil, err
	}

	stream, err := h.getOrCreateStream(opID)
	if err != nil {
		return nil, nil, err
	}

	stream.mu.Lock()
	defer stream.mu.Unlock()

	if stream.closed {
		return nil, nil, ErrHubClosed
	}

	subID := h.subCounter.Add(1)
	sub := &subscriber{
		id:   subID,
		ch:   make(chan Event, TotalSubscriberCapacity),
		done: make(chan struct{}),
	}

	// Replay history
	history := stream.history
	if len(history) == 0 && h.operationsDir != "" {
		loaded, loadErr := loadHistoryFromDisk(h.operationsDir, opID)
		if loadErr != nil && !os.IsNotExist(loadErr) {
			return nil, nil, fmt.Errorf("events: failed to load history for subscription: %w", loadErr)
		}
		history = loaded
	}

	for _, ev := range history {
		if ev.Sequence > afterSeq {
			sub.ch <- ev
		}
	}

	stream.subscribers[subID] = sub

	unsub := func() {
		stream.mu.Lock()
		delete(stream.subscribers, subID)
		stream.mu.Unlock()
		sub.close()
	}

	if ctx != nil && ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				unsub()
			case <-sub.done:
			}
		}()
	}

	return sub.ch, unsub, nil
}

// SubscribeGlobal subscribes to all published events across all operations.
func (h *Hub) SubscribeGlobal(ctx context.Context) (<-chan Event, func()) {
	subID := h.subCounter.Add(1)
	sub := &subscriber{
		id:   subID,
		ch:   make(chan Event, TotalSubscriberCapacity),
		done: make(chan struct{}),
	}

	h.mu.Lock()
	if h.closed.Load() {
		h.mu.Unlock()
		sub.close()
		return sub.ch, func() {}
	}

	h.globalSubs[subID] = sub
	h.mu.Unlock()

	unsub := func() {
		h.mu.Lock()
		delete(h.globalSubs, subID)
		h.mu.Unlock()
		sub.close()
	}

	if ctx != nil && ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				unsub()
			case <-sub.done:
			}
		}()
	}

	return sub.ch, unsub
}

// Close safely detaches and closes all operation and global subscribers without races.
func (h *Hub) Close() error {
	h.mu.Lock()
	if h.closed.Load() {
		h.mu.Unlock()
		return nil
	}
	h.closed.Store(true)

	streams := make([]*opStream, 0, len(h.streams))
	for _, stream := range h.streams {
		streams = append(streams, stream)
	}
	h.streams = make(map[string]*opStream)

	subs := make([]*subscriber, 0, len(h.globalSubs))
	for _, sub := range h.globalSubs {
		subs = append(subs, sub)
	}
	h.globalSubs = make(map[uint64]*subscriber)
	h.mu.Unlock()

	for _, stream := range streams {
		stream.mu.Lock()
		stream.closed = true
		for id, sub := range stream.subscribers {
			sub.close()
			delete(stream.subscribers, id)
		}
		stream.mu.Unlock()
	}

	for _, sub := range subs {
		sub.close()
	}

	return nil
}

// CloseAll is an alias for Close.
func (h *Hub) CloseAll() error {
	return h.Close()
}
