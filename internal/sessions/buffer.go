package sessions

import (
	"fmt"
	"sync"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

const (
	// DefaultRingBufferCapBytes is the default capacity for in-memory terminal scrollback (1 MB).
	DefaultRingBufferCapBytes = 1024 * 1024
	// DefaultMaxReadLimitBytes is the default maximum read chunk limit (64 KB).
	DefaultMaxReadLimitBytes = 64 * 1024
)

// RingBuffer manages an in-memory bounded buffer of terminal output chunks.
type RingBuffer struct {
	mu           sync.RWMutex
	maxBytes     int
	currentBytes int
	chunks       []domain.SessionChunk
	nextSeq      uint64
	droppedBytes uint64
	droppedSeq   uint64
	changeChans  map[chan struct{}]struct{}
}

// NewRingBuffer initializes a RingBuffer with the given maximum byte capacity.
func NewRingBuffer(maxBytes int) *RingBuffer {
	if maxBytes <= 0 {
		maxBytes = DefaultRingBufferCapBytes
	}
	return &RingBuffer{
		maxBytes:    maxBytes,
		nextSeq:     1,
		changeChans: make(map[chan struct{}]struct{}),
	}
}

// Append adds a new string slice to the buffer and broadcasts to change listeners.
func (b *RingBuffer) Append(data string, ts time.Time) domain.SessionChunk {
	b.mu.Lock()
	defer b.mu.Unlock()

	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	seq := b.nextSeq
	b.nextSeq++

	chunk := domain.SessionChunk{
		Seq:       seq,
		Timestamp: ts,
		Data:      data,
	}

	b.chunks = append(b.chunks, chunk)
	b.currentBytes += len(data)

	// Evict oldest chunks if capacity exceeded
	for b.currentBytes > b.maxBytes && len(b.chunks) > 1 {
		oldest := b.chunks[0]
		b.chunks = b.chunks[1:]
		b.currentBytes -= len(oldest.Data)
		b.droppedBytes += uint64(len(oldest.Data))
		b.droppedSeq = oldest.Seq
	}

	// Broadcast update
	for ch := range b.changeChans {
		select {
		case ch <- struct{}{}:
		default:
		}
	}

	return chunk
}

// ReadAfter returns chunks occurring strictly after afterSeq, up to limitBytes.
func (b *RingBuffer) ReadAfter(afterSeq uint64, limitBytes int) ([]domain.SessionChunk, uint64, uint64, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if limitBytes <= 0 || limitBytes > DefaultMaxReadLimitBytes {
		limitBytes = DefaultMaxReadLimitBytes
	}

	if len(b.chunks) == 0 {
		return nil, afterSeq, 0, false
	}

	earliestSeq := b.chunks[0].Seq
	var result []domain.SessionChunk
	var lossBytes uint64

	// If client requested a sequence that was already evicted
	if afterSeq < earliestSeq && b.droppedBytes > 0 {
		lossBytes = b.droppedBytes
		lossMarker := domain.SessionChunk{
			Seq:       earliestSeq - 1,
			Timestamp: b.chunks[0].Timestamp,
			Data:      fmt.Sprintf("[... DROPPED %d BYTES OF TERMINAL OUTPUT ...]\r\n", b.droppedBytes),
			LossBytes: b.droppedBytes,
		}
		result = append(result, lossMarker)
		afterSeq = lossMarker.Seq
	}

	bytesAccum := 0
	nextSeq := afterSeq
	hasMore := false

	for _, c := range b.chunks {
		if c.Seq > afterSeq {
			chunkLen := len(c.Data)
			if bytesAccum+chunkLen > limitBytes && len(result) > 0 {
				hasMore = true
				break
			}
			result = append(result, c)
			bytesAccum += chunkLen
			nextSeq = c.Seq
		}
	}

	return result, nextSeq, lossBytes, hasMore
}

// RegisterChangeChan registers a channel to receive notifications on buffer appends.
func (b *RingBuffer) RegisterChangeChan(ch chan struct{}) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.changeChans[ch] = struct{}{}
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.changeChans, ch)
	}
}

// Stats returns current buffer memory and drop metrics.
func (b *RingBuffer) Stats() (currentBytes int, totalChunks int, droppedBytes uint64, nextSeq uint64) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.currentBytes, len(b.chunks), b.droppedBytes, b.nextSeq
}
