package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/target"
)

// TargetMutationParams describes one operator-only target authority mutation.
type TargetMutationParams struct {
	Kind           domain.OperationKind
	Reference      string
	Aliases        []string
	Caller         domain.ActorContext
	Reason         string
	IdempotencyKey string
	Deadline       time.Time
	ApprovalID     string
}

// TargetMutationResult reports canonical authority and durable public effect evidence.
type TargetMutationResult struct {
	Resolution  TargetResolution
	Publication target.Publication
	Receipt     domain.Receipt
}

// TargetCoordinatorOption configures target mutation coordination.
type TargetCoordinatorOption func(*TargetCoordinator)

// WithTargetCoordinatorClock injects a deterministic clock.
func WithTargetCoordinatorClock(clock func() time.Time) TargetCoordinatorOption {
	return func(coordinator *TargetCoordinator) { coordinator.nowFn = clock }
}

// TargetCoordinator owns operator authorization, approval, reservation, effect, and evidence truth.
type TargetCoordinator struct {
	service       *TargetService
	journal       *target.MutationJournal
	auditStore    *audit.Store
	receiptStore  *receipt.Store
	approvalStore *approval.Store
	nowFn         func() time.Time
}

// NewTargetCoordinator constructs the shared target control-plane mutation service.
func NewTargetCoordinator(
	service *TargetService,
	journal *target.MutationJournal,
	auditStore *audit.Store,
	receiptStore *receipt.Store,
	approvalStore *approval.Store,
	options ...TargetCoordinatorOption,
) (*TargetCoordinator, error) {
	if service == nil || journal == nil || auditStore == nil || receiptStore == nil || approvalStore == nil {
		return nil, errors.New("app: target coordinator requires all durable dependencies")
	}
	coordinator := &TargetCoordinator{
		service: service, journal: journal, auditStore: auditStore, receiptStore: receiptStore,
		approvalStore: approvalStore, nowFn: time.Now,
	}
	for _, option := range options {
		option(coordinator)
	}
	return coordinator, nil
}

func (c *TargetCoordinator) now() time.Time {
	if c.nowFn == nil {
		return time.Now().UTC()
	}
	return c.nowFn().UTC()
}

// Show returns the enrolled canonical target after a fresh inventory refresh.
func (c *TargetCoordinator) Show(ctx context.Context) (TargetResolution, error) {
	return c.service.ShowDefaultTarget(ctx)
}

// Mutate executes or resumes one exact approval-gated target authority transition.
func (c *TargetCoordinator) Mutate(ctx context.Context, params TargetMutationParams) (TargetMutationResult, error) {
	if err := validateTargetOperator(params.Caller); err != nil {
		return TargetMutationResult{}, err
	}
	if params.ApprovalID == "" {
		return TargetMutationResult{}, target.ErrApprovalRequired
	}
	if err := domain.ValidateApprovalID(params.ApprovalID); err != nil {
		return TargetMutationResult{}, target.ErrApprovalRequired
	}

	existing, existingErr := c.journal.LookupKeyContext(ctx, params.Caller.EffectiveActor, params.IdempotencyKey)
	if existingErr != nil && !errors.Is(existingErr, target.ErrMutationNotFound) {
		return TargetMutationResult{}, existingErr
	}
	plan, err := c.prepareMutationPlan(ctx, params, existing)
	if err != nil {
		return TargetMutationResult{}, err
	}
	op, err := buildTargetOperation(plan, params.Caller, params.Reason, params.IdempotencyKey, params.Deadline)
	if err != nil {
		return TargetMutationResult{}, err
	}
	if cached, err := c.receiptStore.LookupIdempotencyContext(ctx, op); err != nil {
		return TargetMutationResult{}, err
	} else if cached != nil {
		return TargetMutationResult{Resolution: plan.Resolution, Publication: target.Publication{Committed: true, Durable: true}, Receipt: *cached}, nil
	}

	record, err := c.journal.ReserveContext(ctx, op, plan.PriorHash, plan.DesiredHash, plan.StateHash, plan.AliasCount, c.now())
	if err != nil {
		return TargetMutationResult{}, err
	}
	wasExisting := existing != nil
	issued, consumed, err := c.loadAndAuthorizeMutation(ctx, op, params.ApprovalID, wasExisting)
	if err != nil {
		if !wasExisting {
			_ = c.journal.CancelContext(context.WithoutCancel(ctx), op)
		}
		return TargetMutationResult{}, err
	}

	currentHash, err := c.currentTargetHash(ctx)
	if err != nil {
		return TargetMutationResult{}, err
	}
	effectApplied := currentHash == record.DesiredHash
	if !effectApplied && currentHash != record.PriorHash {
		return TargetMutationResult{}, target.ErrMutationDrift
	}

	publication := target.Publication{Committed: effectApplied}
	var commitErr error
	consumedHere := false
	if effectApplied {
		publication, commitErr = c.repairPublication(ctx, plan)
	} else {
		if err := c.auditStore.RecordAdmissionIntentContext(ctx, op); err != nil {
			return TargetMutationResult{}, c.cancelUnexecuted(ctx, op, issued, false, err)
		}
		if !consumed {
			if err := c.approvalStore.MarkConsumedContext(ctx, *issued, c.now()); err != nil {
				return TargetMutationResult{}, c.cancelUnexecuted(ctx, op, issued, false, err)
			}
			consumedHere = true
		}
		publication, commitErr = c.service.CommitTargetPlan(ctx, plan)
		if !publication.Committed {
			return TargetMutationResult{}, c.cancelUnexecuted(ctx, op, issued, consumedHere, commitErr)
		}
	}

	receiptValue := targetEffectReceipt(op, record, c.now())
	if record.Receipt != nil {
		receiptValue = *record.Receipt
	}
	if err := c.journal.RecordEffectContext(context.WithoutCancel(ctx), op, receiptValue, true, publication.Durable); err != nil {
		return TargetMutationResult{Resolution: plan.Resolution, Publication: publication, Receipt: receiptValue}, errors.Join(commitErr, err)
	}
	finalizationErr := c.finalizeTargetEffect(ctx, op, receiptValue)
	return TargetMutationResult{Resolution: plan.Resolution, Publication: publication, Receipt: receiptValue}, errors.Join(commitErr, finalizationErr)
}

