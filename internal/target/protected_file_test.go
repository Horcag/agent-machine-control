package target

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProtectInheritedFileForWriteClosesRejectedHandle(t *testing.T) {
	if _, err := protectInheritedFileForWrite(context.Background(), nil, "synthetic", nil); err == nil {
		t.Fatal("missing protected-file dependencies unexpectedly accepted")
	}

	path := filepath.Join(t.TempDir(), "state")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	synthetic := errors.New("synthetic inherited proof failure")
	if _, err := protectInheritedFileForWrite(context.Background(), file, path, &mutationJournalTestSecurity{fileErr: synthetic}); !errors.Is(err, synthetic) {
		t.Fatalf("protect inherited file error = %v, want synthetic failure", err)
	}
	if _, err := file.Write([]byte("must be closed")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("rejected inherited handle write = %v, want os.ErrClosed", err)
	}
}
