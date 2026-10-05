package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image/png"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
)

// ConsoleService shares console admission between direct, daemon, CLI and MCP.
type ConsoleService struct {
	provider      ConsoleProvider
	recovery      *RecoveryService
	target        *TargetService
	framesDir     string
	labSafety     SafetyResolver
	labEnrollment ConsoleLabEnrollmentIdentity
}

func NewConsoleService(provider ConsoleProvider, recovery *RecoveryService, target *TargetService, framesDir string, options ...ConsoleOption) *ConsoleService {
	service := &ConsoleService{provider: provider, recovery: recovery, target: target, framesDir: framesDir}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *ConsoleService) Screenshot(ctx context.Context, actor domain.ActorContext, req ConsoleScreenshotRequest) (domain.ConsoleFrame, error) {
	return s.screenshot(ctx, actor, nil, req)
}

func (s *ConsoleService) screenshot(ctx context.Context, actor domain.ActorContext, progress *recordingProgress, req ConsoleScreenshotRequest) (domain.ConsoleFrame, error) {
	resolution, req, err := s.admitScreenshot(ctx, actor, req)
	if err != nil {
		return domain.ConsoleFrame{}, err
	}
	if err := progress.beforeCapture(ctx); err != nil {
		return domain.ConsoleFrame{}, err
	}
	frame, err := s.provider.CaptureConsole(ctx, resolution.ProviderVMID, req.Width, req.Height)
	if err != nil {
		return domain.ConsoleFrame{}, errors.Join(safeConsoleProviderError(err), progress.afterCapture(false))
	}
	validationErr := validateCapturedFrame(frame, resolution.ProviderVMID, req.Width, req.Height)
	if err := errors.Join(validationErr, progress.afterCapture(validationErr == nil)); err != nil {
		return domain.ConsoleFrame{}, err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return domain.ConsoleFrame{}, err
	}
	digest := sha256.Sum256(frame.Data)
	frame.FrameID = hex.EncodeToString(id[:])
	frame.SHA256 = hex.EncodeToString(digest[:])
	frame.VMID = resolution.Locator.String()
	frame.MIMEType = "image/png"
	frame.ObservedAt = s.recovery.now()
	if err := s.saveFrame(ctx, frame); err != nil {
		return domain.ConsoleFrame{}, err
	}
	return frame, nil
}

func (s *ConsoleService) admitScreenshot(ctx context.Context, actor domain.ActorContext, req ConsoleScreenshotRequest) (TargetResolution, ConsoleScreenshotRequest, error) {
	if err := actor.Validate(); err != nil {
		return TargetResolution{}, req, err
	}
	if !actor.HasScope(domain.ScopeMachineRead) || !actor.HasScope(domain.ScopeEvidenceCapture) {
		return TargetResolution{}, req, &PolicyDeniedError{Reason: policy.DenialMissingScope, Message: "console capture requires machine:read and sensitive evidence authority"}
	}
	if req.Width == 0 && req.Height == 0 {
		req.Width, req.Height = 1024, 768
	}
	if err := domain.ValidateConsoleDimensions(req.Width, req.Height); err != nil {
		return TargetResolution{}, req, err
	}
	if err := s.validateDependencies(); err != nil {
		return TargetResolution{}, req, err
	}
	resolution, err := s.target.ResolveTarget(ctx, req.Target)
	if err != nil {
		return TargetResolution{}, req, err
	}
	caps, err := s.recovery.backend.Capabilities(ctx, resolution.ProviderVMID)
	if err != nil {
		return TargetResolution{}, req, err
	}
	if !caps.Has(domain.CapabilityConsoleScreenshot) {
		return TargetResolution{}, req, errors.New("app: console screenshot capability unavailable")
	}
	return resolution, req, nil
}

func validateCapturedFrame(frame domain.ConsoleFrame, providerID string, width, height int) error {
	if frame.VMID != providerID || frame.NativeWidth < 1 || frame.NativeHeight < 1 || frame.NativeWidth > 65535 || frame.NativeHeight > 65535 || len(frame.Data) > 5*1024*1024 {
		return errors.New("app: invalid console framebuffer identity or bounds")
	}
	config, err := png.DecodeConfig(bytes.NewReader(frame.Data))
	if err != nil || config.Width != width || config.Height != height || frame.Width != width || frame.Height != height {
		return errors.New("app: invalid console framebuffer geometry")
	}
	return nil
}

