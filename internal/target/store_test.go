package target

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

const (
	vmA = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	vmB = "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
)

func testDirectory(t *testing.T) string {
	t.Helper()
	state, err := statedir.Resolve(t.TempDir())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := state.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	return state.TargetsDir()
}

func testDefault(t *testing.T, vmID string, aliases ...string) Default {
	t.Helper()
	locator, err := domain.NewMachineLocator(domain.LocalHostID, vmID)
	if err != nil {
		t.Fatalf("NewMachineLocator: %v", err)
	}
	value, err := NewDefault(locator, aliases)
	if err != nil {
		t.Fatalf("NewDefault: %v", err)
	}
	return value
}

func testStore(t *testing.T, dir string, options ...Option) *Store {
	t.Helper()
	store, err := NewStore(dir, options...)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func requireDurablePublication(t *testing.T, action string, publication Publication, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s error: %v", action, err)
	}
	if !publication.Committed || !publication.Durable {
		t.Fatalf("%s publication = %+v", action, publication)
	}
}

type recordingSecurity struct {
	mu                sync.Mutex
	events            []string
	protectDirErr     error
	validateDirErr    error
	inheritedWasEmpty bool
}

func (s *recordingSecurity) record(event string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *recordingSecurity) ValidateDir(context.Context, string) error {
	s.record("validate-dir")
	return s.validateDirErr
}

func (s *recordingSecurity) ProtectDir(context.Context, string) error {
	s.record("protect-dir")
	return s.protectDirErr
}

func (s *recordingSecurity) ValidateInheritedFile(_ context.Context, path string) error {
	s.record("validate-inherited-file")
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.inheritedWasEmpty = info.Size() == 0
	s.mu.Unlock()
	return nil
}

func (s *recordingSecurity) ValidateFile(context.Context, string) error {
	s.record("validate-file")
	return nil
}

func (s *recordingSecurity) ProtectFile(context.Context, string) error {
	s.record("protect-file")
	return nil
}

func (s *recordingSecurity) snapshot() ([]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...), s.inheritedWasEmpty
}

func TestStoreSaveProtectsDirectoryBeforeCreatingTemporaryState(t *testing.T) {
	dir := testDirectory(t)
	security := &recordingSecurity{}
	store := testStore(t, dir, WithSecurity(security))
	publication, err := store.Save(context.Background(), testDefault(t, vmA))
	requireDurablePublication(t, "Save", publication, err)

	events, inheritedWasEmpty := security.snapshot()
	protectIndex := slices.Index(events, "protect-dir")
	inheritedIndex := slices.Index(events, "validate-inherited-file")
	fileProtectIndex := slices.Index(events, "protect-file")
	if protectIndex < 0 || inheritedIndex <= protectIndex || fileProtectIndex <= inheritedIndex {
		t.Fatalf("security events = %v, want directory protection before inherited and final file proofs", events)
	}
	if !inheritedWasEmpty {
		t.Fatal("temporary file contained payload bytes before inherited authority was validated")
	}
}

func TestStoreSaveRejectsDirectoryProtectionFailureBeforeWritingState(t *testing.T) {
	dir := testDirectory(t)
	security := &recordingSecurity{protectDirErr: errors.New("foreign owner")}
	store := testStore(t, dir, WithSecurity(security))
	publication, err := store.Save(context.Background(), testDefault(t, vmA))
	if err == nil || publication.Committed {
		t.Fatalf("Save = %+v, %v, want pre-commit protection failure", publication, err)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("directory entries after rejected protection = %v, want none", entries)
	}
}

func TestStoreLoadAndClearNeverRepairDirectorySecurity(t *testing.T) {
	dir := testDirectory(t)
	security := &recordingSecurity{protectDirErr: errors.New("must not be called")}
	store := testStore(t, dir, WithSecurity(security))
	if _, err := store.Load(context.Background()); !errors.Is(err, ErrNoDefault) {
		t.Fatalf("Load error = %v, want ErrNoDefault", err)
	}
	publication, err := store.Clear(context.Background())
	requireDurablePublication(t, "Clear", publication, err)
	events, _ := security.snapshot()
	if slices.Contains(events, "protect-dir") {
		t.Fatalf("validation-only operations repaired directory security: %v", events)
	}
}

