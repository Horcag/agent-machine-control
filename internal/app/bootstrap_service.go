package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

const (
	bootstrapTarget            = domain.MachineRef("local-host")
	bootstrapStopGraceInterval = 5 * time.Second
	// bootstrapPostEffectObservationGrace leaves headroom for a live Windows task inspection.
	bootstrapPostEffectObservationGrace = 20 * time.Second
	// bootstrapDurableEvidenceGrace reserves time to persist terminal receipt and audit evidence.
	bootstrapDurableEvidenceGrace = 5 * time.Second
	bootstrapPollInterval         = 25 * time.Millisecond
)

type bootstrapPoller func(context.Context, time.Duration, func(context.Context) (bool, error)) (bool, error)

type BootstrapService struct {
	adapter          BootstrapAdapter
	daemon           BootstrapDaemon
	auditStore       *audit.Store
	receiptStore     *receipt.Store
	now              func() time.Time
	stopGrace        time.Duration
	observationGrace time.Duration
	evidenceGrace    time.Duration
	poll             bootstrapPoller
}

type BootstrapServiceOption func(*BootstrapService)

func WithBootstrapClock(now func() time.Time) BootstrapServiceOption {
	return func(service *BootstrapService) {
		if now != nil {
			service.now = now
		}
	}
}

func NewBootstrapService(adapter BootstrapAdapter, daemon BootstrapDaemon, auditStore *audit.Store, receiptStore *receipt.Store, options ...BootstrapServiceOption) *BootstrapService {
	service := &BootstrapService{
		adapter: adapter, daemon: daemon, auditStore: auditStore, receiptStore: receiptStore,
		now: time.Now, stopGrace: bootstrapStopGraceInterval,
		observationGrace: bootstrapPostEffectObservationGrace, evidenceGrace: bootstrapDurableEvidenceGrace,
		poll: pollBootstrapCondition,
	}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *BootstrapService) Status(ctx context.Context, stateDir string) (BootstrapResult, error) {
	_, spec, err := s.resolve(ctx, stateDir)
	if err != nil {
		return BootstrapResult{}, err
	}
	return s.observe(ctx, spec)
}

func (s *BootstrapService) Ensure(ctx context.Context, req BootstrapMutationRequest) (BootstrapResult, error) {
	return s.mutate(ctx, "bootstrap.ensure", req, s.ensureEffect)
}

func (s *BootstrapService) Start(ctx context.Context, req BootstrapMutationRequest) (BootstrapResult, error) {
	return s.mutate(ctx, "bootstrap.start", req, s.startEffect)
}

func (s *BootstrapService) Stop(ctx context.Context, req BootstrapMutationRequest) (BootstrapResult, error) {
	return s.mutate(ctx, "bootstrap.stop", req, s.stopEffect)
}

func (s *BootstrapService) Remove(ctx context.Context, req BootstrapMutationRequest) (BootstrapResult, error) {
	return s.mutate(ctx, "bootstrap.remove", req, s.removeEffect)
}

type bootstrapEffectOutcome struct {
	taskStopApplied bool
}

type bootstrapEffect func(context.Context, BootstrapSpec) (bootstrapEffectOutcome, error)

