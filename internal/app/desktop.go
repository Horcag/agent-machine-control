package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
)

// DesktopProvider executes only inside the enrolled guest's interactive session.
type DesktopProvider interface {
	Provision(context.Context, domain.MachineRef) (domain.DesktopResponse, error)
	Execute(context.Context, domain.MachineRef, domain.DesktopRequest) (domain.DesktopResponse, error)
	Remove(context.Context, domain.MachineRef) error
}

type DesktopActionRequest struct {
	Target         string                `json:"target,omitempty"`
	Request        domain.DesktopRequest `json:"request"`
	Reason         string                `json:"reason,omitempty"`
	IdempotencyKey string                `json:"idempotency_key,omitempty"`
	ApprovalID     string                `json:"approval_id,omitempty"`
	LabGrantID     string                `json:"lab_grant_id,omitempty"`
}

type DesktopActionResult struct {
	Response      domain.DesktopResponse `json:"response"`
	Receipt       *domain.Receipt        `json:"receipt,omitempty"`
	CachedReceipt bool                   `json:"cached_receipt"`
}

type DesktopService struct {
	provider DesktopProvider
	console  *ConsoleService
}

func NewDesktopService(provider DesktopProvider, console *ConsoleService) *DesktopService {
	return &DesktopService{provider: provider, console: console}
}

