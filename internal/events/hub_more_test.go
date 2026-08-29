package events_test

import (
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

func TestHub_SubscribeReplayAll256Events(t *testing.T) {
	dir := t.TempDir()
	hub := events.NewHub(dir)
	ctx := context.Background()
	opID := "op-00000000000000000000000000000101"

	// Publish exactly 256 events (the full retention buffer)
	for i := 1; i <= 256; i++ {
		_, err := hub.Publish(ctx, events.Event{
			OperationID: opID,
			EventType:   "progress",
			State:       domain.OpStateRunning,
			Progress:    float64(i),
		})
		if err != nil {
			t.Fatalf("Publish event %d failed: %v", i, err)
		}
	}

	// Subscribe from sequence 0 -> must replay ALL 256 events without failing on buffer overflow!
	ch, unsub, err := hub.Subscribe(ctx, opID, 0)
	if err != nil {
		t.Fatalf("Subscribe for all 256 events failed: %v", err)
	}
	defer unsub()

	count := 0
	for i := 1; i <= 256; i++ {
		select {
		case ev := <-ch:
			if ev.Sequence != uint64(i) {
				t.Fatalf("expected sequence %d, got %d", i, ev.Sequence)
			}
			count++
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out reading replayed event %d", i)
		}
	}

	if count != 256 {
		t.Fatalf("expected 256 replayed events, got %d", count)
	}
}

func TestHub_HistoryFailClosed_CorruptJSON(t *testing.T) {
	dir := t.TempDir()
	opID := "op-00000000000000000000000000000102"
	eventsPath := filepath.Join(dir, fmt.Sprintf("%s.events.jsonl", opID))

	// Write valid first line and corrupted second line
	validEv := `{"sequence":1,"timestamp":"2026-08-29T12:00:00Z","operation_id":"op-00000000000000000000000000000102","event_type":"state_change","state":"pending"}`
	corruptContent := validEv + "\n{corrupt-json-line}\n"
	if err := os.WriteFile(eventsPath, []byte(corruptContent), 0600); err != nil {
		t.Fatalf("failed to write corrupt file: %v", err)
	}

	hub := events.NewHub(dir)
	ctx := context.Background()

	// Subscribe must fail closed
	_, _, err := hub.Subscribe(ctx, opID, 0)
	if err == nil {
		t.Errorf("expected Subscribe to fail on corrupt history")
	}

	// Publish must fail closed
	_, err = hub.Publish(ctx, events.Event{
		OperationID: opID,
		EventType:   "state_change",
		State:       domain.OpStateAdmitted,
	})
	if err == nil {
		t.Errorf("expected Publish to fail on corrupt history")
	}
}

func TestHub_HistoryFailClosed_NonMonotonicSequence(t *testing.T) {
	dir := t.TempDir()
	opID := "op-00000000000000000000000000000103"
	eventsPath := filepath.Join(dir, fmt.Sprintf("%s.events.jsonl", opID))

	// Sequence 1 followed by duplicate sequence 1
	ev1 := `{"sequence":1,"timestamp":"2026-08-29T12:00:00Z","operation_id":"op-00000000000000000000000000000103","event_type":"state_change","state":"pending"}`
	ev2 := `{"sequence":1,"timestamp":"2026-08-29T12:00:01Z","operation_id":"op-00000000000000000000000000000103","event_type":"state_change","state":"admitted"}`
	if err := os.WriteFile(eventsPath, []byte(ev1+"\n"+ev2+"\n"), 0600); err != nil {
		t.Fatalf("failed to write non-monotonic file: %v", err)
	}

	hub := events.NewHub(dir)
	ctx := context.Background()

	_, _, err := hub.Subscribe(ctx, opID, 0)
	if err == nil {
		t.Errorf("expected Subscribe to fail on duplicate sequence in history")
	}
}

func TestHub_HistoryFailClosed_OversizedLine(t *testing.T) {
	dir := t.TempDir()
	opID := "op-00000000000000000000000000000104"
	eventsPath := filepath.Join(dir, fmt.Sprintf("%s.events.jsonl", opID))

	oversizedMsg := strings.Repeat("a", 70*1024) // 70 KB > 64 KB limit
	oversizedLine := fmt.Sprintf(`{"sequence":1,"timestamp":"2026-08-29T12:00:00Z","operation_id":"%s","event_type":"state_change","state":"pending","message":"%s"}`, opID, oversizedMsg)
	if err := os.WriteFile(eventsPath, []byte(oversizedLine+"\n"), 0600); err != nil {
		t.Fatalf("failed to write oversized file: %v", err)
	}

	hub := events.NewHub(dir)
	ctx := context.Background()

	_, _, err := hub.Subscribe(ctx, opID, 0)
	if err == nil {
		t.Errorf("expected Subscribe to fail on oversized line")
	}
}

func TestHub_Close_DetachesAndClosesAllSubscribers(t *testing.T) {
	dir := t.TempDir()
	hub := events.NewHub(dir)
	ctx := context.Background()
	opID := "op-00000000000000000000000000000105"

	opCh, _, err := hub.Subscribe(ctx, opID, 0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	globalCh, _ := hub.SubscribeGlobal(ctx)

	// Close hub
	if err := hub.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// opCh and globalCh must be closed
	select {
	case _, ok := <-opCh:
		if ok {
			t.Errorf("expected opCh to be closed")
		}
	case <-time.After(time.Second):
		t.Errorf("timed out waiting for opCh close")
	}

	select {
	case _, ok := <-globalCh:
		if ok {
			t.Errorf("expected globalCh to be closed")
		}
	case <-time.After(time.Second):
		t.Errorf("timed out waiting for globalCh close")
	}

	// Subsequent subscribe or publish must return ErrHubClosed
	_, _, err = hub.Subscribe(ctx, opID, 0)
	if err != events.ErrHubClosed {
		t.Errorf("expected ErrHubClosed on subscribe, got %v", err)
	}

	_, err = hub.Publish(ctx, events.Event{OperationID: opID, EventType: "progress"})
	if err != events.ErrHubClosed {
		t.Errorf("expected ErrHubClosed on publish, got %v", err)
	}
}

func TestHub_BroadcastEphemeral(t *testing.T) {
	dir := t.TempDir()
	hub := events.NewHub(dir)
	ctx := context.Background()
	opID := "op-00000000000000000000000000000106"

	ch, unsub, err := hub.Subscribe(ctx, opID, 0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer unsub()

	hub.BroadcastEphemeral(events.Event{
		OperationID: opID,
		EventType:   "terminal",
		State:       domain.OpStateFailed,
		Category:    "persistence_error",
		Message:     "injected terminal fallback",
	})

	select {
	case ev := <-ch:
		if ev.State != domain.OpStateFailed || ev.Category != "persistence_error" {
			t.Errorf("unexpected ephemeral event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Errorf("timed out waiting for ephemeral event")
	}

	// Verify ephemeral event was NOT saved to disk
	eventsPath := filepath.Join(dir, fmt.Sprintf("%s.events.jsonl", opID))
	if _, err := os.Stat(eventsPath); !os.IsNotExist(err) {
		t.Errorf("expected no events file for purely ephemeral broadcast")
	}
}

func spawnStressSubscribers(ctx context.Context, hub *events.Hub, opID string, count int, wg *sync.WaitGroup) {
	for range count {
		wg.Add(1)
		go func() { //nolint:modernize // waitgroupgo
			defer wg.Done()
			ch, unsub, err := hub.Subscribe(ctx, opID, 0)
			if err != nil {
				return
			}
			defer unsub()
			for ev := range ch {
				_ = ev
			}
		}()
	}
}

func spawnStressGlobalSubscribers(ctx context.Context, hub *events.Hub, count int, wg *sync.WaitGroup) {
	for range count {
		wg.Add(1)
		go func() { //nolint:modernize // waitgroupgo
			defer wg.Done()
			ch, unsub := hub.SubscribeGlobal(ctx)
			defer unsub()
			for ev := range ch {
				_ = ev
			}
		}()
	}
}

func spawnStressPublishers(ctx context.Context, hub *events.Hub, opID string, workers, steps int, wg *sync.WaitGroup) {
	for p := range workers {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := range steps {
				_, _ = hub.Publish(ctx, events.Event{
					OperationID: opID,
					EventType:   "progress",
					State:       domain.OpStateRunning,
					Message:     fmt.Sprintf("worker %d step %d", worker, i),
				})
			}
		}(p)
	}
}

func spawnLinearizableOpSubscribers(ctx context.Context, hub *events.Hub, opID string, count int, startBarrier <-chan struct{}, readyWg, doneWg *sync.WaitGroup, chMu *sync.Mutex, allChannels *[]<-chan events.Event) {
	for range count {
		readyWg.Add(1)
		doneWg.Add(1)
		go func() { //nolint:modernize // waitgroupgo
			defer doneWg.Done()
			readyWg.Done()
			<-startBarrier
			ch, unsub, err := hub.Subscribe(ctx, opID, 0)
			if err == nil {
				chMu.Lock()
				*allChannels = append(*allChannels, ch)
				chMu.Unlock()
				defer unsub()
			}
		}()
	}
}

func spawnLinearizableGlobalSubscribers(ctx context.Context, hub *events.Hub, count int, startBarrier <-chan struct{}, readyWg, doneWg *sync.WaitGroup, chMu *sync.Mutex, allChannels *[]<-chan events.Event) {
	for range count {
		readyWg.Add(1)
		doneWg.Add(1)
		go func() { //nolint:modernize // waitgroupgo
			defer doneWg.Done()
			readyWg.Done()
			<-startBarrier
			ch, unsub := hub.SubscribeGlobal(ctx)
			defer unsub()
			chMu.Lock()
			*allChannels = append(*allChannels, ch)
			chMu.Unlock()
		}()
	}
}

func spawnLinearizablePublishers(ctx context.Context, hub *events.Hub, opID string, count, steps int, startBarrier <-chan struct{}, readyWg, doneWg *sync.WaitGroup) {
	for p := range count {
		readyWg.Add(1)
		doneWg.Add(1)
		go func(worker int) {
			defer doneWg.Done()
			readyWg.Done()
			<-startBarrier
			for i := range steps {
				_, _ = hub.Publish(ctx, events.Event{
					OperationID: opID,
					EventType:   "progress",
					State:       domain.OpStateRunning,
					Message:     fmt.Sprintf("worker %d step %d", worker, i),
				})
			}
		}(p)
	}
}

func assertAllChannelsClosed(t *testing.T, channels []<-chan events.Event) {
	t.Helper()
	if len(channels) == 0 {
		t.Fatalf("expected some subscribers to be registered before or during close")
	}
	for idx, ch := range channels {
		closed := false
		for !closed {
			select {
			case _, ok := <-ch:
				if !ok {
					closed = true
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("subscriber channel %d was not closed following hub.Close()", idx)
			}
		}
	}
}

func TestHub_RaceStress_PublishSubscribeClose(t *testing.T) {
	dir := t.TempDir()
	hub := events.NewHub(dir)
	ctx := context.Background()

	opID := "op-00000000000000000000000000000107"
	var wg sync.WaitGroup

	spawnStressSubscribers(ctx, hub, opID, 20, &wg)
	spawnStressGlobalSubscribers(ctx, hub, 10, &wg)
	spawnStressPublishers(ctx, hub, opID, 10, 30, &wg)

	time.Sleep(50 * time.Millisecond)
	if err := hub.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	wg.Wait()

	// Post-close publish must fail
	_, err := hub.Publish(ctx, events.Event{
		OperationID: opID,
		EventType:   "progress",
		State:       domain.OpStateRunning,
	})
	if !errors.Is(err, events.ErrHubClosed) {
		t.Fatalf("expected ErrHubClosed post-close, got: %v", err)
	}
}

func TestHub_Close_Linearizable_SubscribersAndPublish(t *testing.T) {
	dir := t.TempDir()
	hub := events.NewHub(dir)
	ctx := context.Background()
	opID := "op-00000000000000000000000000000108"

	var chMu sync.Mutex
	var allChannels []<-chan events.Event

	var readyWg sync.WaitGroup
	var doneWg sync.WaitGroup
	startBarrier := make(chan struct{})

	const opSubCount = 30
	const globalSubCount = 30
	const pubCount = 15

	spawnLinearizableOpSubscribers(ctx, hub, opID, opSubCount, startBarrier, &readyWg, &doneWg, &chMu, &allChannels)
	spawnLinearizableGlobalSubscribers(ctx, hub, globalSubCount, startBarrier, &readyWg, &doneWg, &chMu, &allChannels)
	spawnLinearizablePublishers(ctx, hub, opID, pubCount, 20, startBarrier, &readyWg, &doneWg)

	readyWg.Wait()
	close(startBarrier)

	time.Sleep(10 * time.Millisecond)
	if err := hub.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	doneWg.Wait()

	chMu.Lock()
	channelsToCheck := make([]<-chan events.Event, len(allChannels))
	copy(channelsToCheck, allChannels)
	chMu.Unlock()

	assertAllChannelsClosed(t, channelsToCheck)

	// Direct post-close assertions
	_, _, err := hub.Subscribe(ctx, opID, 0)
	if !errors.Is(err, events.ErrHubClosed) {
		t.Fatalf("expected ErrHubClosed for Subscribe after Close, got %v", err)
	}

	globalCh, globalUnsub := hub.SubscribeGlobal(ctx)
	defer globalUnsub()
	select {
	case _, ok := <-globalCh:
		if ok {
			t.Fatalf("expected closed channel from SubscribeGlobal after Close")
		}
	default:
		t.Fatalf("expected immediate EOF on globalCh returned after Close")
	}

	_, err = hub.Publish(ctx, events.Event{
		OperationID: opID,
		EventType:   "progress",
		State:       domain.OpStateRunning,
	})
	if !errors.Is(err, events.ErrHubClosed) {
		t.Fatalf("expected ErrHubClosed for Publish after Close, got %v", err)
	}

	if err := hub.Close(); err != nil {
		t.Fatalf("idempotent Close failed: %v", err)
	}
}
