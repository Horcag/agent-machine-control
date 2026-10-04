package hyperv

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

const consoleTestID = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"

func TestConsoleRejectsBeforeExecutor(t *testing.T) {
	executor := &testMockExecutor{executeFn: func(context.Context, string, []string, []string) ([]byte, []byte, error) {
		t.Fatal("unexpected provider call")
		return nil, nil, nil
	}}
	adapter := New(WithExecutor(executor))
	for _, size := range [][2]int{{-1, 2}, {0, 2}, {65536, 1}, {1024, 1025}} {
		if _, err := adapter.CaptureConsole(context.Background(), consoleTestID, size[0], size[1]); err == nil {
			t.Fatal("accepted dimensions")
		}
	}
	if _, err := adapter.CaptureConsole(context.Background(), "bad", 1, 1); err == nil {
		t.Fatal("accepted GUID")
	}
	for _, input := range []domain.ConsoleInput{{Kind: "scroll"}, {Kind: "type", Text: strings.Repeat("x", 257)}, {Kind: "key", Key: "a+ctrl"}, {Kind: "click", Button: "left"}, {Kind: "move", FrameID: "frame", X: -1}, {Kind: "drag", FrameID: "frame", Button: "left", DurationMS: 5001}, {Kind: "key", Key: "enter", DurationMS: 400}} {
		if err := adapter.SendConsoleInput(context.Background(), consoleTestID, input); err == nil {
			t.Fatal("accepted input")
		}
	}
}

func TestConsoleRGB565(t *testing.T) {
	raw := []byte{0, 248, 224, 7, 31, 0}
	data, err := encodeConsoleRGB565(raw, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	for x, want := range []color.RGBA{{R: 255, A: 255}, {G: 255, A: 255}, {B: 255, A: 255}} {
		if got := color.RGBAModel.Convert(img.At(x, 0)); got != want {
			t.Fatalf("pixel %d = %v", x, got)
		}
	}
	for _, raw := range [][]byte{nil, {0}, {0, 0, 0}} {
		if _, err := encodeConsoleRGB565(raw, 1, 1); err == nil {
			t.Fatal("accepted malformed image")
		}
	}
	if _, err := encodeConsoleRGB565(nil, 65535, 65535); err == nil {
		t.Fatal("accepted allocation")
	}
}

func TestCaptureConsoleEnvelope(t *testing.T) {
	good := consoleResponse{Success: true, VMID: consoleTestID, Width: 1, Height: 1, NativeWidth: 1920, NativeHeight: 1080, RGB565: []byte{0, 248}}
	for _, scenario := range []string{"success", "wrong_id", "wrong_size", "missing_native", "wrong_data", "failure", "unknown", "trailing", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			payload := consoleTestResponse(good, scenario)
			executor := &testMockExecutor{executeFn: func(_ context.Context, _ string, args, env []string) ([]byte, []byte, error) {
				if args[len(args)-1] != ScriptConsoleCapture {
					t.Fatal("wrong script")
				}
				if len(env) != 1 {
					t.Fatal("wrong environment")
				}
				return payload, nil, nil
			}}
			frame, err := New(WithExecutor(executor)).CaptureConsole(context.Background(), consoleTestID, 1, 1)
			if scenario != "success" {
				if err == nil {
					t.Fatalf("unredacted/absent error: %v", err)
				}
				return
			}
			if err != nil || frame.NativeWidth != 1920 || frame.MIMEType != "image/png" || len(frame.SHA256) != 64 || frame.ObservedAt.IsZero() {
				t.Fatalf("frame = %+v, %v", frame, err)
			}
		})
	}
}

