package ssh

import (
	"context"
	"errors"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

func TestLocalKeyProviderCancellationStopsProtectedReads(t *testing.T) {
	sd, err := statedir.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	provider := NewLocalKeyProvider(sd)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	target := domain.MachineRef("c4a523d4-6b99-4d62-a5e2-4752c0f20001")
	if _, err := provider.GetMachineConfigContext(ctx, target); !errors.Is(err, context.Canceled) {
		t.Fatalf("config cancellation error = %v", err)
	}
	if _, err := provider.GetClientSigner(ctx, target); !errors.Is(err, context.Canceled) {
		t.Fatalf("key cancellation error = %v", err)
	}
}
