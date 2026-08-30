package approval_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func writeApprovalFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write approval fixture: %v", err)
	}
	if err := protectApprovalFixture(path); err != nil {
		t.Fatalf("protect approval fixture: %v", err)
	}
}

func TestLoader_ValidFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "valid_approval.json")

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	content := `{
		"id": "app-001",
		"actor": "user:operator",
		"target": "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		"authorized_class": "destructive_privileged",
		"fingerprint": "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		"idempotency_key": "idemp-key-1",
		"issued_at": "` + now.Format(time.RFC3339) + `",
		"expires_at": "` + now.Add(time.Hour).Format(time.RFC3339) + `"
	}`

	writeApprovalFixture(t, filePath, []byte(content))

	app, err := approval.LoadFromFile(filePath)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if string(app.ID) != "app-001" {
		t.Errorf("expected ID app-001, got %s", app.ID)
	}
	if string(app.Actor) != "user:operator" {
		t.Errorf("expected actor user:operator, got %s", app.Actor)
	}
	if app.AuthorizedClass != domain.ClassDestructivePrivileged {
		t.Errorf("expected class %s, got %s", domain.ClassDestructivePrivileged, app.AuthorizedClass)
	}
}

func TestStoreCheckWritable(t *testing.T) {
	store := approval.NewStore(t.TempDir())
	if err := store.CheckWritable(); err != nil {
		t.Fatalf("writable store rejected: %v", err)
	}
	badPath := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(badPath, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := approval.NewStore(badPath).CheckWritable(); err == nil {
		t.Fatal("non-directory approval store accepted")
	}
}

func TestLoader_RejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "unknown_field.json")

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	content := `{
		"id": "app-001",
		"actor": "user:operator",
		"target": "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		"authorized_class": "destructive_privileged",
		"fingerprint": "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		"idempotency_key": "idemp-key-1",
		"issued_at": "` + now.Format(time.RFC3339) + `",
		"expires_at": "` + now.Add(time.Hour).Format(time.RFC3339) + `",
		"unrecognized_field": "danger"
	}`

	writeApprovalFixture(t, filePath, []byte(content))

	_, err := approval.LoadFromFile(filePath)
	if err == nil || !errors.Is(err, domain.ErrInvalidApprovalRecord) {
		t.Fatalf("expected ErrInvalidApprovalRecord for unknown fields, got %v", err)
	}
}

func TestLoader_StrictJSON_TrailingData(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	validContent := `{
		"id": "app-001",
		"actor": "user:operator",
		"target": "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		"authorized_class": "destructive_privileged",
		"fingerprint": "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		"idempotency_key": "idemp-key-1",
		"issued_at": "` + now.Format(time.RFC3339) + `",
		"expires_at": "` + now.Add(time.Hour).Format(time.RFC3339) + `"
	}`

	// 1. Whitespace trailing is accepted
	wsPath := filepath.Join(dir, "valid_ws.json")
	writeApprovalFixture(t, wsPath, []byte(validContent+"\n \t \n"))
	app, err := approval.LoadFromFile(wsPath)
	if err != nil || app == nil {
		t.Fatalf("expected whitespace trailing data to succeed, got %v", err)
	}

	// 2. Trailing second object is rejected
	objPath := filepath.Join(dir, "trailing_obj.json")
	writeApprovalFixture(t, objPath, []byte(validContent+"\n{\"extra\":1}"))
	if _, err := approval.LoadFromFile(objPath); err == nil || !errors.Is(err, approval.ErrTrailingData) {
		t.Fatalf("expected ErrTrailingData for trailing object, got %v", err)
	}

	// 3. Trailing scalar is rejected
	scalarPath := filepath.Join(dir, "trailing_scalar.json")
	writeApprovalFixture(t, scalarPath, []byte(validContent+"\n42"))
	if _, err := approval.LoadFromFile(scalarPath); err == nil || !errors.Is(err, approval.ErrTrailingData) {
		t.Fatalf("expected ErrTrailingData for trailing scalar, got %v", err)
	}
}

