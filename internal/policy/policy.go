package policy

// RollbackPolicy defines how policy treats reversible mutations when no verified rollback point exists.
type RollbackPolicy string

const (
	// RollbackPolicyEscalateToDestructive reclassifies reversible mutation as destructive and requires operator approval.
	RollbackPolicyEscalateToDestructive RollbackPolicy = "escalate_to_destructive"

	// RollbackPolicyDeny denies execution immediately if a verified rollback point is absent.
	RollbackPolicyDeny RollbackPolicy = "deny"
)

// DefaultSensitiveEvidenceScope is the default scope required for console/framebuffer capture.
const DefaultSensitiveEvidenceScope = "evidence:sensitive:capture"

// RollbackState represents the live status of the rollback checkpoint for the target VM.
type RollbackState struct {
	Available    bool
	Verified     bool
	CheckpointID string
}
