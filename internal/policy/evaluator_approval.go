package policy

import (
	"errors"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func evaluateDestructiveApproval(input EvaluationInput, effectiveClass domain.OperationClass, reclassified bool, fp domain.Fingerprint) Decision {
	if input.Approval == nil {
		return Decision{
			Type:           DecisionDeny,
			EffectiveClass: effectiveClass,
			Reclassified:   reclassified,
			DenialReason:   DenialApprovalRequired,
			DenialMessage:  "destructive/privileged operation requires active operator approval",
		}
	}

	if err := input.Approval.Validate(); err != nil {
		return Decision{
			Type:           DecisionDeny,
			EffectiveClass: effectiveClass,
			Reclassified:   reclassified,
			DenialReason:   DenialApprovalMismatch,
			DenialMessage:  "approval record is malformed",
		}
	}

	if err := input.Approval.MatchesEffectiveClass(effectiveClass); err != nil {
		return Decision{
			Type:           DecisionDeny,
			EffectiveClass: effectiveClass,
			Reclassified:   reclassified,
			DenialReason:   DenialApprovalMismatch,
			DenialMessage:  "approval record does not authorize the effective operation class",
		}
	}

	if err := input.Approval.Matches(input.Operation); err != nil {
		return Decision{
			Type:           DecisionDeny,
			EffectiveClass: effectiveClass,
			Reclassified:   reclassified,
			DenialReason:   DenialApprovalMismatch,
			DenialMessage:  "approval record does not match operation target or parameters",
		}
	}

	if err := input.Approval.IsActive(input.Now); err != nil {
		var reason DenialReason
		switch {
		case errors.Is(err, domain.ErrApprovalNotYetValid):
			reason = DenialApprovalNotYetValid
		case errors.Is(err, domain.ErrApprovalExpired):
			reason = DenialApprovalExpired
		case errors.Is(err, domain.ErrApprovalConsumed):
			reason = DenialApprovalConsumed
		default:
			reason = DenialApprovalMismatch
		}
		return Decision{
			Type:           DecisionDeny,
			EffectiveClass: effectiveClass,
			Reclassified:   reclassified,
			DenialReason:   reason,
			DenialMessage:  "approval record is inactive or already consumed",
		}
	}

	return Decision{
		Type:                 DecisionAllow,
		EffectiveClass:       effectiveClass,
		Reclassified:         reclassified,
		OperationFingerprint: fp,
	}
}
