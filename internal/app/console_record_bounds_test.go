package app_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/png"
	"path/filepath"
	"sync"
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

type boundsRecordingProviderFake struct {
	mu        sync.Mutex
	captures  int
	targets   []string
	colors    []color.RGBA
	onCapture func(captureIndex int) error
}

func (p *boundsRecordingProviderFake) CaptureConsole(_ context.Context, id string, w, h int) (domain.ConsoleFrame, error) {
	p.mu.Lock()
	idx := p.captures
	p.captures++
	p.targets = append(p.targets, id)
	onCap := p.onCapture
	colors := p.colors
	p.mu.Unlock()

	if onCap != nil {
		if err := onCap(idx); err != nil {
			return domain.ConsoleFrame{}, err
		}
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	c := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	if idx < len(colors) {
		c = colors[idx]
	}
	draw.Draw(img, img.Bounds(), &image.Uniform{C: c}, image.Point{}, draw.Src)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return domain.ConsoleFrame{}, err
	}
	return domain.ConsoleFrame{
		VMID:         id,
		Width:        w,
		Height:       h,
		NativeWidth:  1920,
		NativeHeight: 1080,
		Data:         buf.Bytes(),
	}, nil
}

func (p *boundsRecordingProviderFake) SendConsoleInput(_ context.Context, _ string, _ domain.ConsoleInput) error {
	return nil
}

type boundsRecordingFixture struct {
	consoleFixture
	backend *mockBackend
}

