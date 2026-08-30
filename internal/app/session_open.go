package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/sessions"
)

func buildOpenOperation(params SessionOpenParams, deadline time.Time) (domain.Operation, uint16, uint16, string) {
	cols := params.Cols
	if cols == 0 {
		cols = domain.DefaultCols
	}
	rows := params.Rows
	if rows == 0 {
		rows = domain.DefaultRows
	}
	term := params.Term
	if term == "" {
		term = domain.DefaultTermType
	}

	opParams := map[string]any{
		"cols": cols,
		"rows": rows,
		"term": term,
	}

	op := domain.Operation{
		Kind:                "session.open",
		Target:              domain.MachineRef(params.Target),
		Actor:               params.Caller,
		Reason:              params.Reason,
		Deadline:            deadline,
		IdempotencyKey:      params.IdempotencyKey,
		RequiredCapability:  domain.CapabilitySessionOpen,
		RequiredScopes:      []string{domain.ScopeSessionOpen},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters:          opParams,
	}
	return op, cols, rows, term
}

// OpenSession coordinates policy, audit, and receipt for session.open.
func (s *SessionService) OpenSession(ctx context.Context, params SessionOpenParams) (*domain.SessionObservation, *domain.Receipt, error) {
	ctx, cancel, deadline, timeout := s.beginSessionMutation(ctx, params.Timeout)
	defer cancel()
	op, cols, rows, term := buildOpenOperation(params, deadline)
	flightKey := fmt.Sprintf("%s:%s:%s", params.Caller.EffectiveActor, params.Target, params.IdempotencyKey)

	result, rcpt, err := s.coordinateSessionMutation(ctx, op, flightKey, params.Approval, timeout, func(execCtx context.Context) (sessionMutationResult, error) {
		observed, err := s.sessionMgr.Open(execCtx, op, cols, rows, term)
		result := sessionMutationResult{Observation: observed, EffectApplied: observed != nil}
		var openFailure *sessions.OpenFailure
		if errors.As(err, &openFailure) && openFailure.ChannelCreated && !openFailure.CleanupComplete {
			result.EffectApplied = true
			result.EvidenceRefs = []string{"session-channel-cleanup-incomplete"}
		}
		return result, err
	})
	return result.Observation, rcpt, err
}
