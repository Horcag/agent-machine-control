package events_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/events"
)

const (
	testOpID1 = "op-00000000000000000000000000000001"
	testOpID2 = "op-00000000000000000000000000000002"
	testOpID3 = "op-00000000000000000000000000000003"
	testOpID4 = "op-00000000000000000000000000000004"
)

func TestHub_PublishAndSubscribe(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	hub := events.NewHub(dir, events.WithClock(func() time.Time { return now }))

	ctx := t.Context()
	opID := testOpID1

	// Publish 2 initial events
	_, err := hub.Publish(ctx, events.Event{
		OperationID: opID,
		EventType:   "state_change",
		State:       domain.OpStatePending,
	})
	if err != nil {
		t.Fatalf("Publish 1 failed: %v", err)
	}

	_, err = hub.Publish(ctx, events.Event{
		OperationID: opID,
		EventType:   "state_change",
		State:       domain.OpStateAdmitted,
	})
	if err != nil {
		t.Fatalf("Publish 2 failed: %v", err)
	}

	// Subscribe after seq 1 -> should replay seq 2
	ch, unsub, err := hub.Subscribe(ctx, opID, 1)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer unsub()

	ev1 := <-ch
	if ev1.Sequence != 2 || ev1.State != domain.OpStateAdmitted {
		t.Fatalf("expected replayed event seq 2 admitted, got seq %d state %s", ev1.Sequence, ev1.State)
	}

	// Publish live event 3
	_, err = hub.Publish(ctx, events.Event{
		OperationID: opID,
		EventType:   "state_change",
		State:       domain.OpStateRunning,
	})
	if err != nil {
		t.Fatalf("Publish 3 failed: %v", err)
	}

	ev2 := <-ch
	if ev2.Sequence != 3 || ev2.State != domain.OpStateRunning {
		t.Fatalf("expected live event seq 3 running, got seq %d state %s", ev2.Sequence, ev2.State)
	}
}

