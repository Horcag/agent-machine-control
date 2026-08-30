package sessions

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

// Session represents an active or durable persistent SSH terminal session.
type Session struct {
	mu           sync.RWMutex
	persistMu    sync.Mutex
	writeSem     chan struct{}
	closeSem     chan struct{}
	obs          domain.SessionObservation
	channel      guestssh.Channel
	buffer       *RingBuffer
	sanitizer    *guestssh.StreamSanitizer
	closed       bool
	terminalErr  error
	closedCh     chan struct{}
	closeOnce    sync.Once
	openParamsFp string
}

// Manager orchestrates lifecycle, state transitions, and concurrency for terminal sessions.
type Manager struct {
	mu              sync.RWMutex
	sessionsDir     string
	transport       guestssh.Transport
	clock           func() time.Time
	sessions        map[domain.SessionID]*Session
	idempotency     map[string]domain.SessionID // key: actor:target:idempotencyKey -> SessionID
	sanitizerConfig guestssh.SanitizerConfig
	closed          bool
}

// ManagerOption configures a session manager.
type ManagerOption func(*Manager)

// WithSanitizerConfig installs server-owned exact-secret and configured-pattern matchers.
func WithSanitizerConfig(config guestssh.SanitizerConfig) ManagerOption {
	return func(m *Manager) { m.sanitizerConfig = config }
}