func newBoundsRecordingFixture(t *testing.T, provider app.ConsoleProvider) boundsRecordingFixture {
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
	observation := domain.MachineObservation{
		HostID:              domain.LocalHostID,
		Locator:             locator,
		ID:                  vmID,
		Name:                "console-fixture",
		State:               domain.MachineStateOff,
		RawState:            "Off",
		Generation:          2,
		Version:             "10.0",
		MemoryAssignedBytes: 1024,
		Capabilities:        domain.DirectMachineCapabilities(),
		ObservedAt:          now,
		ObservationType:     domain.ObservationObserved,
	}
	backend, _ := canonicalTargetBackend(t, observation, vmID, "c4a523d4-6b99-4d62-a5e2-4752c0f20002", now)
	backend.capabilitiesFn = func(context.Context, string) (domain.CapabilitySet, error) {
		return domain.NewCapabilitySet(domain.CapabilityConsoleScreenshot, domain.CapabilityConsoleInput), nil
	}
	target := canonicalTargetService(t, state, backend)
	clock := func() time.Time { return now }
	recovery := app.NewRecoveryService(
		backend,
		lease.NewManager(state.LeasesDir()),
		audit.NewStore(state.AuditDir()),
		receipt.NewStore(state.ReceiptsDir()),
		approval.NewStore(state.ApprovalsDir()),
		app.WithRecoveryClock(clock),
		app.WithRecoveryTargetResolver(target),
	)
	scopes := domain.NewScopeSet(domain.ScopeMachineRead, domain.ScopeMachineWrite, domain.ScopeEvidenceCapture, domain.ScopeOperationAdmin)
	actor, err := domain.NewActorContext("operator:console-test", "operator:console-test", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	service := app.NewConsoleService(provider, recovery, target, filepath.Join(state.Root(), "console-frames"))
	return boundsRecordingFixture{
		consoleFixture: consoleFixture{service: service, recovery: recovery, actor: actor, root: state.Root(), now: &now},
		backend:        backend,
	}
}

func TestConsoleRecordArtifactDimensionsFrameOrderingAndSHA(t *testing.T) {
	colors := []color.RGBA{
		{R: 255, G: 0, B: 0, A: 255}, // Frame 0: Red
		{R: 0, G: 255, B: 0, A: 255}, // Frame 1: Green
		{R: 0, G: 0, B: 255, A: 255}, // Frame 2: Blue
	}
	provider := &boundsRecordingProviderFake{colors: colors}
	f := newBoundsRecordingFixture(t, provider)

	req := app.ConsoleRecordRequest{
		Target:         "default",
		Width:          16,
		Height:         8,
		Frames:         3,
		IntervalMillis: 100,
	}
	out, err := f.service.Record(t.Context(), f.actor, req)
	if err != nil {
		t.Fatalf("unexpected record failure: %v", err)
	}

	if out.MIMEType != "image/gif" {
		t.Fatalf("expected MIMEType image/gif, got %q", out.MIMEType)
	}
	if out.Width != 16 || out.Height != 8 {
		t.Fatalf("expected dimensions 16x8, got %dx%d", out.Width, out.Height)
	}
	if len(out.ObservedAt) != 3 {
		t.Fatalf("expected 3 observed timestamps, got %d", len(out.ObservedAt))
	}
	if len(out.Data) == 0 {
		t.Fatal("expected non-empty GIF artifact bytes")
	}

	// Verify SHA-256 digest matches exact data bytes
	digest := sha256.Sum256(out.Data)
	expectedSHA := hex.EncodeToString(digest[:])
	if out.SHA256 != expectedSHA {
		t.Fatalf("SHA256 mismatch: recorded=%q calculated=%q", out.SHA256, expectedSHA)
	}

	// Verify GIF decode bytes proves artifact dimensions, frame count, frame ordering, and timing
	decoded, err := gif.DecodeAll(bytes.NewReader(out.Data))
	if err != nil {
		t.Fatalf("failed to decode GIF artifact: %v", err)
	}
	if len(decoded.Image) != 3 {
		t.Fatalf("expected 3 decoded frames, got %d", len(decoded.Image))
	}
	if decoded.Config.Width != 16 || decoded.Config.Height != 8 {
		t.Fatalf("expected GIF config 16x8, got %dx%d", decoded.Config.Width, decoded.Config.Height)
	}
	if len(decoded.Delay) != 3 {
		t.Fatalf("expected 3 delays, got %d", len(decoded.Delay))
	}
	assertRecordingFrames(t, decoded)
}

func assertRecordingFrames(t *testing.T, decoded *gif.GIF) {
	t.Helper()
	for i, delay := range decoded.Delay {
		if delay < 1 {
			t.Fatalf("frame %d delay %d must be positive", i, delay)
		}
	}

	// Verify frame dimensions and color ordering: Red -> Green -> Blue
	for i, frameImg := range decoded.Image {
		bounds := frameImg.Bounds()
		if bounds.Dx() != 16 || bounds.Dy() != 8 {
			t.Fatalf("frame %d bounds %dx%d mismatch", i, bounds.Dx(), bounds.Dy())
		}
		r, g, b, _ := frameImg.At(8, 4).RGBA()
		channels := []uint32{r, g, b}
		for channel, value := range channels {
			if channel != i && channels[i] <= value {
				t.Fatalf("frame %d expected dominant channel %d, got r=%d g=%d b=%d", i, i, r, g, b)
			}
		}
	}
}

func TestConsoleRecordCancellationStopsSubsequentCaptures(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	provider := &boundsRecordingProviderFake{}
	provider.onCapture = func(captureIndex int) error {
		if captureIndex == 0 {
			// Cancel immediately after the first capture begins/completes
			cancel()
		}
		return nil
	}
	f := newBoundsRecordingFixture(t, provider)

	req := app.ConsoleRecordRequest{
		Target:         "default",
		Width:          8,
		Height:         4,
		Frames:         5,
		IntervalMillis: 100,
	}

	out, err := f.service.Record(ctx, f.actor, req)
	if err == nil {
		t.Fatal("expected Record to return error on context cancellation, got nil")
	}
	if len(out.Data) != 0 {
		t.Fatalf("expected no final artifact on cancellation, got %d bytes", len(out.Data))
	}
	provider.mu.Lock()
	totalCaptures := provider.captures
	provider.mu.Unlock()

	// Should not have captured all 5 frames because cancellation stopped subsequent captures
	if totalCaptures >= 5 {
		t.Fatalf("expected captures to stop early on cancellation, but got %d captures", totalCaptures)
	}
}

func TestConsoleRecordBoundsValidationEdges(t *testing.T) {
	provider := &boundsRecordingProviderFake{}
	f := newBoundsRecordingFixture(t, provider)

	invalidCases := []struct {
		name string
		req  app.ConsoleRecordRequest
	}{
		{"frames_too_low", app.ConsoleRecordRequest{Width: 16, Height: 8, Frames: 1, IntervalMillis: 100}},
		{"frames_too_high", app.ConsoleRecordRequest{Width: 16, Height: 8, Frames: 31, IntervalMillis: 100}},
		{"interval_too_low", app.ConsoleRecordRequest{Width: 16, Height: 8, Frames: 2, IntervalMillis: 99}},
		{"interval_too_high", app.ConsoleRecordRequest{Width: 16, Height: 8, Frames: 2, IntervalMillis: 2001}},
		{"product_too_high", app.ConsoleRecordRequest{Width: 16, Height: 8, Frames: 20, IntervalMillis: 1600}},
		{"zero_width", app.ConsoleRecordRequest{Width: 0, Height: 8, Frames: 2, IntervalMillis: 100}},
		{"zero_height", app.ConsoleRecordRequest{Width: 16, Height: 0, Frames: 2, IntervalMillis: 100}},
		{"negative_width", app.ConsoleRecordRequest{Width: -1, Height: 8, Frames: 2, IntervalMillis: 100}},
		{"negative_height", app.ConsoleRecordRequest{Width: 16, Height: -1, Frames: 2, IntervalMillis: 100}},
		{"pixels_exceeded", app.ConsoleRecordRequest{Width: 640, Height: 481, Frames: 2, IntervalMillis: 100}},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.service.Record(t.Context(), f.actor, tc.req)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}

	// Valid edge cases
	validEdges := []struct {
		name string
		req  app.ConsoleRecordRequest
	}{
		{"min_valid", app.ConsoleRecordRequest{Width: 1, Height: 1, Frames: 2, IntervalMillis: 100}},
		{"max_pixel_boundary", app.ConsoleRecordRequest{Width: 640, Height: 480, Frames: 2, IntervalMillis: 100}},
		{"valid_small_rectangle", app.ConsoleRecordRequest{Width: 32, Height: 16, Frames: 2, IntervalMillis: 100}},
	}

	for _, tc := range validEdges {
		t.Run(tc.name, func(t *testing.T) {
			out, err := f.service.Record(t.Context(), f.actor, tc.req)
			if err != nil {
				t.Fatalf("expected success for valid edge %s, got error: %v", tc.name, err)
			}
			if len(out.Data) == 0 || out.MIMEType != "image/gif" {
				t.Fatalf("invalid result for %s: %+v", tc.name, out)
			}
		})
	}
}
