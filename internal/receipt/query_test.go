package receipt_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func TestStore_GetAndList(t *testing.T) {
	dir := t.TempDir()
	store := receipt.NewStore(dir)

	// Get non-existent
	_, err := store.Get("rcpt-00000000000000000000000000000001")
	if !errors.Is(err, receipt.ErrReceiptNotFound) {
		t.Fatalf("expected ErrReceiptNotFound, got: %v", err)
	}

	// Save receipts for 2 actors
	baseTime := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= 4; i++ {
		actorName := "agent:mcp-local"
		if i%2 == 0 {
			actorName = "operator:local"
		}
		digest := sha256.Sum256([]byte(fmt.Sprintf("test-fingerprint-%d", i)))
		fp := domain.Fingerprint(fmt.Sprintf("sha256:%s", hex.EncodeToString(digest[:])))

		rcpt := domain.Receipt{
			ReceiptID:        domain.ReceiptID(fmt.Sprintf("rcpt-0000000000000000000000000000000%d", i)),
			OperationKind:    "machine.start",
			Fingerprint:      fp,
			IdempotencyKey:   fmt.Sprintf("key-%d", i),
			Actor:            domain.ActorID(actorName),
			Target:           "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
			Class:            domain.ClassReversibleMutation,
			EffectiveBackend: "hyperv",
			StartedAt:        baseTime.Add(time.Duration(i) * time.Minute),
			CompletedAt:      baseTime.Add(time.Duration(i)*time.Minute + 5*time.Second),
			Outcome: domain.ExecutionOutcome{
				Status:   domain.OutcomeSuccess,
				ExitCode: 0,
			},
			ObservationType: domain.ObservationObserved,
			RollbackRef:     "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
			RedactionStatus: domain.RedactionApplied,
		}
		if err := store.Save(rcpt); err != nil {
			t.Fatalf("Save %d failed: %v", i, err)
		}
	}

	// Get single
	fetched, err := store.Get("rcpt-00000000000000000000000000000001")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(fetched.ReceiptID) != "rcpt-00000000000000000000000000000001" {
		t.Errorf("unexpected receipt ID: %s", fetched.ReceiptID)
	}

	// List all
	all, err := store.List(10, "")
	if err != nil {
		t.Fatalf("List all failed: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("expected 4 receipts, got %d", len(all))
	}
	// Verify sorted by CompletedAt descending
	if all[0].ReceiptID != "rcpt-00000000000000000000000000000004" {
		t.Errorf("expected newest first, got %s", all[0].ReceiptID)
	}

	// List filtered by actor
	agentRcpts, err := store.List(10, "agent:mcp-local")
	if err != nil {
		t.Fatalf("List agent receipts failed: %v", err)
	}
	if len(agentRcpts) != 2 {
		t.Fatalf("expected 2 agent receipts, got %d", len(agentRcpts))
	}
}

func TestStore_GetInvalidIDAndEmptyList(t *testing.T) {
	dir := t.TempDir()
	store := receipt.NewStore(dir)

	// Invalid ID
	_, err := store.Get("invalid-id")
	if err == nil {
		t.Errorf("expected error for invalid receipt ID")
	}

	// Empty list
	list, err := store.List(10, "")
	if err != nil {
		t.Fatalf("List empty failed: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 receipts, got %d", len(list))
	}
}

func TestStore_QueryRejectsNonDirectoryReceiptRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "receipts")
	if err := os.WriteFile(root, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	store := receipt.NewStore(root)
	if _, err := store.List(10, ""); err == nil {
		t.Fatal("list accepted non-directory receipt root")
	}
	if _, err := store.Get("rcpt-00000000000000000000000000000001"); err == nil {
		t.Fatal("get accepted non-directory receipt root")
	}
}

func TestStore_QueryCorruptAndOversized(t *testing.T) {
	dir := t.TempDir()
	store := receipt.NewStore(dir)

	rcptID := "rcpt-00000000000000000000000000000099"
	path := filepath.Join(dir, fmt.Sprintf("%s.json", rcptID))

	// Corrupt JSON
	_ = os.WriteFile(path, []byte("invalid-json"), 0600)
	if _, err := store.Get(rcptID); err == nil {
		t.Errorf("expected error reading corrupt receipt")
	}

	// Symlink
	symPath := filepath.Join(dir, "rcpt-00000000000000000000000000000098.json")
	_ = os.Symlink(path, symPath)
	if _, err := store.Get("rcpt-00000000000000000000000000000098"); err == nil {
		t.Errorf("expected error reading symlinked receipt")
	}
}

