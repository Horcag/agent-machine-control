package app

import (
	"context"
	"errors"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

const MaxConsoleLabGrantValidity = 8 * time.Hour

const ConsoleLabApprovalPrefix = "app-lab-operation-"

var ErrInvalidConsoleLabGrant = errors.New("app: console lab grant is unavailable or invalid")

// ConsoleLabEnrollmentIdentity proves the current protected enrollment epoch.
// It must change when an enrollment is cleared or replaced, even for the same VM.
type ConsoleLabEnrollmentIdentity func(context.Context, domain.MachineRef) (string, error)

type ConsoleOption func(*ConsoleService)

func WithConsoleLabSafetyResolver(resolver SafetyResolver) ConsoleOption {
	return func(s *ConsoleService) { s.labSafety = resolver }
}

func WithConsoleLabEnrollmentIdentity(identity ConsoleLabEnrollmentIdentity) ConsoleOption {
	return func(s *ConsoleService) { s.labEnrollment = identity }
}

// ConsoleLabGrantIssueRequest requests only guest console authority.
type ConsoleLabGrantIssueRequest struct {
	AcknowledgeExternalEffects bool   `json:"acknowledge_external_effects"`
	Target                     string `json:"target"`
	Reason                     string `json:"reason"`
	IdempotencyKey             string `json:"idempotency_key"`
	Beneficiary                string `json:"beneficiary"`
	ValidForMillis             int64  `json:"valid_for_millis"`
}

// ConsoleLabGrant is immutable server-owned authority, never a bearer credential.
type ConsoleLabGrant struct {
	AcknowledgeExternalEffects bool              `json:"acknowledge_external_effects"`
	SchemaVersion              int               `json:"schema_version"`
	GrantID                    string            `json:"grant_id"`
	Target                     domain.MachineRef `json:"target"`
	EnrollmentIdentity         string            `json:"enrollment_identity"`
	Issuer                     domain.ActorID    `json:"issuer"`
	Beneficiary                domain.ActorID    `json:"beneficiary"`
	Reason                     string            `json:"reason"`
	IdempotencyKey             string            `json:"idempotency_key"`
	IssuedAt                   time.Time         `json:"issued_at"`
	ExpiresAt                  time.Time         `json:"expires_at"`
}

type ConsoleLabGrantStatus struct {
	Grant ConsoleLabGrant `json:"grant"`
	State string          `json:"state"`
}

type ConsoleLabGrantRevokeRequest struct {
	GrantID        string `json:"grant_id"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotency_key"`
	Deadline       string `json:"deadline"`
}

type consoleLabRevocation struct {
	GrantID        string         `json:"grant_id"`
	Actor          domain.ActorID `json:"actor"`
	Reason         string         `json:"reason"`
	IdempotencyKey string         `json:"idempotency_key"`
	Deadline       time.Time      `json:"deadline"`
	RevokedAt      time.Time      `json:"revoked_at"`
}
