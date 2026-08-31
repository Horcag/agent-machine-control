package target

import (
	"context"
	"errors"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/statedir"
)

func TestStoreFailsClosedAtSecurityAndCancellationBoundaries(t *testing.T) {
	if _, err := NewStore("relative"); err == nil {
		t.Fatal("relative store path unexpectedly accepted")
	}
	dir := testDirectory(t)
	security := &mutationJournalTestSecurity{}
	store := testStore(t, dir, WithSecurity(security))
	want := testDefault(t, vmA, "primary")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Save(canceled, want); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Save = %v", err)
	}

	synthetic := errors.New("synthetic security failure")
	security.dirErr = synthetic
	if _, err := store.Load(context.Background()); !errors.Is(err, ErrInsecureState) {
		t.Fatalf("Load directory security error = %v", err)
	}
	security.dirErr = nil
	security.protectErr = synthetic
	if _, err := store.Save(context.Background(), want); !errors.Is(err, ErrInsecureState) {
		t.Fatalf("Save file protection error = %v", err)
	}
	security.protectErr = nil
	security.fileErr = synthetic
	if _, err := store.Save(context.Background(), want); !errors.Is(err, ErrInsecureState) {
		t.Fatalf("Save temporary validation error = %v", err)
	}
	security.fileErr = nil
	publication, err := store.Save(context.Background(), want)
	requireDurablePublication(t, "seed", publication, err)
	if _, err := store.Clear(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Clear = %v", err)
	}
}

func TestStoreRejectsUnprovenCommitAndPendingCrossOperationRetry(t *testing.T) {
	dir := testDirectory(t)
	want := testDefault(t, vmA, "primary")
	synthetic := errors.New("synthetic namespace failure")

	uncertainSave := testStore(t, dir, WithOperations(Operations{
		Replace: func(context.Context, string, string) CommitResult { return CommitResult{} },
	}))
	if _, err := uncertainSave.Save(context.Background(), want); !errors.Is(err, ErrAtomicCommitUncertain) {
		t.Fatalf("uncertain Save error = %v", err)
	}
	readFailure := testStore(t, dir, WithOperations(Operations{
		ReadFile: func(context.Context, string) ([]byte, error) { return nil, synthetic },
	}))
	if _, err := readFailure.Load(context.Background()); !errors.Is(err, synthetic) {
		t.Fatalf("read failure = %v", err)
	}

	store := testStore(t, dir)
	publication, err := store.Save(context.Background(), want)
	requireDurablePublication(t, "seed clear", publication, err)
	store.operations.Remove = func(string) CommitResult { return CommitResult{} }
	if _, err := store.Clear(context.Background()); !errors.Is(err, ErrAtomicCommitUncertain) {
		t.Fatalf("uncertain Clear error = %v", err)
	}
	store.operations.Remove = func(string) CommitResult { return CommitResult{Err: synthetic} }
	if _, err := store.Clear(context.Background()); !errors.Is(err, synthetic) {
		t.Fatalf("failed Clear error = %v", err)
	}

	saveDir := testDirectory(t)
	failSaveSync := true
	pendingSave := testStore(t, saveDir, WithOperations(Operations{
		SyncDir: func(path string) error {
			if failSaveSync {
				failSaveSync = false
				return synthetic
			}
			return statedir.SyncDir(path)
		},
	}))
	if publication, err := pendingSave.Save(context.Background(), want); !publication.Committed || !errors.Is(err, ErrCommittedNotDurable) {
		t.Fatalf("pending Save = %+v, %v", publication, err)
	}
	if _, err := pendingSave.Clear(context.Background()); !errors.Is(err, ErrDurabilityPending) {
		t.Fatalf("Clear during pending Save = %v", err)
	}

	clearDir := testDirectory(t)
	failClearSync := false
	pendingClear := testStore(t, clearDir, WithOperations(Operations{
		SyncDir: func(path string) error {
			if failClearSync {
				failClearSync = false
				return synthetic
			}
			return statedir.SyncDir(path)
		},
	}))
	publication, err = pendingClear.Save(context.Background(), want)
	requireDurablePublication(t, "seed pending clear", publication, err)
	failClearSync = true
	if publication, err := pendingClear.Clear(context.Background()); !publication.Committed || !errors.Is(err, ErrCommittedNotDurable) {
		t.Fatalf("pending Clear = %+v, %v", publication, err)
	}
	if _, err := pendingClear.Save(context.Background(), want); !errors.Is(err, ErrDurabilityPending) {
		t.Fatalf("Save during pending Clear = %v", err)
	}
}