// Action routes observations and mutations through the same target and authority.
func (s *DesktopService) Action(ctx context.Context, actor domain.ActorContext, req DesktopActionRequest) (DesktopActionResult, error) {
	var out DesktopActionResult
	if s == nil || s.provider == nil || s.console == nil {
		return out, ErrMissingBackend
	}
	if err := s.console.validateDependencies(); err != nil {
		return out, err
	}
	if err := validateDesktopActor(actor, req.Request.ObserveOnly()); err != nil {
		return out, err
	}
	if err := req.Request.Validate(); err != nil {
		return out, err
	}
	if strings.HasPrefix(req.ApprovalID, ConsoleLabApprovalPrefix) || (req.ApprovalID != "" && req.LabGrantID != "") {
		return out, ErrInvalidConsoleLabGrant
	}
	deadline, _ := time.Parse(time.RFC3339Nano, req.Request.Deadline)
	if !s.console.recovery.now().Before(deadline) || deadline.Sub(s.console.recovery.now()) > time.Minute {
		return out, domain.ErrMissingDeadline
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	canonical, providerID, resolution, err := s.resolveActionTarget(ctx, req)
	if err != nil {
		return out, err
	}
	if req.Request.ObserveOnly() {
		return s.observe(ctx, actor, providerID, req.Request)
	}
	return s.mutate(ctx, actor, req, canonical, providerID, deadline, resolution)
}

// resolveActionTarget retains a validated server observation only for admission
// within this desktop lab request. Other actions keep normal reference resolution.
func (s *DesktopService) resolveActionTarget(ctx context.Context, req DesktopActionRequest) (string, string, *TargetResolution, error) {
	if req.LabGrantID == "" || req.Request.ObserveOnly() || s.console.recovery.targetResolver == nil {
		canonical, providerID, err := s.console.recovery.resolveTargetReference(ctx, req.Target)
		return canonical, providerID, nil, err
	}
	resolution, err := s.console.recovery.targetResolver.ResolveTarget(ctx, req.Target)
	if err != nil {
		return "", "", nil, err
	}
	if err := resolution.Validate(); err != nil {
		return "", "", nil, err
	}
	return resolution.Locator.String(), resolution.ProviderVMID, &resolution, nil
}

func (s *DesktopService) observe(ctx context.Context, actor domain.ActorContext, canonical string, req domain.DesktopRequest) (DesktopActionResult, error) {
	var out DesktopActionResult
	if actor.Validate() != nil || !actor.HasScope(domain.ScopeMachineRead) || !actor.HasScope(domain.ScopeEvidenceCapture) {
		return out, &PolicyDeniedError{Reason: policy.DenialMissingScope, Message: "guest desktop observation requires sensitive evidence authority"}
	}
	response, err := s.provider.Execute(ctx, domain.MachineRef(canonical), req)
	if err != nil {
		return out, safeConsoleProviderError(err)
	}
	if !response.Success || response.RequestID != req.RequestID {
		return out, errors.New("app: invalid guest desktop observation")
	}
	out.Response = response
	return out, nil
}

func (s *DesktopService) mutate(ctx context.Context, actor domain.ActorContext, req DesktopActionRequest, canonical, providerID string, deadline time.Time, resolution *TargetResolution) (DesktopActionResult, error) {
	var out DesktopActionResult
	mut := MutationRequest{TargetID: canonical, Actor: actor, Reason: req.Reason, IdempotencyKey: req.IdempotencyKey, Deadline: deadline, ApprovalID: req.ApprovalID, Timeout: time.Minute}
	mut.cachedReceipt = &out.CachedReceipt
	dispatched := false
	mut.providerDispatched = &dispatched
	op, err := s.console.recovery.buildOperation("desktop.action", mut, domain.ClassDestructivePrivileged, domain.CapabilityDesktopAction, domain.DesktopActionParameters(req.Request))
	if err != nil {
		return out, err
	}
	dispatch := func(execCtx context.Context) error {
		dispatched = true
		out.Response, err = s.dispatch(execCtx, providerID, req.Request)
		return err
	}
	var rcpt domain.Receipt
	if req.LabGrantID != "" {
		rcpt, err = s.console.executeLabMutation(ctx, actor, req.LabGrantID, op, mut, providerID, dispatch, resolution)
	} else {
		if req.ApprovalID != "" {
			mut.Approval, mut.ApprovalError = s.console.recovery.LoadOperationApprovalReference(ctx, op, req.ApprovalID)
		}
		rcpt, err = s.console.recovery.executeMutation(ctx, op, mut, providerID, dispatch)
	}
	if err == nil && rcpt.Outcome.Status == domain.OutcomeFailed {
		err = errors.New("app: guest desktop action previously failed")
	}
	if err != nil {
		out.Response = domain.DesktopResponse{}
		// Admission failures are not evidence of an admitted guest action.
		if (!dispatched && !out.CachedReceipt) || (rcpt.Outcome.Status != domain.OutcomeFailed && rcpt.Outcome.Status != domain.OutcomeAborted) {
			out.CachedReceipt = false
			return out, err
		}
	}
	if !validDesktopReceipt(rcpt, op) {
		out.CachedReceipt = false
		return out, errors.Join(err, errors.New("app: invalid desktop receipt"))
	}
	out.Receipt = &rcpt
	return out, err
}

func (s *DesktopService) dispatch(ctx context.Context, canonical string, req domain.DesktopRequest) (domain.DesktopResponse, error) {
	var response domain.DesktopResponse
	var err error
	switch req.Action {
	case "provision":
		response, err = s.provider.Provision(ctx, domain.MachineRef(canonical))
		response.RequestID = req.RequestID
	case "remove":
		err = s.provider.Remove(ctx, domain.MachineRef(canonical))
		response = domain.DesktopResponse{RequestID: req.RequestID, Success: err == nil}
	default:
		response, err = s.provider.Execute(ctx, domain.MachineRef(canonical), req)
	}
	if err != nil {
		if errors.Is(err, domain.ErrClipboardUncertain) {
			return domain.DesktopResponse{}, domain.ErrClipboardUncertain
		}
		return domain.DesktopResponse{}, safeConsoleProviderError(err)
	}
	if !response.Success || response.RequestID != req.RequestID {
		return domain.DesktopResponse{}, errors.New("app: guest desktop action failed")
	}
	return response, nil
}

// DesktopBackend adds a composed guest executor capability without changing the hypervisor.
type DesktopBackend struct{ Backend }

func (b DesktopBackend) Capabilities(ctx context.Context, target string) (domain.CapabilitySet, error) {
	caps, err := b.Backend.Capabilities(ctx, target)
	if err != nil {
		return nil, err
	}
	caps = caps.Clone()
	if caps.Has(domain.CapabilityConsoleInput) {
		caps[domain.CapabilityDesktopAction] = struct{}{}
	}
	return caps, nil
}

func validateDesktopActor(actor domain.ActorContext, observe bool) error {
	if actor.Validate() != nil {
		return &PolicyDeniedError{Reason: policy.DenialMissingScope, Message: "guest desktop requires caller authority"}
	}
	if observe && (!actor.HasScope(domain.ScopeMachineRead) || !actor.HasScope(domain.ScopeEvidenceCapture)) {
		return &PolicyDeniedError{Reason: policy.DenialMissingScope, Message: "guest desktop observation requires sensitive evidence authority"}
	}
	if !observe && !actor.HasScope(domain.ScopeMachineWrite) {
		return &PolicyDeniedError{Reason: policy.DenialMissingScope, Message: "guest desktop mutation requires write authority"}
	}
	return nil
}

func validDesktopReceipt(rcpt domain.Receipt, op domain.Operation) bool {
	idFingerprint, err := domain.ComputeIdempotencyFingerprint(op)
	fingerprint, _ := op.Fingerprint()
	identityMatches := rcpt.IdempotencyFingerprint == idFingerprint || (rcpt.IdempotencyFingerprint == "" && rcpt.Fingerprint == fingerprint)
	return err == nil && identityMatches && rcpt.Validate() == nil && rcpt.Class == op.Classification && (rcpt.Outcome.Status != domain.OutcomeAborted || slices.Contains(rcpt.EvidenceRefs, domain.DesktopDispatchEvidence)) && rcpt.RedactionStatus == domain.RedactionApplied && rcpt.Actor == op.Actor.EffectiveActor && rcpt.Target == op.Target && rcpt.OperationKind == op.Kind && rcpt.IdempotencyKey == op.IdempotencyKey
}
