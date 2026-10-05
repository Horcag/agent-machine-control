package app

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
)

const recordingStatusTTL = 15 * time.Minute
const recordingStatusCapacity = 64

var ErrRecordingStatusInconclusive = errors.New("app: recording status inconclusive")
var ErrRecordingIDUnavailable = errors.New("app: recording ID unavailable")

type ConsoleRecordStatusRequest struct {
	Target      string `json:"target"`
	RecordingID string `json:"recording_id"`
}

// ConsoleRecordStatus contains capture lifecycle metadata, never guest content.
// Nonterminal snapshots cannot establish that a producer has stopped.
type ConsoleRecordStatus struct {
	SchemaVersion     string    `json:"schema_version"`
	RecordingID       string    `json:"recording_id"`
	VMID              string    `json:"vm_id"`
	RequestedFrames   int       `json:"requested_frames"`
	AttemptedCaptures int       `json:"attempted_captures"`
	CompletedCaptures int       `json:"completed_captures"`
	CaptureInFlight   bool      `json:"capture_in_flight"`
	Terminal          bool      `json:"terminal"`
	TerminalReason    string    `json:"terminal_reason"`
	StartedAt         time.Time `json:"started_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	ExpiresAt         time.Time `json:"expires_at"`
}

type recordingStatusDocument struct {
	SchemaVersion int                 `json:"schema_version"`
	Caller        domain.ActorID      `json:"caller"`
	Actor         domain.ActorID      `json:"actor"`
	Sequence      int                 `json:"sequence"`
	Status        ConsoleRecordStatus `json:"status"`
}

type recordingProgress struct {
	service  *ConsoleService
	document recordingStatusDocument
}

func (s *ConsoleService) recordingDirectory() string {
	return filepath.Join(filepath.Dir(s.framesDir), "console-recordings")
}

var recordingIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func validRecordingID(id string) bool { return recordingIDPattern.MatchString(id) }

func (s *ConsoleService) recordingPath(id string) (string, error) {
	// Validate the component at the filesystem boundary, before joining or access.
	if !recordingIDPattern.MatchString(id) {
		return "", ErrRecordingStatusInconclusive
	}
	return filepath.Join(s.recordingDirectory(), id), nil
}

func (s *ConsoleService) RecordStatus(ctx context.Context, actor domain.ActorContext, req ConsoleRecordStatusRequest) (ConsoleRecordStatus, error) {
	if !validRecordingID(req.RecordingID) {
		return ConsoleRecordStatus{}, ErrRecordingStatusInconclusive
	}
	locator, err := s.recordingTarget(ctx, actor, req.Target)
	if err != nil {
		return ConsoleRecordStatus{}, err
	}
	doc, err := s.readRecordingStatus(ctx, req.RecordingID)
	if ctx.Err() != nil {
		return ConsoleRecordStatus{}, ctx.Err()
	}
	if err != nil || doc.Caller != actor.AuthenticatedCaller || doc.Actor != actor.EffectiveActor || doc.Status.VMID != locator.String() || (!s.recovery.now().Before(doc.Status.ExpiresAt) || doc.Status.UpdatedAt.After(s.recovery.now())) {
		return ConsoleRecordStatus{}, ErrRecordingStatusInconclusive
	}
	return doc.Status, nil
}

func (s *ConsoleService) beginRecording(ctx context.Context, actor domain.ActorContext, req *ConsoleRecordRequest) (*recordingProgress, error) {
	if !validRecordingID(req.RecordingID) {
		return nil, ErrRecordingIDUnavailable
	}
	locator, err := s.recordingTarget(ctx, actor, req.Target)
	if err != nil {
		return nil, err
	}
	req.Target = locator.String()
	now := s.recovery.now()
	p := &recordingProgress{service: s, document: recordingStatusDocument{SchemaVersion: 1, Caller: actor.AuthenticatedCaller, Actor: actor.EffectiveActor, Status: ConsoleRecordStatus{SchemaVersion: "1", RecordingID: req.RecordingID, VMID: req.Target, RequestedFrames: req.Frames, StartedAt: now, UpdatedAt: now, ExpiresAt: now.Add(recordingStatusTTL)}}}
	if err := s.reserveRecording(ctx, p.document); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *recordingProgress) persist() error {
	if p == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.document.Status.UpdatedAt = p.service.recovery.now()
	p.document.Sequence++
	if err := p.service.writeRecordingStatus(ctx, p.document); err != nil {
		return ErrRecordingStatusInconclusive
	}
	return nil
}

func (p *recordingProgress) beforeCapture(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.document.Status.AttemptedCaptures++
	p.document.Status.CaptureInFlight = true
	if err := p.persist(); err != nil {
		p.document.Status.AttemptedCaptures--
		p.document.Status.CaptureInFlight = false
		return err
	}
	return nil
}

func (p *recordingProgress) afterCapture(valid bool) error {
	if p == nil {
		return nil
	}
	p.document.Status.CaptureInFlight = false
	if valid {
		p.document.Status.CompletedCaptures++
	}
	return p.persist()
}

func (p *recordingProgress) finish(err error) error {
	if p == nil {
		return nil
	}
	p.document.Status.Terminal = true
	switch {
	case err == nil:
		p.document.Status.TerminalReason = "completed"
	case errors.Is(err, context.Canceled):
		p.document.Status.TerminalReason = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		p.document.Status.TerminalReason = "deadline_exceeded"
	default:
		p.document.Status.TerminalReason = "failed"
	}
	return p.persist()
}

// Metadata admission uses protected enrollment authority without observing the VM.
// Actual frame admission continues to prove fresh provider identity and capability.
func (s *ConsoleService) recordingTarget(ctx context.Context, actor domain.ActorContext, reference string) (domain.MachineLocator, error) {
	if err := actor.Validate(); err != nil {
		return domain.MachineLocator{}, err
	}
	if !actor.HasScope(domain.ScopeMachineRead) || !actor.HasScope(domain.ScopeEvidenceCapture) {
		return domain.MachineLocator{}, &PolicyDeniedError{Reason: policy.DenialMissingScope, Message: "recording status requires machine:read and sensitive evidence authority"}
	}
	if err := s.validateDependencies(); err != nil {
		return domain.MachineLocator{}, err
	}
	enrolled, err := s.target.store.Load(ctx)
	if err != nil {
		return domain.MachineLocator{}, err
	}
	if err := validateTargetReference(reference, enrolled); err != nil {
		return domain.MachineLocator{}, err
	}
	if _, err := s.target.targetHost(ctx, enrolled.Locator); err != nil {
		return domain.MachineLocator{}, err
	}
	return enrolled.Locator, nil
}
