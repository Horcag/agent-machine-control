package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

// Session represents an active or durable persistent SSH terminal session.
type Session struct {
	mu           sync.RWMutex
	writeSem     chan struct{}
	closeSem     chan struct{}
	obs          domain.SessionObservation
	channel      guestssh.Channel
	buffer       *RingBuffer
	sanitizer    *guestssh.StreamSanitizer
	closed       bool
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

func (m *Manager) persistSession(s *Session) error {
	if m.sessionsDir == "" {
		return nil
	}
	s.mu.RLock()
	obs := s.obs
	s.mu.RUnlock()

	filePath := filepath.Join(m.sessionsDir, fmt.Sprintf("%s.json", obs.ID))
	data, err := json.MarshalIndent(obs, "", "  ")
	if err != nil {
		return fmt.Errorf("sessions: failed to marshal session %s: %w", obs.ID, err)
	}
	if err := os.WriteFile(filePath, data, 0600); err != nil {
		return fmt.Errorf("sessions: failed to write session file %s: %w", filePath, err)
	}
	if err := statedir.SyncDir(m.sessionsDir); err != nil {
		return fmt.Errorf("sessions: failed to sync sessions dir: %w", err)
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

	paramsFp := fmt.Sprintf("%d:%d:%s", cols, rows, term)
	idemKey := fmt.Sprintf("%s:%s:%s", op.Actor.EffectiveActor, op.Target, op.IdempotencyKey)

	m.mu.Lock()
	if existingID, exists := m.idempotency[idemKey]; exists {
		if existingSess, ok := m.sessions[existingID]; ok {
			m.mu.Unlock()
			if existingSess.openParamsFp != paramsFp {
				return nil, domain.ErrSessionConflict
			}
			existingSess.mu.RLock()
			obs := existingSess.obs
			existingSess.mu.RUnlock()
			return &obs, nil
		}
	}
	m.mu.Unlock()

	if m.transport == nil {
		return nil, errors.New("sessions: transport is unconfigured")
	}

	channel, err := m.transport.Dial(ctx, op.Target, cols, rows, term)
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

	if err := m.persistSession(session); err != nil {
		_ = channel.Close(context.Background())
		return nil, err
	}

	m.mu.Lock()
	m.sessions[sessID] = session
	if op.IdempotencyKey != "" {
		m.idempotency[idemKey] = sessID
	}
	m.mu.Unlock()

	// Start reader goroutine
	go m.pumpChannelOutput(session)

	// Start exit waiter
	go m.waitChannelExit(session)

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

func (m *Manager) waitChannelExit(s *Session) {
	exitCode, _ := s.channel.Wait()
	now := m.now()

	s.mu.Lock()
	if !s.obs.State.IsTerminal() && s.obs.State != domain.SessionStateClosing {
		s.obs.State = domain.SessionStateClosed
		s.obs.ClosedAt = &now
		s.obs.ExitCode = &exitCode
		s.closed = true
	}
	s.mu.Unlock()

	_ = m.persistSession(s)
	s.closeOnce.Do(func() {
		close(s.closedCh)
	})
}

// Read returns buffered chunks from a session starting after sequence cursor afterSeq.
func (m *Manager) Read(_ context.Context, id domain.SessionID, caller domain.ActorContext, afterSeq uint64, maxBytes int) ([]domain.SessionChunk, uint64, uint64, bool, *domain.SessionObservation, error) {
	if !caller.HasScope(domain.ScopeSessionRead) {
		return nil, 0, 0, false, nil, domain.ErrSessionAccessDenied
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

	select {
	case s.writeSem <- struct{}{}:
		defer func() { <-s.writeSem }()
	case <-ctx.Done():
		return 0, ctx.Err()
	}

	n, err := s.channel.Write(ctx, []byte(data))
	if err != nil {
		return n, err
	}

	s.mu.Lock()
	if n > 0 {
		s.obs.BytesWritten += uint64(n)
	}
	s.obs.LastActivityAt = m.now()
	s.mu.Unlock()

	if pErr := m.persistSession(s); pErr != nil {
		return n, pErr
	}
	return n, nil
}

// Control sends a whitelisted terminal control key or escape sequence.
func (m *Manager) Control(ctx context.Context, id domain.SessionID, caller domain.ActorContext, key domain.ControlKey, _, _ string) error {
	if !caller.HasScope(domain.ScopeSessionWrite) {
		return domain.ErrSessionAccessDenied
	}

	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok || !m.authorize(caller, s) {
		return domain.ErrSessionNotFound
	}

	s.mu.RLock()
	state := s.obs.State
	s.mu.RUnlock()
	if state.IsTerminal() {
		return domain.ErrSessionClosed
	}

	select {
	case s.writeSem <- struct{}{}:
		defer func() { <-s.writeSem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	if err := s.channel.SendControl(ctx, key); err != nil {
		return err
	}

	s.mu.Lock()
	s.obs.LastActivityAt = m.now()
	s.mu.Unlock()

	return m.persistSession(s)
}

// Close terminates the session and returns its final observation.
func (m *Manager) Close(ctx context.Context, id domain.SessionID, caller domain.ActorContext, _ string, force bool) (*domain.SessionObservation, error) {
	if !caller.HasScope(domain.ScopeSessionClose) && !caller.HasScope(domain.ScopeSessionWrite) {
		return nil, domain.ErrSessionAccessDenied
	}

	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok || !m.authorize(caller, s) {
		return nil, domain.ErrSessionNotFound
	}
	select {
	case s.closeSem <- struct{}{}:
		defer func() { <-s.closeSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	s.mu.Lock()
	if s.obs.State.IsTerminal() {
		obs := s.obs
		s.mu.Unlock()
		if obs.State == domain.SessionStateFailed && obs.ErrorMessage != "" {
			return &obs, errors.New(obs.ErrorMessage)
		}
		return &obs, nil
	}
	if s.obs.State == domain.SessionStateClosing {
		obs := s.obs
		s.mu.Unlock()
		return &obs, errors.New("sessions: transport close is incomplete")
	}
	s.obs.State = domain.SessionStateClosing
	s.mu.Unlock()

	closeErr := s.channel.Close(ctx)
	now := m.now()
	s.mu.Lock()
	if closeErr != nil {
		s.obs.ErrorMessage = "transport_close_failed"
		if force {
			s.obs.State = domain.SessionStateFailed
			s.obs.ClosedAt = &now
			s.closed = true
		}
	} else {
		s.obs.State = domain.SessionStateClosed
		s.obs.ClosedAt = &now
		s.closed = true
	}
	obs := s.obs
	s.mu.Unlock()

	if err := m.persistSession(s); err != nil {
		return &obs, errors.Join(closeErr, err)
	}
	if closeErr != nil {
		return &obs, closeErr
	}

	return &obs, nil
}

// Shutdown cleanly terminates all active sessions.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()

	var errs []error
	for _, s := range sessions {
		s.mu.Lock()
		if !s.obs.State.IsTerminal() {
			s.obs.State = domain.SessionStateClosed
			now := m.now()
			s.obs.ClosedAt = &now
			s.closed = true
		}
		s.mu.Unlock()
		if err := s.channel.Close(ctx); err != nil && !errors.Is(err, io.EOF) {
			errs = append(errs, err)
		}
		if err := m.persistSession(s); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
