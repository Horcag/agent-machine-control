package hyperv

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestConsolePointerModifiersReachGuestAndReleaseAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	executor := &testMockExecutor{executeFn: func(execCtx context.Context, _ string, args, env []string) ([]byte, []byte, error) {
		calls++
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(env[0], ConsoleRequestEnvVar+"="))
		if err != nil {
			t.Fatal(err)
		}
		var request consoleRequest
		if err = json.Unmarshal(raw, &request); err != nil {
			t.Fatal(err)
		}
		if request.VMID != consoleTestID || len(request.Keys) != 2 || request.Keys[0] != 17 || request.Keys[1] != 16 || request.Input.Count != 2 {
			t.Fatalf("wrong guest request %+v", request)
		}
		if calls == 1 {
			cancel()
			return nil, nil, context.Canceled
		}
		if args[len(args)-1] != ScriptConsoleCleanup || execCtx.Err() != nil {
			t.Fatal("cleanup inherited cancellation or wrong route")
		}
		return []byte(`{"success":true}`), nil, nil
	}}
	err := New(WithExecutor(executor)).SendConsoleInput(ctx, consoleTestID, domain.ConsoleInput{Kind: "click", FrameID: "synthetic-frame", Button: "right", Count: 2, Modifiers: "ctrl+shift"})
	if !errors.Is(err, context.Canceled) || calls != 2 {
		t.Fatalf("canceled gesture err=%v calls=%d", err, calls)
	}
}

func TestConsoleMoveModifierFailureRunsIndependentCleanup(t *testing.T) {
	calls := 0
	executor := &testMockExecutor{executeFn: func(_ context.Context, _ string, args, _ []string) ([]byte, []byte, error) {
		calls++
		if calls == 1 {
			return nil, nil, errors.New("synthetic device failure")
		}
		if args[len(args)-1] != ScriptConsoleCleanup {
			t.Fatal("wrong cleanup")
		}
		return []byte(`{"success":true}`), nil, nil
	}}
	if err := New(WithExecutor(executor)).SendConsoleInput(t.Context(), consoleTestID, domain.ConsoleInput{Kind: "move", FrameID: "synthetic-frame", Modifiers: "alt"}); err == nil || calls != 2 {
		t.Fatalf("modifier cleanup err=%v calls=%d", err, calls)
	}
}
