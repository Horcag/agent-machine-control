package daemoncli

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
)

func TestBootstrapCLIStatusJSON(t *testing.T) {
	service := &fakeBootstrapCommandService{result: app.BootstrapResult{
		SchemaVersion: 1, Status: app.BootstrapStopped, Reason: app.BootstrapReasonStopped,
	}}
	restoreBootstrapFactory(t, service)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"bootstrap", "status", "--state-dir", filepath.Join(t.TempDir(), "state"), "--json"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run() code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"status":"stopped"`) || !strings.Contains(stdout.String(), `"schema_version":1`) {
		t.Fatalf("Run() output = %s", stdout.String())
	}
}

func TestBootstrapCLIMutationRequiresAuditFields(t *testing.T) {
	restoreBootstrapFactory(t, &fakeBootstrapCommandService{})

	for _, args := range [][]string{
		{"bootstrap", "ensure", "--idempotency-key", "key"},
		{"bootstrap", "ensure", "--reason", "reason"},
		{"bootstrap", "ensure", "--reason", "reason", "--idempotency-key", "key", "--timeout", "0s"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != ExitUsage {
			t.Fatalf("Run(%v) code = %d, want usage; stderr=%s", args, code, stderr.String())
		}
	}
}

func TestBootstrapCLIInvokesEnsureWithDeadline(t *testing.T) {
	service := &fakeBootstrapCommandService{result: app.BootstrapResult{SchemaVersion: 1, Status: app.BootstrapHealthy}}
	restoreBootstrapFactory(t, service)

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"bootstrap", "ensure", "--state-dir", filepath.Join(t.TempDir(), "state"), "--reason", "operator install",
		"--idempotency-key", "ensure-cli", "--timeout", "5s", "--json",
	}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run() code = %d, stderr = %s", code, stderr.String())
	}
	if service.ensureCalls != 1 || service.lastRequest.Deadline.IsZero() {
		t.Fatalf("ensure calls=%d request=%#v", service.ensureCalls, service.lastRequest)
	}
}

func TestBootstrapCLIPriorFailureEmitsDurableFailedReplay(t *testing.T) {
	service := &fakeBootstrapCommandService{
		result: app.BootstrapResult{
			SchemaVersion: 1, Status: app.BootstrapFailed, Reason: app.ErrBootstrapPriorFailed.Error(),
			ReceiptID: "rcpt-synthetic-prior", Replayed: true,
		},
		err: app.ErrBootstrapPriorFailed,
	}
	restoreBootstrapFactory(t, service)

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"bootstrap", "start", "--state-dir", filepath.Join(t.TempDir(), "state"), "--reason", "retry historical start",
		"--idempotency-key", "prior-failed-cli", "--timeout", "5s", "--json",
	}, &stdout, &stderr)
	if code != ExitConflict {
		t.Fatalf("Run() code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"status":"failed"`) || !strings.Contains(stdout.String(), `"receipt_id":"rcpt-synthetic-prior"`) {
		t.Fatalf("Run() output = %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "prior exact attempt failed") {
		t.Fatalf("Run() stderr = %s", stderr.String())
	}
}

func TestBootstrapCLIDispatchesEveryLifecycleAction(t *testing.T) {
	service := &fakeBootstrapCommandService{result: app.BootstrapResult{SchemaVersion: 1, Status: app.BootstrapStopped}}
	req := app.BootstrapMutationRequest{Deadline: time.Now().Add(time.Minute)}
	for _, action := range []string{"ensure", "start", "stop", "remove"} {
		if _, err := invokeBootstrapMutation(context.Background(), service, action, req); err != nil {
			t.Fatalf("invokeBootstrapMutation(%q) error = %v", action, err)
		}
	}
	if service.ensureCalls != 1 || service.startCalls != 1 || service.stopCalls != 1 || service.removeCalls != 1 {
		t.Fatalf("dispatch counts = %#v", service)
	}
	if _, err := invokeBootstrapMutation(context.Background(), service, "invalid", req); err == nil {
		t.Fatal("invokeBootstrapMutation accepted invalid action")
	}
}

func TestBootstrapCLIHelpUnknownAndErrorMapping(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"bootstrap", "help"}, &stdout, &stderr); code != ExitSuccess || !strings.Contains(stdout.String(), "ensure|status|start|stop|remove") {
		t.Fatalf("help code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"bootstrap", "unknown"}, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("unknown code=%d", code)
	}

	tests := []struct {
		err     error
		code    int
		message string
	}{
		{context.DeadlineExceeded, ExitTimeout, "timed out"},
		{app.ErrBootstrapPriorFailed, ExitConflict, "prior exact attempt failed"},
		{app.ErrBootstrapDrift, ExitConflict, "drift detected"},
		{app.ErrBootstrapAbsent, ExitNotFound, "owned task is absent"},
		{app.ErrBootstrapUnsupported, ExitBackendUnavailable, "not a supported"},
		{errors.New("synthetic"), ExitBackendUnavailable, "lifecycle operation failed"},
	}
	for _, tc := range tests {
		stderr.Reset()
		if code := reportBootstrapError(&stderr, tc.err); code != tc.code {
			t.Fatalf("reportBootstrapError(%v)=%d, want %d", tc.err, code, tc.code)
		}
		if !strings.Contains(stderr.String(), tc.message) {
			t.Fatalf("reportBootstrapError(%v) message=%q, want %q", tc.err, stderr.String(), tc.message)
		}
	}
	stdout.Reset()
	emitBootstrapResult(&stdout, app.BootstrapResult{SchemaVersion: 1, Status: app.BootstrapStopped, Reason: "safe reason"}, false)
	if !strings.Contains(stdout.String(), "stopped (safe reason)") {
		t.Fatalf("human output = %s", stdout.String())
	}
}

func restoreBootstrapFactory(t *testing.T, service bootstrapCommandService) {
	t.Helper()
	original := bootstrapServiceFactory
	bootstrapServiceFactory = func(string, bool) (bootstrapCommandService, error) { return service, nil }
	t.Cleanup(func() { bootstrapServiceFactory = original })
}

type fakeBootstrapCommandService struct {
	result      app.BootstrapResult
	err         error
	ensureCalls int
	startCalls  int
	stopCalls   int
	removeCalls int
	lastRequest app.BootstrapMutationRequest
}

func (f *fakeBootstrapCommandService) Status(context.Context, string) (app.BootstrapResult, error) {
	return f.result, f.err
}

func (f *fakeBootstrapCommandService) Ensure(_ context.Context, req app.BootstrapMutationRequest) (app.BootstrapResult, error) {
	f.ensureCalls++
	f.lastRequest = req
	return f.result, f.err
}

func (f *fakeBootstrapCommandService) Start(context.Context, app.BootstrapMutationRequest) (app.BootstrapResult, error) {
	f.startCalls++
	return f.result, f.err
}

func (f *fakeBootstrapCommandService) Stop(context.Context, app.BootstrapMutationRequest) (app.BootstrapResult, error) {
	f.stopCalls++
	return f.result, f.err
}

func (f *fakeBootstrapCommandService) Remove(context.Context, app.BootstrapMutationRequest) (app.BootstrapResult, error) {
	f.removeCalls++
	return f.result, f.err
}
