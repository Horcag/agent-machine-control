package policy

import (
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

// EvaluationInput provides all contextual state needed for pure policy evaluation.
type EvaluationInput struct {
	Operation               domain.Operation
	Now                     time.Time
	AuditWritable           bool
	RollbackState           RollbackState
	Approval                *domain.Approval
	RollbackPolicy          RollbackPolicy
	SensitiveEvidenceScopes domain.ScopeSet
	AvailableCapabilities   domain.CapabilitySet
}

func makeEvaluationSnapshot(input EvaluationInput) EvaluationInput {
	var approvalClone *domain.Approval
	if input.Approval != nil {
		app := input.Approval.Clone()
		approvalClone = &app
	}
	return EvaluationInput{
		Operation:               input.Operation.Clone(),
		Now:                     input.Now,
		AuditWritable:           input.AuditWritable,
		RollbackState:           input.RollbackState,
		Approval:                approvalClone,
		RollbackPolicy:          input.RollbackPolicy,
		SensitiveEvidenceScopes: input.SensitiveEvidenceScopes.Clone(),
		AvailableCapabilities:   input.AvailableCapabilities.Clone(),
	}
}

// Evaluate performs a pure, deterministic policy evaluation on an operation.
func Evaluate(input EvaluationInput) Decision {
	snapshot := makeEvaluationSnapshot(input)

	// 0. Explicit evaluation timestamp is required.
	if snapshot.Now.IsZero() {
		return Decision{
			Type:           DecisionDeny,
			EffectiveClass: snapshot.Operation.Classification,
			DenialReason:   DenialInvalidOperation,
			DenialMessage:  "missing or zero evaluation timestamp (Now)",
		}
	}

	// 1. Unconditionally forbidden operations.
	if snapshot.Operation.Classification == domain.ClassForbidden {
		return Decision{
			Type:           DecisionDeny,
			EffectiveClass: domain.ClassForbidden,
			DenialReason:   DenialForbidden,
			DenialMessage:  "operation is unconditionally forbidden by policy",
		}
	}

	// 2. Admission deadline, actor context, and structural validation.
	fp, dec, ok := validateOperationAndActor(snapshot)
	if !ok {
		if dec.EffectiveClass == "" {
			dec.EffectiveClass = snapshot.Operation.Classification
		}
		return dec
	}

	// 3. Backend capability enforcement.
	if dec, ok := checkCapabilityRequirements(snapshot); !ok {
		if dec.EffectiveClass == "" {
			dec.EffectiveClass = snapshot.Operation.Classification
		}
		return dec
	}

	// 4. Authorization scope and sensitive-evidence scope validation.
	if dec, ok := checkScopeRequirements(snapshot); !ok {
		if dec.EffectiveClass == "" {
			dec.EffectiveClass = snapshot.Operation.Classification
		}
		return dec
	}

	// 5. Observe operations require no mutation checks or approvals.
	if snapshot.Operation.Classification == domain.ClassObserve {
		return Decision{
			Type:                 DecisionAllow,
			EffectiveClass:       domain.ClassObserve,
			OperationFingerprint: fp,
		}
	}

	// 6. Mutation prerequisites (audit state).
	if dec, ok := evaluateMutationPrerequisites(snapshot); !ok {
		if dec.EffectiveClass == "" {
			dec.EffectiveClass = snapshot.Operation.Classification
		}
		return dec
	}

	// 7. Classification evaluation (reversible mutation vs destructive).
	effectiveClass, reclassified, dec, done := evaluateReversibleMutation(snapshot, fp)
	if done {
		return dec
	}

	// 8. Destructive / privileged approval validation.
	return evaluateDestructiveApproval(snapshot, effectiveClass, reclassified, fp)
}
