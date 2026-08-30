package approval

import (
	"errors"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

var (
	// ErrApprovalNotIssued indicates the server has no matching immutable issuance record.
	ErrApprovalNotIssued = errors.New("approval: approval was not issued by this server")

	// ErrSymlinkNotAllowed indicates an approval file is a symlink.
	ErrSymlinkNotAllowed = errors.New("approval: approval file cannot be a symbolic link")

	// ErrFileTooLarge indicates an approval file exceeds the maximum allowed size (64 KB).
	ErrFileTooLarge = errors.New("approval: approval file exceeds maximum size limit (64 KB)")

	// ErrInsecurePermissions indicates an approval file has insecure (world-writable) permissions.
	ErrInsecurePermissions = errors.New("approval: approval file has insecure permissions (world-writable)")

	// ErrTrailingData indicates trailing unparsed JSON content after the approval object.
	ErrTrailingData = errors.New("approval: trailing data in approval file")
)

// DTO represents the structured JSON representation of domain.Approval.
type DTO struct {
	ID              string                `json:"id"`
	Actor           string                `json:"actor"`
	Target          string                `json:"target"`
	AuthorizedClass domain.OperationClass `json:"authorized_class"`
	Fingerprint     string                `json:"fingerprint"`
	IdempotencyKey  string                `json:"idempotency_key"`
	IssuedAt        string                `json:"issued_at"`
	ExpiresAt       string                `json:"expires_at"`
	Consumed        bool                  `json:"consumed,omitempty"`
	ConsumedAt      *string               `json:"consumed_at,omitempty"`
}

// ConvertToDTO converts a domain.Approval to a DTO.
func ConvertToDTO(a domain.Approval) DTO {
	dto := DTO{
		ID:              string(a.ID),
		Actor:           string(a.Actor),
		Target:          string(a.Target),
		AuthorizedClass: a.AuthorizedClass,
		Fingerprint:     string(a.Fingerprint),
		IdempotencyKey:  a.IdempotencyKey,
		IssuedAt:        a.IssuedAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt:       a.ExpiresAt.UTC().Format(time.RFC3339Nano),
		Consumed:        a.Consumed,
	}
	if a.ConsumedAt != nil {
		s := a.ConsumedAt.UTC().Format(time.RFC3339Nano)
		dto.ConsumedAt = &s
	}
	return dto
}

// ConvertFromDTO converts a DTO to a domain.Approval.
func ConvertFromDTO(dto DTO) (domain.Approval, error) {
	issuedAt, err := time.Parse(time.RFC3339Nano, dto.IssuedAt)
	if err != nil {
		issuedAt, err = time.Parse(time.RFC3339, dto.IssuedAt)
		if err != nil {
			return domain.Approval{}, domain.ErrInvalidApprovalRecord
		}
	}

	expiresAt, err := time.Parse(time.RFC3339Nano, dto.ExpiresAt)
	if err != nil {
		expiresAt, err = time.Parse(time.RFC3339, dto.ExpiresAt)
		if err != nil {
			return domain.Approval{}, domain.ErrInvalidApprovalRecord
		}
	}

	var consumedAt *time.Time
	if dto.ConsumedAt != nil {
		t, err := time.Parse(time.RFC3339Nano, *dto.ConsumedAt)
		if err != nil {
			t, err = time.Parse(time.RFC3339, *dto.ConsumedAt)
			if err != nil {
				return domain.Approval{}, domain.ErrInvalidApprovalRecord
			}
		}
		consumedAt = &t
	}

	return domain.Approval{
		ID:              domain.ApprovalID(dto.ID),
		Actor:           domain.ActorID(dto.Actor),
		Target:          domain.MachineRef(dto.Target),
		AuthorizedClass: dto.AuthorizedClass,
		Fingerprint:     domain.Fingerprint(dto.Fingerprint),
		IdempotencyKey:  dto.IdempotencyKey,
		IssuedAt:        issuedAt,
		ExpiresAt:       expiresAt,
		Consumed:        dto.Consumed,
		ConsumedAt:      consumedAt,
	}, nil
}
