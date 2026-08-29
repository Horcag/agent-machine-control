package daemon_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/daemon"
)

type mockIdent struct {
	runtimeID string
	pid       int
	startTime string
}

func (m *mockIdent) CurrentIdentity() (string, int, string) {
	return m.runtimeID, m.pid, m.startTime
}

type mockLiveness struct {
	alive bool
}

func (m *mockLiveness) IsAlive(_ int, _ string) (bool, error) {
	return m.alive, nil
}

func TestLock_AcquireRelease_AndDeadReclaim(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	ident1 := &mockIdent{runtimeID: "rt-1", pid: 100, startTime: "2026-08-29T12:00:00Z"}
	checkerDead := &mockLiveness{alive: false}
	checkerAlive := &mockLiveness{alive: true}

	// 1. Acquire lock
	lock1, err := daemon.AcquireSingletonLock(dir, ident1, checkerAlive, now)
	if err != nil {
		t.Fatalf("AcquireSingletonLock 1 failed: %v", err)
	}

	// 2. Second acquire while owner is alive -> fails
	ident2 := &mockIdent{runtimeID: "rt-1", pid: 101, startTime: "2026-08-29T12:00:01Z"}
	_, err = daemon.AcquireSingletonLock(dir, ident2, checkerAlive, now)
	if err == nil {
		t.Errorf("expected lock acquisition to fail when owner is alive")
	}

	// 3. Second acquire when owner is dead -> succeeds by reclaiming
	lock2, err := daemon.AcquireSingletonLock(dir, ident2, checkerDead, now)
	if err != nil {
		t.Fatalf("expected dead lock reclamation to succeed, got: %v", err)
	}

	if err := lock2.Release(); err != nil {
		t.Errorf("lock2.Release failed: %v", err)
	}
	_ = lock1.Release()
}

