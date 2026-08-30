package app

import (
	"context"
	"fmt"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func buildOpenOperation(params SessionOpenParams, now time.Time, timeout time.Duration) (domain.Operation, uint16, uint16, string) {
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
		Deadline:            now.Add(timeout),
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
	timeout := params.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	op, cols, rows, term := buildOpenOperation(params, s.now(), timeout)
	flightKey := fmt.Sprintf("%s:%s:%s", params.Caller.EffectiveActor, params.Target, params.IdempotencyKey)

	_, obs, rcpt, err := s.coordinateSessionMutation(ctx, op, flightKey, params.Approval, timeout, func(execCtx context.Context) (int, *domain.SessionObservation, int, error) {
		observed, err := s.sessionMgr.Open(execCtx, op, cols, rows, term)
		return 0, observed, 0, err
	})
	return obs, rcpt, err
}
