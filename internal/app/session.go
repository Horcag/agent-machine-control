package app

import (
	"context"
	"sync"
	"time"

	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/policy"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
)

// SessionService coordinates session mutation admissions, policy, idempotency, audit, and receipts.
type SessionService struct {
	sessionMgr      *sessions.Manager
	safetyResolver  SafetyResolver
	leaseMgr        *lease.Manager
	auditStore      *audit.Store
	receiptStore    *receipt.Store
	approvalStore   *approval.Store
	mutationJournal *sessions.MutationJournal
	nowFn           func() time.Time

	mu       sync.Mutex
	inFlight map[string]*inFlightSessionCall
}

type inFlightSessionCall struct {
	done chan struct{}
	err  error
	rcpt domain.Receipt
	obs  *domain.SessionObservation
	n    int
	idFp domain.Fingerprint
}

// SessionOption configures SessionService.
type SessionOption func(*SessionService)

// WithSessionClock sets a custom clock function for SessionService.
func WithSessionClock(clock func() time.Time) SessionOption {
	return func(s *SessionService) {
		s.nowFn = clock
	}
}

// WithSessionMutationJournal overrides the durable mutation journal, primarily for failure injection.
func WithSessionMutationJournal(journal *sessions.MutationJournal) SessionOption {
	return func(s *SessionService) {
		s.mutationJournal = journal
	}
}

// SessionOpenParams contains parameters for opening a session.
type SessionOpenParams struct {
	Target         string
	Caller         domain.ActorContext
	Reason         string
	IdempotencyKey string
	Timeout        time.Duration
	Cols           uint16
	Rows           uint16
	Term           string
	Approval       *domain.Approval
}

// SessionWriteParams contains parameters for writing to a session.
type SessionWriteParams struct {
	SessionID      domain.SessionID
	Caller         domain.ActorContext
	Data           string
	Reason         string
	IdempotencyKey string
	Timeout        time.Duration
	Approval       *domain.Approval
}

// SessionControlParams contains parameters for sending a control key.
type SessionControlParams struct {
	SessionID      domain.SessionID
	Caller         domain.ActorContext
	Key            domain.ControlKey
	Reason         string
	IdempotencyKey string
	Timeout        time.Duration
	Approval       *domain.Approval
}

// SessionCloseParams contains parameters for closing a session.
type SessionCloseParams struct {
	SessionID      domain.SessionID
	Caller         domain.ActorContext
	Reason         string
	IdempotencyKey string
	Timeout        time.Duration
	Force          bool
	Approval       *domain.Approval
}

// NewSessionService creates a SessionService.
func NewSessionService(
	sessionMgr *sessions.Manager,
	safetyResolver SafetyResolver,
	leaseMgr *lease.Manager,
	auditStore *audit.Store,
	receiptStore *receipt.Store,
	approvalStore *approval.Store,
	opts ...SessionOption,
) *SessionService {
	var mutationJournal *sessions.MutationJournal
	if sessionMgr != nil {
		mutationJournal = sessionMgr.MutationJournal()
	}
	s := &SessionService{
		sessionMgr:      sessionMgr,
		safetyResolver:  safetyResolver,
		leaseMgr:        leaseMgr,
		auditStore:      auditStore,
		receiptStore:    receiptStore,
		approvalStore:   approvalStore,
		mutationJournal: mutationJournal,
		nowFn:           time.Now,
		inFlight:        make(map[string]*inFlightSessionCall),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *SessionService) now() time.Time {
	if s.nowFn != nil {
		return s.nowFn().UTC()
	}
	return time.Now().UTC()
}

func (s *SessionService) beginSessionMutation(parent context.Context, requested time.Duration) (context.Context, context.CancelFunc, time.Time, time.Duration) {
	if requested <= 0 {
		requested = 30 * time.Second
	}
	budget := requested
	if callerDeadline, ok := parent.Deadline(); ok {
		if remaining := time.Until(callerDeadline); remaining < budget {
			budget = remaining
		}
	}
	ctx, cancel := context.WithTimeout(parent, budget)
	return ctx, cancel, s.now().Add(budget), budget
}

func (s *SessionService) hasSensitiveEvidenceScope(caller domain.ActorContext) bool {
	return caller.HasScope("evidence:sensitive") ||
		caller.HasScope("evidence:sensitive:capture") ||
		caller.HasScope(policy.DefaultSensitiveEvidenceScope) ||
		caller.HasScope(domain.ScopeSessionAdmin)
}

// ReadSession enforces read and sensitive-evidence scopes before retrieving chunks.
func (s *SessionService) ReadSession(ctx context.Context, id domain.SessionID, caller domain.ActorContext, afterSeq uint64, limitBytes int) ([]domain.SessionChunk, uint64, uint64, bool, *domain.SessionObservation, error) {
	if !caller.HasScope(domain.ScopeSessionRead) {
		return nil, 0, 0, false, nil, domain.ErrSessionAccessDenied
	}
	if !s.hasSensitiveEvidenceScope(caller) {
		return nil, 0, 0, false, nil, domain.ErrSessionAccessDenied
	}
	if err := domain.ValidateSessionID(string(id)); err != nil {
		return nil, 0, 0, false, nil, err
	}
	return s.sessionMgr.Read(ctx, id, caller, afterSeq, limitBytes)
}

// WaitSession enforces read and sensitive-evidence scopes before waiting for output.
func (s *SessionService) WaitSession(ctx context.Context, id domain.SessionID, caller domain.ActorContext, settle time.Duration, regexStr string, afterSeq uint64, timeout time.Duration) ([]domain.SessionChunk, uint64, uint64, bool, *domain.SessionObservation, error) {
	if !caller.HasScope(domain.ScopeSessionRead) {
		return nil, 0, 0, false, nil, domain.ErrSessionAccessDenied
	}
	if !s.hasSensitiveEvidenceScope(caller) {
		return nil, 0, 0, false, nil, domain.ErrSessionAccessDenied
	}
	if err := domain.ValidateSessionID(string(id)); err != nil {
		return nil, 0, 0, false, nil, err
	}
	return s.sessionMgr.Wait(ctx, id, caller, settle, regexStr, afterSeq, timeout)
}

// ListSessions returns accessible session observations.
func (s *SessionService) ListSessions(ctx context.Context, caller domain.ActorContext, target domain.MachineRef) ([]domain.SessionObservation, error) {
	if !caller.HasScope(domain.ScopeSessionRead) {
		return nil, domain.ErrSessionAccessDenied
	}
	return s.sessionMgr.List(ctx, caller, target)
}

// GetSession returns a single session observation.
func (s *SessionService) GetSession(ctx context.Context, id domain.SessionID, caller domain.ActorContext) (*domain.SessionObservation, error) {
	if !caller.HasScope(domain.ScopeSessionRead) {
		return nil, domain.ErrSessionAccessDenied
	}
	if err := domain.ValidateSessionID(string(id)); err != nil {
		return nil, err
	}
	return s.sessionMgr.Get(ctx, id, caller)
}
