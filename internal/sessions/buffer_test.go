package sessions_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/sessions"
)

func TestRingBuffer_AppendAndRead(t *testing.T) {
	buf := sessions.NewRingBuffer(1024)
	now := time.Now().UTC()

	c1 := buf.Append("First Line\n", now)
	if c1.Seq != 1 {
		t.Errorf("expected seq 1, got %d", c1.Seq)
	}

	c2 := buf.Append("Second Line\n", now)
	if c2.Seq != 2 {
		t.Errorf("expected seq 2, got %d", c2.Seq)
	}

	// Read from beginning (afterSeq = 0)
	chunks, nextSeq, lossBytes, hasMore := buf.ReadAfter(0, 1024)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if nextSeq != 2 {
		t.Errorf("expected nextSeq 2, got %d", nextSeq)
	}
	if lossBytes != 0 {
		t.Errorf("expected lossBytes 0, got %d", lossBytes)
	}
	if hasMore {
		t.Error("expected hasMore false")
	}

	// Incremental read (afterSeq = 1)
	chunks, nextSeq, _, _ = buf.ReadAfter(1, 1024)
	if len(chunks) != 1 || chunks[0].Seq != 2 {
		t.Fatalf("expected chunk with seq 2, got %+v", chunks)
	}
	if nextSeq != 2 {
		t.Errorf("expected nextSeq 2, got %d", nextSeq)
	}
}

func TestRingBuffer_OverflowAndLossMarker(t *testing.T) {
	// Small buffer capacity: 50 bytes
	buf := sessions.NewRingBuffer(50)
	now := time.Now().UTC()

	buf.Append("1234567890\n", now) // 11 bytes, seq 1
	buf.Append("1234567890\n", now) // 11 bytes, seq 2
	buf.Append("1234567890\n", now) // 11 bytes, seq 3
	buf.Append("1234567890\n", now) // 11 bytes, seq 4
	buf.Append("1234567890\n", now) // 11 bytes, seq 5 -> 55 bytes total, triggers eviction of seq 1

	currentBytes, totalChunks, droppedBytes, _ := buf.Stats()
	if droppedBytes == 0 {
		t.Errorf("expected dropped bytes > 0, got %d (current %d, chunks %d)", droppedBytes, currentBytes, totalChunks)
	}

	// Requesting afterSeq = 0 (which was evicted) should return a loss marker
	chunks, _, lossBytes, _ := buf.ReadAfter(0, 1024)
	if lossBytes == 0 {
		t.Fatalf("expected lossBytes > 0, got %d", lossBytes)
	}
	if len(chunks) == 0 {
		t.Fatal("expected chunks including loss marker")
	}
	if !strings.Contains(chunks[0].Data, "DROPPED") {
		t.Errorf("expected loss marker in first chunk, got %q", chunks[0].Data)
	}
}

func TestRingBuffer_MultiReaderIndependence(t *testing.T) {
	buf := sessions.NewRingBuffer(4096)
	now := time.Now().UTC()

	for i := 1; i <= 10; i++ {
		buf.Append("data\n", now)
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// Reader 1: reads with afterSeq = 0
	go func() {
		defer wg.Done()
		chunks, nextSeq, _, _ := buf.ReadAfter(0, 1024)
		if len(chunks) != 10 || nextSeq != 10 {
			t.Errorf("Reader 1 failed: got %d chunks, nextSeq %d", len(chunks), nextSeq)
		}
	}()

	// Reader 2: reads with afterSeq = 5
	go func() {
		defer wg.Done()
		chunks, nextSeq, _, _ := buf.ReadAfter(5, 1024)
		if len(chunks) != 5 || nextSeq != 10 {
			t.Errorf("Reader 2 failed: got %d chunks, nextSeq %d", len(chunks), nextSeq)
		}
	}()

	wg.Wait()
}

func TestRingBuffer_HasMoreAndEmptyAppend(t *testing.T) {
	buf := sessions.NewRingBuffer(1024)
	now := time.Now().UTC()

	// Read on initial empty buffer
	chunks, nextSeq, loss, hasMore := buf.ReadAfter(0, 1024)
	if len(chunks) != 0 || nextSeq != 0 || loss != 0 || hasMore {
		t.Errorf("unexpected read on empty buffer: chunks=%d, nextSeq=%d", len(chunks), nextSeq)
	}

	buf.Append("chunk1\n", now)
	buf.Append("chunk2\n", now)

	// Read with limit 5 bytes (smaller than 14 bytes)
	chunks, nextSeq, _, hasMore = buf.ReadAfter(0, 5)
	if !hasMore || len(chunks) != 1 {
		t.Errorf("expected hasMore true and 1 chunk, got hasMore=%v, len=%d", hasMore, len(chunks))
	}
	if nextSeq != 1 {
		t.Errorf("expected nextSeq 1, got %d", nextSeq)
	}
}