func (c *TargetCoordinator) prepareMutationPlan(ctx context.Context, params TargetMutationParams, existing *target.MutationRecord) (TargetPlan, error) {
	var plan TargetPlan
	var err error
	switch params.Kind {
	case "target.enroll":
		plan, err = c.service.PrepareEnrollDefaultTarget(ctx, params.Reference, params.Aliases)
	case "target.clear":
		plan, err = c.service.PrepareClearDefaultTarget(ctx)
		if errors.Is(err, target.ErrNoDefault) && existing != nil {
			locator, parseErr := domain.ParseMachineLocator(string(existing.Target))
			if parseErr != nil {
				return TargetPlan{}, target.ErrMutationCollision
			}
			plan, err = c.service.prepareReservedClear(ctx, locator)
		}
	default:
		return TargetPlan{}, domain.ErrInvalidOperationKind
	}
	if err != nil {
		return TargetPlan{}, err
	}
	if existing == nil {
		return plan, nil
	}
	if existing.Kind != plan.Kind || existing.Target != domain.MachineRef(plan.Resolution.Locator.String()) ||
		existing.DesiredHash != target.StateDigest(plan.Desired) || existing.AliasCount != plan.AliasCount {
		return TargetPlan{}, target.ErrMutationCollision
	}
	plan.PriorHash = existing.PriorHash
	plan.DesiredHash = existing.DesiredHash
	plan.StateHash = existing.TransitionHash
	return plan, nil
}

func buildTargetOperation(plan TargetPlan, actor domain.ActorContext, reason, key string, deadline time.Time) (domain.Operation, error) {
	op := domain.Operation{
		Kind: plan.Kind, Target: domain.MachineRef(plan.Resolution.Locator.String()), Actor: actor,
		Reason: reason, Deadline: deadline.UTC(), IdempotencyKey: key,
		RequiredScopes: []string{domain.ScopeTargetAdmin}, Classification: domain.ClassDestructivePrivileged,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters: map[string]any{
			"transition_hash": plan.StateHash, "prior_hash": plan.PriorHash,
			"desired_hash": plan.DesiredHash, "alias_count": plan.AliasCount,
		},
	}
	if err := op.Validate(); err != nil {
		return domain.Operation{}, err
	}
	if err := domain.ValidateOperationParameters(op.Kind, op.Parameters); err != nil {
		return domain.Operation{}, err
	}
	return op, nil
}

func validateTargetOperator(caller domain.ActorContext) error {
	if err := caller.Validate(); err != nil {
		return target.ErrAccessDenied
	}
	if caller.IsDelegated() || !caller.HasScope(domain.ScopeTargetAdmin) || caller.AuthenticatedCaller == "agent:mcp-local" {
		return target.ErrAccessDenied
	}
	return nil
}

