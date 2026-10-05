package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
)

func TestRecordingStatusRetentionPreservesActiveForeignAndCorrupt(t *testing.T) {
	f := newBoundsRecordingFixture(t, &boundsRecordingProviderFake{})
	req := statusRecordRequest()
	if _, err := f.service.Record(t.Context(), f.actor, req); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.root, "console-recordings")
	source, err := os.ReadFile(filepath.Join(dir, statusRecordingID, "05.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(source, &doc); err != nil {
		t.Fatal(err)
	}
	// Seed bounded synthetic terminal, active, foreign, and corrupt reservations.
	for i := range 63 {
		writeRetentionFixture(t, f, dir, doc, i)
	}
	*f.now = f.now.Add(15 * time.Minute)
	req.RecordingID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := f.service.Record(t.Context(), f.actor, req); err != nil {
		t.Fatalf("terminal expiry cleanup: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("%032x", i))); err != nil {
			t.Fatalf("protected reservation %d removed", i)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 4 {
		t.Fatalf("remaining entries %d, %v", len(entries), err)
	}
	if _, err := os.Stat(filepath.Join(dir, statusRecordingID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired own terminal retained: %v", err)
	}
}

func TestRecordingStatusCapacityRefusesWithoutCapture(t *testing.T) {
	provider := &boundsRecordingProviderFake{}
	f := newBoundsRecordingFixture(t, provider)
	dir := filepath.Join(f.root, "console-recordings")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for i := range 64 {
		if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("%032x", i)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.service.Record(context.Background(), f.actor, statusRecordRequest()); !errors.Is(err, app.ErrRecordingIDUnavailable) {
		t.Fatalf("capacity refusal: %v", err)
	}
	if provider.captures != 0 {
		t.Fatal("capacity refusal dispatched")
	}
}

func writeRetentionFixture(t *testing.T, f boundsRecordingFixture, dir string, doc map[string]any, i int) {
	t.Helper()
	id := fmt.Sprintf("%032x", i+1)
	child := filepath.Join(dir, id)
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	doc["sequence"] = 0
	status := doc["status"].(map[string]any)
	status["recording_id"] = id
	status["terminal"] = true
	status["terminal_reason"] = "completed"
	doc["caller"] = f.actor.AuthenticatedCaller
	if i == 0 {
		status["terminal"] = false
		status["terminal_reason"] = ""
	}
	if i == 1 {
		doc["caller"] = "foreign"
	}
	data, _ := json.Marshal(doc)
	if i == 2 {
		data = []byte(`{"guest":"secret"}`)
	}
	if err := os.WriteFile(filepath.Join(child, "00.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}