func TestLoader_RejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	realFile := filepath.Join(dir, "real.json")
	symlinkFile := filepath.Join(dir, "symlink.json")

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	content := `{
		"id": "app-001",
		"actor": "user:operator",
		"target": "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		"authorized_class": "destructive_privileged",
		"fingerprint": "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		"idempotency_key": "idemp-key-1",
		"issued_at": "` + now.Format(time.RFC3339) + `",
		"expires_at": "` + now.Add(time.Hour).Format(time.RFC3339) + `"
	}`

	writeApprovalFixture(t, realFile, []byte(content))
	if err := os.Symlink(realFile, symlinkFile); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}

	_, err := approval.LoadFromFile(symlinkFile)
	if err == nil || !errors.Is(err, approval.ErrSymlinkNotAllowed) {
		t.Fatalf("expected ErrSymlinkNotAllowed, got %v", err)
	}
}

func TestStore_MarkConsumed(t *testing.T) {
	dir := t.TempDir()
	store := approval.NewStore(dir)

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	app := domain.Approval{
		ID:              "app-100",
		Actor:           "user:operator",
		Target:          "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		IdempotencyKey:  "key-1",
		IssuedAt:        now,
		ExpiresAt:       now.Add(time.Hour),
	}

	consumed, err := store.IsConsumed(string(app.ID))
	if err != nil || consumed {
		t.Fatalf("expected not consumed, got consumed=%v, err=%v", consumed, err)
	}
	if err := store.MarkConsumed(app, now.Add(time.Minute)); !errors.Is(err, approval.ErrApprovalNotIssued) {
		t.Fatalf("forged approval consumption error = %v, want ErrApprovalNotIssued", err)
	}
	if err := store.Issue(app); err != nil {
		t.Fatalf("Issue failed: %v", err)
	}

	if err := store.MarkConsumed(app, now.Add(time.Minute)); err != nil {
		t.Fatalf("MarkConsumed failed: %v", err)
	}

	consumed, err = store.IsConsumed(string(app.ID))
	if err != nil || !consumed {
		t.Fatalf("expected consumed=true, got consumed=%v, err=%v", consumed, err)
	}

	// Double consumption fails
	err = store.MarkConsumed(app, now.Add(2*time.Minute))
	if err == nil || !errors.Is(err, domain.ErrApprovalConsumed) {
		t.Fatalf("expected ErrApprovalConsumed on replay, got %v", err)
	}

	// Symlink detected in IsConsumed
	symlinkPath := filepath.Join(dir, "app-symlink.json")
	targetFile := filepath.Join(dir, string(app.ID)+".json")
	if err := os.Symlink(targetFile, symlinkPath); err == nil {
		_, err = store.IsConsumed("app-symlink")
		if err == nil {
			t.Fatalf("expected error when symlink is detected in IsConsumed")
		}
	}

	// Non-existent directory returns error on MarkConsumed
	badStore := approval.NewStore(filepath.Join(dir, "nonexistent-subdir"))
	if err := badStore.MarkConsumed(app, now); err == nil {
		t.Fatalf("expected error on MarkConsumed with non-existent directory")
	}
}

func TestStore_IssueAndValidateProvenance(t *testing.T) {
	dir := t.TempDir()
	store := approval.NewStore(dir)
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	issued := domain.Approval{
		ID: "app-issued-1", Actor: "user:operator", Target: "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		IdempotencyKey:  "issued-key-1", IssuedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := store.Issue(issued); err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	if err := store.ValidateIssuedContext(context.Background(), issued); err != nil {
		t.Fatalf("ValidateIssuedContext failed: %v", err)
	}
	if err := store.Issue(issued); err == nil {
		t.Fatal("duplicate issuance unexpectedly replaced immutable authority")
	}
	copied := issued
	copied.IdempotencyKey = "copied-key"
	if err := store.ValidateIssuedContext(context.Background(), copied); !errors.Is(err, approval.ErrApprovalNotIssued) {
		t.Fatalf("copied approval error = %v, want ErrApprovalNotIssued", err)
	}
	missing := issued
	missing.ID = "app-missing-issued"
	if err := store.ValidateIssuedContext(context.Background(), missing); !errors.Is(err, approval.ErrApprovalNotIssued) {
		t.Fatalf("missing approval error = %v, want ErrApprovalNotIssued", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.IssueContext(ctx, missing); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled issuance error = %v", err)
	}
	if err := store.ValidateIssuedContext(ctx, issued); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled validation error = %v", err)
	}
	if err := store.MarkConsumedContext(ctx, issued, now.Add(time.Minute)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled consumption error = %v", err)
	}
	if err := store.Issue(domain.Approval{}); err == nil {
		t.Fatal("invalid approval was issued")
	}
	corrupt := issued
	corrupt.ID = "app-corrupt-issued"
	writeApprovalFixture(t, filepath.Join(dir, string(corrupt.ID)+".issued.json"), []byte("not-json"))
	if err := store.ValidateIssuedContext(context.Background(), corrupt); err == nil {
		t.Fatal("corrupt issuance record was accepted")
	}
}

