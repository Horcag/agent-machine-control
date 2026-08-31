package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func buildWriteOperation(params SessionWriteParams, target domain.MachineRef, deadline time.Time) domain.Operation {
	dataSum := sha256.Sum256([]byte(params.Data))
	return domain.Operation{
		Kind:                "session.write",
		Target:              target,
		Actor:               params.Caller,
		Reason:              params.Reason,
		Deadline:            deadline,
		IdempotencyKey:      params.IdempotencyKey,
		RequiredCapability:  domain.CapabilitySessionWrite,
		RequiredScopes:      []string{domain.ScopeSessionWrite},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters: map[string]any{
			"session_id":  string(params.SessionID),
			"data_sha256": hex.EncodeToString(dataSum[:]),
			"data_length": len(params.Data),
		},
	}
}

func buildControlOperation(params SessionControlParams, target domain.MachineRef, deadline time.Time) domain.Operation {
	return domain.Operation{
		Kind:                "session.control",
		Target:              target,
		Actor:               params.Caller,
		Reason:              params.Reason,
		Deadline:            deadline,
		IdempotencyKey:      params.IdempotencyKey,
		RequiredCapability:  domain.CapabilitySessionControl,
		RequiredScopes:      []string{domain.ScopeSessionWrite},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters: map[string]any{
			"session_id": string(params.SessionID),
			"key":        string(params.Key),
		},
	}
}

func buildCloseOperation(params SessionCloseParams, target domain.MachineRef, deadline time.Time) domain.Operation {
	return domain.Operation{
		Kind:                "session.close",
		Target:              target,
		Actor:               params.Caller,
		Reason:              params.Reason,
		Deadline:            deadline,
		IdempotencyKey:      params.IdempotencyKey,
		RequiredCapability:  domain.CapabilitySessionClose,
		RequiredScopes:      []string{domain.ScopeSessionClose},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters: map[string]any{
			"session_id": string(params.SessionID),
		},
	}
}

// WriteSession coordinates policy, audit, and receipt for session.write.
func (s *SessionService) WriteSession(ctx context.Context, params SessionWriteParams) (int, *domain.Receipt, error) {
	if params.ApprovalID != "" && params.Deadline.IsZero() {
		return 0, nil, fmt.Errorf("%w: approval_id requires the exact issued deadline", domain.ErrMissingDeadline)
	}
	ctx, cancel, deadline, timeout := s.beginSessionMutation(ctx, params.Timeout, params.Deadline)
	defer cancel()
	if err := domain.ValidateSessionID(string(params.SessionID)); err != nil {
		return 0, nil, err
	}
	target, err := s.sessionMgr.MutationTarget(params.SessionID, params.Caller)
	if err != nil {
		return 0, nil, err
	}

	op := buildWriteOperation(params, target, deadline)

	flightKey := fmt.Sprintf("%s:%s:%s", params.Caller.EffectiveActor, target, params.IdempotencyKey)
	result, rcpt, err := s.coordinateSessionMutation(ctx, op, flightKey, params.Approval, params.ApprovalID, timeout, func(execCtx context.Context) (sessionMutationResult, error) {
		bytesWritten, err := s.sessionMgr.Write(execCtx, params.SessionID, params.Caller, params.Data, params.Reason, params.IdempotencyKey)
		return sessionMutationResult{BytesWritten: bytesWritten, EffectApplied: bytesWritten > 0}, err
	})
	return result.BytesWritten, rcpt, err
}

// ControlSession coordinates policy, audit, and receipt for session.control.
func (s *SessionService) ControlSession(ctx context.Context, params SessionControlParams) (*domain.Receipt, error) {
	if params.ApprovalID != "" && params.Deadline.IsZero() {
		return nil, fmt.Errorf("%w: approval_id requires the exact issued deadline", domain.ErrMissingDeadline)
	}
	ctx, cancel, deadline, timeout := s.beginSessionMutation(ctx, params.Timeout, params.Deadline)
	defer cancel()
	if err := domain.ValidateSessionID(string(params.SessionID)); err != nil {
		return nil, err
	}
	target, err := s.sessionMgr.MutationTarget(params.SessionID, params.Caller)
	if err != nil {
		return nil, err
	}

	op := buildControlOperation(params, target, deadline)

	flightKey := fmt.Sprintf("%s:%s:%s", params.Caller.EffectiveActor, target, params.IdempotencyKey)
	_, rcpt, err := s.coordinateSessionMutation(ctx, op, flightKey, params.Approval, params.ApprovalID, timeout, func(execCtx context.Context) (sessionMutationResult, error) {
		controlResult, err := s.sessionMgr.Control(execCtx, params.SessionID, params.Caller, params.Key, params.Reason, params.IdempotencyKey)
		return sessionMutationResult{BytesWritten: controlResult.AcceptedBytes, EffectApplied: controlResult.EffectApplied}, err
	})
	return rcpt, err
}

// CloseSession coordinates policy, audit, and receipt for session.close.
func (s *SessionService) CloseSession(ctx context.Context, params SessionCloseParams) (*domain.SessionObservation, *domain.Receipt, error) {
	if params.ApprovalID != "" && params.Deadline.IsZero() {
		return nil, nil, fmt.Errorf("%w: approval_id requires the exact issued deadline", domain.ErrMissingDeadline)
	}
	ctx, cancel, deadline, timeout := s.beginSessionMutation(ctx, params.Timeout, params.Deadline)
	defer cancel()
	if err := domain.ValidateSessionID(string(params.SessionID)); err != nil {
		return nil, nil, err
	}
	target, err := s.sessionMgr.MutationTarget(params.SessionID, params.Caller)
	if err != nil {
		return nil, nil, err
	}

	op := buildCloseOperation(params, target, deadline)

	flightKey := fmt.Sprintf("%s:%s:%s", params.Caller.EffectiveActor, target, params.IdempotencyKey)
	result, rcpt, err := s.coordinateSessionMutation(ctx, op, flightKey, params.Approval, params.ApprovalID, timeout, func(execCtx context.Context) (sessionMutationResult, error) {
		closeResult, err := s.sessionMgr.CloseWithEffect(execCtx, params.SessionID, params.Caller, params.Reason)
		closedObs := closeResult.Observation
		exitCode := 0
		if closedObs != nil && closedObs.ExitCode != nil {
			exitCode = *closedObs.ExitCode
		}
		return sessionMutationResult{
			Observation:   closedObs,
			ExitCode:      exitCode,
			EffectApplied: closeResult.EffectApplied,
		}, err
	})
	return result.Observation, rcpt, err
}
