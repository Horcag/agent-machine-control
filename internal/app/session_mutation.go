package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

// WriteSession coordinates policy, audit, and receipt for session.write.
func (s *SessionService) WriteSession(ctx context.Context, params SessionWriteParams) (int, *domain.Receipt, error) {
	ctx, cancel, deadline, timeout := s.beginSessionMutation(ctx, params.Timeout)
	defer cancel()
	if err := domain.ValidateSessionID(string(params.SessionID)); err != nil {
		return 0, nil, err
	}
	target, err := s.sessionMgr.MutationTarget(params.SessionID, params.Caller)
	if err != nil {
		return 0, nil, err
	}

	dataSum := sha256.Sum256([]byte(params.Data))
	dataSHA := hex.EncodeToString(dataSum[:])

	op := domain.Operation{
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
			"data_sha256": dataSHA,
			"data_length": len(params.Data),
		},
	}

	flightKey := fmt.Sprintf("%s:%s:%s", params.Caller.EffectiveActor, target, params.IdempotencyKey)
	n, _, rcpt, err := s.coordinateSessionMutation(ctx, op, flightKey, params.Approval, timeout, func(execCtx context.Context) (int, *domain.SessionObservation, int, error) {
		bytesWritten, err := s.sessionMgr.Write(execCtx, params.SessionID, params.Caller, params.Data, params.Reason, params.IdempotencyKey)
		return bytesWritten, nil, 0, err
	})
	return n, rcpt, err
}

// ControlSession coordinates policy, audit, and receipt for session.control.
func (s *SessionService) ControlSession(ctx context.Context, params SessionControlParams) (*domain.Receipt, error) {
	ctx, cancel, deadline, timeout := s.beginSessionMutation(ctx, params.Timeout)
	defer cancel()
	if err := domain.ValidateSessionID(string(params.SessionID)); err != nil {
		return nil, err
	}
	target, err := s.sessionMgr.MutationTarget(params.SessionID, params.Caller)
	if err != nil {
		return nil, err
	}

	op := domain.Operation{
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

	flightKey := fmt.Sprintf("%s:%s:%s", params.Caller.EffectiveActor, target, params.IdempotencyKey)
	_, _, rcpt, err := s.coordinateSessionMutation(ctx, op, flightKey, params.Approval, timeout, func(execCtx context.Context) (int, *domain.SessionObservation, int, error) {
		err := s.sessionMgr.Control(execCtx, params.SessionID, params.Caller, params.Key, params.Reason, params.IdempotencyKey)
		return 0, nil, 0, err
	})
	return rcpt, err
}

// CloseSession coordinates policy, audit, and receipt for session.close.
func (s *SessionService) CloseSession(ctx context.Context, params SessionCloseParams) (*domain.SessionObservation, *domain.Receipt, error) {
	ctx, cancel, deadline, timeout := s.beginSessionMutation(ctx, params.Timeout)
	defer cancel()
	if err := domain.ValidateSessionID(string(params.SessionID)); err != nil {
		return nil, nil, err
	}
	target, err := s.sessionMgr.MutationTarget(params.SessionID, params.Caller)
	if err != nil {
		return nil, nil, err
	}

	op := domain.Operation{
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
			"force":      params.Force,
		},
	}

	flightKey := fmt.Sprintf("%s:%s:%s", params.Caller.EffectiveActor, target, params.IdempotencyKey)
	_, obs, rcpt, err := s.coordinateSessionMutation(ctx, op, flightKey, params.Approval, timeout, func(execCtx context.Context) (int, *domain.SessionObservation, int, error) {
		closedObs, err := s.sessionMgr.Close(execCtx, params.SessionID, params.Caller, params.Reason, params.Force)
		exitCode := 0
		if closedObs != nil && closedObs.ExitCode != nil {
			exitCode = *closedObs.ExitCode
		}
		return 0, closedObs, exitCode, err
	})
	return obs, rcpt, err
}
