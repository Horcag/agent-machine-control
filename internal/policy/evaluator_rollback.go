package policy

import (
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func evaluateReversibleMutation(input EvaluationInput, fp domain.Fingerprint) (domain.OperationClass, bool, Decision, bool) {
	if input.Operation.Classification != domain.ClassReversibleMutation {
		return input.Operation.Classification, false, Decision{}, false
	}

	// Validate rollback policy
	rbPolicy := input.RollbackPolicy
	if rbPolicy == "" {
		rbPolicy = RollbackPolicyEscalateToDestructive
	}
	if rbPolicy != RollbackPolicyEscalateToDestructive && rbPolicy != RollbackPolicyDeny {
		return domain.ClassReversibleMutation, false, Decision{
			Type:           DecisionDeny,
			EffectiveClass: domain.ClassReversibleMutation,
			DenialReason:   DenialInvalidOperation,
			DenialMessage:  "unrecognized rollback policy",
		}, true
	}

	// Fail closed on incoherent rollback state combinations
	// Incoherent 1: Verified without Available
	if input.RollbackState.Verified && !input.RollbackState.Available {
		return domain.ClassReversibleMutation, false, Decision{
			Type:           DecisionDeny,
			EffectiveClass: domain.ClassReversibleMutation,
			DenialReason:   DenialRollbackMissing,
			DenialMessage:  "incoherent rollback state: verified without available",
		}, true
	}

	// Incoherent 2: Checkpoint ID present when Available is false
	if !input.RollbackState.Available && input.RollbackState.CheckpointID != "" {
		return domain.ClassReversibleMutation, false, Decision{
			Type:           DecisionDeny,
			EffectiveClass: domain.ClassReversibleMutation,
			DenialReason:   DenialRollbackMissing,
			DenialMessage:  "incoherent rollback state: checkpoint ID present when unavailable",
		}, true
	}

	// Verified and Available requires a validated non-empty checkpoint reference
	if input.RollbackState.Available && input.RollbackState.Verified {
		if err := domain.ValidateRollbackRef(input.RollbackState.CheckpointID); err != nil {
			return domain.ClassReversibleMutation, false, Decision{
				Type:           DecisionDeny,
				EffectiveClass: domain.ClassReversibleMutation,
				DenialReason:   DenialRollbackMissing,
				DenialMessage:  "verified rollback requires a valid non-empty checkpoint reference",
			}, true
		}

		return domain.ClassReversibleMutation, false, Decision{
			Type:                 DecisionAllow,
			EffectiveClass:       domain.ClassReversibleMutation,
			OperationFingerprint: fp,
			RollbackCheckpointID: input.RollbackState.CheckpointID,
		}, true
	}

	// Rollback unavailable or unverified
	if rbPolicy == RollbackPolicyDeny {
		return domain.ClassReversibleMutation, false, Decision{
			Type:           DecisionDeny,
			EffectiveClass: domain.ClassReversibleMutation,
			DenialReason:   DenialRollbackMissing,
			DenialMessage:  "reversible mutation denied due to missing or unverified rollback point",
		}, true
	}

	// Escalate to destructive
	return domain.ClassDestructivePrivileged, true, Decision{}, false
}