func TestStore_ReleasesConsumedApprovalOnlyForMatchingUnexecutedAuthority(t *testing.T) {
	store := approval.NewStore(t.TempDir())
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	issued := domain.Approval{
		ID: "app-release-1", Actor: "user:operator", Target: "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		IdempotencyKey:  "release-key-1", IssuedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := store.Issue(issued); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkConsumed(issued, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseUnexecutedContext(context.Background(), issued); err != nil {
		t.Fatal(err)
	}
	if consumed, err := store.IsConsumed(string(issued.ID)); err != nil || consumed {
		t.Fatalf("released approval consumed=%v err=%v", consumed, err)
	}
}

func TestStore_IsConsumedRejectsInsecureConsumedRecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission regression")
	}
	dir := t.TempDir()
	store := approval.NewStore(dir)
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	issued := domain.Approval{
		ID: "app-insecure-consumed", Actor: "operator:privacy", Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		IdempotencyKey:  "insecure-consumed", IssuedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := store.Issue(issued); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkConsumed(issued, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, string(issued.ID)+".json")
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if consumed, err := store.IsConsumed(string(issued.ID)); consumed || !errors.Is(err, approval.ErrInsecurePermissions) {
		t.Fatalf("IsConsumed = (%v, %v), want false and ErrInsecurePermissions", consumed, err)
	}
}

func TestApproval_DTO_Conversion(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	consumedAt := now.Add(10 * time.Minute)
	app := domain.Approval{
		ID:              "app-200",
		Actor:           "user:admin",
		Target:          "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		IdempotencyKey:  "key-200",
		IssuedAt:        now,
		ExpiresAt:       now.Add(time.Hour),
		Consumed:        true,
		ConsumedAt:      &consumedAt,
	}

	dto := approval.ConvertToDTO(app)
	converted, err := approval.ConvertFromDTO(dto)
	if err != nil {
		t.Fatalf("ConvertFromDTO failed: %v", err)
	}
	if converted.ID != app.ID || !converted.Consumed || converted.ConsumedAt == nil {
		t.Errorf("converted mismatch: %+v", converted)
	}

	// Invalid issued_at
	badDTO := dto
	badDTO.IssuedAt = "invalid-date"
	if _, err := approval.ConvertFromDTO(badDTO); err == nil {
		t.Errorf("expected error for invalid issued_at")
	}

	// Invalid expires_at
	badDTO = dto
	badDTO.ExpiresAt = "invalid-date"
	if _, err := approval.ConvertFromDTO(badDTO); err == nil {
		t.Errorf("expected error for invalid expires_at")
	}

	// Invalid consumed_at
	badDTO = dto
	badConsumed := "invalid-date"
	badDTO.ConsumedAt = &badConsumed
	if _, err := approval.ConvertFromDTO(badDTO); err == nil {
		t.Errorf("expected error for invalid consumed_at")
	}
}

func TestLoader_EdgeCases(t *testing.T) {
	if _, err := approval.LoadFromFile(""); err == nil {
		t.Errorf("expected error for empty path")
	}
	if _, err := approval.LoadFromFile("non_existent_file.json"); err == nil {
		t.Errorf("expected error for non-existent file")
	}
}