// NewManager constructs a SessionManager.
func NewManager(sessionsDir string, transport guestssh.Transport, clock func() time.Time, opts ...ManagerOption) *Manager {
	if clock == nil {
		clock = time.Now
	}
	m := &Manager{
		sessionsDir: sessionsDir,
		transport:   transport,
		clock:       clock,
		sessions:    make(map[domain.SessionID]*Session),
		idempotency: make(map[string]domain.SessionID),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func (m *Manager) now() time.Time {
	return m.clock().UTC()
}

// MutationJournal returns the durable journal owned by this session manager.
func (m *Manager) MutationJournal() *MutationJournal {
	if m == nil || m.sessionsDir == "" {
		return nil
	}
	return NewMutationJournal(filepath.Join(m.sessionsDir, "mutations"))
}

func (m *Manager) authorize(caller domain.ActorContext, s *Session) bool {
	if caller.HasScope(domain.ScopeSessionAdmin) {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return string(caller.EffectiveActor) == string(s.obs.OwnerActor)
}

func (m *Manager) dialSessionChannel(ctx context.Context, op domain.Operation, cols, rows uint16, term string) (guestssh.Channel, error) {
	if m.transport == nil {
		return nil, errors.New("sessions: transport is unconfigured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	channel, err := m.transport.Dial(ctx, op.Target, cols, rows, term)
	if err != nil {
		return nil, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), sessionCleanupTimeout)
		defer cancel()
		_, _ = closeChannel(cleanupCtx, channel)
		return nil, ctxErr
	}
	return channel, nil
}

func (m *Manager) findIdempotentOpen(idemKey, paramsFp string) (*domain.SessionObservation, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, false, domain.ErrSessionManagerClosed
	}
	existingID, exists := m.idempotency[idemKey]
	if !exists {
		return nil, false, nil
	}
	existing, exists := m.sessions[existingID]
	if !exists {
		return nil, false, nil
	}
	if existing.openParamsFp != paramsFp {
		return nil, false, domain.ErrSessionConflict
	}
	existing.mu.RLock()
	obs := existing.obs
	existing.mu.RUnlock()
	return &obs, true, nil
}

func (m *Manager) publishOpenSession(session *Session, idemKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return domain.ErrSessionManagerClosed
	}
	if err := m.persistSession(session); err != nil {
		return err
	}
	m.sessions[session.obs.ID] = session
	if idemKey != "" {
		m.idempotency[idemKey] = session.obs.ID
	}
	return nil
}

// Open establishes a new persistent SSH terminal session or returns an existing idempotent session.
func (m *Manager) Open(ctx context.Context, op domain.Operation, cols, rows uint16, term string) (*domain.SessionObservation, error) {
	if !op.Actor.HasScope(domain.ScopeSessionOpen) && !op.Actor.HasScope(domain.ScopeSessionWrite) {
		return nil, domain.ErrSessionAccessDenied
	}
	if cols == 0 {
		cols = domain.DefaultCols
	}
	if rows == 0 {
		rows = domain.DefaultRows
	}
	if term == "" {
		term = domain.DefaultTermType
	}
	if err := domain.ValidateTerminalDimensions(cols, rows); err != nil {
		return nil, err
	}
	if err := domain.ValidateTerminalType(term); err != nil {
		return nil, err
	}

	paramsFp := fmt.Sprintf("%d:%d:%s", cols, rows, term)
	idemKey := fmt.Sprintf("%s:%s:%s", op.Actor.EffectiveActor, op.Target, op.IdempotencyKey)

	if existing, found, err := m.findIdempotentOpen(idemKey, paramsFp); err != nil || found {
		return existing, err
	}

	channel, err := m.dialSessionChannel(ctx, op, cols, rows, term)
	if err != nil {
		return nil, err
	}

	sessID, err := domain.GenerateSessionID()
	if err != nil {
		_ = channel.Close(context.Background())
		return nil, err
	}

	now := m.now()
	obs := domain.SessionObservation{
		ID:              sessID,
		Target:          op.Target,
		OwnerActor:      op.Actor.EffectiveActor,
		State:           domain.SessionStateActive,
		CreatedAt:       now,
		LastActivityAt:  now,
		Cols:            cols,
		Rows:            rows,
		TermType:        term,
		ObservationType: domain.ObservationObserved,
	}

	session := &Session{
		obs:          obs,
		channel:      channel,
		writeSem:     make(chan struct{}, 1),
		closeSem:     make(chan struct{}, 1),
		buffer:       NewRingBuffer(DefaultRingBufferCapBytes),
		sanitizer:    guestssh.NewStreamSanitizer(m.sanitizerConfig),
		closedCh:     make(chan struct{}),
		openParamsFp: paramsFp,
	}

	publishKey := ""
	if op.IdempotencyKey != "" {
		publishKey = idemKey
	}
	if err := m.publishOpenSession(session, publishKey); err != nil {
		_ = channel.Close(context.Background())
		return nil, err
	}

	// Start reader goroutine
	go m.pumpChannelOutput(session)

	// Start exit waiter
	go m.waitChannelExit(context.WithoutCancel(ctx), session)

	return &obs, nil
}

func (m *Manager) pumpChannelOutput(s *Session) {
	buf := make([]byte, 4096)
	for {
		n, err := s.channel.Read(buf)
		if n > 0 {
			raw := buf[:n]
			clean := s.sanitizer.Push(raw)
			s.mu.Lock()
			s.obs.BytesRead += uint64(n)
			s.obs.LastActivityAt = m.now()
			s.mu.Unlock()

			if len(clean) > 0 {
				s.buffer.Append(clean, m.now())
			}
		}
		if err != nil {
			if tail := s.sanitizer.Flush(); tail != "" {
				s.buffer.Append(tail, m.now())
			}
			break
		}
	}
}

func (m *Manager) waitChannelExit(parent context.Context, s *Session) {
	exitCode, waitErr := s.channel.Wait()
	ctx, cancel := context.WithTimeout(parent, sessionCleanupTimeout)
	defer cancel()
	if err := acquireSessionCloseLane(ctx, s); err != nil {
		return
	}
	defer releaseSessionCloseLane(s)
	_, _ = m.finalizeSession(ctx, s, finalizationNaturalExit, &exitCode, waitErr, false)
}

// Read returns buffered chunks from a session starting after sequence cursor afterSeq.
func (m *Manager) Read(_ context.Context, id domain.SessionID, caller domain.ActorContext, afterSeq uint64, maxBytes int) ([]domain.SessionChunk, uint64, uint64, bool, *domain.SessionObservation, error) {
	if !caller.HasScope(domain.ScopeSessionRead) {
		return nil, 0, 0, false, nil, domain.ErrSessionAccessDenied
	}
	if err := domain.ValidateSessionID(string(id)); err != nil {
		return nil, 0, 0, false, nil, err
	}

	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok || !m.authorize(caller, s) {
		return nil, 0, 0, false, nil, domain.ErrSessionNotFound
	}

	s.mu.Lock()
	s.obs.LastActivityAt = m.now()
	obs := s.obs
	s.mu.Unlock()

	chunks, nextSeq, lossBytes, hasMore := s.buffer.ReadAfter(afterSeq, maxBytes)
	return chunks, nextSeq, lossBytes, hasMore, &obs, nil
}

// Write sends UTF-8 character data to the session stdin.
func (m *Manager) Write(ctx context.Context, id domain.SessionID, caller domain.ActorContext, data string, _, _ string) (int, error) {
	if !caller.HasScope(domain.ScopeSessionWrite) {
		return 0, domain.ErrSessionAccessDenied
	}
	if err := domain.ValidateSessionID(string(id)); err != nil {
		return 0, err
	}
	if len(data) > domain.MaxSessionWriteBytes {
		return 0, fmt.Errorf("%w: write exceeds maximum limit", domain.ErrNonCanonicalParameter)
	}

	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok || !m.authorize(caller, s) {
		return 0, domain.ErrSessionNotFound
	}

	s.mu.RLock()
	state := s.obs.State
	s.mu.RUnlock()
	if state.IsTerminal() {
		return 0, domain.ErrSessionClosed
	}

	if err := acquireSessionLane(ctx, s.writeSem); err != nil {
		return 0, err
	}
	defer func() { <-s.writeSem }()

	n, writeErr := s.channel.Write(ctx, []byte(data))
	s.mu.Lock()
	if n > 0 {
		s.obs.BytesWritten += uint64(n)
	}
	s.obs.LastActivityAt = m.now()
	s.mu.Unlock()

	if pErr := m.persistSession(s); pErr != nil {
		return n, errors.Join(writeErr, pErr)
	}
	return n, writeErr
}

// Control sends a whitelisted terminal control key or escape sequence.
func (m *Manager) Control(ctx context.Context, id domain.SessionID, caller domain.ActorContext, key domain.ControlKey, _, _ string) (int, error) {
	if !caller.HasScope(domain.ScopeSessionWrite) {
		return 0, domain.ErrSessionAccessDenied
	}
	if err := domain.ValidateSessionID(string(id)); err != nil {
		return 0, err
	}

	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok || !m.authorize(caller, s) {
		return 0, domain.ErrSessionNotFound
	}

	s.mu.RLock()
	state := s.obs.State
	s.mu.RUnlock()
	if state.IsTerminal() {
		return 0, domain.ErrSessionClosed
	}

	if err := acquireSessionLane(ctx, s.writeSem); err != nil {
		return 0, err
	}
	defer func() { <-s.writeSem }()

	n, controlErr := s.channel.SendControl(ctx, key)
	if n == 0 {
		return 0, controlErr
	}
	s.mu.Lock()
	s.obs.LastActivityAt = m.now()
	s.mu.Unlock()
	return n, errors.Join(controlErr, m.persistSession(s))
}

// Close terminates the session and returns its final observation.
func (m *Manager) Close(ctx context.Context, id domain.SessionID, caller domain.ActorContext, _ string, force bool) (*domain.SessionObservation, error) {
	if !caller.HasScope(domain.ScopeSessionClose) && !caller.HasScope(domain.ScopeSessionWrite) {
		return nil, domain.ErrSessionAccessDenied
	}
	if err := domain.ValidateSessionID(string(id)); err != nil {
		return nil, err
	}

	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok || !m.authorize(caller, s) {
		return nil, domain.ErrSessionNotFound
	}
	if err := acquireSessionCloseLane(ctx, s); err != nil {
		return nil, err
	}
	defer releaseSessionCloseLane(s)
	return m.finalizeSession(ctx, s, finalizationExplicitClose, nil, nil, force)
}

// Shutdown cleanly terminates all active sessions.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()

	var errs []error
	for _, s := range sessions {
		if err := acquireSessionCloseLane(ctx, s); err != nil {
			errs = append(errs, ctx.Err())
			continue
		}
		_, closeErr := m.finalizeSession(ctx, s, finalizationShutdown, nil, nil, true)
		if closeErr != nil {
			errs = append(errs, closeErr)
		}
		releaseSessionCloseLane(s)
	}

	return errors.Join(errs...)
}