func TestStoreSaveLoadRestartIdempotentAndClear(t *testing.T) {
	dir := testDirectory(t)
	store := testStore(t, dir)
	want := testDefault(t, vmA, "zeta", "alpha")

	publication, err := store.Save(context.Background(), want)
	requireDurablePublication(t, "Save", publication, err)
	payload, err := os.ReadFile(filepath.Join(dir, StateFileName))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	exact := fmt.Sprintf("{\"schema_version\":1,\"default_locator\":\"local:%s\",\"aliases\":[\"alpha\",\"zeta\"]}\n", vmA)
	if string(payload) != exact {
		t.Fatalf("stored document = %q, want %q", payload, exact)
	}
	info, err := os.Stat(filepath.Join(dir, StateFileName))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("state mode = %v, %v", info.Mode().Perm(), err)
	}

	reopened := testStore(t, dir)
	got, err := reopened.Load(context.Background())
	if err != nil || !got.equal(want) {
		t.Fatalf("Load after restart = %+v, %v", got, err)
	}
	publication, err = reopened.Save(context.Background(), want)
	requireDurablePublication(t, "idempotent Save", publication, err)
	publication, err = reopened.Clear(context.Background())
	requireDurablePublication(t, "Clear", publication, err)
	if _, err := reopened.Load(context.Background()); !errors.Is(err, ErrNoDefault) {
		t.Fatalf("Load after Clear = %v, want ErrNoDefault", err)
	}
}

func TestStoreStrictDocumentValidation(t *testing.T) {
	dir := testDirectory(t)
	path := filepath.Join(dir, StateFileName)
	validPrefix := fmt.Sprintf(`{"schema_version":1,"default_locator":"local:%s","aliases":[]}`, vmA)
	tests := map[string]string{
		"malformed":           `{`,
		"trailing":            validPrefix + `{}`,
		"unknown":             fmt.Sprintf(`{"schema_version":1,"default_locator":"local:%s","aliases":[],"extra":true}`, vmA),
		"duplicate field":     fmt.Sprintf(`{"schema_version":1,"schema_version":1,"default_locator":"local:%s","aliases":[]}`, vmA),
		"wrong schema":        fmt.Sprintf(`{"schema_version":2,"default_locator":"local:%s","aliases":[]}`, vmA),
		"missing aliases":     fmt.Sprintf(`{"schema_version":1,"default_locator":"local:%s"}`, vmA),
		"null aliases":        fmt.Sprintf(`{"schema_version":1,"default_locator":"local:%s","aliases":null}`, vmA),
		"remote locator":      fmt.Sprintf(`{"schema_version":1,"default_locator":"remote:%s","aliases":[]}`, vmA),
		"padded locator":      fmt.Sprintf(`{"schema_version":1,"default_locator":" local:%s","aliases":[]}`, vmA),
		"padded alias":        fmt.Sprintf(`{"schema_version":1,"default_locator":"local:%s","aliases":[" alias "]}`, vmA),
		"duplicate alias":     fmt.Sprintf(`{"schema_version":1,"default_locator":"local:%s","aliases":["alias","alias"]}`, vmA),
		"reserved alias":      fmt.Sprintf(`{"schema_version":1,"default_locator":"local:%s","aliases":["default"]}`, vmA),
		"canonical collision": fmt.Sprintf(`{"schema_version":1,"default_locator":"local:%s","aliases":["%s"]}`, vmA, vmA),
		"oversized":           string(make([]byte, MaxDocumentBytes+1)),
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			store := testStore(t, dir)
			if _, err := store.Load(context.Background()); !errors.Is(err, ErrInvalidDocument) && !errors.Is(err, ErrUnsupportedHost) {
				t.Fatalf("Load error = %v", err)
			}
		})
	}
}