func TestEndpoint_WriteReadRemove(t *testing.T) {
	dir := t.TempDir()
	rec := daemon.EndpointRecord{
		SchemaVersion:    daemon.SchemaVersion,
		PID:              1234,
		RuntimeID:        "rt-test",
		ProcessStartTime: "2026-08-29T12:00:00Z",
		StartedAt:        time.Now().UTC(),
		Endpoint:         "http://127.0.0.1:45678",
	}

	if err := daemon.WriteEndpointFile(dir, rec); err != nil {
		t.Fatalf("WriteEndpointFile failed: %v", err)
	}

	readRec, err := daemon.ReadEndpointFile(dir)
	if err != nil {
		t.Fatalf("ReadEndpointFile failed: %v", err)
	}
	if readRec.PID != 1234 || readRec.Endpoint != "http://127.0.0.1:45678" {
		t.Errorf("unexpected endpoint record: %+v", readRec)
	}

	// Remove if owned
	if err := daemon.RemoveEndpointFileIfOwned(dir, 1234, "rt-test", "2026-08-29T12:00:00Z"); err != nil {
		t.Fatalf("RemoveEndpointFileIfOwned failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "endpoint.json")); !os.IsNotExist(err) {
		t.Errorf("expected endpoint file to be deleted")
	}
}

func TestEndpoint_CorruptAndEdgeCases(t *testing.T) {
	dir := t.TempDir()

	// Empty dir
	if err := daemon.WriteEndpointFile("", daemon.EndpointRecord{}); err == nil {
		t.Errorf("expected error for empty dir in WriteEndpointFile")
	}

	// Read non-existent
	if _, err := daemon.ReadEndpointFile(dir); err == nil {
		t.Errorf("expected error reading missing endpoint file")
	}

	// Corrupt JSON
	_ = os.WriteFile(filepath.Join(dir, "endpoint.json"), []byte("corrupt-json"), 0600)
	if _, err := daemon.ReadEndpointFile(dir); err == nil {
		t.Errorf("expected error reading corrupt endpoint file")
	}

	// Invalid schema version
	_ = os.WriteFile(filepath.Join(dir, "endpoint.json"), []byte(`{"schema_version":"99","pid":1,"endpoint":"http://127.0.0.1:1"}`), 0600)
	if _, err := daemon.ReadEndpointFile(dir); err == nil {
		t.Errorf("expected error reading invalid schema endpoint file")
	}

	// Non-loopback endpoint rejection
	nonLoopbackRec := daemon.EndpointRecord{
		SchemaVersion: daemon.SchemaVersion,
		PID:           1234,
		RuntimeID:     "rt-test",
		Endpoint:      "http://0.0.0.0:8080",
	}
	if err := daemon.WriteEndpointFile(dir, nonLoopbackRec); err == nil {
		t.Errorf("expected error writing non-loopback endpoint")
	}

	// Symlink endpoint rejection
	realEndpoint := filepath.Join(dir, "real_endpoint.json")
	_ = daemon.WriteEndpointFile(dir, daemon.EndpointRecord{
		SchemaVersion: daemon.SchemaVersion,
		PID:           1234,
		RuntimeID:     "rt-test",
		Endpoint:      "http://127.0.0.1:8080",
	})
	_ = os.Rename(filepath.Join(dir, "endpoint.json"), realEndpoint)
	_ = os.Symlink(realEndpoint, filepath.Join(dir, "endpoint.json"))
	if _, err := daemon.ReadEndpointFile(dir); err == nil {
		t.Errorf("expected error reading symlinked endpoint file")
	}

	// ValidateEndpointURL tests
	if err := daemon.ValidateEndpointURL(""); err == nil {
		t.Errorf("expected error for empty endpoint URL")
	}
	if err := daemon.ValidateEndpointURL("https://127.0.0.1:8080"); err == nil {
		t.Errorf("expected error for https scheme")
	}
	if err := daemon.ValidateEndpointURL("http://example.com:8080"); err == nil {
		t.Errorf("expected error for non-loopback host")
	}
	if err := daemon.ValidateEndpointURL("http://127.0.0.1:8080"); err != nil {
		t.Errorf("expected valid for loopback URL: %v", err)
	}

	// RemoveEndpointFileIfOwned on unowned
	_ = os.Remove(filepath.Join(dir, "endpoint.json"))
	_ = daemon.WriteEndpointFile(dir, daemon.EndpointRecord{
		SchemaVersion: daemon.SchemaVersion,
		PID:           1234,
		RuntimeID:     "rt-test",
		Endpoint:      "http://127.0.0.1:8080",
	})
	if err := daemon.RemoveEndpointFileIfOwned(dir, 9999, "rt-other", ""); err != nil {
		t.Errorf("expected nil when not matching owner, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "endpoint.json")); os.IsNotExist(err) {
		t.Errorf("unowned remove must not delete file")
	}
}

func TestLock_OwnerRecordValidationErrors(t *testing.T) {
	dir := t.TempDir()
	lockDir := filepath.Join(dir, "singleton.lock")
	_ = os.Mkdir(lockDir, 0700)
	ownerPath := filepath.Join(lockDir, "owner.json")

	ident := &mockIdent{runtimeID: "rt-1", pid: 100, startTime: "2026-08-29T12:00:00Z"}
	checker := &mockLiveness{alive: false}
	now := time.Now().UTC()

	// 1. Corrupt JSON
	_ = os.WriteFile(ownerPath, []byte("bad-json"), 0600)
	_, err := daemon.AcquireSingletonLock(dir, ident, checker, now)
	if err == nil {
		t.Errorf("expected AcquireSingletonLock to fail on corrupt owner.json")
	}

	// 2. Trailing data
	_ = os.WriteFile(ownerPath, []byte(`{"schema_version":"1","pid":100,"runtime_id":"rt-1"} extra`), 0600)
	_, err = daemon.AcquireSingletonLock(dir, ident, checker, now)
	if err == nil {
		t.Errorf("expected AcquireSingletonLock to fail on trailing data in owner.json")
	}

	// 3. Invalid schema version or invalid PID
	_ = os.WriteFile(ownerPath, []byte(`{"schema_version":"99","pid":100,"runtime_id":"rt-1"}`), 0600)
	_, err = daemon.AcquireSingletonLock(dir, ident, checker, now)
	if err == nil {
		t.Errorf("expected AcquireSingletonLock to fail on invalid schema")
	}

	// 4. Symlink owner file
	_ = os.Remove(ownerPath)
	realOwner := filepath.Join(dir, "real_owner.json")
	_ = os.WriteFile(realOwner, []byte(`{"schema_version":"1","pid":100,"runtime_id":"rt-1"}`), 0600)
	_ = os.Symlink(realOwner, ownerPath)
	_, err = daemon.AcquireSingletonLock(dir, ident, checker, now)
	if err == nil {
		t.Errorf("expected AcquireSingletonLock to fail on symlinked owner.json")
	}

	// 5. Cross-runtime ID dead lock rejection
	_ = os.Remove(ownerPath)
	_ = os.WriteFile(ownerPath, []byte(`{"schema_version":"1","pid":100,"runtime_id":"rt-other"}`), 0600)
	_, err = daemon.AcquireSingletonLock(dir, ident, checker, now)
	if err == nil {
		t.Errorf("expected AcquireSingletonLock to fail for cross-runtime lock")
	}

	// 6. Checker returns error
	checkerErr := &mockLivenessErr{}
	_ = os.Remove(ownerPath)
	_ = os.WriteFile(ownerPath, []byte(`{"schema_version":"1","pid":100,"runtime_id":"rt-1"}`), 0600)
	_, err = daemon.AcquireSingletonLock(dir, ident, checkerErr, now)
	if err == nil {
		t.Errorf("expected AcquireSingletonLock to fail when checker errors")
	}
}

type mockLivenessErr struct{}

func (m *mockLivenessErr) IsAlive(_ int, _ string) (bool, error) {
	return false, errors.New("liveness probe error")
}

func TestLock_ReleaseUnownedOrMissing(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	ident := &mockIdent{runtimeID: "rt-1", pid: 100, startTime: "2026-08-29T12:00:00Z"}
	checker := &mockLiveness{alive: true}

	lock, err := daemon.AcquireSingletonLock(dir, ident, checker, now)
	if err != nil {
		t.Fatalf("AcquireSingletonLock failed: %v", err)
	}

	// Overwrite owner file with different PID before Release
	lockDir := filepath.Join(dir, "singleton.lock")
	ownerPath := filepath.Join(lockDir, "owner.json")
	_ = os.WriteFile(ownerPath, []byte(`{"schema_version":"1","pid":999,"runtime_id":"rt-1","process_start_time":"2026-08-29T12:00:00Z"}`), 0600)

	// Release should succeed silently without deleting other owner's file
	if err := lock.Release(); err != nil {
		t.Errorf("Release on unowned lock returned error: %v", err)
	}

	if _, err := os.Stat(ownerPath); os.IsNotExist(err) {
		t.Errorf("unowned Release must not delete owner file")
	}

	// Missing owner file on Release
	_ = os.Remove(ownerPath)
	if err := lock.Release(); err != nil {
		t.Errorf("Release on missing owner file returned error: %v", err)
	}

	// Corrupt owner file on Release -> must return error
	_ = os.WriteFile(ownerPath, []byte("corrupt-data"), 0600)
	if err := lock.Release(); err == nil {
		t.Errorf("expected Release to fail on corrupt owner file")
	}
}

func TestEndpoint_RemoveCorruptError(t *testing.T) {
	dir := t.TempDir()
	endpointFile := filepath.Join(dir, "endpoint.json")
	_ = os.WriteFile(endpointFile, []byte("corrupt-endpoint-data"), 0600)

	if err := daemon.RemoveEndpointFileIfOwned(dir, 1234, "rt-1", "start"); err == nil {
		t.Errorf("expected RemoveEndpointFileIfOwned to fail on corrupt endpoint file")
	}
}
