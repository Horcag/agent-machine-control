package hyperv

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

type testMockExecutor struct {
	lookPathFn func(file string) (string, error)
	executeFn  func(ctx context.Context, name string, args []string, env []string) ([]byte, []byte, error)
}

func (m *testMockExecutor) LookPath(file string) (string, error) {
	if m.lookPathFn != nil {
		return m.lookPathFn(file)
	}
	return "/usr/bin/" + file, nil
}

func (m *testMockExecutor) Execute(ctx context.Context, name string, args []string, env []string) ([]byte, []byte, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, name, args, env)
	}
	return nil, nil, nil
}

func TestAdapter_OptionsAndDefaults(t *testing.T) {
	adapter := New(WithExecutablePath("/usr/local/bin/powershell.exe"))
	if adapter.exePath != "/usr/local/bin/powershell.exe" {
		t.Errorf("expected custom exe path, got %q", adapter.exePath)
	}

	exe, err := adapter.resolveExecutable()
	if err != nil || exe != "/usr/local/bin/powershell.exe" {
		t.Errorf("expected resolved custom exe path, got %q, err=%v", exe, err)
	}

	// Default now() with nil nowFn
	adapterNilNow := &Adapter{}
	now := adapterNilNow.now()
	if now.IsZero() {
		t.Errorf("expected non-zero time from default now()")
	}
}

func TestAdapter_Doctor_AdditionalBranches(t *testing.T) {
	t.Run("generic failure", func(t *testing.T) {
		mock := &testMockExecutor{
			executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
				return nil, nil, errors.New("exit status 1")
			},
		}
		adapter := New(WithExecutor(mock))
		report, err := adapter.Doctor(context.Background())
		if err != nil {
			t.Fatalf("unexpected Doctor error: %v", err)
		}
		if report.Status != app.DoctorUnavailable || report.Reason != app.DoctorReasonHostUnavailable {
			t.Errorf("expected DoctorReasonHostUnavailable, got %+v", report)
		}
	})

	t.Run("context cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		mock := &testMockExecutor{
			executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
				return nil, nil, errors.New("context cancelled")
			},
		}
		adapter := New(WithExecutor(mock))
		report, err := adapter.Doctor(ctx)
		if err != nil {
			t.Fatalf("unexpected Doctor error: %v", err)
		}
		if report.Reason != app.DoctorReasonHostUnavailable {
			t.Errorf("expected DoctorReasonHostUnavailable on timeout, got %v", report.Reason)
		}
	})
}

func TestAdapter_ExecutableNotFound_ListAndInspect(t *testing.T) {
	mock := &testMockExecutor{
		lookPathFn: func(_ string) (string, error) {
			return "", errors.New("powershell not found")
		},
	}
	adapter := New(WithExecutor(mock))

	_, err := adapter.ListMachines(context.Background())
	if !errors.Is(err, ErrExecutableNotFound) {
		t.Errorf("expected ErrExecutableNotFound for list, got %v", err)
	}

	_, err = adapter.InspectMachine(context.Background(), "c4a523d4-6b99-4d62-a5e2-4752c0f20001")
	if !errors.Is(err, ErrExecutableNotFound) {
		t.Errorf("expected ErrExecutableNotFound for inspect, got %v", err)
	}
}

func TestParser_EmptyJSON(t *testing.T) {
	var out doctorEnvelope
	err := decodeStrictJSON([]byte("   "), &out)
	if !errors.Is(err, ErrMalformedResponse) {
		t.Errorf("expected ErrMalformedResponse for empty json, got %v", err)
	}
}

func TestParser_MapDoctorReason(t *testing.T) {
	cases := []struct {
		category string
		want     app.DoctorReason
	}{
		{CategoryModuleMissing, app.DoctorReasonModuleMissing},
		{CategoryAccessDenied, app.DoctorReasonAccessDenied},
		{CategoryHostUnavailable, app.DoctorReasonHostUnavailable},
		{CategoryTimeout, app.DoctorReasonHostUnavailable},
		{"executable_missing", app.DoctorReasonExecutableMissing},
		{"unknown_category", app.DoctorReasonHostUnavailable},
	}
	for _, tc := range cases {
		if got := mapDoctorReason(tc.category); got != tc.want {
			t.Errorf("mapDoctorReason(%q) = %v, want %v", tc.category, got, tc.want)
		}
	}
}

