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
	"strings"

	"github.com/Horcag/agent-machine-control/internal/domain"
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
	if s == nil || s.session == nil || s.resolver == nil || s.config == nil || s.transport == nil || s.store == nil || s.session.leaseMgr == nil {
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
		return resolution, config, ErrGuestAuthority
	}
	if config.Caller.Validate() != nil || config.Caller.AuthenticatedCaller != p.Caller.AuthenticatedCaller || config.Caller.EffectiveActor != p.Caller.EffectiveActor || !config.Caller.EffectivePermissions.IsSubsetOf(p.Caller.EffectivePermissions) || !config.Caller.CallerPermissions.IsSubsetOf(p.Caller.CallerPermissions) || domain.ValidateBoundedString(config.Generation, 1, 256, ErrGuestAuthority) != nil || !validGuestRoot(config.WorkspaceIdentity) {
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
func (s *GuestService) Execute(ctx context.Context, p GuestOperationParams, input io.Reader, output io.Writer) (GuestOperationResult, error) {
	var out GuestOperationResult
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
	if (p.Operation == "put" || p.Operation == "exec") && input == nil {
		return out, domain.ErrNonCanonicalParameter
	}
	if (p.Operation == "get" || p.Operation == "exec") && output == nil {
		return out, domain.ErrNonCanonicalParameter
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
	safety := s.session.resolveSafety(ctx, op.Target)
	_, rcpt, err := s.session.coordinateSessionMutation(ctx, op, "guest:"+guestDigest([]string{string(op.Actor.EffectiveActor), string(op.Target), p.IdempotencyKey}), nil, p.ApprovalID, timeout, func(execCtx context.Context) (sessionMutationResult, error) {
		currentResolution, current, checkErr := s.resolve(execCtx, p)
		if checkErr != nil || currentResolution.Locator != resolution.Locator || !reflect.DeepEqual(current, config) || !reflect.DeepEqual(s.session.resolveSafety(execCtx, op.Target), safety) {
			return sessionMutationResult{}, ErrGuestAuthority
		}
		if err := checkGuestScopes(op); err != nil {
			return sessionMutationResult{}, err
		}
		dispatched = true
		result, runErr := s.run(execCtx, p, op, config, key, recordID, input, output, &effectApplied)
		out = result
		if runErr == nil {
			if persistErr := s.store.write(execCtx, recordID, ".result", out); persistErr != nil {
				runErr = ErrGuestUncertain
			}
		}
		effect := effectApplied
		return sessionMutationResult{EffectApplied: effect, ExitCode: out.ExitCode, EvidenceRefs: []string{"guest-operation-dispatched"}}, runErr
	})
	out.Receipt = rcpt
	if !dispatched && err == nil && rcpt != nil {
		var cached GuestOperationResult
		if readErr := s.store.read(ctx, recordID, ".result", &cached); readErr != nil {
			return out, ErrGuestUncertain
		}
		cached.Receipt = rcpt
		cached.CachedReceipt = true
		out = cached
	}
	if err != nil && effectApplied {
		err = errors.Join(ErrGuestUncertain, err)
	}
	return out, err
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
