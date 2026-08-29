package hyperv_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
)

type mockExecutor struct {
	lookPathFn func(file string) (string, error)
	executeFn  func(ctx context.Context, name string, args []string, env []string) ([]byte, []byte, error)
}

func (m *mockExecutor) LookPath(file string) (string, error) {
	if m.lookPathFn != nil {
		return m.lookPathFn(file)
	}
	return "/usr/bin/" + file, nil
}

func (m *mockExecutor) Execute(ctx context.Context, name string, args []string, env []string) ([]byte, []byte, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, name, args, env)
	}
	return nil, nil, nil
}

func TestAdapter_Doctor_ExecutableMissing(t *testing.T) {
	mock := &mockExecutor{
		lookPathFn: func(_ string) (string, error) {
			return "", errors.New("file not found")
		},
	}

	adapter := hyperv.New(hyperv.WithExecutor(mock))
	report, err := adapter.Doctor(context.Background())
	if err != nil {
		t.Fatalf("unexpected Doctor error: %v", err)
	}
	if report.Status != app.DoctorUnavailable || report.Ready {
		t.Errorf("expected DoctorUnavailable, got %v", report.Status)
	}
	if report.Reason != app.DoctorReasonExecutableMissing {
		t.Errorf("expected DoctorReasonExecutableMissing, got %v", report.Reason)
	}
}

func TestAdapter_Doctor_Ready(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
			return []byte(`{"schema_version":"1","ready":true}`), nil, nil
		},
	}

	adapter := hyperv.New(
		hyperv.WithExecutor(mock),
		hyperv.WithNowFunc(func() time.Time { return now }),
	)
	report, err := adapter.Doctor(context.Background())
	if err != nil {
		t.Fatalf("unexpected Doctor error: %v", err)
	}
	if report.Status != app.DoctorReady || !report.Ready {
		t.Errorf("expected DoctorReady, got %v", report.Status)
	}
}

func TestAdapter_Doctor_ModuleMissingViaJSON(t *testing.T) {
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
			return []byte(`{"schema_version":"1","ready":false,"error_category":"module_missing"}`), nil, nil
		},
	}

	adapter := hyperv.New(hyperv.WithExecutor(mock))
	report, err := adapter.Doctor(context.Background())
	if err != nil {
		t.Fatalf("unexpected Doctor error: %v", err)
	}
	if report.Status != app.DoctorUnavailable || report.Ready {
		t.Errorf("expected DoctorUnavailable, got %v", report.Status)
	}
	if report.Reason != app.DoctorReasonModuleMissing {
		t.Errorf("expected DoctorReasonModuleMissing, got %v", report.Reason)
	}
}

func TestAdapter_Doctor_AccessDeniedViaStructuredJSON(t *testing.T) {
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
			return []byte(`{"schema_version":"1","ready":false,"error_category":"access_denied"}`), []byte("Localized access denied message"), nil
		},
	}

	adapter := hyperv.New(hyperv.WithExecutor(mock))
	report, err := adapter.Doctor(context.Background())
	if err != nil {
		t.Fatalf("unexpected Doctor error: %v", err)
	}
	if report.Status != app.DoctorUnavailable {
		t.Errorf("expected DoctorUnavailable, got %v", report.Status)
	}
	if report.Reason != app.DoctorReasonAccessDenied {
		t.Errorf("expected DoctorReasonAccessDenied, got %v", report.Reason)
	}
}

func TestAdapter_Doctor_MalformedProviderOutput(t *testing.T) {
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
			return []byte("This is not JSON"), nil, nil
		},
	}

	adapter := hyperv.New(hyperv.WithExecutor(mock))
	report, err := adapter.Doctor(context.Background())
	if err != nil {
		t.Fatalf("unexpected Doctor error: %v", err)
	}
	if report.Status != app.DoctorUnavailable {
		t.Errorf("expected DoctorUnavailable, got %v", report.Status)
	}
	if report.Reason != app.DoctorReasonMalformedOutput {
		t.Errorf("expected DoctorReasonMalformedOutput, got %v", report.Reason)
	}
}

func TestAdapter_Doctor_OutputLimitExceeded(t *testing.T) {
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
			return nil, nil, hyperv.ErrOutputExceededLimit
		},
	}

	adapter := hyperv.New(hyperv.WithExecutor(mock))
	report, err := adapter.Doctor(context.Background())
	if err != nil {
		t.Fatalf("unexpected Doctor error: %v", err)
	}
	if report.Status != app.DoctorUnavailable || report.Reason != app.DoctorReasonMalformedOutput {
		t.Errorf("expected DoctorReasonMalformedOutput on output limit, got %+v", report)
	}
}

func TestAdapter_Doctor_NonZeroExitWithValidJSON(t *testing.T) {
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
			// Even with valid-looking JSON, non-zero exit must map to host unavailable
			return []byte(`{"schema_version":"1","ready":true}`), []byte("process failed"), errors.New("exit status 1")
		},
	}

	adapter := hyperv.New(hyperv.WithExecutor(mock))
	report, err := adapter.Doctor(context.Background())
	if err != nil {
		t.Fatalf("unexpected Doctor error: %v", err)
	}
	if report.Status != app.DoctorUnavailable || report.Ready {
		t.Errorf("expected DoctorUnavailable, got %v", report.Status)
	}
	if report.Reason != app.DoctorReasonHostUnavailable {
		t.Errorf("expected DoctorReasonHostUnavailable, got %v", report.Reason)
	}
}
