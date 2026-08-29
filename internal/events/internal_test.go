package events

import (
	"context"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestHub_GlobalFanOutVsClose(t *testing.T) {
	dir := t.TempDir()
	hub := NewHub(dir)
	ctx := context.Background()

	opID := "op-00000000000000000000000000000999"

	// subscribe globally
	ch, unsub := hub.SubscribeGlobal(ctx)
	defer unsub()

	// obtain private subscriber under Hub.mu
	hub.mu.RLock()
	var sub *subscriber
	for _, s := range hub.globalSubs {
		sub = s
		break
	}
	hub.mu.RUnlock()

	if sub == nil {
		t.Fatal("expected subscriber")
	}

	// lock its subscriber.mu to pause Publish exactly inside global fan-out
	sub.mu.Lock()

	publishDone := make(chan struct{})
	publishEv := Event{
		OperationID: opID,
		EventType:   "state_change",
		State:       domain.OpStatePending,
	}

	go func() {
		_, err := hub.Publish(ctx, publishEv)
		if err != nil {
			t.Errorf("Publish failed: %v", err)
		}
		close(publishDone)
	}()

	// wait for Publish to block on sub.mu
	time.Sleep(50 * time.Millisecond)

	// start Close
	closeDone := make(chan struct{})
	go func() {
		err := hub.Close()
		if err != nil {
			t.Errorf("Close failed: %v", err)
		}
		close(closeDone)
	}()

	// wait for Close to block on hub.mu wait
	time.Sleep(50 * time.Millisecond)

	// release subscriber lock
	sub.mu.Unlock()

	// assert Publish succeeds
	select {
	case <-publishDone:
	case <-time.After(2 * time.Second):
		t.Fatal("publish timed out")
	}

	// event is received
	select {
	case ev := <-ch:
		if ev.State != domain.OpStatePending {
			t.Errorf("expected pending state, got %s", ev.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive event")
	}

	// and only then EOF occurs
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected EOF on channel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive EOF")
	}

	// close finishes
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("close timed out")
	}
}

func TestHub_WatcherLifecycle(t *testing.T) {
	dir := t.TempDir()
	hub := NewHub(dir)
	opID := "op-00000000000000000000000000000888"

	bgCtx := context.Background()
	_, bgUnsub, err := hub.Subscribe(bgCtx, opID, 0)
	if err != nil {
		t.Fatalf("Subscribe bg failed: %v", err)
	}

	hub.mu.RLock()
	stream := hub.streams[opID]
	hub.mu.RUnlock()

	stream.mu.Lock()
	var bgSub *subscriber
	for _, s := range stream.subscribers {
		bgSub = s
		break
	}
	stream.mu.Unlock()

	if bgSub == nil {
		t.Fatal("expected bg subscriber")
	}

	bgUnsub()

	ctx1, cancel1 := context.WithCancel(t.Context())
	defer cancel1()
	_, unsub1, err := hub.Subscribe(ctx1, opID, 0)
	if err != nil {
		t.Fatalf("Subscribe ctx1 failed: %v", err)
	}

	stream.mu.Lock()
	var sub1 *subscriber
	for _, s := range stream.subscribers {
		if s != bgSub {
			sub1 = s
			break
		}
	}
	stream.mu.Unlock()

	unsub1()

	select {
	case <-sub1.done:
	case <-time.After(1 * time.Second):
		t.Fatal("expected done to be closed on unsub")
	}

	ctx2, cancel2 := context.WithCancel(t.Context())
	defer cancel2()
	_, _, err = hub.Subscribe(ctx2, opID, 0)
	if err != nil {
		t.Fatalf("Subscribe ctx2 failed: %v", err)
	}

	stream.mu.Lock()
	var sub2 *subscriber
	for _, s := range stream.subscribers {
		if s != bgSub && s != sub1 {
			sub2 = s
			break
		}
	}
	stream.mu.Unlock()

	hub.Close()

	select {
	case <-sub2.done:
	case <-time.After(1 * time.Second):
		t.Fatal("expected done to be closed on Close")
	}
}