func TestHub_SlowConsumer_OverflowDisconnect(t *testing.T) {
	dir := t.TempDir()
	hub := events.NewHub(dir)
	ctx := context.Background()
	opID := testOpID2

	ch, unsub, err := hub.Subscribe(ctx, opID, 0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer unsub()

	// Fill the subscriber queue beyond TotalSubscriberCapacity (320) without reading
	for i := range 350 {
		_, err := hub.Publish(ctx, events.Event{
			OperationID: opID,
			EventType:   "progress",
			State:       domain.OpStateRunning,
			Progress:    float64(i),
		})
		if err != nil {
			t.Fatalf("Publish %d failed: %v", i, err)
		}
	}

	// Drain channel; should eventually see overflow event or closed channel
	var sawOverflow bool
	for ev := range ch {
		if ev.EventType == "overflow" {
			sawOverflow = true
		}
	}

	if !sawOverflow {
		t.Logf("channel closed without overflow event in drained queue, buffer was full and disconnected")
	}
}

func TestHub_RestartSequenceContinuity(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	opID := testOpID3

	// Hub instance 1 publishes 5 events
	hub1 := events.NewHub(dir)
	for range 5 {
		_, err := hub1.Publish(ctx, events.Event{
			OperationID: opID,
			EventType:   "progress",
			State:       domain.OpStateRunning,
		})
		if err != nil {
			t.Fatalf("Publish failed: %v", err)
		}
	}

	// Hub instance 2 (simulating restart) publishes next event
	hub2 := events.NewHub(dir)
	ev6, err := hub2.Publish(ctx, events.Event{
		OperationID: opID,
		EventType:   "state_change",
		State:       domain.OpStateCompleted,
	})
	if err != nil {
		t.Fatalf("Publish on hub2 failed: %v", err)
	}

	if ev6.Sequence != 6 {
		t.Fatalf("expected sequence 6 after restart, got %d", ev6.Sequence)
	}
}

func TestHub_BoundedDiskRetention(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	opID := testOpID4

	hub := events.NewHub(dir)
	// Publish 300 events
	for i := range 300 {
		_, err := hub.Publish(ctx, events.Event{
			OperationID: opID,
			EventType:   "progress",
			State:       domain.OpStateRunning,
			Progress:    float64(i),
		})
		if err != nil {
			t.Fatalf("Publish %d failed: %v", i, err)
		}
	}

	// Check line count in disk file
	eventsPath := filepath.Join(dir, fmt.Sprintf("%s.events.jsonl", opID))
	content, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("failed to read events file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != events.MaxReplayBuffer {
		t.Fatalf("expected exactly %d retained events on disk, got %d", events.MaxReplayBuffer, len(lines))
	}
}

func TestHub_ConcurrentPublishAndSubscribe_Race(t *testing.T) {
	dir := t.TempDir()
	hub := events.NewHub(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	opID := "op-000000000000000000000000000000aa"
	var wg sync.WaitGroup

	// 5 subscribers
	for range 5 {
		wg.Add(1)
		go func() { //nolint:modernize // waitgroupgo
			defer wg.Done()
			ch, unsub, err := hub.Subscribe(ctx, opID, 0)
			if err != nil {
				return
			}
			defer unsub()
			for {
				select {
				case _, ok := <-ch:
					if !ok {
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// 5 concurrent publishers
	for p := range 5 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := range 20 {
				_, _ = hub.Publish(ctx, events.Event{
					OperationID: opID,
					EventType:   "progress",
					State:       domain.OpStateRunning,
					Message:     fmt.Sprintf("worker %d step %d", worker, i),
				})
			}
		}(p)
	}

	wg.Wait()
}

func TestFormatAndParseSSE(t *testing.T) {
	ev := events.Event{
		Sequence:    12,
		Timestamp:   time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
		OperationID: "op-000000000000000000000000000000bb",
		EventType:   "state_change",
		State:       domain.OpStateCompleted,
		Message:     "done",
	}

	formatted, err := events.FormatSSE(ev)
	if err != nil {
		t.Fatalf("FormatSSE failed: %v", err)
	}

	// Extract data: line
	var dataLine []byte
	for line := range bytes.SplitSeq(formatted, []byte("\n")) {
		if data, ok := bytes.CutPrefix(line, []byte("data: ")); ok {
			dataLine = data
			break
		}
	}

	parsed, err := events.ParseSSE(dataLine)
	if err != nil {
		t.Fatalf("ParseSSE failed: %v", err)
	}

	if parsed.Sequence != ev.Sequence || parsed.OperationID != ev.OperationID || parsed.State != ev.State {
		t.Errorf("parsed event mismatch: %+v", parsed)
	}
}

func TestHub_GlobalSubscription(t *testing.T) {
	dir := t.TempDir()
	hub := events.NewHub(dir)
	ctx := t.Context()

	ch, unsub := hub.SubscribeGlobal(ctx)
	defer unsub()

	opID := "op-000000000000000000000000000000cc"
	_, err := hub.Publish(ctx, events.Event{
		OperationID: opID,
		EventType:   "state_change",
		State:       domain.OpStatePending,
	})
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	select {
	case ev := <-ch:
		if ev.OperationID != opID {
			t.Errorf("expected %s, got %s", opID, ev.OperationID)
		}
	case <-time.After(time.Second):
		t.Errorf("timed out waiting for global event")
	}
}

func TestHub_SSEFormattingAndErrors(t *testing.T) {
	ev := domain.Event{
		Sequence:    1,
		OperationID: "op-000000000000000000000000000000dd",
		EventType:   "state_change",
		State:       domain.OpStatePending,
		Timestamp:   time.Now().UTC(),
	}

	data, err := events.FormatSSE(ev)
	if err != nil {
		t.Fatalf("FormatSSE failed: %v", err)
	}

	lines := bytes.Split(data, []byte("\n"))
	var dataLine []byte
	for _, l := range lines {
		if data, ok := bytes.CutPrefix(l, []byte("data: ")); ok {
			dataLine = data
			break
		}
	}

	parsed, err := events.ParseSSE(dataLine)
	if err != nil {
		t.Fatalf("ParseSSE failed: %v", err)
	}
	if parsed.Sequence != 1 || parsed.OperationID != ev.OperationID {
		t.Errorf("parsed event mismatch: %+v", parsed)
	}

	_, err = events.ParseSSE([]byte("invalid json"))
	if err == nil {
		t.Errorf("expected error parsing invalid SSE json")
	}
}

func TestHub_LoadHistoryFromDiskAndErrors(t *testing.T) {
	dir := t.TempDir()
	hub1 := events.NewHub(dir)

	opID := "op-000000000000000000000000000000ee"
	ctx := context.Background()

	_, err := hub1.Publish(ctx, events.Event{
		OperationID: opID,
		EventType:   "state_change",
		State:       domain.OpStatePending,
	})
	if err != nil {
		t.Fatalf("Publish 1 failed: %v", err)
	}
	_, err = hub1.Publish(ctx, events.Event{
		OperationID: opID,
		EventType:   "state_change",
		State:       domain.OpStateAdmitted,
	})
	if err != nil {
		t.Fatalf("Publish 2 failed: %v", err)
	}

	// Create new Hub with same dir to test disk history loading
	hub2 := events.NewHub(dir)
	ch, unsub, err := hub2.Subscribe(ctx, opID, 0)
	if err != nil {
		t.Fatalf("Subscribe on hub2 failed: %v", err)
	}
	defer unsub()

	ev1 := <-ch
	if ev1.State != domain.OpStatePending {
		t.Errorf("expected pending, got %s", ev1.State)
	}
	ev2 := <-ch
	if ev2.State != domain.OpStateAdmitted {
		t.Errorf("expected admitted, got %s", ev2.State)
	}

	// Empty op ID
	_, _, err = hub2.Subscribe(ctx, "", 0)
	if !errors.Is(err, domain.ErrInvalidOperationID) {
		t.Errorf("expected ErrInvalidOperationID, got %v", err)
	}
}

func TestEvent_ParseSSE_CorruptAndTrailing(t *testing.T) {
	// Corrupt JSON
	if _, err := events.ParseSSE([]byte("corrupt-data")); err == nil {
		t.Errorf("expected error for corrupt JSON in ParseSSE")
	}

	// Trailing data
	if _, err := events.ParseSSE([]byte(`{"sequence":1,"event_type":"progress"} extra`)); err == nil {
		t.Errorf("expected error for trailing data in ParseSSE")
	}
}

func TestHub_GlobalSlowSubscriberOverflow(t *testing.T) {
	dir := t.TempDir()
	hub := events.NewHub(dir)
	ctx := context.Background()

	ch, unsub := hub.SubscribeGlobal(ctx)
	defer unsub()

	opID := "op-000000000000000000000000000000ff"
	for i := range 350 {
		_, err := hub.Publish(ctx, events.Event{
			OperationID: opID,
			EventType:   "progress",
			State:       domain.OpStateRunning,
			Progress:    float64(i),
		})
		if err != nil {
			t.Fatalf("Publish %d failed: %v", i, err)
		}
	}

	var sawOverflow bool
	for ev := range ch {
		if ev.EventType == "overflow" {
			sawOverflow = true
		}
	}
	if !sawOverflow {
		t.Logf("global subscriber channel closed without overflow event, buffer full")
	}
}

func assertEventReceived(t *testing.T, ch <-chan events.Event) {
	t.Helper()
	select {
	case ev := <-ch:
		if ev.Timestamp.IsZero() {
			t.Errorf("ephemeral event missing timestamp")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("timeout waiting for ephemeral event")
	}
}

func assertChannelClosed(t *testing.T, ch <-chan events.Event) {
	t.Helper()
	select {
	case _, ok := <-ch:
		if ok {
			t.Errorf("expected channel to be closed")
		}
	case <-time.After(100 * time.Millisecond):
		t.Errorf("timeout waiting for channel close")
	}
}

func TestHub_ContractsAndLifecycle(t *testing.T) {
	dir := t.TempDir()
	hub := events.NewHub(dir) // no WithClock, testing default now()

	opID := "op-00000000000000000000000000000999"

	// test nil/non-cancelable context
	ctx := context.Background()
	opCh, unsubOp, err := hub.Subscribe(ctx, opID, 0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	globalCh, unsubGlobal := hub.SubscribeGlobal(ctx)

	// BroadcastEphemeral sets Timestamp via default nowFn and operates normally
	hub.BroadcastEphemeral(events.Event{
		OperationID: opID,
		EventType:   "ping",
	})

	assertEventReceived(t, opCh)
	assertEventReceived(t, globalCh)

	// explicit unsubscribe closes both channels
	unsubOp()
	unsubGlobal()

	assertChannelClosed(t, opCh)
	assertChannelClosed(t, globalCh)

	// CloseAll is idempotent
	if err := hub.CloseAll(); err != nil {
		t.Fatalf("CloseAll failed: %v", err)
	}
	if err := hub.CloseAll(); err != nil {
		t.Fatalf("CloseAll second call failed: %v", err)
	}

	// post-close global subscription is immediately EOF
	ch2, unsub2 := hub.SubscribeGlobal(ctx)
	defer unsub2()

	assertChannelClosed(t, ch2)

	// BroadcastEphemeral after Close is a no-op
	hub.BroadcastEphemeral(events.Event{
		OperationID: opID,
		EventType:   "pong",
	})

	// Ensure nothing was pushed to the closed channel
	select {
	case _, ok := <-ch2:
		if ok {
			t.Errorf("unexpected event received on closed channel")
		}
	default:
	}
}

func TestHub_BroadcastEphemeral_SlowGlobalSub(t *testing.T) {
	dir := t.TempDir()
	hub := events.NewHub(dir)
	ctx := context.Background()

	ch, unsub := hub.SubscribeGlobal(ctx)
	defer unsub()

	opID := "op-00000000000000000000000000000888"

	// Fill buffer completely
	for i := range 350 {
		hub.BroadcastEphemeral(events.Event{
			OperationID: opID,
			EventType:   "ping",
			Progress:    float64(i),
		})
	}

	// Read until EOF
	count := 0
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				if count >= 350 {
					t.Errorf("expected subscriber to be closed before receiving all 350 events, got %d", count)
				}
				return
			}
			count++
			if ev.EventType == "overflow" {
				t.Fatalf("did not expect overflow event for broadcast ephemeral")
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for slow subscriber channel to close")
		}
	}
}

func TestHub_ContextCancellation(t *testing.T) {
	dir := t.TempDir()
	hub := events.NewHub(dir)
	opID := "op-00000000000000000000000000000777"

	// Test cancellation for Subscribe
	ctx, cancel := context.WithCancel(context.Background())
	opCh, _, err := hub.Subscribe(ctx, opID, 0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// Test cancellation for SubscribeGlobal
	ctxGlobal, cancelGlobal := context.WithCancel(context.Background())
	globalCh, _ := hub.SubscribeGlobal(ctxGlobal)

	// Trigger cancellation
	cancel()
	cancelGlobal()

	// Prove cancellation by immediate eventual EOF on bounded selects
	select {
	case _, ok := <-opCh:
		if ok {
			t.Errorf("expected op channel to be closed on context cancel")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timeout waiting for op channel close after context cancel")
	}

	select {
	case _, ok := <-globalCh:
		if ok {
			t.Errorf("expected global channel to be closed on context cancel")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timeout waiting for global channel close after context cancel")
	}
}
