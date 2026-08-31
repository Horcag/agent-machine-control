package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/target"
)

type operationFilterTargetResolver struct {
	requests []string
	vmID     string
	err      error
}

func (r *operationFilterTargetResolver) ResolveTarget(_ context.Context, reference string) (app.TargetResolution, error) {
	r.requests = append(r.requests, reference)
	if r.err != nil {
		return app.TargetResolution{}, r.err
	}
	locator, err := domain.NewMachineLocator(domain.LocalHostID, r.vmID)
	if err != nil {
		return app.TargetResolution{}, err
	}
	return app.TargetResolution{Locator: locator, ProviderVMID: r.vmID}, nil
}

func TestOperationMachineFilterRequiresEnrolledTargetAuthority(t *testing.T) {
	const vmID = "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	resolver := &operationFilterTargetResolver{vmID: vmID}
	server := &Server{recoveryService: app.NewRecoveryService(nil, nil, nil, nil, nil, app.WithRecoveryTargetResolver(resolver))}
	for _, reference := range []string{vmID, "local:" + vmID} {
		canonical, err := server.normalizeOperationMachineFilter(context.Background(), reference)
		if err != nil || canonical != "local:"+vmID {
			t.Fatalf("normalize %q = %q, %v", reference, canonical, err)
		}
	}
	if len(resolver.requests) != 2 || resolver.requests[0] != vmID || resolver.requests[1] != "local:"+vmID {
		t.Fatalf("resolver requests = %v", resolver.requests)
	}
	if canonical, err := server.normalizeOperationMachineFilter(context.Background(), ""); err != nil || canonical != "" || len(resolver.requests) != 2 {
		t.Fatalf("empty filter = %q, %v requests=%v", canonical, err, resolver.requests)
	}

	for _, authorityErr := range []error{target.ErrNoDefault, target.ErrDifferentTarget, domain.ErrMachineReferenceStale} {
		resolver.err = authorityErr
		if canonical, err := server.normalizeOperationMachineFilter(context.Background(), vmID); !errors.Is(err, authorityErr) || canonical != "" {
			t.Fatalf("authority error %v returned %q, %v", authorityErr, canonical, err)
		}
	}
}