func (s *BootstrapService) mutate(ctx context.Context, kind string, req BootstrapMutationRequest, effect bootstrapEffect) (BootstrapResult, error) {
	if err := s.validateMutation(req); err != nil {
		return BootstrapResult{}, err
	}
	ctx, cancel := context.WithDeadline(ctx, req.Deadline)
	defer cancel()
	identity, spec, err := s.resolve(ctx, req.StateDir)
	if err != nil {
		return BootstrapResult{}, err
	}
	op, err := bootstrapOperation(kind, req, identity, spec)
	if err != nil {
		return BootstrapResult{}, err
	}
	sd, err := statedir.Resolve(req.StateDir)
	if err != nil {
		return BootstrapResult{}, err
	}
	if err := sd.EnsureDirs(); err != nil {
		return BootstrapResult{}, err
	}
	if err := s.auditStore.CheckWritableContext(ctx); err != nil {
		return BootstrapResult{}, fmt.Errorf("bootstrap: audit store is not writable: %w", err)
	}
	if err := s.receiptStore.CheckWritableContext(ctx); err != nil {
		return BootstrapResult{}, fmt.Errorf("bootstrap: receipt store is not writable: %w", err)
	}
	if result, replayed, replayErr := s.replayPrior(ctx, op, spec); replayed {
		return result, replayErr
	}
	if err := s.auditStore.RecordAdmissionIntentContext(ctx, op); err != nil {
		return BootstrapResult{}, fmt.Errorf("bootstrap: admission audit failed: %w", err)
	}
	startedAt := s.now().UTC()
	effectOutcome, effectErr := effect(ctx, spec)
	observationCtx, cancelObservation := context.WithTimeout(context.WithoutCancel(ctx), s.observationGrace)
	result, observeErr := s.observe(observationCtx, spec)
	cancelObservation()
	result.TaskStopApplied = effectOutcome.taskStopApplied
	if effectErr == nil {
		effectErr = observeErr
	}
	result = normalizeBootstrapMutationResult(kind, result, effectErr)
	evidenceCtx, cancelEvidence := context.WithTimeout(context.WithoutCancel(ctx), s.evidenceGrace)
	defer cancelEvidence()
	receiptRecord, receiptErr := s.finalize(evidenceCtx, op, startedAt, result, effectErr)
	if receiptRecord != nil {
		result.ReceiptID = string(receiptRecord.ReceiptID)
	}
	if receiptErr != nil {
		return result, fmt.Errorf("bootstrap: effect committed but terminal evidence failed: %w", receiptErr)
	}
	return result, effectErr
}

func normalizeBootstrapMutationResult(kind string, result BootstrapResult, effectErr error) BootstrapResult {
	if kind == "bootstrap.stop" && effectErr == nil && result.Status == BootstrapAbsent {
		result.Status = BootstrapStopped
		result.Reason = BootstrapReasonStopped
	}
	return result
}

func (s *BootstrapService) replayPrior(ctx context.Context, op domain.Operation, spec BootstrapSpec) (BootstrapResult, bool, error) {
	prior, err := s.receiptStore.LookupIdempotencyContext(ctx, op)
	if err != nil {
		return BootstrapResult{}, true, err
	}
	if prior == nil {
		return BootstrapResult{}, false, nil
	}
	if err := s.auditStore.EnsureTerminalOutcomeContext(ctx, *prior); err != nil {
		return BootstrapResult{}, true, fmt.Errorf("bootstrap: reconcile terminal audit: %w", err)
	}
	result := BootstrapResult{
		SchemaVersion:   1,
		TaskPath:        spec.TaskPath,
		TaskName:        spec.TaskName,
		ReceiptID:       string(prior.ReceiptID),
		Replayed:        true,
		TaskStopApplied: bootstrapReceiptContainsEvidence(*prior, "bootstrap-task-stop-applied"),
	}
	if prior.Outcome.Status != domain.OutcomeSuccess {
		result.Status = BootstrapFailed
		result.Reason = ErrBootstrapPriorFailed.Error()
		return result, true, ErrBootstrapPriorFailed
	}
	switch prior.OperationKind {
	case domain.OperationKind("bootstrap.ensure"), domain.OperationKind("bootstrap.start"):
		result.Status, result.Reason = BootstrapHealthy, BootstrapReasonHealthy
	case domain.OperationKind("bootstrap.stop"):
		result.Status, result.Reason = BootstrapStopped, BootstrapReasonStopped
	case domain.OperationKind("bootstrap.remove"):
		result.Status, result.Reason = BootstrapAbsent, BootstrapReasonAbsent
	default:
		return result, true, fmt.Errorf("bootstrap: unsupported replay operation %q", prior.OperationKind)
	}
	return result, true, nil
}

