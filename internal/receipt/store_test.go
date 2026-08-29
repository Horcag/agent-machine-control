package receipt_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func TestStore_SaveAndLookupIdempotency_ExactMatch(t *testing.T) {
	dir := t.TempDir()
	store := receipt.NewStore(dir)

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	actor, _ := domain.NewActorContext("user:alice", "user:alice", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	op := domain.Operation{
		Kind:                "machine.start",
		Target:              "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		Actor:               actor,
		Reason:              "test start",
		Deadline:            now.Add(5 * time.Minute),
		IdempotencyKey:      "idemp-key-1",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	fp, err := op.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint failed: %v", err)
	}

	r := domain.Receipt{
		ReceiptID:        "rcpt-1",
		OperationKind:    op.Kind,
		Fingerprint:      fp,
		IdempotencyKey:   op.IdempotencyKey,
		Actor:            actor.EffectiveActor,
		Target:           op.Target,
		Class:            op.Classification,
		EffectiveBackend: "hyperv",
		StartedAt:        now,
		CompletedAt:      now.Add(2 * time.Second),
		Outcome:          domain.ExecutionOutcome{Status: domain.OutcomeSuccess, ExitCode: 0},
		ObservationType:  domain.ObservationObserved,
		RollbackRef:      "chk-1",
		RedactionStatus:  domain.RedactionApplied,
	}

	if err := store.Save(r); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Lookup exact match
	cached, err := store.LookupIdempotency(op)
	if err != nil {
		t.Fatalf("LookupIdempotency failed: %v", err)
	}
	if cached == nil {
		t.Fatalf("expected cached receipt, got nil")
	}
	if cached.ReceiptID != r.ReceiptID {
		t.Errorf("expected receipt ID %s, got %s", r.ReceiptID, cached.ReceiptID)
	}
}

func TestStore_LookupIdempotency_CrossActorCollision(t *testing.T) {
	dir := t.TempDir()
	store := receipt.NewStore(dir)

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	alice, _ := domain.NewActorContext("user:alice", "user:alice", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	opAlice := domain.Operation{
		Kind:                "machine.start",
		Target:              "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		Actor:               alice,
		Reason:              "test start",
		Deadline:            now.Add(5 * time.Minute),
		IdempotencyKey:      "shared-key-1",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	fpAlice, _ := opAlice.Fingerprint()
	rAlice := domain.Receipt{
		ReceiptID:        "rcpt-alice",
		OperationKind:    opAlice.Kind,
		Fingerprint:      fpAlice,
		IdempotencyKey:   opAlice.IdempotencyKey,
		Actor:            alice.EffectiveActor,
		Target:           opAlice.Target,
		Class:            opAlice.Classification,
		EffectiveBackend: "hyperv",
		StartedAt:        now,
		CompletedAt:      now.Add(2 * time.Second),
		Outcome:          domain.ExecutionOutcome{Status: domain.OutcomeSuccess, ExitCode: 0},
		ObservationType:  domain.ObservationObserved,
		RollbackRef:      "chk-1",
		RedactionStatus:  domain.RedactionApplied,
	}
	_ = store.Save(rAlice)

	// Bob submits same key
	bob, _ := domain.NewActorContext("user:bob", "user:bob", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	opBob := opAlice
	opBob.Actor = bob

	cached, err := store.LookupIdempotency(opBob)
	if err == nil || !errors.Is(err, receipt.ErrIdempotencyCollision) {
		t.Fatalf("expected ErrIdempotencyCollision for cross-actor, got %v", err)
	}
	if cached != nil {
		t.Fatalf("must not disclose cached receipt across actor boundaries")
	}
}

func TestStore_LookupIdempotency_ParameterCollision(t *testing.T) {
	dir := t.TempDir()
	store := receipt.NewStore(dir)

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	alice, _ := domain.NewActorContext("user:alice", "user:alice", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	op1 := domain.Operation{
		Kind:                "machine.start",
		Target:              "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		Actor:               alice,
		Reason:              "test start",
		Deadline:            now.Add(5 * time.Minute),
		IdempotencyKey:      "idemp-key-param",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	fp1, _ := op1.Fingerprint()
	r1 := domain.Receipt{
		ReceiptID:        "rcpt-1",
		OperationKind:    op1.Kind,
		Fingerprint:      fp1,
		IdempotencyKey:   op1.IdempotencyKey,
		Actor:            alice.EffectiveActor,
		Target:           op1.Target,
		Class:            op1.Classification,
		EffectiveBackend: "hyperv",
		StartedAt:        now,
		CompletedAt:      now.Add(2 * time.Second),
		Outcome:          domain.ExecutionOutcome{Status: domain.OutcomeSuccess, ExitCode: 0},
		ObservationType:  domain.ObservationObserved,
		RollbackRef:      "chk-1",
		RedactionStatus:  domain.RedactionApplied,
	}
	_ = store.Save(r1)

	// Same actor and key, but different target VM
	op2 := op1
	op2.Target = "c0b1c2d3-e4f5-6789-abcd-ef0123456789"

	cached, err := store.LookupIdempotency(op2)
	if err == nil || !errors.Is(err, receipt.ErrIdempotencyCollision) {
		t.Fatalf("expected ErrIdempotencyCollision for target collision, got %v", err)
	}
	if cached != nil {
		t.Fatalf("must not disclose cached receipt on collision")
	}
}

func TestStore_Save_InvalidReceipt(t *testing.T) {
	dir := t.TempDir()
	store := receipt.NewStore(dir)

	badReceipt := domain.Receipt{
		ReceiptID: "invalid-id",
	}
	if err := store.Save(badReceipt); err == nil {
		t.Errorf("expected error saving invalid receipt")
	}
}

func TestStore_LookupIdempotency_EmptyKey(t *testing.T) {
	dir := t.TempDir()
	store := receipt.NewStore(dir)

	rcpt, err := store.LookupIdempotency(domain.Operation{IdempotencyKey: ""})
	if err != nil || rcpt != nil {
		t.Errorf("expected nil, nil for empty idempotency key, got rcpt=%v err=%v", rcpt, err)
	}
}

func TestReceipt_DTO_Conversion_Errors(t *testing.T) {
	dto := receipt.DTO{
		ReceiptID:        "rcpt-1",
		OperationKind:    "machine.start",
		Fingerprint:      "fp-1",
		Actor:            "user:alice",
		Target:           "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		Class:            domain.ClassReversibleMutation,
		EffectiveBackend: "hyperv",
		StartedAt:        "invalid-date",
		CompletedAt:      "invalid-date",
	}

	if _, err := receipt.ConvertFromDTO(dto); err == nil {
		t.Errorf("expected error for invalid started_at")
	}

	dto.StartedAt = time.Now().Format(time.RFC3339)
	if _, err := receipt.ConvertFromDTO(dto); err == nil {
		t.Errorf("expected error for invalid completed_at")
	}

	// Valid RFC3339 non-nano
	dto.CompletedAt = time.Now().Format(time.RFC3339)
	if _, err := receipt.ConvertFromDTO(dto); err != nil {
		t.Errorf("expected successful conversion with RFC3339 non-nano: %v", err)
	}
}

func TestStore_CorruptReceiptFile_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	store := receipt.NewStore(dir)

	// Write a non-JSON file (should be ignored since not .json)
	_ = os.WriteFile(dir+"/random.txt", []byte("ignore"), 0600)
	// Write a corrupt .json file
	_ = os.WriteFile(dir+"/bad.json", []byte("{not-json"), 0600)

	actor, _ := domain.NewActorContext("user:alice", "user:alice", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	op := domain.Operation{
		Kind:                "machine.start",
		Target:              "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		Actor:               actor,
		Reason:              "test",
		Deadline:            time.Now().Add(time.Hour),
		IdempotencyKey:      "key-1",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	_, err := store.LookupIdempotency(op)
	if err == nil {
		t.Fatalf("expected error when directory contains corrupt .json receipt files")
	}
}

func TestStore_SymlinkAndOverlarge_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	store := receipt.NewStore(dir)

	// Symlink test
	realTarget := dir + "/real.json"
	_ = os.WriteFile(realTarget, []byte(`{}`), 0600)
	symlinkPath := dir + "/link.json"
	_ = os.Symlink(realTarget, symlinkPath)

	actor, _ := domain.NewActorContext("user:alice", "user:alice", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	op := domain.Operation{
		Kind:                "machine.start",
		Target:              "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		Actor:               actor,
		Reason:              "test",
		Deadline:            time.Now().Add(time.Hour),
		IdempotencyKey:      "key-1",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	_, err := store.LookupIdempotency(op)
	if err == nil {
		t.Fatalf("expected error when receipt directory contains symlinked receipt file")
	}

	// Overlarge test
	_ = os.Remove(symlinkPath)
	_ = os.Remove(realTarget)
	overlargePath := dir + "/overlarge.json"
	bigData := make([]byte, 70*1024)
	_ = os.WriteFile(overlargePath, bigData, 0600)

	_, err = store.LookupIdempotency(op)
	if err == nil {
		t.Fatalf("expected error when receipt file exceeds 64KB")
	}

	// Trailing data test
	_ = os.Remove(overlargePath)
	trailingPath := dir + "/trailing.json"
	_ = os.WriteFile(trailingPath, []byte(`{"receipt_id":"rcpt-1"} trailing bytes`), 0600)

	_, err = store.LookupIdempotency(op)
	if err == nil {
		t.Fatalf("expected error when receipt file has trailing data")
	}

	// Invalid timestamp in cached receipt structure test
	_ = os.Remove(trailingPath)
	invalidDatePath := dir + "/invalid_date.json"
	_ = os.WriteFile(invalidDatePath, []byte(`{"receipt_id":"rcpt-1","schema_version":"1","started_at":"bad-date","completed_at":"bad-date"}`), 0600)

	_, err = store.LookupIdempotency(op)
	if err == nil {
		t.Fatalf("expected error when cached receipt has invalid timestamp structure")
	}

	// Invalid target GUID validation failure in cached receipt
	_ = os.Remove(invalidDatePath)
	invalidTargetPath := dir + "/invalid_target.json"
	_ = os.WriteFile(invalidTargetPath, []byte(`{"schema_version":"1","receipt_id":"rcpt-1","operation_kind":"machine.start","fingerprint":"fp-1","idempotency_key":"key-1","actor":"user:alice","target":"invalid-non-guid","class":"reversible_mutation","effective_backend":"hyperv","started_at":"2026-08-29T12:00:00Z","completed_at":"2026-08-29T12:00:01Z","outcome":{"status":"success","exit_code":0},"observation_type":"observed","rollback_ref":"ref-1","redaction_status":"applied"}`), 0600)

	_, err = store.LookupIdempotency(op)
	if err == nil {
		t.Fatalf("expected error when cached receipt fails Validate()")
	}
}

func TestStore_Save_UnwritableDir_Error(t *testing.T) {
	nonExistent := "/non/existent/dir/receipts"
	store := receipt.NewStore(nonExistent)

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	r := domain.Receipt{
		ReceiptID:        "rcpt-1",
		OperationKind:    "machine.start",
		Fingerprint:      "fp-1",
		IdempotencyKey:   "key-1",
		Actor:            "user:alice",
		Target:           "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		Class:            domain.ClassReversibleMutation,
		EffectiveBackend: "hyperv",
		StartedAt:        now,
		CompletedAt:      now.Add(time.Second),
		Outcome:          domain.ExecutionOutcome{Status: domain.OutcomeSuccess, ExitCode: 0},
		ObservationType:  domain.ObservationObserved,
		RollbackRef:      "chk-1",
		RedactionStatus:  domain.RedactionApplied,
	}

	if err := store.Save(r); err == nil {
		t.Fatalf("expected error when saving to non-existent directory")
	}
}

func TestStore_Lookup_NonExistentDir(t *testing.T) {
	nonExistent := "/non/existent/dir/receipts"
	store := receipt.NewStore(nonExistent)

	actor, _ := domain.NewActorContext("user:alice", "user:alice", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	op := domain.Operation{
		Kind:                "machine.start",
		Target:              "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		Actor:               actor,
		Reason:              "test",
		Deadline:            time.Now().Add(time.Hour),
		IdempotencyKey:      "key-1",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	rcpt, err := store.LookupIdempotency(op)
	if err != nil || rcpt != nil {
		t.Fatalf("expected nil, nil for non-existent directory, got %v, %v", rcpt, err)
	}
}

func TestStore_LookupIdempotency_InvalidOperationFingerprint(t *testing.T) {
	dir := t.TempDir()
	store := receipt.NewStore(dir)

	op := domain.Operation{
		IdempotencyKey: "key-1",
		Target:         "", // invalid target
	}

	_, err := store.LookupIdempotency(op)
	if err == nil {
		t.Fatalf("expected error for invalid operation fingerprint")
	}
}

func TestStore_LookupIdempotency_DirIsAFile(t *testing.T) {
	tempFile := filepath.Join(t.TempDir(), "regular_file")
	_ = os.WriteFile(tempFile, []byte("data"), 0600)
	store := receipt.NewStore(tempFile)

	actor, _ := domain.NewActorContext("user:alice", "user:alice", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	op := domain.Operation{
		Kind:                "machine.start",
		Target:              "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		Actor:               actor,
		Reason:              "test",
		Deadline:            time.Now().Add(time.Hour),
		IdempotencyKey:      "key-1",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	_, err := store.LookupIdempotency(op)
	if err == nil {
		t.Fatalf("expected error when receipts dir is a file")
	}
}

func TestStore_Save_CannotOverwrite(t *testing.T) {
	dir := t.TempDir()
	store := receipt.NewStore(dir)

	actor, _ := domain.NewActorContext("user:alice", "user:alice", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	op := domain.Operation{
		Kind:                "machine.start",
		Target:              "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		Actor:               actor,
		Reason:              "test",
		Deadline:            time.Date(2026, 8, 29, 13, 0, 0, 0, time.UTC),
		IdempotencyKey:      "key-1",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}
	fp, err := op.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint failed: %v", err)
	}

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	r := domain.Receipt{
		ReceiptID:        "rcpt-1",
		OperationKind:    "machine.start",
		Fingerprint:      fp,
		IdempotencyKey:   "key-1",
		Actor:            "user:alice",
		Target:           "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		Class:            domain.ClassReversibleMutation,
		EffectiveBackend: "hyperv",
		StartedAt:        now,
		CompletedAt:      now.Add(time.Second),
		Outcome:          domain.ExecutionOutcome{Status: domain.OutcomeSuccess, ExitCode: 0},
		ObservationType:  domain.ObservationObserved,
		RollbackRef:      "chk-1",
		RedactionStatus:  domain.RedactionApplied,
	}

	if err := store.Save(r); err != nil {
		t.Fatalf("first Save failed: %v", err)
	}

	// Attempting to save again with same ReceiptID must fail and not silently overwrite
	if err := store.Save(r); err == nil {
		t.Fatalf("expected error when attempting to overwrite existing receipt")
	}
}

func TestStore_StrictJSON_TrailingData(t *testing.T) {
	dir := t.TempDir()
	store := receipt.NewStore(dir)

	actor, _ := domain.NewActorContext("user:alice", "user:alice", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	op := domain.Operation{
		Kind:                "machine.start",
		Target:              "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		Actor:               actor,
		Reason:              "test",
		Deadline:            time.Date(2026, 8, 29, 13, 0, 0, 0, time.UTC),
		IdempotencyKey:      "key-trailing",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}
	fp, err := op.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint failed: %v", err)
	}

	validJSON := `{"receipt_id":"rcpt-trailing","operation_kind":"machine.start","fingerprint":"` + string(fp) + `","idempotency_key":"key-trailing","actor":"user:alice","target":"a0b1c2d3-e4f5-6789-abcd-ef0123456789","class":"reversible_mutation","effective_backend":"hyperv","started_at":"2026-08-29T12:00:00Z","completed_at":"2026-08-29T12:00:01Z","outcome":{"status":"success","exit_code":0},"observation_type":"observed","rollback_ref":"chk-1","redaction_status":"applied"}`

	// 1. Whitespace trailing is accepted
	_ = os.WriteFile(dir+"/rcpt-trailing.json", []byte(validJSON+"\n \t \n"), 0600)
	rcpt, err := store.LookupIdempotency(op)
	if err != nil || rcpt == nil {
		t.Fatalf("expected whitespace trailing data to succeed, got rcpt=%v, err=%v", rcpt, err)
	}

	// 2. Trailing second object is rejected
	_ = os.WriteFile(dir+"/rcpt-trailing.json", []byte(validJSON+"\n{\"extra\":1}"), 0600)
	_, err = store.LookupIdempotency(op)
	if err == nil {
		t.Fatalf("expected error for trailing second object in receipt file")
	}

	// 3. Trailing scalar is rejected
	_ = os.WriteFile(dir+"/rcpt-trailing.json", []byte(validJSON+"\n42"), 0600)
	_, err = store.LookupIdempotency(op)
	if err == nil {
		t.Fatalf("expected error for trailing scalar in receipt file")
	}
}