func (c *TargetCoordinator) loadAndAuthorizeMutation(ctx context.Context, op domain.Operation, approvalID string, retry bool) (*domain.Approval, bool, error) {
	issued, err := c.approvalStore.LoadIssuedContext(ctx, approvalID)
	if err != nil {
		return nil, false, target.ErrApprovalRequired
	}
	if err := c.approvalStore.ValidateIssuedContext(ctx, *issued); err != nil {
		return nil, false, target.ErrApprovalRequired
	}
	consumed, err := c.approvalStore.IsConsumedContext(ctx, approvalID)
	if err != nil {
		return nil, false, err
	}
	if consumed && !retry {
		return nil, true, target.ErrApprovalRequired
	}
	auditWritable := c.auditStore.CheckWritableContext(ctx) == nil && c.receiptStore.CheckWritableContext(ctx) == nil &&
		c.approvalStore.CheckWritableContext(ctx) == nil && c.journal.CheckWritableContext(ctx) == nil
	decision := policy.Evaluate(policy.EvaluationInput{
		Operation: op, Now: c.now(), AuditWritable: auditWritable, Approval: issued,
		AvailableCapabilities: domain.NewCapabilitySet(), SensitiveEvidenceScopes: domain.NewScopeSet(),
	})
	if decision.Type != policy.DecisionAllow {
		return nil, consumed, errors.Join(target.ErrApprovalRequired, &PolicyDeniedError{Reason: decision.DenialReason, Message: decision.DenialMessage})
	}
	return issued, consumed, nil
}

func (c *TargetCoordinator) currentTargetHash(ctx context.Context) (string, error) {
	current, err := c.service.store.Load(ctx)
	if errors.Is(err, target.ErrNoDefault) {
		return target.StateDigest(nil), nil
	}
	if err != nil {
		return "", err
	}
	return target.StateDigest(&current), nil
}

func (c *TargetCoordinator) repairPublication(ctx context.Context, plan TargetPlan) (target.Publication, error) {
	if plan.Desired == nil {
		return c.service.store.Clear(ctx)
	}
	return c.service.store.Save(ctx, plan.Desired.Clone())
}

func (c *TargetCoordinator) cancelUnexecuted(ctx context.Context, op domain.Operation, issued *domain.Approval, consumedHere bool, primary error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	journalErr := c.journal.CancelContext(cleanupCtx, op)
	var releaseErr error
	if consumedHere && issued != nil {
		releaseErr = c.approvalStore.ReleaseUnexecutedContext(cleanupCtx, *issued)
	}
	return errors.Join(primary, journalErr, releaseErr)
}

func targetEffectReceipt(op domain.Operation, record *target.MutationRecord, completedAt time.Time) domain.Receipt {
	completedAt = completedAt.UTC()
	if completedAt.Before(record.CreatedAt) {
		completedAt = record.CreatedAt
	}
	digest := sha256.Sum256([]byte("target-effect\x00" + string(record.Fingerprint) + "\x00" + string(record.IdempotencyFingerprint)))
	return domain.Receipt{
		ReceiptID: domain.ReceiptID("rcpt-" + hex.EncodeToString(digest[:16])), OperationKind: op.Kind,
		Fingerprint: record.Fingerprint, IdempotencyFingerprint: record.IdempotencyFingerprint,
		IdempotencyKey: op.IdempotencyKey, Actor: op.Actor.EffectiveActor, Target: op.Target,
		Class: domain.ClassDestructivePrivileged, EffectiveBackend: "control-plane",
		StartedAt: record.CreatedAt, CompletedAt: completedAt,
		Outcome:         domain.ExecutionOutcome{Status: domain.OutcomeSuccess, ExitCode: 0},
		ObservationType: domain.ObservationObserved, EvidenceRefs: []string{record.TransitionHash},
		RedactionStatus: domain.RedactionApplied,
	}
}

func (c *TargetCoordinator) finalizeTargetEffect(ctx context.Context, op domain.Operation, receiptValue domain.Receipt) error {
	finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := c.receiptStore.EnsureContext(finalizeCtx, receiptValue); err != nil {
		return err
	}
	if err := c.auditStore.EnsureTerminalOutcomeContext(finalizeCtx, receiptValue); err != nil {
		return err
	}
	return c.journal.MarkFinalizedContext(finalizeCtx, op, c.now())
}

func targetApprovalID(actor domain.ActorID, key string) string {
	digest := sha256.Sum256([]byte(string(actor) + "\x00" + key))
	return "app-target-" + hex.EncodeToString(digest[:16])
}

func targetApprovalCollision(existing, issued domain.Approval) bool {
	return existing.ID != issued.ID || existing.Actor != issued.Actor || existing.Target != issued.Target ||
		existing.AuthorizedClass != issued.AuthorizedClass || existing.Fingerprint != issued.Fingerprint ||
		existing.IdempotencyKey != issued.IdempotencyKey || !existing.IssuedAt.Equal(issued.IssuedAt) || !existing.ExpiresAt.Equal(issued.ExpiresAt)
}