func TestStore_QueryTrailingData(t *testing.T) {
	dir := t.TempDir()
	store := receipt.NewStore(dir)

	rcptID := "rcpt-00000000000000000000000000000097"
	path := filepath.Join(dir, fmt.Sprintf("%s.json", rcptID))

	// Valid JSON + trailing garbage
	_ = os.WriteFile(path, []byte(`{"schema_version":"1","receipt_id":"rcpt-00000000000000000000000000000097"} trailing`), 0600)
	if _, err := store.Get(rcptID); err == nil {
		t.Errorf("expected error reading receipt with trailing data")
	}
}

func TestStore_QueryValidationFailure(t *testing.T) {
	dir := t.TempDir()
	store := receipt.NewStore(dir)

	rcptID := "rcpt-00000000000000000000000000000096"
	path := filepath.Join(dir, fmt.Sprintf("%s.json", rcptID))

	// Missing required fields
	_ = os.WriteFile(path, []byte(`{"schema_version":"1","receipt_id":"rcpt-00000000000000000000000000000096","operation_kind":""}`), 0600)
	if _, err := store.Get(rcptID); err == nil {
		t.Errorf("expected error reading invalid receipt structure")
	}
}

func TestStore_ConvertFromDTO_RFC3339(t *testing.T) {
	dto := receipt.DTO{
		ReceiptID:        "rcpt-00000000000000000000000000000095",
		OperationKind:    "machine.start",
		Fingerprint:      "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Actor:            "operator:local",
		Target:           "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Class:            domain.ClassReversibleMutation,
		EffectiveBackend: "hyperv",
		StartedAt:        "2026-08-29T12:00:00Z",
		CompletedAt:      "2026-08-29T12:00:05Z",
		Outcome: receipt.OutcomeDTO{
			Status:   domain.OutcomeSuccess,
			ExitCode: 0,
		},
		ObservationType: domain.ObservationObserved,
		RedactionStatus: domain.RedactionApplied,
	}

	rcpt, err := receipt.ConvertFromDTO(dto)
	if err != nil {
		t.Fatalf("ConvertFromDTO failed: %v", err)
	}
	if string(rcpt.ReceiptID) != dto.ReceiptID {
		t.Errorf("unexpected receipt ID: %s", rcpt.ReceiptID)
	}
}

func TestStore_ConvertFromDTO_InvalidTimestamps(t *testing.T) {
	dto := receipt.DTO{
		ReceiptID:   "rcpt-00000000000000000000000000000094",
		StartedAt:   "invalid-date",
		CompletedAt: "2026-08-29T12:00:00Z",
	}
	if _, err := receipt.ConvertFromDTO(dto); err == nil {
		t.Errorf("expected error for invalid started_at timestamp")
	}

	dto.StartedAt = "2026-08-29T12:00:00Z"
	dto.CompletedAt = "invalid-date"
	if _, err := receipt.ConvertFromDTO(dto); err == nil {
		t.Errorf("expected error for invalid completed_at timestamp")
	}
}

func TestStore_SaveInvalidAndLookupEmptyKey(t *testing.T) {
	dir := t.TempDir()
	store := receipt.NewStore(dir)

	// Save invalid receipt
	if err := store.Save(domain.Receipt{}); err == nil {
		t.Errorf("expected error saving empty receipt")
	}

	// Lookup with empty key
	rcpt, err := store.LookupIdempotency(domain.Operation{})
	if err != nil || rcpt != nil {
		t.Errorf("expected (nil, nil) for empty idempotency key, got rcpt %v, err %v", rcpt, err)
	}
}

func TestStore_ListLimitBounds(t *testing.T) {
	dir := t.TempDir()
	store := receipt.NewStore(dir)

	// List with negative limit (defaults to 50)
	list, err := store.List(-5, "")
	if err != nil || len(list) != 0 {
		t.Errorf("expected empty list for negative limit")
	}

	// List with excessive limit (capped to 1000)
	list, err = store.List(5000, "")
	if err != nil || len(list) != 0 {
		t.Errorf("expected empty list for excessive limit")
	}
}