func TestStore_ConcurrentMarkConsumed_ExactlyOneWinner(t *testing.T) {
	dir := t.TempDir()
	store := approval.NewStore(dir)

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	app := domain.Approval{
		ID:              "app-concurrent-1",
		Actor:           "user:operator",
		Target:          "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		IdempotencyKey:  "key-conc-1",
		IssuedAt:        now,
		ExpiresAt:       now.Add(time.Hour),
	}
	if err := store.Issue(app); err != nil {
		t.Fatalf("Issue failed: %v", err)
	}

	numWorkers := 10
	var successCount int
	var consumedCount int
	var otherErrors int

	var wg sync.WaitGroup
	var mu sync.Mutex
	wg.Add(numWorkers)

	for range numWorkers {
		go func() {
			defer wg.Done()
			err := store.MarkConsumed(app, now.Add(time.Minute))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successCount++
			case errors.Is(err, domain.ErrApprovalConsumed):
				consumedCount++
			default:
				otherErrors++
			}
		}()
	}

	wg.Wait()

	if successCount != 1 {
		t.Fatalf("expected exactly 1 successful consumption, got %d (consumed: %d, other errs: %d)", successCount, consumedCount, otherErrors)
	}
	if consumedCount != numWorkers-1 {
		t.Fatalf("expected %d ErrApprovalConsumed, got %d", numWorkers-1, consumedCount)
	}
}

func TestStore_CorruptAndMissingFiles(t *testing.T) {
	dir := t.TempDir()
	store := approval.NewStore(dir)

	// Symlink in approval file
	targetFile := filepath.Join(dir, "target.json")
	_ = os.WriteFile(targetFile, []byte("{}"), 0600)
	symlinkFile := filepath.Join(dir, "app-symlink.json")
	if err := os.Symlink(targetFile, symlinkFile); err == nil {
		if _, err := store.IsConsumed("app-symlink"); err == nil {
			t.Errorf("expected error for symlink approval file")
		}
	}

	// Missing file in LoadFromFile
	_, err := approval.LoadFromFile(filepath.Join(dir, "nonexistent.json"))
	if err == nil {
		t.Errorf("expected error loading missing approval file")
	}
}

func TestApprovalIDCanonicalGrammar(t *testing.T) {
	valid := []string{"a", "app-001", "approval_2026-A"}
	for _, id := range valid {
		if err := domain.ValidateApprovalID(id); err != nil {
			t.Errorf("valid approval ID %q rejected: %v", id, err)
		}
	}

	invalid := []string{
		"", ".", "..", "../outside", `..\\outside`, "/absolute", `C:\\absolute`,
		"app/child", `app\\child`, "app..child", "-leading", "trailing_", "has space",
		"app\u2215confusable", "\u0430pp-confusable", strings.Repeat("a", domain.MaxApprovalIDLength+1),
	}
	for _, id := range invalid {
		if err := domain.ValidateApprovalID(id); !errors.Is(err, domain.ErrInvalidApprovalRecord) {
			t.Errorf("invalid approval ID %q error = %v, want ErrInvalidApprovalRecord", id, err)
		}
	}
}

func TestStore_RejectsUnsafeApprovalIDsBeforePathAccess(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "approvals")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside.json")
	if err := os.WriteFile(outside, []byte("synthetic sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	store := approval.NewStore(dir)

	for _, id := range []string{"../outside", "/absolute", `..\\outside`, "app/child", "app..child", "\u0430pp-confusable"} {
		if consumed, err := store.IsConsumed(id); err == nil || consumed {
			t.Errorf("IsConsumed(%q) = (%v, %v), want validation failure", id, consumed, err)
		}
		invalid := domain.Approval{ID: domain.ApprovalID(id)}
		if err := store.MarkConsumed(invalid, time.Now().UTC()); err == nil {
			t.Errorf("MarkConsumed(%q) unexpectedly succeeded", id)
		}
	}

	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "synthetic sentinel" {
		t.Fatalf("outside sentinel changed: data=%q err=%v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("unsafe IDs created approval-store entries: %v", entries)
	}
}
