package daemon_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

type unavailableTargetBackend struct {
	mockDaemonBackend
	err error
}

func (b *unavailableTargetBackend) InspectMachine(context.Context, string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, fmt.Errorf("private provider detail: %w", b.err)
}

func TestExactTargetProviderFailuresRetainHTTPCategories(t *testing.T) {
	for _, test := range []struct {
		name, category string
		err            error
		status         int
	}{
		{"host", "host_unavailable", hyperv.ErrHostUnavailable, http.StatusServiceUnavailable},
		{"module", "module_missing", hyperv.ErrModuleMissing, http.StatusServiceUnavailable},
		{"executable", "backend_unavailable", hyperv.ErrExecutableNotFound, http.StatusServiceUnavailable},
		{"access", "access_denied", hyperv.ErrAccessDenied, http.StatusForbidden},
		{"executor timeout", "timeout", hyperv.ErrCommandTimeout, http.StatusGatewayTimeout},
		{"deadline", "timeout", context.DeadlineExceeded, http.StatusGatewayTimeout},
		{"cancelled", "cancelled", context.Canceled, http.StatusRequestTimeout},
		{"missing machine", "machine_not_found", hyperv.ErrMachineNotFound, http.StatusNotFound},
		{"malformed provider", "malformed_response", hyperv.ErrMalformedResponse, http.StatusBadGateway},
		{"identity mismatch", "invalid_target", domain.ErrInvalidMachineLocator, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertTargetFailureEndpoints(t, test.err, test.status, test.category)
		})
	}
}

func assertTargetFailureEndpoints(t *testing.T, backendErr error, status int, category string) {
	t.Helper()
	server, operator, _ := setupOperationApprovalServer(t, &unavailableTargetBackend{err: backendErr})
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	_, err := operator.GetTarget(t.Context())
	assertSafeTargetAPIError(t, err, status, category)
	_, err = operator.CreateOperation(t.Context(), daemon.CreateOperationRequest{
		Kind: "machine.start", Target: "default", Reason: "synthetic provider failure",
		IdempotencyKey: "synthetic-target-failure", TimeoutSeconds: 30,
	})
	assertSafeTargetAPIError(t, err, status, category)
	_, err = operator.IssueOperationApproval(t.Context(), daemon.OperationApprovalIssueRequest{
		Kind: "machine.start", Target: "default", Reason: "synthetic provider failure",
		IdempotencyKey: "synthetic-target-approval", ValidForMillis: 30_000,
	})
	assertSafeTargetAPIError(t, err, status, category)
}

func assertSafeTargetAPIError(t *testing.T, err error, status int, category string) {
	t.Helper()
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("lost APIError: %v", err)
	}
	if apiErr.StatusCode != status || apiErr.Category != category || apiErr.Message == "" || strings.Contains(err.Error(), "private provider detail") {
		t.Fatalf("unsafe or lost category: %v (want %s/%d)", err, category, status)
	}
}
