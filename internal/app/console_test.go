package app_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

type consoleProviderFake struct {
	captures    int
	inputs      []domain.ConsoleInput
	nativeWidth int
	badPNG      bool
	failInput   bool
}

func (p *consoleProviderFake) CaptureConsole(_ context.Context, id string, w, h int) (domain.ConsoleFrame, error) {
	p.captures++
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		return domain.ConsoleFrame{}, err
	}
	data := buf.Bytes()
	if p.badPNG {
		data = []byte("bad")
	}
	return domain.ConsoleFrame{VMID: id, Width: w, Height: h, NativeWidth: p.nativeWidth, NativeHeight: 1080, Data: data}, nil
}
func (p *consoleProviderFake) SendConsoleInput(_ context.Context, _ string, input domain.ConsoleInput) error {
	p.inputs = append(p.inputs, input)
	if p.failInput {
		return errors.New("provider accidentally echoed secret-password")
	}
	return nil
}

type consoleFixture struct {
	service  *app.ConsoleService
	recovery *app.RecoveryService
	provider *consoleProviderFake
	actor    domain.ActorContext
	root     string
	now      *time.Time
}

func newConsoleFixture(t *testing.T) consoleFixture {
	t.Helper()
	state, err := statedir.Resolve(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	const vmID = "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	now := time.Now().UTC()
	locator, _ := domain.NewMachineLocator(domain.LocalHostID, vmID)
	observation := domain.MachineObservation{HostID: domain.LocalHostID, Locator: locator, ID: vmID, Name: "console-fixture", State: domain.MachineStateOff, RawState: "Off", Generation: 2, Version: "10.0", MemoryAssignedBytes: 1024, Capabilities: domain.DirectMachineCapabilities(), ObservedAt: now, ObservationType: domain.ObservationObserved}
	backend, _ := canonicalTargetBackend(t, observation, vmID, "c4a523d4-6b99-4d62-a5e2-4752c0f20002", now)
	backend.capabilitiesFn = func(context.Context, string) (domain.CapabilitySet, error) {
		return domain.NewCapabilitySet(domain.CapabilityConsoleScreenshot, domain.CapabilityConsoleInput), nil
	}
	target := canonicalTargetService(t, state, backend)
	clock := func() time.Time { return now }
	recovery := app.NewRecoveryService(app.DesktopBackend{Backend: backend}, lease.NewManager(state.LeasesDir()), audit.NewStore(state.AuditDir()), receipt.NewStore(state.ReceiptsDir()), approval.NewStore(state.ApprovalsDir()), app.WithRecoveryClock(clock), app.WithRecoveryTargetResolver(target))
	scopes := domain.NewScopeSet(domain.ScopeMachineRead, domain.ScopeMachineWrite, domain.ScopeEvidenceCapture, domain.ScopeOperationAdmin)
	actor, err := domain.NewActorContext("operator:console-test", "operator:console-test", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	provider := &consoleProviderFake{nativeWidth: 1920}
	service := app.NewConsoleService(provider, recovery, target, filepath.Join(state.Root(), "console-frames"))
	return consoleFixture{service, recovery, provider, actor, state.Root(), &now}
}
func (f consoleFixture) approved(t *testing.T, input domain.ConsoleInput, key string) app.ConsoleInputRequest {
	t.Helper()
	grant, _, err := f.recovery.IssueOperationApproval(context.Background(), app.OperationApprovalIssueParams{Kind: "console.input", Caller: f.actor, Target: "default", Reason: "test native guest console input", IdempotencyKey: key, ValidFor: time.Minute, Parameters: domain.ConsoleInputParameters(input)})
	if err != nil {
		t.Fatal(err)
	}
	return app.ConsoleInputRequest{Target: "default", Input: input, Reason: grant.Operation.Reason, IdempotencyKey: key, Deadline: grant.Deadline.Format(time.RFC3339Nano), ApprovalID: grant.ApprovalID}
}
func TestConsoleScreenshotScopeAndTargetFailBeforeCapture(t *testing.T) {
	f := newConsoleFixture(t)
	scopes := domain.NewScopeSet(domain.ScopeMachineRead)
	actor, err := domain.NewActorContext("agent:test", "agent:test", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Screenshot(context.Background(), actor, app.ConsoleScreenshotRequest{}); err == nil {
		t.Fatal("capture allowed without sensitive scope")
	}
	if _, err := f.service.Screenshot(context.Background(), f.actor, app.ConsoleScreenshotRequest{Target: "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"}); err == nil {
		t.Fatal("foreign target allowed")
	}
	if f.provider.captures != 0 {
		t.Fatal("provider called before admission")
	}
}
func TestConsoleScreenshotMetadataNeverStoresPixels(t *testing.T) {
	f := newConsoleFixture(t)
	frame, err := f.service.Screenshot(context.Background(), f.actor, app.ConsoleScreenshotRequest{Width: 100, Height: 50})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(f.root, "console-frames", frame.FrameID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "\"data\"") || len(frame.Data) == 0 || frame.MIMEType != "image/png" {
		t.Fatal("frame storage/image contract violated")
	}
	f.provider.badPNG = true
	if _, err := f.service.Screenshot(context.Background(), f.actor, app.ConsoleScreenshotRequest{Width: 100, Height: 50}); err == nil {
		t.Fatal("malformed PNG accepted")
	}
}
func TestConsoleInputMappingAndIdempotentRetry(t *testing.T) {
	f := newConsoleFixture(t)
	frame, err := f.service.Screenshot(context.Background(), f.actor, app.ConsoleScreenshotRequest{Width: 100, Height: 50})
	if err != nil {
		t.Fatal(err)
	}
	req := f.approved(t, domain.ConsoleInput{Kind: "click", FrameID: frame.FrameID, X: 50, Y: 25, Button: "left"}, "console-mapping")
	first, err := f.service.Input(context.Background(), f.actor, req)
	if err != nil {
		t.Fatal(err)
	}
	*f.now = f.now.Add(3 * time.Minute)
	second, err := f.service.Input(context.Background(), f.actor, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.provider.inputs) != 1 || f.provider.inputs[0].X != 960 || f.provider.inputs[0].Y != 540 || first.ReceiptID != second.ReceiptID {
		t.Fatalf("mapping/retry: %+v", f.provider.inputs)
	}
}
func TestConsoleInputRequiresApprovalAndRedactsTypedData(t *testing.T) {
	f := newConsoleFixture(t)
	input := domain.ConsoleInput{Kind: "type", Text: "secret-password"}
	denied := app.ConsoleInputRequest{Target: "default", Input: input, Reason: "verify approval denied", IdempotencyKey: "console-denied", Deadline: f.now.Add(time.Minute).Format(time.RFC3339Nano)}
	if _, err := f.service.Input(context.Background(), f.actor, denied); err == nil || len(f.provider.inputs) != 0 {
		t.Fatal("unapproved input executed")
	}
	req := f.approved(t, input, "console-secret")
	f.provider.failInput = true
	if _, err := f.service.Input(context.Background(), f.actor, req); err == nil || strings.Contains(err.Error(), input.Text) {
		t.Fatal("provider failure discarded or exposed secret")
	}
	if err := filepath.WalkDir(f.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		root, err := os.OpenRoot(f.root)
		if err != nil {
			return err
		}
		defer root.Close()
		relative, err := filepath.Rel(f.root, path)
		if err != nil {
			return err
		}
		data, err := root.ReadFile(relative)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(input.Text)) {
			return errors.New("typed secret retained in state")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestConsoleInputRejectsStaleAndChangedDisplay(t *testing.T) {
	for _, variant := range []string{"stale", "resize", "bounds"} {
		t.Run(variant, func(t *testing.T) {
			f := newConsoleFixture(t)
			frame, err := f.service.Screenshot(context.Background(), f.actor, app.ConsoleScreenshotRequest{Width: 100, Height: 50})
			if err != nil {
				t.Fatal(err)
			}
			input := domain.ConsoleInput{Kind: "move", FrameID: frame.FrameID, X: 50, Y: 25}
			if variant == "bounds" {
				input.X = 100
			}
			if variant == "stale" {
				*f.now = f.now.Add(3 * time.Minute)
			}
			req := f.approved(t, input, "console-frame-"+variant)
			if variant == "resize" {
				f.provider.nativeWidth = 1280
			}
			if _, err := f.service.Input(context.Background(), f.actor, req); err == nil || len(f.provider.inputs) != 0 {
				t.Fatal("unsafe pointer input executed")
			}
		})
	}
}

func TestConsoleInputApprovalCannotAuthorizeChangedPayload(t *testing.T) {
	f := newConsoleFixture(t)
	req := f.approved(t, domain.ConsoleInput{Kind: "type", Text: "original"}, "console-mismatch")
	req.Input.Text = "changed"
	if _, err := f.service.Input(context.Background(), f.actor, req); err == nil || len(f.provider.inputs) != 0 {
		t.Fatal("changed payload used exact approval")
	}
}
