package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

const bootstrapTarget = domain.MachineRef("local-host")

type BootstrapService struct {
	adapter      BootstrapAdapter
	daemon       BootstrapDaemon
	auditStore   *audit.Store
	receiptStore *receipt.Store
	now          func() time.Time
}

func NewBootstrapService(adapter BootstrapAdapter, daemon BootstrapDaemon, auditStore *audit.Store, receiptStore *receipt.Store) *BootstrapService {
	return &BootstrapService{
		adapter: adapter, daemon: daemon, auditStore: auditStore, receiptStore: receiptStore,
		now: time.Now,
	}
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

type bootstrapEffect func(context.Context, BootstrapSpec) error

func (s *BootstrapService) mutate(ctx context.Context, kind string, req BootstrapMutationRequest, effect bootstrapEffect) (BootstrapResult, error) {
	if err := validateBootstrapMutation(req); err != nil {
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
	effectErr := effect(ctx, spec)
	result, observeErr := s.observe(ctx, spec)
	if effectErr == nil {
		effectErr = observeErr
	}
	receiptRecord, receiptErr := s.finalize(ctx, op, startedAt, effectErr)
	if receiptRecord != nil {
		result.ReceiptID = string(receiptRecord.ReceiptID)
	}
	if receiptErr != nil {
		return result, fmt.Errorf("bootstrap: effect committed but terminal evidence failed: %w", receiptErr)
	}
	return result, effectErr
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
	result, observeErr := s.observe(ctx, spec)
	result.ReceiptID = string(prior.ReceiptID)
	result.Replayed = true
	if prior.Outcome.Status != domain.OutcomeSuccess {
		return result, true, ErrBootstrapPriorFailed
	}
	return result, true, observeErr
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
	result := BootstrapResult{SchemaVersion: 1, Status: observation.State, Reason: observation.Reason, TaskPath: spec.TaskPath, TaskName: spec.TaskName}
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
		return BootstrapResult{}, healthErr
	}
	if healthy && observation.TaskRunning {
		result.Status, result.Reason = BootstrapHealthy, BootstrapReasonHealthy
		return result, nil
	}
	result.Status, result.Reason = BootstrapStopped, BootstrapReasonStopped
	return result, nil
}

func validateBootstrapMutation(req BootstrapMutationRequest) error {
	if req.Deadline.IsZero() || !req.Deadline.After(time.Now()) {
		return domain.ErrMissingDeadline
	}
	if err := domain.ValidateReason(req.Reason); err != nil {
		return err
	}
	return domain.ValidateIdempotencyKey(req.IdempotencyKey)
}

func bootstrapOperation(kind string, req BootstrapMutationRequest, identity BootstrapIdentity, spec BootstrapSpec) (domain.Operation, error) {
	actorDigest := sha256.Sum256([]byte(identity.SID))
	actor := domain.ActorID("windows-user-" + hex.EncodeToString(actorDigest[:8]))
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
		Classification: domain.ClassReversibleMutation,
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

func (s *BootstrapService) finalize(ctx context.Context, op domain.Operation, startedAt time.Time, effectErr error) (*domain.Receipt, error) {
	receiptID, err := domain.GenerateReceiptID()
	if err != nil {
		return nil, err
	}
	fingerprint, err := op.Fingerprint()
	if err != nil {
		return nil, err
	}
	idempotencyFingerprint, err := domain.ComputeIdempotencyFingerprint(op)
	if err != nil {
		return nil, err
	}
	outcome := domain.OutcomeSuccess
	exitCode := 0
	rollbackRef := "bootstrap-task-owned"
	if effectErr != nil {
		outcome = domain.OutcomeFailed
		exitCode = 1
		rollbackRef = ""
	}
	record := domain.Receipt{
		ReceiptID: receiptID, OperationKind: op.Kind, Fingerprint: fingerprint,
		IdempotencyFingerprint: idempotencyFingerprint, IdempotencyKey: op.IdempotencyKey,
		Actor: op.Actor.EffectiveActor, Target: op.Target, Class: op.Classification,
		EffectiveBackend: "windows-task-scheduler", StartedAt: startedAt, CompletedAt: s.now().UTC(),
		Outcome: domain.ExecutionOutcome{Status: outcome, ExitCode: exitCode}, ObservationType: domain.ObservationObserved,
		RollbackRef: rollbackRef, RedactionStatus: domain.RedactionApplied,
	}
	if err := s.receiptStore.EnsureContext(ctx, record); err != nil {
		return &record, err
	}
	if err := s.auditStore.EnsureTerminalOutcomeContext(ctx, record); err != nil {
		return &record, err
	}
	return &record, nil
}

func (s *BootstrapService) ensureEffect(ctx context.Context, spec BootstrapSpec) error {
	obs, err := s.adapter.Inspect(ctx, spec)
	if err != nil {
		return err
	}
	switch obs.State {
	case BootstrapAbsent:
		healthy, healthErr := s.daemon.Healthy(ctx, spec.StateDir)
		if healthErr != nil {
			return healthErr
		}
		if healthy {
			return fmt.Errorf("%w: daemon exists without the owned task", ErrBootstrapDrift)
		}
		if err := s.adapter.Install(ctx, spec); err != nil {
			return err
		}
	case BootstrapDrift:
		return ErrBootstrapDrift
	default:
		if !obs.Exact {
			return ErrBootstrapDrift
		}
	}
	return s.startEffect(ctx, spec)
}

func (s *BootstrapService) startEffect(ctx context.Context, spec BootstrapSpec) error {
	obs, err := s.requireExact(ctx, spec)
	if err != nil {
		return err
	}
	if obs.TaskRunning {
		healthy, healthErr := s.daemon.Healthy(ctx, spec.StateDir)
		if healthErr == nil && healthy {
			return nil
		}
	}
	if err := s.adapter.StartTask(ctx, spec); err != nil {
		return err
	}
	return s.waitHealth(ctx, spec.StateDir, true)
}

func (s *BootstrapService) stopEffect(ctx context.Context, spec BootstrapSpec) error {
	obs, err := s.requireExact(ctx, spec)
	if err != nil {
		if errors.Is(err, ErrBootstrapAbsent) {
			return nil
		}
		return err
	}
	healthy, err := s.daemon.Healthy(ctx, spec.StateDir)
	if err != nil {
		return err
	}
	if healthy {
		_ = s.daemon.Stop(ctx, spec.StateDir)
	}
	stillHealthy, healthErr := s.daemon.Healthy(ctx, spec.StateDir)
	if healthErr != nil {
		if errors.Is(healthErr, ErrBootstrapDrift) {
			stillHealthy = true
		} else {
			return healthErr
		}
	}
	obs, err = s.requireExact(ctx, spec)
	if err != nil {
		return err
	}
	if stillHealthy || obs.TaskRunning {
		if err := s.adapter.StopTask(ctx, spec); err != nil {
			return err
		}
	}
	return s.waitHealth(ctx, spec.StateDir, false)
}

func (s *BootstrapService) removeEffect(ctx context.Context, spec BootstrapSpec) error {
	obs, err := s.adapter.Inspect(ctx, spec)
	if err != nil {
		return err
	}
	if obs.State == BootstrapAbsent {
		return nil
	}
	if _, err := s.requireExact(ctx, spec); err != nil {
		return err
	}
	if err := s.stopEffect(ctx, spec); err != nil {
		return err
	}
	if err := s.adapter.Remove(ctx, spec); err != nil {
		return err
	}
	obs, err = s.adapter.Inspect(ctx, spec)
	if err != nil {
		return err
	}
	if obs.State != BootstrapAbsent {
		return ErrBootstrapDrift
	}
	return nil
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
			if !want && errors.Is(err, ErrBootstrapDrift) {
				healthy = true
			} else {
				return err
			}
		}
		if healthy == want {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %v", ErrBootstrapUnhealthy, ctx.Err())
		case <-ticker.C:
		}
	}
}