func TestStoreRejectsInsecureAndLinkedState(t *testing.T) {
	dir := testDirectory(t)
	path := filepath.Join(dir, StateFileName)
	value := testDefault(t, vmA)
	payload, _ := encode(value)
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if _, err := testStore(t, dir).Load(context.Background()); !errors.Is(err, ErrInsecureState) {
		t.Fatalf("insecure mode error = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	realPath := filepath.Join(dir, "real.json")
	if err := os.WriteFile(realPath, payload, 0600); err != nil {
		t.Fatalf("WriteFile real: %v", err)
	}
	if err := os.Symlink(realPath, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := testStore(t, dir).Load(context.Background()); !errors.Is(err, ErrInsecureState) {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestStorePreCommitFailurePreservesAcceptedState(t *testing.T) {
	dir := testDirectory(t)
	accepted := testDefault(t, vmA, "accepted")
	if _, err := testStore(t, dir).Save(context.Background(), accepted); err != nil {
		t.Fatalf("initial Save: %v", err)
	}
	failing := testStore(t, dir, WithOperations(Operations{
		Replace: func(context.Context, string, string) CommitResult {
			return CommitResult{Err: errors.New("injected pre-commit failure")}
		},
	}))
	publication, err := failing.Save(context.Background(), testDefault(t, vmB, "replacement"))
	if err == nil || publication.Committed {
		t.Fatalf("failed Save = %+v, %v", publication, err)
	}
	got, err := testStore(t, dir).Load(context.Background())
	if err != nil || !got.equal(accepted) {
		t.Fatalf("accepted state changed: %+v, %v", got, err)
	}
}

func TestStorePostCommitFailureRequiresExactRepair(t *testing.T) {
	dir := testDirectory(t)
	var namespaceCommitted atomic.Bool
	store := testStore(t, dir, WithOperations(Operations{
		Replace: func(ctx context.Context, oldPath, newPath string) CommitResult {
			result := atomicReplace(ctx, oldPath, newPath)
			namespaceCommitted.Store(result.Committed)
			return result
		},
		SyncDir: func(path string) error {
			if namespaceCommitted.Swap(false) {
				return errors.New("injected post-commit sync failure")
			}
			return statedir.SyncDir(path)
		},
	}))
	committed := testDefault(t, vmA, "primary")
	publication, err := store.Save(context.Background(), committed)
	if !errors.Is(err, ErrCommittedNotDurable) || !publication.Committed || publication.Durable {
		t.Fatalf("post-commit Save = %+v, %v", publication, err)
	}
	if publication, err := store.Save(context.Background(), testDefault(t, vmB)); !errors.Is(err, ErrDurabilityPending) || publication.Committed {
		t.Fatalf("non-exact retry = %+v, %v", publication, err)
	}
	publication, err = store.Save(context.Background(), committed)
	if err != nil || !publication.Committed || !publication.Durable {
		t.Fatalf("exact repair = %+v, %v", publication, err)
	}
	got, err := store.Load(context.Background())
	if err != nil || !got.equal(committed) {
		t.Fatalf("Load after repair = %+v, %v", got, err)
	}
}

func TestStorePendingSaveSecurityFailurePreservesPriorCommitTruth(t *testing.T) {
	dir := testDirectory(t)
	var namespaceCommitted atomic.Bool
	store := testStore(t, dir, WithOperations(Operations{
		Replace: func(ctx context.Context, oldPath, newPath string) CommitResult {
			result := atomicReplace(ctx, oldPath, newPath)
			namespaceCommitted.Store(result.Committed)
			return result
		},
		SyncDir: func(path string) error {
			if namespaceCommitted.Swap(false) {
				return errors.New("injected post-commit sync failure")
			}
			return statedir.SyncDir(path)
		},
	}))
	want := testDefault(t, vmA)
	publication, err := store.Save(context.Background(), want)
	if !errors.Is(err, ErrCommittedNotDurable) || !publication.Committed {
		t.Fatalf("first Save = %+v, %v", publication, err)
	}
	store.security = &recordingSecurity{protectDirErr: errors.New("injected directory drift")}
	publication, err = store.Save(context.Background(), want)
	if !errors.Is(err, ErrCommittedNotDurable) || !publication.Committed || publication.Durable {
		t.Fatalf("exact retry = %+v, %v, want prior commit truth", publication, err)
	}
}

func TestStorePendingClearSecurityFailurePreservesPriorCommitTruth(t *testing.T) {
	dir := testDirectory(t)
	store := testStore(t, dir)
	publication, err := store.Save(context.Background(), testDefault(t, vmA))
	requireDurablePublication(t, "initial Save", publication, err)
	store.operations.Remove = func(path string) CommitResult {
		if err := os.Remove(path); err != nil {
			return CommitResult{Err: err}
		}
		return CommitResult{Committed: true, Err: errors.New("injected post-remove failure")}
	}
	publication, err = store.Clear(context.Background())
	if !errors.Is(err, ErrCommittedNotDurable) || !publication.Committed {
		t.Fatalf("first Clear = %+v, %v", publication, err)
	}
	store.security = &recordingSecurity{validateDirErr: errors.New("injected directory drift")}
	publication, err = store.Clear(context.Background())
	if !errors.Is(err, ErrCommittedNotDurable) || !publication.Committed || publication.Durable {
		t.Fatalf("exact retry = %+v, %v, want prior commit truth", publication, err)
	}
}

func TestStoreRestartRepairsCommittedSaveWithoutSecondReplace(t *testing.T) {
	dir := testDirectory(t)
	var replaceCalls atomic.Int32
	var failPostCommit atomic.Bool
	failPostCommit.Store(true)
	operations := Operations{
		Replace: func(ctx context.Context, oldPath, newPath string) CommitResult {
			replaceCalls.Add(1)
			return atomicReplace(ctx, oldPath, newPath)
		},
		SyncDir: func(path string) error {
			if replaceCalls.Load() > 0 && failPostCommit.Swap(false) {
				return errors.New("injected post-commit sync failure")
			}
			return statedir.SyncDir(path)
		},
	}
	want := testDefault(t, vmA, "primary")
	publication, err := testStore(t, dir, WithOperations(operations)).Save(context.Background(), want)
	if !errors.Is(err, ErrCommittedNotDurable) || !publication.Committed || publication.Durable {
		t.Fatalf("first Save = %+v, %v", publication, err)
	}

	publication, err = testStore(t, dir, WithOperations(operations)).Save(context.Background(), want)
	requireDurablePublication(t, "restart repair", publication, err)
	if replaceCalls.Load() != 1 {
		t.Fatalf("replace calls = %d, want exactly one namespace commit", replaceCalls.Load())
	}
}

func TestStoreRestartRepairsCommittedClearWithoutSecondRemove(t *testing.T) {
	dir := testDirectory(t)
	want := testDefault(t, vmA)
	store := testStore(t, dir)
	publication, err := store.Save(context.Background(), want)
	requireDurablePublication(t, "initial Save", publication, err)

	var removeCalls atomic.Int32
	var failPostCommit atomic.Bool
	failPostCommit.Store(true)
	operations := Operations{
		Remove: func(path string) CommitResult {
			removeCalls.Add(1)
			if err := os.Remove(path); err != nil {
				return CommitResult{Err: err}
			}
			return CommitResult{Committed: true}
		},
		SyncDir: func(path string) error {
			if removeCalls.Load() > 0 && failPostCommit.Swap(false) {
				return errors.New("injected post-commit sync failure")
			}
			return statedir.SyncDir(path)
		},
	}
	publication, err = testStore(t, dir, WithOperations(operations)).Clear(context.Background())
	if !errors.Is(err, ErrCommittedNotDurable) || !publication.Committed || publication.Durable {
		t.Fatalf("first Clear = %+v, %v", publication, err)
	}

	publication, err = testStore(t, dir, WithOperations(operations)).Clear(context.Background())
	requireDurablePublication(t, "restart clear repair", publication, err)
	if removeCalls.Load() != 1 {
		t.Fatalf("remove calls = %d, want exactly one namespace effect", removeCalls.Load())
	}
}

func TestStoreRestartRepairSyncFailureFailsClosed(t *testing.T) {
	dir := testDirectory(t)
	want := testDefault(t, vmA)
	var replaceCalls atomic.Int32
	var failPostCommit atomic.Bool
	failPostCommit.Store(true)
	publication, err := testStore(t, dir, WithOperations(Operations{
		Replace: func(ctx context.Context, oldPath, newPath string) CommitResult {
			replaceCalls.Add(1)
			return atomicReplace(ctx, oldPath, newPath)
		},
		SyncDir: func(path string) error {
			if replaceCalls.Load() > 0 && failPostCommit.Swap(false) {
				return errors.New("injected post-commit sync failure")
			}
			return statedir.SyncDir(path)
		},
	})).Save(context.Background(), want)
	if !errors.Is(err, ErrCommittedNotDurable) || !publication.Committed || publication.Durable {
		t.Fatalf("first Save = %+v, %v", publication, err)
	}

	store := testStore(t, dir, WithOperations(Operations{
		SyncDir: func(string) error { return errors.New("injected restart sync failure") },
	}))
	if got, err := store.Load(context.Background()); err == nil || !got.equal(Default{}) {
		t.Fatalf("Load = %+v, %v, want fail-closed sync error", got, err)
	}
}

func TestStoreCommittedCloseErrorRequiresExactRepairWithoutSecondReplace(t *testing.T) {
	dir := testDirectory(t)
	var inject atomic.Bool
	var replaceCalls atomic.Int32
	store := testStore(t, dir, WithOperations(Operations{
		Replace: func(ctx context.Context, oldPath, newPath string) CommitResult {
			replaceCalls.Add(1)
			result := atomicReplace(ctx, oldPath, newPath)
			if result.Committed && !inject.Swap(true) {
				result.Err = errors.New("target: FileRenameInfoEx committed but source handle close failed: injected close failure")
			}
			return result
		},
	}))
	want := testDefault(t, vmA, "primary")
	publication, err := store.Save(context.Background(), want)
	if !errors.Is(err, ErrCommittedNotDurable) || !publication.Committed || publication.Durable {
		t.Fatalf("Save = %+v, %v", publication, err)
	}
	if publication, err := store.Save(context.Background(), testDefault(t, vmB)); !errors.Is(err, ErrDurabilityPending) || publication.Committed {
		t.Fatalf("non-exact retry = %+v, %v", publication, err)
	}
	publication, err = store.Save(context.Background(), want)
	requireDurablePublication(t, "exact close-anomaly repair", publication, err)
	if replaceCalls.Load() != 1 {
		t.Fatalf("replace calls = %d, want one namespace commit and no fallback", replaceCalls.Load())
	}
}

func TestStoreCommittedRemoveErrorPreservesEffectTruth(t *testing.T) {
	dir := testDirectory(t)
	store := testStore(t, dir)
	want := testDefault(t, vmA)
	publication, err := store.Save(context.Background(), want)
	requireDurablePublication(t, "initial Save", publication, err)
	var inject atomic.Bool
	store.operations.Remove = func(path string) CommitResult {
		result := CommitResult{}
		if err := os.Remove(path); err != nil {
			result.Err = err
			return result
		}
		result.Committed = true
		if !inject.Swap(true) {
			result.Err = errors.New("injected post-remove failure")
		}
		return result
	}
	publication, err = store.Clear(context.Background())
	if !errors.Is(err, ErrCommittedNotDurable) || !publication.Committed || publication.Durable {
		t.Fatalf("Clear = %+v, %v", publication, err)
	}
	publication, err = store.Clear(context.Background())
	requireDurablePublication(t, "exact clear repair", publication, err)
}

func TestStoreReadBackFailureRepairsOnLoad(t *testing.T) {
	dir := testDirectory(t)
	var reads atomic.Int32
	var store *Store
	store = testStore(t, dir, WithOperations(Operations{
		ReadFile: func(ctx context.Context, path string) ([]byte, error) {
			if reads.Add(1) == 2 {
				return nil, errors.New("injected read-back failure")
			}
			return store.readProtectedFile(ctx, path)
		},
	}))
	want := testDefault(t, vmA, "primary")
	publication, err := store.Save(context.Background(), want)
	if !errors.Is(err, ErrCommittedNotDurable) || !publication.Committed {
		t.Fatalf("Save = %+v, %v", publication, err)
	}
	got, err := store.Load(context.Background())
	if err != nil || !got.equal(want) {
		t.Fatalf("Load repair = %+v, %v", got, err)
	}
}

func TestStoreCanceledAfterCommitRepairsExactly(t *testing.T) {
	dir := testDirectory(t)
	ctx, cancel := context.WithCancel(context.Background())
	store := testStore(t, dir, WithOperations(Operations{
		Replace: func(ctx context.Context, oldPath, newPath string) CommitResult {
			result := atomicReplace(ctx, oldPath, newPath)
			cancel()
			return result
		},
	}))
	want := testDefault(t, vmA)
	publication, err := store.Save(ctx, want)
	if !errors.Is(err, context.Canceled) || !publication.Committed || publication.Durable {
		t.Fatalf("Save = %+v, %v", publication, err)
	}
	publication, err = store.Save(context.Background(), want)
	requireDurablePublication(t, "canceled commit repair", publication, err)
}

func TestStoreClearDurabilityFailuresRepairOnLoad(t *testing.T) {
	dir := testDirectory(t)
	var failSync atomic.Bool
	var clearCommitted atomic.Bool
	store := testStore(t, dir, WithOperations(Operations{
		Remove: func(path string) CommitResult {
			if err := os.Remove(path); err != nil {
				return CommitResult{Err: err}
			}
			clearCommitted.Store(true)
			return CommitResult{Committed: true}
		},
		SyncDir: func(path string) error {
			if clearCommitted.Load() && failSync.Swap(false) {
				return errors.New("injected clear sync failure")
			}
			return statedir.SyncDir(path)
		},
	}))
	want := testDefault(t, vmA)
	publication, err := store.Save(context.Background(), want)
	requireDurablePublication(t, "initial Save", publication, err)
	failSync.Store(true)
	publication, err = store.Clear(context.Background())
	if !errors.Is(err, ErrCommittedNotDurable) || !publication.Committed || publication.Durable {
		t.Fatalf("Clear = %+v, %v", publication, err)
	}
	if _, err := store.Load(context.Background()); !errors.Is(err, ErrNoDefault) {
		t.Fatalf("Load after clear repair = %v", err)
	}
}

func TestStoreRejectsFalseCommittedClearProof(t *testing.T) {
	dir := testDirectory(t)
	store := testStore(t, dir)
	publication, err := store.Save(context.Background(), testDefault(t, vmA))
	requireDurablePublication(t, "initial Save", publication, err)
	store.operations.Remove = func(string) CommitResult { return CommitResult{Committed: true} }
	publication, err = store.Clear(context.Background())
	if !errors.Is(err, ErrCommittedNotDurable) || !publication.Committed || publication.Durable {
		t.Fatalf("Clear = %+v, %v", publication, err)
	}
}

func TestStoreConcurrentSaveClearNeverCorruptsAuthority(t *testing.T) {
	dir := testDirectory(t)
	store := testStore(t, dir)
	values := []Default{testDefault(t, vmA, "alpha"), testDefault(t, vmB, "bravo")}
	var wait sync.WaitGroup
	for index := range 60 {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			if index%3 == 0 {
				_, _ = store.Clear(context.Background())
				return
			}
			_, _ = store.Save(context.Background(), values[index%len(values)])
		}(index)
	}
	wait.Wait()
	got, err := store.Load(context.Background())
	if errors.Is(err, ErrNoDefault) {
		return
	}
	if err != nil || !got.equal(values[0]) && !got.equal(values[1]) {
		t.Fatalf("final state = %+v, %v", got, err)
	}
}
