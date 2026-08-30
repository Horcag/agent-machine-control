package operations_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/operations"
)

func TestOperationStoreContextCancellationStopsStorageBoundaries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	if err := operations.SaveRecordContext(ctx, dir, domain.OperationRecord{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("SaveRecordContext error = %v", err)
	}
	if _, err := operations.ReadRecordContext(ctx, dir, "invalid"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadRecordContext error = %v", err)
	}
	if _, err := operations.ListRecordsContext(ctx, dir, operations.ListOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListRecordsContext error = %v", err)
	}
}
