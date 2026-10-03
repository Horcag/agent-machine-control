package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func TestConsoleFailureReceiptDispatchAndCache(t *testing.T) {
	for _, authority := range []string{"normal", "lab"} {
		for _, cause := range []error{errors.New("synthetic-private-provider"), context.DeadlineExceeded, context.Canceled} {
			t.Run(authority+"/"+cause.Error(), func(t *testing.T) {
				testConsoleFailureReceipt(t, authority, cause)
			})
		}
	}
}

func testConsoleFailureReceipt(t *testing.T, authority string, cause error) {
	t.Helper()
	f := newConsoleFixture(t)
	input := domain.ConsoleInput{Kind: "type", Text: "synthetic-private-text"}
	actor := f.actor
	var req app.ConsoleInputRequest
	if authority == "normal" {
		req = f.approved(t, input, "synthetic-native-failure")
	} else {
		configureLab(f)
		req = labRequest(issueLab(t, f))
		actor = labActor(t)
	}
	f.provider.inputErr = cause
	first, err := f.service.InputResult(t.Context(), actor, req)
	if err == nil || first.Receipt == nil || first.Receipt.Validate() != nil || first.CachedReceipt {
		t.Fatal(first, err)
	}
	retry, err := f.service.InputResult(t.Context(), actor, req)
	if err == nil || !retry.CachedReceipt || !reflect.DeepEqual(first.Receipt, retry.Receipt) || len(f.provider.inputs) != 1 {
		t.Fatal(retry, err)
	}
	assertDesktopRedacted(t, f.root, app.DesktopActionResult{Receipt: retry.Receipt}, err)
	scopes := domain.NewScopeSet(domain.ScopeMachineRead)
	restricted, err := domain.NewActorContext(actor.AuthenticatedCaller, actor.EffectiveActor, scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := f.service.InputResult(t.Context(), restricted, req)
	if err == nil || rejected.Receipt != nil || rejected.CachedReceipt || len(f.provider.inputs) != 1 {
		t.Fatal("restricted actor replayed receipt", rejected, err)
	}
}

func TestConsoleFrameFailureDoesNotCacheOrConsumeExecution(t *testing.T) {
	for _, authority := range []string{"normal", "lab"} {
		for _, variant := range []string{"invalid", "stale", "capture timeout", "resize"} {
			t.Run(authority+"/"+variant, func(t *testing.T) {
				testConsoleFrameFailure(t, authority, variant)
			})
		}
	}
}

func testConsoleFrameFailure(t *testing.T, authority, variant string) {
	t.Helper()
	f := newConsoleFixture(t)
	frame, err := f.service.Screenshot(t.Context(), f.actor, app.ConsoleScreenshotRequest{Width: 100, Height: 50})
	if err != nil {
		t.Fatal(err)
	}
	input := domain.ConsoleInput{Kind: "drag", FrameID: frame.FrameID, Button: "left", X: 1, Y: 1, ToX: 20, ToY: 20}
	actor := f.actor
	var req app.ConsoleInputRequest
	if authority == "normal" {
		req = f.approved(t, input, "synthetic-frame-failure")
	} else {
		configureLab(f)
		req = labRequest(issueLab(t, f))
		req.Input = input
		actor = labActor(t)
	}
	original := frame
	frame = faultConsoleFrame(f, frame, variant)
	frame.Data = nil
	path := filepath.Join(f.root, "console-frames", frame.FrameID)
	data, _ := json.Marshal(frame)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		out, err := f.service.InputResult(t.Context(), actor, req)
		if err == nil || out.Receipt != nil || out.CachedReceipt || len(f.provider.inputs) != 0 {
			t.Fatal(out, err)
		}
	}
	original.Data = nil
	data, _ = json.Marshal(original)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	f.provider.captureErr, f.provider.nativeWidth = nil, 1920
	out, err := f.service.InputResult(t.Context(), actor, req)
	if err != nil || out.Receipt == nil || out.CachedReceipt || len(f.provider.inputs) != 1 {
		t.Fatal("fresh input did not execute after frame repair", out, err)
	}
}

func faultConsoleFrame(f consoleFixture, frame domain.ConsoleFrame, variant string) domain.ConsoleFrame {
	switch variant {
	case "invalid":
		frame.VMID = "local:bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
	case "stale":
		frame.ObservedAt = frame.ObservedAt.Add(-app.MaxConsoleLabGrantValidity)
	case "capture timeout":
		f.provider.captureErr = context.DeadlineExceeded
	case "resize":
		f.provider.nativeWidth = 1280
	}
	return frame
}

func TestDesktopAndConsoleCorruptCachedClassWithholdsEvidence(t *testing.T) {
	for _, kind := range []string{"desktop", "console"} {
		for _, class := range []domain.OperationClass{domain.ClassObserve, domain.ClassReversibleMutation, domain.ClassDestructivePrivileged} {
			t.Run(kind+"/"+string(class), func(t *testing.T) {
				testCorruptDesktopCache(t, kind, class)
			})
		}
	}
}

func testCorruptDesktopCache(t *testing.T, kind string, class domain.OperationClass) {
	t.Helper()
	f := newConsoleFixture(t)
	var run func() (*domain.Receipt, bool, error)
	var count func() int
	if kind == "console" {
		req := f.approved(t, domain.ConsoleInput{Kind: "key", Key: "enter"}, "synthetic-corrupt-class")
		f.provider.inputErr = context.DeadlineExceeded
		run = func() (*domain.Receipt, bool, error) {
			out, err := f.service.InputResult(t.Context(), f.actor, req)
			return out.Receipt, out.CachedReceipt, err
		}
		count = func() int { return len(f.provider.inputs) }
	} else {
		req := approveDesktop(t, f, desktopRequest(f, "clipboard.set"))
		p := desktopProvider(req)
		p.err = context.DeadlineExceeded
		service := app.NewDesktopService(p, f.service)
		run = func() (*domain.Receipt, bool, error) {
			out, err := service.Action(t.Context(), f.actor, req)
			return out.Receipt, out.CachedReceipt, err
		}
		count = func() int { return len(p.targets) }
	}
	rcpt, _, err := run()
	if rcpt == nil || err == nil {
		t.Fatal(rcpt, err)
	}
	dto := receipt.ConvertToDTO(*rcpt)
	dto.Class = class
	if class == domain.ClassDestructivePrivileged {
		dto.EvidenceRefs = nil
	}
	data, _ := json.Marshal(dto)
	if err := os.WriteFile(filepath.Join(f.root, "receipts", string(rcpt.ReceiptID)+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	rcpt, cached, err := run()
	if err == nil || rcpt != nil || cached || count() != 1 {
		t.Fatal("corrupt class replayed", rcpt, cached, err)
	}
}

func TestConsolePreAdmissionCancellationDoesNotCacheInput(t *testing.T) {
	f := newConsoleFixture(t)
	req := f.approved(t, domain.ConsoleInput{Kind: "key", Key: "enter"}, "synthetic-precancel")
	for range 2 {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		out, err := f.service.InputResult(ctx, f.actor, req)
		if err == nil || out.Receipt != nil || out.CachedReceipt || len(f.provider.inputs) != 0 {
			t.Fatal(out, err)
		}
	}
	out, err := f.service.InputResult(t.Context(), f.actor, req)
	if err != nil || out.Receipt == nil || out.CachedReceipt || len(f.provider.inputs) != 1 {
		t.Fatal(out, err)
	}
}