func TestParser_SchemaVersionMismatches(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)

	docPayload := []byte(`{"schema_version":"99","ready":true}`)
	_, err := parseDoctorResponse(docPayload, now)
	if !errors.Is(err, ErrUnexpectedSchemaVersion) {
		t.Errorf("expected ErrUnexpectedSchemaVersion for doctor, got %v", err)
	}

	listPayload := []byte(`{"schema_version":"99","machines":[]}`)
	_, err = parseListResponse(listPayload, now)
	if !errors.Is(err, ErrUnexpectedSchemaVersion) {
		t.Errorf("expected ErrUnexpectedSchemaVersion for list, got %v", err)
	}

	inspectPayload := []byte(`{"schema_version":"99","machine":{"id":"c4a523d4-6b99-4d62-a5e2-4752c0f20001","name":"v1","state":"Running","generation":2,"version":"10.0"}}`)
	_, err = parseInspectResponse(inspectPayload, now)
	if !errors.Is(err, ErrUnexpectedSchemaVersion) {
		t.Errorf("expected ErrUnexpectedSchemaVersion for inspect, got %v", err)
	}
}

func TestParser_ConvertRawMachine_NegativeMemory(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	raw := rawMachine{
		ID:                  "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Name:                "vm-test",
		State:               "Running",
		Generation:          2,
		Version:             "10.0",
		MemoryAssignedBytes: -100,
	}
	_, err := convertRawMachine(raw, now)
	if !errors.Is(err, domain.ErrInvalidMetricValue) {
		t.Errorf("expected ErrInvalidMetricValue for negative memory, got %v", err)
	}
}

func TestParser_DoctorValidation(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)

	// Missing ready field
	_, err := parseDoctorResponse([]byte(`{"schema_version":"1"}`), now)
	if !errors.Is(err, ErrMalformedResponse) {
		t.Errorf("expected ErrMalformedResponse for missing ready, got %v", err)
	}

	// Ready with error category
	_, err = parseDoctorResponse([]byte(`{"schema_version":"1","ready":true,"error_category":"access_denied"}`), now)
	if !errors.Is(err, ErrMalformedResponse) {
		t.Errorf("expected ErrMalformedResponse for ready with error_category, got %v", err)
	}

	// Unavailable with unknown category
	_, err = parseDoctorResponse([]byte(`{"schema_version":"1","ready":false,"error_category":"bogus_secret_code"}`), now)
	if !errors.Is(err, ErrMalformedResponse) {
		t.Errorf("expected ErrMalformedResponse for bogus error_category, got %v", err)
	}
	if strings.Contains(err.Error(), "bogus_secret_code") {
		t.Errorf("error echoed raw invalid category: %v", err)
	}

	// Doctor rejects machine_not_found
	_, err = parseDoctorResponse([]byte(`{"schema_version":"1","ready":false,"error_category":"machine_not_found"}`), now)
	if !errors.Is(err, ErrMalformedResponse) {
		t.Errorf("expected ErrMalformedResponse for doctor with machine_not_found, got %v", err)
	}

	// Unavailable with missing error category
	_, err = parseDoctorResponse([]byte(`{"schema_version":"1","ready":false}`), now)
	if !errors.Is(err, ErrMalformedResponse) {
		t.Errorf("expected ErrMalformedResponse for missing error_category, got %v", err)
	}

	// Unknown field in JSON
	_, err = parseDoctorResponse([]byte(`{"schema_version":"1","ready":true,"extra_field":"secret"}`), now)
	if !errors.Is(err, ErrMalformedResponse) {
		t.Errorf("expected ErrMalformedResponse for unknown field, got %v", err)
	}
}

func TestParser_SanitizedSchemaVersionError(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	secretSchema := "v999_secret_value"
	_, err := parseDoctorResponse([]byte(`{"schema_version":"`+secretSchema+`","ready":true}`), now)
	if !errors.Is(err, ErrUnexpectedSchemaVersion) {
		t.Errorf("expected ErrUnexpectedSchemaVersion, got %v", err)
	}
	if strings.Contains(err.Error(), secretSchema) {
		t.Errorf("schema version error leaked raw schema string: %v", err)
	}
}

func TestParser_NormalizeIPAddresses_EdgeCases(t *testing.T) {
	ips, err := normalizeIPAddresses([]byte(`""`))
	if err != nil || len(ips) != 0 {
		t.Errorf("expected empty ips for empty string, got %v, err=%v", ips, err)
	}

	_, err = normalizeIPAddresses([]byte(`12345`))
	if !errors.Is(err, ErrMalformedResponse) {
		t.Errorf("expected ErrMalformedResponse for integer IP address, got %v", err)
	}
}