func (s *BootstrapService) resolve(ctx context.Context, stateDir string) (BootstrapIdentity, BootstrapSpec, error) {
	identity, err := s.adapter.Identity(ctx)
	if err != nil {
		return BootstrapIdentity{}, BootstrapSpec{}, err
	}
	if err := identity.Validate(); err != nil {
		return BootstrapIdentity{}, BootstrapSpec{}, err
	}
	spec, err := s.adapter.Desired(ctx, stateDir, identity)
	if err != nil {
		return BootstrapIdentity{}, BootstrapSpec{}, err
	}
	if err := spec.Validate(identity); err != nil {
		return BootstrapIdentity{}, BootstrapSpec{}, err
	}
	return identity, spec, nil
}

func (s *BootstrapService) observe(ctx context.Context, spec BootstrapSpec) (BootstrapResult, error) {
	observation, err := s.adapter.Inspect(ctx, spec)
	if err != nil {
		return BootstrapResult{}, err
	}
	result := BootstrapResult{
		SchemaVersion: 1, Status: observation.State, Reason: observation.Reason,
		TaskRunning: observation.TaskRunning, TaskPath: spec.TaskPath, TaskName: spec.TaskName,
	}
	if observation.State == BootstrapAbsent {
		result.Reason = BootstrapReasonAbsent
		return result, nil
	}
	if observation.State == BootstrapDrift || !observation.Exact {
		result.Status = BootstrapDrift
		if result.Reason == "" {
			result.Reason = BootstrapReasonTaskMismatch
		}
		return result, ErrBootstrapDrift
	}
	healthy, healthErr := s.daemon.Healthy(ctx, spec.StateDir)
	if healthErr != nil {
		return result, healthErr
	}
	if healthy && observation.TaskRunning {
		result.Status, result.Reason = BootstrapHealthy, BootstrapReasonHealthy
		return result, nil
	}
	result.Status, result.Reason = BootstrapStopped, BootstrapReasonStopped
	return result, nil
}

func (s *BootstrapService) validateMutation(req BootstrapMutationRequest) error {
	if req.Deadline.IsZero() || !req.Deadline.After(s.now()) {
		return domain.ErrMissingDeadline
	}
	if err := domain.ValidateReason(req.Reason); err != nil {
		return err
	}
	return domain.ValidateIdempotencyKey(req.IdempotencyKey)
}

func bootstrapOperation(kind string, req BootstrapMutationRequest, identity BootstrapIdentity, spec BootstrapSpec) (domain.Operation, error) {
	actor := domain.ActorID("windows-sid:" + identity.SID)
	actorCtx, err := domain.NewActorContext(actor, actor, nil, nil)
	if err != nil {
		return domain.Operation{}, err
	}
	specDigest, err := bootstrapSpecFingerprint(spec)
	if err != nil {
		return domain.Operation{}, err
	}
	return domain.Operation{
		Kind: domain.OperationKind(kind), Target: bootstrapTarget, Actor: actorCtx,
		Reason: req.Reason, Deadline: req.Deadline, IdempotencyKey: req.IdempotencyKey,
		Classification: domain.ClassDestructivePrivileged,
		Parameters:     map[string]any{"spec_fingerprint": specDigest},
	}, nil
}