// ConsoleInputResult distinguishes valid execution evidence from admission failures.
type ConsoleInputResult struct {
	Receipt       *domain.Receipt
	CachedReceipt bool
}

// Input preserves the receipt-only API used by direct recovery and console callers.
func (s *ConsoleService) Input(ctx context.Context, actor domain.ActorContext, req ConsoleInputRequest) (domain.Receipt, error) {
	out, err := s.InputResult(ctx, actor, req)
	if out.Receipt == nil {
		return domain.Receipt{}, err
	}
	return *out.Receipt, err
}

func (s *ConsoleService) InputResult(ctx context.Context, actor domain.ActorContext, req ConsoleInputRequest) (ConsoleInputResult, error) {
	var out ConsoleInputResult
	if err := validateDesktopActor(actor, false); err != nil {
		return out, err
	}
	if strings.HasPrefix(req.ApprovalID, ConsoleLabApprovalPrefix) || (req.LabGrantID != "" && req.ApprovalID != "") {
		return out, ErrInvalidConsoleLabGrant
	}
	if err := req.Input.Validate(); err != nil {
		return out, err
	}
	deadline, err := time.Parse(time.RFC3339Nano, req.Deadline)
	if err != nil {
		return out, domain.ErrMissingDeadline
	}
	if err := s.validateDependencies(); err != nil {
		return out, err
	}
	canonical, providerID, err := s.recovery.resolveTargetReference(ctx, req.Target)
	if err != nil {
		return out, err
	}
	return s.mutateConsoleInput(ctx, actor, req, canonical, providerID, deadline)
}

func (s *ConsoleService) mutateConsoleInput(ctx context.Context, actor domain.ActorContext, req ConsoleInputRequest, canonical, providerID string, deadline time.Time) (ConsoleInputResult, error) {
	var out ConsoleInputResult
	mut := MutationRequest{TargetID: canonical, Actor: actor, Reason: req.Reason, IdempotencyKey: req.IdempotencyKey, Deadline: deadline, ApprovalID: req.ApprovalID, Timeout: 5 * time.Minute}
	dispatched := false
	mut.providerDispatched, mut.cachedReceipt = &dispatched, &out.CachedReceipt
	op, err := s.recovery.buildOperation("console.input", mut, domain.ClassDestructivePrivileged, domain.CapabilityConsoleInput, domain.ConsoleInputParameters(req.Input))
	if err != nil {
		return out, err
	}
	dispatch := func(execCtx context.Context) error {
		return s.dispatchConsoleInput(execCtx, canonical, providerID, req.Input, &dispatched)
	}

	var rcpt domain.Receipt
	if req.LabGrantID != "" {
		rcpt, err = s.ExecuteLabMutation(ctx, actor, req.LabGrantID, op, mut, providerID, dispatch)
	} else {
		if req.ApprovalID != "" {
			mut.Approval, mut.ApprovalError = s.recovery.LoadOperationApprovalReference(ctx, op, req.ApprovalID)
		}
		rcpt, err = s.recovery.executeMutation(ctx, op, mut, providerID, dispatch)
	}
	if err == nil && rcpt.Outcome.Status == domain.OutcomeFailed {
		err = errors.New("app: console input previously failed")
	}
	if err != nil && ((!dispatched && !out.CachedReceipt) || (rcpt.Outcome.Status != domain.OutcomeFailed && rcpt.Outcome.Status != domain.OutcomeAborted)) {
		out.CachedReceipt = false
		return out, err
	}
	if !validDesktopReceipt(rcpt, op) {
		out.CachedReceipt = false
		return out, errors.Join(err, errors.New("app: invalid console input receipt"))
	}
	out.Receipt = &rcpt
	return out, err
}

// dispatchConsoleInput validates frame evidence before marking actual input dispatch.
func (s *ConsoleService) dispatchConsoleInput(ctx context.Context, canonical, providerID string, input domain.ConsoleInput, dispatched *bool) error {
	input, err := s.resolveFrameInput(ctx, canonical, providerID, input)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	*dispatched = true
	if err := s.provider.SendConsoleInput(ctx, providerID, input); err != nil {
		return safeConsoleProviderError(err)
	}
	return nil
}

func (s *ConsoleService) validateDependencies() error {
	if s == nil || s.provider == nil || s.recovery == nil || s.target == nil {
		return ErrMissingBackend
	}
	return s.recovery.validateDependencies()
}

func safeConsoleProviderError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New("app: console provider failed")
}
