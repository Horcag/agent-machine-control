package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
	"github.com/Horcag/agent-machine-control/internal/sessions"
)

func guestDigest(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func guestPoolKey(target domain.MachineRef, actor domain.ActorContext, generation string) string {
	return guestDigest([]string{string(target), string(actor.AuthenticatedCaller), string(actor.EffectiveActor), generation})
}

func (s *GuestService) resolve(ctx context.Context, p GuestOperationParams) (TargetResolution, GuestConfiguration, error) {
	if !s.guestDependenciesAvailable() {
		return TargetResolution{}, GuestConfiguration{}, ErrGuestAuthority
	}
	if err := p.Caller.Validate(); err != nil {
		return TargetResolution{}, GuestConfiguration{}, err
	}
	resolution, err := s.resolver.ResolveTarget(ctx, p.Target)
	if err != nil {
		return resolution, GuestConfiguration{}, err
	}
	if err = resolution.Validate(); err != nil {
		return resolution, GuestConfiguration{}, err
	}
	config, err := s.config.Current(ctx, domain.MachineRef(resolution.Locator.String()), p.Caller)
	if err != nil {
		if errors.Is(err, ErrGuestWorkspaceUnprovisioned) {
			return resolution, config, ErrGuestWorkspaceUnprovisioned
		}
		return resolution, config, ErrGuestAuthority
	}
	if !validGuestConfiguration(config, p.Caller) {
		return resolution, config, ErrGuestAuthority
	}

	return resolution, config, ctx.Err()
}

func guestOperation(p GuestOperationParams, target domain.MachineRef, config GuestConfiguration) (domain.Operation, error) {
	digest := p.SHA256
	if p.Operation != "put" && p.Operation != "exec" {
		if digest != "" {
			return domain.Operation{}, domain.ErrNonCanonicalParameter
		}
		sum := sha256.Sum256(nil)
		digest = hex.EncodeToString(sum[:])
	}
	if p.Deadline.IsZero() {
		return domain.Operation{}, domain.ErrMissingDeadline
	}
	kind := domain.OperationKind("guest." + p.Operation)
	if !domain.IsGuestOperation(kind) {
		return domain.Operation{}, domain.ErrInvalidOperationKind
	}
	op := domain.Operation{Kind: kind, Target: target, Actor: config.Caller.Clone(), Reason: p.Reason, Deadline: p.Deadline.UTC(), IdempotencyKey: p.IdempotencyKey, RequiredCapability: domain.CapabilityGuestOperation, RequiredScopes: domain.GuestOperationScopes(kind), Classification: domain.ClassReversibleMutation, EvidenceSensitivity: domain.EvidenceSensitivityStandard, Parameters: map[string]any{"operation": p.Operation, "configuration_sha256": guestDigest(struct {
		Generation string
		Root       any
	}{config.Generation, config.WorkspaceIdentity}), "payload_sha256": digest, "size": p.Size, "max_output": p.MaxOutput, "artifact_ids": append([]string{}, p.ArtifactIDs...)}}
	if err := op.Validate(); err != nil {
		return op, err
	}
	return op, domain.ValidateOperationParameters(kind, op.Parameters)
}

// Execute admits one complete request. A retry returns durable metadata only;
// streamed output is never replayed or represented as a host-side durable file.
func (s *GuestService) Execute(ctx context.Context, p GuestOperationParams, input io.Reader, output io.Writer) (out GuestOperationResult, callErr error) {
	if err := s.beginGuestCall(); err != nil {
		return out, err
	}
	defer s.endGuestCall()
	resolution, config, err := s.resolve(ctx, p)
	if err != nil {
		return out, err
	}
	p.Target = resolution.Locator.String()
	p.Caller = config.Caller
	op, err := guestOperation(p, domain.MachineRef(p.Target), config)
	if err != nil {
		return out, err
	}
	if !op.Deadline.After(s.session.now()) {
		return out, context.DeadlineExceeded
	}
	if err = checkGuestScopes(op); err != nil {
		return out, err
	}
	ctx, cancel, _, timeout := s.session.beginSessionMutation(ctx, 0, op.Deadline)
	defer cancel()
	idFP, err := domain.ComputeIdempotencyFingerprint(op)
	if err != nil {
		return out, err
	}
	recordID := strings.TrimPrefix(string(idFP), "sha256:")
	key := guestPoolKey(op.Target, op.Actor, config.Generation)
	dispatched := false
	effectApplied := false
	input, spool, sink, err := s.prepareGuestIO(ctx, p, input, output)
	if err != nil {
		return out, err
	}
	if spool != nil {
		defer func() { callErr = errors.Join(callErr, spool.cleanup()) }()
	}
	var transportOutput io.Writer
	if spool != nil {
		transportOutput = spool
	}
	_, rcpt, err := s.session.coordinateSessionMutation(ctx, op, "guest:"+guestDigest([]string{string(op.Actor.EffectiveActor), string(op.Target), p.IdempotencyKey}), nil, p.ApprovalID, timeout, func(execCtx context.Context) (sessionMutationResult, error) {
		dispatched = true
		var runErr error
		out, effectApplied, runErr = s.dispatchGuest(execCtx, p, op, config, key, recordID, input, transportOutput)
		return sessionMutationResult{EffectApplied: effectApplied, ExitCode: out.ExitCode, EvidenceRefs: []string{"guest-operation-dispatched"}}, runErr
	})
	return s.finishGuest(ctx, p, config, out, rcpt, err, dispatched, effectApplied, recordID, spool, sink)
}

func (s *GuestService) dispatchGuest(ctx context.Context, p GuestOperationParams, op domain.Operation, config GuestConfiguration, key, id string, input io.Reader, output io.Writer) (GuestOperationResult, bool, error) {
	if err := s.validateEffectAuthority(ctx, p, config); err != nil {
		return GuestOperationResult{}, false, err
	}
	if err := checkGuestScopes(op); err != nil {
		return GuestOperationResult{}, false, err
	}
	effect := false
	out, err := s.run(ctx, p, op, config, key, id, input, output, &effect)
	if err == nil {
		if persistErr := s.store.write(ctx, id, ".result", out); persistErr != nil {
			err = ErrGuestUncertain
		}
	}
	return out, effect, err
}

func (s *GuestService) prepareGuestIO(ctx context.Context, p GuestOperationParams, input io.Reader, output io.Writer) (io.Reader, *guestSpool, stream.ContextWriter, error) {
	needsInput := p.Operation == "put" || p.Operation == "exec"
	needsOutput := p.Operation == "get" || p.Operation == "exec"
	if needsInput != (input != nil) || needsOutput != (output != nil) {
		return nil, nil, nil, domain.ErrNonCanonicalParameter
	}
	if input != nil {
		source, ok := input.(stream.ContextReader)
		if !ok {
			return nil, nil, nil, domain.ErrNonCanonicalParameter
		}
		input = &guestCheckedInput{source: source, size: p.Size, expected: p.SHA256, digest: sha256.New()}
	}
	if output == nil {
		return input, nil, nil, nil
	}
	sink, ok := output.(stream.ContextWriter)
	if !ok {
		return nil, nil, nil, domain.ErrNonCanonicalParameter
	}
	spool, err := s.spool(ctx, p)
	return input, spool, sink, err
}

func (s *GuestService) finishGuest(ctx context.Context, p GuestOperationParams, config GuestConfiguration, out GuestOperationResult, rcpt *domain.Receipt, err error, dispatched, effectApplied bool, recordID string, spool *guestSpool, sink stream.ContextWriter) (GuestOperationResult, error) {
	out.Receipt = rcpt
	out.CachedReceipt = !dispatched && rcpt != nil
	if !dispatched && err == nil && rcpt != nil {
		var cached GuestOperationResult
		if readErr := s.store.read(ctx, recordID, ".result", &cached); readErr != nil {
			return out, ErrGuestUncertain
		}
		cached.Receipt = rcpt
		cached.CachedReceipt = true
		out = cached
	}
	if err != nil && (guestOutcomeUncertain(err, rcpt, effectApplied)) {
		err = errors.Join(ErrGuestUncertain, err)
	}
	if err == nil && spool != nil && dispatched {
		err = s.publishGuest(ctx, p, config, out, rcpt, spool, sink)
	}

	return out, err
}

func (s *GuestService) publishGuest(ctx context.Context, p GuestOperationParams, config GuestConfiguration, out GuestOperationResult, rcpt *domain.Receipt, spool *guestSpool, sink stream.ContextWriter) error {
	if rcpt == nil || rcpt.Validate() != nil || rcpt.Outcome.Status != domain.OutcomeSuccess || !spool.matches(out) {
		return ErrGuestUncertain
	}
	_, fresh, err := s.resolve(ctx, p)
	if err != nil || !reflect.DeepEqual(fresh, config) {
		return ErrGuestUncertain
	}
	if err = spool.publish(ctx, sink); err != nil {
		return errors.Join(ErrGuestUncertain, err)
	}
	return nil
}

func checkGuestScopes(op domain.Operation) error {
	for _, scope := range op.RequiredScopes {
		if !op.Actor.HasScope(scope) {
			return domain.ErrSessionAccessDenied
		}
	}
	return nil
}
func guestArtifactPath(root, id string) string {
	return strings.TrimRight(root, "\\") + "\\amc-" + id + ".bin"
}
func guestArtifactStoreID(id string) string { return guestDigest("guest-artifact:" + id) }
func guestProtocolError() error             { return fmt.Errorf("%w: invalid guest result", ErrGuestOwnership) }
func (s *GuestService) guestDependenciesAvailable() bool {
	return s != nil && s.session != nil && s.resolver != nil && s.config != nil && s.transport != nil && s.store != nil && s.session.leaseMgr != nil
}
func validGuestConfiguration(config GuestConfiguration, caller domain.ActorContext) bool {
	return config.Caller.Validate() == nil && config.Caller.AuthenticatedCaller == caller.AuthenticatedCaller && config.Caller.EffectiveActor == caller.EffectiveActor && config.Caller.EffectivePermissions.IsSubsetOf(caller.EffectivePermissions) && config.Caller.CallerPermissions.IsSubsetOf(caller.CallerPermissions) && domain.ValidateBoundedString(config.Generation, 1, 256, ErrGuestAuthority) == nil && validGuestRoot(config.WorkspaceIdentity)
}

func guestOutcomeUncertain(err error, rcpt *domain.Receipt, effect bool) bool {
	return effect || errors.Is(err, sessions.ErrMutationEffectUnknown) || errors.Is(err, sessions.ErrMutationFinalizationPending) || (rcpt != nil && rcpt.Outcome.Status == domain.OutcomeFailed && slices.Contains(rcpt.EvidenceRefs, "guest-operation-dispatched"))
}