func bootstrapSpecFingerprint(spec BootstrapSpec) (string, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (s *BootstrapService) ensureEffect(ctx context.Context, spec BootstrapSpec) (bootstrapEffectOutcome, error) {
	obs, err := s.adapter.Inspect(ctx, spec)
	if err != nil {
		return bootstrapEffectOutcome{}, err
	}
	switch obs.State {
	case BootstrapAbsent:
		healthy, healthErr := s.daemon.Healthy(ctx, spec.StateDir)
		if healthErr != nil {
			return bootstrapEffectOutcome{}, healthErr
		}
		if healthy {
			return bootstrapEffectOutcome{}, fmt.Errorf("%w: daemon exists without the owned task", ErrBootstrapDrift)
		}
		if err := s.adapter.Install(ctx, spec); err != nil {
			return bootstrapEffectOutcome{}, err
		}
	case BootstrapDrift:
		return bootstrapEffectOutcome{}, ErrBootstrapDrift
	default:
		if !obs.Exact {
			return bootstrapEffectOutcome{}, ErrBootstrapDrift
		}
	}
	return s.startEffect(ctx, spec)
}

func (s *BootstrapService) startEffect(ctx context.Context, spec BootstrapSpec) (bootstrapEffectOutcome, error) {
	obs, err := s.requireExact(ctx, spec)
	if err != nil {
		return bootstrapEffectOutcome{}, err
	}
	if obs.TaskRunning {
		healthy, healthErr := s.daemon.Healthy(ctx, spec.StateDir)
		if healthErr == nil && healthy {
			return bootstrapEffectOutcome{}, nil
		}
	}
	if err := s.adapter.StartTask(ctx, spec); err != nil {
		return bootstrapEffectOutcome{}, err
	}
	return bootstrapEffectOutcome{}, s.waitHealth(ctx, spec.StateDir, true)
}

func (s *BootstrapService) removeEffect(ctx context.Context, spec BootstrapSpec) (bootstrapEffectOutcome, error) {
	outcome := bootstrapEffectOutcome{}
	obs, err := s.adapter.Inspect(ctx, spec)
	if err != nil {
		return outcome, err
	}
	if obs.State == BootstrapAbsent {
		return outcome, nil
	}
	if _, err := s.requireExact(ctx, spec); err != nil {
		return outcome, err
	}
	outcome, err = s.stopEffect(ctx, spec)
	if err != nil {
		return outcome, err
	}
	if err := s.adapter.Remove(ctx, spec); err != nil {
		return outcome, err
	}
	obs, err = s.adapter.Inspect(ctx, spec)
	if err != nil {
		return outcome, err
	}
	if obs.State != BootstrapAbsent {
		return outcome, ErrBootstrapDrift
	}
	return outcome, nil
}

func (s *BootstrapService) inspectExactOrAbsent(ctx context.Context, spec BootstrapSpec) (BootstrapObservation, error) {
	obs, err := s.adapter.Inspect(ctx, spec)
	if err != nil {
		return BootstrapObservation{}, err
	}
	if obs.State == BootstrapAbsent {
		return obs, nil
	}
	if obs.State == BootstrapDrift || !obs.Exact {
		return obs, ErrBootstrapDrift
	}
	return obs, nil
}

func (s *BootstrapService) requireExact(ctx context.Context, spec BootstrapSpec) (BootstrapObservation, error) {
	obs, err := s.adapter.Inspect(ctx, spec)
	if err != nil {
		return BootstrapObservation{}, err
	}
	if obs.State == BootstrapAbsent {
		return obs, ErrBootstrapAbsent
	}
	if obs.State == BootstrapDrift || !obs.Exact {
		return obs, ErrBootstrapDrift
	}
	return obs, nil
}

func (s *BootstrapService) waitHealth(ctx context.Context, stateDir string, want bool) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		healthy, err := s.daemon.Healthy(ctx, stateDir)
		if err != nil {
			return err
		}
		if healthy == want {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", ErrBootstrapUnhealthy, ctx.Err())
		case <-ticker.C:
		}
	}
}

func pollBootstrapCondition(ctx context.Context, timeout time.Duration, check func(context.Context) (bool, error)) (bool, error) {
	ticker := time.NewTicker(bootstrapPollInterval)
	defer ticker.Stop()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		done, err := check(ctx)
		if done || err != nil {
			return done, err
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timer.C:
			return false, nil
		case <-ticker.C:
		}
	}
}
