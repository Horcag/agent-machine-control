package approval_test

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

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

	if err := os.WriteFile(filePath, []byte(content), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

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

	if err := os.WriteFile(filePath, []byte(content), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

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
	_ = os.WriteFile(wsPath, []byte(validContent+"\n \t \n"), 0600)
	app, err := approval.LoadFromFile(wsPath)
	if err != nil || app == nil {
		t.Fatalf("expected whitespace trailing data to succeed, got %v", err)
	}

	// 2. Trailing second object is rejected
	objPath := filepath.Join(dir, "trailing_obj.json")
	_ = os.WriteFile(objPath, []byte(validContent+"\n{\"extra\":1}"), 0600)
	if _, err := approval.LoadFromFile(objPath); err == nil || !errors.Is(err, approval.ErrTrailingData) {
		t.Fatalf("expected ErrTrailingData for trailing object, got %v", err)
	}

	// 3. Trailing scalar is rejected
	scalarPath := filepath.Join(dir, "trailing_scalar.json")
	_ = os.WriteFile(scalarPath, []byte(validContent+"\n42"), 0600)
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

	_ = os.WriteFile(realFile, []byte(content), 0600)
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
	_ = os.Symlink(targetFile, symlinkFile)
	_, err := store.IsConsumed("app-symlink")
	if err == nil {
		t.Errorf("expected error for symlink approval file")
	}

	// Missing file in LoadFromFile
	_, err = approval.LoadFromFile(filepath.Join(dir, "nonexistent.json"))
	if err == nil {
		t.Errorf("expected error loading missing approval file")
	}
}