func TestConsoleInputDataAndCleanup(t *testing.T) {
	private := `private ' $(bad)`
	calls := 0
	executor := &testMockExecutor{executeFn: func(_ context.Context, _ string, args, env []string) ([]byte, []byte, error) {
		calls++
		if strings.Contains(args[len(args)-1], private) {
			t.Fatal("text interpolated into script")
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(env[0], ConsoleRequestEnvVar+"="))
		if err != nil {
			t.Fatal(err)
		}
		var request consoleRequest
		if err = json.Unmarshal(raw, &request); err != nil {
			t.Fatal(err)
		}
		if request.Input.Text != private {
			t.Fatal("lost input data")
		}
		return []byte(`{"success":true,"vm_id":"` + consoleTestID + `"}`), nil, nil
	}}
	adapter := New(WithExecutor(executor))
	if err := adapter.SendConsoleInput(context.Background(), consoleTestID, domain.ConsoleInput{Kind: "type", Text: private}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("unexpected calls")
	}
	calls = 0
	executor.executeFn = func(ctx context.Context, _ string, args, _ []string) ([]byte, []byte, error) {
		calls++
		if calls == 1 {
			return nil, nil, ErrCommandTimeout
		}
		if args[len(args)-1] != ScriptConsoleCleanup {
			t.Fatal("missing cleanup")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 3*time.Second || ctx.Err() != nil {
			t.Fatal("unbounded/cancelled cleanup")
		}
		return []byte(`{"success":true}`), nil, nil
	}
	if err := adapter.SendConsoleInput(context.Background(), consoleTestID, domain.ConsoleInput{Kind: "key", Key: "ctrl+a"}); !errors.Is(err, ErrCommandTimeout) {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("missing cleanup attempt")
	}
}

func TestConsoleScriptsGuestBoundary(t *testing.T) {
	for _, script := range []string{ScriptConsoleCapture, ScriptConsoleInput, ScriptConsoleCleanup} {
		for _, forbidden := range []string{"SendInput", "SetCursorPos", "GetForegroundWindow", "VMConnect", "CopyFromScreen", "user32", "Invoke-Expression"} {
			if strings.Contains(script, forbidden) {
				t.Fatal("host API in script")
			}
		}
		for _, required := range []string{"[guid]::Parse", "Name='$id'", "Msvm_SystemDevice", "SystemName -ne $id", "AMC_CONSOLE_REQUEST"} {
			if !strings.Contains(script, required) {
				t.Fatalf("missing %s", required)
			}
		}
	}
	for _, required := range []string{"finally", "ReleaseGuestInput", "$held +=", "$buttonHeld=$true", "default { throw", "SetAbsolutePosition", "TypeText"} {
		if !strings.Contains(ScriptConsoleInput, required) {
			t.Fatalf("missing %s", required)
		}
	}
}

func consoleTestResponse(good consoleResponse, scenario string) []byte {
	result := good
	switch scenario {
	case "wrong_id":
		result.VMID = "other"
	case "wrong_size":
		result.Width = 2
	case "missing_native":
		result.NativeWidth = 0
	case "wrong_data":
		result.RGB565 = nil
	case "failure":
		result.Success = false
	}
	payload, _ := json.Marshal(result)
	switch scenario {
	case "unknown":
		payload = []byte(`{"success":true,"secret":"private"}`)
	case "trailing":
		payload = append(payload, []byte(" {}")...)
	case "malformed":
		payload = []byte("private invalid")
	}
	return payload
}

func TestConsoleProviderFailures(t *testing.T) {
	for _, failure := range []struct {
		name   string
		output []byte
		err    error
		want   error
	}{
		{"nonzero", nil, errors.New("private provider detail"), ErrHostUnavailable},
		{"output_limit", nil, ErrOutputExceededLimit, ErrOutputExceededLimit},
		{"malformed", []byte("private invalid"), nil, ErrMalformedResponse},
		{"failure", []byte(`{"success":false,"vm_id":"` + consoleTestID + `"}`), nil, ErrHostUnavailable},
	} {
		t.Run(failure.name, func(t *testing.T) {
			calls := 0
			executor := &testMockExecutor{executeFn: func(_ context.Context, _ string, args, _ []string) ([]byte, []byte, error) {
				calls++
				if calls > 1 {
					if args[len(args)-1] != ScriptConsoleCleanup {
						t.Fatal("unexpected cleanup script")
					}
					return []byte(`{"success":true}`), nil, nil
				}
				return failure.output, nil, failure.err
			}}
			adapter := New(WithExecutor(executor))
			err := adapter.SendConsoleInput(context.Background(), consoleTestID, domain.ConsoleInput{Kind: "click", FrameID: "frame", Button: "left"})
			if !errors.Is(err, failure.want) || strings.Contains(err.Error(), "private") || calls != 2 {
				t.Fatalf("result=%v calls=%d", err, calls)
			}
		})
	}
}

func TestConsoleDefaultDimensionsAndCapabilities(t *testing.T) {
	executor := &testMockExecutor{executeFn: func(_ context.Context, _ string, _ []string, env []string) ([]byte, []byte, error) {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(env[0], ConsoleRequestEnvVar+"="))
		if err != nil {
			t.Fatal(err)
		}
		var request consoleRequest
		if err = json.Unmarshal(raw, &request); err != nil {
			t.Fatal(err)
		}
		if request.VMID != consoleTestID || request.Width != 1024 || request.Height != 768 {
			t.Fatalf("request=%+v", request)
		}
		result := consoleResponse{Success: true, VMID: consoleTestID, Width: 1024, Height: 768, NativeWidth: 1024, NativeHeight: 768, RGB565: make([]byte, 1024*768*2)}
		output, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		return output, nil, nil
	}}
	adapter := New(WithExecutor(executor))
	if _, err := adapter.CaptureConsole(context.Background(), strings.ToUpper(consoleTestID), 0, 0); err != nil {
		t.Fatal(err)
	}
	caps, err := adapter.Capabilities(context.Background(), consoleTestID)
	if err != nil {
		t.Fatal(err)
	}
	if !caps.Has(domain.CapabilityConsoleInput) || !caps.Has(domain.CapabilityConsoleScreenshot) {
		t.Fatal("missing capabilities")
	}
	if domain.DirectMachineCapabilities().Has(domain.CapabilityConsoleInput) {
		t.Fatal("mutated shared capability set")
	}
	remote := New(WithExecutor(executor), WithHostRoute(HostRoute{HostID: "remote", Address: "trusted-host.example", Remote: true}))
	if _, err := remote.CaptureConsole(context.Background(), consoleTestID, 1, 1); !errors.Is(err, ErrRemoteRouteReadOnly) {
		t.Fatal(err)
	}
	if err := remote.SendConsoleInput(context.Background(), consoleTestID, domain.ConsoleInput{Kind: "type", Text: "ok"}); !errors.Is(err, ErrRemoteRouteReadOnly) {
		t.Fatal(err)
	}
}

func TestConsoleRGB565NativeTrailingMargin(t *testing.T) {
	exact := []byte{0, 248, 224, 7}
	expected, err := encodeConsoleRGB565(exact, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic native-provider fixture: four ancillary bytes beyond its pixel plane.
	padded := append(append([]byte(nil), exact...), []byte{4, 3, 2, 1}...)
	actual, err := encodeConsoleRGB565(padded, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatal("provider trailing bytes entered PNG pixels")
	}
	for _, extra := range []int{1, 2, 3, 5, 6} {
		malformed := append(append([]byte(nil), exact...), make([]byte, extra)...)
		if _, err := encodeConsoleRGB565(malformed, 2, 1); err == nil {
			t.Fatalf("accepted %d trailing bytes", extra)
		}
	}
	if !strings.Contains(ScriptConsoleCapture, "-ne ($w*$h*2+4)") {
		t.Fatal("script does not preserve bounded native payload")
	}
}
